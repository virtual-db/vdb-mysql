// Package oracle — gen.go
//
// The generator (plan doc 15 §3, Generator): a deterministic list of steps
// for one seed. Writes build every Delta state VDB has (Source rows updated
// and deleted, Delta rows inserted and changed, primary-key changes,
// unique-key and foreign-key collisions, cascades, keyless duplicates,
// multi-table writes, INSERT ... SELECT, REPLACE, ON DUPLICATE KEY UPDATE,
// TRUNCATE) inside autocommit statements and explicit transactions with
// savepoints; reads exercise every read shape (point, range, ordered and
// paged scans, aggregates, DISTINCT, HAVING, joins of every kind,
// subqueries, UNION, CTEs, window functions, JSON); DDL adds and drops
// columns and indexes. In Source-writes mode the Source changes _s rows and
// its definition between steps.
package oracle

import (
	"fmt"
	"math/rand"
	"strings"
)

// Kind classifies a step.
type Kind uint8

const (
	Read Kind = iota
	Write
	DDL
	Tx          // BEGIN, COMMIT, ROLLBACK, SAVEPOINT, ROLLBACK TO
	SourceWrite // run on the Source and the reference, never on VDB
	SourceDDL   // the same, then wait until VDB shows the change
)

// Step is one statement of a generated case.
type Step struct {
	SQL      string
	Kind     Kind
	Ordered  bool     // the result order is fully determined by the statement
	Tables   []string // tables a write changes, for the checksum after it
	Prepared bool     // run as a server-side prepared statement
	Session  int      // concurrency mode: which session runs it
	Write    bool     // kept for callers of the first oracle: Kind == Write
}

// Options select the oracle's modes (plan doc 15 §3, Modes).
type Options struct {
	Steps        int
	SourceWrites bool    // Source writes and Source DDL between steps
	Prepared     float64 // share of steps run as prepared statements
	Transactions bool    // explicit transactions and savepoints in single mode
}

type gen struct {
	r     *rand.Rand
	s     *Schema
	o     Options
	inTx  bool
	sp    int
	cols  int // columns added to _a by DDL
	idx   bool
	scols int // columns added to _s by Source DDL
	sNext int
}

// Generate builds a seed's steps in single mode.
func Generate(s *Schema, seed int64, o Options) []Step {
	g := &gen{r: rand.New(rand.NewSource(seed * 7919)), s: s, o: o, sNext: s.RowsS + 1}
	var out []Step
	for len(out) < o.Steps {
		out = append(out, g.next()...)
	}
	if g.inTx {
		out = append(out, Step{SQL: "COMMIT", Kind: Tx})
	}
	for i := range out {
		out[i].Write = out[i].Kind == Write
		if out[i].Kind == Read || out[i].Kind == Write {
			out[i].Prepared = g.r.Float64() < o.Prepared
		}
	}
	return out
}

func (g *gen) next() []Step {
	switch x := g.r.Intn(100); {
	case g.o.Transactions && x < 6:
		return g.tx()
	case g.o.SourceWrites && x < 12:
		return []Step{g.sourceWrite()}
	case x < 14:
		return g.ddl()
	case x < 47:
		return []Step{g.write()}
	default:
		return []Step{g.read()}
	}
}

func (g *gen) tx() []Step {
	switch {
	case !g.inTx:
		g.inTx = true
		g.sp = 0
		return []Step{{SQL: []string{"BEGIN", "START TRANSACTION", "START TRANSACTION WITH CONSISTENT SNAPSHOT"}[g.r.Intn(3)], Kind: Tx}}
	case g.sp == 0 && g.r.Intn(2) == 0:
		g.sp = 1
		return []Step{{SQL: "SAVEPOINT sp1", Kind: Tx}}
	case g.sp == 1 && g.r.Intn(3) == 0:
		g.sp = 0
		return []Step{{SQL: "ROLLBACK TO SAVEPOINT sp1", Kind: Tx}}
	default:
		g.inTx = false
		return []Step{{SQL: []string{"COMMIT", "COMMIT", "ROLLBACK"}[g.r.Intn(3)], Kind: Tx}}
	}
}

func (g *gen) a() string  { return g.s.T("a") }
func (g *gen) b() string  { return g.s.T("b") }
func (g *gen) c() string  { return g.s.T("c") }
func (g *gen) d() string  { return g.s.T("d") }
func (g *gen) sT() string { return g.s.T("s") }

