// Package source is the Source adapter (implementation plan doc 10 §1 to §5):
// two connection pools, pinned snapshots, session mirroring, the request
// shapes, paged reads, the request cache, the request log and the
// guardrails. It implements core.Source; the core never sees SQL.
package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/virtual-db/vdb-mysql/internal/logx"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/config"
	"github.com/virtual-db/vdb-mysql/internal/sourcetls"
)

var log = logx.Pkg("source")

// Pushdown reduces a pushed condition to the parts the Source may evaluate.
type Pushdown interface {
	Reduce(d *core.TableDef, cond string) (sql string, none bool)
}

// Adapter is the Source adapter. Safe for concurrent use.
type Adapter struct {
	cfg      config.Source
	snapPool *sql.DB
	bgPool   *sql.DB
	catalog  core.Catalog
	pushdown Pushdown
	Log      *Log
	version  string

	mu        sync.Mutex
	snapshots map[*snapshot]bool
	stop      chan struct{}
	emit      func(event string, payload any)
}

var _ core.Source = (*Adapter)(nil)

// New returns an adapter; Connect opens the pools.
func New(cfg config.Source) *Adapter {
	a := &Adapter{cfg: cfg, snapshots: map[*snapshot]bool{}, stop: make(chan struct{})}
	return a
}

// SetCatalog installs the catalog of record.
func (a *Adapter) SetCatalog(c core.Catalog) { a.catalog = c }

// SetPushdown installs the pushdown reducer.
func (a *Adapter) SetPushdown(p Pushdown) { a.pushdown = p }

// SetEmitter installs the event emitter and creates the request log.
func (a *Adapter) SetEmitter(emit func(event string, payload any)) {
	a.emit = emit
	a.Log = NewLog(a.cfg.LogSize, a.cfg.LogFile, func(r Record) {
		if a.emit != nil {
			a.emit("vdb.source.requested", r)
		}
	})
}

// DSN builds the driver DSN for the service account.
func (a *Adapter) DSN() string {
	c := mysql.NewConfig()
	c.User = a.cfg.User
	c.Passwd = a.cfg.Password
	c.Net = "tcp"
	c.Addr = a.cfg.Host + ":" + strconv.Itoa(a.cfg.Port)
	c.TLSConfig = sourcetls.Name
	c.AllowNativePasswords = true
	c.InterpolateParams = false
	c.ParseTime = false
	c.Timeout = a.cfg.ConnectWait
	c.Params = map[string]string{
		"transaction_read_only": "1",
		"character_set_results": "NULL",
		"max_execution_time":    strconv.FormatInt(a.cfg.RequestTimeout.Milliseconds(), 10),
	}
	return c.FormatDSN()
}

