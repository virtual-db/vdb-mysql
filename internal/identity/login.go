// Package identity is the login relay (implementation plan doc 11 §2,
// product doc 11 §2): the Source decides every login. One Login per login
// connection runs the state machine start → challenged → (need_password) →
// done, and any state → closed on abort. The password, when the Source asks
// for it, is sent over verified TLS and wiped.
package identity

import (
	"context"
	"crypto/tls"
	"log/slog"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/virtual-db/vdb-mysql/internal/grants"
	"github.com/virtual-db/vdb-mysql/internal/handshake"
)

var log = slog.With("pkg", "identity")

// Grants is what the relay needs from the grant mirror.
type Grants interface {
	Fallback() bool
	EnsureAccount(ctx context.Context, account, method string) (grants.Principal, error)
	ApplyForLogin(ctx context.Context, account string, showGrants []string) error
}

// Relay opens Login sessions to the Source.
type Relay struct {
	Addr    string
	TLS     *tls.Config
	Timeout time.Duration
	Grants  Grants

	Accepted, Refused, PasswordAsked atomic.Int64
}

// State is a login's state.
type State int

const (
	Start State = iota
	Challenged
	NeedPassword
	Done
	Closed
)

// Result is a login step's outcome.
type Result struct {
	Accepted       bool
	PasswordNeeded bool
	Account        string // 'user'@'host'
	User, Host     string
	RequireSSL     bool
	Code           uint16
	Message        string
}

// Login is one login attempt. Used by one goroutine.
type Login struct {
	r         *Relay
	state     State
	conn      *handshake.Conn
	user      string
	host      string
	method    string // the method the plug-in speaks to the client
	srcMethod string // the Source account's method
	nonce     []byte
	pending   string
	pendNonce []byte
}

// New starts a login.
func (r *Relay) New() *Login { return &Login{r: r} }

func refused(err error) Result {
	if d, ok := err.(*handshake.Deny); ok {
		return Result{Code: d.Code, Message: d.Msg}
	}
	return Result{Code: 1045, Message: err.Error()}
}

// Begin dials the Source, upgrades to TLS and learns the account's method and
// challenge. It returns the challenge to send to the client.
func (l *Login) Begin(user, host, method string) ([]byte, Result) {
	if l.state != Start {
		return nil, Result{Code: 1045, Message: "login out of order"}
	}
	c, err := handshake.Dial(l.r.Addr, l.r.TLS, l.r.Timeout)
	if err != nil {
		l.state = Closed
		l.r.Refused.Add(1)
		return nil, refused(err)
	}
	l.conn, l.user, l.host, l.method = c, user, host, method
	m, nonce, err := c.Probe(user)
	if err != nil {
		l.Abort()
		l.r.Refused.Add(1)
		return nil, refused(err)
	}
	l.srcMethod, l.nonce = m, nonce
	l.state = Challenged
	if nonce == nil {
		// Methods without a 20-byte challenge cannot be relayed; the client
		// still needs one to answer.
		nonce = make([]byte, 20)
	}
	return nonce, Result{}
}

// Answer forwards the client's answer to the Source's challenge.
func (l *Login) Answer(ans []byte) Result {
	if l.state != Challenged {
		return Result{Code: 1045, Message: "login out of order"}
	}
	if l.srcMethod != l.method || l.nonce == nil {
		// The client answered for another method than the Source account
		// uses; the Source needs the password (doc 11 §2.3).
		l.pending, l.pendNonce = l.srcMethod, l.nonce
		l.state = NeedPassword
		l.r.PasswordAsked.Add(1)
		return Result{PasswordNeeded: true}
	}
	step, m, n, err := l.conn.Send(ans)
	return l.after(step, m, n, err)
}

