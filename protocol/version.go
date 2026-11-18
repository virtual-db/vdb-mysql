// Package protocol — version.go
package protocol

// Version is the plug-in protocol version, raised on any incompatible change.
const Version = 1

// MinVersion is the oldest version vdb-mysql accepts: the current and the
// previous one (product doc 04 §1).
const MinVersion = Version - 1

// Accepts reports whether v is an accepted protocol version.
func Accepts(v uint32) bool { return v >= MinVersion && v <= Version && v > 0 }
