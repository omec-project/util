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
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// selfSignedLeaf returns a self-signed certificate that is not a CA, as cert and key PEM, valid
// from notBefore to notAfter.
func selfSignedLeaf(t *testing.T, cn string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// TestALeafInTheCABundleDoesNotAuthenticateItsHolder: a certificate in the trust pool is
// trusted as it stands, and Go accepts a peer presenting it as a chain of one. A ca.crt that
// also carried a leaf therefore let that leaf's holder through mutual TLS on every LI
// interface, with no CA having issued it for that. Only CA certificates may be anchors.
func TestALeafInTheCABundleDoesNotAuthenticateItsHolder(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	neCert, neKey := newLeaf(t, "ne", ca, caKey)
	strayCert, strayKey := selfSignedLeaf(t, "stray", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	bundle := append(append([]byte{}, caPEM...), strayCert...)
	m := writeMaterial(t, dir, neCert, neKey, bundle)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", m.ServerTLS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// The server's own verdict, not the client's: under TLS 1.3 a client learns of the
	// refusal only on a later read, and a closed connection reads EOF either way.
	verdict := make(chan error, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			verdict <- acceptErr

			return
		}
		verdict <- c.(*tls.Conn).HandshakeContext(t.Context())
		_ = c.Close()
	}()

	stray, err := tls.X509KeyPair(strayCert, strayKey)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &tls.Dialer{Config: &tls.Config{
		Certificates: []tls.Certificate{stray},
		RootCAs:      m.caPool,
		MinVersion:   tls.VersionTLS12,
	}}
	if conn, dialErr := dialer.DialContext(t.Context(), "tcp", ln.Addr().String()); dialErr == nil {
		_ = conn.Close()
	}

	if err := <-verdict; err == nil {
		t.Error("the server completed mutual TLS with a peer presenting a leaf that sat in the " +
			"CA bundle: the leaf was trusted as an anchor")
	}
}

// TestABundleWithNoCAIsRefused: with every non-CA entry skipped, a bundle holding only leaves
// has no anchor at all, and Load says so rather than building a pool that trusts nothing.
func TestABundleWithNoCAIsRefused(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := newCA(t, "LI Test CA")
	neCert, neKey := newLeaf(t, "ne", ca, caKey)
	onlyLeaf, _ := selfSignedLeaf(t, "stray", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	_, err := Load(writeTriple(t, dir, neCert, neKey, onlyLeaf))
	if err == nil || !strings.Contains(err.Error(), "no usable CA") {
		t.Errorf("Load of a bundle holding only a leaf = %v, want a refusal naming the missing CA", err)
	}
	// And why: a root issued without the extension is skipped by the same rule, so the error
	// must name the rule rather than leave the operator to suspect the wrong file.
	if err != nil && !strings.Contains(err.Error(), "1 without basicConstraints CA:TRUE") {
		t.Errorf("the refusal does not say why the certificate was skipped: %v", err)
	}
}

// TestAnExpiredCertificateIsRefusedAtLoad: an expired NE certificate fails every handshake, and
// the failure surfaces only at the peers. Load refuses it where the operator is looking.
func TestAnExpiredCertificateIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()
	_, _, caPEM := newCA(t, "LI Test CA")
	expired, key := selfSignedLeaf(t, "ne", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))

	if _, err := Load(writeTriple(t, dir, expired, key, caPEM)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("Load of an expired certificate = %v, want a refusal saying it expired", err)
	}
}

// TestANotYetValidCertificateIsRefusedAtLoad: a certificate whose validity has not begun
// fails every handshake just as an expired one does — a clock behind the issuer's, or a
// certificate rolled out early.
func TestANotYetValidCertificateIsRefusedAtLoad(t *testing.T) {
	_, _, caPEM := newCA(t, "LI Test CA")
	early, key := selfSignedLeaf(t, "ne", time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))

	if _, err := Load(writeTriple(t, t.TempDir(), early, key, caPEM)); err == nil || !strings.Contains(err.Error(), "not valid until") {
		t.Errorf("Load of a not-yet-valid certificate = %v, want a refusal saying when it becomes valid", err)
	}
}

// TestAnAnchorOutsideItsValidityIsNotTrusted: an expired CA verifies no chain, so a bundle
// holding only one is refused with that reason, and one holding a current CA beside it
// loads.
func TestAnAnchorOutsideItsValidityIsNotTrusted(t *testing.T) {
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	neCert, neKey := newLeaf(t, "ne", ca, caKey)
	_, _, expiredPEM := newCAValid(t, "Old LI CA", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))

	_, err := Load(writeTriple(t, t.TempDir(), neCert, neKey, expiredPEM))
	if err == nil || !strings.Contains(err.Error(), "1 outside their validity period") {
		t.Errorf("Load with only an expired CA = %v, want a refusal saying the CA is out of date", err)
	}

	if _, err := Load(writeTriple(t, t.TempDir(), neCert, neKey, append(expiredPEM, caPEM...))); err != nil {
		t.Errorf("Load with an expired CA beside a current one = %v, want it to load", err)
	}
}

// TestADamagedCABundleIsRefused: a CERTIFICATE block that does not parse may be the anchor
// the peers use, so it is an error rather than an entry silently skipped; other PEM blocks
// are counted in the refusal, so a key pasted into ca.crt is visible as one.
func TestADamagedCABundleIsRefused(t *testing.T) {
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	neCert, neKey := newLeaf(t, "ne", ca, caKey)
	garbage := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})

	if _, err := Load(writeTriple(t, t.TempDir(), neCert, neKey, append(garbage, caPEM...))); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("Load of a bundle with a damaged certificate = %v, want a refusal", err)
	}

	_, err := Load(writeTriple(t, t.TempDir(), neCert, neKey, neKey))
	if err == nil || !strings.Contains(err.Error(), "1 PEM block(s) that are not certificates") {
		t.Errorf("Load of a CA file holding a key = %v, want a refusal counting the key block", err)
	}
}
