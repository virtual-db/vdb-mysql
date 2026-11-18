// Package discovery reads the Source at start (implementation plan doc 12
// §4): databases, base tables, their definitions and structure, views and
// routines when granted, and statistics, and builds the catalog of record's
// TableDefs from them. Every read runs on the background pool.
package discovery

import (
	"context"
	"fmt"
	"github.com/virtual-db/vdb-mysql/internal/logx"
	"sort"
	"strconv"
	"strings"

	core "github.com/virtual-db/vdb-core"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/virtual-db/vdb-mysql/internal/collations"
	"github.com/virtual-db/vdb-mysql/internal/source"
)

var log = logx.Pkg("discovery")

// Reader runs catalog reads on the Source.
type Reader interface {
	Background(ctx context.Context, s source.Shaped) ([][]core.Value, error)
}

// CatalogError is a Source object VDB could not mirror (vdb.catalog_errors).
type CatalogError struct {
	Type, Schema, Name, Error string
}

// Result is what discovery found.
type Result struct {
	Databases []Database
	Tables    []core.TableDef
	Objects   []core.Object
	Errors    []CatalogError
	Warnings  []CatalogError
}

// Database is one exposed database.
type Database struct {
	Name, Charset, Collation string
}

// System schemas are never exposed.
var System = map[string]bool{"mysql": true, "sys": true, "performance_schema": true, "information_schema": true, "vdb": true}

// Discoverer reads the Source.
type Discoverer struct {
	r      Reader
	parser *sqlparser.Parser
	proven *collations.Proven
	only   []string
}

// New returns a Discoverer for the databases only (all visible when empty).
func New(r Reader, proven *collations.Proven, only []string) (*Discoverer, error) {
	p, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.4.0"})
	if err != nil {
		return nil, err
	}
	return &Discoverer{r: r, parser: p, proven: proven, only: only}, nil
}

func s(v core.Value) string { return string(v.B) }

func (d *Discoverer) q(ctx context.Context, sql string) ([][]core.Value, error) {
	return d.r.Background(ctx, source.RenderCatalog(sql))
}

