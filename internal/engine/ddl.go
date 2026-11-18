// Package engine — ddl.go
//
// Catalog and Alter messages (doc 09 §3.2): definitions from mysqld become
// core.TableDefs; client DDL is staged with ApplyDDL, control-session DDL
// with ApplySourceDDL; copies are refused (D-45).
package engine

import (
	"strings"

	core "github.com/virtual-db/vdb-core"
	"google.golang.org/protobuf/proto"

	"github.com/virtual-db/vdb-mysql/protocol"
)

// toDef turns mysqld's definition into a TableDef. Indexes keep mysqld's key
// numbers; the identity is the primary key, else the first unique key whose
// columns are all NOT NULL, else the whole row (D-10).
func (h *Handler) toDef(t *protocol.TableDefinition) core.TableDef {
	d := core.TableDef{Database: t.Database, Name: t.Name, Definition: t.Definition, Identity: core.WholeRow}
	for i, c := range t.Columns {
		col := core.ColumnDef{Name: c.Name, Pos: uint16(i), Type: c.Type, Charset: c.Charset, Collation: c.Collation,
			Nullable: c.Nullable, Timestamp: c.Timestamp, Float: c.Float, AutoIncrement: c.AutoIncrement}
		if c.VirtualGen {
			col.Fetch = core.Virtual
		}
		if c.HasDefault && c.DefaultValue != nil {
			col.Default = core.Value{B: c.DefaultValue.B, Null: c.DefaultValue.Null}
		} else {
			col.Default = core.Value{Null: true}
		}
		d.Columns = append(d.Columns, col)
	}
	for i, ix := range t.Indexes {
		if ix.FulltextOrSpatial {
			continue
		}
		def := core.IndexDef{ID: core.IndexID(i), Name: ix.Name, Primary: ix.Primary, Unique: ix.Unique || ix.Primary,
			OrderProven: true, EqualityProven: true}
		notNull := true
		for k, c := range ix.Columns {
			def.Columns = append(def.Columns, uint16(c))
			if k < len(ix.PartLens) {
				def.PartLens = append(def.PartLens, ix.PartLens[k])
			}
			coll := ""
			if int(c) < len(d.Columns) {
				coll = d.Columns[c].Collation
				if d.Columns[c].Nullable {
					notNull = false
				}
			}
			if k == 0 && !h.proven.Order(coll) {
				def.OrderProven = false
			}
			if !h.proven.Equality(coll) {
				def.EqualityProven = false
			}
		}
		if d.Identity == core.WholeRow && (def.Primary || (def.Unique && notNull)) {
			d.Identity = def.ID
		}
		d.Indexes = append(d.Indexes, def)
	}
	for _, f := range t.ForeignKeys {
		fk := core.ForeignKeyDef{Name: f.Name, ParentDB: f.ParentDb, ParentTable: f.ParentTable, ParentColumns: f.ParentColumns,
			OnDelete: fkAction(f.OnDelete), OnUpdate: fkAction(f.OnUpdate), ChildIndex: core.IndexID(f.ChildIndex)}
		for _, c := range f.Columns {
			fk.Columns = append(fk.Columns, uint16(c))
		}
		d.ForeignKeys = append(d.ForeignKeys, fk)
	}
	h.applyTransforms(&d)
	return d
}

// applyTransforms marks columns a plugin transforms and declares their
// indexes unordered (doc 06 §5.3).
func (h *Handler) applyTransforms(d *core.TableDef) {
	tr := h.api.Transforms()
	if len(tr) == 0 {
		return
	}
	set := map[string]bool{}
	for _, t := range tr {
		set[strings.ToLower(t)] = true
	}
	for i, c := range d.Columns {
		if set[strings.ToLower(d.Database+"."+d.Name+"."+c.Name)] && c.Fetch == core.FromSource {
			d.Columns[i].Fetch = core.Transformed
		}
	}
	for i, ix := range d.Indexes {
		for _, c := range ix.Columns {
			if d.Columns[c].Fetch == core.Transformed {
				d.Indexes[i].OrderProven = false
			}
		}
	}
}

// keepFetch carries fetch modes over from the old definition: a column that
// exists on the Source stays FromSource; a column added by a client is
// filled with its default for Source rows.
func keepFetch(old *core.TableDef, d *core.TableDef, fromSource bool) {
	for i, c := range d.Columns {
		if c.Fetch == core.Virtual {
			continue
		}
		j := old.ColumnByName(c.Name)
		switch {
		case j >= 0:
			if old.Columns[j].Fetch != core.Virtual {
				d.Columns[i].Fetch = old.Columns[j].Fetch
			}
		case fromSource || old.Kind != core.Mirrored:
			d.Columns[i].Fetch = core.FromSource
		default:
			d.Columns[i].Fetch = core.Default
		}
	}
}

