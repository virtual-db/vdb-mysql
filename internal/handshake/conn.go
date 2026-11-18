// Package handshake — conn.go: dialing, the greeting, the TLS upgrade, the
// handshake response, method switches, results, and queries after success.
package handshake

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	capLongPassword     = 0x00000001
	capLongFlag         = 0x00000004
	capProtocol41       = 0x00000200
	capSSL              = 0x00000800
	capTransactions     = 0x00002000
	capSecureConn       = 0x00008000
	capMultiResults     = 0x00020000
	capPluginAuth       = 0x00080000
	capConnectAttrs     = 0x00100000
	capPluginAuthLenenc = 0x00200000
)

// Login methods.
const (
	Sha2   = "caching_sha2_password"
	Native = "mysql_native_password"
	Sha256 = "sha256_password"
)

// probeMethod is a client method no server plug-in uses: naming it makes the
// Source answer with a method switch carrying the account's own method and
// challenge, for unknown users too, so it reveals nothing.
const probeMethod = "vdb_relay_probe"

// Conn is one login relay connection to the Source.
type Conn struct {
	raw       net.Conn
	c         net.Conn
	br        *bufio.Reader
	seq       byte
	nonce     []byte
	Version   string
	ServerDef string // the Source's default method from the greeting
}

func clientCaps() uint32 {
	return capLongPassword | capLongFlag | capProtocol41 | capSSL | capTransactions | capSecureConn |
		capMultiResults | capPluginAuth | capPluginAuthLenenc | capConnectAttrs
}

// Dial connects, reads the greeting and upgrades to verified TLS. A Source
// that does not offer TLS is refused before anything else is sent (D-41).
func Dial(addr string, tlsCfg *tls.Config, timeout time.Duration) (*Conn, error) {
	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, VDBDeny("VDB could not reach the Source: " + err.Error())
	}
	raw.SetDeadline(time.Now().Add(timeout))
	c := &Conn{raw: raw, c: raw, br: bufio.NewReader(raw)}
	g, err := c.readPacket()
	if err != nil {
		raw.Close()
		return nil, VDBDeny("VDB could not read the Source greeting: " + err.Error())
	}
	if len(g) > 0 && g[0] == 0xff {
		raw.Close()
		return nil, ParseErr(g)
	}
	caps, err := c.parseGreeting(g)
	if err != nil {
		raw.Close()
		return nil, err
	}
	if caps&capSSL == 0 || tlsCfg == nil {
		raw.Close()
		return nil, VDBDeny("VDB requires TLS to the Source, and the Source does not offer it")
	}
	req := make([]byte, 32)
	binary.LittleEndian.PutUint32(req, clientCaps())
	binary.LittleEndian.PutUint32(req[4:], 1<<24)
	req[8] = 255 // utf8mb4_0900_ai_ci
	if err := c.writePacket(req); err != nil {
		raw.Close()
		return nil, VDBDeny("Source TLS request failed: " + err.Error())
	}
	tc := tls.Client(raw, tlsCfg)
	if err := tc.Handshake(); err != nil {
		raw.Close()
		return nil, VDBDeny("Source TLS certificate refused: " + err.Error())
	}
	c.c, c.br = tc, bufio.NewReader(tc)
	return c, nil
}

// parseGreeting reads the initial handshake (protocol 10).
func (c *Conn) parseGreeting(g []byte) (uint32, error) {
	if len(g) < 40 || g[0] != 10 {
		return 0, VDBDeny("unexpected Source greeting")
	}
	end := strings.IndexByte(string(g[1:]), 0)
	if end < 0 {
		return 0, VDBDeny("malformed Source greeting")
	}
	c.Version = string(g[1 : 1+end])
	p := 1 + end + 1 + 4
	n1 := g[p : p+8]
	p += 8 + 1
	caps := uint32(binary.LittleEndian.Uint16(g[p:]))
	p += 2 + 1 + 2
	caps |= uint32(binary.LittleEndian.Uint16(g[p:])) << 16
	p += 2
	alen := int(g[p])
	p += 1 + 10
	n2len := alen - 8
	if n2len < 13 {
		n2len = 13
	}
	if p+n2len > len(g) {
		return 0, VDBDeny("malformed Source greeting")
	}
	n2 := g[p : p+n2len]
	p += n2len
	c.nonce = append(append([]byte{}, n1...), n2[:12]...)
	if caps&capPluginAuth != 0 && p < len(g) {
		c.ServerDef = strings.TrimRight(string(g[p:]), "\x00")
	}
	return caps, nil
}

// Respond sends HandshakeResponse41 with an answer computed elsewhere.
func (c *Conn) Respond(user string, answer []byte, method string) error {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint32(b, clientCaps())
	binary.LittleEndian.PutUint32(b[4:], 1<<24)
	b[8] = 255
	b = append(append(b, user...), 0)
	b = append(lenenc(b, len(answer)), answer...)
	b = append(append(b, method...), 0)
	var attrs []byte
	for _, kv := range [][2]string{{"_client_name", "vdb-mysql"}, {"program_name", "vdb-mysql"}} {
		attrs = append(lenenc(attrs, len(kv[0])), kv[0]...)
		attrs = append(lenenc(attrs, len(kv[1])), kv[1]...)
	}
	b = append(lenenc(b, len(attrs)), attrs...)
	return c.writePacket(b)
}

