// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

// Package mtls loads the Lawful Interception PKI credentials and builds the
// mutual-TLS configurations for the X1/X2/X3 interfaces.
//
// LI requires mutual TLS with certificate verification on every interface
// (ETSI TS 103 221-1), using credentials from the operator's LI PKI — kept
// separate from the general SBI certificates for isolation. The credentials
// (this network element's LI certificate + key, and the LI CA trust anchor)
// are pre-provisioned out of band before any X1 tasking, corresponding to the
// ETSI TS 104 000 (X0) step; here that means the deployment places them at
// configured file paths (e.g. a dedicated, access-restricted Kubernetes
// Secret). Unlike SD-Core's SBI TLS, verification is never skipped.
//
// There is no reload. Load reads the files once and the configurations it returns
// hold what it read, so rotating the certificate or the trust anchor takes effect only
// when the network function restarts. A certificate that expires in service makes every
// X1, X2 and X3 handshake fail, and the failure is visible only at the peer.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// Material holds the loaded LI PKI credentials for one network element.
type Material struct {
	cert   tls.Certificate
	caPool *x509.CertPool
}

// Load reads this network element's LI certificate and key, and the LI CA
// trust anchor, from the given file paths (X0-pre-provisioned). All three are
// required, and the configurations built from them never skip verification. Running X1
// with no TLS material at all is possible, but then the X1 server refuses every message
// (see x1.NewListener); there is no mode that authenticates without verifying.
//
// A certificate outside its validity period — expired, or not valid yet — is refused
// here, where the operator is looking, rather than discovered handshake by handshake
// at every peer.
//
// Two properties of this element's own certificate are checked: its validity period, here,
// and that the private key matches the certificate's public key, which tls.LoadX509KeyPair
// refuses otherwise. Load does not verify that the certificate chains to the trust anchor it
// loads, nor that its
// extended key usage admits the roles it is put to (serverAuth where it serves X1,
// clientAuth where it connects as a client); the anchor is used only to verify peers. A
// certificate wrong in either way loads, and fails at the peer's handshake.
//
// Nor does it warn ahead of expiry. TS 103 221-1 clause 5.3 says warnings "should include
// that keys/certificates are about to expire"; Load reads the certificate once at start,
// and nothing here reports over X1, so an approaching expiry is the deployment's to
// monitor.
//
// The key file's permissions are not checked. A Kubernetes Secret volume mounts its
// files 0644 unless the deployment sets defaultMode, so a check would refuse an
// ordinary deployment; access to the key is the volume's to restrict.
func Load(certPath, keyPath, caCertPath string) (*Material, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: load LI certificate/key: %w", err)
	}
	// LoadX509KeyPair fills Leaf unless GODEBUG x509keypairleaf=0 turns that off.
	leaf := cert.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fmt.Errorf("mtls: parse LI certificate: %w", err)
		}
	}
	if invalid := withinValidity(leaf, time.Now()); invalid != nil {
		return nil, fmt.Errorf("mtls: LI certificate %q %w", leaf.Subject.CommonName, invalid)
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: read LI CA certificate: %w", err)
	}
	pool, err := caPool(caPEM, time.Now())
	if err != nil {
		return nil, fmt.Errorf("mtls: LI CA file %q: %w", caCertPath, err)
	}
	return &Material{cert: cert, caPool: pool}, nil
}

// withinValidity reports, as an error completing "certificate X …", why c is not valid
// at now, or nil when it is.
func withinValidity(c *x509.Certificate, now time.Time) error {
	switch {
	case now.After(c.NotAfter):
		return fmt.Errorf("expired at %s", c.NotAfter.UTC().Format(time.RFC3339))
	case now.Before(c.NotBefore):
		return fmt.Errorf("is not valid until %s", c.NotBefore.UTC().Format(time.RFC3339))
	}

	return nil
}

// caPool builds the trust anchors from a PEM bundle, admitting only CA certificates that
// are within their validity period.
//
// x509.CertPool.AppendCertsFromPEM admits any certificate, and a certificate in the
// pool is trusted as it stands: verification accepts a peer presenting it as a
// complete chain of one. A bundle carrying a leaf — a misassembled ca.crt, say —
// would make that leaf's holder trusted on every LI interface without any CA having
// issued it for that, so only certificates marked CA by basicConstraints are anchors.
// An anchor outside its validity period verifies no chain, so it is left out too, and
// a bundle that is left with none is refused with the count of what was skipped and
// why. A CERTIFICATE block that does not parse is an error: the bundle is damaged, and
// the anchor it should have carried may be the one the peers use.
func caPool(bundle []byte, now time.Time) (*x509.CertPool, error) {
	pool := x509.NewCertPool()

	var anchors, notCA, outOfDate, otherBlocks int

	for rest := bundle; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			otherBlocks++
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate %d does not parse: %w", anchors+notCA+outOfDate+1, err)
		}
		// IsCA is only set when basicConstraints is present, so this is the
		// basicConstraints CA:TRUE test.
		if !c.IsCA {
			notCA++
			continue
		}
		if withinValidity(c, now) != nil {
			outOfDate++
			continue
		}
		pool.AddCert(c)
		anchors++
	}

	if anchors == 0 {
		return nil, fmt.Errorf("contains no usable CA certificate: %d without basicConstraints "+
			"CA:TRUE, %d outside their validity period, %d PEM block(s) that are not "+
			"certificates", notCA, outOfDate, otherBlocks)
	}
	return pool, nil
}

// ServerTLS returns the TLS config for the X1 listener: it presents this NE's
// LI certificate and requires and verifies a client (ADMF) certificate issued
// by the LI CA. A peer without a valid LI certificate is rejected at handshake.
//
// TLS 1.2 is the floor; the cipher suites are Go's defaults, which exclude the
// suites Go considers insecure, so none are configured here.
func (m *Material) ServerTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.cert},
		ClientCAs:    m.caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}

// ClientTLS returns the TLS config for delivering X2 (xIRI) and X3 (xCC) to an
// MDF: it presents this NE's LI certificate and verifies the MDF's server
// certificate against the LI CA (verification is never skipped).
func (m *Material) ClientTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.cert},
		RootCAs:      m.caPool,
		MinVersion:   tls.VersionTLS12,
	}
}
