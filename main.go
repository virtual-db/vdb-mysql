// Command vdb-mysql is VDB for MySQL: it supervises a stock mysqld that
// loads vdb.so, serves the plug-ins over the plug-in protocol, and keeps the
// Delta in vdb-core (implementation plan doc 09 §2).
package main

import (
	"log/slog"
	"os"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/internal/config"
	"github.com/virtual-db/vdb-mysql/internal/server"
	"github.com/virtual-db/vdb-mysql/internal/source"
	"github.com/virtual-db/vdb-mysql/internal/supervisor"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		setupLogging("json")
		slog.Error("vdb-mysql: configuration", "pkg", "main", "err", err)
		os.Exit(2)
	}
	setupLogging(cfg.LogFormat)

	src := source.New(cfg.Source) // not connected yet
	sup := supervisor.New(cfg.Mysqld, cfg.Socket, cfg.MysqldSocket)

	app := core.New(core.Config{
		PluginDir:        cfg.PluginDir,
		MemoryReserve:    cfg.MemoryReserve,
		DeltaMaxBytes:    cfg.DeltaMaxBytes,
		MemoryProbe:      supervisor.MemoryProbe(sup),
		AutoIncrementGap: cfg.AutoIncrementGap,
		CountDeclineKeys: cfg.CountMaxShadowed,
		LookupBatch:      cfg.Source.LookupBatch,
	})
	app.UseSource(src)

	api := app.DriverAPI()
	srv := server.New(cfg, api, app, src, sup)
	srv.Version = version
	app.UseDriver(srv)

	if err := app.Run(); err != nil {
		slog.Error("vdb-mysql stopped", "pkg", "main", "err", err)
		os.Exit(1)
	}
}

// setupLogging installs log/slog: JSON by default, text with VDB_LOG_FORMAT=text (D-82).
func setupLogging(format string) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if os.Getenv("VDB_LOG_LEVEL") == "debug" {
		opts.Level = slog.LevelDebug
	}
	var h slog.Handler = slog.NewJSONHandler(os.Stderr, opts)
	if format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}
