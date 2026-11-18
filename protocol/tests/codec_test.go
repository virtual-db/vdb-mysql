// Package protocol_test — tests/codec_test.go
//
// Codec round trip of every message against testdata/ (doc 09 §6), framing
// limits, version negotiation and a server round trip with Waiting and
// ImagesNeeded frames.
package protocol_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	. "github.com/virtual-db/vdb-mysql/protocol"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestGoldenFrames(t *testing.T) {
	for _, g := range Goldens() {
		var buf bytes.Buffer
		if err := Encode(&buf, g.Op, g.Msg); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("..", "testdata", g.Name+".bin")
		if *update {
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./protocol/tests -update)", g.Name, err)
		}
		if !bytes.Equal(buf.Bytes(), want) {
			t.Fatalf("%s: frame differs from its golden file", g.Name)
		}
		op, body, err := ReadFrame(bytes.NewReader(want))
		if err != nil || op != g.Op {
			t.Fatalf("%s: read back op %s err %v", g.Name, op, err)
		}
		m := g.Msg.ProtoReflect().New().Interface()
		if err := Decode(body, m); err != nil || !proto.Equal(m, g.Msg) {
			t.Fatalf("%s: round trip differs", g.Name)
		}
	}
}

func TestFrameLimit(t *testing.T) {
	hdr := []byte{0x10, 0xff, 0xff, 0xff, 0x7f}
	if _, _, err := ReadFrame(bytes.NewReader(hdr)); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestVersions(t *testing.T) {
	if !Accepts(Version) || Accepts(Version+1) || Accepts(0) {
		t.Fatal("version range")
	}
}

type echo struct{}

func (echo) Hello(*Hello) (*HelloReply, error) { return &HelloReply{QueryIntercept: true}, nil }
func (echo) Serve(c *Conn, _ *Hello) {
	for {
		op, _, err := c.ReadRequest()
		if err != nil {
			return
		}
		_ = c.Waiting(42)
		r, err := c.AskImages(&ImagesNeeded{Table: 1, Rows: []*Row{{}}})
		if err != nil || len(r.Rows) != 1 {
			_ = c.Fail(&Error{Code: 1, Message: "no images"})
			continue
		}
		_ = c.Reply(op, &Empty{})
	}
}

func TestServerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vdb.sock")
	srv, err := Listen(path, "test", Handlers{Data: echo{}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := Dial(path, ConnKind_CONN_DATA, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Hello.QueryIntercept {
		t.Fatal("hello reply lost")
	}
	c.OnImages = func(*ImagesNeeded) *ImagesReply { return &ImagesReply{Rows: []*Images{{}}} }
	if err := c.Call(OpStmtEnd, &Empty{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.Waits) != 1 || c.Waits[0] != 42 {
		t.Fatalf("waits %v", c.Waits)
	}
	if _, err := Dial(path, ConnKind_CONN_LOGIN, 0); err == nil {
		t.Fatal("login kind has no handler and must be refused")
	}
}
