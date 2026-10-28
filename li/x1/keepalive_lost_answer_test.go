// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAFaultReportWhoseAnswerIsLostIsStillCleared: the ADMF receives the 9050 report and its
// answer never reaches the element — the connection drops after the request arrived. The ADMF
// holds the fault; the element heard no acknowledgement. When the ADMF is heard again, clause
// 6.6.2 owes it a FaultCleared, and an element that clears only what it heard acknowledged
// would leave the ADMF holding a keepalive fault for an element it is talking to, for good.
func TestAFaultReportWhoseAnswerIsLostIsStillCleared(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()

		if strings.Contains(string(body), faultReport9050) {
			// Received, and the answer lost: the connection goes before a byte of it.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}

			return
		}
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	t.Cleanup(admf.Close)

	count := func(s string) int {
		mu.Lock()
		defer mu.Unlock()

		n := 0
		for _, b := range bodies {
			if strings.Contains(b, s) {
				n++
			}
		}

		return n
	}

	rig := newProcedureRig(t, NewReporter(admf.URL, "admfID", "neID", nil))
	rig.stepAt(61 * time.Minute)
	if count(faultReport9050) != 1 {
		t.Fatalf("the 9050 report did not reach the ADMF")
	}

	rig.heardAt(t, 62*time.Minute)
	within(t, "the FaultCleared for the fault whose answer was lost", func() bool { return count(faultCleared) == 1 })
}

// TestAFaultReportThatNeverLeftIsNotCleared: a report whose connection was never made did not
// reach the ADMF, so there is nothing for a FaultCleared to clear. The lost-answer case above
// must not become a FaultCleared after every failed report.
func TestAFaultReportThatNeverLeftIsNotCleared(t *testing.T) {
	// A listener that is closed at once: the address refuses connections.
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()

	reporter := NewReporter(url, "admfID", "neID", nil)
	rig := newProcedureRig(t, reporter)
	rig.stepAt(61 * time.Minute)

	if reporter.holds(reportKey{scope: scopeElement, condition: NEIssueKeepalivesMissing}) {
		t.Error("the reporter records a 9050 report whose connection was refused")
	}
}

// TestAFaultReportTheADMFCouldNotParseIsNotCleared: an ADMF that answers with the clause 6.1
// TopLevelError has said it could not read the report, so it holds no fault to clear.
func TestAFaultReportTheADMFCouldNotParseIsNotCleared(t *testing.T) {
	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		//nolint:errcheck // test handler
		_, _ = w.Write([]byte(`<?xml version="1.0"?><X1TopLevelErrorResponse xmlns="http://uri.etsi.org/03221/X1/2017/10">` +
			`<admfIdentifier>admfID</admfIdentifier><neIdentifier>neID</neIdentifier>` +
			`<messageTimestamp>2026-01-01T00:00:00.000000Z</messageTimestamp><version>v1.6.1</version>` +
			`</X1TopLevelErrorResponse>`))
	}))
	t.Cleanup(admf.Close)

	reporter := NewReporter(admf.URL, "admfID", "neID", nil)
	rig := newProcedureRig(t, reporter)
	rig.stepAt(61 * time.Minute)

	if reporter.holds(reportKey{scope: scopeElement, condition: NEIssueKeepalivesMissing}) {
		t.Error("the reporter records a 9050 report the ADMF could not parse")
	}
}