// aid is an id of _a: mostly an existing (even) one, sometimes a new one.
func (g *gen) aid() int {
	if g.r.Intn(5) == 0 {
		return 1 + 2*g.r.Intn(g.s.RowsA+50)
	}
	return 2 * (1 + g.r.Intn(g.s.RowsA))
}

func (g *gen) pred() string {
	switch g.r.Intn(9) {
	case 0:
		return fmt.Sprintf("k = %d", g.r.Intn(20))
	case 1:
		lo := g.r.Intn(2 * g.s.RowsA)
		return fmt.Sprintf("id BETWEEN %d AND %d", lo, lo+g.r.Intn(120))
	case 2:
		return fmt.Sprintf("s LIKE '%c%%'", "abGdAxzéÜ"[g.r.Intn(9)])
	case 3:
		return fmt.Sprintf("n > %d", g.r.Intn(1000))
	case 4:
		return "u IS NULL AND k < " + fmt.Sprint(g.r.Intn(20))
	case 5:
		return fmt.Sprintf("ci = %s", str(g.r))
	case 6:
		return fmt.Sprintf("e = '%c' AND k > %d", "xyz"[g.r.Intn(3)], g.r.Intn(20))
	case 7:
		return fmt.Sprintf("t < '2026-01-%02d'", 1+g.r.Intn(28))
	}
	return fmt.Sprintf("id IN (%d, %d, %d)", g.aid(), g.aid(), g.aid())
}

func (g *gen) aRow(id int) string {
	u := "NULL"
	if g.r.Intn(2) == 0 {
		u = fmt.Sprint(7 * g.r.Intn(g.s.RowsA+40))
	}
	return fmt.Sprintf("(%d, %d, %s, %s, 'm%d', %d.50, '2026-02-%02d 00:00:00.000', '2026-04-01 08:00:00', %s, '%c', '{\"v\": %d}', %d.5)",
		id, g.r.Intn(20), str(g.r), str(g.r), g.r.Intn(5), g.r.Intn(900), 1+g.r.Intn(27), u, "xyz"[g.r.Intn(3)], g.r.Intn(50), g.r.Intn(90))
}

const aCols = "(id, k, s, ci, l, n, t, ts, u, e, j, f)"