// Probe asks which method the account uses and the challenge it expects.
// The returned nonce is nil when the method has no 20-byte challenge.
func (c *Conn) Probe(user string) (method string, nonce []byte, err error) {
	if err := c.Respond(user, nil, probeMethod); err != nil {
		return "", nil, VDBDeny("Source connection lost: " + err.Error())
	}
	p, err := c.readPacket()
	if err != nil {
		return "", nil, VDBDeny("Source connection lost: " + err.Error())
	}
	switch {
	case len(p) > 0 && p[0] == 0xfe:
		m, n := parseSwitch(p)
		if len(n) >= 20 {
			n = n[:20]
			c.nonce = append([]byte(nil), n...)
			return m, c.nonce, nil
		}
		return m, nil, nil
	case len(p) > 0 && p[0] == 0xff:
		return "", nil, ParseErr(p)
	}
	return "", nil, VDBDeny(fmt.Sprintf("unexpected Source reply 0x%02x to the method probe", p[0]))
}

func parseSwitch(p []byte) (string, []byte) {
	i := strings.IndexByte(string(p[1:]), 0)
	if i < 0 {
		return string(p[1:]), nil
	}
	m := string(p[1 : 1+i])
	n := p[2+i:]
	if len(n) > 0 && n[len(n)-1] == 0 {
		n = n[:len(n)-1]
	}
	return m, append([]byte(nil), n...)
}

// Step is the Source's answer to an authentication packet.
type Step int

const (
	// OK: the Source accepted the login.
	OK Step = iota
	// FullAuth: caching_sha2_password wants the password (cache miss).
	FullAuth
	// Switch: the Source switched to another method with a new challenge.
	Switch
)

// Send writes one authentication packet (an answer or a password) and reads
// the Source's reply. For Switch, method and nonce describe the new method.
func (c *Conn) Send(data []byte) (Step, string, []byte, error) {
	if err := c.writePacket(data); err != nil {
		return 0, "", nil, VDBDeny("Source connection lost: " + err.Error())
	}
	return c.Result()
}

// Result reads the Source's reply to authentication data.
func (c *Conn) Result() (Step, string, []byte, error) {
	for {
		p, err := c.readPacket()
		if err != nil {
			return 0, "", nil, VDBDeny("Source connection lost: " + err.Error())
		}
		switch {
		case len(p) == 0:
			return 0, "", nil, VDBDeny("empty Source auth packet")
		case p[0] == 0x00:
			return OK, "", nil, nil
		case p[0] == 0xff:
			return 0, "", nil, ParseErr(p)
		case p[0] == 0x01 && len(p) == 2 && p[1] == 3: // fast_auth_success, OK follows
			continue
		case p[0] == 0x01 && len(p) == 2 && p[1] == 4: // perform_full_authentication
			return FullAuth, Sha2, nil, nil
		case p[0] == 0xfe:
			m, n := parseSwitch(p)
			return Switch, m, n, nil
		}
		return 0, "", nil, VDBDeny(fmt.Sprintf("unexpected Source auth packet 0x%02x", p[0]))
	}
}

// Query runs a statement on the authenticated connection (text protocol) and
// returns every row's columns as strings.
func (c *Conn) Query(q string) ([][]string, error) {
	c.raw.SetDeadline(time.Now().Add(10 * time.Second))
	c.seq = 0
	if err := c.writePacket(append([]byte{3}, q...)); err != nil {
		return nil, err
	}
	p, err := c.readPacket()
	if err != nil {
		return nil, err
	}
	if p[0] == 0xff {
		return nil, ParseErr(p)
	}
	if p[0] == 0x00 {
		return nil, nil
	}
	ncol, _ := readLenenc(p)
	for i := 0; i < int(ncol); i++ {
		if _, err := c.readPacket(); err != nil {
			return nil, err
		}
	}
	if _, err := c.readPacket(); err != nil { // EOF after column definitions
		return nil, err
	}
	var out [][]string
	for {
		p, err := c.readPacket()
		if err != nil {
			return nil, err
		}
		if p[0] == 0xfe && len(p) < 9 {
			return out, nil
		}
		if p[0] == 0xff {
			return nil, ParseErr(p)
		}
		var row []string
		for i := 0; i < int(ncol) && len(p) > 0; i++ {
			if p[0] == 0xfb {
				row = append(row, "")
				p = p[1:]
				continue
			}
			n, w := readLenenc(p)
			row = append(row, string(p[w:w+int(n)]))
			p = p[w+int(n):]
		}
		out = append(out, row)
	}
}

// Quit sends COM_QUIT and closes.
func (c *Conn) Quit() {
	c.seq = 0
	_ = c.writePacket([]byte{1})
	c.raw.Close()
}

// Close closes without COM_QUIT.
func (c *Conn) Close() { c.raw.Close() }
