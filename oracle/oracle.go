// Package oracle compares VDB with InnoDB (implementation plan doc 15 §3,
// product doc 16 §1). For each seed the same tables are created and filled
// on the Source and on the reference (the same mysqld build without vdb.so);
// the same generated steps run on VDB and on the reference; and every
// outcome is compared (exec.go). Source-writes steps change the Source and,
// identically, the reference. A mismatch is shrunk by delta debugging over
// all steps to a minimal case, written as a regression file that Replay runs
// again, and the run continues with the next seed; mismatches are grouped by
// signature in the report.
//
// Modes: single (one session, with explicit transactions and savepoints),
// Source writes (Source changes and Source DDL between steps), and
// concurrency (several sessions interleaved, concurrency.go).
//
// Tables are named per run and seed (orc_<run>_<seed>), so every run starts
// from an empty Delta without restarting VDB: VDB keeps the Delta of a
// Source table dropped and re-created between two schema polls, so a run
// must never reuse an earlier run's table names.
package oracle

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Env holds the three servers.
type Env struct {
	Source, VDB, Reference *sql.DB
	// WaitTables blocks until VDB sees the tables just created on the Source.
	WaitTables func(prefix string) error
}

// Mismatch is one difference between VDB and the reference.
type Mismatch struct {
	Seed      int64
	Mode      string
	Step      int
	SQL       string
	What      string
	VDB       Result
	Ref       Result
	Minimal   []Step
	Prefix    string
	Signature string
	File      string
	Detail    string // the first rows that differ
}

// Report summarises a run.
type Report struct {
	Run          string
	Seeds, Steps int
	Mismatches   []Mismatch
}

func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "oracle run %s: %d seeds, %d steps, %d mismatches\n", r.Run, r.Seeds, r.Steps, len(r.Mismatches))
	sigs := map[string]int{}
	for _, m := range r.Mismatches {
		sigs[m.Signature]++
	}
	if len(sigs) > 0 {
		keys := make([]string, 0, len(sigs))
		for k := range sigs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "by signature:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %3d  %s\n", sigs[k], k)
		}
	}
	for _, m := range r.Mismatches {
		fmt.Fprintf(&b, "\nseed %d (%s) step %d, %s differ: %s\n  vdb:       %s\n  reference: %s\n", m.Seed, m.Mode, m.Step, m.What, m.SQL, m.VDB.short(), m.Ref.short())
		if m.Detail != "" {
			fmt.Fprintf(&b, "%s", m.Detail)
		}
		if len(m.Minimal) > 0 {
			fmt.Fprintf(&b, "  minimal case (%d steps)", len(m.Minimal))
			if m.File != "" {
				fmt.Fprintf(&b, ", written to %s", m.File)
			}
			fmt.Fprintf(&b, ":\n")
			for _, s := range m.Minimal {
				fmt.Fprintf(&b, "    %s%s;\n", kindTag(s), s.SQL)
			}
		}
	}
	return b.String()
}

func kindTag(s Step) string {
	switch s.Kind {
	case SourceWrite, SourceDDL:
		return "-- source: "
	}
	if s.Prepared {
		return "-- prepared: "
	}
	return ""
}

// Config is a run's configuration.
type Config struct {
	Run         string // table-name run id; empty: derived from the clock
	Steps       int
	Modes       []string // single, source, concurrency
	Prepared    float64
	Regressions string // directory for minimal cases; empty: not written
}

// DefaultModes are the modes a release runs.
var DefaultModes = []string{"single", "source", "concurrency"}

// Run runs single mode over seeds, steps statements each (the first
// oracle's entry point, kept for vdb-tests).
func Run(env Env, seeds []int64, steps int) (*Report, error) {
	return RunWith(env, seeds, Config{Steps: steps, Modes: []string{"single"}})
}

// RunWith runs every mode of cfg over seeds.
func RunWith(env Env, seeds []int64, cfg Config) (*Report, error) {
	if cfg.Run == "" {
		cfg.Run = fmt.Sprintf("%x", time.Now().UnixNano()%0xfffffff)
	}
	if len(cfg.Modes) == 0 {
		cfg.Modes = []string{"single"}
	}
	if cfg.Steps <= 0 {
		cfg.Steps = 200
	}
	rep := &Report{Run: cfg.Run}
	for _, seed := range seeds {
		for _, mode := range cfg.Modes {
			rep.Seeds++
			var m *Mismatch
			var n int
			var err error
			switch mode {
			case "concurrency":
				m, n, err = runConcurrent(env, cfg, seed)
			default:
				m, n, err = runSingle(env, cfg, seed, mode)
			}
			rep.Steps += n
			if err != nil {
				return rep, fmt.Errorf("oracle: seed %d (%s): %w", seed, mode, err)
			}
			if m != nil {
				rep.Mismatches = append(rep.Mismatches, *m)
			}
		}
	}
	return rep, nil
}

