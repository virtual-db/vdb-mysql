// Package grants — manager.go
//
// The mirror's state, its application over the control channel, the login
// hooks (EnsureAccount, ApplyForLogin) and the poller (doc 11 §3.3, §3.4).
package grants

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

var log = slog.With("pkg", "grants")

// Exec runs one statement on mysqld.
type Exec func(ctx context.Context, stmt string) error

// Status is one account's row of vdb.accounts.
type Status struct {
	User, Host, Method string
	GrantsReadAt       time.Time
	Reachable          bool
	Reason             string
	Process            bool // the Source account holds PROCESS
}

// Manager keeps the mirrored accounts. Safe for concurrent use.
type Manager struct {
	source   Querier
	mysqld   Querier
	exec     Exec
	exposed  []string
	vdbHost  string
	service  string
	defaultM string

	mu                     sync.Mutex
	fallback               bool
	want                   map[Principal]*Account
	status                 map[Principal]Status
	process                map[Principal]bool
	mandatory, activateAll string
	lastPoll               time.Time
	pollDur                time.Duration
	pollErr                string
	locks                  sync.Map // user -> *sync.Mutex
}

// Config configures the manager.
type Config struct {
	Source        Querier
	Mysqld        Querier
	Exec          Exec
	Exposed       []string
	ServiceUser   string
	DefaultMethod string // the Source's default login method (its greeting)
}

// New returns a Manager.
func New(c Config) *Manager {
	return &Manager{source: c.Source, mysqld: c.Mysqld, exec: c.Exec, exposed: c.Exposed, service: c.ServiceUser,
		defaultM: c.DefaultMethod, want: map[Principal]*Account{}, status: map[Principal]Status{}, process: map[Principal]bool{}}
}

// SetExposed sets the exposed databases.
func (m *Manager) SetExposed(dbs []string) {
	m.mu.Lock()
	m.exposed = dbs
	m.mu.Unlock()
}

// SetDefaultMethod sets the Source's default login method.
func (m *Manager) SetDefaultMethod(method string) {
	m.mu.Lock()
	m.defaultM = method
	m.mu.Unlock()
}

// Fallback reports whether grants come from SHOW GRANTS at login.
func (m *Manager) Fallback() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fallback
}

func (m *Manager) skip(p Principal) bool { return Internal(p) }

// Load reads the Source's accounts and computes the mirrored set. A read that
// fails for lack of privilege switches to the fallback.
func (m *Manager) Load(ctx context.Context) error {
	if m.vdbHost == "" {
		if rows, err := m.source(ctx, "SELECT USER()"); err == nil && len(rows) == 1 {
			if i := strings.LastIndexByte(rows[0][0], '@'); i >= 0 {
				m.vdbHost = rows[0][0][i+1:]
			}
		}
	}
	if rows, err := m.source(ctx, "SELECT @@global.mandatory_roles, @@global.activate_all_roles_on_login"); err == nil && len(rows) == 1 {
		m.mu.Lock()
		m.mandatory, m.activateAll = rows[0][0], rows[0][1]
		m.mu.Unlock()
	}
	src, err := ReadAccounts(ctx, m.source, m.skip)
	if err != nil {
		m.mu.Lock()
		m.fallback = true
		m.mu.Unlock()
		log.Warn("grant tables not readable; privileges refresh at each login from SHOW GRANTS", "err", err)
		return nil
	}
	procRows, _ := m.source(ctx, "SELECT User, Host FROM mysql.user WHERE Process_priv = 'Y'")
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallback = false
	m.want = map[Principal]*Account{}
	m.status = map[Principal]Status{}
	m.process = map[Principal]bool{}
	for _, r := range procRows {
		m.process[Principal{r[0], r[1]}] = true
	}
	for p, a := range src {
		w := Mirror(a, m.exposed)
		if !w.Role {
			w.Atoms[Atom{Priv: "SELECT", DB: "vdb", Table: "*"}] = true
		}
		m.want[p] = w
		st := Status{User: p.User, Host: p.Host, Method: a.Method, GrantsReadAt: now, Reachable: true, Process: m.process[p]}
		if m.vdbHost != "" && !Reachable(p.Host, m.vdbHost) {
			st.Reachable, st.Reason = false, "the Source matches VDB as host "+m.vdbHost+", which this account's host does not match"
		}
		if !w.Role && a.Method != "caching_sha2_password" && a.Method != "mysql_native_password" && a.Method != "sha256_password" {
			st.Reason = "login method " + a.Method + " is not supported"
		}
		m.status[p] = st
	}
	return nil
}

