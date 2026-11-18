// Package sourcetls builds the TLS configuration for every connection to the
// Source, with nothing to set up (implementation plan doc 10 §8, D-41, D-75):
// the system trust store plus the managed clouds' CA bundles, chain and host
// name verification as VERIFY_IDENTITY does, and SHA-256 fingerprint pinning
// for a self-signed Source.
package sourcetls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Name is the name the TLS configuration is registered under for the driver.
const Name = "vdb"

// Fingerprint returns the lower-case hex SHA-256 of a certificate's DER bytes.
func Fingerprint(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// ErrUntrusted is returned when the Source's certificate is neither trusted
// nor pinned. Its message carries the fingerprint and how to pin it.
type ErrUntrusted struct {
	Host        string
	Fingerprint string
	Cause       error
}

func (e *ErrUntrusted) Error() string {
	return fmt.Sprintf("sourcetls: the Source certificate for %s is not trusted (%v); if this Source is yours and self-signed, set VDB_SOURCE_TLS_FINGERPRINT=%s",
		e.Host, e.Cause, e.Fingerprint)
}

// Pool returns the system trust store plus every PEM file in caDir.
func Pool(caDir string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	entries, err := os.ReadDir(caDir)
	if err != nil {
		if os.IsNotExist(err) {
			return pool, nil
		}
		return nil, fmt.Errorf("sourcetls: %s: %w", caDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(caDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("sourcetls: %w", err)
		}
		if !pool.AppendCertsFromPEM(b) {
			slog.Warn("no certificates in CA bundle", "pkg", "sourcetls", "file", e.Name())
		}
	}
	return pool, nil
}

// Config returns the TLS configuration for host. The standard chain and host
// name checks run in VerifyConnection; when they fail, a leaf whose
// fingerprint equals pin is accepted.
func Config(host string, pool *x509.CertPool, pin string) *tls.Config {
	pin = strings.ToLower(strings.ReplaceAll(pin, ":", ""))
	return &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // verification is done in VerifyConnection, which also allows pinning
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("sourcetls: the Source sent no certificate")
			}
			leaf := cs.PeerCertificates[0]
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool, Intermediates: inter})
			if err == nil {
				return nil
			}
			fp := Fingerprint(leaf)
			if pin != "" && fp == pin {
				return nil
			}
			return &ErrUntrusted{Host: host, Fingerprint: fp, Cause: err}
		},
	}
}

// Register builds the configuration and registers it for go-sql-driver/mysql
// under Name. The same *tls.Config is returned for the login relay.
func Register(host, caDir, pin string) (*tls.Config, error) {
	pool, err := Pool(caDir)
	if err != nil {
		return nil, err
	}
	cfg := Config(host, pool, pin)
	if err := mysql.RegisterTLSConfig(Name, cfg); err != nil {
		return nil, fmt.Errorf("sourcetls: register: %w", err)
	}
	return cfg, nil
}
