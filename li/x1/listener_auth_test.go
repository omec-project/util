// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
)

// TestTheListenerRequiresAVerifiedClientCertificate: the X1 identity binding reads the peer's
// first certificate, and under RequestClientCert or RequireAnyClientCert that certificate is
// presented but never verified, so binding to it would bind to whatever the peer claimed.
// NewListener promises a mutually authenticated listener, so it must hold to that whatever the
// caller configured — without changing the caller's own configuration.
func TestTheListenerRequiresAVerifiedClientCertificate(t *testing.T) {
	for _, weaker := range []tls.ClientAuthType{
		tls.NoClientCert, tls.RequestClientCert, tls.RequireAnyClientCert, tls.VerifyClientCertIfGiven,
	} {
		given := &tls.Config{ClientAuth: weaker, MinVersion: tls.VersionTLS12}

		srv := NewListener(http.NotFoundHandler(), given)

		if got := srv.TLSConfig.ClientAuth; got != tls.RequireAndVerifyClientCert {
			t.Errorf("a listener configured with %v serves with %v, want RequireAndVerifyClientCert",
				weaker, got)
		}
		if given.ClientAuth != weaker {
			t.Errorf("NewListener changed the caller's configuration from %v to %v; it must clone it",
				weaker, given.ClientAuth)
		}
	}

	if srv := NewListener(http.NotFoundHandler(), nil); srv.TLSConfig != nil {
		t.Error("the certificate-less mode was given a TLS configuration")
	}
}

// TestAListenerWithNoClientCAsTrustsNoClient: a nil ClientCAs verifies a client against the
// host's system roots, so any publicly issued certificate would pass. Without the LI CA the
// listener must verify no client at all.
func TestAListenerWithNoClientCAsTrustsNoClient(t *testing.T) {
	given := &tls.Config{MinVersion: tls.VersionTLS12}

	srv := NewListener(http.NotFoundHandler(), given)

	if srv.TLSConfig.ClientCAs == nil {
		t.Fatal("a listener with no ClientCAs verifies clients against the system roots")
	}
	if !srv.TLSConfig.ClientCAs.Equal(x509.NewCertPool()) {
		t.Error("a listener with no ClientCAs was given anchors of its own; want an empty pool")
	}
	if given.ClientCAs != nil {
		t.Error("NewListener changed the caller's configuration; it must clone it")
	}
}
