// Package oracle compares VDB with InnoDB (implementation plan doc 15 §3,
// product doc 16 §1): the same tables are seeded on the Source and on the
// reference (the same mysqld build without vdb.so), the same generated steps
// run on VDB and on the reference, and every result, error, affected-row
// count and warning count is compared. A mismatch is shrunk to a minimal step
// list by delta debugging and reported.
//
// Tables are named per seed (orc_<seed>_<n>), so every run starts from an
// empty Delta without restarting VDB.
package oracle

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Step is one statement of a generated case.
type Step struct {
	SQL   string
	Write bool
}

// Result is one statement's outcome.
type Result struct {
	Rows     []string
	Ordered  bool
	Affected int64
	ErrCode  uint16
	Err      string
}

// Mismatch is one difference between VDB and the reference.
type Mismatch struct {
	Seed    int64
	Step    int
	SQL     string
	VDB     Result
	Ref     Result
	Minimal []Step
}

// Env holds the three connections.
type Env struct {
	Source, VDB, Reference *sql.DB
	// WaitTables blocks until VDB sees the tables just seeded on the Source.
	WaitTables func(prefix string) error
}

// Report summarises a run.
type Report struct {
	Seeds, Steps int
	Mismatches   []Mismatch
}

func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "oracle: %d seeds, %d steps, %d mismatches\n", r.Seeds, r.Steps, len(r.Mismatches))
	for _, m := range r.Mismatches {
		fmt.Fprintf(&b, "\nseed %d step %d: %s\n  vdb:       %s\n  reference: %s\n", m.Seed, m.Step, m.SQL, m.VDB.short(), m.Ref.short())
		if len(m.Minimal) > 0 {
			fmt.Fprintf(&b, "  minimal case (%d steps):\n", len(m.Minimal))
			for _, s := range m.Minimal {
				fmt.Fprintf(&b, "    %s;\n", s.SQL)
			}
		}
	}
	return b.String()
}

func (r Result) short() string {
	if r.Err != "" {
		return fmt.Sprintf("error %d %s", r.ErrCode, r.Err)
	}
	s := fmt.Sprintf("%d rows, %d affected", len(r.Rows), r.Affected)
	if len(r.Rows) > 0 && len(r.Rows) <= 5 {
		s += " " + strings.Join(r.Rows, " | ")
	}
	return s
}

// Run runs the single mode over seeds, steps statements each.
func Run(env Env, seeds []int64, steps int) (*Report, error) {
	rep := &Report{}
	for _, seed := range seeds {
		rep.Seeds++
		prefix := prefixFor(seed)
		if err := Seed(env, prefix, seed); err != nil {
			return rep, fmt.Errorf("oracle: seed %d: %w", seed, err)
		}
		case_ := Generate(prefix, seed, steps)
		rep.Steps += len(case_)
		if i, v, r, bad := Exec(env, case_); bad {
			m := Mismatch{Seed: seed, Step: i, SQL: case_[i].SQL, VDB: v, Ref: r}
			m.Minimal = Shrink(env, seed, case_[:i+1])
			rep.Mismatches = append(rep.Mismatches, m)
		}
	}
	return rep, nil
}

// Seed creates and fills the tables of one seed on the Source and the reference.
func Seed(env Env, prefix string, seed int64) error {
	stmts := []string{
		fmt.Sprintf("CREATE TABLE %s_a (id INT NOT NULL PRIMARY KEY, k INT NOT NULL, s VARCHAR(24) COLLATE utf8mb4_bin, n DECIMAL(10,2), t DATETIME(3), u INT, KEY k (k), KEY s (s), UNIQUE KEY u (u)) ENGINE=InnoDB", prefix),
		fmt.Sprintf("CREATE TABLE %s_b (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, a_id INT, v INT, KEY a_id (a_id)) ENGINE=InnoDB", prefix),
		fmt.Sprintf("CREATE TABLE %s_c (x INT, y VARCHAR(8)) ENGINE=InnoDB", prefix),
	}
	r := rand.New(rand.NewSource(seed))
	for i := 1; i <= 200; i++ {
		u := "NULL"
		if i%3 == 0 {
			u = fmt.Sprint(i * 7)
		}
		stmts = append(stmts, fmt.Sprintf("INSERT INTO %s_a VALUES (%d, %d, %s, %d.%02d, '2026-01-%02d 10:00:%02d.%03d', %s)",
			prefix, i*2, r.Intn(20), str(r), r.Intn(1000), r.Intn(100), 1+r.Intn(28), r.Intn(60), r.Intn(1000), u))
	}
	for i := 1; i <= 150; i++ {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO %s_b (a_id, v) VALUES (%d, %d)", prefix, 2*(1+r.Intn(220)), r.Intn(50)))
	}
	for i := 0; i < 40; i++ {
		x := r.Intn(5)
		stmts = append(stmts, fmt.Sprintf("INSERT INTO %s_c VALUES (%d, 'v%d')", prefix, x, x))
	}
	drop := fmt.Sprintf("DROP TABLE IF EXISTS %s_a, %s_b, %s_c", prefix, prefix, prefix)
	stmts = append([]string{drop}, stmts...)
	for _, db := range []*sql.DB{env.Source, env.Reference} {
		for _, s := range stmts {
			if _, err := db.Exec(s); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
		}
	}
	if env.WaitTables != nil {
		return env.WaitTables(prefix)
	}
	return nil
}

