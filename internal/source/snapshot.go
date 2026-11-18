// Package source — snapshot.go
//
// The Source half of a snapshot pair (doc 10 §2): a pinned connection inside
// START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY, its mirrored
// session settings, its request cache, and every read the core asks of it.
package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"

	core "github.com/virtual-db/vdb-core"
)

// MirroredSettings are the session settings mirrored to the Source (product doc 08 §4).
var MirroredSettings = []string{"time_zone", "sql_mode", "div_precision_increment", "collation_connection", "character_set_connection"}

type snapshot struct {
	a        *Adapter
	info     core.SessionInfo
	ctx      core.StmtContext
	digest   string
	conn     *sql.Conn
	off      bool // VDB_SOURCE_SNAPSHOT=off: no transaction, any pooled connection
	mu       sync.Mutex
	opened   time.Time
	lastUsed time.Time
	mirrored map[string]string
	expired  bool
	closed   bool
	noPush   bool
	rows     int64
}

func (s *snapshot) open() error {
	if err := s.reserve(); err != nil {
		return err
	}
	return s.begin()
}

// reserve takes a pooled connection and mirrors the session settings onto
// it; it may wait up to the connect wait for a free connection.
func (s *snapshot) reserve() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.a.cfg.ConnectWait)
	defer cancel()
	conn, err := s.a.snapPool.Conn(ctx)
	if err != nil {
		return &core.Error{Code: core.ErrSource, Msg: "Source connection limit reached or Source unavailable: " + err.Error() + "; retry the statement"}
	}
	s.conn = conn
	if err := s.mirror(); err != nil {
		conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// Start opens the consistent snapshot on the reserved connection.
func (s *snapshot) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.off {
		return nil
	}
	s.opened, s.lastUsed = time.Now(), time.Now()
	if err := s.begin(); err != nil {
		return err
	}
	s.a.mu.Lock()
	s.a.snapshots[s] = true
	s.a.mu.Unlock()
	return nil
}

func (s *snapshot) begin() error {
	conn := s.conn
	if _, err := conn.ExecContext(context.Background(), s.a.comment(s.info.ConnID)+"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
		conn.Close()
		s.conn = nil
		return &core.Error{Code: core.ErrSource, Msg: "cannot open a Source snapshot: " + err.Error()}
	}
	return nil
}

// mirror applies the evaluation context's settings that differ from what the
// connection already has. A setting the Source refuses turns pushdown off for
// the session, with a warning.
func (s *snapshot) mirror() error {
	var set [][2]string
	for _, name := range MirroredSettings {
		v, ok := s.ctx.Eval[name]
		if !ok || s.mirrored[name] == v {
			continue
		}
		set = append(set, [2]string{name, v})
	}
	if len(set) == 0 {
		return nil
	}
	q := querier(s.a.snapPool)
	if s.conn != nil {
		q = s.conn
	}
	sh := renderSet(set)
	if err := check(sh); err != nil {
		return err
	}
	if _, err := q.ExecContext(context.Background(), s.a.comment(s.info.ConnID)+sh.sql); err != nil {
		log.Warn("Source refused a mirrored setting; pushdown is off for this session", "conn", s.info.ConnID, "err", err)
		s.noPush = true
		return nil
	}
	for _, kv := range set {
		s.mirrored[kv[0]] = kv[1]
	}
	return nil
}

func (s *snapshot) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastUsed)
}

// expire closes an idle snapshot; its next request fails or reopens.
func (s *snapshot) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.expired {
		return
	}
	s.expired = true
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	s.a.forget(s)
	if s.a.emit != nil {
		s.a.emit("vdb.snapshot.expired", map[string]any{"ConnectionID": s.info.ConnID, "AgeMs": time.Since(s.opened).Milliseconds()})
	}
}

// before checks the snapshot's age and idle limits before a request.
func (s *snapshot) before() (querier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("source: snapshot closed")
	}
	if s.off {
		s.lastUsed = time.Now()
		return s.a.snapPool, nil
	}
	tooOld := time.Since(s.opened) > s.a.cfg.SnapshotMaxAge
	if s.expired || tooOld {
		if s.a.cfg.SnapshotPolicy != "relaxed" {
			return nil, &core.Error{Code: core.ErrSource, Msg: "the Source snapshot expired; retry the transaction"}
		}
		if s.conn != nil {
			s.conn.Close()
			s.conn = nil
		}
		s.mirrored = map[string]string{}
		if err := s.open(); err != nil {
			return nil, err
		}
		s.opened, s.expired = time.Now(), false
		log.Warn("Source snapshot renewed under the relaxed policy", "conn", s.info.ConnID)
	}
	s.lastUsed = time.Now()
	return s.conn, nil
}

