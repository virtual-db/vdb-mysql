// Command vdb-mysql is VDB for MySQL: it wires vdb-core and
// vdb-mysql-engine together and runs them (implementation plan doc 09 §2.2).
// Everything it does lives in those two modules; this file only connects them.
package main

import (
	"log/slog"
	"os"

	core "github.com/virtual-db/vdb-core"
	engine "github.com/virtual-db/vdb-mysql-engine"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
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
