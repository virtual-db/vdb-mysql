// Package protocol — server.go
//
// The listener: creates the socket, accepts connections, reads Hello, checks
// the version, and hands each connection to the handler of its kind.
package protocol

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"google.golang.org/protobuf/proto"
)

// Handler serves every connection of one kind.
type Handler interface {
	// Hello validates a Hello and builds the reply.
	Hello(h *Hello) (*HelloReply, error)
	// Serve serves the connection until it closes. The server closes it after.
	Serve(c *Conn, h *Hello)
}

// DataHandler serves data connections (vdb_mysql_storage).
type DataHandler = Handler

// LoginHandler serves login connections (vdb_mysql_auth, vdb_mysql_auth_native).
type LoginHandler = Handler

// EventHandler serves event connections (vdb_mysql_statements).
type EventHandler = Handler

// KillHandler serves the kill connection.
type KillHandler = Handler

// Handlers names the handler of each connection kind.
type Handlers struct {
	Data  DataHandler
	Login LoginHandler
	Event EventHandler
	Kill  KillHandler
}

// Server accepts plug-in connections.
type Server struct {
	ln       net.Listener
	path     string
	build    string
	handlers Handlers
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[*Conn]bool
	closed   bool
}

// Listen creates the socket at path with mode 0600, replacing a stale one.
func Listen(path, build string, h Handlers) (*Server, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("protocol.listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("protocol.listen: chmod: %w", err)
	}
	s := &Server{ln: ln, path: path, build: build, handlers: h, conns: map[*Conn]bool{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Path returns the socket path.
func (s *Server) Path() string { return s.path }

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("accept failed", "pkg", "protocol", "err", err)
			continue
		}
		c := newConn(nc)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			nc.Close()
			return
		}
		s.conns[c] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(c)
	}
}

func (s *Server) serve(c *Conn) {
	defer s.wg.Done()
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	op, body, err := c.ReadRequest()
	if err != nil {
		return
	}
	if op != OpHello {
		_ = c.Fail(&Error{Code: CodeProtocol, Message: "the first message must be Hello"})
		return
	}
	var h Hello
	if err := Decode(body, &h); err != nil {
		_ = c.Fail(&Error{Code: CodeProtocol, Message: err.Error()})
		return
	}
	if !Accepts(h.Version) {
		_ = c.Fail(&Error{Code: CodeVersion, Message: fmt.Sprintf("protocol version %d refused: vdb-mysql %s speaks versions %d to %d; the plug-in library is %s",
			h.Version, s.build, MinVersion, Version, h.Build)})
		return
	}
	var hd Handler
	switch h.Kind {
	case ConnKind_CONN_DATA:
		hd = s.handlers.Data
	case ConnKind_CONN_LOGIN:
		hd = s.handlers.Login
	case ConnKind_CONN_EVENT:
		hd = s.handlers.Event
	case ConnKind_CONN_KILL:
		hd = s.handlers.Kill
	}
	if hd == nil {
		_ = c.Fail(&Error{Code: CodeUnavailable, Message: "no handler for this connection kind"})
		return
	}
	reply, err := hd.Hello(&h)
	if err != nil {
		_ = c.Fail(&Error{Code: CodeUnavailable, Message: err.Error()})
		return
	}
	if reply == nil {
		reply = &HelloReply{}
	}
	reply.Version = h.Version
	if reply.Build == "" {
		reply.Build = s.build
	}
	if err := c.Reply(OpHello, reply); err != nil {
		return
	}
	hd.Serve(c, &h)
}

// Close stops accepting, closes every connection and waits for handlers.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	err := s.ln.Close()
	s.wg.Wait()
	_ = os.Remove(s.path)
	return err
}

// Conn is one plug-in connection, used by one goroutine.
type Conn struct {
	nc     net.Conn
	once   sync.Once
	closed chan struct{}
}

func newConn(nc net.Conn) *Conn { return &Conn{nc: nc, closed: make(chan struct{})} }

// ReadRequest reads the next request frame.
func (c *Conn) ReadRequest() (Op, []byte, error) { return ReadFrame(c.nc) }

// Reply sends the reply of a request op.
func (c *Conn) Reply(op Op, msg proto.Message) error { return Encode(c.nc, op.Reply(), msg) }

// Raw sends a frame with an arbitrary body (tests that send malformed frames).
func (c *Conn) Raw(op Op, body []byte) error { return WriteFrame(c.nc, op, body) }

// Fail sends an error reply.
func (c *Conn) Fail(e *Error) error { return Encode(c.nc, OpError, e) }

// Waiting sends a lock wait notice naming the holder's connection id.
func (c *Conn) Waiting(holder uint64) error { return Encode(c.nc, OpWaiting, &Waiting{Holder: holder}) }

// AskImages sends an ImagesNeeded frame and reads the client's ImagesReply (D-86).
func (c *Conn) AskImages(req *ImagesNeeded) (*ImagesReply, error) {
	if err := Encode(c.nc, OpImagesNeeded, req); err != nil {
		return nil, err
	}
	op, body, err := ReadFrame(c.nc)
	if err != nil {
		return nil, err
	}
	if op != OpImagesNeeded {
		return nil, fmt.Errorf("protocol: expected ImagesNeeded reply, got %s", op)
	}
	var r ImagesReply
	if err := Decode(body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Close closes the connection.
func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.closed)
		c.nc.Close()
	})
}

// Done is closed when the connection closes.
func (c *Conn) Done() <-chan struct{} { return c.closed }
