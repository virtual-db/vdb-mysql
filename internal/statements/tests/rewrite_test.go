// Package statements_test — tests/rewrite_test.go
//
// Rewrite on a corpus of CREATE TABLE forms (doc 09 §6): every engine clause
// spelling, comments, strings containing ENGINE=, partitions, CREATE
// TEMPORARY TABLE, LIKE and AS SELECT.
package statements_test

import (
	"testing"

	"vitess.io/vitess/go/vt/sqlparser"

	. "github.com/virtual-db/vdb-mysql/internal/statements"
)

func TestEngineClauses(t *testing.T) {
	p, _ := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.4.0"})
	cases := []struct{ in, want string }{
		{"CREATE TABLE t (a int) ENGINE=InnoDB", "CREATE TABLE t (a int) "},
		{"create table t (a int) engine = innodb default charset=utf8mb4", "create table t (a int)  default charset=utf8mb4"},
		{"CREATE TABLE t (a int) DEFAULT CHARSET=utf8mb4, ENGINE=MyISAM", "CREATE TABLE t (a int) DEFAULT CHARSET=utf8mb4"},
		{"CREATE TABLE t (a int, c varchar(9) DEFAULT 'ENGINE=x') COMMENT='ENGINE=InnoDB'", ""},
		{"CREATE TABLE t (a int) /* ENGINE=InnoDB */", ""},
		{"CREATE TEMPORARY TABLE t (a int) ENGINE=InnoDB", ""},
		{"CREATE TABLE t LIKE u", ""},
		{"CREATE TABLE t AS SELECT 1 AS a", ""},
		{"CREATE TABLE t (a int) ENGINE InnoDB PARTITION BY HASH(a) (PARTITION p0 ENGINE=InnoDB)", "CREATE TABLE t (a int)  PARTITION BY HASH(a) (PARTITION p0 ENGINE=InnoDB)"},
	}
	for _, c := range cases {
		rs := EngineClauses(p, c.in)
		got := ""
		if len(rs) > 0 {
			got = Apply(c.in, rs)
		}
		if got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
