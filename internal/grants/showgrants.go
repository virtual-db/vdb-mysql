// Package grants — showgrants.go
//
// The fallback without grant-table access: privileges from SHOW GRANTS on
// the relay connection at login (product doc 11 §4.5).
package grants

import (
	"regexp"
	"strings"
)

var grantRe = regexp.MustCompile(`^GRANT (.+) ON (\S+(?: \S+)?) TO .+$`)
var revokeRe = regexp.MustCompile(`^REVOKE (.+) ON (\S+) FROM .+$`)
var roleRe = regexp.MustCompile("^GRANT (`[^`]+`@`[^`]*`(?:,\\s*`[^`]+`@`[^`]*`)*) TO .+$")

func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	inq := false
	for i, c := range s {
		switch {
		case c == '`':
			inq = !inq
		case inq:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '`' {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	return s
}

func splitObj(obj string) (db, table string) {
	inq := false
	for i, c := range obj {
		if c == '`' {
			inq = !inq
		} else if c == '.' && !inq {
			return unquote(obj[:i]), unquote(obj[i+1:])
		}
	}
	return unquote(obj), ""
}

// FromShowGrants builds an account's atoms, partial revokes and roles from
// SHOW GRANTS lines.
func FromShowGrants(p Principal, lines []string) *Account {
	a := &Account{Principal: p, Atoms: map[Atom]bool{}}
	for _, g := range lines {
		g = strings.TrimSuffix(g, " WITH GRANT OPTION")
		if m := revokeRe.FindStringSubmatch(g); m != nil {
			db, _ := splitObj(m[2])
			for _, pr := range splitTop(m[1]) {
				a.Revokes = append(a.Revokes, Atom{Priv: strings.ToUpper(pr), DB: db, Table: "*"})
			}
			continue
		}
		if m := roleRe.FindStringSubmatch(g); m != nil {
			for _, r := range splitTop(m[1]) {
				parts := strings.SplitN(r, "@", 2)
				if len(parts) == 2 {
					a.Roles = append(a.Roles, Principal{unquote(parts[0]), unquote(parts[1])})
				}
			}
			continue
		}
		m := grantRe.FindStringSubmatch(g)
		if m == nil {
			continue
		}
		obj := m[2]
		routine := ""
		for _, kw := range []string{"PROCEDURE ", "FUNCTION "} {
			if strings.HasPrefix(obj, kw) {
				routine, obj = strings.TrimSpace(kw), strings.TrimPrefix(obj, kw)
			}
		}
		db, table := splitObj(obj)
		for _, pr := range splitTop(m[1]) {
			base, colsPart := pr, ""
			if i := strings.Index(pr, " ("); i > 0 {
				base, colsPart = pr[:i], strings.TrimSuffix(pr[i+2:], ")")
			}
			base = strings.ToUpper(strings.TrimSpace(base))
			names := []string{base}
			if base == "ALL PRIVILEGES" || base == "ALL" {
				names = Copied
			}
			for _, b := range names {
				if colsPart == "" {
					a.Atoms[Atom{Priv: b, DB: db, Table: table, Routine: routine}] = true
					continue
				}
				for _, c := range splitTop(colsPart) {
					a.Atoms[Atom{Priv: b, DB: db, Table: table, Col: unquote(c)}] = true
				}
			}
		}
	}
	return a
}
