// Package protocol — frame.go
//
// Framing (doc 07 §1): 1 byte op, 4 bytes body length (little-endian,
// unsigned), body. A body larger than MaxBody is a protocol error that
// closes the connection.
package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MaxBody is the largest body accepted: 64 MiB.
const MaxBody = 64 << 20

// ErrTooLarge is returned for a frame whose length exceeds MaxBody.
var ErrTooLarge = fmt.Errorf("protocol: frame body exceeds %d bytes", MaxBody)

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) (Op, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[1:])
	if n > MaxBody {
		return 0, nil, ErrTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return Op(hdr[0]), body, nil
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, op Op, body []byte) error {
	if len(body) > MaxBody {
		return ErrTooLarge
	}
	buf := make([]byte, 5+len(body))
	buf[0] = byte(op)
	binary.LittleEndian.PutUint32(buf[1:], uint32(len(body)))
	copy(buf[5:], body)
	_, err := w.Write(buf)
	return err
}
