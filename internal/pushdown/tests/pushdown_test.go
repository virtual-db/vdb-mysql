// Package pushdown_test — tests/pushdown_test.go
//
// Kept, dropped and partially kept conditions; OR and NOT; added, masked and
// float columns; unknown functions (doc 10 §9).
package pushdown_test

import (
	"testing"

	core "github.com/virtual-db/vdb-core"

	. "github.com/virtual-db/vdb-mysql/internal/pushdown"
)

func table() *core.TableDef {
	return &core.TableDef{Database: "shop", Name: "users", Columns: []core.ColumnDef{
		{Name: "id"}, {Name: "name"}, {Name: "score", Float: true}, {Name: "added", Fetch: core.Default}, {Name: "email"},
	}}
}

func TestReduce(t *testing.T) {
	p, err := New("8.4.6", []string{"shop.users.email"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"(`users`.`id` > 5)", "id > 5"},
		{"(`users`.`id` > 5) and (`users`.`score` > 1)", "id > 5"},
		{"((`users`.`id` = 1) or (`users`.`added` = 2))", ""},
		{"(`users`.`name` = 'x')", ""},
		{"(`users`.`name` = _utf8mb4'x' collate utf8mb4_bin)", "`name` = _utf8mb4 'x' collate utf8mb4_bin"},
		{"(`users`.`email` like _utf8mb4'%a')", ""},
		{"(lower(`users`.`name`) = _utf8mb4'x')", ""},
		{"(`users`.`id` in (1,2,3)) and (not((`users`.`id` between 7 and 9)))", "id in (1, 2, 3) and not id between 7 and 9"},
	}
	for _, c := range cases {
		got, _ := p.Reduce(table(), c.in)
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