func (s *snapshot) run(d *core.TableDef, sh shaped) ([][]core.Value, error) {
	q, err := s.before()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.a.cfg.RequestTimeout+5*time.Second)
	defer cancel()
	rows, err := s.a.run(ctx, q, s.info.ConnID, s.digest, d, sh)
	if err != nil {
		return nil, &core.Error{Code: core.ErrSource, Table: d.ID, Msg: "Source request failed for " + d.Database + "." + d.Name + ": " + err.Error()}
	}
	s.rows += int64(len(rows))
	if max := s.a.cfg.MaxSourceRows; max > 0 && s.rows > max {
		return nil, &core.Error{Code: core.ErrSource, Table: d.ID, Msg: fmt.Sprintf("the statement read more than VDB_MAX_SOURCE_ROWS (%d) rows from the Source", max)}
	}
	return rows, nil
}

// fill turns result rows of a projection into requested rows and keys.
func fill(d *core.TableDef, p projection, raw [][]core.Value, skip int) core.SourceRows {
	var out core.SourceRows
	for _, r := range raw {
		r = r[skip:]
		row := make(core.Row, len(p.reqAt))
		for i, at := range p.reqAt {
			if at >= 0 && at < len(r) {
				row[i] = r[at]
				continue
			}
			c := d.Columns[p.reqCols[i]]
			if c.Fetch == core.Default {
				row[i] = c.Default
			} else {
				row[i] = core.Value{Null: true}
			}
		}
		kv := make([]core.Value, len(p.keyAt))
		for i, at := range p.keyAt {
			kv[i] = r[at]
		}
		out.Rows = append(out.Rows, row)
		out.Keys = append(out.Keys, core.EncodeSourceKey(kv))
	}
	return out
}

func (s *snapshot) pushed(d *core.TableDef, cond string) (string, bool) {
	if cond == "" || s.noPush || !s.a.cfg.Pushdown || s.a.pushdown == nil {
		return "", false
	}
	return s.a.pushdown.Reduce(d, cond)
}

// Scan opens a paged Source cursor (D-84).
func (s *snapshot) Scan(req core.SourceScan) (core.SourceCursor, error) {
	d, err := s.a.table(req.Table)
	if err != nil {
		return nil, err
	}
	pushed, none := s.pushed(d, req.Pushed)
	idx := req.Index
	if !req.Ordered {
		idx = d.Identity
	}
	order := pageOrder(d, idx)
	return &pager{s: s, d: d, p: project(d, req.Columns), req: req, idx: idx, pushed: pushed, none: none,
		order: order, keyset: keysetSafe(d, order), limit: s.a.cfg.BatchRows}, nil
}

// pager reads one page per Next; each page is read to its end before the
// first row is returned, so the connection is idle between pages.
type pager struct {
	s      *snapshot
	d      *core.TableDef
	p      projection
	req    core.SourceScan
	idx    core.IndexID
	pushed string
	none   bool
	order  []uint16
	keyset bool
	after  []core.Value
	offset int
	limit  int
	done   bool
}

func (c *pager) Next(maxRows int) (core.SourceRows, error) {
	if c.done || c.none {
		return core.SourceRows{}, io.EOF
	}
	limit := c.limit
	if maxRows > 0 && maxRows < limit {
		limit = maxRows
	}
	// Select the order columns too, for keyset continuation.
	p := c.p
	base := len(p.exprs)
	exprs := append(append([]string(nil), p.exprs...), rawCols(c.d, c.order)...)
	p.exprs = exprs
	var after []core.Value
	if c.keyset {
		after = c.after
	}
	sh := renderPage(c.d, p, c.idx, c.req.Ranges, after, c.offset, c.pushed, c.req.Dir, limit, c.req.Ordered)
	raw, err := c.s.run(c.d, sh)
	if err != nil {
		return core.SourceRows{}, err
	}
	if len(raw) < limit {
		c.done = true
	}
	if len(raw) > 0 {
		last := raw[len(raw)-1]
		c.after = append([]core.Value(nil), last[base:]...)
		c.offset += len(raw)
	}
	trimmed := make([][]core.Value, len(raw))
	for i, r := range raw {
		trimmed[i] = r[:base]
	}
	out := fill(c.d, c.p, trimmed, 0)
	if len(out.Rows) == 0 && c.done {
		return out, io.EOF
	}
	return out, nil
}

func (c *pager) Close() error { c.done = true; return nil }

