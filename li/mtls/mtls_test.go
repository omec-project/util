// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newCA returns a self-signed CA certificate, its key, and its PEM encoding.
func newCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()

	return newCAValid(t, cn, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

// newCAValid is newCA valid from notBefore to notAfter.
func newCAValid(t *testing.T, cn string, notBefore, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newLeaf returns a cert+key PEM pair for cn, signed by (caCert, caKey).
func newLeaf(t *testing.T, cn string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return
}

// writeTriple writes a cert/key/ca triple to dir and returns the three paths.
func writeTriple(t *testing.T, dir string, cert, key, ca []byte) (certPath, keyPath, caPath string) {
	t.Helper()
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	caPath = filepath.Join(dir, "ca.crt")
	for p, b := range map[string][]byte{certPath: cert, keyPath: key, caPath: ca} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return certPath, keyPath, caPath
}

// writeMaterial writes a cert/key/ca triple to dir and Loads it.
func writeMaterial(t *testing.T, dir string, cert, key, ca []byte) *Material {
	t.Helper()
	m, err := Load(writeTriple(t, dir, cert, key, ca))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

func TestMutualTLS(t *testing.T) {
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	rogueCA, rogueKey, _ := newCA(t, "Rogue CA")

	serverCert, serverKey := newLeaf(t, "network-element", ca, caKey)
	clientCert, clientKey := newLeaf(t, "admf", ca, caKey)
	rogueCert, rogueKeyPEM := newLeaf(t, "rogue-admf", rogueCA, rogueKey)

	serverMat := writeMaterial(t, t.TempDir(), serverCert, serverKey, caPEM)
	clientMat := writeMaterial(t, t.TempDir(), clientCert, clientKey, caPEM)

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // test
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = serverMat.ServerTLS()
	ts.StartTLS()
	defer ts.Close()

	get := func(cfg *tls.Config) (int, error) {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL, nil)
		if err != nil {
			return 0, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}

	// Valid: client presents an LI-CA-signed cert and trusts the server CA.
	if code, err := get(clientMat.ClientTLS()); err != nil || code != http.StatusOK {
		t.Errorf("valid mutual TLS: code=%d err=%v, want 200/nil", code, err)
	}

	// No client certificate: the server must reject the handshake.
	trustOnly := &tls.Config{RootCAs: clientMat.caPool}
	if _, err := get(trustOnly); err == nil {
		t.Error("server accepted a client with no certificate")
	}

	// Wrong CA: a rogue client cert (not issued by the LI CA) must be rejected.
	rogueTLSCert, err := tls.X509KeyPair(rogueCert, rogueKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	rogueCfg := &tls.Config{Certificates: []tls.Certificate{rogueTLSCert}, RootCAs: clientMat.caPool}
	if _, err := get(rogueCfg); err == nil {
		t.Error("server accepted a client cert signed by an untrusted CA")
	}
}

// TestClientVerifiesMDF checks the X2/X3 delivery side: the client must verify
// the MDF and refuse to deliver intercept product to a server whose certificate
// is not signed by the LI CA — and it must never disable verification.
func TestClientVerifiesMDF(t *testing.T) {
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	clientCert, clientKey := newLeaf(t, "poi", ca, caKey)
	clientMat := writeMaterial(t, t.TempDir(), clientCert, clientKey, caPEM)

	if clientMat.ClientTLS().InsecureSkipVerify {
		t.Fatal("ClientTLS must never set InsecureSkipVerify")
	}

	// A rogue MDF whose server cert is signed by an untrusted CA. It requires no
	// client cert, so the only property under test is the client verifying the
	// server.
	rogueCA, rogueKey, _ := newCA(t, "Rogue CA")
	rogueServerCert, rogueServerKey := newLeaf(t, "rogue-mdf", rogueCA, rogueKey)
	rogueTLSCert, err := tls.X509KeyPair(rogueServerCert, rogueServerKey)
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{rogueTLSCert}}
	ts.StartTLS()
	defer ts.Close()

	c := &http.Client{Transport: &http.Transport{TLSClientConfig: clientMat.ClientTLS()}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Error("X2/X3 client accepted an MDF whose cert is not signed by the LI CA")
	}
}

func TestLoadRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	cert, key := newLeaf(t, "ne", ca, caKey)

	// Missing files.
	if _, err := Load("/nope/tls.crt", "/nope/tls.key", "/nope/ca.crt"); err == nil {
		t.Error("Load accepted missing files")
	}
	// Non-certificate CA file.
	if _, err := Load(writeTriple(t, t.TempDir(), cert, key, []byte("not a certificate"))); err == nil {
		t.Error("Load accepted a CA file with no certificate")
	}
	// A key that is not the certificate's: the one check on this element's own certificate
	// besides its validity, which Load's documentation states and LoadX509KeyPair performs.
	_, otherKey := newLeaf(t, "other", ca, caKey)
	if _, err := Load(writeTriple(t, t.TempDir(), cert, otherKey, caPEM)); err == nil ||
		!strings.Contains(err.Error(), "private key does not match public key") {
		t.Errorf("Load with a key that is not the certificate's: err = %v, want a mismatch refusal", err)
	}
	// Sanity: a good triple loads.
	if writeMaterial(t, dir, cert, key, caPEM) == nil {
		t.Error("Load rejected valid material")
	}
}
