// Package metrics keeps counters and gauges in memory and exposes them as
// rows of vdb.metrics (implementation plan doc 13 §4, D-83). Nothing listens:
// metrics are read with SQL only.
package metrics

import (
	"sort"
	"strings"
	"sync"
)

// Registry holds metrics. Safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	values  map[string]float64 // name + "\x00" + labels
	sources []func(set func(name, labels string, v float64))
}

// New returns an empty registry.
func New() *Registry { return &Registry{values: map[string]float64{}} }

// Add adds n to a counter.
func (r *Registry) Add(name, labels string, n float64) {
	r.mu.Lock()
	r.values[name+"\x00"+labels] += n
	r.mu.Unlock()
}

// Set sets a gauge.
func (r *Registry) Set(name, labels string, v float64) {
	r.mu.Lock()
	r.values[name+"\x00"+labels] = v
	r.mu.Unlock()
}

// Source registers a function that sets gauges when metrics are read, for
// numbers owned elsewhere (Delta size, lock entries, request log totals).
func (r *Registry) Source(f func(set func(name, labels string, v float64))) {
	r.mu.Lock()
	r.sources = append(r.sources, f)
	r.mu.Unlock()
}

// Row is one metric.
type Row struct {
	Name, Labels string
	Value        float64
}

// Rows returns every metric, sorted.
func (r *Registry) Rows() []Row {
	r.mu.Lock()
	srcs := append([]func(func(string, string, float64)){}, r.sources...)
	r.mu.Unlock()
	for _, f := range srcs {
		f(r.Set)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Row, 0, len(r.values))
	for k, v := range r.values {
		i := strings.IndexByte(k, 0)
		out = append(out, Row{Name: k[:i], Labels: k[i+1:], Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Labels < out[j].Labels
	})
	return out
}
