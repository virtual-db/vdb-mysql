// Package source — check.go
//
// The shape check (doc 10 §4.3): the send function accepts only a shaped
// value, which only the renderer functions produce, and checks its first
// keyword. This is the third enforcement of read-only, after the service
// account's privileges and transaction_read_only.
package source

import (
	"fmt"
	"strings"
)

// shaped is a statement produced by a renderer function.
type shaped struct {
	sql   string
	shape string
}

var allowed = []string{"SELECT", "EXPLAIN", "SHOW", "SET SESSION", "START TRANSACTION", "COMMIT", "ROLLBACK"}

// check confirms a statement's first keyword is allowed.
func check(s shaped) error {
	body := s.sql
	// Skip the identification comment.
	if strings.HasPrefix(body, "/*") {
		if i := strings.Index(body, "*/"); i >= 0 {
			body = body[i+2:]
		}
	}
	// Tagged key batches are parenthesised SELECTs joined with UNION ALL.
	body = strings.TrimLeft(strings.TrimSpace(body), "(")
	up := strings.ToUpper(body)
	for _, a := range allowed {
		if strings.HasPrefix(up, a) && (len(up) == len(a) || up[len(a)] == ' ' || up[len(a)] == '(') {
			return nil
		}
	}
	return fmt.Errorf("source: refused to send a statement of shape %q that is not read-only", s.shape)
}

// Shaped is a statement produced by a renderer function; other packages can
// hold one but only obtain it from this package's renderers.
type Shaped = shaped
