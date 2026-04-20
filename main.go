package main

import (
	"log"
	"os"

	core "github.com/virtual-db/core"
	mysqldriver "github.com/virtual-db/mysql-driver"
)

func main() {
	app := core.New(core.Config{
		PluginDir: env("VDB_PLUGIN_DIR", "plugins"),
	})
	driver := mysqldriver.NewDriver(mysqldriver.Config{
		Addr:           env("VDB_LISTEN_ADDR", ":3306"),
		DBName:         env("VDB_DB_NAME", "appdb"),
		SourceDSN:      env("VDB_SOURCE_DSN", ""),
		AuthSourceAddr: env("VDB_AUTH_SOURCE_ADDR", ""),
	}, app.DriverAPI())
	app.UseDriver(driver)

	if err := app.Run(); err != nil {
		log.Fatalf("vdb-mysql: %v", err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
