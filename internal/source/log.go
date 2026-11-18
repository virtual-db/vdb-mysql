// Package source — log.go
//
// The source-request log (doc 10 §5): a ring buffer of the last records,
// read by vdb.source_requests; an optional JSON-lines file; the
// vdb.source.requested event; and the counters metrics aggregate.
package source

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Record is one Source request.
type Record struct {
	ConnID   uint64        `json:"connection_id"`
	Digest   string        `json:"digest"`
	Shape    string        `json:"shape"`
	Schema   string        `json:"table_schema"`
	Table    string        `json:"table_name"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration_us"`
	Rows     int64         `json:"rows"`
	Bytes    int64         `json:"bytes"`
	Error    string        `json:"error,omitempty"`
}

// Log is the request log. Safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	ring    []Record
	next    int
	full    bool
	file    *os.File
	emit    func(Record)
	Count   atomic.Int64
	RowsN   atomic.Int64
	BytesN  atomic.Int64
	Errors  atomic.Int64
	TotalUs atomic.Int64
}

// NewLog returns a log of size records, writing to path when set.
func NewLog(size int, path string, emit func(Record)) *Log {
	if size <= 0 {
		size = 10000
	}
	l := &Log{ring: make([]Record, size), emit: emit}
	if path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			l.file = f
		}
	}
	return l
}

// Add appends a record.
func (l *Log) Add(r Record) {
	l.Count.Add(1)
	l.RowsN.Add(r.Rows)
	l.BytesN.Add(r.Bytes)
	l.TotalUs.Add(r.Duration.Microseconds())
	if r.Error != "" {
		l.Errors.Add(1)
	}
	l.mu.Lock()
	l.ring[l.next] = r
	l.next = (l.next + 1) % len(l.ring)
	if l.next == 0 {
		l.full = true
	}
	if l.file != nil {
		if b, err := json.Marshal(r); err == nil {
			l.file.Write(append(b, '\n'))
		}
	}
	l.mu.Unlock()
	if l.emit != nil {
		l.emit(r)
	}
}

// Records returns the buffered records, oldest first.
func (l *Log) Records() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.full {
		return append([]Record(nil), l.ring[:l.next]...)
	}
	out := append([]Record(nil), l.ring[l.next:]...)
	return append(out, l.ring[:l.next]...)
}