// Lookup answers keys on an index in batches of vdb_lookup_batch keys.
func (s *snapshot) Lookup(t core.TableID, idx core.IndexID, keys []core.Key, cols core.ColumnSet) (core.SourceRows, error) {
	d, err := s.a.table(t)
	if err != nil {
		return core.SourceRows{}, err
	}
	ix := d.Index(idx)
	var icols []uint16
	if ix != nil {
		icols = ix.Columns
	} else {
		icols = identityCols(d)
	}
	vals := make([][]core.Value, len(keys))
	for i, k := range keys {
		v := core.DecodeSourceKey(k.Raw)
		if len(v) > len(icols) {
			v = v[:len(icols)]
		}
		vals[i] = v
	}
	return s.tagged(d, cols, icols, vals, ShapeKeyBatch)
}

func (s *snapshot) tagged(d *core.TableDef, cols core.ColumnSet, keyCols []uint16, vals [][]core.Value, shape string) (core.SourceRows, error) {
	p := project(d, cols)
	var out core.SourceRows
	batch := s.a.cfg.LookupBatch
	for start := 0; start < len(vals); start += batch {
		end := start + batch
		if end > len(vals) {
			end = len(vals)
		}
		var part [][]core.Value
		var at []int
		for i := start; i < end; i++ {
			if len(vals[i]) == 0 {
				continue
			}
			part = append(part, vals[i])
			at = append(at, i)
		}
		if len(part) == 0 {
			continue
		}
		raw, err := s.run(d, renderKeyBatch(d, p, keyCols[:len(part[0])], part, shape))
		if err != nil {
			return core.SourceRows{}, err
		}
		sort.SliceStable(raw, func(i, j int) bool { return tagOf(raw[i]) < tagOf(raw[j]) })
		got := fill(d, p, raw, 1)
		for i, r := range raw {
			out.Rows = append(out.Rows, got.Rows[i])
			out.Keys = append(out.Keys, got.Keys[i])
			out.KeyIdx = append(out.KeyIdx, uint32(at[tagOf(r)]))
		}
	}
	return out, nil
}

func tagOf(r []core.Value) int {
	n, _ := strconv.Atoi(string(r[0].B))
	return n
}

// Exists reports which identity keys exist on the Source.
func (s *snapshot) Exists(t core.TableID, keys []core.SourceKey) ([]bool, error) {
	d, err := s.a.table(t)
	if err != nil {
		return nil, err
	}
	vals := make([][]core.Value, len(keys))
	for i, k := range keys {
		vals[i] = core.DecodeSourceKey(k)
	}
	icols := identityCols(d)
	out := make([]bool, len(keys))
	// For tables without a usable key the "key" is the whole row; ask by every column.
	sr, err := s.tagged(d, core.ColumnSet{}, icols, vals, ShapeExists)
	if err != nil {
		return nil, err
	}
	for _, k := range sr.KeyIdx {
		out[k] = true
	}
	return out, nil
}

// Count runs SELECT COUNT(*).
func (s *snapshot) Count(t core.TableID) (uint64, error) {
	d, err := s.a.table(t)
	if err != nil {
		return 0, err
	}
	raw, err := s.run(d, renderCount(d))
	if err != nil {
		return 0, err
	}
	if len(raw) == 0 {
		return 0, nil
	}
	return strconv.ParseUint(string(raw[0][0].B), 10, 64)
}

// ByColumns returns the rows whose columns cols equal one of values.
func (s *snapshot) ByColumns(t core.TableID, cols core.ColumnSet, values []core.Row, want core.ColumnSet) (core.SourceCursor, error) {
	d, err := s.a.table(t)
	if err != nil {
		return nil, err
	}
	vals := make([][]core.Value, len(values))
	for i, v := range values {
		vals[i] = v
	}
	sr, err := s.tagged(d, want, cols, vals, ShapeColumns)
	if err != nil {
		return nil, err
	}
	return &onceCursor{sr: sr}, nil
}

type onceCursor struct {
	sr   core.SourceRows
	done bool
}

func (c *onceCursor) Next(int) (core.SourceRows, error) {
	if c.done {
		return core.SourceRows{}, io.EOF
	}
	c.done = true
	return c.sr, nil
}
func (c *onceCursor) Close() error { return nil }

// Close commits the read-only transaction and returns the connection.
func (s *snapshot) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.a.forget(s)
	if s.conn != nil {
		sh := shaped{sql: "COMMIT", shape: ShapeSession}
		_, _ = s.conn.ExecContext(context.Background(), s.a.comment(s.info.ConnID)+sh.sql)
		s.conn.Close()
		s.conn = nil
	}
	return nil
}
