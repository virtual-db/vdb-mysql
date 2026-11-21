// Package oracle — env.go
//
// Connections to the three servers of a run (plan doc 15 §2.1): the Source,
// the VDB image under test, and the reference (the same mysqld without
// vdb.so), and the waits a run needs before it starts.
package oracle

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Open opens a pool to one server.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("oracle: open: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

// WaitReady polls a DSN until it accepts a ping or the timeout expires. The
// driver's connection-refused noise is discarded while waiting.
func WaitReady(dsn string, timeout time.Duration) error {
	_ = mysql.SetLogger(log.New(io.Discard, "", 0))
	defer func() { _ = mysql.SetLogger(log.New(log.Writer(), "mysql: ", log.LstdFlags)) }()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			err = db.Ping()
			db.Close()
			if err == nil {
				return nil
			}
		}
		last = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("oracle: %s not ready after %s: %w", maskDSN(dsn), timeout, last)
}

// WaitTablesLike blocks until every Source base table of testdb whose name
// matches like is visible in VDB, which learns new Source tables through its
// schema poller, or until the timeout.
func WaitTablesLike(source, vdb *sql.DB, like string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		missing, err := missingTables(source, vdb, like)
		if err == nil && missing == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("oracle: %d Source tables like %q not visible in VDB", missing, like)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func missingTables(source, vdb *sql.DB, like string) (int, error) {
	rows, err := source.Query(`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'testdb' AND TABLE_TYPE = 'BASE TABLE' AND TABLE_NAME LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	missing := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, err
		}
		var n int
		if err := vdb.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = 'testdb' AND TABLE_NAME = ?`, name).Scan(&n); err != nil || n == 0 {
			missing++
		}
	}
	return missing, rows.Err()
}

func maskDSN(dsn string) string {
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == '@' {
			return "***" + dsn[i:]
		}
	}
	return dsn
}