// Statements returns what to run on mysqld now: the account differences,
// PROXY grants for each account to the catch-all and same-user accounts,
// and the global role settings.
func (m *Manager) Statements(ctx context.Context) ([]string, error) {
	have, err := ReadAccounts(ctx, m.mysqld, func(p Principal) bool { return Internal(p) })
	if err != nil {
		return nil, fmt.Errorf("grants: read mysqld: %w", err)
	}
	m.mu.Lock()
	want := m.want
	fallback := m.fallback
	mandatory, activate := m.mandatory, m.activateAll
	m.mu.Unlock()
	if fallback {
		// Without grant tables only learned accounts exist; never drop them.
		for k := range have {
			if want[k] == nil {
				want[k] = have[k]
			}
		}
	}
	delete(have, Principal{"", "%"})
	stmts := Diff(want, have)
	stmts = append(stmts, m.proxyStatements(want, have)...)
	if mandatory != "" || activate != "" {
		if mandatory != "" {
			stmts = append(stmts, "SET GLOBAL mandatory_roles = "+quoteName(mandatory))
		}
		if activate == "1" || strings.EqualFold(activate, "ON") {
			stmts = append(stmts, "SET GLOBAL activate_all_roles_on_login = ON")
		} else if activate != "" {
			stmts = append(stmts, "SET GLOBAL activate_all_roles_on_login = OFF")
		}
	}
	return stmts, nil
}

