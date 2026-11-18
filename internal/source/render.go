// Package source — render.go
//
// Every statement sent to the Source is built here from parts, never from a
// client statement (doc 10 §4.1, product doc 08 §5). One function per shape.
package source

import (
	"strconv"
	"strings"

	core "github.com/virtual-db/vdb-core"
)

// Shapes, as recorded in the source-request log.
const (
	ShapePKPage    = "pk_page"
	ShapeIndexPage = "index_page"
	ShapeKeyBatch  = "key_batch"
	ShapeExists    = "exists"
	ShapeColumns   = "by_columns"
	ShapeCount     = "count"
	ShapeExplain   = "explain"
	ShapeCatalog   = "catalog"
	ShapeSession   = "session"
)

// projection is the select list of a Source read: the requested columns that
// come from the Source, then the identity key columns.
type projection struct {
	exprs   []string
	reqAt   []int // for each requested column, its position in the result, or -1
	keyAt   []int // for each identity column, its position in the result
	reqCols core.ColumnSet
}

func fetched(c core.ColumnDef) bool { return c.Fetch == core.FromSource || c.Fetch == core.Transformed }

func colExpr(c core.ColumnDef) string {
	if c.Timestamp {
		return "UNIX_TIMESTAMP(" + Ident(c.Name) + ")"
	}
	return Ident(c.Name)
}

// identityCols returns the identity key's columns, or every column for
// tables without a usable key.
func identityCols(d *core.TableDef) []uint16 {
	if cols := d.IdentityColumns(); cols != nil {
		return cols
	}
	all := make([]uint16, len(d.Columns))
	for i := range all {
		all[i] = uint16(i)
	}
	return all
}

func project(d *core.TableDef, cols core.ColumnSet) projection {
	p := projection{reqCols: cols}
	pos := map[uint16]int{}
	add := func(c uint16) int {
		if i, ok := pos[c]; ok {
			return i
		}
		pos[c] = len(p.exprs)
		p.exprs = append(p.exprs, colExpr(d.Columns[c]))
		return pos[c]
	}
	for _, c := range cols {
		if int(c) < len(d.Columns) && fetched(d.Columns[c]) {
			p.reqAt = append(p.reqAt, add(c))
		} else {
			p.reqAt = append(p.reqAt, -1)
		}
	}
	for _, c := range identityCols(d) {
		if fetched(d.Columns[c]) {
			p.keyAt = append(p.keyAt, add(c))
		}
	}
	if len(p.exprs) == 0 {
		p.exprs = []string{"1"}
	}
	return p
}

func tableRef(d *core.TableDef) string {
	db, name := d.Database, d.Name
	if d.Source != nil {
		db, name = d.Source.Database, d.Source.Table
	}
	return Ident(db) + "." + Ident(name)
}

func colList(d *core.TableDef, cols []uint16) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = colExpr(d.Columns[c])
	}
	return strings.Join(parts, ", ")
}

func rawCols(d *core.TableDef, cols []uint16) []string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = Ident(d.Columns[c].Name)
	}
	return parts
}

func tuple(d *core.TableDef, cols []uint16, vals []core.Value) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = Literal(d.Columns[cols[i]], v)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func lhs(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return "(" + strings.Join(names, ", ") + ")"
}

// boundCond renders one bound over the first parts of an index.
func boundCond(d *core.TableDef, cols []uint16, vals []core.Value, op string) string {
	cols = cols[:len(vals)]
	names := rawCols(d, cols)
	// NULL in a bound: equality means IS NULL; a lower bound of NULL is the
	// start of the index (NULL sorts first); an exclusive lower bound of NULL
	// means IS NOT NULL.
	for i, v := range vals {
		if !v.Null {
			continue
		}
		if len(vals) == 1 {
			switch op {
			case ">=", "<=":
				if op == "<=" {
					return names[0] + " IS NULL"
				}
				return ""
			case ">":
				return names[0] + " IS NOT NULL"
			case "<":
				return "FALSE"
			}
		}
		// Multi-part bound with a NULL: bound only the parts before it; the
		// Delta side and mysqld's own condition check keep results exact.
		if i == 0 {
			return ""
		}
		return boundCond(d, cols[:i], vals[:i], strings.TrimSuffix(op, "=")+"=")
	}
	return lhs(names) + " " + op + " " + tuple(d, cols, vals)
}

// rangeCond renders a KeyRange over an index as a WHERE fragment.
func rangeCond(d *core.TableDef, idxCols []uint16, r core.KeyRange) []string {
	var out []string
	lo := core.DecodeSourceKey(r.RawLow)
	hi := core.DecodeSourceKey(r.RawHigh)
	if len(lo) > len(idxCols) {
		lo = lo[:len(idxCols)]
	}
	if len(hi) > len(idxCols) {
		hi = hi[:len(idxCols)]
	}
	if len(lo) > 0 && len(hi) > 0 && r.RawLowIncl && r.RawHighIncl && equalVals(lo, hi) {
		if c := boundCond(d, idxCols, lo, "="); c != "" {
			return []string{eqNull(c)}
		}
	}
	if len(lo) > 0 {
		op := ">"
		if r.RawLowIncl {
			op = ">="
		}
		if c := boundCond(d, idxCols, lo, op); c != "" {
			out = append(out, c)
		}
	}
	if len(hi) > 0 {
		op := "<"
		if r.RawHighIncl {
			op = "<="
		}
		if c := boundCond(d, idxCols, hi, op); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func eqNull(c string) string { return c }

func equalVals(a, b []core.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Null != b[i].Null || string(a[i].B) != string(b[i].B) {
			return false
		}
	}
	return true
}

func where(parts []string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, "("+p+")")
		}
	}
	if len(keep) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(keep, " AND ")
}

