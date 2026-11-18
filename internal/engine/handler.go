// Package engine is the data-channel handler (implementation plan doc 09 §3):
// one conn per mysqld session with its core.Session and open scans, and one
// method per message of doc 07 §3.2. It also serves the kill connection.
package engine

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/virtual-db/vdb-core"
	"google.golang.org/protobuf/proto"

	"github.com/virtual-db/vdb-mysql/internal/collations"
	"github.com/virtual-db/vdb-mysql/internal/stats"
	"github.com/virtual-db/vdb-mysql/protocol"
)

var log = slog.With("pkg", "engine")

// Config holds the batch limits.
type Config struct {
	BatchRows   int
	BatchBytes  int
	InsertBatch int
}

// SessionStats are one session's counters for vdb.sessions.
type SessionStats struct {
	ConnID          uint64
	Account         string
	StatementsNoBKA int64
	Since           time.Time
}

// Handler implements protocol.DataHandler and protocol.KillHandler.
type Handler struct {
	api    core.DriverAPI
	stats  *stats.Stats
	proven *collations.Proven
	cfg    Config

	mu    sync.Mutex
	conns map[uint64]*conn
}

// New returns a Handler.
func New(api core.DriverAPI, st *stats.Stats, proven *collations.Proven, cfg Config) *Handler {
	if cfg.BatchRows <= 0 {
		cfg.BatchRows = 4096
	}
	if cfg.BatchBytes <= 0 {
		cfg.BatchBytes = 512 * 1024
	}
	return &Handler{api: api, stats: st, proven: proven, cfg: cfg, conns: map[uint64]*conn{}}
}

// Hello accepts data and kill connections.
func (h *Handler) Hello(*protocol.Hello) (*protocol.HelloReply, error) {
	return &protocol.HelloReply{}, nil
}

// Sessions returns the counters of every open data connection.
func (h *Handler) Sessions() []SessionStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionStats, 0, len(h.conns))
	for _, c := range h.conns {
		out = append(out, SessionStats{ConnID: c.id, Account: c.account, StatementsNoBKA: c.noBKA.Load(), Since: c.since})
	}
	return out
}

// DropAllConnections ends every data connection's session (mysqld crashed).
func (h *Handler) DropAllConnections() {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.pc.Close()
	}
}

// Serve serves a data or kill connection.
func (h *Handler) Serve(pc *protocol.Conn, hello *protocol.Hello) {
	if hello.Kind == protocol.ConnKind_CONN_KILL {
		h.serveKill(pc)
		return
	}
	c := &conn{h: h, pc: pc, id: hello.ConnId, scans: map[uint32]*scan{}, seen: map[core.TableID]uint64{}, since: time.Now()}
	h.mu.Lock()
	if old := h.conns[c.id]; old != nil {
		old.pc.Close()
	}
	h.conns[c.id] = c
	h.mu.Unlock()
	defer func() {
		c.end()
		h.mu.Lock()
		if h.conns[c.id] == c {
			delete(h.conns, c.id)
		}
		h.mu.Unlock()
	}()
	for {
		op, body, err := pc.ReadRequest()
		if err != nil {
			return
		}
		if op == protocol.OpSessionEnd {
			c.end()
			_ = pc.Reply(op, &protocol.Empty{})
			return
		}
		reply, err := c.handle(op, body)
		if err != nil {
			if pe, ok := err.(*protoErr); ok {
				_ = pc.Fail(&protocol.Error{Code: protocol.CodeProtocol, Message: pe.msg})
				return
			}
			if werr := pc.Fail(toError(err)); werr != nil {
				return
			}
			continue
		}
		if err := pc.Reply(op, reply); err != nil {
			return
		}
	}
}

func (h *Handler) serveKill(pc *protocol.Conn) {
	for {
		op, body, err := pc.ReadRequest()
		if err != nil {
			return
		}
		if op != protocol.OpKill {
			_ = pc.Fail(&protocol.Error{Code: protocol.CodeProtocol, Message: "only Kill is accepted on the kill connection"})
			return
		}
		var k protocol.Kill
		if err := protocol.Decode(body, &k); err != nil {
			return
		}
		h.api.Kill(k.ConnId)
		_ = pc.Reply(op, &protocol.Empty{})
	}
}

type protoErr struct{ msg string }

func (e *protoErr) Error() string { return e.msg }

