// Package handshake_test — tests/scramble_test.go
//
// The scramble functions against an independent computation of MySQL's
// published formulas (doc 11 §4).
package handshake_test

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"testing"

	. "github.com/virtual-db/vdb-mysql/internal/handshake"
)

func TestScrambles(t *testing.T) {
	pw, nonce := []byte("secret"), []byte("01234567890123456789")
	// mysql_native_password: SHA1(pw) XOR SHA1(nonce + SHA1(SHA1(pw)))
	h1 := sha1.Sum(pw)
	h2 := sha1.Sum(h1[:])
	h3 := sha1.Sum(append(append([]byte{}, nonce...), h2[:]...))
	for i := range h3 {
		h3[i] ^= h1[i]
	}
	if !bytes.Equal(ScrambleNative(pw, nonce), h3[:]) {
		t.Fatal("native scramble")
	}
	// caching_sha2_password: SHA256(pw) XOR SHA256(SHA256(SHA256(pw)) + nonce)
	s1 := sha256.Sum256(pw)
	s2 := sha256.Sum256(s1[:])
	s3 := sha256.Sum256(append(append([]byte{}, s2[:]...), nonce...))
	for i := range s3 {
		s3[i] ^= s1[i]
	}
	if !bytes.Equal(ScrambleSha2(pw, nonce), s3[:]) {
		t.Fatal("sha2 scramble")
	}
	if ScrambleNative(nil, nonce) != nil {
		t.Fatal("an empty password has an empty answer")
	}
}
