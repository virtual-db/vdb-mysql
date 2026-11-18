// Package statements — rewrite.go
//
// Rewrite (doc 09 §5.1, D-54): for CREATE TABLE that is not CREATE TEMPORARY
// TABLE, find every ENGINE [=] name table option at the top level of the
// table options, not inside a partition definition, comment or string, and
// reply with the byte ranges to remove. The client's own text is kept with
// only the clause removed.
package statements

import (
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// Range is a byte range to remove.
type Range struct{ Start, End int }

// tok is one token with its byte offsets.
type tok struct {
	typ        int
	val        string
	start, end int
}

// EngineClauses returns the ENGINE clauses to remove from a statement, or nil.
func EngineClauses(p *sqlparser.Parser, sql string) []Range {
	toks := tokens(p, sql)
	i := 0
	word := func(k int) string {
		if k < len(toks) {
			return strings.ToUpper(toks[k].val)
		}
		return ""
	}
	if word(i) != "CREATE" {
		return nil
	}
	i++
	if word(i) == "TEMPORARY" {
		return nil
	}
	if word(i) != "TABLE" {
		return nil
	}
	i++
	// Find the end of the column list: the top-level parenthesis group, or
	// the table name for CREATE TABLE ... LIKE / AS SELECT without one.
	depth := 0
	afterCols := -1
	for k := i; k < len(toks); k++ {
		switch toks[k].val {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 && afterCols < 0 {
				afterCols = k + 1
			}
		}
		if afterCols >= 0 {
			break
		}
		if depth == 0 && (word(k) == "LIKE" || word(k) == "AS" || word(k) == "SELECT") {
			return nil
		}
	}
	if afterCols < 0 {
		return nil
	}
	var out []Range
	depth = 0
	for k := afterCols; k < len(toks); k++ {
		switch toks[k].val {
		case "(":
			depth++
			continue
		case ")":
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		w := word(k)
		if w == "PARTITION" || w == "SELECT" || w == "AS" || w == "IGNORE" || w == "REPLACE" {
			break
		}
		if w != "ENGINE" {
			continue
		}
		start := toks[k].start
		end := k + 1
		if end < len(toks) && toks[end].val == "=" {
			end++
		}
		if end >= len(toks) {
			break
		}
		stop := toks[end].end
		// Remove a separating comma before the clause, or after it.
		if k > afterCols && toks[k-1].val == "," {
			start = toks[k-1].start
		} else if end+1 < len(toks) && toks[end+1].val == "," {
			stop = toks[end+1].end
		}
		out = append(out, Range{Start: start, End: stop})
		k = end
	}
	return out
}

// tokens scans a statement with Vitess's tokenizer; comments and strings are
// single tokens, so an ENGINE= inside them is never matched.
func tokens(p *sqlparser.Parser, sql string) []tok {
	t := p.NewStringTokenizer(sql)
	var out []tok
	for {
		start := t.Pos
		typ, val := t.Scan()
		if typ == 0 {
			break
		}
		end := t.Pos
		// Pos is just past the token; find where it began (after blanks).
		s := start
		for s < end && s < len(sql) && (sql[s] == ' ' || sql[s] == '\t' || sql[s] == '\n' || sql[s] == '\r') {
			s++
		}
		if typ == sqlparser.LEX_ERROR {
			break
		}
		v := val
		if len(val) == 0 && s < end {
			v = sql[s:end]
		}
		if typ < 128 {
			v = string(rune(typ))
		}
		out = append(out, tok{typ: typ, val: v, start: s, end: end})
	}
	return out
}

// Apply removes the ranges from sql.
func Apply(sql string, rs []Range) string {
	var b strings.Builder
	last := 0
	for _, r := range rs {
		if r.Start < last || r.End > len(sql) {
			continue
		}
		b.WriteString(sql[last:r.Start])
		last = r.End
	}
	b.WriteString(sql[last:])
	return b.String()
}
