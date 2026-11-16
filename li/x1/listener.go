// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"time"
)

// X1 is a request/response control interface carrying a handful of small messages,
// so every phase of a connection can be given a short deadline. Without them
// net/http applies none at all, and a peer that opens a connection and then stalls
// — during the TLS handshake, before authentication has happened or can happen —
// holds it open indefinitely. Enough such connections and the element can no longer
// be tasked or, more to the point, untasked.
const (
	// x1ReadHeaderTimeout also bounds the TLS handshake, which is what makes it the
	// one that matters here: the stall an unauthenticated peer can cause happens
	// before any request exists.
	x1ReadHeaderTimeout = 10 * time.Second
	x1ReadTimeout       = 30 * time.Second
	x1WriteTimeout      = 30 * time.Second
	x1IdleTimeout       = 60 * time.Second
)

// NewListener returns the HTTP server an X1 endpoint should be served with:
// mutually authenticated, bounded in time, and silent. Every network function serving X1
// takes its server from here, so none can drift from the others on a timeout or a
// disclosure.
//
// **Mutual authentication is enforced here, not trusted to the caller.** The
// configuration is cloned and its ClientAuth set to RequireAndVerifyClientCert.
// Server.ServeHTTP binds the peer by PeerCertificates[0], which is populated under
// RequestClientCert or RequireAnyClientCert too -- but unverified, so ServeHTTP treats it as
// no certificate, and a weaker setting would leave every message refused. A configuration
// with no ClientCAs is given an empty pool rather than left nil: nil verifies a client
// against the host's system roots, which would admit any publicly issued certificate, so
// without the LI CA no client verifies at all. The caller remains responsible for a GetConfigForClient
// hook, whose returned configuration is used as returned.
//
// A nil configuration is left nil and the listener serves plain HTTP. The Server then
// refuses every message: authentication requires a client certificate, so each is
// answered 1030. That mode exists so an element can be brought up before its LI PKI is;
// it cannot be tasked until it has one.
func NewListener(handler http.Handler, tlsConfig *tls.Config) *http.Server {
	if tlsConfig != nil {
		tlsConfig = tlsConfig.Clone()
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		if tlsConfig.ClientCAs == nil {
			tlsConfig.ClientCAs = x509.NewCertPool()
		}
	}

	return &http.Server{
		Handler:   handler,
		TLSConfig: tlsConfig,

		ReadHeaderTimeout: x1ReadHeaderTimeout,
		ReadTimeout:       x1ReadTimeout,
		WriteTimeout:      x1WriteTimeout,
		IdleTimeout:       x1IdleTimeout,

		// net/http sends server errors to the default logger, so a failed handshake
		// would print the peer's address to this process's stderr and hence to the
		// general operator log — publishing the LI domain's address and marking the
		// element as running a mutually authenticated listener that nothing else in
		// its configuration explains. Faults on this plane go to the ADMF over X1,
		// which is the only channel entitled to know.
		ErrorLog: log.New(io.Discard, "", 0),
	}
}
