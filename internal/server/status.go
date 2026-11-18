// Package server — status.go
//
// The vdb schema's providers (doc 13 §1) and the metrics sources (doc 13 §4).
package server

import (
	"os"
	"strconv"
	"strings"
	"time"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/deltasql"
	"github.com/virtual-db/vdb-mysql/internal/schemapoll"
	"github.com/virtual-db/vdb-mysql/internal/vdbschema"
	"github.com/virtual-db/vdb-mysql/protocol"
)

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func u(n uint64) string  { return strconv.FormatUint(n, 10) }
func i64(n int64) string { return strconv.FormatInt(n, 10) }

func (s *Server) statusRows(core.SessionInfo) [][]string {
	s.mu.Lock()
	state, mysqldV := s.state, s.mysqldV
	s.mu.Unlock()
	ds := s.api.DeltaStats()
	ls := s.api.LockStats()
	ms := s.api.MemoryStats()
	grantMode := "grant tables"
	if s.grants != nil && s.grants.Fallback() {
		grantMode = "SHOW GRANTS at login"
	}
	push := "on"
	if !s.cfg.Source.Pushdown {
		push = "off"
	}
	rows := [][]string{
		{"state", state},
		{"source_host", s.cfg.Source.Host + ":" + strconv.Itoa(s.cfg.Source.Port)},
		{"source_version", s.src.Version()},
		{"mysqld_release", mysqldV},
		{"vdb_mysql_version", s.Version},
		{"protocol_version", strconv.Itoa(protocol.Version)},
		{"delta_bytes", u(ds.Bytes)},
		{"commit_seq", u(ds.Seq)},
		{"memory_available", u(ms.Available)},
		{"memory_ceiling", u(ms.Ceiling)},
		{"memory_reserve", strconv.FormatFloat(ms.Reserve, 'f', 2, 64)},
		{"resident_memory", u(ms.Resident)},
		{"memory_refusals", u(ms.Refusals)},
		{"tmpdir_bytes", u(s.sup.TmpdirUse())},
		{"lock_entries", u(ls.Entries)},
		{"lock_bytes", u(ls.Bytes)},
		{"persistence", s.persistence()},
		{"grant_mode", grantMode},
		{"pushdown", push},
	}
	return rows
}

func (s *Server) persistence() string {
	if s.app.HasHandlers("vdb.transaction.commit.apply") {
		return "sync plugin attached"
	}
	return "none: the Delta lives in memory only"
}

func (s *Server) tablesRows(core.SessionInfo) [][]string {
	stats := map[core.TableID]core.TableStats{}
	for _, t := range s.api.DeltaStats().Tables {
		stats[t.Table] = t
	}
	var rows [][]string
	for _, d := range s.api.Catalog().Tables() {
		if d.Kind == core.Provided {
			continue
		}
		st := stats[d.ID]
		var unproven []string
		for _, ix := range d.Indexes {
			if !ix.OrderProven || !ix.EqualityProven {
				for _, c := range ix.Columns {
					if coll := d.Columns[c].Collation; coll != "" && !contains(unproven, coll) {
						unproven = append(unproven, coll)
					}
				}
			}
		}
		last := vdbschema.NULL
		if s.poll != nil {
			last = vdbschema.Time(s.poll.LastChange(d.ID))
		}
		rows = append(rows, []string{d.Database, d.Name, d.Kind.String(), u(st.DeltaRows), u(st.TouchedKeys), u(st.Bytes), strings.Join(unproven, ","), last})
	}
	return rows
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) hasProcess(account string) bool {
	return s.grants != nil && s.grants.HasProcess(account)
}

func (s *Server) sessionsRows(reader core.SessionInfo) [][]string {
	ages := s.src.Snapshots()
	reqs := map[uint64][2]int64{}
	for _, r := range s.src.Log.Records() {
		x := reqs[r.ConnID]
		x[0]++
		x[1] += r.Rows
		reqs[r.ConnID] = x
	}
	var rows [][]string
	for _, c := range s.engine.Sessions() {
		if !vdbschema.Visible(reader, c.Account, s.hasProcess) {
			continue
		}
		age := vdbschema.NULL
		if a, ok := ages[c.ConnID]; ok {
			age = i64(a.Milliseconds())
		}
		rows = append(rows, []string{u(c.ConnID), c.Account, age, i64(reqs[c.ConnID][0]), i64(reqs[c.ConnID][1]), i64(c.StatementsNoBKA)})
	}
	return rows
}

func (s *Server) sourceRequestsRows(reader core.SessionInfo) [][]string {
	accounts := map[uint64]string{}
	for _, c := range s.engine.Sessions() {
		accounts[c.ConnID] = c.Account
	}
	var rows [][]string
	for _, r := range s.src.Log.Records() {
		if r.ConnID != 0 && !vdbschema.Visible(reader, accounts[r.ConnID], s.hasProcess) {
			continue
		}
		if r.ConnID == 0 && !reader.Control && !s.hasProcess(reader.Account) {
			continue
		}
		e := vdbschema.NULL
		if r.Error != "" {
			e = r.Error
		}
		rows = append(rows, []string{u(r.ConnID), r.Digest, r.Shape, r.Schema, r.Table, vdbschema.Time(r.Started), i64(r.Duration.Microseconds()), i64(r.Rows), i64(r.Bytes), e})
	}
	return rows
}

