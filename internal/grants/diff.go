// Package grants — diff.go
//
// The statements that turn mysqld's accounts into the wanted set.
package grants

import (
	"sort"
	"strings"
)

func qid(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func object(a Atom) string {
	switch {
	case a.Routine != "":
		return a.Routine + " " + qid(a.DB) + "." + qid(a.Table)
	case a.Table == "*":
		return qid(a.DB) + ".*"
	}
	return qid(a.DB) + "." + qid(a.Table)
}

// privStatements groups atoms by object into GRANT or REVOKE statements.
func privStatements(verb, prep string, acct Principal, atoms []Atom) []string {
	type objPrivs map[string][]string
	by := map[string]objPrivs{}
	for _, a := range atoms {
		o := object(a)
		if by[o] == nil {
			by[o] = objPrivs{}
		}
		by[o][a.Priv] = append(by[o][a.Priv], a.Col)
	}
	var objs []string
	for o := range by {
		objs = append(objs, o)
	}
	sort.Strings(objs)
	var out []string
	for _, o := range objs {
		var parts []string
		for p, cs := range by[o] {
			whole := false
			var named []string
			for _, c := range cs {
				if c == "" {
					whole = true
				} else {
					named = append(named, qid(c))
				}
			}
			if whole {
				parts = append(parts, p)
			}
			if len(named) > 0 {
				sort.Strings(named)
				parts = append(parts, p+" ("+strings.Join(named, ", ")+")")
			}
		}
		sort.Strings(parts)
		out = append(out, verb+" "+strings.Join(parts, ", ")+" ON "+o+" "+prep+" "+acct.String())
	}
	return out
}

func principalSet(ps []Principal) map[Principal]bool {
	m := map[Principal]bool{}
	for _, p := range ps {
		m[p] = true
	}
	return m
}

func names(ps []Principal) string {
	sort.Slice(ps, func(i, j int) bool { return ps[i].String() < ps[j].String() })
	var s []string
	for _, p := range ps {
		s = append(s, p.String())
	}
	return strings.Join(s, ", ")
}

// Diff returns the statements, in order, that turn have into want. Accounts
// are created before privileges, roles before the grants of roles, and
// dropped accounts last.
func Diff(want, have map[Principal]*Account) []string {
	var keys []Principal
	for k := range want {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		// Roles first, so GRANT role TO account finds the role.
		if want[keys[i]].Role != want[keys[j]].Role {
			return want[keys[i]].Role
		}
		return keys[i].String() < keys[j].String()
	})
	var create, alter, grant, roles []string
	for _, k := range keys {
		w := want[k]
		h := have[k]
		plugin := PluginFor(w.Method)
		if h == nil {
			s := "CREATE USER " + k.String() + " IDENTIFIED WITH " + plugin
			if w.RequireSSL {
				s += " REQUIRE SSL"
			}
			if w.Locked {
				s += " ACCOUNT LOCK"
			}
			create = append(create, s)
			h = &Account{Principal: k, Atoms: map[Atom]bool{}}
		} else {
			if PluginFor(h.Method) != plugin && h.Method != plugin {
				alter = append(alter, "ALTER USER "+k.String()+" IDENTIFIED WITH "+plugin)
			}
			if h.RequireSSL != w.RequireSSL {
				req := "NONE"
				if w.RequireSSL {
					req = "SSL"
				}
				alter = append(alter, "ALTER USER "+k.String()+" REQUIRE "+req)
			}
			if h.Locked != w.Locked {
				l := "UNLOCK"
				if w.Locked {
					l = "LOCK"
				}
				alter = append(alter, "ALTER USER "+k.String()+" ACCOUNT "+l)
			}
		}
		var add, del []Atom
		for _, a := range sortedAtoms(w.Atoms) {
			if !h.Atoms[a] {
				add = append(add, a)
			}
		}
		for _, a := range sortedAtoms(h.Atoms) {
			if !w.Atoms[a] {
				del = append(del, a)
			}
		}
		grant = append(grant, privStatements("REVOKE", "FROM", k, del)...)
		grant = append(grant, privStatements("GRANT", "TO", k, add)...)
		wr, hr := principalSet(w.Roles), principalSet(h.Roles)
		var radd, rdel []Principal
		for r := range wr {
			if !hr[r] {
				radd = append(radd, r)
			}
		}
		for r := range hr {
			if !wr[r] {
				rdel = append(rdel, r)
			}
		}
		if len(rdel) > 0 {
			roles = append(roles, "REVOKE "+names(rdel)+" FROM "+k.String())
		}
		if len(radd) > 0 {
			roles = append(roles, "GRANT "+names(radd)+" TO "+k.String())
		}
		wd, hd := principalSet(w.DefaultRoles), principalSet(h.DefaultRoles)
		if !sameSet(wd, hd) {
			if len(w.DefaultRoles) == 0 {
				roles = append(roles, "SET DEFAULT ROLE NONE TO "+k.String())
			} else {
				roles = append(roles, "SET DEFAULT ROLE "+names(append([]Principal(nil), w.DefaultRoles...))+" TO "+k.String())
			}
		}
	}
	var drop []string
	var hk []Principal
	for k := range have {
		if want[k] == nil && !Internal(k) && k.User != "" {
			hk = append(hk, k)
		}
	}
	sort.Slice(hk, func(i, j int) bool { return hk[i].String() < hk[j].String() })
	for _, k := range hk {
		drop = append(drop, "DROP USER "+k.String())
	}
	out := append(create, alter...)
	out = append(out, grant...)
	out = append(out, roles...)
	return append(out, drop...)
}

func sameSet(a, b map[Principal]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
