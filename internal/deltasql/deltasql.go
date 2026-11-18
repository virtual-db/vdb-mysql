// Package deltasql renders the Delta as SQL for vdb.delta_sql (implementation
// plan doc 13 §2, product doc 13 §5): local tables' definitions, local
// changes on mirrored tables, client objects in commit order, then inserts
// for Delta rows of local origin, updates for Delta rows of Source origin, and
// deletes for touched keys without a Delta row. Values are rendered with the
// same literal code that renders keys for the Source. VDB never executes them.
package deltasql

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/source"
)

// Statement is one row of vdb.delta_sql.
type Statement struct {
	Seq          uint64
	Kind         string // ddl, insert, update, delete
	Schema, Name string
	SQL          string
}

var engineClause = regexp.MustCompile(`(?i)\s*ENGINE\s*=\s*\w+`)

func qual(d *core.TableDef) string { return source.Ident(d.Database) + "." + source.Ident(d.Name) }

// Render renders every statement.
func Render(api core.DriverAPI) []Statement {
	cat := api.Catalog()
	tables := cat.Tables()
	sort.Slice(tables, func(i, j int) bool { return tables[i].ID < tables[j].ID })
	var out []Statement
	add := func(kind string, d *core.TableDef, schema, name, sql string) {
		if d != nil {
			schema, name = d.Database, d.Name
		}
		out = append(out, Statement{Seq: uint64(len(out) + 1), Kind: kind, Schema: schema, Name: name, SQL: sql})
	}
	for i := range tables {
		d := &tables[i]
		if d.Kind == core.Local && !strings.EqualFold(d.Database, "vdb") {
			add("ddl", d, "", "", engineClause.ReplaceAllString(d.Definition, ""))
		}
	}
	for i := range tables {
		d := &tables[i]
		for _, lc := range d.LocalChanges {
			add("ddl", d, "", "", lc.Statement)
		}
	}
	for _, o := range cat.Objects() {
		if o.Origin == core.ObjectClient {
			add("ddl", nil, o.Database, o.Name, o.Statement)
		}
	}
	reader := api.Delta()
	byID := map[core.TableID]*core.TableDef{}
	for i := range tables {
		byID[tables[i].ID] = &tables[i]
	}
	ids := reader.Tables()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d := byID[id]
		if d == nil || d.Kind == core.Provided {
			continue
		}
		withDelta := map[string]bool{}
		reader.Rows(id, func(r core.DeltaRowInfo) bool {
			if r.Origin == core.OriginSource && d.Kind == core.Mirrored {
				add("update", d, "", "", update(d, r))
				withDelta[string(r.SourceKey)] = true
			} else {
				add("insert", d, "", "", insert(d, r.Row))
			}
			return true
		})
		if d.Kind != core.Mirrored {
			continue
		}
		reader.Touched(id, func(t core.TouchedInfo) bool {
			if !withDelta[string(t.Key)] {
				add("delete", d, "", "", del(d, t))
			}
			return true
		})
	}
	return out
}

func insert(d *core.TableDef, row core.Row) string {
	var cols, vals []string
	for i, c := range d.Columns {
		if c.Fetch == core.Virtual || i >= len(row) {
			continue
		}
		cols = append(cols, source.Ident(c.Name))
		vals = append(vals, source.Literal(c, row[i]))
	}
	return "INSERT INTO " + qual(d) + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")"
}

func keyCols(d *core.TableDef) []uint16 {
	if c := d.IdentityColumns(); c != nil {
		return c
	}
	all := make([]uint16, len(d.Columns))
	for i := range all {
		all[i] = uint16(i)
	}
	return all
}

func where(d *core.TableDef, key core.SourceKey) string {
	vals := core.DecodeSourceKey(key)
	var parts []string
	for i, c := range keyCols(d) {
		if i >= len(vals) {
			break
		}
		col := d.Columns[c]
		if vals[i].Null {
			parts = append(parts, source.Ident(col.Name)+" IS NULL")
			continue
		}
		parts = append(parts, source.Ident(col.Name)+" = "+source.Literal(col, vals[i]))
	}
	return strings.Join(parts, " AND ")
}

func update(d *core.TableDef, r core.DeltaRowInfo) string {
	var sets []string
	for i, c := range d.Columns {
		if c.Fetch == core.Virtual || i >= len(r.Row) {
			continue
		}
		sets = append(sets, source.Ident(c.Name)+" = "+source.Literal(c, r.Row[i]))
	}
	s := "UPDATE " + qual(d) + " SET " + strings.Join(sets, ", ") + " WHERE " + where(d, r.SourceKey)
	if d.Identity == core.WholeRow {
		s += " LIMIT 1"
	}
	return s
}

func del(d *core.TableDef, t core.TouchedInfo) string {
	s := "DELETE FROM " + qual(d) + " WHERE " + where(d, t.Key)
	if d.Identity == core.WholeRow {
		s += " LIMIT " + strconv.Itoa(int(t.Count))
	}
	return s
}
