// Package grants — hosts.go
//
// MySQL's host-pattern matching, for D-70: which accounts can ever
// authenticate through VDB, given the host the Source sees VDB connect from.
package grants

import (
	"net"
	"strings"
)

// Reachable reports whether an account host pattern matches vdbHost (an IP
// address or host name as SELECT USER() reports it).
func Reachable(acctHost, vdbHost string) bool {
	if acctHost == "" || acctHost == "%" {
		return true
	}
	if i := strings.IndexByte(acctHost, '/'); i > 0 {
		ip := net.ParseIP(vdbHost)
		base := net.ParseIP(acctHost[:i])
		maskIP := net.ParseIP(acctHost[i+1:])
		if ip == nil || base == nil {
			return false
		}
		var mask net.IPMask
		if maskIP != nil {
			if m4 := maskIP.To4(); m4 != nil {
				mask = net.IPMask(m4)
			} else {
				mask = net.IPMask(maskIP)
			}
		} else if _, n, err := net.ParseCIDR(acctHost); err == nil {
			mask = n.Mask
		}
		if mask == nil {
			return false
		}
		b := base.To4()
		v := ip.To4()
		if b == nil || v == nil {
			b, v = base.To16(), ip.To16()
		}
		if len(b) != len(mask) || len(v) != len(mask) {
			return false
		}
		for k := range mask {
			if b[k]&mask[k] != v[k]&mask[k] {
				return false
			}
		}
		return true
	}
	if strings.EqualFold(acctHost, "localhost") || acctHost == "127.0.0.1" || acctHost == "::1" {
		return strings.EqualFold(vdbHost, acctHost)
	}
	return pattern(strings.ToLower(acctHost)).MatchString(strings.ToLower(vdbHost))
}
