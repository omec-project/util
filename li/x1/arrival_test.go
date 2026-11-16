// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var keepaliveKey = reportKey{scope: scopeElement, condition: NEIssueKeepalivesMissing}

// TestAFaultReportWhoseHandshakeFailedIsNotCleared: a report whose TLS handshake failed never
// obtained a connection, so none of it was sent and the ADMF holds no fault for a FaultCleared
// to clear. Both handshake failures an element meets are covered: a certificate the roots do
// not trust, and a trusted one that does not bind the ADMF's identifier.
func TestAFaultReportWhoseHandshakeFailedIsNotCleared(t *testing.T) {
	t.Run("untrusted certificate", func(t *testing.T) {
		admf := httptest.NewTLSServer(answeringADMFHandler(t))
		t.Cleanup(admf.Close)

		// No TLS configuration: the system roots, which do not trust the test certificate.
		reporter := NewReporter(admf.URL, "admfID", "neID", nil)
		newProcedureRig(t, reporter).stepAt(61 * time.Minute)

		if reporter.holds(keepaliveKey) {
			t.Error("the reporter records a 9050 report whose TLS handshake failed")
		}
	})

	t.Run("trusted certificate that does not bind the ADMF", func(t *testing.T) {
		unbound, roots := liCert(t, "", "")
		srv, hits := countingTLSServer(t, unbound, answeringADMFHandler(t))

		reporter := NewReporter(srv.URL, "admfID", "neID", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
		newProcedureRig(t, reporter).stepAt(61 * time.Minute)

		if reporter.holds(keepaliveKey) || hits.Load() != 0 {
			t.Errorf("held = %v after %d delivered request(s), want a report refused in the "+
				"handshake neither sent nor recorded", reporter.holds(keepaliveKey), hits.Load())
		}
	})
}

// TestAFaultReportAnsweredWithAnHTTPErrorIsStillCleared: a status other than 200 may come from
// a proxy in front of the ADMF or from the ADMF itself, and does not say which. The report may
// have arrived, so the element keeps it recorded and clears it when the ADMF is heard again.
func TestAFaultReportAnsweredWithAnHTTPErrorIsStillCleared(t *testing.T) {
	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) //nolint:errcheck // test handler
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(admf.Close)

	reporter := NewReporter(admf.URL, "admfID", "neID", nil)
	newProcedureRig(t, reporter).stepAt(61 * time.Minute)

	if !reporter.holds(keepaliveKey) {
		t.Error("a 9050 report answered with HTTP 503 was recorded as never having arrived")
	}
}

// TestARefusedFaultClearedIsNotSentAgain: the 9050 report's answer is lost, so the element
// owes the ADMF a FaultCleared when it is heard again — and the ADMF refuses it, as one that
// never received the report may. Sending the same retraction on every later X1 request gets
// the same refusal each time, for as long as the ADMF keeps talking: a retry loop the ADMF
// itself drives. The refusal is the ADMF's answer, so it settles the retraction.
func TestARefusedFaultClearedIsNotSentAgain(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()

		switch {
		case strings.Contains(string(body), faultReport9050):
			// Received, and the answer lost.
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		case strings.Contains(string(body), faultCleared):
			_, _ = w.Write([]byte(admfErrorResponse(t, body, 1000, "no such fault"))) //nolint:errcheck // test handler
		default:
			_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
		}
	}))
	t.Cleanup(admf.Close)

	clears := func() int {
		mu.Lock()
		defer mu.Unlock()

		n := 0
		for _, b := range bodies {
			if strings.Contains(b, faultCleared) {
				n++
			}
		}

		return n
	}
	idle := func(r *Reporter) bool {
		r.mu.Lock()
		defer r.mu.Unlock()

		return !r.sending[keepaliveKey]
	}

	reporter := NewReporter(admf.URL, "admfID", "neID", nil)
	rig := newProcedureRig(t, reporter)
	rig.stepAt(61 * time.Minute)
	if !reporter.holds(keepaliveKey) {
		t.Fatal("the 9050 report whose answer was lost is not recorded; this test needs it to be")
	}

	for i := range 5 {
		rig.heardAt(t, time.Duration(62+i)*time.Minute)
		within(t, "the clear to settle", func() bool { return clears() >= 1 && idle(reporter) })
	}

	if n := clears(); n != 1 {
		t.Errorf("%d FaultCleared reports over five ADMF requests, each refused; want 1", n)
	}
	if reporter.holds(keepaliveKey) {
		t.Error("the fault is still recorded after the ADMF refused its retraction")
	}
}