func str(r *rand.Rand) string {
	words := []string{"alpha", "beta", "Gamma", "delta", "ALPHA", "x", "", "zeta ", "émile", "Ünder"}
	return "'" + words[r.Intn(len(words))] + fmt.Sprint(r.Intn(4)) + "'"
}

// Generate builds a deterministic step list: writes that build Delta states
// (Source rows updated and deleted, Delta rows inserted and changed, unique
// collisions, keyless rows) and reads that exercise every read shape.
func Generate(prefix string, seed int64, n int) []Step {
	r := rand.New(rand.NewSource(seed * 7919))
	a, b, c := prefix+"_a", prefix+"_b", prefix+"_c"
	pred := func() string {
		switch r.Intn(6) {
		case 0:
			return fmt.Sprintf("k = %d", r.Intn(20))
		case 1:
			return fmt.Sprintf("id BETWEEN %d AND %d", r.Intn(200), 200+r.Intn(250))
		case 2:
			return fmt.Sprintf("s LIKE '%c%%'", "abGdAxzéÜ"[r.Intn(9)])
		case 3:
			return fmt.Sprintf("n > %d", r.Intn(1000))
		case 4:
			return "u IS NULL"
		}
		return fmt.Sprintf("id IN (%d, %d, %d)", 2*r.Intn(220), 2*r.Intn(220), 2*r.Intn(220))
	}
	writes := []func() string{
		func() string {
			return fmt.Sprintf("INSERT INTO %s VALUES (%d, %d, %s, %d.5, '2026-02-01 00:00:00.000', %d)", a, 1001+r.Intn(500), r.Intn(20), str(r), r.Intn(900), 3*r.Intn(400))
		},
		func() string { return fmt.Sprintf("UPDATE %s SET k = k + 1, s = %s WHERE %s", a, str(r), pred()) },
		func() string { return fmt.Sprintf("DELETE FROM %s WHERE %s", a, pred()) },
		func() string { return fmt.Sprintf("UPDATE %s SET id = id + 1 WHERE id = %d", a, 2*r.Intn(220)) },
		// Explicit ids: VDB's auto-increment starts after a gap (D-15), InnoDB's does not.
		func() string {
			return fmt.Sprintf("INSERT INTO %s (id, a_id, v) VALUES (%d, %d, %d)", b, 5000+r.Intn(300), 2*r.Intn(220), r.Intn(50))
		},
		func() string { return fmt.Sprintf("DELETE FROM %s WHERE v = %d ORDER BY id LIMIT 2", b, r.Intn(50)) },
		// Rows with the same x are identical, so LIMIT 1 picks an equal row on both sides.
		func() string {
			x := r.Intn(5)
			return fmt.Sprintf("UPDATE %s SET x = x + 10, y = 'w%d' WHERE x = %d LIMIT 1", c, x, x)
		},
		func() string { return fmt.Sprintf("DELETE FROM %s WHERE x = %d LIMIT 1", c, r.Intn(5)) },
		func() string { x := r.Intn(5); return fmt.Sprintf("INSERT INTO %s VALUES (%d, 'v%d')", c, x, x) },
		func() string {
			return fmt.Sprintf("UPDATE %s SET u = %d WHERE id = %d", a, 7*r.Intn(200), 2*r.Intn(220))
		},
	}
	reads := []func() string{
		func() string { return fmt.Sprintf("SELECT * FROM %s WHERE %s", a, pred()) },
		func() string {
			return fmt.Sprintf("SELECT id, k FROM %s WHERE %s ORDER BY id LIMIT %d", a, pred(), 1+r.Intn(20))
		},
		func() string { return fmt.Sprintf("SELECT id FROM %s ORDER BY id DESC LIMIT %d", a, 1+r.Intn(30)) },
		func() string { return fmt.Sprintf("SELECT k, COUNT(*), SUM(n) FROM %s GROUP BY k", a) },
		func() string { return fmt.Sprintf("SELECT COUNT(*) FROM %s", a) },
		func() string { return fmt.Sprintf("SELECT MIN(id), MAX(id) FROM %s", a) },
		func() string { return fmt.Sprintf("SELECT s FROM %s WHERE s >= %s ORDER BY s, id LIMIT 10", a, str(r)) },
		func() string {
			return fmt.Sprintf("SELECT %s.id, %s.v FROM %s JOIN %s ON %s.a_id = %s.id WHERE %s.k < %d", a, b, a, b, b, a, a, r.Intn(20))
		},
		func() string { return fmt.Sprintf("SELECT * FROM %s WHERE u = %d", a, 7*r.Intn(200)) },
		func() string { return fmt.Sprintf("SELECT x, y, COUNT(*) FROM %s GROUP BY x, y", c) },
		func() string { return fmt.Sprintf("SELECT COUNT(*) FROM %s", c) },
		func() string { return fmt.Sprintf("SELECT * FROM %s WHERE id = %d", a, 2*r.Intn(220)) },
	}
	var out []Step
	for i := 0; i < n; i++ {
		if r.Intn(3) == 0 {
			out = append(out, Step{SQL: writes[r.Intn(len(writes))](), Write: true})
		} else {
			out = append(out, Step{SQL: reads[r.Intn(len(reads))]()})
		}
	}
	return out
}

