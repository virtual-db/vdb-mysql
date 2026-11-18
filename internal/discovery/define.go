// Package discovery — define.go
//
// Turning a Source CREATE TABLE into the definition mysqld creates (doc 12
// §4.2, product doc 09 §2.2): ENGINE=vdb_mysql_storage; AUTO_INCREMENT,
// partitioning, tablespace, DATA DIRECTORY, INDEX DIRECTORY, row format,
// compression and encryption removed; FULLTEXT and spatial indexes removed
// with a warning; foreign keys to tables the service account cannot see
// removed with a warning. Columns, types, defaults, generated columns, CHECK
// constraints, character sets, collations and comments are kept: the rewrite
// removes clauses from the AST and never re-types a column.
package discovery

import (
	"fmt"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// Engine is the storage engine every VDB table uses.
const Engine = "vdb_mysql_storage"

var dropOptions = map[string]bool{
	"auto_increment": true, "tablespace": true, "data directory": true, "index directory": true,
	"row_format": true, "compression": true, "encryption": true, "key_block_size": true,
	"stats_persistent": true, "stats_auto_recalc": true, "stats_sample_pages": true,
	"engine": true, "secondary_engine": true, "autoextend_size": true, "insert_method": true,
	"union": true, "delay_key_write": true, "pack_keys": true, "checksum": true,
}

// Define rewrites a Source CREATE TABLE. visible reports whether a referenced
// table can be seen by the service account; db is the table's database.
func Define(parser *sqlparser.Parser, create, db string, visible func(db, table string) bool) (string, []string, error) {
	stmt, err := parser.Parse(create)
	if err != nil {
		return "", nil, fmt.Errorf("discovery.define: %w", err)
	}
	ct, ok := stmt.(*sqlparser.CreateTable)
	if !ok || ct.TableSpec == nil {
		return "", nil, fmt.Errorf("discovery.define: not a CREATE TABLE")
	}
	var warnings []string
	spec := ct.TableSpec
	spec.PartitionOption = nil
	var opts sqlparser.TableOptions
	for _, o := range spec.Options {
		if dropOptions[strings.ToLower(o.Name)] {
			continue
		}
		opts = append(opts, o)
	}
	opts = append(opts, &sqlparser.TableOption{Name: "ENGINE", String: Engine})
	spec.Options = opts
	var idx []*sqlparser.IndexDefinition
	for _, ix := range spec.Indexes {
		if ix.Info != nil && (ix.Info.Type == sqlparser.IndexTypeFullText || ix.Info.Type == sqlparser.IndexTypeSpatial) {
			warnings = append(warnings, "index "+ix.Info.Name.String()+" removed: FULLTEXT and spatial indexes are not supported")
			continue
		}
		idx = append(idx, ix)
	}
	spec.Indexes = idx
	var cons []*sqlparser.ConstraintDefinition
	for _, c := range spec.Constraints {
		if fk, ok := c.Details.(*sqlparser.ForeignKeyDefinition); ok && fk.ReferenceDefinition != nil {
			rt := fk.ReferenceDefinition.ReferencedTable
			rdb := db
			if !rt.Qualifier.IsEmpty() {
				rdb = rt.Qualifier.String()
			}
			if visible != nil && !visible(rdb, rt.Name.String()) {
				warnings = append(warnings, "foreign key "+c.Name.String()+" removed: it references "+rdb+"."+rt.Name.String()+", which the service account cannot see")
				continue
			}
		}
		cons = append(cons, c)
	}
	spec.Constraints = cons
	ct.Table.Qualifier = sqlparser.IdentifierCS{}
	return sqlparser.String(ct), warnings, nil
}
