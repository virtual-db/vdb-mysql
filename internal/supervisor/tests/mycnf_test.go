// Package supervisor_test — tests/mycnf_test.go
//
// Option-file rendering with and without networking, and VmRSS parsing (doc 12 §6).
package supervisor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/virtual-db/vdb-mysql/internal/supervisor"
)

func TestMyCnf(t *testing.T) {
	o := Options{Datadir: "/d", Socket: "/s", Port: 3306, VdbSocket: "/v", SortBuffer: "1M", MaxConnections: 9}
	off, _ := RenderMyCnf(o)
	o.Networking = true
	on, _ := RenderMyCnf(o)
	for _, want := range []string{"default_storage_engine = vdb_mysql_storage", "vdb_mysql_storage_socket = /v", "partial_revokes = ON", "max_connections = 9"} {
		if !strings.Contains(on, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(off, "skip-networking") || strings.Contains(on, "skip-networking") {
		t.Fatal("networking switch")
	}
}

func TestVmRSS(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status")
	os.WriteFile(p, []byte("Name:\tx\nVmRSS:\t  2048 kB\n"), 0o644)
	if n, err := VmRSS(p); err != nil || n != 2048*1024 {
		t.Fatalf("%d %v", n, err)
	}
}
