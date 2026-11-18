// Package grants — rules.go
//
// The copy rules of product doc 11 §4.4 as a function.
package grants

import (
	"regexp"
	"strings"
)

// Copied lists the privileges that carry over. Everything else (WITH GRANT
// OPTION, administrative and dynamic privileges) is dropped.
var Copied = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX", "REFERENCES",
	"CREATE VIEW", "SHOW VIEW", "CREATE ROUTINE", "ALTER ROUTINE", "EXECUTE", "TRIGGER",
	"CREATE TEMPORARY TABLES", "LOCK TABLES"}

var copied = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range Copied {
		m[p] = true
	}
	return m
}()

// Allowed reports whether a privilege carries over.
func Allowed(p string) bool { return copied[strings.ToUpper(strings.TrimSpace(p))] }

// dbLevelOnly privileges exist only at database level or above.
var dbLevelOnly = map[string]bool{"CREATE TEMPORARY TABLES": true, "LOCK TABLES": true, "CREATE ROUTINE": true}

// tableLevel privileges can be granted on a table.
var tableLevel = map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "CREATE": true, "DROP": true,
	"ALTER": true, "INDEX": true, "REFERENCES": true, "CREATE VIEW": true, "SHOW VIEW": true, "TRIGGER": true}

// routineLevel privileges can be granted on a routine.
var routineLevel = map[string]bool{"EXECUTE": true, "ALTER ROUTINE": true}

// Excluded are mysqld's own schemas and the vdb schema: never granted on.
var Excluded = map[string]bool{"mysql": true, "sys": true, "performance_schema": true, "information_schema": true, "vdb": true}

// Mirror maps a Source account to the account mysqld must hold: only copied
// privileges, ALL expanded to them, *.* and wildcard database grants mapped
// to each exposed database they match, partial revokes honoured.
func Mirror(src *Account, exposed []string) *Account {
	out := &Account{Principal: src.Principal, Method: src.Method, RequireSSL: src.RequireSSL, Locked: src.Locked, Role: src.Role,
		Atoms: map[Atom]bool{}, Roles: src.Roles, DefaultRoles: src.DefaultRoles}
	for a := range src.Atoms {
		if !Allowed(a.Priv) {
			continue
		}
		p := strings.ToUpper(a.Priv)
		switch {
		case a.Routine != "":
			if !routineLevel[p] {
				continue
			}
		case a.Table != "*":
			if !tableLevel[p] {
				continue
			}
		}
		for _, db := range expandDB(a.DB, exposed) {
			b := a
			b.Priv, b.DB = p, db
			if a.DB == "*" {
				b.Table = "*"
			}
			out.Atoms[b] = true
		}
	}
	for _, r := range src.Revokes {
		for a := range out.Atoms {
			if strings.EqualFold(a.Priv, r.Priv) && a.DB == r.DB {
				delete(out.Atoms, a)
			}
		}
	}
	return out
}

// expandDB maps a Source database name, pattern or * to exposed databases.
func expandDB(db string, exposed []string) []string {
	var out []string
	switch {
	case db == "*":
		for _, d := range exposed {
			if !Excluded[strings.ToLower(d)] {
				out = append(out, d)
			}
		}
	case strings.ContainsAny(db, "%_"):
		re := pattern(db)
		for _, d := range exposed {
			if !Excluded[strings.ToLower(d)] && re.MatchString(d) {
				out = append(out, d)
			}
		}
		// An escaped name without wildcards may still name one database.
		plain := strings.NewReplacer(`\_`, "_", `\%`, "%").Replace(db)
		if plain != db {
			for _, d := range exposed {
				if d == plain && !contains(out, d) {
					out = append(out, d)
				}
			}
		}
	default:
		for _, d := range exposed {
			if d == db && !Excluded[strings.ToLower(d)] {
				out = append(out, d)
			}
		}
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// pattern turns a MySQL database pattern (% and _, \ escapes) into a regexp.
func pattern(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			i++
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		case c == '%':
			b.WriteString(".*")
		case c == '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// PluginFor returns the VDB login plug-in for a Source account's method.
func PluginFor(method string) string {
	if method == "mysql_native_password" {
		return "vdb_mysql_auth_native"
	}
	return "vdb_mysql_auth"
}