func options(cfg Config, mode string) Options {
	return Options{Steps: cfg.Steps, SourceWrites: mode == "source", Prepared: cfg.Prepared, Transactions: true}
}

var modeTag = map[string]string{"single": "i", "source": "w", "concurrency": "c"}

func prefixFor(run string, mode string, seed int64, attempt int) string {
	tag := modeTag[mode]
	if tag == "" {
		tag = "x"
	}
	p := fmt.Sprintf("orc_%s_%s%d", run, tag, seed)
	if attempt > 0 {
		p += fmt.Sprintf("_s%d", attempt)
	}
	return p
}

// Seed creates and fills a seed's tables on the Source and the reference
// and waits until VDB sees them.
func Seed(env Env, s *Schema) error {
	for _, db := range []*sql.DB{env.Source, env.Reference} {
		if err := onConn(db, func(c *sql.Conn) error {
			for _, q := range append(s.DDL(), s.Rows()...) {
				if _, err := c.ExecContext(bg(), q); err != nil {
					return fmt.Errorf("%s: %w", firstLine(q), err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if env.WaitTables != nil {
		return env.WaitTables(s.Prefix)
	}
	return nil
}

func firstLine(q string) string {
	if i := strings.IndexByte(q, '\n'); i > 0 {
		return q[:i]
	}
	if len(q) > 120 {
		return q[:120]
	}
	return q
}

// pair is one session on VDB and the same session on the reference.
type pair struct{ vdb, ref *sql.Conn }

func openPair(env Env, s *Schema) (*pair, error) {
	ctx := bg()
	v, err := env.VDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	r, err := env.Reference.Conn(ctx)
	if err != nil {
		v.Close()
		return nil, err
	}
	for _, q := range s.Settings() {
		if _, err := v.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("vdb: %s: %w", q, err)
		}
		if _, err := r.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("reference: %s: %w", q, err)
		}
	}
	return &pair{vdb: v, ref: r}, nil
}

func (p *pair) close() {
	p.vdb.Close()
	p.ref.Close()
}

// sourceStep applies a Source write or Source DDL to the Source and the
// reference. Source DDL then waits until VDB shows the change.
func sourceStep(env Env, s *Schema, st Step) error {
	exec := func(db *sql.DB) error {
		return onConn(db, func(c *sql.Conn) error { _, err := c.ExecContext(bg(), st.SQL); return err })
	}
	errS := exec(env.Source)
	errR := exec(env.Reference)
	if (errS == nil) != (errR == nil) {
		return fmt.Errorf("the Source and the reference disagree on %s: %v / %v", st.SQL, errS, errR)
	}
	if st.Kind == SourceDDL && errS == nil {
		return waitColumns(env, s.T("s"), 30*time.Second)
	}
	return nil
}

// waitColumns blocks until VDB's table has as many columns as the Source's.
func waitColumns(env Env, table string, timeout time.Duration) error {
	q := "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = 'testdb' AND TABLE_NAME = ?"
	var want int
	if err := env.Source.QueryRow(q, table).Scan(&want); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		var got int
		if err := env.VDB.QueryRow(q, table).Scan(&got); err == nil && got == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("VDB did not show the Source's change to %s within %s", table, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// execSteps runs steps on one session pair from the start, stopping at the
// first difference. It returns that step's index and both outcomes.
func execSteps(env Env, s *Schema, steps []Step) (int, Result, Result, bool, error) {
	p, err := openPair(env, s)
	if err != nil {
		return 0, Result{}, Result{}, false, err
	}
	defer p.close()
	for i, st := range steps {
		if st.Kind == SourceWrite || st.Kind == SourceDDL {
			if err := sourceStep(env, s, st); err != nil {
				return i, Result{}, Result{}, false, err
			}
			continue
		}
		var sums []string
		if st.Kind == Write || st.Kind == DDL {
			for _, t := range st.Tables {
				if q := s.Checksum(t); q != "" {
					sums = append(sums, q)
				}
			}
		}
		v := run(p.vdb, st, sums)
		r := run(p.ref, st, sums)
		if !Equal(v, r) {
			return i, v, r, true, nil
		}
		if v.Timeout {
			return i, v, r, true, nil
		}
	}
	return 0, Result{}, Result{}, false, nil
}

func runSingle(env Env, cfg Config, seed int64, mode string) (*Mismatch, int, error) {
	s := NewSchema(prefixFor(cfg.Run, mode, seed, 0), seed)
	if err := Seed(env, s); err != nil {
		return nil, 0, err
	}
	steps := Generate(s, seed, options(cfg, mode))
	i, v, r, bad, err := execSteps(env, s, steps)
	if err != nil {
		return nil, len(steps), err
	}
	if !bad {
		return nil, len(steps), nil
	}
	m := &Mismatch{Seed: seed, Mode: mode, Step: i, SQL: steps[i].SQL, What: What(v, r), VDB: v, Ref: r, Prefix: s.Prefix}
	m.Signature = signature(mode, steps[i], m.What)
	m.Detail = detail(env, s, steps[i], v, r)
	m.Minimal = Shrink(env, cfg, seed, mode, s.Prefix, steps[:i+1])
	if cfg.Regressions != "" {
		m.File = writeRegression(cfg.Regressions, m)
	}
	return m, len(steps), nil
}

// signature groups mismatches: the mode, the statement's first words and
// table, and what differs.
func signature(mode string, st Step, what string) string {
	f := strings.Fields(st.SQL)
	head := strings.Join(f[:min(3, len(f))], " ")
	for _, w := range f {
		if strings.HasPrefix(w, "orc_") {
			if i := strings.LastIndexByte(w, '_'); i > 0 {
				head += " " + strings.Trim(w[i:], "(),;")
			}
			break
		}
	}
	return mode + " / " + strings.ToUpper(head) + " / " + what
}

// Shrink removes steps the failure does not need, by delta debugging over
// fresh tables (a new prefix per attempt). The last step is always kept.
func Shrink(env Env, cfg Config, seed int64, mode, prefix string, steps []Step) []Step {
	if len(steps) <= 1 {
		return steps
	}
	last := steps[len(steps)-1]
	rest := steps[:len(steps)-1]
	attempt := 0
	fails := func(cand []Step) bool {
		attempt++
		p := prefixFor(cfg.Run, mode, seed, attempt)
		s := NewSchema(p, seed)
		if err := Seed(env, s); err != nil {
			return false
		}
		cs := Rename(append(append([]Step(nil), cand...), last), prefix, p)
		_, _, _, bad, err := execSteps(env, s, cs)
		return err == nil && bad
	}
	for chunk := len(rest) / 2; chunk >= 1 && attempt < 60; chunk /= 2 {
		for i := 0; i+chunk <= len(rest) && attempt < 60; {
			try := append(append([]Step{}, rest[:i]...), rest[i+chunk:]...)
			if fails(try) {
				rest = try
			} else {
				i += chunk
			}
		}
	}
	return append(rest, last)
}

// writeRegression writes a minimal case as a file Replay runs.
func writeRegression(dir string, m *Mismatch) string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	name := filepath.Join(dir, fmt.Sprintf("seed%d-%s-step%d.sql", m.Seed, m.Mode, m.Step))
	var b strings.Builder
	fmt.Fprintf(&b, "-- vdb-mysql oracle regression: %s\n-- seed: %d\n-- mode: %s\n", m.Signature, m.Seed, m.Mode)
	for _, s := range Rename(m.Minimal, m.Prefix, "{p}") {
		fmt.Fprintf(&b, "%s%s;\n", kindTag(s), strings.ReplaceAll(s.SQL, "\n", " "))
	}
	if err := os.WriteFile(name, []byte(b.String()), 0o644); err != nil {
		return ""
	}
	return name
}

// Replay runs a regression file: it seeds fresh tables for the file's seed
// and runs its steps. It returns a mismatch, or nil when the case passes.
func Replay(env Env, file, run string) (*Mismatch, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var seed int64
	mode := "single"
	var steps []Step
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "-- seed: "):
			seed, _ = strconv.ParseInt(strings.TrimPrefix(line, "-- seed: "), 10, 64)
		case strings.HasPrefix(line, "-- mode: "):
			mode = strings.TrimPrefix(line, "-- mode: ")
		case line == "" || (strings.HasPrefix(line, "--") && !strings.HasPrefix(line, "-- source: ") && !strings.HasPrefix(line, "-- prepared: ")):
		default:
			st := Step{Kind: Read}
			if strings.HasPrefix(line, "-- source: ") {
				line = strings.TrimPrefix(line, "-- source: ")
				st.Kind = SourceWrite
				if strings.HasPrefix(strings.ToUpper(line), "ALTER") {
					st.Kind = SourceDDL
				}
			} else if strings.HasPrefix(line, "-- prepared: ") {
				line = strings.TrimPrefix(line, "-- prepared: ")
				st.Prepared = true
			}
			st.SQL = strings.TrimSuffix(line, ";")
			if st.Kind == Read {
				st.Kind = classify(st.SQL)
				st.Ordered = strings.Contains(strings.ToUpper(st.SQL), "ORDER BY")
			}
			steps = append(steps, st)
		}
	}
	prefix := fmt.Sprintf("orc_%s_r%d", run, rand.Intn(1<<20))
	s := NewSchema(prefix, seed)
	if err := Seed(env, s); err != nil {
		return nil, err
	}
	steps = Rename(steps, "{p}", prefix)
	for i := range steps {
		if steps[i].Kind == Write {
			steps[i].Tables = s.Tables()
		}
	}
	i, v, r, bad, err := execSteps(env, s, steps)
	if err != nil || !bad {
		return nil, err
	}
	return &Mismatch{Seed: seed, Mode: mode, Step: i, SQL: steps[i].SQL, What: What(v, r), VDB: v, Ref: r, File: file}, nil
}

func classify(q string) Kind {
	u := strings.ToUpper(strings.TrimSpace(q))
	switch {
	case strings.HasPrefix(u, "SELECT"), strings.HasPrefix(u, "WITH"):
		return Read
	case strings.HasPrefix(u, "ALTER"):
		return DDL
	case strings.HasPrefix(u, "BEGIN"), strings.HasPrefix(u, "START"), strings.HasPrefix(u, "COMMIT"),
		strings.HasPrefix(u, "ROLLBACK"), strings.HasPrefix(u, "SAVEPOINT"):
		return Tx
	}
	return Write
}

// detail shows the first rows that differ: of the result, or, when a write
// left different table contents, of the tables it changed.
func detail(env Env, s *Schema, st Step, v, r Result) string {
	var b strings.Builder
	diffRows := func(label string, a, c []string) {
		n := 0
		for i := 0; i < len(a) || i < len(c); i++ {
			var x, y string
			if i < len(a) {
				x = a[i]
			}
			if i < len(c) {
				y = c[i]
			}
			if x != y {
				fmt.Fprintf(&b, "  %s row %d\n    vdb:       %s\n    reference: %s\n", label, i, x, y)
				if n++; n == 3 {
					return
				}
			}
		}
	}
	switch What(v, r) {
	case "rows":
		diffRows("result", v.Rows, r.Rows)
	case "columns":
		fmt.Fprintf(&b, "  columns\n    vdb:       %v\n    reference: %v\n", v.Columns, r.Columns)
	case "table contents":
		for _, t := range st.Tables {
			q := "SELECT * FROM " + t + " ORDER BY 1, 2"
			a := dump(env.VDB, s, q)
			c := dump(env.Reference, s, q)
			diffRows(t, a, c)
		}
	}
	return b.String()
}

func dump(db *sql.DB, s *Schema, q string) []string {
	conn, err := db.Conn(bg())
	if err != nil {
		return nil
	}
	defer conn.Close()
	for _, x := range s.Settings() {
		_, _ = conn.ExecContext(bg(), x)
	}
	return run(conn, Step{SQL: q, Kind: Read, Ordered: true}, nil).Rows
}

// baseSettings are the session settings the Source and the reference are
// written with. Connections return to their pool with whatever a session set
// (a seed's time_zone and sql_mode), so every borrowed connection sets these
// first: TIMESTAMP literals then mean the same instant on both servers.
var baseSettings = []string{"SET SESSION time_zone = '+00:00'", "SET SESSION sql_mode = DEFAULT"}

func onConn(db *sql.DB, fn func(*sql.Conn) error) error {
	c, err := db.Conn(bg())
	if err != nil {
		return err
	}
	defer c.Close()
	for _, q := range baseSettings {
		if _, err := c.ExecContext(bg(), q); err != nil {
			return err
		}
	}
	return fn(c)
}