// Databases returns the exposed databases.
func (d *Discoverer) Databases(ctx context.Context) ([]Database, error) {
	rows, err := d.q(ctx, "SELECT SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
	if err != nil {
		return nil, fmt.Errorf("discovery.databases: %w", err)
	}
	want := map[string]bool{}
	for _, n := range d.only {
		want[strings.ToLower(n)] = true
	}
	var out []Database
	for _, r := range rows {
		name := s(r[0])
		if System[strings.ToLower(name)] || (len(want) > 0 && !want[strings.ToLower(name)]) {
			continue
		}
		out = append(out, Database{Name: name, Charset: s(r[1]), Collation: s(r[2])})
	}
	return out, nil
}

// Run discovers every exposed database.
func (d *Discoverer) Run(ctx context.Context) (*Result, error) {
	dbs, err := d.Databases(ctx)
	if err != nil {
		return nil, err
	}
	res := &Result{Databases: dbs}
	visible := map[string]bool{}
	tablesByDB := map[string][]string{}
	for _, db := range dbs {
		rows, err := d.q(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+source.Quote(db.Name)+" AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME")
		if err != nil {
			return nil, fmt.Errorf("discovery.tables: %w", err)
		}
		for _, r := range rows {
			tablesByDB[db.Name] = append(tablesByDB[db.Name], s(r[0]))
			visible[strings.ToLower(db.Name+"."+s(r[0]))] = true
		}
	}
	isVisible := func(db, t string) bool { return visible[strings.ToLower(db+"."+t)] }
	for _, db := range dbs {
		for _, t := range tablesByDB[db.Name] {
			def, warns, err := d.Table(ctx, db.Name, t, isVisible)
			if err != nil {
				res.Errors = append(res.Errors, CatalogError{Type: "table", Schema: db.Name, Name: t, Error: err.Error()})
				log.Warn("table not mirrored", "table", db.Name+"."+t, "err", err)
				continue
			}
			for _, w := range warns {
				res.Warnings = append(res.Warnings, CatalogError{Type: "table", Schema: db.Name, Name: t, Error: w})
			}
			res.Tables = append(res.Tables, *def)
		}
		objs, errs := d.Objects(ctx, db.Name)
		res.Objects = append(res.Objects, objs...)
		res.Errors = append(res.Errors, errs...)
	}
	return res, nil
}

// Table reads one table's definition and structure.
func (d *Discoverer) Table(ctx context.Context, db, table string, visible func(db, table string) bool) (*core.TableDef, []string, error) {
	rows, err := d.q(ctx, "SHOW CREATE TABLE "+source.Ident(db)+"."+source.Ident(table))
	if err != nil || len(rows) == 0 || len(rows[0]) < 2 {
		return nil, nil, fmt.Errorf("SHOW CREATE TABLE: %v", err)
	}
	srcDef := s(rows[0][1])
	defn, warns, err := Define(d.parser, srcDef, db, visible)
	if err != nil {
		return nil, nil, err
	}
	def := &core.TableDef{Database: db, Name: table, Kind: core.Mirrored, Definition: defn,
		Source: &core.SourceMapping{Database: db, Table: table, Definition: srcDef, Fingerprint: Fingerprint(srcDef)}}
	if err := d.columns(ctx, def); err != nil {
		return nil, nil, err
	}
	if err := d.indexes(ctx, def); err != nil {
		return nil, nil, err
	}
	if err := d.foreignKeys(ctx, def, visible); err != nil {
		return nil, nil, err
	}
	rows, err = d.q(ctx, "SELECT IFNULL(AUTO_INCREMENT,0), IFNULL(TABLE_ROWS,0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+source.Quote(db)+" AND TABLE_NAME = "+source.Quote(table))
	if err == nil && len(rows) == 1 {
		def.Source.AutoInc, _ = strconv.ParseUint(s(rows[0][0]), 10, 64)
		def.Source.RowsEst, _ = strconv.ParseUint(s(rows[0][1]), 10, 64)
	}
	return def, warns, nil
}

func (d *Discoverer) columns(ctx context.Context, def *core.TableDef) error {
	rows, err := d.q(ctx, "SELECT COLUMN_NAME, COLUMN_TYPE, IFNULL(CHARACTER_SET_NAME,''), IFNULL(COLLATION_NAME,''), IS_NULLABLE, COLUMN_DEFAULT, EXTRA, DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+
		source.Quote(def.Database)+" AND TABLE_NAME = "+source.Quote(def.Name)+" ORDER BY ORDINAL_POSITION")
	if err != nil {
		return fmt.Errorf("columns: %w", err)
	}
	for i, r := range rows {
		extra := strings.ToLower(s(r[6]))
		dt := strings.ToLower(s(r[7]))
		c := core.ColumnDef{Name: s(r[0]), Pos: uint16(i), Type: s(r[1]), Charset: s(r[2]), Collation: s(r[3]),
			Nullable: s(r[4]) == "YES", Default: r[5], Timestamp: dt == "timestamp", Float: dt == "float" || dt == "double",
			AutoIncrement: strings.Contains(extra, "auto_increment")}
		if strings.Contains(extra, "virtual generated") {
			c.Fetch = core.Virtual
		}
		def.Columns = append(def.Columns, c)
	}
	if len(def.Columns) == 0 {
		return fmt.Errorf("columns: none readable")
	}
	return nil
}

type rawIndex struct {
	name    string
	unique  bool
	primary bool
	cols    []uint16
	notNull bool
	kind    string
}

func (d *Discoverer) indexes(ctx context.Context, def *core.TableDef) error {
	rows, err := d.q(ctx, "SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, INDEX_TYPE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = "+
		source.Quote(def.Database)+" AND TABLE_NAME = "+source.Quote(def.Name)+" ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME, SEQ_IN_INDEX")
	if err != nil {
		return fmt.Errorf("indexes: %w", err)
	}
	var list []*rawIndex
	by := map[string]*rawIndex{}
	for _, r := range rows {
		name := s(r[0])
		kind := strings.ToUpper(s(r[3]))
		if kind == "FULLTEXT" || kind == "SPATIAL" {
			continue
		}
		ix := by[name]
		if ix == nil {
			ix = &rawIndex{name: name, unique: s(r[1]) == "0", primary: name == "PRIMARY", notNull: true, kind: kind}
			by[name] = ix
			list = append(list, ix)
		}
		c := def.ColumnByName(s(r[2]))
		if c < 0 {
			continue // functional key part: not mirrored as a column
		}
		ix.cols = append(ix.cols, uint16(c))
		if def.Columns[c].Nullable {
			ix.notNull = false
		}
	}
	// mysqld's key order: PRIMARY, unique NOT NULL keys, other unique keys, the rest.
	rank := func(ix *rawIndex) int {
		switch {
		case ix.primary:
			return 0
		case ix.unique && ix.notNull:
			return 1
		case ix.unique:
			return 2
		}
		return 3
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i]) < rank(list[j]) })
	def.Identity = core.WholeRow
	for i, ix := range list {
		order, eq := true, true
		for k, c := range ix.cols {
			coll := def.Columns[c].Collation
			if k == 0 && !d.proven.Order(coll) {
				order = false
			}
			if !d.proven.Equality(coll) {
				eq = false
			}
		}
		def.Indexes = append(def.Indexes, core.IndexDef{ID: core.IndexID(i), Name: ix.name, Primary: ix.primary, Unique: ix.unique || ix.primary,
			Columns: ix.cols, OrderProven: order, EqualityProven: eq})
		if def.Identity == core.WholeRow && i == 0 && (ix.primary || (ix.unique && ix.notNull)) {
			def.Identity = 0
		}
	}
	return nil
}

