// Package config_test — tests/config_test.go
//
// Defaults, environment classes, YAML below environment variables, and
// invalid values naming their variable (doc 09 §6).
package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/virtual-db/vdb-mysql/internal/config"
)

func load(env map[string]string, yaml string) (*Config, error) {
	return LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(p string) ([]byte, error) {
		if yaml == "" {
			return nil, errors.New("no file")
		}
		return []byte(yaml), nil
	})
}

func TestDefaults(t *testing.T) {
	c, err := load(map[string]string{"VDB_SOURCE_HOST": "db", "VDB_SOURCE_USER": "svc"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Source.Port != 3306 || c.Source.BatchRows != 4096 || c.MemoryReserve != 0.10 || c.SchemaPoll != time.Minute || c.Source.MaxConnections != Classes["small"].SourceMaxConnections {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestPrecedenceAndErrors(t *testing.T) {
	c, err := load(map[string]string{"VDB_SOURCE_HOST": "db", "VDB_SOURCE_USER": "svc", "VDB_BATCH_ROWS": "100"}, "batch_rows: 50\nenv_class: large\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Source.BatchRows != 100 || c.EnvClass != "large" {
		t.Fatalf("env beats yaml: %+v", c.Source.BatchRows)
	}
	_, err = load(map[string]string{"VDB_SOURCE_USER": "svc", "VDB_MEMORY_RESERVE": "2"}, "")
	if err == nil || !strings.Contains(err.Error(), "VDB_SOURCE_HOST") || !strings.Contains(err.Error(), "VDB_MEMORY_RESERVE") {
		t.Fatalf("errors name the variables: %v", err)
	}
}
