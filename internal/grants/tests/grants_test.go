// Package grants_test — tests/grants_test.go
//
// The copy rules as a table test, Diff round trips, SHOW GRANTS parsing and
// MySQL's host matching (doc 11 §4). rules.go is the code the negative suite
// guards: a wrong copy is a data exposure.
package grants_test

import (
	"strings"
	"testing"

	. "github.com/virtual-db/vdb-mysql/internal/grants"
)

var exposed = []string{"shop", "shop_2", "crm"}

func TestMirror(t *testing.T) {
	src := &Account{Principal: Principal{"alice", "%"}, Atoms: map[Atom]bool{
		{Priv: "SELECT", DB: "*", Table: "*"}:                          true, // global → each exposed database
		{Priv: "SUPER", DB: "*", Table: "*"}:                           true, // administrative: dropped
		{Priv: "INSERT", DB: "shop\\_2", Table: "*"}:                   true, // escaped name
		{Priv: "UPDATE", DB: "sh%", Table: "*"}:                        true, // wildcard
		{Priv: "SELECT", DB: "mysql", Table: "*"}:                      true, // never mysqld's own schemas
		{Priv: "LOCK TABLES", DB: "crm", Table: "t"}:                   true, // database-level only
		{Priv: "EXECUTE", DB: "crm", Table: "p", Routine: "PROCEDURE"}: true,
	}, Revokes: []Atom{{Priv: "SELECT", DB: "crm", Table: "*"}}}
	m := Mirror(src, exposed)
	want := []Atom{
		{Priv: "SELECT", DB: "shop", Table: "*"}, {Priv: "SELECT", DB: "shop_2", Table: "*"},
		{Priv: "INSERT", DB: "shop_2", Table: "*"},
		{Priv: "UPDATE", DB: "shop", Table: "*"}, {Priv: "UPDATE", DB: "shop_2", Table: "*"},
		{Priv: "EXECUTE", DB: "crm", Table: "p", Routine: "PROCEDURE"},
	}
	if len(m.Atoms) != len(want) {
		t.Fatalf("got %d atoms: %+v", len(m.Atoms), m.Atoms)
	}
	for _, a := range want {
		if !m.Atoms[a] {
			t.Errorf("missing %+v", a)
		}
	}
}

func TestDiff(t *testing.T) {
	p := Principal{"bob", "10.%"}
	want := map[Principal]*Account{p: {Principal: p, Method: "mysql_native_password", RequireSSL: true,
		Atoms: map[Atom]bool{{Priv: "SELECT", DB: "shop", Table: "t", Col: "a"}: true}}}
	stmts := Diff(want, map[Principal]*Account{})
	all := strings.Join(stmts, "\n")
	if !strings.Contains(all, "CREATE USER 'bob'@'10.%' IDENTIFIED WITH vdb_mysql_auth_native REQUIRE SSL") ||
		!strings.Contains(all, "GRANT SELECT (`a`) ON `shop`.`t` TO 'bob'@'10.%'") {
		t.Fatalf("statements:\n%s", all)
	}
	have := map[Principal]*Account{p: want[p], {"gone", "%"}: {Principal: Principal{"gone", "%"}, Atoms: map[Atom]bool{}}}
	have[p].Method = "vdb_mysql_auth_native"
	stmts = Diff(want, have)
	if len(stmts) != 1 || stmts[0] != "DROP USER 'gone'@'%'" {
		t.Fatalf("only the drop remains: %v", stmts)
	}
}

func TestShowGrants(t *testing.T) {
	a := FromShowGrants(Principal{"c", "%"}, []string{
		"GRANT SELECT, INSERT ON `shop`.* TO `c`@`%`",
		"GRANT UPDATE (`x`, `y`) ON `shop`.`t` TO `c`@`%`",
		"GRANT `reader`@`%` TO `c`@`%`",
	})
	if !a.Atoms[Atom{Priv: "INSERT", DB: "shop", Table: "*"}] || !a.Atoms[Atom{Priv: "UPDATE", DB: "shop", Table: "t", Col: "y"}] || len(a.Roles) != 1 {
		t.Fatalf("%+v", a)
	}
}

func TestReachable(t *testing.T) {
	cases := []struct {
		host, vdb string
		ok        bool
	}{
		{"%", "10.0.0.5", true}, {"10.%", "10.0.0.5", true}, {"10.%", "192.168.1.1", false},
		{"10.0.0.0/255.255.255.0", "10.0.0.9", true}, {"10.0.0.0/255.255.255.0", "10.0.1.9", false},
		{"localhost", "10.0.0.5", false}, {"app_.example.com", "app1.example.com", true},
	}
	for _, c := range cases {
		if Reachable(c.host, c.vdb) != c.ok {
			t.Errorf("%s vs %s", c.host, c.vdb)
		}
	}
}
