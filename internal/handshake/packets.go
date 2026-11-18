// Package handshake is VDB's own MySQL client handshake for the login relay
// (implementation plan doc 11 §1): the relay must pass the Source's challenge
// to a different client and pass the answer back, which no driver allows.
// It starts from the authentication spike's relay, which passed 73 of 73
// checks (experiments/2026-11-11-engine-shim/core/sourceauth.go).
package handshake

// Package handshake — packets.go: packet framing, OK, ERR and length-encoded integers.

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Deny is a refusal to pass to the client unchanged: code, SQLSTATE, text.
type Deny struct {
	Code  uint16
	State string
	Msg   string
}

func (d *Deny) Error() string { return fmt.Sprintf("%d (%s): %s", d.Code, d.State, d.Msg) }

// VDBDeny is an access-denied refusal VDB itself produces.
func VDBDeny(msg string) *Deny { return &Deny{Code: 1045, State: "28000", Msg: msg} }

func (c *Conn) readPacket() ([]byte, error) {
	var out []byte
	for {
		var h [4]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return nil, err
		}
		n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
		c.seq = h[3] + 1
		b := make([]byte, n)
		if _, err := io.ReadFull(c.br, b); err != nil {
			return nil, err
		}
		out = append(out, b...)
		if n < 0xffffff {
			return out, nil
		}
	}
}

func (c *Conn) writePacket(b []byte) error {
	h := []byte{byte(len(b)), byte(len(b) >> 8), byte(len(b) >> 16), c.seq}
	c.seq++
	_, err := c.c.Write(append(h, b...))
	return err
}

// ParseErr parses an ERR packet.
func ParseErr(p []byte) *Deny {
	if len(p) < 3 {
		return VDBDeny("malformed error from the Source")
	}
	d := &Deny{Code: binary.LittleEndian.Uint16(p[1:3]), State: "HY000"}
	rest := p[3:]
	if len(rest) > 6 && rest[0] == '#' {
		d.State, rest = string(rest[1:6]), rest[6:]
	}
	d.Msg = string(rest)
	return d
}

func lenenc(b []byte, n int) []byte {
	switch {
	case n < 251:
		return append(b, byte(n))
	case n < 1<<16:
		return append(b, 0xfc, byte(n), byte(n>>8))
	default:
		return append(b, 0xfd, byte(n), byte(n>>8), byte(n>>16))
	}
}

func readLenenc(p []byte) (uint64, int) {
	if len(p) == 0 {
		return 0, 0
	}
	switch p[0] {
	case 0xfc:
		return uint64(binary.LittleEndian.Uint16(p[1:])), 3
	case 0xfd:
		return uint64(p[1]) | uint64(p[2])<<8 | uint64(p[3])<<16, 4
	case 0xfe:
		return binary.LittleEndian.Uint64(p[1:]), 9
	}
	return uint64(p[0]), 1
}
