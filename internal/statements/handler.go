// Package statements is the event-channel handler (implementation plan doc 09
// §5): Rewrite, Ping, DdlCaptured, Event and QueryReceived from the
// statements plug-in.
package statements

import (
	"strings"
	"sync"
	"time"

	core "github.com/virtual-db/vdb-core"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/virtual-db/vdb-mysql/protocol"
)

// Counts are per-connection statement counters for vdb.sessions.
type Counts struct {
	Statements int64
	Rows       int64
}

// Handler implements protocol.EventHandler.
type Handler struct {
	api       core.DriverAPI
	parser    *sqlparser.Parser
	intercept func() bool

	mu     sync.Mutex
	counts map[uint64]*Counts
	conns  map[uint64][2]string // account, host
	Drops  int64
}

// New returns a Handler. intercept reports whether a handler is attached to
// vdb.query.received.intercept.
func New(api core.DriverAPI, intercept func() bool) (*Handler, error) {
	p, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.4.0"})
	if err != nil {
		return nil, err
	}
	return &Handler{api: api, parser: p, intercept: intercept, counts: map[uint64]*Counts{}, conns: map[uint64][2]string{}}, nil
}

// Hello tells the plug-in whether to send every statement as QueryReceived.
func (h *Handler) Hello(*protocol.Hello) (*protocol.HelloReply, error) {
	return &protocol.HelloReply{QueryIntercept: h.intercept != nil && h.intercept()}, nil
}

// Counts returns a connection's counters.
func (h *Handler) Counts(conn uint64) Counts {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.counts[conn]; c != nil {
		return *c
	}
	return Counts{}
}

// Serve serves one event connection.
func (h *Handler) Serve(c *protocol.Conn, _ *protocol.Hello) {
	for {
		op, body, err := c.ReadRequest()
		if err != nil {
			return
		}
		switch op {
		case protocol.OpRewrite:
			var m protocol.Rewrite
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			rs := EngineClauses(h.parser, m.Statement)
			rep := &protocol.RewriteReply{Unchanged: len(rs) == 0}
			for _, r := range rs {
				rep.Remove = append(rep.Remove, &protocol.ByteRange{Start: uint32(r.Start), End: uint32(r.End)})
			}
			_ = c.Reply(op, rep)
		case protocol.OpPing:
			_ = c.Reply(op, &protocol.Empty{})
		case protocol.OpDdlCaptured:
			var m protocol.DdlCaptured
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			if err := h.captured(&m); err != nil {
				_ = c.Fail(&protocol.Error{Code: uint32(core.ErrInternal), Message: err.Error()})
				continue
			}
			_ = c.Reply(op, &protocol.Empty{})
		case protocol.OpEvent:
			var m protocol.Event
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			h.event(&m)
		case protocol.OpQueryReceived:
			var m protocol.QueryReceived
			if err := protocol.Decode(body, &m); err != nil {
				return
			}
			q, err := h.api.QueryReceived(m.ConnId, m.Statement, m.Database)
			if err != nil {
				_ = c.Fail(&protocol.Error{Code: uint32(core.ErrRefused), Message: err.Error()})
				continue
			}
			_ = c.Reply(op, &protocol.QueryReceivedReply{Statement: q})
		default:
			_ = c.Fail(&protocol.Error{Code: protocol.CodeProtocol, Message: "unexpected op on an event connection"})
			return
		}
	}
}

// objectKind classifies a captured statement by its leading words.
func objectKind(stmt string) (core.ObjectKind, bool, bool) {
	f := strings.Fields(strings.ToUpper(stmt))
	drop := len(f) > 0 && f[0] == "DROP"
	for _, w := range f {
		switch w {
		case "VIEW":
			return core.View, drop, true
		case "PROCEDURE":
			return core.Procedure, drop, true
		case "FUNCTION":
			return core.Function, drop, true
		case "TRIGGER":
			return core.Trigger, drop, true
		}
	}
	return 0, drop, false
}

func (h *Handler) captured(m *protocol.DdlCaptured) error {
	kind, drop, ok := objectKind(m.Statement)
	if !ok {
		return nil
	}
	name := m.ObjectName
	db := m.Database
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		db, name = strings.Trim(name[:i], "`"), name[i+1:]
	}
	name = strings.Trim(name, "`")
	return h.api.Catalog().RecordObject(core.Object{Kind: kind, Database: db, Name: name, Statement: m.Statement,
		SQLMode: m.SqlMode, Definer: m.Definer, Dropped: drop})
}

func (h *Handler) event(m *protocol.Event) {
	switch m.Kind {
	case protocol.EventKind_EVENT_CONNECT:
		h.mu.Lock()
		h.conns[m.ConnId] = [2]string{m.Account, m.Host}
		h.mu.Unlock()
		_ = h.api.ConnectionOpened(m.ConnId, m.Account, m.Host)
	case protocol.EventKind_EVENT_DISCONNECT:
		h.mu.Lock()
		delete(h.conns, m.ConnId)
		delete(h.counts, m.ConnId)
		h.mu.Unlock()
		h.api.ConnectionClosed(m.ConnId, m.Account, m.Host)
	case protocol.EventKind_EVENT_STATEMENT:
		h.mu.Lock()
		c := h.counts[m.ConnId]
		if c == nil {
			c = &Counts{}
			h.counts[m.ConnId] = c
		}
		c.Statements++
		c.Rows += int64(m.Rows)
		h.mu.Unlock()
		h.api.QueryCompleted(m.ConnId, core.QueryInfo{Query: m.Query, Digest: m.Digest, Database: m.Database, Account: m.Account,
			Duration: time.Duration(m.DurationUs) * time.Microsecond, RowsAffected: int64(m.Rows), RowsSent: int64(m.RowsSent),
			ErrorCode: uint16(m.ErrorCode)})
	}
}
