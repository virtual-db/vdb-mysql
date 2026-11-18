// Package config reads vdb-mysql's settings (implementation plan doc 13 §3):
// environment variables first, then /etc/vdb/vdb.yaml (VDB_CONFIG_FILE), then
// defaults. Every value is validated; Load fails with a message naming the
// variable.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Source holds the Source connection settings.
type Source struct {
	Host           string
	Port           int
	User           string
	Password       string
	Databases      []string
	TLSFingerprint string
	CADir          string

	MaxConnections int
	ConnectWait    time.Duration
	RequestTimeout time.Duration
	RequestMaxRows int
	MaxSourceRows  int64
	RetryWindow    time.Duration
	Snapshot       bool
	SnapshotIdle   time.Duration
	SnapshotMaxAge time.Duration
	SnapshotPolicy string // strict or relaxed
	BusyThreads    int
	LogSize        int
	LogFile        string
	Pushdown       bool
	BatchRows      int
	BatchBytes     int
	LookupBatch    int
	EnvID          string
}

// Mysqld holds the settings of the supervised mysqld.
type Mysqld struct {
	Basedir         string
	Datadir         string
	Template        string
	RunDir          string
	TmpDir          string
	FilesDir        string
	ListenPort      int
	ClientCert      string
	ClientKey       string
	StopTimeout     time.Duration
	RestartWindow   time.Duration
	SortBuffer      string
	JoinBuffer      string
	TmpTableSize    string
	TemptableMaxRAM string
	MaxConnections  int
}

// Config is every setting.
type Config struct {
	Source           Source
	Mysqld           Mysqld
	EnvClass         string
	MemoryReserve    float64
	DeltaMaxBytes    uint64
	AutoIncrementGap uint64
	InsertBatch      int
	CountMaxShadowed int
	SchemaPoll       time.Duration
	GrantPoll        time.Duration
	LogFormat        string
	PluginDir        string
	Socket           string // the plug-in protocol socket
	MysqldSocket     string
	Version          string
}

// lookup reads a setting: environment variable, then YAML, then empty.
type lookup struct {
	env  func(string) (string, bool)
	yaml map[string]string
}

func (l lookup) get(name string) (string, bool) {
	if v, ok := l.env("VDB_" + strings.ToUpper(name)); ok && v != "" {
		return v, true
	}
	if v, ok := l.yaml[strings.ToLower(name)]; ok {
		return v, true
	}
	return "", false
}

type errs []string

func (e *errs) add(name, msg string) { *e = append(*e, "VDB_"+strings.ToUpper(name)+": "+msg) }

func (l lookup) str(name, def string) string {
	if v, ok := l.get(name); ok {
		return v
	}
	return def
}

func (l lookup) integer(e *errs, name string, def int) int {
	v, ok := l.get(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		e.add(name, "must be a non-negative integer, got "+strconv.Quote(v))
		return def
	}
	return n
}

func (l lookup) uint64(e *errs, name string, def uint64) uint64 {
	v, ok := l.get(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		e.add(name, "must be a non-negative integer, got "+strconv.Quote(v))
		return def
	}
	return n
}

func (l lookup) float(e *errs, name string, def, lo, hi float64) float64 {
	v, ok := l.get(name)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < lo || f > hi {
		e.add(name, fmt.Sprintf("must be a number from %g to %g, got %q", lo, hi, v))
		return def
	}
	return f
}

func (l lookup) duration(e *errs, name string, def time.Duration) time.Duration {
	v, ok := l.get(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		// A bare number is seconds.
		if n, err2 := strconv.Atoi(v); err2 == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		e.add(name, "must be a duration such as 30s, got "+strconv.Quote(v))
		return def
	}
	return d
}

func (l lookup) onOff(e *errs, name string, def bool) bool {
	v, ok := l.get(name)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "on", "true", "1", "yes":
		return true
	case "off", "false", "0", "no":
		return false
	}
	e.add(name, "must be on or off, got "+strconv.Quote(v))
	return def
}

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.LookupEnv, os.ReadFile) }

