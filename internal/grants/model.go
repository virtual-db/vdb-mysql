// Package grants mirrors Source accounts and privileges into mysqld
// (implementation plan doc 11 §3, product doc 11 §4): it reads accounts,
// privileges, roles and default roles, computes the mirrored set with the
// copy rules, diffs it against what mysqld holds, applies the difference over
// the control channel, and polls. It starts from the authentication spike's
// code (experiments/2026-11-11-engine-shim/core/auth.go, grantpoll.go).
//
// rules.go is the code the negative suite guards: the service account can
// read everything, so a wrong copy is a data exposure.
package grants

import (
	"sort"
	"strings"
)

// Atom is one privilege on one object: a database (Table "*"), a table, a
// column of a table, or a routine (Routine set to "PROCEDURE" or "FUNCTION").
type Atom struct {
	Priv    string
	DB      string
	Table   string
	Col     string
	Routine string
}

// Principal names an account or role.
type Principal struct {
	User string
	Host string
}

// String renders 'user'@'host'.
func (p Principal) String() string { return quoteName(p.User) + "@" + quoteName(p.Host) }

func quoteName(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
}

// Account is one account (or role) with its direct privileges.
type Account struct {
	Principal
	Method       string // the Source's plugin column
	RequireSSL   bool
	Locked       bool
	Role         bool // a role (locked, no login) granted to accounts
	Atoms        map[Atom]bool
	Revokes      []Atom // partial revokes (User_attributes Restrictions)
	Roles        []Principal
	DefaultRoles []Principal
}

// Key returns the account's identity.
func (a *Account) Key() Principal { return a.Principal }

func sortedAtoms(m map[Atom]bool) []Atom {
	out := make([]Atom, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.DB != y.DB {
			return x.DB < y.DB
		}
		if x.Table != y.Table {
			return x.Table < y.Table
		}
		if x.Routine != y.Routine {
			return x.Routine < y.Routine
		}
		if x.Priv != y.Priv {
			return x.Priv < y.Priv
		}
		return x.Col < y.Col
	})
	return out
}
