// Package server is the core.Server implementation (implementation plan doc
// 09 §2.2): Run executes the start-up steps of doc 02 §4.1 in order, one
// method per step, and blocks; Stop executes doc 02 §4.2 step 2. Each step's
// failure ends Run with an error naming the step.
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/collations"
	"github.com/virtual-db/vdb-mysql/internal/config"
	"github.com/virtual-db/vdb-mysql/internal/control"
	"github.com/virtual-db/vdb-mysql/internal/discovery"
	"github.com/virtual-db/vdb-mysql/internal/engine"
	"github.com/virtual-db/vdb-mysql/internal/grants"
	"github.com/virtual-db/vdb-mysql/internal/handshake"
	"github.com/virtual-db/vdb-mysql/internal/identity"
	"github.com/virtual-db/vdb-mysql/internal/login"
	"github.com/virtual-db/vdb-mysql/internal/metrics"
	"github.com/virtual-db/vdb-mysql/internal/pushdown"
	"github.com/virtual-db/vdb-mysql/internal/rebuild"
	"github.com/virtual-db/vdb-mysql/internal/schemapoll"
	"github.com/virtual-db/vdb-mysql/internal/source"
	"github.com/virtual-db/vdb-mysql/internal/sourcetls"
	"github.com/virtual-db/vdb-mysql/internal/statements"
	"github.com/virtual-db/vdb-mysql/internal/stats"
	"github.com/virtual-db/vdb-mysql/internal/supervisor"
	"github.com/virtual-db/vdb-mysql/internal/vdbschema"
	"github.com/virtual-db/vdb-mysql/protocol"
)

var log = slog.With("pkg", "server")

// App is what the server needs from core.App.
type App interface {
	HasHandlers(point string) bool
}

// Server runs the environment.
type Server struct {
	cfg     *config.Config
	api     core.DriverAPI
	app     App
	src     *source.Adapter
	sup     *supervisor.Supervisor
	Version string

	tlsCfg      *tls.Config
	proven      *collations.Proven
	disc        *discovery.Discoverer
	dbs         []discovery.Database
	listener    *protocol.Server
	ctl         *control.Control
	grants      *grants.Manager
	rebuild     *rebuild.Rebuilder
	engine      *engine.Handler
	stmts       *statements.Handler
	stats       *stats.Stats
	poll        *schemapoll.Poller
	metrics     *metrics.Registry
	pushdown    *pushdown.Pushdown
	relay       *identity.Relay
	rebuildErrs []rebuild.Error

	mu      sync.Mutex
	state   string
	mysqldV string
	cancel  context.CancelFunc
	done    chan struct{}
	recover sync.Mutex
}

// New returns a Server.
func New(cfg *config.Config, api core.DriverAPI, app App, src *source.Adapter, sup *supervisor.Supervisor) *Server {
	return &Server{cfg: cfg, api: api, app: app, src: src, sup: sup, state: "starting", done: make(chan struct{}), metrics: metrics.New()}
}

func (s *Server) setState(st string) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	log.Info("state", "state", st)
}

// Run executes start-up steps 5 to 11 and blocks until Stop.
func (s *Server) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"connect to the Source", s.connectSource},
		{"discover the Source", s.discover},
		{"open the plug-in socket", s.listen},
		{"start mysqld", s.startMysqld},
		{"rebuild the data directory", s.rebuildAll},
		{"open the client port", s.openPort},
		{"start the pollers", s.startPollers},
	}
	for _, st := range steps {
		if err := st.fn(ctx); err != nil {
			s.setState("failed: " + st.name)
			s.cleanup()
			return fmt.Errorf("server: %s: %w", st.name, err)
		}
	}
	s.setState("ready")
	<-s.done
	return nil
}

// Step 5: TLS to the Source and the service-account pool.
func (s *Server) connectSource(ctx context.Context) error {
	cfg, err := sourcetls.Register(s.cfg.Source.Host, s.cfg.Source.CADir, s.cfg.Source.TLSFingerprint)
	if err != nil {
		return err
	}
	s.tlsCfg = cfg
	s.src.SetCatalog(s.api.Catalog())
	s.src.SetEmitter(s.api.Emit)
	if err := s.src.Connect(ctx); err != nil {
		return err
	}
	pd, err := pushdown.New(s.src.Version(), s.api.Transforms())
	if err != nil {
		return err
	}
	if !s.cfg.Source.Pushdown {
		pd.Disable()
	}
	s.pushdown = pd
	s.src.SetPushdown(pd)
	log.Info("connected to the Source", "host", s.cfg.Source.Host, "version", s.src.Version())
	return nil
}

