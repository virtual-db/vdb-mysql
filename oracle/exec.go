// Package oracle — exec.go
//
// The executor and the comparer (plan doc 15 §3, Executor and Comparer).
// Each step runs on one session of VDB and the same session of the
// reference, as text or as a server-side prepared statement. The outcome
// captured for comparison is: rows (sorted unless the statement orders them
// completely), column names and types, the error code, affected rows, the
// warnings' codes, and after a write the checksum of every table it changes.
// LAST_INSERT_ID is not compared: VDB's auto-increment values start above a
// gap by design (D-15), and the generator always gives explicit ids.
package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Result is one statement's outcome.
type Result struct {
	Rows     []string
	Columns  []string // name:type per column
	Ordered  bool
	Affected int64
	ErrCode  uint16
	Err      string
	Warnings []uint16
	Sums     []string // checksums of the tables a write changed
	Timeout  bool
}

func (r Result) short() string {
	switch {
	case r.Timeout:
		return "did not finish in time"
	case r.Err != "":
		return fmt.Sprintf("error %d %s", r.ErrCode, r.Err)
	}
	s := fmt.Sprintf("%d rows, %d affected", len(r.Rows), r.Affected)
	if len(r.Rows) > 0 && len(r.Rows) <= 5 {
		s += " " + strings.Join(r.Rows, " | ")
	}
	if len(r.Warnings) > 0 {
		s += fmt.Sprintf(", warnings %v", r.Warnings)
	}
	if len(r.Sums) > 0 {
		s += fmt.Sprintf(", tables %v", r.Sums)
	}
	return s
}

// stepTimeout bounds one statement; a statement that waits longer on one
// side than the other is a difference too.
const stepTimeout = 20 * time.Second

func run(conn *sql.Conn, s Step, sums []string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	var res Result
	if s.Kind == Read {
		res = query(ctx, conn, s)
	} else {
		res = exec(ctx, conn, s)
	}
	if res.Timeout {
		return res
	}
	res.Warnings = warnings(ctx, conn)
	if res.Err == "" {
		for _, q := range sums {
			var n, x sql.NullString
			if err := conn.QueryRowContext(ctx, q).Scan(&n, &x); err != nil {
				res.Sums = append(res.Sums, "error "+err.Error())
				continue
			}
			res.Sums = append(res.Sums, n.String+":"+x.String)
		}
	}
	return res
}

func exec(ctx context.Context, conn *sql.Conn, s Step) Result {
	var r sql.Result
	var err error
	if s.Prepared {
		var st *sql.Stmt
		if st, err = conn.PrepareContext(ctx, s.SQL); err == nil {
			r, err = st.ExecContext(ctx)
			st.Close()
		}
	} else {
		r, err = conn.ExecContext(ctx, s.SQL)
	}
	if err != nil {
		return errResult(ctx, err)
	}
	n, _ := r.RowsAffected()
	return Result{Affected: n}
}

func query(ctx context.Context, conn *sql.Conn, s Step) Result {
	var rows *sql.Rows
	var err error
	if s.Prepared {
		var st *sql.Stmt
		if st, err = conn.PrepareContext(ctx, s.SQL); err == nil {
			defer st.Close()
			rows, err = st.QueryContext(ctx)
		}
	} else {
		rows, err = conn.QueryContext(ctx, s.SQL)
	}
	if err != nil {
		return errResult(ctx, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var meta []string
	if cts, err := rows.ColumnTypes(); err == nil {
		for _, ct := range cts {
			meta = append(meta, ct.Name()+":"+ct.DatabaseTypeName())
		}
	}
	var out []string
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return errResult(ctx, err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "NULL"
			} else {
				parts[i] = string(v)
			}
		}
		out = append(out, strings.Join(parts, ","))
	}
	if err := rows.Err(); err != nil {
		return errResult(ctx, err)
	}
	if !s.Ordered {
		sort.Strings(out)
	}
	return Result{Rows: out, Columns: meta, Ordered: s.Ordered}
}

func errResult(ctx context.Context, err error) Result {
	if ctx.Err() != nil {
		return Result{Timeout: true, Err: "timeout"}
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return Result{ErrCode: me.Number, Err: me.Message}
	}
	return Result{ErrCode: 0, Err: err.Error()}
}

// warnings reads the codes of the last statement's warnings. VDB's own
// warning that a table changed on the Source (code 1105) is not InnoDB's and
// is left out.
func warnings(ctx context.Context, conn *sql.Conn) []uint16 {
	rows, err := conn.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []uint16
	for rows.Next() {
		var level, msg string
		var code uint16
		if rows.Scan(&level, &code, &msg) == nil {
			if code == 1105 && strings.Contains(msg, "changed on the Source") {
				continue
			}
			out = append(out, code)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// What is the part of an outcome two results differ in, or "".
func What(a, b Result) string {
	switch {
	case a.Timeout != b.Timeout:
		return "wait"
	case a.ErrCode != b.ErrCode || (a.Err == "") != (b.Err == ""):
		return "error"
	case a.Err != "":
		return ""
	case a.Affected != b.Affected:
		return "affected"
	case len(a.Rows) != len(b.Rows):
		return "rows"
	}
	for i := range a.Rows {
		if a.Rows[i] != b.Rows[i] {
			return "rows"
		}
	}
	if strings.Join(a.Columns, ";") != strings.Join(b.Columns, ";") {
		return "columns"
	}
	if fmt.Sprint(a.Warnings) != fmt.Sprint(b.Warnings) {
		return "warnings"
	}
	if strings.Join(a.Sums, ";") != strings.Join(b.Sums, ";") {
		return "table contents"
	}
	return ""
}

// Equal reports whether two outcomes match.
func Equal(a, b Result) bool { return What(a, b) == "" }
