// Package protocol — client.go
//
// A client for tests: dials, sends Hello, sends requests and reads replies,
// answering ImagesNeeded frames through a callback and collecting Waiting
// notices.
package protocol

import (
	"fmt"
	"net"

	"google.golang.org/protobuf/proto"
)

// Client is a test client of the protocol.
type Client struct {
	nc       net.Conn
	Hello    *HelloReply
	Waits    []uint64
	OnImages func(*ImagesNeeded) *ImagesReply
}

// Dial connects and sends Hello.
func Dial(path string, kind ConnKind, connID uint64) (*Client, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	c := &Client{nc: nc}
	var hr HelloReply
	if err := c.Call(OpHello, &Hello{Version: Version, Kind: kind, Build: "test", ConnId: connID}, &hr); err != nil {
		nc.Close()
		return nil, err
	}
	c.Hello = &hr
	return c, nil
}

// RemoteError is an error reply.
type RemoteError struct{ E *Error }

func (r *RemoteError) Error() string { return fmt.Sprintf("vdb error %d: %s", r.E.Code, r.E.Message) }

// Call sends a request and reads its reply into reply (which may be nil).
func (c *Client) Call(op Op, req proto.Message, reply proto.Message) error {
	if err := Encode(c.nc, op, req); err != nil {
		return err
	}
	for {
		rop, body, err := ReadFrame(c.nc)
		if err != nil {
			return err
		}
		switch rop {
		case OpWaiting:
			var w Waiting
			_ = Decode(body, &w)
			c.Waits = append(c.Waits, w.Holder)
			continue
		case OpImagesNeeded:
			var q ImagesNeeded
			if err := Decode(body, &q); err != nil {
				return err
			}
			var r *ImagesReply
			if c.OnImages != nil {
				r = c.OnImages(&q)
			} else {
				r = &ImagesReply{Error: &Error{Code: CodeUnavailable, Message: "no image callback"}}
			}
			if err := Encode(c.nc, OpImagesNeeded, r); err != nil {
				return err
			}
			continue
		case OpError:
			var e Error
			if err := Decode(body, &e); err != nil {
				return err
			}
			return &RemoteError{E: &e}
		}
		if rop != op.Reply() {
			return fmt.Errorf("protocol: expected %s, got %s", op.Reply(), rop)
		}
		if reply != nil {
			return Decode(body, reply)
		}
		return nil
	}
}

// Send sends a request that has no reply (Event).
func (c *Client) Send(op Op, req proto.Message) error { return Encode(c.nc, op, req) }

// Close closes the connection.
func (c *Client) Close() error { return c.nc.Close() }
