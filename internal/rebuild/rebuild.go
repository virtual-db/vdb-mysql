// Package rebuild builds the data directory's objects in order from the
// catalog of record (implementation plan doc 12 §3, product doc 09 §4):
// plug-ins checked, databases, accounts, tables (then the vdb schema's
// tables), Source views and routines, client objects in commit order with one
// retry, then privileges. Failures are recorded for vdb.catalog_errors.
package rebuild

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/control"
)

var log = slog.With("pkg", "rebuild")

// Control is what the rebuild needs from the control channel.
type Control interface {
	control.Exec
	Session(ctx context.Context, fn func(control.Exec) error) error
}

// Grants is what the rebuild needs from the grant mirror.
type Grants interface {
	CatchAll() string
	Apply(ctx context.Context) error
}

// Error is one object that could not be created.
type Error struct {
	Type, Schema, Name, Error string
}

// Database is one database to create.
type Database struct {
	Name, Charset, Collation string
}

// Rebuilder runs rebuilds and keeps the last run's errors. Safe for concurrent use.
type Rebuilder struct {
	ctl       Control
	grants    Grants
	catalog   core.Catalog
	vdbTables []string // CREATE TABLE statements of the vdb schema
	dbs       func() []Database

	mu     sync.Mutex
	errors []Error
	extra  []Error // discovery's errors, kept with each rebuild's
}

// New returns a Rebuilder.
func New(ctl Control, g Grants, cat core.Catalog, vdbTables []string, dbs func() []Database) *Rebuilder {
	return &Rebuilder{ctl: ctl, grants: g, catalog: cat, vdbTables: vdbTables, dbs: dbs}
}

// AddErrors records errors found outside the rebuild (discovery).
func (r *Rebuilder) AddErrors(es []Error) {
	r.mu.Lock()
	r.extra = append(r.extra, es...)
	r.mu.Unlock()
}

// Errors returns the rows of vdb.catalog_errors.
func (r *Rebuilder) Errors() []Error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(append([]Error(nil), r.extra...), r.errors...)
}

func (r *Rebuilder) fail(e Error) {
	log.Warn("object not created", "type", e.Type, "object", e.Schema+"."+e.Name, "err", e.Error)
	r.errors = append(r.errors, e)
}

// Run rebuilds every object.
func (r *Rebuilder) Run(ctx context.Context) error {
	r.mu.Lock()
	r.errors = nil
	r.mu.Unlock()
	// 1. The four plug-ins.
	if err := control.CheckPlugins(ctx, r.ctl); err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}
	// 2. Databases, and vdb.
	tables := r.catalog.Tables()
	seen := map[string]bool{}
	for _, d := range r.dbs() {
		seen[strings.ToLower(d.Name)] = true
		if err := r.ctl.Exec(ctx, control.CreateDatabase(d.Name, d.Charset, d.Collation)); err != nil {
			return fmt.Errorf("rebuild: %w", err)
		}
	}
	for _, t := range tables {
		if !seen[strings.ToLower(t.Database)] {
			seen[strings.ToLower(t.Database)] = true
			if err := r.ctl.Exec(ctx, control.CreateDatabase(t.Database, "", "")); err != nil {
				return fmt.Errorf("rebuild: %w", err)
			}
		}
	}
	if err := r.ctl.Exec(ctx, control.CreateDatabase("vdb", "utf8mb4", "utf8mb4_0900_ai_ci")); err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}
	// 3. Accounts: the catch-all, then the mirrored accounts.
	if r.grants != nil {
		if err := r.ctl.Exec(ctx, r.grants.CatchAll()); err != nil {
			return fmt.Errorf("rebuild: catch-all: %w", err)
		}
		if err := r.grants.Apply(ctx); err != nil {
			log.Warn("some account statements failed before tables existed; retried after", "err", err)
		}
	}
	// 4. Tables, with foreign_key_checks=0, then the vdb schema's tables.
	sort.Slice(tables, func(i, j int) bool { return tables[i].ID < tables[j].ID })
	err := r.ctl.Session(ctx, func(e control.Exec) error {
		if err := e.Exec(ctx, control.SetSession("foreign_key_checks", "0")); err != nil {
			return err
		}
		for _, t := range tables {
			if t.Kind == core.Provided || strings.EqualFold(t.Database, "vdb") {
				continue
			}
			if err := e.Exec(ctx, control.Use(t.Database)); err != nil {
				return err
			}
			if err := e.Exec(ctx, t.Definition); err != nil {
				r.mu.Lock()
				r.fail(Error{Type: "table", Schema: t.Database, Name: t.Name, Error: err.Error()})
				r.mu.Unlock()
			}
		}
		if err := e.Exec(ctx, control.Use("vdb")); err != nil {
			return err
		}
		for _, stmt := range r.vdbTables {
			if err := e.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("vdb schema: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("rebuild: tables: %w", err)
	}
	// 5 and 6. Source objects, then client objects in commit order; failures retried once.
	objs := r.catalog.Objects()
	sort.SliceStable(objs, func(i, j int) bool {
		if objs[i].Origin != objs[j].Origin {
			return objs[i].Origin == core.ObjectFromSource
		}
		return objs[i].Seq < objs[j].Seq
	})
	var retry []core.Object
	for _, o := range objs {
		if err := r.object(ctx, o); err != nil {
			retry = append(retry, o)
		}
	}
	for _, o := range retry {
		if err := r.object(ctx, o); err != nil {
			r.mu.Lock()
			r.fail(Error{Type: o.Kind.String(), Schema: o.Database, Name: o.Name, Error: err.Error()})
			r.mu.Unlock()
		}
	}
	// 7. Privileges, now that every object exists.
	if r.grants != nil {
		if err := r.grants.Apply(ctx); err != nil {
			log.Warn("some grant statements failed", "err", err)
		}
	}
	return nil
}

// object creates one view, routine or trigger with its database and sql_mode.
func (r *Rebuilder) object(ctx context.Context, o core.Object) error {
	return r.ctl.Session(ctx, func(e control.Exec) error {
		if err := e.Exec(ctx, control.Use(o.Database)); err != nil {
			return err
		}
		if o.SQLMode != "" {
			if err := e.Exec(ctx, control.SetSession("sql_mode", control.Str(o.SQLMode))); err != nil {
				return err
			}
		}
		return e.Exec(ctx, WithDefiner(o.Statement, o.Definer))
	})
}

var definerRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(OR\s+REPLACE\s+)?(ALGORITHM\s*=\s*\w+\s+)?`)

// WithDefiner adds DEFINER=<definer> to a captured CREATE statement that
// names none, so the control session does not become the definer at rebuild.
func WithDefiner(stmt, definer string) string {
	if definer == "" || strings.Contains(strings.ToUpper(stmt), "DEFINER") {
		return stmt
	}
	loc := definerRe.FindStringIndex(stmt)
	if loc == nil {
		return stmt
	}
	return stmt[:loc[1]] + "DEFINER=" + definer + " " + stmt[loc[1]:]
}