// Step 6: databases, tables, objects and accounts into the catalog and the account set.
func (s *Server) discover(ctx context.Context) error {
	s.proven = collations.For(mysqldRelease())
	d, err := discovery.New(s.src, s.proven, s.cfg.Source.Databases)
	if err != nil {
		return err
	}
	s.disc = d
	res, err := d.Run(ctx)
	if err != nil {
		return err
	}
	s.dbs = res.Databases
	if err := s.api.Catalog().Load(res.Tables, res.Objects); err != nil {
		return err
	}
	s.stats = stats.New(s.src, s.api, s.cfg.Source.BusyThreads)
	for _, db := range s.dbs {
		_ = s.stats.Refresh(ctx, db.Name)
	}
	s.grants = grants.New(grants.Config{
		Source:      s.sourceQuerier,
		Mysqld:      s.mysqldQuerier,
		Exec:        s.mysqldExec,
		Exposed:     s.exposed(),
		ServiceUser: s.cfg.Source.User,
	})
	if c, err := handshake.Dial(s.cfg.Source.Host+":"+strconv.Itoa(s.cfg.Source.Port), s.tlsCfg, s.cfg.Source.ConnectWait); err == nil {
		s.grants.SetDefaultMethod(c.ServerDef)
		c.Close()
	}
	if err := s.grants.Load(ctx); err != nil {
		return err
	}
	var errs []rebuild.Error
	for _, e := range res.Errors {
		errs = append(errs, rebuild.Error{Type: e.Type, Schema: e.Schema, Name: e.Name, Error: e.Error})
	}
	for _, e := range res.Warnings {
		errs = append(errs, rebuild.Error{Type: e.Type, Schema: e.Schema, Name: e.Name, Error: "warning: " + e.Error})
	}
	s.rebuildErrs = errs
	log.Info("discovered the Source", "databases", len(res.Databases), "tables", len(res.Tables), "objects", len(res.Objects))
	return nil
}

func (s *Server) exposed() []string {
	var out []string
	for _, d := range s.dbs {
		out = append(out, d.Name)
	}
	return out
}

func (s *Server) sourceQuerier(ctx context.Context, sql string) ([][]string, error) {
	rows, err := s.src.Background(ctx, source.RenderCatalog(sql))
	if err != nil {
		return nil, err
	}
	return strRows(rows), nil
}

func strRows(rows [][]core.Value) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = make([]string, len(r))
		for j, v := range r {
			out[i][j] = string(v.B)
		}
	}
	return out
}

func (s *Server) mysqldQuerier(ctx context.Context, sql string) ([][]string, error) {
	rows, err := s.ctl.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = make([]string, len(r))
		for j, b := range r {
			out[i][j] = string(b)
		}
	}
	return out, nil
}

func (s *Server) mysqldExec(ctx context.Context, stmt string) error { return s.ctl.Exec(ctx, stmt) }

// Step 7: the plug-in protocol socket.
func (s *Server) listen(ctx context.Context) error {
	s.engine = engine.New(s.api, s.stats, s.proven, engine.Config{BatchRows: s.cfg.Source.BatchRows, BatchBytes: s.cfg.Source.BatchBytes, InsertBatch: s.cfg.InsertBatch})
	st, err := statements.New(s.api, func() bool { return s.app.HasHandlers("vdb.query.received.intercept") })
	if err != nil {
		return err
	}
	s.stmts = st
	relay := &identity.Relay{Addr: s.cfg.Source.Host + ":" + strconv.Itoa(s.cfg.Source.Port), TLS: s.tlsCfg,
		Timeout: s.cfg.Source.ConnectWait + 5*time.Second, Grants: s.grants}
	l, err := protocol.Listen(s.cfg.Socket, "vdb-mysql "+s.Version, protocol.Handlers{
		Data: s.engine, Kill: s.engine, Login: &login.Handler{Relay: relay}, Event: s.stmts})
	if err != nil {
		return err
	}
	s.listener = l
	s.relay = relay
	return nil
}

