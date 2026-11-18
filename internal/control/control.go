// Package control is the control channel (implementation plan doc 12 §2): a
// database/sql pool on mysqld's Unix socket as vdb_admin, which logs in with
// auth_socket, and one typed helper per statement kind VDB issues.
// Identifiers are always quoted with backticks, doubling backticks inside.
package control

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Admin is the control account.
const Admin = "vdb_admin"

// Exec runs statements; *Control implements it and tests record it.
type Exec interface {
	Exec(ctx context.Context, stmt string) error
	Query(ctx context.Context, stmt string) ([][]sql.RawBytes, error)
}

// Control is the control channel. Safe for concurrent use.
type Control struct {
	db *sql.DB
}

// Open opens the pool on socket. It does not connect.
func Open(socket string) (*Control, error) {
	c := mysql.NewConfig()
	c.User = Admin
	c.Net = "unix"
	c.Addr = socket
	c.MultiStatements = false
	c.InterpolateParams = false
	c.AllowCleartextPasswords = false
	c.Timeout = 5 * time.Second
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("control.open: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Minute)
	return &Control{db: db}, nil
}

// Close closes the pool.
func (c *Control) Close() error { return c.db.Close() }

// Ping checks a control login works.
func (c *Control) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

// Exec runs one statement.
func (c *Control) Exec(ctx context.Context, stmt string) error {
	_, err := c.db.ExecContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("control: %s: %w", firstWords(stmt), err)
	}
	return nil
}

// Query runs one statement and returns its rows as raw bytes.
func (c *Control) Query(ctx context.Context, stmt string) ([][]sql.RawBytes, error) {
	rows, err := c.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("control: %s: %w", firstWords(stmt), err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]sql.RawBytes
	for rows.Next() {
		raw := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		cp := make([]sql.RawBytes, len(raw))
		for i, b := range raw {
			if b != nil {
				cp[i] = append(sql.RawBytes(nil), b...)
			}
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

// Session runs a group of statements on one connection (session settings
// such as foreign_key_checks=0 and sql_mode apply to the whole group).
func (c *Control) Session(ctx context.Context, fn func(Exec) error) error {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("control.session: %w", err)
	}
	defer conn.Close()
	return fn(connExec{conn})
}

type connExec struct{ c *sql.Conn }

func (e connExec) Exec(ctx context.Context, stmt string) error {
	if _, err := e.c.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("control: %s: %w", firstWords(stmt), err)
	}
	return nil
}

func (e connExec) Query(ctx context.Context, stmt string) ([][]sql.RawBytes, error) {
	rows, err := e.c.QueryContext(ctx, stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]sql.RawBytes
	for rows.Next() {
		raw := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		cp := make([]sql.RawBytes, len(raw))
		for i, b := range raw {
			if b != nil {
				cp[i] = append(sql.RawBytes(nil), b...)
			}
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

func firstWords(s string) string {
	f := strings.Fields(s)
	if len(f) > 3 {
		f = f[:3]
	}
	return strings.Join(f, " ")
}

// ── statement rendering (pure functions, unit-tested) ──

// Ident quotes an identifier.
func Ident(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// Str renders a string literal.
func Str(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `''`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\x1a", `\Z`)
	return "'" + r.Replace(s) + "'"
}

// Account renders 'user'@'host'.
func Account(user, host string) string { return Str(user) + "@" + Str(host) }

// CreateDatabase renders CREATE DATABASE.
func CreateDatabase(db, charset, collation string) string {
	s := "CREATE DATABASE IF NOT EXISTS " + Ident(db)
	if charset != "" {
		s += " CHARACTER SET " + charset
	}
	if collation != "" {
		s += " COLLATE " + collation
	}
	return s
}

// DropTable renders DROP TABLE.
func DropTable(db, table string) string {
	return "DROP TABLE IF EXISTS " + Ident(db) + "." + Ident(table)
}

// Use renders USE.
func Use(db string) string { return "USE " + Ident(db) }

// SetSession renders SET SESSION name = value (value is already SQL).
func SetSession(name, value string) string { return "SET SESSION " + name + " = " + value }

// SetGlobal renders SET GLOBAL name = value (value is already SQL).
func SetGlobal(name, value string) string { return "SET GLOBAL " + name + " = " + value }

// Kill renders KILL.
func Kill(id uint64) string { return fmt.Sprintf("KILL %d", id) }

// PluginsActive renders the query that lists VDB's four plug-ins and their status.
func PluginsActive() string {
	return "SELECT PLUGIN_NAME, PLUGIN_STATUS FROM information_schema.PLUGINS WHERE PLUGIN_NAME IN ('vdb_mysql_storage','vdb_mysql_auth','vdb_mysql_auth_native','vdb_mysql_statements')"
}

// Shutdown renders SHUTDOWN.
func Shutdown() string { return "SHUTDOWN" }

// Plugins are the four plug-ins that must be ACTIVE.
var Plugins = []string{"vdb_mysql_storage", "vdb_mysql_auth", "vdb_mysql_auth_native", "vdb_mysql_statements"}

// CheckPlugins checks all four plug-ins are ACTIVE.
func CheckPlugins(ctx context.Context, e Exec) error {
	rows, err := e.Query(ctx, PluginsActive())
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, r := range rows {
		if string(r[1]) == "ACTIVE" {
			active[string(r[0])] = true
		}
	}
	var missing []string
	for _, p := range Plugins {
		if !active[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("control: plug-ins not ACTIVE: %s", strings.Join(missing, ", "))
	}
	return nil
}