// conn is one data connection.
type conn struct {
	h       *Handler
	pc      *protocol.Conn
	id      uint64
	account string
	control bool
	sess    core.Session
	scans   map[uint32]*scan
	nextID  uint32
	seen    map[core.TableID]uint64
	pending []core.Batch // the rest of a lookup cut at the byte cap
	pendKey []byte
	noBKA   atomic.Int64
	since   time.Time
	ctx     core.StmtContext
	waited  bool
}

type scan struct {
	h     core.ScanHandle
	done  [3]bool // merged, source, delta
	table core.TableID
}

func (c *conn) end() {
	for id, s := range c.scans {
		_ = s.h.Close()
		delete(c.scans, id)
	}
	if c.sess != nil {
		_ = c.sess.End()
		c.sess = nil
	}
}

func (c *conn) session() (core.Session, error) {
	if c.sess == nil {
		s, err := c.h.api.OpenSession(core.SessionInfo{ConnID: c.id, Account: c.account, Control: c.control})
		if err != nil {
			return nil, err
		}
		c.sess = s
	}
	c.waited = false
	c.sess.SetCallbacks(c.onWait, c.onImages)
	return c.sess, nil
}

// onWait writes the Waiting frame once per request (doc 07 §1).
func (c *conn) onWait(holder uint64) {
	if c.waited {
		return
	}
	c.waited = true
	_ = c.pc.Waiting(holder)
}

// onImages asks vdb.so for the sort images of rows a cascade writes (D-86).
func (c *conn) onImages(t core.TableID, rows []core.Row) ([][]core.KeyImage, error) {
	req := &protocol.ImagesNeeded{Table: uint32(t)}
	if d, ok := c.h.api.Catalog().Table(t); ok {
		req.Database, req.Name = d.Database, d.Name
	}
	for _, r := range rows {
		req.Rows = append(req.Rows, fromRow(r))
	}
	rep, err := c.pc.AskImages(req)
	if err != nil {
		return nil, err
	}
	if rep.Error != nil {
		return nil, &core.Error{Code: core.ErrInternal, Table: t, Msg: rep.Error.Message}
	}
	out := make([][]core.KeyImage, len(rep.Rows))
	for i, im := range rep.Rows {
		out[i] = toImages(im)
	}
	return out, nil
}

func decode[T any, P interface {
	*T
	proto.Message
}](body []byte) (P, error) {
	m := P(new(T))
	if err := protocol.Decode(body, m); err != nil {
		return nil, &protoErr{msg: err.Error()}
	}
	return m, nil
}

// warnings returns a warning when a table changed since this session last saw it.
func (c *conn) warnings(t core.TableID, version uint64) []*protocol.Warning {
	d, ok := c.h.api.Catalog().Table(t)
	if !ok {
		return nil
	}
	last, seen := c.seen[t]
	c.seen[t] = d.Version
	if (seen && last != d.Version) || (version != 0 && version < d.Version && !seen) {
		return []*protocol.Warning{{Code: 1105, Text: "table " + d.Database + "." + d.Name + " changed on the Source: see vdb.schema_changes"}}
	}
	return nil
}