// LoadFrom reads the configuration with the given environment and file readers (tests).
func LoadFrom(env func(string) (string, bool), readFile func(string) ([]byte, error)) (*Config, error) {
	l := lookup{env: env, yaml: map[string]string{}}
	var e errs
	path := "/etc/vdb/vdb.yaml"
	if p, ok := env("VDB_CONFIG_FILE"); ok && p != "" {
		path = p
	}
	if b, err := readFile(path); err == nil {
		raw := map[string]any{}
		if err := yaml.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
		for k, v := range raw {
			l.yaml[strings.ToLower(k)] = fmt.Sprint(v)
		}
	} else if _, set := env("VDB_CONFIG_FILE"); set && !os.IsNotExist(err) {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}

	c := &Config{}
	c.EnvClass = l.str("env_class", "small")
	class, ok := Classes[c.EnvClass]
	if !ok {
		e.add("env_class", "unknown class "+strconv.Quote(c.EnvClass)+"; known: small, medium, large")
		class = Classes["small"]
	}
	host, _ := os.Hostname()
	s := &c.Source
	s.Host = l.str("source_host", "")
	if s.Host == "" {
		e.add("source_host", "is required")
	}
	s.Port = l.integer(&e, "source_port", 3306)
	s.User = l.str("source_user", "")
	if s.User == "" {
		e.add("source_user", "is required")
	}
	s.Password = l.str("source_password", "")
	if pf := l.str("source_password_file", ""); pf != "" {
		b, err := readFile(pf)
		if err != nil {
			e.add("source_password_file", err.Error())
		} else {
			s.Password = strings.TrimRight(string(b), "\r\n")
		}
	}
	if dbs := l.str("source_databases", ""); dbs != "" {
		for _, d := range strings.Split(dbs, ",") {
			if d = strings.TrimSpace(d); d != "" {
				s.Databases = append(s.Databases, d)
			}
		}
	}
	s.TLSFingerprint = strings.ToLower(strings.ReplaceAll(l.str("source_tls_fingerprint", ""), ":", ""))
	s.CADir = l.str("ca_dir", "/usr/share/vdb/ca")
	s.EnvID = l.str("env_id", host)
	s.MaxConnections = l.integer(&e, "source_max_connections", class.SourceMaxConnections)
	s.ConnectWait = l.duration(&e, "source_connect_wait", 5*time.Second)
	s.RequestTimeout = l.duration(&e, "source_request_timeout", 30*time.Second)
	s.RequestMaxRows = l.integer(&e, "source_request_max_rows", 100000)
	s.MaxSourceRows = int64(l.integer(&e, "max_source_rows", 0))
	s.RetryWindow = l.duration(&e, "source_retry_window", 10*time.Second)
	s.Snapshot = l.onOff(&e, "source_snapshot", true)
	s.SnapshotIdle = l.duration(&e, "source_snapshot_idle_timeout", 60*time.Second)
	s.SnapshotMaxAge = l.duration(&e, "source_snapshot_max_age", 15*time.Minute)
	s.SnapshotPolicy = l.str("source_snapshot_policy", "strict")
	if s.SnapshotPolicy != "strict" && s.SnapshotPolicy != "relaxed" {
		e.add("source_snapshot_policy", "must be strict or relaxed")
	}
	s.BusyThreads = l.integer(&e, "source_busy_threads", 64)
	s.LogSize = l.integer(&e, "source_log_size", 10000)
	s.LogFile = l.str("source_log_file", "")
	s.Pushdown = l.onOff(&e, "pushdown", true)
	s.BatchRows = l.integer(&e, "batch_rows", 4096)
	s.BatchBytes = l.integer(&e, "batch_bytes", 524288)
	s.LookupBatch = l.integer(&e, "lookup_batch", 1000)
	if s.BatchRows == 0 || s.BatchBytes == 0 || s.LookupBatch == 0 {
		e.add("batch_rows", "batch sizes must be above zero")
	}

	m := &c.Mysqld
	m.Basedir = l.str("mysqld_basedir", "/usr/local/mysql")
	m.Datadir = l.str("mysqld_datadir", "/var/lib/vdb/mysql")
	m.Template = l.str("mysqld_template", "/usr/share/vdb/template")
	m.RunDir = l.str("run_dir", "/run/vdb")
	m.TmpDir = l.str("mysqld_tmpdir", "/var/lib/vdb/tmp")
	m.FilesDir = l.str("mysqld_files_dir", "/var/lib/vdb/files")
	m.ListenPort = l.integer(&e, "listen_port", 3306)
	if m.ListenPort == 0 || m.ListenPort > 65535 {
		e.add("listen_port", "must be a port number")
	}
	m.ClientCert = l.str("client_tls_cert_file", "")
	m.ClientKey = l.str("client_tls_key_file", "")
	if (m.ClientCert == "") != (m.ClientKey == "") {
		e.add("client_tls_cert_file", "VDB_CLIENT_TLS_CERT_FILE and VDB_CLIENT_TLS_KEY_FILE must be set together")
	}
	m.StopTimeout = l.duration(&e, "mysqld_stop_timeout", 30*time.Second)
	m.RestartWindow = l.duration(&e, "mysqld_restart_window", 5*time.Minute)
	m.SortBuffer = l.str("mysqld_sort_buffer_size", class.SortBuffer)
	m.JoinBuffer = l.str("mysqld_join_buffer_size", class.JoinBuffer)
	m.TmpTableSize = l.str("mysqld_tmp_table_size", class.TmpTableSize)
	m.TemptableMaxRAM = l.str("mysqld_temptable_max_ram", class.TemptableMaxRAM)
	m.MaxConnections = l.integer(&e, "mysqld_max_connections", class.MaxConnections)

	c.MemoryReserve = l.float(&e, "memory_reserve", 0.10, 0.01, 0.9)
	c.DeltaMaxBytes = l.uint64(&e, "delta_max_bytes", 0)
	c.AutoIncrementGap = l.uint64(&e, "auto_increment_gap", 1000000)
	c.InsertBatch = l.integer(&e, "insert_batch", 1000)
	c.CountMaxShadowed = l.integer(&e, "count_max_shadowed", 100000)
	c.SchemaPoll = l.duration(&e, "schema_poll", 60*time.Second)
	c.GrantPoll = l.duration(&e, "grant_poll", 30*time.Second)
	c.LogFormat = l.str("log_format", "json")
	if c.LogFormat != "json" && c.LogFormat != "text" {
		e.add("log_format", "must be json or text")
	}
	c.PluginDir = l.str("plugin_dir", "/var/lib/vdb/plugins")
	c.Socket = m.RunDir + "/vdb.sock"
	c.MysqldSocket = m.RunDir + "/mysqld.sock"

	if len(e) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(e, "; "))
	}
	return c, nil
}
