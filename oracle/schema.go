// Package oracle — schema.go
//
// The tables of one seed (plan doc 15 §3, Seeder). Every seed gets the same
// five table shapes with seed-chosen sizes, actions and settings, created and
// filled identically on the Source and on the reference:
//
//	_a  integer primary key; binary, case-insensitive and latin1 strings;
//	    DECIMAL, DATETIME(3), TIMESTAMP, ENUM, JSON, DOUBLE; a nullable
//	    unique key; secondary keys, one in a collation not proven for order
//	_b  AUTO_INCREMENT primary key (always given explicitly) and a foreign
//	    key to _a with seed-chosen ON DELETE and ON UPDATE actions
//	_c  no key at all, with duplicate rows (D-10)
//	_d  composite binary-string and unsigned primary key
//	_s  read-only for clients; only the Source writes it (Source-writes mode)
//
// Row counts are chosen per seed so some tables cross the lookup batch
// (1,000 keys) and the Source page size the oracle runs with.
package oracle

import (
	"fmt"
	"math/rand"
	"strings"
)

// Schema is one seed's tables and session settings.
type Schema struct {
	Prefix   string
	Seed     int64
	RowsA    int
	RowsB    int
	RowsS    int
	OnDelete string // CASCADE, SET NULL or RESTRICT
	OnUpdate string // CASCADE or RESTRICT
	SQLMode  string
	TimeZone string
}

var (
	words    = []string{"alpha", "beta", "Gamma", "delta", "ALPHA", "x", "", "zeta ", "émile", "Ünder", "Beta", "gamma"}
	sqlModes = []string{
		"ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION",
		"STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION",
		"NO_ENGINE_SUBSTITUTION",
	}
	timeZones = []string{"+00:00", "-05:00", "+09:30"}
)

// NewSchema chooses a seed's sizes, actions and settings.
func NewSchema(prefix string, seed int64) *Schema {
	r := rand.New(rand.NewSource(seed*31 + 7))
	sizes := []int{120, 600, 1300}
	return &Schema{
		Prefix:   prefix,
		Seed:     seed,
		RowsA:    sizes[r.Intn(len(sizes))],
		RowsB:    150 + r.Intn(400),
		RowsS:    60 + r.Intn(120),
		OnDelete: []string{"CASCADE", "SET NULL", "RESTRICT"}[r.Intn(3)],
		OnUpdate: []string{"CASCADE", "RESTRICT"}[r.Intn(2)],
		SQLMode:  sqlModes[r.Intn(len(sqlModes))],
		TimeZone: timeZones[r.Intn(len(timeZones))],
	}
}

// T names one of the seed's tables: T("a") is <prefix>_a.
func (s *Schema) T(x string) string { return s.Prefix + "_" + x }

// Settings are the session statements every connection of the seed runs first.
func (s *Schema) Settings() []string {
	return []string{"SET SESSION sql_mode = '" + s.SQLMode + "'", "SET SESSION time_zone = '" + s.TimeZone + "'"}
}

func str(r *rand.Rand) string {
	return "'" + words[r.Intn(len(words))] + fmt.Sprint(r.Intn(4)) + "'"
}

// DDL returns the statements that create the seed's tables.
func (s *Schema) DDL() []string {
	return []string{
		fmt.Sprintf("DROP TABLE IF EXISTS %s, %s, %s, %s, %s", s.T("b"), s.T("a"), s.T("c"), s.T("d"), s.T("s")),
		fmt.Sprintf(`CREATE TABLE %s (
  id INT NOT NULL PRIMARY KEY,
  k INT NOT NULL,
  s VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  ci VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci,
  l VARCHAR(16) CHARACTER SET latin1 COLLATE latin1_bin,
  n DECIMAL(10,2),
  t DATETIME(3),
  ts TIMESTAMP NULL,
  u INT NULL,
  e ENUM('x','y','z') NOT NULL DEFAULT 'x',
  j JSON NULL,
  f DOUBLE,
  KEY k (k), KEY s (s), KEY ci (ci), UNIQUE KEY u (u)) ENGINE=InnoDB`, s.T("a")),
		fmt.Sprintf(`CREATE TABLE %s (
  id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  a_id INT NULL,
  v INT NOT NULL,
  w VARCHAR(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  KEY a_id (a_id),
  CONSTRAINT %s FOREIGN KEY (a_id) REFERENCES %s (id) ON DELETE %s ON UPDATE %s) ENGINE=InnoDB`,
			s.T("b"), s.T("b")+"_fk", s.T("a"), s.OnDelete, s.OnUpdate),
		fmt.Sprintf("CREATE TABLE %s (x INT, y VARCHAR(8) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin) ENGINE=InnoDB", s.T("c")),
		fmt.Sprintf(`CREATE TABLE %s (
  g CHAR(3) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  h BIGINT UNSIGNED NOT NULL,
  val VARCHAR(40) CHARACTER SET latin1 COLLATE latin1_bin,
  PRIMARY KEY (g, h)) ENGINE=InnoDB`, s.T("d")),
		fmt.Sprintf("CREATE TABLE %s (id INT NOT NULL PRIMARY KEY, q INT NOT NULL, r VARCHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin, KEY q (q)) ENGINE=InnoDB", s.T("s")),
	}
}