// Step 8: the data directory from the template, mysqld without networking.
func (s *Server) startMysqld(ctx context.Context) error {
	ctl, err := control.Open(s.cfg.MysqldSocket)
	if err != nil {
		return err
	}
	s.ctl = ctl
	s.sup.SetShutdown(func(ctx context.Context) error { return s.ctl.Exec(ctx, control.Shutdown()) })
	s.sup.OnCrash(s.recoverMysqld)
	if err := s.sup.Prepare(); err != nil {
		return err
	}
	if err := s.sup.Start(false); err != nil {
		return err
	}
	return s.waitControl(ctx)
}

func (s *Server) waitControl(ctx context.Context) error {
	deadline := time.Now().Add(time.Minute)
	for {
		err := s.ctl.Ping(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("control login as vdb_admin failed: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if rows, err := s.ctl.Query(ctx, "SELECT VERSION()"); err == nil && len(rows) == 1 {
		s.mu.Lock()
		s.mysqldV = string(rows[0][0])
		s.mu.Unlock()
	}
	return nil
}

// Step 9: every object, in order.
func (s *Server) rebuildAll(ctx context.Context) error {
	if s.rebuild == nil {
		s.rebuild = rebuild.New(s.ctl, s.grants, s.api.Catalog(), vdbschema.Tables(), func() []rebuild.Database {
			var out []rebuild.Database
			for _, d := range s.dbs {
				out = append(out, rebuild.Database{Name: d.Name, Charset: d.Charset, Collation: d.Collation})
			}
			return out
		})
		s.rebuild.AddErrors(s.rebuildErrs)
	}
	if err := s.rebuild.Run(ctx); err != nil {
		return err
	}
	s.registerProviders()
	return nil
}

// Step 10: a clean restart with networking on; the port opens only when the
// environment is complete.
func (s *Server) openPort(ctx context.Context) error {
	if err := s.sup.Shutdown(); err != nil {
		return err
	}
	if err := s.sup.Start(true); err != nil {
		return err
	}
	return s.waitControl(ctx)
}

// Step 11: the grant and schema pollers.
func (s *Server) startPollers(ctx context.Context) error {
	p, err := schemapoll.New(s.src, s.disc, s.ctl, s.api, s.stats, s.exposed)
	if err != nil {
		return err
	}
	s.poll = p
	if err := p.Init(ctx); err != nil {
		log.Warn("schema fingerprints not read at start; the first poll reads them", "err", err)
	}
	go p.Run(ctx, s.cfg.SchemaPoll)
	if !s.grants.Fallback() {
		go s.grants.Run(ctx, s.cfg.GrantPoll)
	}
	s.registerMetrics()
	return nil
}

// recoverMysqld rebuilds after an unexpected mysqld exit (doc 02 §4.3).
func (s *Server) recoverMysqld() {
	s.recover.Lock()
	defer s.recover.Unlock()
	s.setState("recovering after a mysqld exit")
	s.metrics.Add("vdb_mysqld_restarts", "", 1)
	s.engine.DropAllConnections()
	ctx := context.Background()
	for _, step := range []func(context.Context) error{
		func(context.Context) error { return s.sup.Prepare() },
		func(ctx context.Context) error {
			if err := s.sup.Start(false); err != nil {
				return err
			}
			return s.waitControl(ctx)
		},
		s.rebuildAll,
		s.openPort,
	} {
		if err := step(ctx); err != nil {
			log.Error("recovery failed", "err", err)
			s.setState("failed: recovery")
			return
		}
	}
	s.setState("ready")
}

// Stop stops the pollers, shuts mysqld down, closes the socket and the pool.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.setState("stopping")
	s.sup.Stopping()
	s.cleanup()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

func (s *Server) cleanup() {
	if s.sup != nil {
		_ = s.sup.Shutdown()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.ctl != nil {
		_ = s.ctl.Close()
	}
	if s.src != nil {
		_ = s.src.Close()
	}
}

func mysqldRelease() string {
	if v := strings.TrimSpace(readFile("/usr/local/mysql/share/vdb/release")); v != "" {
		return v
	}
	return "8.4"
}
