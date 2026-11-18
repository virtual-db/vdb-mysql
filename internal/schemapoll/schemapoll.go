// Package schemapoll detects and applies Source schema changes
// (implementation plan doc 12 §5, product doc 09 §5): every poll it reads a
// fingerprint per exposed table, view and routine, reads SHOW CREATE only for
// what changed, appeared or disappeared, classifies the change, applies it to
// mysqld over the control channel (the engine records it through the control
// session's transaction), and records it for vdb.schema_changes.
package schemapoll

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/virtual-db/vdb-mysql/internal/logx"
	"sort"
	"strings"
	"sync"
	"time"

	core "github.com/virtual-db/vdb-core"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/virtual-db/vdb-mysql/internal/control"
	"github.com/virtual-db/vdb-mysql/internal/discovery"
	"github.com/virtual-db/vdb-mysql/internal/source"
)

var log = logx.Pkg("schemapoll")

// Change is one applied Source change (a row of vdb.schema_changes).
type Change struct {
	Seq         uint64
	AppliedAt   time.Time
	Schema      string
	Table       string
	Change      string
	DeltaEffect string
	Affected    []string
}

// Reader runs catalog reads on the Source.
type Reader interface {
	Background(ctx context.Context, s source.Shaped) ([][]core.Value, error)
}

// Refresher refreshes statistics in the same pass.
type Refresher interface {
	Refresh(ctx context.Context, db string) error
}

// Poller polls the Source schema. Safe for concurrent use.
type Poller struct {
	r      Reader
	disc   *discovery.Discoverer
	ctl    *control.Control
	api    core.DriverAPI
	stats  Refresher
	dbs    func() []string
	parser *sqlparser.Parser

	mu      sync.Mutex
	prints  map[string]string // "db.table" -> structure fingerprint
	objs    map[string]string // "kind db.name" -> definition fingerprint
	changes []Change
	last    map[core.TableID]time.Time
	seq     uint64
}

// New returns a Poller.
func New(r Reader, disc *discovery.Discoverer, ctl *control.Control, api core.DriverAPI, st Refresher, dbs func() []string) (*Poller, error) {
	p, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.4.0"})
	if err != nil {
		return nil, err
	}
	return &Poller{r: r, disc: disc, ctl: ctl, api: api, stats: st, dbs: dbs, parser: p,
		prints: map[string]string{}, objs: map[string]string{}, last: map[core.TableID]time.Time{}}, nil
}

func str(v core.Value) string { return string(v.B) }

// fingerprints reads one fingerprint per base table of a database: a hash
// over its columns, indexes, check and referential constraints, and options.
func (p *Poller) fingerprints(ctx context.Context, db string) (map[string]string, error) {
	q := source.Quote(db)
	out := map[string]string{}
	parts := []string{
		"SELECT TABLE_NAME, CONCAT(COUNT(*), ':', BIT_XOR(CRC32(CONCAT_WS('|', COLUMN_NAME, ORDINAL_POSITION, COLUMN_TYPE, IS_NULLABLE, IFNULL(COLUMN_DEFAULT,'<null>'), IFNULL(COLLATION_NAME,''), EXTRA, IFNULL(GENERATION_EXPRESSION,''))))) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = " + q + " GROUP BY TABLE_NAME",
		"SELECT TABLE_NAME, CONCAT(COUNT(*), ':', BIT_XOR(CRC32(CONCAT_WS('|', INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, IFNULL(COLUMN_NAME,''), INDEX_TYPE)))) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = " + q + " GROUP BY TABLE_NAME",
		"SELECT TABLE_NAME, CONCAT(COUNT(*), ':', BIT_XOR(CRC32(CONCAT_WS('|', CONSTRAINT_NAME, UPDATE_RULE, DELETE_RULE, REFERENCED_TABLE_NAME)))) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = " + q + " GROUP BY TABLE_NAME",
		"SELECT TABLE_NAME, CONCAT_WS('|', TABLE_COLLATION, IFNULL(CREATE_OPTIONS,''), TABLE_COMMENT) FROM information_schema.TABLES WHERE TABLE_SCHEMA = " + q + " AND TABLE_TYPE = 'BASE TABLE'",
	}
	for i, sql := range parts {
		rows, err := p.r.Background(ctx, source.RenderCatalog(sql))
		if err != nil {
			if i == 0 || i == 3 {
				return nil, err
			}
			continue
		}
		for _, r := range rows {
			out[strings.ToLower(str(r[0]))] += "/" + str(r[1])
		}
	}
	// Only tables that exist (the TABLES read) count.
	rows, err := p.r.Background(ctx, source.RenderCatalog("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+q+" AND TABLE_TYPE = 'BASE TABLE'"))
	if err != nil {
		return nil, err
	}
	exists := map[string]bool{}
	for _, r := range rows {
		exists[strings.ToLower(str(r[0]))] = true
	}
	for k := range out {
		if !exists[k] {
			delete(out, k)
		}
	}
	return out, nil
}

// Init records the fingerprints at start, after discovery.
func (p *Poller) Init(ctx context.Context) error {
	for _, db := range p.dbs() {
		fp, err := p.fingerprints(ctx, db)
		if err != nil {
			return err
		}
		p.mu.Lock()
		for t, f := range fp {
			p.prints[strings.ToLower(db)+"."+t] = f
		}
		p.mu.Unlock()
		p.objectPrints(ctx, db, true)
	}
	return nil
}

// Run polls every interval until ctx ends.
func (p *Poller) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.Poll(ctx); err != nil {
				log.Warn("schema poll failed; retried next interval", "err", err)
			}
		}
	}
}