func (g *gen) write() Step {
	a, b, c, d := g.a(), g.b(), g.c(), g.d()
	w := func(sql string, tables ...string) Step { return Step{SQL: sql, Kind: Write, Tables: tables} }
	switch g.r.Intn(22) {
	case 0:
		return w(fmt.Sprintf("INSERT INTO %s %s VALUES %s", a, aCols, g.aRow(g.aid())), a)
	case 1:
		return w(fmt.Sprintf("INSERT INTO %s %s VALUES %s, %s, %s", a, aCols, g.aRow(g.aid()), g.aRow(g.aid()), g.aRow(g.aid())), a)
	case 2:
		return w(fmt.Sprintf("INSERT IGNORE INTO %s %s VALUES %s, %s", a, aCols, g.aRow(g.aid()), g.aRow(g.aid())), a)
	case 3:
		return w(fmt.Sprintf("INSERT INTO %s %s VALUES %s AS nw ON DUPLICATE KEY UPDATE k = %s.k + 1, s = nw.s", a, aCols, g.aRow(g.aid()), a), a, b)
	case 4:
		return w(fmt.Sprintf("REPLACE INTO %s %s VALUES %s", a, aCols, g.aRow(g.aid())), a, b)
	case 5:
		return w(fmt.Sprintf("UPDATE %s SET k = k + 1, s = %s WHERE %s", a, str(g.r), g.pred()), a)
	case 6:
		return w(fmt.Sprintf("UPDATE %s SET n = n + 1.25, ci = %s WHERE %s ORDER BY id LIMIT %d", a, str(g.r), g.pred(), 1+g.r.Intn(6)), a)
	case 7:
		return w(fmt.Sprintf("UPDATE %s SET id = id + 1 WHERE id = %d", a, g.aid()), a, b)
	case 8:
		return w(fmt.Sprintf("DELETE FROM %s WHERE %s", a, g.pred()), a, b)
	case 9:
		return w(fmt.Sprintf("UPDATE %s SET u = %d WHERE id = %d", a, 7*g.r.Intn(g.s.RowsA+40), g.aid()), a)
	case 10:
		return w(fmt.Sprintf("DELETE FROM %s WHERE v = %d ORDER BY id LIMIT 2", b, g.r.Intn(50)), b)
	case 11:
		aid := "NULL"
		if g.r.Intn(5) != 0 {
			aid = fmt.Sprint(g.aid())
		}
		return w(fmt.Sprintf("INSERT INTO %s (id, a_id, v, w) VALUES (%d, %s, %d, 'n%d')", b, 100000+g.r.Intn(400), aid, g.r.Intn(50), g.r.Intn(7)), b)
	case 12:
		return w(fmt.Sprintf("UPDATE %s SET a_id = %d WHERE id = %d", b, g.aid(), 1+g.r.Intn(g.s.RowsB)), b)
	case 13:
		return w(fmt.Sprintf("UPDATE %s JOIN %s ON %s.a_id = %s.id SET %s.v = %s.v + 1 WHERE %s.k < %d", a, b, b, a, b, b, a, g.r.Intn(8)), b)
	case 14:
		return w(fmt.Sprintf("DELETE %s FROM %s JOIN %s ON %s.a_id = %s.id WHERE %s.k = %d", b, b, a, b, a, a, g.r.Intn(20)), b)
	case 15:
		x := g.r.Intn(6)
		return w(fmt.Sprintf("INSERT INTO %s VALUES (%d, 'v%d'), (%d, 'v%d')", c, x, x, x, x), c)
	case 16:
		x := g.r.Intn(6)
		return w(fmt.Sprintf("UPDATE %s SET x = x + 10, y = 'w%d' WHERE x = %d LIMIT 1", c, x, x), c)
	case 17:
		return w(fmt.Sprintf("DELETE FROM %s WHERE x = %d LIMIT 1", c, g.r.Intn(6)), c)
	case 18:
		gg := []string{"abc", "abd", "ABC", "b", "zz", "new"}[g.r.Intn(6)]
		return w(fmt.Sprintf("INSERT INTO %s VALUES ('%s', %d, 'n%d') ON DUPLICATE KEY UPDATE val = CONCAT(val, '+')", d, gg, uint64(g.r.Intn(40))*1000003, g.r.Intn(9)), d)
	case 19:
		return w(fmt.Sprintf("UPDATE %s SET h = h + 1 WHERE g = '%s' AND h < %d", d, []string{"abc", "b", "zz"}[g.r.Intn(3)], uint64(g.r.Intn(20))*1000003), d)
	case 20:
		return w(fmt.Sprintf("INSERT INTO %s %s SELECT id + 100001, k, s, ci, l, n, t, ts, NULL, e, j, f FROM %s WHERE %s ORDER BY id LIMIT 5", a, aCols, a, g.pred()), a)
	}
	if g.r.Intn(4) == 0 {
		return w("TRUNCATE TABLE "+c, c)
	}
	return w(fmt.Sprintf("DELETE FROM %s WHERE g = '%s' AND h = %d", d, []string{"abc", "abd", "b"}[g.r.Intn(3)], uint64(g.r.Intn(30))*1000003), d)
}

