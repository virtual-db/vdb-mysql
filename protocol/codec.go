// Package protocol — codec.go
//
// Marshalling bodies with the op code and frame of doc 07 §1.
package protocol

import (
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// Encode writes msg as a frame of op.
func Encode(w io.Writer, op Op, msg proto.Message) error {
	var body []byte
	if msg != nil {
		var err error
		body, err = proto.Marshal(msg)
		if err != nil {
			return fmt.Errorf("protocol: marshal %s: %w", op, err)
		}
	}
	return WriteFrame(w, op, body)
}

// Decode unmarshals a body into msg.
func Decode(body []byte, msg proto.Message) error {
	if err := proto.Unmarshal(body, msg); err != nil {
		return fmt.Errorf("protocol: malformed body: %w", err)
	}
	return nil
}

// Error codes beyond the core's error codes (doc 07 §4).
const (
	CodeProtocol    = 1000 // unknown op, malformed body
	CodeVersion     = 1001 // protocol version refused
	CodeUnavailable = 1002 // vdb-mysql cannot serve the request
)
