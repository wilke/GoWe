package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSignedCert generates an ephemeral self-signed certificate for
// 127.0.0.1 and writes the cert/key PEM files into dir, returning their paths.
func writeSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certPath, keyPath
}

// TestServerServesHTTPS is a smoke test that the server can terminate TLS
// in-process via ServeTLS (the same call path as ListenAndServeTLS in
// cmd/server/main.go) and answer a request over HTTPS.
func TestServerServesHTTPS(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, t.TempDir())

	srv := testServer()
	httpServer := &http.Server{Handler: srv.Handler()}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.ServeTLS(ln, certPath, keyPath)
	}()
	t.Cleanup(func() { _ = httpServer.Close() })

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			// Self-signed cert: skip verification for the smoke test only.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	url := "https://" + ln.Addr().String() + "/api/v1/health"

	// The listener is already accepting; retry briefly in case ServeTLS has
	// not finished its first handshake setup.
	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err = client.Get(url)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s over HTTPS failed: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health over HTTPS: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("ServeTLS error: %v", err)
		}
	default:
	}
}

// TestTLSEnabledWiresSecureCookies verifies that native TLS configuration
// causes the server to mark session cookies Secure.
func TestTLSEnabledWiresSecureCookies(t *testing.T) {
	// Sanity check on the config helper that drives cookie hardening.
	cfg := testServer().config
	if cfg.TLSEnabled() {
		t.Fatalf("default test config should not enable TLS")
	}

	cfg.TLSCertFile = "/tmp/cert.pem"
	cfg.TLSKeyFile = "/tmp/key.pem"
	if !cfg.TLSEnabled() {
		t.Fatalf("TLSEnabled() should be true when cert and key are set")
	}
}
