// Package control_test — tests/control_test.go
//
// Identifier and string quoting and statement rendering (doc 12 §6).
package control_test

import (
	"testing"

	. "github.com/virtual-db/vdb-mysql/internal/control"
)

func TestRendering(t *testing.T) {
	if Ident("a`b") != "`a``b`" || Str("o'k\\") != "'o''k\\\\'" || Account("u", "%") != "'u'@'%'" {
		t.Fatal("quoting")
	}
	if CreateDatabase("d", "utf8mb4", "utf8mb4_bin") != "CREATE DATABASE IF NOT EXISTS `d` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin" {
		t.Fatal(CreateDatabase("d", "utf8mb4", "utf8mb4_bin"))
	}
	if DropTable("d", "t") != "DROP TABLE IF EXISTS `d`.`t`" || Kill(7) != "KILL 7" {
		t.Fatal("statements")
	}
}