func (s *Server) accountsRows(core.SessionInfo) [][]string {
	var rows [][]string
	for _, a := range s.grants.Accounts() {
		reach := "1"
		if !a.Reachable {
			reach = "0"
		}
		reason := vdbschema.NULL
		if a.Reason != "" {
			reason = a.Reason
		}
		rows = append(rows, []string{a.User, a.Host, a.Method, vdbschema.Time(a.GrantsReadAt), reach, reason})
	}
	return rows
}

func (s *Server) catalogErrorsRows(core.SessionInfo) [][]string {
	var rows [][]string
	for _, e := range s.rebuild.Errors() {
		rows = append(rows, []string{e.Type, e.Schema, e.Name, e.Error})
	}
	return rows
}

func (s *Server) deltaSQLRows(core.SessionInfo) [][]string {
	var rows [][]string
	for _, st := range deltasql.Render(s.api) {
		rows = append(rows, []string{u(st.Seq), st.Kind, st.Schema, st.Name, st.SQL})
	}
	return rows
}

func (s *Server) schemaChangesRows(core.SessionInfo) [][]string {
	if s.poll == nil {
		return nil
	}
	var rows [][]string
	for _, c := range s.poll.Changes() {
		rows = append(rows, []string{u(c.Seq), vdbschema.Time(c.AppliedAt), c.Schema, c.Table, c.Change, c.DeltaEffect, schemapoll.AffectedJSON(c.Affected)})
	}
	return rows
}

func (s *Server) metricsRows(core.SessionInfo) [][]string {
	var rows [][]string
	for _, m := range s.metrics.Rows() {
		rows = append(rows, []string{m.Name, m.Labels, strconv.FormatFloat(m.Value, 'g', -1, 64)})
	}
	return rows
}

func (s *Server) registerProviders() {
	vdbschema.Register(s.api.Catalog(), map[string]vdbschema.Rows{
		"status": s.statusRows, "tables": s.tablesRows, "sessions": s.sessionsRows,
		"source_requests": s.sourceRequestsRows, "accounts": s.accountsRows, "catalog_errors": s.catalogErrorsRows,
		"delta_sql": s.deltaSQLRows, "schema_changes": s.schemaChangesRows, "metrics": s.metricsRows,
	})
}

// registerMetrics adds the sources of product doc 14 §7's metrics.
func (s *Server) registerMetrics() {
	s.metrics.Source(func(set func(name, labels string, v float64)) {
		l := s.src.Log
		set("vdb_source_requests_total", "", float64(l.Count.Load()))
		set("vdb_source_rows_total", "", float64(l.RowsN.Load()))
		set("vdb_source_bytes_total", "", float64(l.BytesN.Load()))
		set("vdb_source_errors_total", "", float64(l.Errors.Load()))
		set("vdb_source_request_seconds_total", "", float64(l.TotalUs.Load())/1e6)
		perShape := map[string]float64{}
		for _, r := range l.Records() {
			perShape[r.Shape]++
		}
		for shape, n := range perShape {
			set("vdb_source_requests_recent", "shape="+shape, n)
		}
		ds := s.api.DeltaStats()
		set("vdb_delta_bytes", "", float64(ds.Bytes))
		names := map[core.TableID]string{}
		for _, d := range s.api.Catalog().Tables() {
			names[d.ID] = d.Database + "." + d.Name
		}
		for _, t := range ds.Tables {
			set("vdb_delta_rows", "table="+names[t.Table], float64(t.DeltaRows))
			set("vdb_delta_table_bytes", "table="+names[t.Table], float64(t.Bytes))
		}
		ls := s.api.LockStats()
		set("vdb_lock_entries", "", float64(ls.Entries))
		set("vdb_lock_bytes", "", float64(ls.Bytes))
		set("vdb_lock_waits_total", "", float64(ls.Waits))
		set("vdb_deadlocks_total", "", float64(ls.Deadlocks))
		set("vdb_lock_wait_timeouts_total", "", float64(ls.Timeouts))
		var oldest time.Duration
		for _, a := range s.src.Snapshots() {
			if a > oldest {
				oldest = a
			}
		}
		set("vdb_snapshot_age_max_seconds", "", oldest.Seconds())
		set("vdb_logins_accepted_total", "", float64(s.relay.Accepted.Load()))
		set("vdb_logins_refused_total", "", float64(s.relay.Refused.Load()))
		set("vdb_logins_password_asked_total", "", float64(s.relay.PasswordAsked.Load()))
		if t, d, e := s.grants.PollInfo(); !t.IsZero() {
			set("vdb_grant_poll_seconds", "", d.Seconds())
			ok := 1.0
			if e != "" {
				ok = 0
			}
			set("vdb_grant_poll_ok", "", ok)
		}
		if s.poll != nil {
			set("vdb_source_schema_changes_total", "", float64(len(s.poll.Changes())))
		}
		var stmts float64
		for _, c := range s.engine.Sessions() {
			stmts += float64(s.stmts.Counts(c.ConnID).Statements)
		}
		set("vdb_statements_open_sessions", "", stmts)
	})
}
