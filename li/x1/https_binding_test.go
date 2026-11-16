// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// countingTLSServer serves h over TLS with cert and counts the requests that reached it — a
// request the handler sees is a request whose body was sent.
func countingTLSServer(t *testing.T, cert tls.Certificate, h http.Handler) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var n atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h.ServeHTTP(w, r)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, &n
}

// trustRoots points a client built by newX1Client at roots in place of the system's, as a
// public CA would be trusted, without replacing the client — so what is tested is the
// connection verification the constructor installed.
func trustRoots(t *testing.T, c *http.Client, roots *x509.CertPool) {
	t.Helper()

	rt := c.Transport
	if h, ok := rt.(httpsOnly); ok {
		rt = h.next
	}
	tr, ok := rt.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatalf("the client has no TLS configuration to trust roots in: %T", c.Transport)
	}
	tr.TLSClientConfig.RootCAs = roots
}

// TestAPeerThatDoesNotBindIsSentNothing: an element holds a peer to the clause 8.2.4 binding
// before it sends the request, not only when the answer comes back. Refusing only the answer
// would still hand a trigger's SEID and correlation, or a fault report, to whoever answered
// the dial with a certificate the roots trust — a public CA's, for a Requester or Reporter
// built with no TLS configuration and an https URL. Here the peer's certificate is trusted
// and binds nobody: the request must not reach it at all, and the refusal still reads as an
// answer that could not be attributed.
func TestAPeerThatDoesNotBindIsSentNothing(t *testing.T) {
	unbound, roots := liCert(t, "", "")

	t.Run("requester, no TLS configuration", func(t *testing.T) {
		srv, hits := countingTLSServer(t, unbound, acknowledging(t))
		req := NewRequester(srv.URL, "smf-1", "upf-1", nil)
		trustRoots(t, req.client, roots)

		err := req.ActivateTask(testTrigger())
		assertRefusedUnsent(t, err, hits)
	})

	t.Run("requester, a TLS configuration of the caller's", func(t *testing.T) {
		srv, hits := countingTLSServer(t, unbound, acknowledging(t))
		req := NewRequester(srv.URL, "smf-1", "upf-1", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})

		err := req.ActivateTask(testTrigger())
		assertRefusedUnsent(t, err, hits)
	})

	t.Run("reporter, no TLS configuration", func(t *testing.T) {
		srv, hits := countingTLSServer(t, unbound, answeringADMFHandler(t))
		rep := NewReporter(srv.URL, "admf-1", "neID", nil)
		trustRoots(t, rep.client, roots)

		err := rep.ReportTaskIssue(taskID, TaskReportWarning, "details")
		assertRefusedUnsent(t, err, hits)
	})

	t.Run("reporter, a TLS configuration of the caller's", func(t *testing.T) {
		srv, hits := countingTLSServer(t, unbound, answeringADMFHandler(t))
		rep := NewReporter(srv.URL, "admf-1", "neID", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})

		err := rep.ReportTaskIssue(taskID, TaskReportWarning, "details")
		assertRefusedUnsent(t, err, hits)
	})
}

func assertRefusedUnsent(t *testing.T, err error, hits *atomic.Int64) {
	t.Helper()

	if n := hits.Load(); n != 0 {
		t.Errorf("%d request(s) reached a peer whose certificate binds nobody: the binding was "+
			"checked after the request had been sent", n)
	}
	var unattributed *ResponseError
	if !errors.As(err, &unattributed) || unattributed.Field != "peer certificate" {
		t.Errorf("error = %v, want the peer certificate refused", err)
	}
	var unsent *NotSentError
	if !errors.As(err, &unsent) {
		t.Errorf("error = %v, want it marked as never sent", err)
	}
}

func answeringADMFHandler(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)                 //nolint:errcheck // test handler
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	})
}

// TestAPeerThatBindsIsStillReachedWithoutTLSConfiguration: the handshake check refuses only a
// peer that does not bind. One whose certificate binds the identifier addressed is reached and
// believed, with or without a configuration of the caller's.
func TestAPeerThatBindsIsStillReachedWithoutTLSConfiguration(t *testing.T) {
	bound, roots := liCert(t, "", certBindingURNPrefix+roleNE+":upf-1")
	srv, hits := countingTLSServer(t, bound, acknowledging(t))

	req := NewRequester(srv.URL, "smf-1", "upf-1", nil)
	trustRoots(t, req.client, roots)
	if err := req.ActivateTask(testTrigger()); err != nil {
		t.Fatalf("ActivateTask to a peer that binds = %v, want success", err)
	}
	if hits.Load() != 1 {
		t.Errorf("the peer received %d requests, want 1", hits.Load())
	}
}

// TestACallersOwnConnectionVerificationStillRuns: the binding is added to a configuration's
// connection verification, not substituted for it. A caller's own check runs, and its refusal
// stands.
func TestACallersOwnConnectionVerificationStillRuns(t *testing.T) {
	bound, roots := liCert(t, "", certBindingURNPrefix+roleNE+":upf-1")
	srv, hits := countingTLSServer(t, bound, acknowledging(t))

	callerRefuses := errors.New("refused by the caller's own verification")
	var ran atomic.Bool
	cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, VerifyConnection: func(tls.ConnectionState) error {
		ran.Store(true)

		return callerRefuses
	}}

	req := NewRequester(srv.URL, "smf-1", "upf-1", cfg)
	if err := req.ActivateTask(testTrigger()); !errors.Is(err, callerRefuses) {
		t.Errorf("ActivateTask = %v, want the caller's own refusal", err)
	}
	if !ran.Load() || hits.Load() != 0 {
		t.Errorf("caller's verification ran = %v, requests delivered = %d; want true and 0", ran.Load(), hits.Load())
	}
	if cfg.VerifyConnection == nil {
		t.Fatal("the caller's configuration was changed")
	}
}

// TestCredentialsForAPlainHTTPPeerSendNothing: an element given TLS material for a peer whose
// URL is plain http would send the request unencrypted and then refuse the answer for carrying
// no certificate. It refuses the request instead, before anything is sent.
func TestCredentialsForAPlainHTTPPeerSendNothing(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		acknowledging(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	req := NewRequester(srv.URL, "smf-1", "upf-1", &tls.Config{MinVersion: tls.VersionTLS12})
	err := req.ActivateTask(testTrigger())
	var unsent *NotSentError
	if !errors.As(err, &unsent) || hits.Load() != 0 {
		t.Errorf("ActivateTask over http with TLS credentials = %v after %d delivered request(s), "+
			"want refused unsent", err, hits.Load())
	}

	// Plain HTTP with no credentials is unchanged: there is no certificate to bind.
	plain, _ := requesterTo(t, acknowledging(t))
	if err := plain.ActivateTask(testTrigger()); err != nil {
		t.Errorf("ActivateTask over http with no TLS configuration = %v, want success", err)
	}
}