// Rows returns the statements that fill the seed's tables, in batches.
func (s *Schema) Rows() []string {
	r := rand.New(rand.NewSource(s.Seed))
	var out []string
	batch := func(table string, vals []string) {
		for i := 0; i < len(vals); i += 200 {
			end := i + 200
			if end > len(vals) {
				end = len(vals)
			}
			out = append(out, "INSERT INTO "+table+" VALUES "+strings.Join(vals[i:end], ", "))
		}
	}
	var a []string
	for i := 1; i <= s.RowsA; i++ {
		u := "NULL"
		if i%3 == 0 {
			u = fmt.Sprint(i * 7)
		}
		ts := "NULL"
		if i%4 != 0 {
			ts = fmt.Sprintf("'2026-03-%02d 12:%02d:%02d'", 1+r.Intn(27), r.Intn(60), r.Intn(60))
		}
		j := "NULL"
		if i%5 != 0 {
			j = fmt.Sprintf(`'{"v": %d, "w": "%s"}'`, r.Intn(50), words[r.Intn(len(words))])
		}
		a = append(a, fmt.Sprintf("(%d, %d, %s, %s, 'l%d', %d.%02d, '2026-01-%02d 10:00:%02d.%03d', %s, %s, '%s', %s, %d.25)",
			i*2, r.Intn(20), str(r), str(r), r.Intn(9), r.Intn(1000), r.Intn(100), 1+r.Intn(28), r.Intn(60), r.Intn(1000),
			ts, u, []string{"x", "y", "z"}[r.Intn(3)], j, r.Intn(100)))
	}
	batch(s.T("a"), a)
	var b []string
	for i := 1; i <= s.RowsB; i++ {
		aid := "NULL"
		if r.Intn(6) != 0 {
			aid = fmt.Sprint(2 * (1 + r.Intn(s.RowsA)))
		}
		b = append(b, fmt.Sprintf("(%d, %s, %d, 'w%d')", i, aid, r.Intn(50), r.Intn(7)))
	}
	batch(s.T("b"), b)
	var c []string
	for i := 0; i < 60; i++ {
		x := r.Intn(6)
		c = append(c, fmt.Sprintf("(%d, 'v%d')", x, x))
	}
	batch(s.T("c"), c)
	var d []string
	for _, g := range []string{"abc", "abd", "ABC", "b", "zz"} {
		for h := 0; h < 30; h++ {
			d = append(d, fmt.Sprintf("('%s', %d, 'val%d')", g, uint64(h)*1000003, r.Intn(20)))
		}
	}
	batch(s.T("d"), d)
	var sv []string
	for i := 1; i <= s.RowsS; i++ {
		sv = append(sv, fmt.Sprintf("(%d, %d, 'r%d')", i, r.Intn(15), r.Intn(9)))
	}
	batch(s.T("s"), sv)
	return out
}

// Tables lists the seed's tables, parents first.
func (s *Schema) Tables() []string {
	return []string{s.T("a"), s.T("b"), s.T("c"), s.T("d"), s.T("s")}
}

// Checksum returns a statement that summarises a table's rows, so a write is
// followed by a comparison of the whole table at the cost of one row.
func (s *Schema) Checksum(table string) string {
	cols := map[string]string{
		s.T("a"): "id, k, s, ci, l, n, t, ts, u, e, j, f",
		s.T("b"): "id, a_id, v, w",
		s.T("c"): "x, y",
		s.T("d"): "g, h, val",
		s.T("s"): "id, q, r",
	}[table]
	if cols == "" {
		return ""
	}
	return fmt.Sprintf("SELECT COUNT(*), BIT_XOR(CRC32(CONCAT_WS('|', %s))) FROM %s", cols, table)
}
