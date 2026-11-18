// Package vdbschema provides the vdb schema's tables (implementation plan doc
// 13 §1): each is an ordinary VDB table in database vdb, created at every
// rebuild from tables.sql and marked Provided; a scan calls its provider
// with the reader's SessionInfo, and rows are produced on each read and never
// stored. Rows about sessions and requests are filtered to the reader's own
// account unless the reader's Source account holds PROCESS.
package vdbschema

import (
	_ "embed"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	core "github.com/virtual-db/vdb-core"
)

//go:embed tables.sql
var tablesSQL string

// Tables returns the CREATE TABLE statements of the vdb schema.
func Tables() []string {
	var out []string
	for _, s := range strings.Split(tablesSQL, ";\n") {
		if s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Rows produces a table's full rows for a reader.
type Rows func(reader core.SessionInfo) [][]string

// Provider is one table's provider.
type Provider struct {
	rows  Rows
	nulls map[int]bool // columns where "" means NULL
}

// Rows implements core.RowProvider.
func (p *Provider) Rows(s core.SessionInfo, cols core.ColumnSet) (core.Cursor, error) {
	var out []core.Row
	for _, r := range p.rows(s) {
		row := make(core.Row, len(r))
		for i, v := range r {
			if v == "\x00NULL" || (p.nulls[i] && v == "") {
				row[i] = core.Value{Null: true}
			} else {
				row[i] = core.Value{B: []byte(v)}
			}
		}
		out = append(out, core.Project(row, cols))
	}
	return &cursor{rows: out}, nil
}

type cursor struct {
	rows []core.Row
	i    int
}

func (c *cursor) Next(maxRows, _ int) (core.Batch, error) {
	var b core.Batch
	for c.i < len(c.rows) && (maxRows <= 0 || len(b.Rows) < maxRows) {
		b.Rows = append(b.Rows, c.rows[c.i])
		b.Refs = append(b.Refs, core.RowRef{Origin: core.OriginDelta, PK: core.Key{Image: []byte(strconv.Itoa(c.i))}})
		c.i++
	}
	if len(b.Rows) == 0 {
		return b, io.EOF
	}
	return b, nil
}

func (c *cursor) Close() error { return nil }

// NULL is the marker for a NULL value in a provider's rows.
const NULL = "\x00NULL"

// Time renders a time for a datetime(6) column, or NULL for the zero time.
func Time(t time.Time) string {
	if t.IsZero() {
		return NULL
	}
	return t.UTC().Format("2006-01-02 15:04:05.000000")
}

// Register marks every vdb table Provided and installs its provider.
func Register(cat core.Catalog, providers map[string]Rows) {
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if d, ok := cat.TableByName("vdb", n); ok {
			cat.Provide(d.ID, &Provider{rows: providers[n]})
		}
	}
}

// Visible reports whether a reader may see a row about account owner.
func Visible(reader core.SessionInfo, owner string, hasProcess func(string) bool) bool {
	if reader.Control || hasProcess(reader.Account) {
		return true
	}
	return normalize(reader.Account) == normalize(owner)
}

func normalize(a string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(a, "'", ""), "`", ""))
}
