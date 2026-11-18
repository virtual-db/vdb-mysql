// Package stats answers the engine's statistics requests (implementation plan
// doc 10 §6, product doc 04 §5): Source table and index statistics refreshed
// by the schema poller, adjusted with exact Delta counts; range estimates from
// a cache of EXPLAIN results; and batch costs measured from the request log.
package stats

import (
	"context"
	"strings"
	"sync"
	"time"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/source"
)

// Explainer asks the Source for a range estimate.
type Explainer interface {
	ExplainRange(ctx context.Context, d *core.TableDef, idx core.IndexID, r core.KeyRange) (uint64, error)
	ReadTableStats(ctx context.Context, db string) (map[string]*source.TableStats, error)
	ThreadsRunning(ctx context.Context) int
}

// Reply is one statistics answer.
type Reply struct {
	Records         uint64
	MeanRecLength   uint64
	MrrLengthPerRec uint64
	RecPerKey       map[core.IndexID][]float64
	RecordsInRange  uint64
	BatchCost       float64
	DataFileLength  uint64
}

// Stats keeps statistics. Safe for concurrent use.
type Stats struct {
	src         Explainer
	api         core.DriverAPI
	busyThreads int

	mu      sync.Mutex
	tables  map[string]*source.TableStats // "db.table" lower case
	explain map[string]uint64
	cost    map[core.TableID]float64
	rowLen  map[core.TableID]float64
}

// New returns a Stats.
func New(src Explainer, api core.DriverAPI, busyThreads int) *Stats {
	return &Stats{src: src, api: api, busyThreads: busyThreads, tables: map[string]*source.TableStats{},
		explain: map[string]uint64{}, cost: map[core.TableID]float64{}, rowLen: map[core.TableID]float64{}}
}

// Refresh re-reads the Source statistics of a database. Background reads
// pause while the Source is busy (product doc 08 §10).
func (s *Stats) Refresh(ctx context.Context, db string) error {
	if s.busyThreads > 0 {
		if n := s.src.ThreadsRunning(ctx); n > s.busyThreads {
			return nil
		}
	}
	ts, err := s.src.ReadTableStats(ctx, db)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for name, t := range ts {
		s.tables[strings.ToLower(db)+"."+name] = t
	}
	s.explain = map[string]uint64{}
	s.mu.Unlock()
	return nil
}

// Table returns a table's Source statistics.
func (s *Stats) Table(db, name string) (*source.TableStats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tables[strings.ToLower(db+"."+name)]
	return t, ok
}

// Observe records a measured batch: rows, bytes and round-trip time.
func (s *Stats) Observe(t core.TableID, rows int, bytes int, d time.Duration) {
	if rows == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cost := float64(d.Microseconds()) / 1000
	if c, ok := s.cost[t]; ok {
		s.cost[t] = 0.8*c + 0.2*cost
	} else {
		s.cost[t] = cost
	}
	rl := float64(bytes) / float64(rows)
	if r, ok := s.rowLen[t]; ok {
		s.rowLen[t] = 0.8*r + 0.2*rl
	} else {
		s.rowLen[t] = rl
	}
}

func explainKey(t core.TableID, idx core.IndexID, r core.KeyRange) string {
	var b strings.Builder
	b.WriteString(string(rune(t)))
	b.WriteByte('/')
	b.WriteString(string(rune(idx)))
	b.WriteByte('/')
	b.WriteString(string(rune(r.LowParts)))
	b.WriteString(string(rune(r.HighParts)))
	if len(r.RawLow) > 0 {
		b.WriteByte('L')
	}
	if len(r.RawHigh) > 0 {
		b.WriteByte('H')
	}
	if r.RawLowIncl {
		b.WriteByte('i')
	}
	if r.RawHighIncl {
		b.WriteByte('j')
	}
	return b.String()
}

// For answers a Stats request: table statistics, and records_in_range when a
// range is given.
func (s *Stats) For(ctx context.Context, d *core.TableDef, idx core.IndexID, r *core.KeyRange) Reply {
	var rep Reply
	var deltaRows, touched uint64
	for _, t := range s.api.DeltaStats().Tables {
		if t.Table == d.ID {
			deltaRows, touched = t.DeltaRows, t.TouchedKeys
		}
	}
	src, _ := s.Table(d.Database, d.Name)
	if d.Kind == core.Mirrored && src != nil {
		rep.Records = src.Rows
		rep.MeanRecLength = src.AvgRowLen
		rep.DataFileLength = src.DataLength
	}
	if rep.Records > touched {
		rep.Records -= touched
	} else if d.Kind == core.Mirrored {
		rep.Records = 0
	}
	rep.Records += deltaRows
	if rep.Records == 0 {
		rep.Records = 1
	}
	s.mu.Lock()
	if rl, ok := s.rowLen[d.ID]; ok && rl > 0 {
		rep.MeanRecLength = uint64(rl)
	}
	rep.BatchCost = s.cost[d.ID]
	s.mu.Unlock()
	if rep.MeanRecLength == 0 {
		rep.MeanRecLength = uint64(16 * len(d.Columns))
	}
	rep.MrrLengthPerRec = rep.MeanRecLength + 8
	if rep.BatchCost == 0 {
		rep.BatchCost = 1
	}
	rep.RecPerKey = map[core.IndexID][]float64{}
	for _, ix := range d.Indexes {
		var card []float64
		if src != nil {
			card = src.Cardinality[ix.Name]
		}
		rpk := make([]float64, len(ix.Columns))
		for i := range rpk {
			c := 0.0
			if i < len(card) {
				c = card[i]
			}
			if c > 0 {
				rpk[i] = float64(rep.Records) / c
			} else if ix.Unique && i == len(ix.Columns)-1 {
				rpk[i] = 1
			} else {
				rpk[i] = 10
			}
			if rpk[i] < 1 {
				rpk[i] = 1
			}
		}
		rep.RecPerKey[ix.ID] = rpk
	}
	if r != nil {
		rep.RecordsInRange = s.inRange(ctx, d, idx, *r, rep.Records)
	}
	return rep
}

func (s *Stats) inRange(ctx context.Context, d *core.TableDef, idx core.IndexID, r core.KeyRange, total uint64) uint64 {
	if d.Kind != core.Mirrored {
		return total/10 + 1
	}
	key := explainKey(d.ID, idx, r)
	s.mu.Lock()
	n, ok := s.explain[key]
	s.mu.Unlock()
	if !ok {
		var err error
		n, err = s.src.ExplainRange(ctx, d, idx, r)
		if err != nil {
			n = total/10 + 1
		}
		s.mu.Lock()
		s.explain[key] = n
		s.mu.Unlock()
	}
	if n == 0 {
		n = 1
	}
	return n
}