func (l *Login) after(step handshake.Step, m string, n []byte, err error) Result {
	if err != nil {
		l.Abort()
		l.r.Refused.Add(1)
		return refused(err)
	}
	switch step {
	case handshake.OK:
		return l.success()
	case handshake.FullAuth:
		l.pending = "sha2-full"
	case handshake.Switch:
		l.pending, l.pendNonce, l.srcMethod = m, n, m
	}
	l.state = NeedPassword
	l.r.PasswordAsked.Add(1)
	return Result{PasswordNeeded: true}
}

// Password completes what the Source asked for. pw is wiped before return.
func (l *Login) Password(pw []byte) Result {
	defer handshake.Wipe(pw)
	if l.state != NeedPassword {
		return Result{Code: 1045, Message: "login out of order"}
	}
	var out []byte
	switch l.pending {
	case "sha2-full", handshake.Sha256:
		out = append(append(out, pw...), 0) // over verified TLS only (Dial refuses without it)
	case handshake.Native:
		out = handshake.ScrambleNative(pw, l.pendNonce)
	case handshake.Sha2:
		out = handshake.ScrambleSha2(pw, l.pendNonce)
	default:
		l.Abort()
		l.r.Refused.Add(1)
		return Result{Code: 1045, Message: "the Source account uses the " + l.pending + " login method, which VDB does not support"}
	}
	pending := l.pending
	l.pending = ""
	step, m, n, err := l.conn.Send(out)
	handshake.Wipe(out)
	if err == nil && step == handshake.FullAuth && pending == handshake.Sha2 {
		// Switched to caching_sha2 and its cache is cold: send the password itself.
		out = append(append([]byte(nil), pw...), 0)
		step, m, n, err = l.conn.Send(out)
		handshake.Wipe(out)
	}
	if err == nil && step != handshake.OK {
		l.Abort()
		l.r.Refused.Add(1)
		return Result{Code: 1045, Message: "the Source asked for the password twice"}
	}
	return l.after(step, m, n, err)
}

var requireRe = regexp.MustCompile(`\bREQUIRE (SSL|X509|ISSUER|SUBJECT|CIPHER)\b`)

// success reads the authenticated account, refreshes privileges when the
// grant tables are not readable, makes sure the mirrored account exists, and
// closes the Source connection.
func (l *Login) success() Result {
	defer l.Abort()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := l.conn.Query("SELECT CURRENT_USER()")
	if err != nil || len(rows) == 0 {
		l.r.Refused.Add(1)
		return Result{Code: 1045, Message: "VDB could not read the authenticated account from the Source"}
	}
	account := rows[0][0]
	p := grants.ParseAccount(account)
	res := Result{Accepted: true, User: p.User, Host: p.Host, Account: p.String()}
	if cu, err := l.conn.Query("SHOW CREATE USER CURRENT_USER()"); err == nil && len(cu) > 0 {
		res.RequireSSL = requireRe.MatchString(cu[0][0])
	}
	if l.r.Grants != nil {
		if l.r.Grants.Fallback() {
			if g, err := l.conn.Query("SHOW GRANTS"); err == nil {
				var lines []string
				for _, r := range g {
					lines = append(lines, r[0])
				}
				if err := l.r.Grants.ApplyForLogin(ctx, account, lines); err != nil {
					log.Warn("privileges from SHOW GRANTS not applied", "err", err)
				}
			}
		}
		if _, err := l.r.Grants.EnsureAccount(ctx, account, l.srcMethod); err != nil {
			l.r.Refused.Add(1)
			return Result{Code: 1045, Message: "VDB could not create the mirrored account: " + err.Error()}
		}
	}
	l.state = Done
	l.r.Accepted.Add(1)
	return res
}

// Abort closes the Source connection. Safe to call in any state.
func (l *Login) Abort() {
	if l.conn != nil {
		if l.state == Done {
			l.conn.Quit()
		} else {
			l.conn.Close()
		}
		l.conn = nil
	}
	if l.state != Done {
		l.state = Closed
	}
}

// State returns the login's state.
func (l *Login) State() State { return l.state }

// Method returns the method the plug-in speaks.
func (l *Login) Method() string { return l.method }
