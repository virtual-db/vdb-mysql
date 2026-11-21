// Package oracle — concurrency.go
//
// Concurrency mode (plan doc 15 §3, Modes): three sessions at READ
// UNCOMMITTED, READ COMMITTED and REPEATABLE READ run short transactions,
// interleaved statement by statement in a seed-chosen order, on VDB and in
// the same order on the reference. It checks what each session sees of the
// others' committed and uncommitted writes, savepoints and rollbacks.
//
// The sessions never wait for each other by construction, so the comparison
// does not depend on timing: session k writes only rows of _a whose id is k
// modulo 3, by primary key, only rows that exist (point writes to a missing
// key would take gap locks), with unique values no other session uses, and
// locks only its own rows. Lock waits and deadlocks between sessions are
// covered by the core's and the engine's unit tests, not here.
package oracle

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
)

func bg() context.Context { return context.Background() }

var isolations = []string{"READ UNCOMMITTED", "READ COMMITTED", "REPEATABLE READ"}

type conState struct {
	k      int
	iso    string
	inTx   bool
	exists map[int]bool // the session's committed view of its own rows
	txLog  []func()     // undo of the model changes of the open transaction
}

func runConcurrent(env Env, cfg Config, seed int64) (*Mismatch, int, error) {
	s := NewSchema(prefixFor(cfg.Run, "concurrency", seed, 0), seed)
	if err := Seed(env, s); err != nil {
		return nil, 0, err
	}
	r := rand.New(rand.NewSource(seed*104729 + 3))
	const n = 3
	pairs := make([]*pair, n)
	states := make([]*conState, n)
	for k := 0; k < n; k++ {
		p, err := openPair(env, s)
		if err != nil {
			return nil, 0, err
		}
		defer p.close()
		iso := isolations[(k+int(seed))%len(isolations)]
		q := "SET SESSION TRANSACTION ISOLATION LEVEL " + iso
		if _, err := p.vdb.ExecContext(bg(), q); err != nil {
			return nil, 0, err
		}
		if _, err := p.ref.ExecContext(bg(), q); err != nil {
			return nil, 0, err
		}
		pairs[k] = p
		st := &conState{k: k, iso: iso, exists: map[int]bool{}}
		for i := 1; i <= s.RowsA; i++ {
			if (2*i)%n == k {
				st.exists[2*i] = true
			}
		}
		states[k] = st
	}
	var done []Step
	for len(done) < cfg.Steps {
		k := r.Intn(n)
		st := concurrentStep(r, s, states[k])
		st.Session = k
		var sums []string
		if st.Kind == Write {
			sums = []string{s.Checksum(s.T("a"))}
		}
		v := run(pairs[k].vdb, st, sums)
		ref := run(pairs[k].ref, st, sums)
		done = append(done, st)
		if !Equal(v, ref) || v.Timeout {
			m := &Mismatch{Seed: seed, Mode: "concurrency", Step: len(done) - 1, What: What(v, ref), VDB: v, Ref: ref, Prefix: s.Prefix,
				SQL: fmt.Sprintf("[session %d, %s] %s", k, states[k].iso, st.SQL)}
			m.Signature = signature("concurrency", st, m.What)
			m.Detail = detail(env, s, st, v, ref)
			for _, d := range done {
				d.SQL = fmt.Sprintf("/* session %d */ %s", d.Session, d.SQL)
				m.Minimal = append(m.Minimal, d)
			}
			return m, len(done), nil
		}
		if ref.Err != "" && st.Kind == Write {
			undoLast(states[k])
		}
	}
	for k, p := range pairs {
		if states[k].inTx {
			_, _ = p.vdb.ExecContext(bg(), "COMMIT")
			_, _ = p.ref.ExecContext(bg(), "COMMIT")
		}
	}
	return nil, len(done), nil
}

