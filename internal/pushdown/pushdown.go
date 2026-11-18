// Package pushdown reduces a condition the engine pushed to the parts the
// Source may evaluate (implementation plan doc 10 §7, product doc 08 §7.3).
// The text is parsed with Vitess as `SELECT 1 FROM t WHERE <text>`; each
// top-level AND part is kept or dropped on its own. A part is kept only if
// every node is allowed for the Source's version, every column belongs to the
// table and comes from the Source, no column is FLOAT or DOUBLE, and every
// string literal kept its introducer. mysqld re-checks every row, so a dropped
// part costs rows shipped, never correctness.
package pushdown

import (
	_ "embed"
	"log/slog"
	"strings"
	"sync/atomic"

	core "github.com/virtual-db/vdb-core"
	"gopkg.in/yaml.v3"
	"vitess.io/vitess/go/vt/sqlparser"
)

var log = slog.With("pkg", "pushdown")

//go:embed allowlist.yaml
var allowlistYAML []byte

// Entry is one version's allowlist.
type Entry struct {
	Operators []string `yaml:"operators"`
	Functions []string `yaml:"functions"`
}

type allowlist struct {
	Versions map[string]Entry `yaml:"versions"`
}

// Pushdown reduces pushed conditions. Safe for concurrent use.
type Pushdown struct {
	parser    *sqlparser.Parser
	ops       map[string]bool
	funcs     map[string]bool
	off       atomic.Bool
	transform map[string]bool // "db.table.column" lower case
}

// New returns a Pushdown for a Source version.
func New(sourceVersion string, transforms []string) (*Pushdown, error) {
	p, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.4.0"})
	if err != nil {
		return nil, err
	}
	pd := &Pushdown{parser: p, ops: map[string]bool{}, funcs: map[string]bool{}, transform: map[string]bool{}}
	for _, op := range []string{"=", "<", ">", "<=", ">=", "!=", "<>", "<=>", "in", "not in", "like", "not like", "between", "is"} {
		pd.ops[op] = true
	}
	var al allowlist
	if err := yaml.Unmarshal(allowlistYAML, &al); err == nil {
		best := ""
		for v := range al.Versions {
			if strings.HasPrefix(sourceVersion, v) && len(v) > len(best) {
				best = v
			}
		}
		if best != "" {
			for _, o := range al.Versions[best].Operators {
				pd.ops[strings.ToLower(o)] = true
			}
			for _, f := range al.Versions[best].Functions {
				pd.funcs[strings.ToLower(f)] = true
			}
		}
	}
	for _, t := range transforms {
		pd.transform[strings.ToLower(t)] = true
	}
	return pd, nil
}

// Disable turns pushdown off (VDB_PUSHDOWN=off).
func (p *Pushdown) Disable() { p.off.Store(true) }

// Transformed reports whether a plugin transforms a column.
func (p *Pushdown) Transformed(db, table, col string) bool {
	return p.transform[strings.ToLower(db+"."+table+"."+col)]
}

// Reduce returns the SQL of the kept parts, or "" when nothing is kept.
// The second result is reserved for conditions proven false without the
// Source; it is always false today.
func (p *Pushdown) Reduce(d *core.TableDef, cond string) (string, bool) {
	if p.off.Load() || strings.TrimSpace(cond) == "" {
		return "", false
	}
	stmt, err := p.parser.Parse("SELECT 1 FROM t WHERE " + cond)
	if err != nil {
		log.Debug("pushed condition does not parse; nothing pushed", "cond", cond, "err", err)
		return "", false
	}
	sel, ok := stmt.(*sqlparser.Select)
	if !ok || sel.Where == nil {
		return "", false
	}
	var parts []sqlparser.Expr
	splitAnd(sel.Where.Expr, &parts)
	var kept []string
	for _, e := range parts {
		if p.allowed(d, e, false) {
			kept = append(kept, sqlparser.String(e))
		}
	}
	out := strings.Join(kept, " and ")
	log.Debug("pushdown", "cond", cond, "kept", out)
	return out, false
}

func splitAnd(e sqlparser.Expr, out *[]sqlparser.Expr) {
	if a, ok := e.(*sqlparser.AndExpr); ok {
		splitAnd(a.Left, out)
		splitAnd(a.Right, out)
		return
	}
	*out = append(*out, e)
}

func (p *Pushdown) column(d *core.TableDef, c *sqlparser.ColName) bool {
	if !c.Qualifier.IsEmpty() && !strings.EqualFold(c.Qualifier.Name.String(), d.Name) {
		return false
	}
	i := d.ColumnByName(c.Name.String())
	if i < 0 {
		return false
	}
	col := d.Columns[i]
	if col.Fetch != core.FromSource || col.Float || col.Timestamp || p.Transformed(d.Database, d.Name, col.Name) {
		return false
	}
	// Render the column unqualified.
	c.Qualifier = sqlparser.TableName{}
	return true
}

// allowed checks a node; introduced reports whether a string literal here
// sits under an introducer.
func (p *Pushdown) allowed(d *core.TableDef, e sqlparser.Expr, introduced bool) bool {
	switch n := e.(type) {
	case *sqlparser.AndExpr:
		return p.allowed(d, n.Left, false) && p.allowed(d, n.Right, false)
	case *sqlparser.OrExpr:
		return p.allowed(d, n.Left, false) && p.allowed(d, n.Right, false)
	case *sqlparser.NotExpr:
		return p.allowed(d, n.Expr, false)
	case *sqlparser.ComparisonExpr:
		if !p.ops[strings.ToLower(n.Operator.ToString())] {
			return false
		}
		if n.Escape != nil && !p.allowed(d, n.Escape, false) {
			return false
		}
		return p.allowed(d, n.Left, false) && p.allowed(d, n.Right, false)
	case *sqlparser.BetweenExpr:
		return p.ops["between"] && p.allowed(d, n.Left, false) && p.allowed(d, n.From, false) && p.allowed(d, n.To, false)
	case *sqlparser.IsExpr:
		return p.ops["is"] && p.allowed(d, n.Left, false)
	case *sqlparser.ColName:
		return p.column(d, n)
	case *sqlparser.Literal:
		switch n.Type {
		case sqlparser.StrVal:
			return introduced
		case sqlparser.FloatVal:
			return false
		}
		return true
	case *sqlparser.NullVal, sqlparser.BoolVal:
		return true
	case *sqlparser.IntroducerExpr:
		return p.allowed(d, n.Expr, true)
	case *sqlparser.CollateExpr:
		return p.allowed(d, n.Expr, introduced)
	case sqlparser.ValTuple:
		for _, x := range n {
			if !p.allowed(d, x, false) {
				return false
			}
		}
		return true
	case *sqlparser.UnaryExpr:
		return n.Operator == sqlparser.UMinusOp && p.allowed(d, n.Expr, false)
	case *sqlparser.BinaryExpr:
		return p.ops[strings.ToLower(n.Operator.ToString())] && p.allowed(d, n.Left, false) && p.allowed(d, n.Right, false)
	case *sqlparser.FuncExpr:
		if !p.funcs[n.Name.Lowered()] {
			return false
		}
		for _, x := range n.Exprs {
			if !p.allowed(d, x, false) {
				return false
			}
		}
		return true
	}
	return false
}
