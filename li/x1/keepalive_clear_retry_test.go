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

// TestAFaultClearedThatDoesNotArriveIsSentAgain: the ADMF holds the 9050 fault until a
// FaultCleared reaches it. One that fails in transit leaves the fault standing at the ADMF
// while the element is talking to it, so the next X1 request sends the clear again, and
// once one has arrived no further request sends another.
func TestAFaultClearedThatDoesNotArriveIsSentAgain(t *testing.T) {
	var (
		mu       sync.Mutex
		clears   int
		accepted int
	)
	admf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		if strings.Contains(string(body), faultCleared) {
			mu.Lock()
			clears++
			first := clears == 1
			if !first {
				accepted++
			}
			mu.Unlock()
			if first {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	t.Cleanup(admf.Close)

	rig := newProcedureRig(t, NewReporter(admf.URL, "admfID", "neID", nil))
	rig.stepAt(61 * time.Minute) // TIME_P2 expired: the 9050 report reaches the ADMF

	counts := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()

		return clears, accepted
	}

	rig.heardAt(t, 62*time.Minute)
	within(t, "the first FaultCleared attempt", func() bool { c, _ := counts(); return c == 1 })
	within(t, "the failed clear to settle", func() bool {
		r := rig.srv.keepaliveReporter
		r.mu.Lock()
		defer r.mu.Unlock()

		return !r.sending[reportKey{scope: scopeElement, condition: NEIssueKeepalivesMissing}]
	})

	rig.heardAt(t, 63*time.Minute)
	within(t, "the FaultCleared to be sent again", func() bool { _, a := counts(); return a == 1 })

	rig.heardAt(t, 64*time.Minute)
	time.Sleep(100 * time.Millisecond) // a third clear would be on its own goroutine; give it time
	if c, a := counts(); c != 2 || a != 1 {
		t.Errorf("%d clears sent, %d accepted, want 2 and 1: the clear must be retried until it "+
			"arrives and never sent once it has", c, a)
	}
}