func (c *conn) catalog(m *protocol.Catalog) (proto.Message, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	cat := c.h.api.Catalog()
	switch m.Kind {
	case protocol.CatalogKind_CATALOG_CREATE:
		if m.Table == nil {
			return nil, &core.Error{Code: core.ErrRefused, Msg: "CREATE without a definition"}
		}
		def := c.h.toDef(m.Table)
		if existing, ok := cat.TableByName(def.Database, def.Name); ok && c.control {
			// Rebuild: the table is already in the catalog. Keep its id, kind
			// and Source mapping; take mysqld's own definition and key layout.
			def.ID, def.Kind, def.Source, def.LocalChanges = existing.ID, existing.Kind, existing.Source, existing.LocalChanges
			def.Version = existing.Version
			keepFetch(&existing, &def, false)
			for i := range def.Columns {
				if j := existing.ColumnByName(def.Columns[i].Name); j >= 0 && existing.Columns[j].Fetch == core.Default {
					def.Columns[i].Default = existing.Columns[j].Default
				}
			}
			if _, err := cat.ApplySource(core.SourceChange{Kind: core.SourceStatsRefreshed, ID: existing.ID, Table: def}); err != nil {
				return nil, err
			}
			return &protocol.CatalogReply{Table: uint32(existing.ID), Version: existing.Version}, nil
		}
		def.Kind = core.Local
		if strings.EqualFold(def.Database, "vdb") {
			def.Kind = core.Provided
		}
		for i := range def.Columns {
			if def.Columns[i].Fetch != core.Virtual {
				def.Columns[i].Fetch = core.FromSource
			}
		}
		ch := core.CatalogChange{Kind: core.CreateTable, Table: def}
		var id core.TableID
		if c.control {
			id, err = s.ApplySourceDDL(ch)
		} else {
			id, err = s.ApplyDDL(ch)
		}
		if err != nil {
			return nil, err
		}
		return &protocol.CatalogReply{Table: uint32(id), Version: 1}, nil
	case protocol.CatalogKind_CATALOG_DROP:
		id := core.TableID(m.TableId)
		if id == 0 && m.Table != nil {
			if d, ok := cat.TableByName(m.Table.Database, m.Table.Name); ok {
				id = d.ID
			}
		}
		if id == 0 {
			return &protocol.CatalogReply{}, nil
		}
		ch := core.CatalogChange{Kind: core.DropTable, ID: id}
		if c.control {
			_, err = s.ApplySourceDDL(ch)
		} else {
			_, err = s.ApplyDDL(ch)
		}
		return &protocol.CatalogReply{Table: uint32(id)}, err
	case protocol.CatalogKind_CATALOG_RENAME:
		id := core.TableID(m.TableId)
		if id == 0 && m.Table != nil {
			if d, ok := cat.TableByName(m.Table.Database, m.Table.Name); ok {
				id = d.ID
			}
		}
		ch := core.CatalogChange{Kind: core.RenameTable, ID: id, NewDB: m.NewDatabase, NewName: m.NewName}
		if m.Table != nil {
			ch.Table.Definition = m.Table.Definition
		}
		if c.control {
			_, err = s.ApplySourceDDL(ch)
		} else {
			_, err = s.ApplyDDL(ch)
		}
		return &protocol.CatalogReply{Table: uint32(id)}, err
	}
	return nil, &protoErr{msg: "unknown catalog kind"}
}

var localKinds = map[protocol.AlterKind]core.LocalChangeKind{
	protocol.AlterKind_ALTER_ADD_COLUMN: core.AddColumn, protocol.AlterKind_ALTER_DROP_COLUMN: core.DropColumn,
	protocol.AlterKind_ALTER_ADD_INDEX: core.AddIndex, protocol.AlterKind_ALTER_DROP_INDEX: core.DropIndex,
	protocol.AlterKind_ALTER_CHANGE_DEFAULT: core.ChangeDefault, protocol.AlterKind_ALTER_ADD_FOREIGN_KEY: core.AddForeignKey,
	protocol.AlterKind_ALTER_DROP_FOREIGN_KEY: core.DropForeignKey,
}

// alter decides an in-place ALTER: instant changes are accepted; renames,
// type and key changes need a copy and are refused for client sessions
// (D-45), and need row conversion for control sessions (doc 12 §5.3).
func (c *conn) alter(m *protocol.Alter) (proto.Message, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	old, ok := c.h.api.Catalog().Table(core.TableID(m.Table))
	if !ok {
		return nil, &core.Error{Code: core.ErrRefused, Msg: "ALTER of a table not in the catalog of record"}
	}
	verdict := protocol.AlterVerdict_ALTER_OK
	for _, k := range m.Kinds {
		switch k {
		case protocol.AlterKind_ALTER_RENAME_COLUMN, protocol.AlterKind_ALTER_CHANGE_TYPE, protocol.AlterKind_ALTER_CHANGE_KEY:
			if !c.control && old.Kind == core.Mirrored {
				return &protocol.AlterReply{Verdict: protocol.AlterVerdict_ALTER_NEEDS_COPY,
					Message: "this ALTER TABLE needs a table copy, which would move the whole Source table into the Delta; VDB refuses it (D-45)"}, nil
			}
			verdict = protocol.AlterVerdict_ALTER_NEEDS_CONVERSION
		}
	}
	if m.CopyNeeded && !c.control && old.Kind == core.Mirrored {
		return &protocol.AlterReply{Verdict: protocol.AlterVerdict_ALTER_NEEDS_COPY,
			Message: "this ALTER TABLE needs a table copy, which VDB refuses on tables read from the Source (D-45)"}, nil
	}
	def := old
	if m.TableDef != nil {
		def = c.h.toDef(m.TableDef)
		keepFetch(&old, &def, c.control)
		def.Source = old.Source
	}
	ch := core.CatalogChange{Kind: core.AlterTable, ID: old.ID, Table: def}
	if len(m.Kinds) > 0 {
		lk, ok := localKinds[m.Kinds[0]]
		if !ok {
			lk = core.OtherChange
		}
		ch.Local = &core.LocalChange{Kind: lk, Object: m.Object, Statement: m.Statement}
	}
	if c.control {
		_, err = s.ApplySourceDDL(ch)
	} else {
		_, err = s.ApplyDDL(ch)
	}
	if err != nil {
		return nil, err
	}
	return &protocol.AlterReply{Verdict: verdict, Version: old.Version + 1}, nil
}