// Poll runs one pass.
func (p *Poller) Poll(ctx context.Context) error {
	for _, db := range p.dbs() {
		fp, err := p.fingerprints(ctx, db)
		if err != nil {
			return err
		}
		ldb := strings.ToLower(db)
		p.mu.Lock()
		var added, dropped, changed []string
		for t, f := range fp {
			old, ok := p.prints[ldb+"."+t]
			if !ok {
				added = append(added, t)
			} else if old != f {
				changed = append(changed, t)
			}
		}
		for k := range p.prints {
			if strings.HasPrefix(k, ldb+".") {
				t := strings.TrimPrefix(k, ldb+".")
				if _, ok := fp[t]; !ok {
					dropped = append(dropped, t)
				}
			}
		}
		p.mu.Unlock()
		sort.Strings(added)
		sort.Strings(dropped)
		sort.Strings(changed)
		renamed := p.renames(ctx, db, added, dropped)
		for from, to := range renamed {
			p.rename(ctx, db, from, to)
			added = remove(added, to)
			dropped = remove(dropped, from)
		}
		for _, t := range added {
			p.add(ctx, db, t)
		}
		for _, t := range dropped {
			p.drop(ctx, db, t)
		}
		for _, t := range changed {
			p.change(ctx, db, t)
		}
		p.mu.Lock()
		for t, f := range fp {
			p.prints[ldb+"."+t] = f
		}
		for _, t := range dropped {
			delete(p.prints, ldb+"."+t)
		}
		p.mu.Unlock()
		p.objectPrints(ctx, db, false)
		if p.stats != nil {
			_ = p.stats.Refresh(ctx, db)
		}
	}
	return nil
}

func remove(xs []string, x string) []string {
	var out []string
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

func (p *Poller) record(schema, table, change, effect string, affected []string) {
	p.mu.Lock()
	p.seq++
	p.changes = append(p.changes, Change{Seq: p.seq, AppliedAt: time.Now(), Schema: schema, Table: table, Change: change, DeltaEffect: effect, Affected: affected})
	if d, ok := p.api.Catalog().TableByName(schema, table); ok {
		p.last[d.ID] = time.Now()
	}
	p.mu.Unlock()
	log.Info("Source schema change applied", "table", schema+"."+table, "change", change)
}

// Changes returns the rows of vdb.schema_changes.
func (p *Poller) Changes() []Change {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Change(nil), p.changes...)
}

// LastChange returns when a table last changed on the Source.
func (p *Poller) LastChange(id core.TableID) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last[id]
}

// AffectedJSON renders the affected list.
func AffectedJSON(a []string) string {
	b, _ := json.Marshal(a)
	return string(b)
}

