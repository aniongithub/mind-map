// Package tls generates a local CA and server certificate so that
// mind-map can serve HTTPS on mind-map.local without browser warnings.
//
// Certificates are stored in ~/.mind-map/tls/:
//
//	ca.crt, ca.key   — local certificate authority
//	server.crt, server.key — server cert signed by the CA
//
// The CA cert must be installed in the system trust store (once) so
// browsers accept the server cert. The Setup function handles this.
package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// DefaultDir returns the default TLS directory (~/.mind-map/tls).
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".mind-map", "tls")
	}
	return filepath.Join(home, ".mind-map", "tls")
}

// DirFromWikiDir derives the TLS directory from the wiki directory.
// The wiki dir is typically ~/.mind-map/wiki, so TLS is the sibling
// ~/.mind-map/tls. This is more reliable than DefaultDir() when
// running as a system service (where os.UserHomeDir() may differ).
func DirFromWikiDir(wikiDir string) string {
	return filepath.Join(filepath.Dir(wikiDir), "tls")
}

// CertPaths returns the paths to the server cert and key.
func CertPaths(dir string) (certFile, keyFile string) {
	return filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
}

// CACertPath returns the path to the CA certificate.
func CACertPath(dir string) string {
	return filepath.Join(dir, "ca.crt")
}

// HasCerts returns true if the server cert and key exist.
func HasCerts(dir string) bool {
	cert, key := CertPaths(dir)
	if _, err := os.Stat(cert); err != nil {
		return false
	}
	if _, err := os.Stat(key); err != nil {
		return false
	}
	return true
}

// Generate creates a local CA and a server certificate for mind-map.local.
// If certs already exist, they are overwritten.
func Generate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}

	// --- CA ---
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}

	caSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"mind-map"},
			CommonName:   "mind-map Local CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create CA cert: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}

	if err := writePEM(filepath.Join(dir, "ca.crt"), "CERTIFICATE", caCertDER); err != nil {
		return err
	}
	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return fmt.Errorf("marshal CA key: %w", err)
	}
	if err := writePEM(filepath.Join(dir, "ca.key"), "EC PRIVATE KEY", caKeyDER); err != nil {
		return err
	}

	// --- Server cert ---
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate server key: %w", err)
	}

	srvSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	srvTemplate := &x509.Certificate{
		SerialNumber: srvSerial,
		Subject: pkix.Name{
			Organization: []string{"mind-map"},
			CommonName:   "mind-map.local",
		},
		DNSNames:    []string{"mind-map.local", "localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(2 * 365 * 24 * time.Hour), // 2 years
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	srvCertDER, err := x509.CreateCertificate(rand.Reader, srvTemplate, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create server cert: %w", err)
	}

	if err := writePEM(filepath.Join(dir, "server.crt"), "CERTIFICATE", srvCertDER); err != nil {
		return err
	}
	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return fmt.Errorf("marshal server key: %w", err)
	}
	if err := writePEM(filepath.Join(dir, "server.key"), "EC PRIVATE KEY", srvKeyDER); err != nil {
		return err
	}

	return nil
}

// Remove deletes all generated TLS files.
func Remove(dir string) error {
	for _, name := range []string{"ca.crt", "ca.key", "server.crt", "server.key"} {
		os.Remove(filepath.Join(dir, name))
	}
	// Remove dir if empty
	os.Remove(dir)
	return nil
}

func writePEM(path, blockType string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: data})
}