func (m *Manager) proxyStatements(want, have map[Principal]*Account) []string {
	var keys []Principal
	for k, a := range want {
		if !a.Role {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	var out []string
	for _, k := range keys {
		if have[k] != nil {
			continue // granted when it was created
		}
		out = append(out, "GRANT PROXY ON "+k.String()+" TO ''@'%'")
		for _, o := range keys {
			if o.User == k.User && o != k {
				out = append(out, "GRANT PROXY ON "+k.String()+" TO "+o.String())
			}
		}
	}
	return out
}

// CatchAll returns the statement that creates the catch-all account.
func (m *Manager) CatchAll() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return "CREATE USER IF NOT EXISTS ''@'%' IDENTIFIED WITH " + PluginFor(m.defaultM)
}

// Apply runs the statements in order. A failing statement is logged; the
// next poll recomputes from scratch, so nothing stays partially applied.
func (m *Manager) Apply(ctx context.Context) error {
	stmts, err := m.Statements(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, s := range stmts {
		if err := m.exec(ctx, s); err != nil {
			log.Warn("grant statement failed", "stmt", firstWords(s), "err", err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func firstWords(s string) string {
	f := strings.Fields(s)
	if len(f) > 4 {
		f = f[:4]
	}
	return strings.Join(f, " ")
}

// Poll runs one poll: read, mirror, diff, apply, end sessions of locked or dropped accounts.
func (m *Manager) Poll(ctx context.Context) {
	start := time.Now()
	m.mu.Lock()
	before := map[Principal]bool{}
	for k, a := range m.want {
		if !a.Locked {
			before[k] = true
		}
	}
	m.mu.Unlock()
	err := m.Load(ctx)
	if err == nil {
		err = m.Apply(ctx)
	}
	m.mu.Lock()
	var gone []Principal
	if !m.fallback {
		for k := range before {
			if a := m.want[k]; a == nil || a.Locked {
				gone = append(gone, k)
			}
		}
	}
	m.lastPoll, m.pollDur = time.Now(), time.Since(start)
	if err != nil {
		m.pollErr = err.Error()
	} else {
		m.pollErr = ""
	}
	m.mu.Unlock()
	for _, k := range gone {
		m.killSessions(ctx, k)
	}
}

// killSessions ends the open sessions of an account.
func (m *Manager) killSessions(ctx context.Context, p Principal) {
	rows, err := m.mysqld(ctx, "SELECT ID, HOST FROM information_schema.PROCESSLIST WHERE USER = "+quoteName(p.User))
	if err != nil {
		return
	}
	for _, r := range rows {
		host := r[1]
		if i := strings.LastIndexByte(host, ':'); i > 0 {
			host = host[:i]
		}
		if Reachable(p.Host, host) {
			_ = m.exec(ctx, "KILL "+r[0])
		}
	}
}

// Run polls every interval until ctx ends.
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Poll(ctx)
		}
	}
}

func (m *Manager) userLock(user string) func() {
	v, _ := m.locks.LoadOrStore(user, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// EnsureAccount makes sure the mirrored account named account ('user'@'host'
// as CURRENT_USER() returns it) exists in mysqld, created with the plug-in
// for method, with PROXY granted to the catch-all and same-user accounts.
func (m *Manager) EnsureAccount(ctx context.Context, account, method string) (Principal, error) {
	p := ParseAccount(account)
	defer m.userLock(p.User)()
	rows, err := m.mysqld(ctx, "SELECT plugin FROM mysql.user WHERE User = "+quoteName(p.User)+" AND Host = "+quoteName(p.Host))
	if err != nil {
		return p, err
	}
	if len(rows) == 0 {
		stmts := []string{
			"CREATE USER " + p.String() + " IDENTIFIED WITH " + PluginFor(method),
			"GRANT SELECT ON `vdb`.* TO " + p.String(),
			"GRANT PROXY ON " + p.String() + " TO ''@'%'",
		}
		others, _ := m.mysqld(ctx, "SELECT Host FROM mysql.user WHERE User = "+quoteName(p.User)+" AND Host <> "+quoteName(p.Host))
		for _, o := range others {
			stmts = append(stmts, "GRANT PROXY ON "+p.String()+" TO "+Principal{p.User, o[0]}.String())
		}
		for _, s := range stmts {
			if err := m.exec(ctx, s); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}

// ApplyForLogin applies the privileges read with SHOW GRANTS on the relay
// connection, when the grant tables are not readable.
func (m *Manager) ApplyForLogin(ctx context.Context, account string, showGrants []string) error {
	p := ParseAccount(account)
	src := FromShowGrants(p, showGrants)
	m.mu.Lock()
	w := Mirror(src, m.exposed)
	w.Atoms[Atom{Priv: "SELECT", DB: "vdb", Table: "*"}] = true
	if old := m.want[p]; old != nil {
		w.Method = old.Method
	}
	m.want[p] = w
	m.status[p] = Status{User: p.User, Host: p.Host, Method: w.Method, GrantsReadAt: time.Now(), Reachable: true, Reason: "learned at login (SHOW GRANTS)"}
	m.mu.Unlock()
	defer m.userLock(p.User)()
	have, err := ReadAccounts(ctx, m.mysqld, func(q Principal) bool { return q != p })
	if err != nil {
		return err
	}
	for _, s := range Diff(map[Principal]*Account{p: w}, have) {
		if strings.HasPrefix(s, "DROP USER") || strings.HasPrefix(s, "CREATE USER") {
			continue
		}
		if err := m.exec(ctx, s); err != nil {
			log.Warn("login grant statement failed", "stmt", firstWords(s), "err", err)
		}
	}
	return nil
}

// ParseAccount parses 'user'@'host' or user@host.
func ParseAccount(s string) Principal {
	i := strings.LastIndexByte(s, '@')
	if i < 0 {
		return Principal{User: unq(s), Host: "%"}
	}
	return Principal{User: unq(s[:i]), Host: unq(s[i+1:])}
}

func unq(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '`' || s[0] == '"') {
		s = s[1 : len(s)-1]
	}
	return strings.ReplaceAll(s, "''", "'")
}

// Accounts returns the rows of vdb.accounts.
func (m *Manager) Accounts() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.status))
	for _, s := range m.status {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User+"@"+out[i].Host < out[j].User+"@"+out[j].Host })
	return out
}

// HasProcess reports whether an account holds PROCESS on the Source.
func (m *Manager) HasProcess(account string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.process[ParseAccount(account)]
}

// PollInfo returns the last poll's time, duration and error.
func (m *Manager) PollInfo() (time.Time, time.Duration, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPoll, m.pollDur, m.pollErr
}
