// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/types"
)

// hookedADMF acknowledges every report, running before each answer the hook the test sets,
// so a test can place an event at the instant the element is waiting for the ADMF.
func hookedADMF(t *testing.T, hook func()) *collectingADMF {
	t.Helper()

	a := &collectingADMF{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		a.mu.Lock()
		a.bodies = append(a.bodies, string(body))
		first := len(a.bodies) == 1
		a.mu.Unlock()
		if first && hook != nil {
			hook()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(admfResponse(t, body))) //nolint:errcheck // test handler
	}))
	t.Cleanup(a.srv.Close)

	return a
}

// TestARequestDuringThePurgeLeavesTheProcedureRunning: a request that arrives while the
// purge is clearing the store has already reset the procedure, so the purge must not mark it
// finished afterwards. If it did, whatever the ADMF tasks next would be held with no
// fail-safe behind it once the ADMF went quiet again.
func TestARequestDuringThePurgeLeavesTheProcedureRunning(t *testing.T) {
	rig := newProcedureRig(t, nil)

	rig.stepAt(61 * time.Minute) // P2 expired: no reporter, so P1 runs to its deadline
	once := false
	rig.srv.afterPurgeSnapshot = func() {
		if !once {
			once = true
			rig.srv.recordActivity() // the ADMF is heard while the store is being cleared
		}
	}
	purgedAt := 61*time.Minute + testProcedure.TimeP1 + time.Second
	rig.stepAt(purgedAt)
	if rig.tasked() {
		t.Fatal("the purge did not run")
	}

	rig.mu.Lock()
	phase := rig.srv.keepalive.phase
	rig.mu.Unlock()
	if phase != keepaliveListening {
		t.Fatalf("after a request during the purge the procedure is in phase %d, want P2 running", phase)
	}

	// The ADMF tasks again, then goes quiet: the procedure must run its course a second time.
	rig.st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}})
	rig.stepAt(purgedAt + 61*time.Minute)
	rig.stepAt(purgedAt + 61*time.Minute + testProcedure.TimeP1 + time.Second)
	if rig.tasked() {
		t.Error("tasking made after the first purge was never deactivated: the procedure stopped for good")
	}
}

// TestALateAcknowledgementDoesNotStartP3: an OK answer that arrives after timer P1 has expired
// is not one "received from the ADMF before the expiry of timer P1", so P1's expiry decides —
// the element deactivates rather than waiting out P3.
func TestALateAcknowledgementDoesNotStartP3(t *testing.T) {
	var rig *procedureRig
	admf := hookedADMF(t, func() {
		rig.mu.Lock()
		rig.at = zeroTailInstant.Add(61*time.Minute + testProcedure.TimeP1 + time.Second)
		rig.mu.Unlock()
	})
	rig = newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepAt(61 * time.Minute) // the answer lands after P1 has expired
	rig.stepAt(61*time.Minute + testProcedure.TimeP1 + 2*time.Second)
	if rig.tasked() {
		t.Error("an acknowledgement arriving after TIME_P1 started TIME_P3 instead of letting P1's expiry deactivate")
	}
}

// TestARequestWhileTheReportIsInFlightIsCleared: the ADMF is heard while the 9050 report is
// still on the wire. The request ends the procedure, but the fault it should clear was not yet
// recorded as reported, so the procedure's own tail sends the FaultCleared once the report
// settles.
func TestARequestWhileTheReportIsInFlightIsCleared(t *testing.T) {
	var rig *procedureRig
	admf := hookedADMF(t, func() {
		// A request ends the procedure. Its own clearing report would find the fault still in
		// flight and clear nothing, so this records the request without one.
		rig.mu.Lock()
		rig.at = zeroTailInstant.Add(61*time.Minute + time.Second)
		rig.mu.Unlock()
		rig.srv.mu.Lock()
		rig.srv.lastSeen = rig.srv.now()
		rig.srv.keepalive = keepaliveState{}
		rig.srv.mu.Unlock()
	})
	rig = newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepAt(61 * time.Minute)
	within(t, "the FaultCleared", func() bool { return len(admf.reports()) == 2 })
	if !strings.Contains(admf.reports()[1], faultCleared) {
		t.Errorf("the second report is not a FaultCleared:\n%s", admf.reports()[1])
	}
}

// TestRestartingP2AfterARequestChangesNothing: a retain-tasking element decides to restart P2
// at P1's expiry, and a request arrives before it acts. The request has already ended the
// procedure, so the restart must leave its state alone.
func TestRestartingP2AfterARequestChangesNothing(t *testing.T) {
	rig := newProcedureRig(t, nil)
	faultAt := zeroTailInstant.Add(61 * time.Minute)

	rig.mu.Lock()
	rig.at = faultAt.Add(30 * time.Second)
	rig.mu.Unlock()
	rig.srv.recordActivity() // the request, after the fault

	rig.srv.restartP2(faultAt, faultAt.Add(testProcedure.TimeP1+time.Second))

	rig.srv.mu.Lock()
	ka := rig.srv.keepalive
	rig.srv.mu.Unlock()
	if !ka.reset.IsZero() || ka.phase != keepaliveListening {
		t.Errorf("a restart decided before the request overwrote the state it reset: %+v", ka)
	}
}

// TestNothingFollowsAPurge: once every task has been deactivated and the Alert sent, the
// procedure waits for an X1 request; it does not purge again, or send a second Alert, however
// long the silence lasts.
func TestNothingFollowsAPurge(t *testing.T) {
	admf := newCollectingADMF(t)
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepAt(61 * time.Minute)
	rig.stepAt(61*time.Minute + testProcedure.TimeP3 + time.Second)
	if rig.tasked() {
		t.Fatal("TIME_P3 expired and tasking was not deactivated")
	}

	for _, d := range []time.Duration{4 * time.Hour, 8 * time.Hour, 20 * time.Hour} {
		rig.stepAt(d)

		// The reporter throttles a repeated Alert, so the count below cannot see a repeated
		// purge; the procedure's own state can.
		rig.srv.mu.Lock()
		phase := rig.srv.keepalive.phase
		rig.srv.mu.Unlock()
		if phase != keepalivePurged {
			t.Fatalf("at %s after the purge the procedure is in phase %d, want it to wait for an X1 request", d, phase)
		}
	}
	if got := admf.counting(alert)[alert]; got != 1 {
		t.Errorf("%d Alerts after one purge, want 1", got)
	}
	if got := admf.counting(faultReport9050)[faultReport9050]; got != 1 {
		t.Errorf("%d FaultReports, want 1: the procedure restarted without an X1 request", got)
	}
}