// Connect opens both pools and checks the service account can log in.
// Connection failures are retried within the retry window (product doc 08 §4).
func (a *Adapter) Connect(ctx context.Context) error {
	if a.Log == nil {
		a.SetEmitter(nil)
	}
	dsn := a.DSN()
	var err error
	a.snapPool, err = sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("source.connect: %w", err)
	}
	a.snapPool.SetMaxOpenConns(a.cfg.MaxConnections)
	a.snapPool.SetMaxIdleConns(a.cfg.MaxConnections)
	a.bgPool, err = sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("source.connect: %w", err)
	}
	a.bgPool.SetMaxOpenConns(4)
	deadline := time.Now().Add(a.cfg.RetryWindow)
	for {
		var v string
		err = a.bgPool.QueryRowContext(ctx, "/* vdb env="+a.cfg.EnvID+" */ SELECT VERSION()").Scan(&v)
		if err == nil {
			a.version = v
			break
		}
		var ut *sourcetls.ErrUntrusted
		if errors.As(err, &ut) || time.Now().After(deadline) {
			return fmt.Errorf("source.connect: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	go a.watchdog()
	return nil
}

// Version returns the Source's VERSION().
func (a *Adapter) Version() string { return a.version }

// Close closes the pools.
func (a *Adapter) Close() error {
	close(a.stop)
	if a.snapPool != nil {
		a.snapPool.Close()
	}
	if a.bgPool != nil {
		a.bgPool.Close()
	}
	return nil
}

func (a *Adapter) comment(conn uint64) string {
	return "/* vdb env=" + strings.ReplaceAll(a.cfg.EnvID, "*/", "") + " conn=" + strconv.FormatUint(conn, 10) + " */ "
}

// table returns a table's definition.
func (a *Adapter) table(t core.TableID) (*core.TableDef, error) {
	if a.catalog == nil {
		return nil, errors.New("source: no catalog")
	}
	d, ok := a.catalog.Table(t)
	if !ok {
		return nil, fmt.Errorf("source: table %d is not in the catalog", t)
	}
	return &d, nil
}

// querier runs statements: *sql.Conn or *sql.DB.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// run sends a shaped statement and reads every row as raw bytes, logging it.
func (a *Adapter) run(ctx context.Context, q querier, conn uint64, digest string, d *core.TableDef, s shaped) ([][]core.Value, error) {
	if err := check(s); err != nil {
		return nil, err
	}
	start := time.Now()
	rec := Record{ConnID: conn, Digest: digest, Shape: s.shape, Started: start}
	if d != nil {
		rec.Schema, rec.Table = d.Database, d.Name
	}
	rows, err := q.QueryContext(ctx, a.comment(conn)+s.sql)
	if err != nil {
		rec.Duration, rec.Error = time.Since(start), err.Error()
		a.Log.Add(rec)
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]core.Value
	var nbytes int64
	raw := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			rec.Duration, rec.Error = time.Since(start), err.Error()
			a.Log.Add(rec)
			return nil, err
		}
		r := make([]core.Value, len(raw))
		for i, b := range raw {
			if b == nil {
				r[i] = core.Value{Null: true}
			} else {
				r[i] = core.Value{B: append([]byte(nil), b...)}
				nbytes += int64(len(b))
			}
		}
		out = append(out, r)
	}
	err = rows.Err()
	rec.Duration, rec.Rows, rec.Bytes = time.Since(start), int64(len(out)), nbytes
	if err != nil {
		rec.Error = err.Error()
	}
	a.Log.Add(rec)
	return out, err
}

// Background runs a fixed catalog read on the background pool.
func (a *Adapter) Background(ctx context.Context, s shaped) ([][]core.Value, error) {
	return a.run(ctx, a.bgPool, 0, "", nil, s)
}

// Snapshot opens the Source half of a snapshot pair.
func (a *Adapter) Snapshot(info core.SessionInfo, ctx core.StmtContext) (core.SourceSnapshot, error) {
	s := &snapshot{a: a, info: info, ctx: ctx, opened: time.Now(), mirrored: map[string]string{}, digest: ctx.Digest}
	s.lastUsed = s.opened
	if !a.cfg.Snapshot {
		s.off = true
		return s, nil
	}
	if err := s.open(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.snapshots[s] = true
	a.mu.Unlock()
	return s, nil
}

// watchdog closes snapshots idle past the idle timeout, so a client that
// leaves a transaction open does not hold back the Source's purge.
func (a *Adapter) watchdog() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
		a.mu.Lock()
		var idle []*snapshot
		for s := range a.snapshots {
			if s.idleFor() > a.cfg.SnapshotIdle {
				idle = append(idle, s)
			}
		}
		a.mu.Unlock()
		for _, s := range idle {
			s.expire()
		}
	}
}

// Snapshots returns the age of every open snapshot by connection id.
func (a *Adapter) Snapshots() map[uint64]time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[uint64]time.Duration{}
	for s := range a.snapshots {
		out[s.info.ConnID] = time.Since(s.opened)
	}
	return out
}

func (a *Adapter) forget(s *snapshot) {
	a.mu.Lock()
	delete(a.snapshots, s)
	a.mu.Unlock()
}
