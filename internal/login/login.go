// Package login is the login-channel handler (implementation plan doc 09 §4):
// each login connection becomes one identity.Login, and each message calls
// its method. The connection's close aborts an unfinished login, so the
// Source connection is never leaked.
package login

import (
	"github.com/virtual-db/vdb-mysql/internal/handshake"
	"github.com/virtual-db/vdb-mysql/internal/identity"
	"github.com/virtual-db/vdb-mysql/protocol"
)

// Handler implements protocol.LoginHandler.
type Handler struct {
	Relay *identity.Relay
}

// Hello accepts login connections.
func (h *Handler) Hello(*protocol.Hello) (*protocol.HelloReply, error) {
	return &protocol.HelloReply{}, nil
}

func reply(r identity.Result) *protocol.AuthReply {
	switch {
	case r.Accepted:
		return &protocol.AuthReply{Result: protocol.AuthResult_AUTH_ACCEPTED, Account: r.Account, User: r.User, Host: r.Host, RequireSsl: r.RequireSSL}
	case r.PasswordNeeded:
		return &protocol.AuthReply{Result: protocol.AuthResult_AUTH_PASSWORD_NEEDED}
	}
	return &protocol.AuthReply{Result: protocol.AuthResult_AUTH_REFUSED, Code: uint32(r.Code), Message: r.Message}
}

// Serve runs one login.
func (h *Handler) Serve(c *protocol.Conn, _ *protocol.Hello) {
	l := h.Relay.New()
	defer l.Abort()
	for {
		op, body, err := c.ReadRequest()
		if err != nil {
			return
		}
		switch op {
		case protocol.OpAuthBegin:
			var m protocol.AuthBegin
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			nonce, r := l.Begin(m.User, m.Host, m.Method)
			if r.Code != 0 {
				_ = c.Reply(op, &protocol.AuthBeginReply{Refused: true, Code: uint32(r.Code), Message: r.Message})
				continue
			}
			_ = c.Reply(op, &protocol.AuthBeginReply{Scramble: nonce, Method: m.Method})
		case protocol.OpAuthAnswer:
			var m protocol.AuthAnswer
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			_ = c.Reply(op, reply(l.Answer(m.Answer)))
		case protocol.OpAuthPass:
			var m protocol.AuthPass
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			r := l.Password(m.Password)
			handshake.Wipe(body)
			_ = c.Reply(op, reply(r))
		case protocol.OpAuthAbort:
			l.Abort()
			_ = c.Reply(op, &protocol.Empty{})
			return
		default:
			_ = c.Fail(&protocol.Error{Code: protocol.CodeProtocol, Message: "unexpected op on a login connection"})
			return
		}
	}
}