func (p *Poller) deltaState(db, table string) (core.TableDef, uint64, bool) {
	d, ok := p.api.Catalog().TableByName(db, table)
	if !ok {
		return d, 0, false
	}
	for _, t := range p.api.DeltaStats().Tables {
		if t.Table == d.ID {
			return d, t.DeltaRows + t.TouchedKeys, true
		}
	}
	return d, 0, true
}

func (p *Poller) exec(ctx context.Context, db string, stmts ...string) error {
	return p.ctl.Session(ctx, func(e control.Exec) error {
		if err := e.Exec(ctx, control.SetSession("foreign_key_checks", "0")); err != nil {
			return err
		}
		if err := e.Exec(ctx, control.Use(db)); err != nil {
			return err
		}
		for _, s := range stmts {
			if err := e.Exec(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *Poller) visible(db, t string) bool {
	for _, d := range p.dbs() {
		if strings.EqualFold(d, db) {
			return true
		}
	}
	return false
}

// add mirrors a table that appeared on the Source.
func (p *Poller) add(ctx context.Context, db, table string) {
	def, warns, err := p.disc.Table(ctx, db, table, p.visible)
	if err != nil {
		log.Warn("new Source table not mirrored", "table", db+"."+table, "err", err)
		return
	}
	def.Kind = core.Mirrored
	if _, err := p.api.Catalog().ApplySource(core.SourceChange{Kind: core.SourceTableAdded, Table: *def}); err != nil {
		log.Warn("catalog refused a new Source table", "err", err)
		return
	}
	if err := p.exec(ctx, db, def.Definition); err != nil {
		log.Warn("new Source table not created", "table", db+"."+table, "err", err)
		return
	}
	p.record(db, table, "table added", "none", warns)
}

// drop handles a table that disappeared: dropped when it has no Delta rows,
// otherwise detached so its Delta rows stay.
func (p *Poller) drop(ctx context.Context, db, table string) {
	d, n, ok := p.deltaState(db, table)
	if !ok {
		return
	}
	if n == 0 && len(d.LocalChanges) == 0 {
		if err := p.exec(ctx, db, control.DropTable(db, table)); err != nil {
			log.Warn("dropped Source table not removed", "err", err)
			return
		}
		p.record(db, table, "table dropped", "none", nil)
		return
	}
	d.Kind = core.Detached
	d.Version++
	if _, err := p.api.Catalog().ApplySource(core.SourceChange{Kind: core.SourceTableChanged, ID: d.ID, Table: d}); err != nil {
		return
	}
	p.record(db, table, "table dropped on the Source", "kept as a detached table with its Delta rows", nil)
}

// rename applies a rename proven by exact evidence (doc 12 §5.2).
func (p *Poller) rename(ctx context.Context, db, from, to string) {
	d, ok := p.api.Catalog().TableByName(db, from)
	if !ok {
		return
	}
	if err := p.exec(ctx, db, "RENAME TABLE "+source.Ident(db)+"."+source.Ident(from)+" TO "+source.Ident(db)+"."+source.Ident(to)); err != nil {
		log.Warn("Source rename not applied", "err", err)
		return
	}
	nd := d
	nd.Name = to
	if nd.Source != nil {
		m := *nd.Source
		m.Table = to
		nd.Source = &m
	}
	_, _ = p.api.Catalog().ApplySource(core.SourceChange{Kind: core.SourceTableRenamed, ID: d.ID, Table: nd})
	p.record(db, to, "table renamed from "+from, "Delta rows kept", nil)
}

// renames pairs one dropped and one added table with identical definitions
// apart from the name; anything less is a drop and an add.
func (p *Poller) renames(ctx context.Context, db string, added, dropped []string) map[string]string {
	out := map[string]string{}
	if len(added) != 1 || len(dropped) != 1 {
		return out
	}
	old, ok := p.api.Catalog().TableByName(db, dropped[0])
	if !ok || old.Source == nil {
		return out
	}
	nd, _, err := p.disc.Table(ctx, db, added[0], p.visible)
	if err != nil {
		return out
	}
	norm := func(s, name string) string { return strings.Replace(s, "`"+name+"`", "`T`", 1) }
	if norm(discoveryStrip(old.Source.Definition), dropped[0]) == norm(discoveryStrip(nd.Source.Definition), added[0]) {
		out[dropped[0]] = added[0]
	}
	return out
}

func discoveryStrip(s string) string {
	return strings.TrimSpace(strings.Split(s, ") ENGINE")[0])
}

// change applies a changed table: recreated when it has no Delta rows and no
// local changes, otherwise altered column by column and index by index.
func (p *Poller) change(ctx context.Context, db, table string) {
	nd, warns, err := p.disc.Table(ctx, db, table, p.visible)
	if err != nil {
		log.Warn("changed Source table not read", "err", err)
		return
	}
	d, n, ok := p.deltaState(db, table)
	if !ok {
		return
	}
	if n == 0 && len(d.LocalChanges) == 0 {
		nd.Kind = d.Kind
		if _, err := p.api.Catalog().ApplySource(core.SourceChange{Kind: core.SourceTableChanged, ID: d.ID, Table: *nd}); err != nil {
			return
		}
		if err := p.exec(ctx, db, control.DropTable(db, table), nd.Definition); err != nil {
			log.Warn("changed Source table not recreated", "err", err)
			return
		}
		p.record(db, table, "table recreated from the new Source definition", "none", warns)
		return
	}
	stmts, removed, desc := p.alters(&d, nd)
	if len(stmts) == 0 {
		return
	}
	if d.Source != nil {
		d.Source.Definition, d.Source.Fingerprint = nd.Source.Definition, nd.Source.Fingerprint
	}
	if err := p.exec(ctx, db, stmts...); err != nil {
		log.Warn("Source change not applied", "table", db+"."+table, "err", err)
		p.record(db, table, "change not applied: "+err.Error(), "none", nil)
		return
	}
	effect := "Delta rows converted"
	if len(removed) > 0 {
		effect = "conflicting local changes removed"
	}
	p.record(db, table, desc, effect, removed)
}

// alters renders one ALTER TABLE per change between the current definition
// and the new Source definition. Local changes that conflict (same column or
// index name) are removed first.
func (p *Poller) alters(cur *core.TableDef, nd *core.TableDef) ([]string, []string, string) {
	oldCols := columnLines(p.parser, cur.Source.Definition)
	newCols := columnLines(p.parser, nd.Source.Definition)
	oldIdx := indexLines(p.parser, cur.Source.Definition)
	newIdx := indexLines(p.parser, nd.Source.Definition)
	t := source.Ident(cur.Database) + "." + source.Ident(cur.Name)
	var stmts, removed, desc []string
	local := map[string]bool{}
	for _, lc := range cur.LocalChanges {
		local[strings.ToLower(lc.Object)] = true
	}
	for name, line := range newCols {
		if _, ok := oldCols[name]; !ok {
			if local[name] {
				stmts = append(stmts, "ALTER TABLE "+t+" DROP COLUMN "+source.Ident(name))
				removed = append(removed, "local column "+name)
			}
			stmts = append(stmts, "ALTER TABLE "+t+" ADD COLUMN "+line)
			desc = append(desc, "column "+name+" added")
		} else if oldCols[name] != line {
			stmts = append(stmts, "ALTER TABLE "+t+" MODIFY COLUMN "+line)
			desc = append(desc, "column "+name+" changed")
		}
	}
	for name := range oldCols {
		if _, ok := newCols[name]; !ok {
			stmts = append(stmts, "ALTER TABLE "+t+" DROP COLUMN "+source.Ident(name))
			desc = append(desc, "column "+name+" dropped")
		}
	}
	for name, line := range newIdx {
		if old, ok := oldIdx[name]; !ok || old != line {
			if ok || local[name] {
				stmts = append(stmts, "ALTER TABLE "+t+" DROP INDEX "+source.Ident(name))
				if local[name] && !ok {
					removed = append(removed, "local index "+name)
				}
			}
			stmts = append(stmts, "ALTER TABLE "+t+" ADD "+line)
			desc = append(desc, "index "+name+" changed")
		}
	}
	for name := range oldIdx {
		if _, ok := newIdx[name]; !ok {
			stmts = append(stmts, "ALTER TABLE "+t+" DROP INDEX "+source.Ident(name))
			desc = append(desc, "index "+name+" dropped")
		}
	}
	sort.Strings(desc)
	return stmts, removed, strings.Join(desc, "; ")
}

func parseCreate(p *sqlparser.Parser, create string) *sqlparser.CreateTable {
	stmt, err := p.Parse(create)
	if err != nil {
		return nil
	}
	ct, _ := stmt.(*sqlparser.CreateTable)
	return ct
}

func columnLines(p *sqlparser.Parser, create string) map[string]string {
	out := map[string]string{}
	ct := parseCreate(p, create)
	if ct == nil || ct.TableSpec == nil {
		return out
	}
	for _, c := range ct.TableSpec.Columns {
		out[strings.ToLower(c.Name.String())] = sqlparser.String(c)
	}
	return out
}

func indexLines(p *sqlparser.Parser, create string) map[string]string {
	out := map[string]string{}
	ct := parseCreate(p, create)
	if ct == nil || ct.TableSpec == nil {
		return out
	}
	for _, ix := range ct.TableSpec.Indexes {
		if ix.Info == nil || ix.Info.Type == sqlparser.IndexTypeFullText || ix.Info.Type == sqlparser.IndexTypeSpatial {
			continue
		}
		name := strings.ToLower(ix.Info.Name.String())
		if ix.Info.Type == sqlparser.IndexTypePrimary {
			name = "primary"
		}
		out[name] = sqlparser.String(ix)
	}
	return out
}

// objectPrints compares views and routines and applies changes.
func (p *Poller) objectPrints(ctx context.Context, db string, init bool) {
	q := source.Quote(db)
	rows, err := p.r.Background(ctx, source.RenderCatalog("SELECT 'view', TABLE_NAME, MD5(VIEW_DEFINITION) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = "+q+
		" UNION ALL SELECT ROUTINE_TYPE, ROUTINE_NAME, CAST(LAST_ALTERED AS CHAR) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = "+q))
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, r := range rows {
		key := strings.ToLower(str(r[0])) + " " + strings.ToLower(db) + "." + str(r[1])
		seen[key] = true
		p.mu.Lock()
		old, ok := p.objs[key]
		p.objs[key] = str(r[2])
		p.mu.Unlock()
		if init || (ok && old == str(r[2])) {
			continue
		}
		objs, _ := p.disc.Objects(ctx, db)
		for _, o := range objs {
			if strings.EqualFold(o.Name, str(r[1])) && strings.EqualFold(o.Kind.String(), str(r[0])) {
				p.applyObject(ctx, o)
			}
		}
	}
	if init {
		return
	}
	p.mu.Lock()
	var gone []string
	for k := range p.objs {
		if strings.Contains(k, " "+strings.ToLower(db)+".") && !seen[k] {
			gone = append(gone, k)
		}
	}
	for _, k := range gone {
		delete(p.objs, k)
	}
	p.mu.Unlock()
	for _, k := range gone {
		f := strings.SplitN(k, " ", 2)
		name := strings.SplitN(f[1], ".", 2)[1]
		word := strings.ToUpper(f[0])
		_ = p.exec(ctx, db, "DROP "+word+" IF EXISTS "+source.Ident(name))
		kind := core.View
		switch word {
		case "PROCEDURE":
			kind = core.Procedure
		case "FUNCTION":
			kind = core.Function
		}
		_, _ = p.api.Catalog().ApplySource(core.SourceChange{Kind: core.SourceObjectDropped, Object: core.Object{Kind: kind, Database: db, Name: name}})
		p.record(db, name, strings.ToLower(word)+" dropped", "none", nil)
	}
}

func (p *Poller) applyObject(ctx context.Context, o core.Object) {
	stmts := []string{}
	switch o.Kind {
	case core.View:
		stmts = append(stmts, strings.Replace(o.Statement, "CREATE ", "CREATE OR REPLACE ", 1))
	case core.Procedure, core.Function:
		stmts = append(stmts, "DROP "+strings.ToUpper(o.Kind.String())+" IF EXISTS "+source.Ident(o.Name), o.Statement)
	}
	if err := p.exec(ctx, o.Database, stmts...); err != nil {
		log.Warn("Source object change not applied", "object", o.Database+"."+o.Name, "err", err)
		return
	}
	_, _ = p.api.Catalog().ApplySource(core.SourceChange{Kind: core.SourceObjectChanged, Object: o})
	p.record(o.Database, o.Name, fmt.Sprintf("%s changed", o.Kind), "none", nil)
}