func (d *Discoverer) foreignKeys(ctx context.Context, def *core.TableDef, visible func(db, table string) bool) error {
	rows, err := d.q(ctx, "SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.UPDATE_RULE, r.DELETE_RULE "+
		"FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME "+
		"WHERE k.TABLE_SCHEMA = "+source.Quote(def.Database)+" AND k.TABLE_NAME = "+source.Quote(def.Name)+" AND k.REFERENCED_TABLE_NAME IS NOT NULL ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION")
	if err != nil {
		return nil // not readable: the definitions in mysqld still hold them
	}
	by := map[string]*core.ForeignKeyDef{}
	var order []string
	for _, r := range rows {
		name := s(r[0])
		if visible != nil && !visible(s(r[2]), s(r[3])) {
			continue
		}
		fk := by[name]
		if fk == nil {
			fk = &core.ForeignKeyDef{Name: name, ParentDB: s(r[2]), ParentTable: s(r[3]), OnUpdate: action(s(r[5])), OnDelete: action(s(r[6]))}
			by[name] = fk
			order = append(order, name)
		}
		if c := def.ColumnByName(s(r[1])); c >= 0 {
			fk.Columns = append(fk.Columns, uint16(c))
		}
		fk.ParentColumns = append(fk.ParentColumns, s(r[4]))
	}
	for _, n := range order {
		fk := by[n]
		fk.ChildIndex = SupportingIndex(def, fk.Columns)
		def.ForeignKeys = append(def.ForeignKeys, *fk)
	}
	return nil
}

// SupportingIndex returns the first index whose leading columns are cols.
func SupportingIndex(def *core.TableDef, cols []uint16) core.IndexID {
	for _, ix := range def.Indexes {
		if len(ix.Columns) < len(cols) {
			continue
		}
		ok := true
		for i, c := range cols {
			if ix.Columns[i] != c {
				ok = false
				break
			}
		}
		if ok {
			return ix.ID
		}
	}
	return 0
}

func action(rule string) core.FKAction {
	switch strings.ToUpper(rule) {
	case "CASCADE":
		return core.FKCascade
	case "SET NULL":
		return core.FKSetNull
	case "NO ACTION":
		return core.FKNoAction
	}
	return core.FKRestrict
}

// Objects reads a database's views and routines when the service account
// may see their definitions.
func (d *Discoverer) Objects(ctx context.Context, db string) ([]core.Object, []CatalogError) {
	var out []core.Object
	var errs []CatalogError
	rows, err := d.q(ctx, "SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA = "+source.Quote(db)+" ORDER BY TABLE_NAME")
	if err == nil {
		for _, r := range rows {
			v, err := d.q(ctx, "SHOW CREATE VIEW "+source.Ident(db)+"."+source.Ident(s(r[0])))
			if err != nil || len(v) == 0 {
				errs = append(errs, CatalogError{Type: "view", Schema: db, Name: s(r[0]), Error: fmt.Sprint("SHOW CREATE VIEW: ", err)})
				continue
			}
			out = append(out, core.Object{Kind: core.View, Database: db, Name: s(r[0]), Statement: s(v[0][1]), Origin: core.ObjectFromSource})
		}
	}
	rows, err = d.q(ctx, "SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = "+source.Quote(db)+" ORDER BY ROUTINE_NAME")
	if err == nil {
		for _, r := range rows {
			kind, word := core.Procedure, "PROCEDURE"
			if strings.EqualFold(s(r[1]), "FUNCTION") {
				kind, word = core.Function, "FUNCTION"
			}
			v, err := d.q(ctx, "SHOW CREATE "+word+" "+source.Ident(db)+"."+source.Ident(s(r[0])))
			if err != nil || len(v) == 0 || len(v[0]) < 3 || v[0][2].Null {
				errs = append(errs, CatalogError{Type: strings.ToLower(word), Schema: db, Name: s(r[0]), Error: "definition not readable by the service account"})
				continue
			}
			out = append(out, core.Object{Kind: kind, Database: db, Name: s(r[0]), Statement: s(v[0][2]), SQLMode: s(v[0][1]), Origin: core.ObjectFromSource})
		}
	}
	return out, errs
}