func (g *gen) ddl() []Step {
	a := g.a()
	if g.inTx {
		return []Step{g.read()}
	}
	switch g.r.Intn(4) {
	case 0:
		if g.cols < 3 {
			g.cols++
			return []Step{{SQL: fmt.Sprintf("ALTER TABLE %s ADD COLUMN extra%d INT NULL DEFAULT %d", a, g.cols, g.r.Intn(9)), Kind: DDL, Tables: []string{a}}}
		}
	case 1:
		if !g.idx {
			g.idx = true
			return []Step{{SQL: fmt.Sprintf("ALTER TABLE %s ADD INDEX ix_n (n)", a), Kind: DDL}}
		}
		g.idx = false
		return []Step{{SQL: fmt.Sprintf("ALTER TABLE %s DROP INDEX ix_n", a), Kind: DDL}}
	case 2:
		if g.cols > 0 {
			col := g.cols
			g.cols--
			return []Step{{SQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN extra%d", a, col), Kind: DDL}}
		}
	}
	if g.o.SourceWrites && g.scols < 2 {
		g.scols++
		return []Step{{SQL: fmt.Sprintf("ALTER TABLE %s ADD COLUMN z%d INT NOT NULL DEFAULT %d", g.sT(), g.scols, 3+g.scols), Kind: SourceDDL}}
	}
	return []Step{g.read()}
}

func (g *gen) sourceWrite() Step {
	t := g.sT()
	switch g.r.Intn(4) {
	case 0:
		g.sNext++
		return Step{SQL: fmt.Sprintf("INSERT INTO %s (id, q, r) VALUES (%d, %d, 'src%d')", t, g.sNext, g.r.Intn(15), g.r.Intn(9)), Kind: SourceWrite}
	case 1:
		return Step{SQL: fmt.Sprintf("UPDATE %s SET q = q + 1, r = 'u%d' WHERE q = %d", t, g.r.Intn(9), g.r.Intn(15)), Kind: SourceWrite}
	case 2:
		return Step{SQL: fmt.Sprintf("DELETE FROM %s WHERE id = %d", t, 1+g.r.Intn(g.sNext)), Kind: SourceWrite}
	}
	return Step{SQL: fmt.Sprintf("UPDATE %s SET id = id + 1000 WHERE id = %d", t, 1+g.r.Intn(g.sNext)), Kind: SourceWrite}
}

func (g *gen) read() Step {
	a, b, c, d, s := g.a(), g.b(), g.c(), g.d(), g.sT()
	q := func(sql string, ordered bool) Step { return Step{SQL: sql, Kind: Read, Ordered: ordered} }
	lock := ""
	if g.inTx && g.r.Intn(4) == 0 {
		lock = []string{" FOR UPDATE", " FOR SHARE"}[g.r.Intn(2)]
	}
	switch g.r.Intn(30) {
	case 0:
		return q(fmt.Sprintf("SELECT * FROM %s WHERE %s ORDER BY id%s", a, g.pred(), lock), true)
	case 1:
		return q(fmt.Sprintf("SELECT id, k FROM %s WHERE %s ORDER BY id LIMIT %d OFFSET %d", a, g.pred(), 1+g.r.Intn(20), g.r.Intn(10)), true)
	case 2:
		return q(fmt.Sprintf("SELECT id FROM %s ORDER BY id DESC LIMIT %d", a, 1+g.r.Intn(40)), true)
	case 3:
		return q(fmt.Sprintf("SELECT k, COUNT(*), SUM(n), MIN(s), MAX(t) FROM %s GROUP BY k HAVING COUNT(*) > %d ORDER BY k", a, g.r.Intn(3)), true)
	case 4:
		return q("SELECT COUNT(*) FROM "+[]string{a, b, c, d, s}[g.r.Intn(5)], false)
	case 5:
		return q(fmt.Sprintf("SELECT MIN(id), MAX(id), COUNT(u), COUNT(DISTINCT k) FROM %s", a), false)
	case 6:
		return q(fmt.Sprintf("SELECT s, id FROM %s WHERE s >= %s ORDER BY s, id LIMIT 15", a, str(g.r)), true)
	case 7:
		return q(fmt.Sprintf("SELECT %s.id, %s.v FROM %s JOIN %s ON %s.a_id = %s.id WHERE %s.k < %d", a, b, a, b, b, a, a, g.r.Intn(20)), false)
	case 8:
		return q(fmt.Sprintf("SELECT %s.id, %s.id FROM %s LEFT JOIN %s ON %s.a_id = %s.id WHERE %s.id < %d", a, b, a, b, b, a, a, g.r.Intn(200)), false)
	case 9:
		return q(fmt.Sprintf("SELECT %s.id, %s.a_id FROM %s RIGHT JOIN %s ON %s.a_id = %s.id WHERE %s.v < %d", a, b, a, b, b, a, b, g.r.Intn(20)), false)
	case 10:
		return q(fmt.Sprintf("SELECT x.id, y.id FROM %s x JOIN %s y ON x.k = y.k AND x.id < y.id WHERE x.id < %d AND y.id < %d", a, a, 40+g.r.Intn(60), 40+g.r.Intn(60)), false)
	case 11:
		return q(fmt.Sprintf("SELECT id FROM %s WHERE id IN (SELECT a_id FROM %s WHERE v > %d) ORDER BY id", a, b, g.r.Intn(50)), true)
	case 12:
		return q(fmt.Sprintf("SELECT id FROM %s WHERE EXISTS (SELECT 1 FROM %s WHERE %s.a_id = %s.id AND %s.v = %d) ORDER BY id", a, b, b, a, b, g.r.Intn(50)), true)
	case 13:
		return q(fmt.Sprintf("SELECT id, (SELECT MAX(v) FROM %s WHERE %s.a_id = %s.id) FROM %s WHERE %s ORDER BY id", b, b, a, a, g.pred()), true)
	case 14:
		return q(fmt.Sprintf("SELECT dk, cnt FROM (SELECT k AS dk, COUNT(*) AS cnt FROM %s GROUP BY k) t WHERE cnt > %d ORDER BY dk", a, g.r.Intn(30)), true)
	case 15:
		return q(fmt.Sprintf("SELECT id FROM %s WHERE k = %d UNION SELECT a_id FROM %s WHERE v = %d AND a_id IS NOT NULL ORDER BY 1", a, g.r.Intn(20), b, g.r.Intn(50)), true)
	case 16:
		return q(fmt.Sprintf("SELECT x, y FROM %s UNION ALL SELECT k, s FROM %s WHERE id < %d", c, a, g.r.Intn(60)), false)
	case 17:
		return q(fmt.Sprintf("WITH t AS (SELECT id, k FROM %s WHERE %s) SELECT k, COUNT(*) FROM t GROUP BY k ORDER BY k", a, g.pred()), true)
	case 18:
		return q(fmt.Sprintf("SELECT id, k, ROW_NUMBER() OVER (PARTITION BY k ORDER BY id), SUM(n) OVER (PARTITION BY k ORDER BY id) FROM %s WHERE %s ORDER BY id", a, g.pred()), true)
	case 19:
		return q(fmt.Sprintf("SELECT * FROM %s WHERE u = %d%s", a, 7*g.r.Intn(g.s.RowsA), lock), false)
	case 20:
		return q(fmt.Sprintf("SELECT * FROM %s WHERE id = %d%s", a, g.aid(), lock), false)
	case 21:
		return q(fmt.Sprintf("SELECT LOWER(ci) AS lc, COUNT(*) FROM %s GROUP BY lc ORDER BY lc", a), true)
	case 22:
		return q(fmt.Sprintf("SELECT id FROM %s WHERE ci = %s ORDER BY id", a, str(g.r)), true)
	case 23:
		return q(fmt.Sprintf("SELECT x, y, COUNT(*) FROM %s GROUP BY x, y ORDER BY x, y", c), true)
	case 24:
		return q(fmt.Sprintf("SELECT * FROM %s WHERE g = '%s' AND h BETWEEN %d AND %d ORDER BY g, h", d, []string{"abc", "ABC", "b", "zz"}[g.r.Intn(4)], uint64(g.r.Intn(10))*1000003, uint64(10+g.r.Intn(20))*1000003), true)
	case 25:
		return q(fmt.Sprintf("SELECT g, COUNT(*), MAX(h) FROM %s GROUP BY g ORDER BY g", d), true)
	case 26:
		return q(fmt.Sprintf("SELECT id, JSON_EXTRACT(j, '$.v'), ts FROM %s WHERE %s ORDER BY id", a, g.pred()), true)
	case 27:
		return q(fmt.Sprintf("SELECT DISTINCT e, k FROM %s ORDER BY e, k", a), true)
	case 28:
		return q(fmt.Sprintf("SELECT * FROM %s WHERE q = %d ORDER BY id", s, g.r.Intn(15)), true)
	}
	return q(fmt.Sprintf("SELECT %s.id, %s.r FROM %s JOIN %s ON %s.q = %s.k WHERE %s.id < %d ORDER BY %s.id, %s.id", a, s, a, s, s, a, a, g.r.Intn(80), a, s), true)
}

// Rename rewrites a step list from one table prefix to another.
func Rename(steps []Step, from, to string) []Step {
	out := make([]Step, len(steps))
	for i, s := range steps {
		s.SQL = strings.ReplaceAll(s.SQL, from+"_", to+"_")
		ts := make([]string, len(s.Tables))
		for j, t := range s.Tables {
			ts[j] = strings.ReplaceAll(t, from+"_", to+"_")
		}
		s.Tables = ts
		out[i] = s
	}
	return out
}
