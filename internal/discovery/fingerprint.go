// Package discovery — fingerprint.go
package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

var autoIncClause = regexp.MustCompile(`(?i)\s*AUTO_INCREMENT=\d+`)

// Fingerprint hashes a SHOW CREATE TABLE text, ignoring the AUTO_INCREMENT
// counter, which changes with every Source insert.
func Fingerprint(create string) string {
	h := sha256.Sum256([]byte(autoIncClause.ReplaceAllString(create, "")))
	return hex.EncodeToString(h[:16])
}
