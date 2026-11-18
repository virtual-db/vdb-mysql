// Package source_test — tests/source_test.go
//
// Literal rendering per column type and character set, and the identifier
// quoting the renderers rely on (doc 10 §9).
package source_test

import (
	"testing"

	core "github.com/virtual-db/vdb-core"

	. "github.com/virtual-db/vdb-mysql/internal/source"
)

func TestLiterals(t *testing.T) {
	cases := []struct {
		col  core.ColumnDef
		v    core.Value
		want string
	}{
		{core.ColumnDef{Type: "int"}, core.Value{B: []byte("-42")}, "-42"},
		{core.ColumnDef{Type: "int"}, core.Value{B: []byte("1; DROP")}, "X'313b2044524f50'"},
		{core.ColumnDef{Type: "varchar(10)", Charset: "utf8mb4", Collation: "utf8mb4_0900_ai_ci"}, core.Value{B: []byte("a'b")},
			"_utf8mb4 X'612762' COLLATE utf8mb4_0900_ai_ci"},
		{core.ColumnDef{Type: "varbinary(4)", Charset: "binary"}, core.Value{B: []byte{0, 1}}, "X'0001'"},
		{core.ColumnDef{Type: "timestamp(3)"}, core.Value{B: []byte("1700000000.123")}, "FROM_UNIXTIME(1700000000.123)"},
		{core.ColumnDef{Type: "datetime"}, core.Value{B: []byte("2026-01-01 00:00:00")}, "'2026-01-01 00:00:00'"},
		{core.ColumnDef{Type: "int"}, core.Value{Null: true}, "NULL"},
	}
	for _, c := range cases {
		if got := Literal(c.col, c.v); got != c.want {
			t.Errorf("%s %q: got %s want %s", c.col.Type, c.v.B, got, c.want)
		}
	}
	if Ident("a`b") != "`a``b`" {
		t.Fatal("identifier quoting")
	}
}