// undoLast reverts the model change of a write the servers refused.
func undoLast(st *conState) {
	if len(st.txLog) > 0 {
		st.txLog[len(st.txLog)-1]()
		st.txLog = st.txLog[:len(st.txLog)-1]
	}
}

func ownRow(r *rand.Rand, st *conState) (int, bool) {
	if len(st.exists) == 0 {
		return 0, false
	}
	ids := make([]int, 0, len(st.exists))
	for id := range st.exists {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids[r.Intn(len(ids))], true
}

func concurrentStep(r *rand.Rand, s *Schema, st *conState) Step {
	a := s.T("a")
	x := r.Intn(100)
	switch {
	case !st.inTx && x < 25:
		st.inTx = true
		st.txLog = nil
		return Step{SQL: "BEGIN", Kind: Tx}
	case st.inTx && x < 15:
		st.inTx = false
		if r.Intn(3) == 0 {
			for i := len(st.txLog) - 1; i >= 0; i-- {
				st.txLog[i]()
			}
			st.txLog = nil
			return Step{SQL: "ROLLBACK", Kind: Tx}
		}
		st.txLog = nil
		return Step{SQL: "COMMIT", Kind: Tx}
	case x < 50:
		return concurrentWrite(r, s, st)
	}
	switch r.Intn(6) {
	case 0:
		return Step{SQL: "SELECT COUNT(*), SUM(k) FROM " + a, Kind: Read}
	case 1:
		lo := r.Intn(2 * s.RowsA)
		return Step{SQL: fmt.Sprintf("SELECT id, k, s FROM %s WHERE id BETWEEN %d AND %d ORDER BY id", a, lo, lo+60), Kind: Read, Ordered: true}
	case 2:
		return Step{SQL: fmt.Sprintf("SELECT k, COUNT(*) FROM %s GROUP BY k ORDER BY k", a), Kind: Read, Ordered: true}
	case 3:
		if id, ok := ownRow(r, st); ok && st.inTx {
			return Step{SQL: fmt.Sprintf("SELECT id, k FROM %s WHERE id = %d FOR UPDATE", a, id), Kind: Read}
		}
	case 4:
		return Step{SQL: fmt.Sprintf("SELECT %s.id, %s.v FROM %s JOIN %s ON %s.a_id = %s.id WHERE %s.k = %d", a, s.T("b"), a, s.T("b"), s.T("b"), a, a, r.Intn(20)), Kind: Read}
	}
	return Step{SQL: fmt.Sprintf("SELECT * FROM %s WHERE id %% 3 = %d ORDER BY id LIMIT 30", a, r.Intn(3)), Kind: Read, Ordered: true}
}

func concurrentWrite(r *rand.Rand, s *Schema, st *conState) Step {
	a := s.T("a")
	w := func(sql string) Step { return Step{SQL: sql, Kind: Write, Tables: []string{a}} }
	switch r.Intn(3) {
	case 0:
		if id, ok := ownRow(r, st); ok {
			return w(fmt.Sprintf("UPDATE %s SET k = k + 1, s = 'c%d', n = n + 1 WHERE id = %d", a, st.k, id))
		}
	case 1:
		if id, ok := ownRow(r, st); ok && s.OnDelete != "RESTRICT" {
			delete(st.exists, id)
			st.txLog = append(st.txLog, func() { st.exists[id] = true })
			return w(fmt.Sprintf("DELETE FROM %s WHERE id = %d", a, id))
		}
	}
	id := 3*(500000+r.Intn(100000)) + st.k
	if st.exists[id] {
		id += 3
	}
	st.exists[id] = true
	st.txLog = append(st.txLog, func() { delete(st.exists, id) })
	return w(fmt.Sprintf("INSERT INTO %s (id, k, s, ci, l, n, t, u, e) VALUES (%d, %d, 'c%d', 'c', 'c', 1.00, '2026-05-01 00:00:00.000', %d, 'y')",
		a, id, r.Intn(20), st.k, 1000000+3*r.Intn(100000)+st.k))
}