func (c *conn) handle(op protocol.Op, body []byte) (proto.Message, error) {
	switch op {
	case protocol.OpTableOpen:
		m, err := decode[protocol.TableOpen](body)
		if err != nil {
			return nil, err
		}
		return c.tableOpen(m)
	case protocol.OpStmtBegin:
		m, err := decode[protocol.StmtBegin](body)
		if err != nil {
			return nil, err
		}
		c.account, c.control = m.Account, m.Control
		if !m.Bka {
			c.noBKA.Add(1)
		}
		c.ctx = toStmtContext(m)
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		return &protocol.Empty{}, s.StmtBegin(c.ctx)
	case protocol.OpStmtEnd, protocol.OpStmtRollback, protocol.OpCommit, protocol.OpRollback, protocol.OpSnapshot:
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		switch op {
		case protocol.OpStmtEnd:
			c.closeScans()
			err = s.StmtEnd()
		case protocol.OpStmtRollback:
			c.closeScans()
			err = s.StmtRollback()
		case protocol.OpCommit:
			c.closeScans()
			err = s.Commit()
		case protocol.OpRollback:
			c.closeScans()
			err = s.Rollback()
		case protocol.OpSnapshot:
			err = s.Snapshot()
		}
		return &protocol.Empty{}, err
	case protocol.OpSavepoint, protocol.OpRollbackTo, protocol.OpRelease:
		m, err := decode[protocol.Savepoint](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		switch op {
		case protocol.OpSavepoint:
			err = s.Savepoint(m.Name)
		case protocol.OpRollbackTo:
			err = s.RollbackTo(m.Name)
		default:
			err = s.Release(m.Name)
		}
		return &protocol.Empty{}, err
	case protocol.OpScanOpen:
		m, err := decode[protocol.ScanOpen](body)
		if err != nil {
			return nil, err
		}
		return c.scanOpen(m)
	case protocol.OpScanNext:
		m, err := decode[protocol.ScanNext](body)
		if err != nil {
			return nil, err
		}
		return c.scanNext(m)
	case protocol.OpScanClose:
		m, err := decode[protocol.ScanClose](body)
		if err != nil {
			return nil, err
		}
		if s := c.scans[m.Scan]; s != nil {
			_ = s.h.Close()
			delete(c.scans, m.Scan)
		}
		return &protocol.Empty{}, nil
	case protocol.OpLookup:
		m, err := decode[protocol.Lookup](body)
		if err != nil {
			return nil, err
		}
		return c.lookup(m)
	case protocol.OpLookupNext:
		return c.lookupNext()
	case protocol.OpCount:
		m, err := decode[protocol.Count](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		n, declined, err := s.Count(core.TableID(m.Table))
		return &protocol.CountReply{Count: n, Declined: declined}, err
	case protocol.OpStats:
		m, err := decode[protocol.Stats](body)
		if err != nil {
			return nil, err
		}
		return c.statsReply(m)
	case protocol.OpInsert:
		m, err := decode[protocol.Insert](body)
		if err != nil {
			return nil, err
		}
		return c.insert(m.Table, []*protocol.Row{m.Row}, []*protocol.Images{m.Images})
	case protocol.OpInsertBatch:
		m, err := decode[protocol.InsertBatch](body)
		if err != nil {
			return nil, err
		}
		return c.insert(m.Table, m.Rows, m.Images)
	case protocol.OpUpdate:
		m, err := decode[protocol.Update](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		return &protocol.Empty{}, s.Update(core.TableID(m.Table), toRef(m.Old), toRow(m.Row), toImages(m.Images))
	case protocol.OpDelete:
		m, err := decode[protocol.Delete](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		return &protocol.Empty{}, s.Delete(core.TableID(m.Table), toRef(m.Old))
	case protocol.OpAutoIncrement:
		m, err := decode[protocol.AutoIncrement](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		first, granted, err := s.AutoIncrement(core.TableID(m.Table), m.Count)
		return &protocol.AutoIncrementReply{First: first, Granted: granted}, err
	case protocol.OpTruncate:
		m, err := decode[protocol.Truncate](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		return &protocol.Empty{}, s.Truncate(core.TableID(m.Table))
	case protocol.OpCatalog:
		m, err := decode[protocol.Catalog](body)
		if err != nil {
			return nil, err
		}
		return c.catalog(m)
	case protocol.OpAlter:
		m, err := decode[protocol.Alter](body)
		if err != nil {
			return nil, err
		}
		return c.alter(m)
	case protocol.OpAlterRows:
		m, err := decode[protocol.AlterRows](body)
		if err != nil {
			return nil, err
		}
		return c.alterRows(m)
	case protocol.OpAlterPut:
		m, err := decode[protocol.AlterPut](body)
		if err != nil {
			return nil, err
		}
		s, err := c.session()
		if err != nil {
			return nil, err
		}
		rows := make([]core.Row, len(m.Rows))
		ims := make([][]core.KeyImage, len(m.Rows))
		for i, r := range m.Rows {
			rows[i] = toRow(r)
			if i < len(m.Images) {
				ims[i] = toImages(m.Images[i])
			}
		}
		return &protocol.Empty{}, s.AlterPut(core.TableID(m.Table), rows, ims)
	}
	return nil, &protoErr{msg: "unknown op " + op.String()}
}

func (c *conn) closeScans() {
	for id, s := range c.scans {
		_ = s.h.Close()
		delete(c.scans, id)
	}
	c.pending = nil
}

func (c *conn) tableOpen(m *protocol.TableOpen) (proto.Message, error) {
	d, ok := c.h.api.Catalog().TableByName(m.Database, m.Table)
	if !ok {
		return nil, &core.Error{Code: core.ErrRefused, Msg: "table " + m.Database + "." + m.Table + " is not in VDB's catalog of record"}
	}
	rep := &protocol.TableOpenReply{Table: uint32(d.ID), Version: d.Version, Kind: kindOf(d.Kind),
		ShadowCheck: d.Kind == core.Mirrored, Identity: uint32(d.Identity)}
	for _, ix := range d.Indexes {
		rep.Indexes = append(rep.Indexes, &protocol.IndexFlags{Id: uint32(ix.ID), Name: ix.Name, Ordered: ix.OrderProven, EqualityProven: ix.EqualityProven})
	}
	for _, col := range d.Columns {
		rep.Fetch = append(rep.Fetch, fetchOf(col.Fetch))
	}
	c.seen[d.ID] = d.Version
	return rep, nil
}

func (c *conn) next(cur core.Cursor, max int) (core.Batch, bool, error) {
	b, err := cur.Next(max, c.h.cfg.BatchBytes)
	if err == io.EOF {
		return b, true, nil
	}
	return b, false, err
}

func (c *conn) scanOpen(m *protocol.ScanOpen) (proto.Message, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	req := core.ScanRequest{Table: core.TableID(m.Table), Index: core.IndexID(m.Index), Ordered: m.Kind != protocol.ScanKind_SCAN_UNORDERED,
		Columns: toCols(m.Columns), Pushed: m.Pushed, Lock: toLock(m.Lock)}
	if m.Backward {
		req.Dir = core.Backward
	}
	for _, r := range m.Ranges {
		req.Ranges = append(req.Ranges, toRange(r))
	}
	start := time.Now()
	sh, err := s.Scan(req)
	if err != nil {
		return nil, err
	}
	max := int(m.MaxRows)
	if max <= 0 || max > c.h.cfg.BatchRows {
		max = c.h.cfg.BatchRows
	}
	c.nextID++
	id := c.nextID
	sc := &scan{h: sh, table: req.Table}
	rep := &protocol.ScanOpenReply{Scan: id, Warnings: c.warnings(req.Table, m.Version)}
	for _, img := range sh.ShadowImages() {
		rep.ShadowImages = append(rep.ShadowImages, img)
	}
	if !req.Ordered {
		b, end, err := c.next(sh.Merged(), max)
		if err != nil {
			_ = sh.Close()
			return nil, err
		}
		sc.done[0] = end
		rep.Merged = fromBatch(b, end)
		c.observe(req.Table, b, start)
	} else {
		b, end, err := c.next(sh.Source(), max)
		if err != nil {
			_ = sh.Close()
			return nil, err
		}
		sc.done[1] = end
		rep.Source = fromBatch(b, end)
		db, dend, err := c.next(sh.Delta(), max)
		if err != nil {
			_ = sh.Close()
			return nil, err
		}
		sc.done[2] = dend
		rep.Delta = fromBatch(db, dend)
		c.observe(req.Table, b, start)
	}
	c.scans[id] = sc
	return rep, nil
}

func (c *conn) observe(t core.TableID, b core.Batch, start time.Time) {
	if c.h.stats == nil || len(b.Rows) == 0 {
		return
	}
	n := 0
	for _, r := range b.Rows {
		n += core.RowSize(r)
	}
	c.h.stats.Observe(t, len(b.Rows), n, time.Since(start))
}

func (c *conn) scanNext(m *protocol.ScanNext) (proto.Message, error) {
	sc := c.scans[m.Scan]
	if sc == nil {
		return nil, &core.Error{Code: core.ErrInternal, Msg: "unknown scan " + strconv.Itoa(int(m.Scan))}
	}
	if _, err := c.session(); err != nil {
		return nil, err
	}
	var cur core.Cursor
	idx := 0
	switch m.Cursor {
	case protocol.CursorKind_CURSOR_SOURCE:
		cur, idx = sc.h.Source(), 1
	case protocol.CursorKind_CURSOR_DELTA:
		cur, idx = sc.h.Delta(), 2
	default:
		cur = sc.h.Merged()
	}
	if cur == nil || sc.done[idx] {
		return &protocol.ScanNextReply{Batch: &protocol.Batch{End: true}}, nil
	}
	max := int(m.MaxRows)
	if max <= 0 || max > c.h.cfg.BatchRows {
		max = c.h.cfg.BatchRows
	}
	start := time.Now()
	b, end, err := c.next(cur, max)
	if err != nil {
		return nil, err
	}
	sc.done[idx] = end
	c.observe(sc.table, b, start)
	return &protocol.ScanNextReply{Batch: fromBatch(b, end)}, nil
}

// lookup answers keys; a reply larger than the byte cap is cut and continued.
func (c *conn) lookup(m *protocol.Lookup) (proto.Message, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	keys := make([]core.Key, len(m.Keys))
	for i, k := range m.Keys {
		keys[i] = toKey(k)
	}
	b, shadows, err := s.Lookup(core.TableID(m.Table), core.IndexID(m.Index), keys, toCols(m.Columns), toLock(m.Lock))
	if err != nil {
		return nil, err
	}
	rep := &protocol.LookupReply{Warnings: c.warnings(core.TableID(m.Table), m.Version)}
	for _, img := range shadows {
		rep.ShadowImages = append(rep.ShadowImages, img)
	}
	first, rest := c.cut(b)
	rep.Batch = fromBatch(first, rest == nil)
	if rest != nil {
		c.pending = []core.Batch{*rest}
		c.pendKey = []byte(strconv.FormatInt(time.Now().UnixNano(), 36))
		rep.Continuation = c.pendKey
	}
	return rep, nil
}

func (c *conn) lookupNext() (proto.Message, error) {
	if len(c.pending) == 0 {
		return &protocol.LookupReply{Batch: &protocol.Batch{End: true}}, nil
	}
	b := c.pending[0]
	c.pending = nil
	first, rest := c.cut(b)
	rep := &protocol.LookupReply{Batch: fromBatch(first, rest == nil)}
	if rest != nil {
		c.pending = []core.Batch{*rest}
		rep.Continuation = c.pendKey
	}
	return rep, nil
}

// cut splits a batch at the byte cap; one row larger than the cap travels alone.
func (c *conn) cut(b core.Batch) (core.Batch, *core.Batch) {
	size := 0
	for i, r := range b.Rows {
		size += core.RowSize(r)
		if size > c.h.cfg.BatchBytes && i > 0 {
			first := core.Batch{Rows: b.Rows[:i], Refs: b.Refs[:i]}
			rest := core.Batch{Rows: b.Rows[i:], Refs: b.Refs[i:]}
			if len(b.KeyIdx) == len(b.Rows) {
				first.KeyIdx, rest.KeyIdx = b.KeyIdx[:i], b.KeyIdx[i:]
			}
			return first, &rest
		}
	}
	return b, nil
}

func (c *conn) statsReply(m *protocol.Stats) (proto.Message, error) {
	d, ok := c.h.api.Catalog().Table(core.TableID(m.Table))
	if !ok {
		return nil, &core.Error{Code: core.ErrRefused, Msg: "unknown table"}
	}
	var r *core.KeyRange
	if m.Range != nil {
		kr := toRange(m.Range)
		r = &kr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := c.h.stats.For(ctx, &d, core.IndexID(m.Index), r)
	rep := &protocol.StatsReply{Records: st.Records, MeanRecLength: st.MeanRecLength, MrrLengthPerRec: st.MrrLengthPerRec,
		RecordsInRange: st.RecordsInRange, BatchCost: st.BatchCost, Estimated: true, DataFileLength: st.DataFileLength}
	for _, ix := range d.Indexes {
		rep.Indexes = append(rep.Indexes, &protocol.IndexStats{Index: uint32(ix.ID), RecPerKey: st.RecPerKey[ix.ID]})
	}
	return rep, nil
}

func (c *conn) insert(t uint32, prows []*protocol.Row, pims []*protocol.Images) (proto.Message, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	rows := make([]core.Row, len(prows))
	ims := make([][]core.KeyImage, len(prows))
	for i, r := range prows {
		rows[i] = toRow(r)
		if i < len(pims) {
			ims[i] = toImages(pims[i])
		}
	}
	res, err := s.Insert(core.TableID(t), rows, ims)
	if err != nil {
		return nil, err
	}
	rep := &protocol.InsertReply{}
	for _, r := range res {
		if r.Err != nil {
			rep.Results = append(rep.Results, &protocol.WriteResult{Error: toError(r.Err)})
		} else {
			rep.Results = append(rep.Results, &protocol.WriteResult{Ok: true})
		}
	}
	for len(rep.Results) < len(rows) {
		rep.Results = append(rep.Results, &protocol.WriteResult{Ok: true})
	}
	return rep, nil
}

func (c *conn) alterRows(m *protocol.AlterRows) (proto.Message, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	cur := s.AlterRows(core.TableID(m.Table))
	var all core.Batch
	for {
		b, err := cur.Next(c.h.cfg.BatchRows, 0)
		all.Rows = append(all.Rows, b.Rows...)
		all.Refs = append(all.Refs, b.Refs...)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return &protocol.ScanNextReply{Batch: fromBatch(all, true)}, nil
}

func lower(s string) string { return strings.ToLower(s) }