// Exec runs every step on VDB and on the reference; it stops at the first
// difference and returns its index.
func Exec(env Env, steps []Step) (int, Result, Result, bool) {
	for i, s := range steps {
		v := run(env.VDB, s)
		r := run(env.Reference, s)
		if !Equal(v, r) {
			return i, v, r, true
		}
	}
	return 0, Result{}, Result{}, false
}

func run(db *sql.DB, s Step) Result {
	if s.Write {
		res, err := db.Exec(s.SQL)
		if err != nil {
			return errResult(err)
		}
		n, _ := res.RowsAffected()
		return Result{Affected: n}
	}
	rows, err := db.Query(s.SQL)
	if err != nil {
		return errResult(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return errResult(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "NULL"
			} else {
				parts[i] = string(v)
			}
		}
		out = append(out, strings.Join(parts, ","))
	}
	if err := rows.Err(); err != nil {
		return errResult(err)
	}
	ordered := strings.Contains(strings.ToUpper(s.SQL), "ORDER BY")
	if !ordered {
		sort.Strings(out)
	}
	return Result{Rows: out, Ordered: ordered}
}

func errResult(err error) Result {
	var code uint16
	var msg = err.Error()
	if i := strings.Index(msg, "Error "); i >= 0 {
		fmt.Sscanf(msg[i+6:], "%d", &code)
	}
	return Result{ErrCode: code, Err: msg}
}

// Equal compares two results: rows (sorted unless ordered), affected rows, error codes.
func Equal(a, b Result) bool {
	if a.ErrCode != b.ErrCode || (a.Err == "") != (b.Err == "") {
		return false
	}
	if a.Affected != b.Affected || len(a.Rows) != len(b.Rows) {
		return false
	}
	for i := range a.Rows {
		if a.Rows[i] != b.Rows[i] {
			return false
		}
	}
	return true
}

// Shrink removes write steps that are not needed for the failure, by delta
// debugging over fresh tables (one new seed prefix per attempt).
func Shrink(env Env, seed int64, steps []Step) []Step {
	if len(steps) == 0 {
		return steps
	}
	last := steps[len(steps)-1]
	writes := []Step{}
	for _, s := range steps[:len(steps)-1] {
		if s.Write {
			writes = append(writes, s)
		}
	}
	attempt := int64(0)
	fails := func(ws []Step) bool {
		attempt++
		prefix := prefixFor(seed)
		fresh := fmt.Sprintf("%s_s%d", prefix, attempt)
		if err := Seed(env, fresh, seed); err != nil {
			return false
		}
		rename := func(s Step) Step { return Step{SQL: strings.ReplaceAll(s.SQL, prefix+"_", fresh+"_"), Write: s.Write} }
		var cs []Step
		for _, w := range ws {
			cs = append(cs, rename(w))
		}
		cs = append(cs, rename(last))
		_, _, _, bad := Exec(env, cs)
		return bad
	}
	for chunk := len(writes) / 2; chunk >= 1 && attempt < 40; chunk /= 2 {
		for i := 0; i+chunk <= len(writes) && attempt < 40; {
			try := append(append([]Step{}, writes[:i]...), writes[i+chunk:]...)
			if fails(try) {
				writes = try
			} else {
				i += chunk
			}
		}
	}
	return append(writes, last)
}

// runID keeps each run's tables apart: VDB keeps the Delta of a Source table
// that is dropped and re-created between two schema polls, so a run must
// never reuse an earlier run's table names.
var runID = fmt.Sprintf("%x", time.Now().Unix()%0xfffff)

func prefixFor(seed int64) string { return fmt.Sprintf("orc_%s_%d", runID, seed) }