func orderBy(d *core.TableDef, cols []uint16, dir core.Direction) string {
	names := rawCols(d, cols)
	suffix := ""
	if dir == core.Backward {
		suffix = " DESC"
	}
	for i := range names {
		names[i] += suffix
	}
	return " ORDER BY " + strings.Join(names, ", ")
}

// pageOrder returns the columns a page is ordered by: the index's columns
// then the identity columns not already in it.
func pageOrder(d *core.TableDef, idx core.IndexID) []uint16 {
	var cols []uint16
	seen := map[uint16]bool{}
	if ix := d.Index(idx); ix != nil {
		for _, c := range ix.Columns {
			cols = append(cols, c)
			seen[c] = true
		}
	}
	for _, c := range identityCols(d) {
		if !seen[c] {
			cols = append(cols, c)
		}
	}
	return cols
}

// keysetSafe reports whether keyset continuation works on these columns:
// every one is NOT NULL.
func keysetSafe(d *core.TableDef, cols []uint16) bool {
	for _, c := range cols {
		if d.Columns[c].Nullable {
			return false
		}
	}
	return d.Identity != core.WholeRow
}

// renderPage renders one page of a scan (renderPKPage and renderIndexPage of
// doc 10 §4.1). after holds the order columns' values of the last row
// delivered, for keyset continuation; offset is used when keyset is unsafe.
func renderPage(d *core.TableDef, p projection, idx core.IndexID, ranges []core.KeyRange, after []core.Value,
	offset int, pushed string, dir core.Direction, limit int, force bool) shaped {
	order := pageOrder(d, idx)
	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(strings.Join(p.exprs, ", "))
	sb.WriteString(" FROM ")
	sb.WriteString(tableRef(d))
	shape := ShapePKPage
	if ix := d.Index(idx); ix != nil && force && !ix.Primary {
		sb.WriteString(" FORCE INDEX (" + Ident(ix.Name) + ")")
		shape = ShapeIndexPage
	}
	var conds []string
	if len(ranges) > 0 {
		var ors []string
		ix := d.Index(idx)
		for _, r := range ranges {
			if ix == nil {
				break
			}
			rc := rangeCond(d, ix.Columns, r)
			if len(rc) == 0 {
				ors = nil
				break
			}
			ors = append(ors, "("+strings.Join(rc, " AND ")+")")
		}
		if len(ors) > 0 {
			conds = append(conds, strings.Join(ors, " OR "))
		}
	}
	if pushed != "" {
		conds = append(conds, pushed)
	}
	if after != nil {
		op := ">"
		if dir == core.Backward {
			op = "<"
		}
		conds = append(conds, lhs(rawCols(d, order))+" "+op+" "+tuple(d, order, after))
	}
	sb.WriteString(where(conds))
	sb.WriteString(orderBy(d, order, dir))
	sb.WriteString(" LIMIT " + strconv.Itoa(limit))
	if offset > 0 {
		sb.WriteString(" OFFSET " + strconv.Itoa(offset))
	}
	return shaped{sql: sb.String(), shape: shape}
}

// renderKeyBatch renders one tagged request per key, joined with UNION ALL,
// so every row names the key it answers: `SELECT 0 AS vdb_k, cols FROM t
// WHERE (k) = (v) UNION ALL ...` (the IN-list shape with exact key indexes).
func renderKeyBatch(d *core.TableDef, p projection, cols []uint16, keys [][]core.Value, shape string) shaped {
	var parts []string
	for i, k := range keys {
		c := keyEq(d, cols, k)
		parts = append(parts, "(SELECT "+strconv.Itoa(i)+" AS vdb_k, "+strings.Join(p.exprs, ", ")+" FROM "+tableRef(d)+" WHERE "+c+")")
	}
	return shaped{sql: strings.Join(parts, " UNION ALL "), shape: shape}
}

// keyEq matches one exact key. A handler key lookup matches NULL key parts
// as values (the index's NULL marker), so a NULL part renders as IS NULL.
func keyEq(d *core.TableDef, cols []uint16, k []core.Value) string {
	cols = cols[:len(k)]
	var parts []string
	for i, v := range k {
		if v.Null {
			parts = append(parts, rawCols(d, cols[i:i+1])[0]+" IS NULL")
			continue
		}
		parts = append(parts, boundCond(d, cols[i:i+1], k[i:i+1], "="))
	}
	if len(parts) == 0 {
		return "TRUE"
	}
	return strings.Join(parts, " AND ")
}

// renderCount renders SELECT COUNT(*).
func renderCount(d *core.TableDef) shaped {
	return shaped{sql: "SELECT COUNT(*) FROM " + tableRef(d), shape: ShapeCount}
}

// renderExplain renders EXPLAIN FORMAT=JSON of a shaped read.
func renderExplain(s shaped) shaped {
	return shaped{sql: "EXPLAIN FORMAT=JSON " + s.sql, shape: ShapeExplain}
}

// renderSet renders SET SESSION for mirrored settings.
func renderSet(settings [][2]string) shaped {
	parts := make([]string, len(settings))
	for i, s := range settings {
		v := quote([]byte(s[1]))
		if isUint(s[1]) {
			v = s[1] // integer variables refuse a quoted value
		}
		parts[i] = s[0] + " = " + v
	}
	return shaped{sql: "SET SESSION " + strings.Join(parts, ", "), shape: ShapeSession}
}

func isUint(v string) bool {
	if v == "" || len(v) > 9 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// RenderCatalog wraps a fixed catalog read (information_schema, SHOW CREATE,
// grant tables) for the background pool. Only read-only first keywords pass
// the check.
func RenderCatalog(sql string) shaped { return shaped{sql: sql, shape: ShapeCatalog} }
