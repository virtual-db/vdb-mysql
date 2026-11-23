// Command vdb-mysql is VDB for MySQL: it wires vdb-core and
// vdb-mysql-engine together and runs them (implementation plan doc 09 §2.2).
// Everything it does lives in those two modules; this file only connects them.
package main

import (
	"fmt"
	"log/slog"
	"os"

	core "github.com/virtual-db/vdb-core"
	engine "github.com/virtual-db/vdb-mysql-engine"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	if len(os.Args) == 3 && os.Args[1] == "check-collations" {
		os.Exit(checkCollations(os.Args[2]))
	}
	cfg, err := engine.ConfigFromEnv()
	if err != nil {
		slog.Error("vdb-mysql: configuration", "pkg", "main", "err", err)
		os.Exit(2)
	}
	engine.Version = version

	app := core.New(cfg.CoreConfig())
	driver := engine.NewDriver(cfg, app.DriverAPI())
	app.UseDriver(driver)

	if err := app.Run(); err != nil {
		slog.Error("vdb-mysql stopped", "pkg", "main", "err", err)
		os.Exit(1)
	}
}

// checkCollations is the image build's check (vdb-mysql#23): the engine's
// proven.yaml must have an entry for the image's mysqld release, or every
// non-binary string index of that image would read whole tables.
func checkCollations(release string) int {
	if engine.CollationsProven(release) {
		fmt.Printf("vdb-mysql: collations proven for mysqld %s\n", release)
		return 0
	}
	fmt.Fprintf(os.Stderr, "vdb-mysql: no proven collations for mysqld %s in vdb-mysql-engine's proven.yaml.\n"+
		"Run the collation proof suite for this release (vdb-tests: runner collations) and add its results to\n"+
		"vdb-mysql-engine/internal/collations/proven.yaml by pull request first.\n", release)
	return 1
}
