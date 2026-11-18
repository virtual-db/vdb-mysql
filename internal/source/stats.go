// Package source — stats.go
//
// The Source reads behind statistics (doc 10 §6): table and index statistics
// from information_schema, and EXPLAIN FORMAT=JSON of a range request.
package source

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	core "github.com/virtual-db/vdb-core"
)

// TableStats are one table's Source statistics.
type TableStats struct {
	Rows        uint64
	AvgRowLen   uint64
	DataLength  uint64
	AutoInc     uint64
	Cardinality map[string][]float64 // index name -> cardinality per key prefix
}

// ReadTableStats reads TABLE_ROWS, AVG_ROW_LENGTH, DATA_LENGTH and
// AUTO_INCREMENT for every table of a database, and CARDINALITY per index prefix.
func (a *Adapter) ReadTableStats(ctx context.Context, db string) (map[string]*TableStats, error) {
	q := literalString(db)
	rows, err := a.Background(ctx, RenderCatalog("SELECT TABLE_NAME, IFNULL(TABLE_ROWS,0), IFNULL(AVG_ROW_LENGTH,0), IFNULL(DATA_LENGTH,0), IFNULL(AUTO_INCREMENT,0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+q+" AND TABLE_TYPE = 'BASE TABLE'"))
	if err != nil {
		return nil, err
	}
	out := map[string]*TableStats{}
	for _, r := range rows {
		ts := &TableStats{Cardinality: map[string][]float64{}}
		ts.Rows, _ = strconv.ParseUint(string(r[1].B), 10, 64)
		ts.AvgRowLen, _ = strconv.ParseUint(string(r[2].B), 10, 64)
		ts.DataLength, _ = strconv.ParseUint(string(r[3].B), 10, 64)
		ts.AutoInc, _ = strconv.ParseUint(string(r[4].B), 10, 64)
		out[strings.ToLower(string(r[0].B))] = ts
	}
	rows, err = a.Background(ctx, RenderCatalog("SELECT TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX, IFNULL(CARDINALITY,0) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = "+q+" ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX"))
	if err != nil {
		return out, nil
	}
	for _, r := range rows {
		ts := out[strings.ToLower(string(r[0].B))]
		if ts == nil {
			continue
		}
		card, _ := strconv.ParseFloat(string(r[3].B), 64)
		ix := string(r[1].B)
		ts.Cardinality[ix] = append(ts.Cardinality[ix], card)
	}
	return out, nil
}

// ExplainRange asks the Source for the estimated rows of a range read on an
// index: one EXPLAIN FORMAT=JSON on the background pool.
func (a *Adapter) ExplainRange(ctx context.Context, d *core.TableDef, idx core.IndexID, r core.KeyRange) (uint64, error) {
	p := project(d, nil)
	sh := renderPage(d, p, idx, []core.KeyRange{r}, nil, 0, "", core.Forward, 1000000000, true)
	rows, err := a.run(ctx, a.bgPool, 0, "", d, renderExplain(sh))
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	var doc any
	if err := json.Unmarshal(rows[0][0].B, &doc); err != nil {
		return 0, err
	}
	return findRows(doc), nil
}

// findRows returns the first rows_examined_per_scan in an EXPLAIN document.
func findRows(v any) uint64 {
	switch x := v.(type) {
	case map[string]any:
		if n, ok := x["rows_examined_per_scan"].(float64); ok {
			return uint64(n)
		}
		for _, y := range x {
			if n := findRows(y); n > 0 {
				return n
			}
		}
	case []any:
		for _, y := range x {
			if n := findRows(y); n > 0 {
				return n
			}
		}
	}
	return 0
}

// ThreadsRunning reads the Source's Threads_running, or -1 when not readable.
func (a *Adapter) ThreadsRunning(ctx context.Context) int {
	rows, err := a.Background(ctx, RenderCatalog("SHOW GLOBAL STATUS LIKE 'Threads_running'"))
	if err != nil || len(rows) == 0 {
		return -1
	}
	n, err := strconv.Atoi(string(rows[0][1].B))
	if err != nil {
		return -1
	}
	return n
}

func literalString(s string) string { return quote([]byte(s)) }

// Quote renders a string literal (catalog reads in other packages).
func Quote(s string) string { return quote([]byte(s)) }
