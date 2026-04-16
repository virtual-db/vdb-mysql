package main

import (
	"log"
	"os"

	core "github.com/AnqorDX/vdb-core"
	mysqldriver "github.com/AnqorDX/vdb-mysql-driver"
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

	// Wire the built-in MySQL credential probe at priority 50.
	// This is the composition-root step required by the authentication design:
	// the driver provides the PointFunc but cannot attach it to the pipeline
	// itself because it has no reference to app.
	app.Attach("vdb.connection.auth.verify", 50, driver.AuthHandler())

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
