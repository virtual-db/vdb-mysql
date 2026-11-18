// Package discovery_test — tests/define_test.go
//
// define.go on real SHOW CREATE TABLE forms: engine, AUTO_INCREMENT,
// partitions, row format, FULLTEXT and spatial indexes, invisible foreign keys
// removed; columns kept (doc 12 §6).
package discovery_test

import (
	"strings"
	"testing"

	"vitess.io/vitess/go/vt/sqlparser"

	. "github.com/virtual-db/vdb-mysql/internal/discovery"
)

func TestDefine(t *testing.T) {
	p, _ := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.4.0"})
	src := "CREATE TABLE `orders` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `user_id` int DEFAULT NULL,\n  `note` text COLLATE utf8mb4_bin,\n" +
		"  `total` decimal(10,2) GENERATED ALWAYS AS (1) VIRTUAL,\n  PRIMARY KEY (`id`),\n  FULLTEXT KEY `ft` (`note`),\n" +
		"  CONSTRAINT `fk_u` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`),\n  CONSTRAINT `fk_x` FOREIGN KEY (`user_id`) REFERENCES `hidden`.`x` (`id`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4 ROW_FORMAT=COMPRESSED\n/*!50100 PARTITION BY HASH (`id`) PARTITIONS 4 */"
	out, warns, err := Define(p, src, "shop", func(db, t string) bool { return db == "shop" })
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ToUpper(out)
	for _, gone := range []string{"INNODB", "AUTO_INCREMENT=42", "ROW_FORMAT", "PARTITION", "FULLTEXT", "FK_X"} {
		if strings.Contains(up, gone) {
			t.Errorf("%q not removed:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"VDB_MYSQL_STORAGE", "AUTO_INCREMENT,", "FK_U", "UTF8MB4_BIN", "VIRTUAL"} {
		if !strings.Contains(up, kept) {
			t.Errorf("%q not kept:\n%s", kept, out)
		}
	}
	if len(warns) != 2 {
		t.Fatalf("warnings: %v", warns)
	}
}
