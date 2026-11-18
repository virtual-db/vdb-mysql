// Package handshake — scramble.go: computing an answer from a password, only
// for a method mismatch (doc 11 §2.3).
package handshake

import (
	"crypto/sha1"
	"crypto/sha256"
)

// ScrambleSha2 is caching_sha2_password's fast-auth answer.
func ScrambleSha2(pw, nonce []byte) []byte {
	if len(pw) == 0 {
		return nil
	}
	h1 := sha256.Sum256(pw)
	h2 := sha256.Sum256(h1[:])
	h := sha256.New()
	h.Write(h2[:])
	h.Write(nonce)
	h3 := h.Sum(nil)
	for i := range h3 {
		h3[i] ^= h1[i]
	}
	return h3
}

// ScrambleNative is mysql_native_password's answer.
func ScrambleNative(pw, nonce []byte) []byte {
	if len(pw) == 0 {
		return nil
	}
	h1 := sha1.Sum(pw)
	h2 := sha1.Sum(h1[:])
	h := sha1.New()
	h.Write(nonce)
	h.Write(h2[:])
	h3 := h.Sum(nil)
	for i := range h3 {
		h3[i] ^= h1[i]
	}
	return h3
}

// Wipe zeroes a buffer.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
