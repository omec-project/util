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

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// testProcedure is the clause 6.6.2 procedure at its default timers, written out so a test
// reads the instants it moves the clock to.
var testProcedure = KeepaliveProcedure{
	TimeP1: time.Minute,
	TimeP2: time.Hour,
	TimeP3: 2 * time.Hour,
}

// procedureRig is a tasked element with an injected clock and, where a test gives it one,
// a reporter to an ADMF stub.
type procedureRig struct {
	st  *store.Store
	srv *Server
	at  time.Time

	mu     sync.Mutex
	purged []PurgeReason
}

func newProcedureRig(t *testing.T, reporter *Reporter) *procedureRig {
	t.Helper()

	rig := &procedureRig{st: store.New(), at: zeroTailInstant}
	rig.st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}})

	opts := []Option{OnPurge(func(_ types.InterceptTask, reason PurgeReason) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.purged = append(rig.purged, reason)
	})}
	if reporter != nil {
		opts = append(opts, KeepaliveReporter(reporter))
	}
	rig.srv = testServer(rig.st, opts...)
	rig.srv.now = func() time.Time {
		rig.mu.Lock()
		defer rig.mu.Unlock()

		return rig.at
	}
	rig.srv.recordActivity() // the ADMF was last heard at the start

	return rig
}

// stepAt moves the clock to start+d and advances the procedure.
func (rig *procedureRig) stepAt(d time.Duration) {
	rig.mu.Lock()
	rig.at = zeroTailInstant.Add(d)
	rig.mu.Unlock()

	rig.srv.keepaliveStep(testProcedure)
}

// stepWith is stepAt for a procedure other than testProcedure.
func (rig *procedureRig) stepWith(p KeepaliveProcedure, d time.Duration) {
	rig.mu.Lock()
	rig.at = zeroTailInstant.Add(d)
	rig.mu.Unlock()

	rig.srv.keepaliveStep(p)
}

func (rig *procedureRig) tasked() bool {
	_, held := rig.st.Get(testXID)

	return held
}

func (rig *procedureRig) purges() []PurgeReason {
	rig.mu.Lock()
	defer rig.mu.Unlock()

	return append([]PurgeReason(nil), rig.purged...)
}

// heardAt delivers an authenticated KeepaliveRequest from the ADMF at start+d.
func (rig *procedureRig) heardAt(t *testing.T, d time.Duration) {
	t.Helper()

	rig.mu.Lock()
	rig.at = zeroTailInstant.Add(d)
	rig.mu.Unlock()

	if _, err := rig.srv.Process([]byte(keepaliveXML), admfPeer(t)); err != nil {
		t.Fatalf("keepalive: %v", err)
	}
}

// within waits up to two seconds for cond, for a report sent on its own goroutine.
func within(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// refusingADMF answers every report with an X1 ErrorResponse inside a 200, which clause 6.6.2
// treats as no acknowledgement.
func refusingADMF(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test handler
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(admfErrorResponse(t, body, 1000, "refused"))) //nolint:errcheck // test handler
	}))
	t.Cleanup(srv.Close)

	return srv
}

const (
	faultReport9050 = `<ns1:typeOfNeIssueMessage>FaultReport</ns1:typeOfNeIssueMessage>`
	faultCleared    = `<ns1:typeOfNeIssueMessage>FaultCleared</ns1:typeOfNeIssueMessage>`
	alert           = `<ns1:typeOfNeIssueMessage>Alert</ns1:typeOfNeIssueMessage>`
	code9050        = `<ns1:issueCode>9050</ns1:issueCode>`
	code10000       = `<ns1:issueCode>10000</ns1:issueCode>`
)

// TestKeepalivesThatStopAreReportedAndClearedByTheNextRequest is clause 6.6.2's first two
// steps: when timer P2 expires the element sends a FaultReport with code 9050 and keeps its
// tasking, and the next X1 request from the ADMF clears the fault with a FaultCleared.
func TestKeepalivesThatStopAreReportedAndClearedByTheNextRequest(t *testing.T) {
	admf := newCollectingADMF(t)
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepAt(59 * time.Minute)
	if n := len(admf.reports()); n != 0 {
		t.Fatalf("%d reports before TIME_P2 expired, want 0", n)
	}

	rig.stepAt(61 * time.Minute)
	reports := admf.reports()
	if len(reports) != 1 {
		t.Fatalf("%d reports when TIME_P2 expired, want the one FaultReport", len(reports))
	}
	for _, want := range []string{faultReport9050, code9050, NEIssueKeepalivesMissing + ":"} {
		if !strings.Contains(reports[0], want) {
			t.Errorf("the report lacks %q:\n%s", want, reports[0])
		}
	}
	if !rig.tasked() {
		t.Fatal("tasking was removed when TIME_P2 expired; clause 6.6.2 reports first")
	}

	rig.heardAt(t, 62*time.Minute)
	within(t, "the FaultCleared", func() bool { return len(admf.reports()) == 2 })

	cleared := admf.reports()[1]
	for _, want := range []string{faultCleared, code9050} {
		if !strings.Contains(cleared, want) {
			t.Errorf("the clearing report lacks %q:\n%s", want, cleared)
		}
	}
	// The condition leads the description once: the reporter prefixes it, so the clearing
	// report's own text is only what happened.
	if !strings.Contains(cleared, "<ns1:description>"+NEIssueKeepalivesMissing+": resolved</ns1:description>") {
		t.Errorf("the clearing report's description is not %q:\n%s", NEIssueKeepalivesMissing+": resolved", cleared)
	}

	// The procedure is over: P2 runs from the request, and the deadline that would have
	// followed the report no longer exists.
	rig.stepAt(62*time.Minute + 3*time.Hour)
	if !rig.tasked() {
		t.Error("tasking was purged after the ADMF was heard again")
	}
}

// TestAnAcknowledgedReportDeactivatesAllTasksWhenP3Expires: the ADMF answered the 9050 report,
// so timer P3 runs, and when it expires with no X1 request every task is deactivated and an
// Alert with code 10000 follows.
func TestAnAcknowledgedReportDeactivatesAllTasksWhenP3Expires(t *testing.T) {
	admf := newCollectingADMF(t)
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepAt(61 * time.Minute)
	rig.stepAt(61*time.Minute + testProcedure.TimeP1 + time.Second) // past P1: acknowledged, so it does not apply
	if !rig.tasked() {
		t.Fatal("tasking was removed at TIME_P1 although the ADMF acknowledged the report")
	}

	rig.stepAt(61*time.Minute + testProcedure.TimeP3 - time.Second)
	if !rig.tasked() {
		t.Fatal("tasking was removed before TIME_P3 expired")
	}

	rig.stepAt(61*time.Minute + testProcedure.TimeP3 + time.Second)
	if rig.tasked() {
		t.Fatal("TIME_P3 expired with no X1 request and tasking was not deactivated")
	}
	if got := rig.purges(); len(got) != 1 || got[0] != PurgeKeepaliveLapse {
		t.Errorf("the purge was reported as %v, want one PurgeKeepaliveLapse", got)
	}

	reports := admf.reports()
	if len(reports) != 2 {
		t.Fatalf("%d reports, want the FaultReport and the Alert", len(reports))
	}
	for _, want := range []string{alert, code10000, NEIssueTaskingPurged + ":"} {
		if !strings.Contains(reports[1], want) {
			t.Errorf("the report after the purge lacks %q:\n%s", want, reports[1])
		}
	}
}

// TestAnUnansweredReportDeactivatesAllTasksWhenP1Expires: no acknowledgement of the 9050
// report — an error answer, no ADMF reachable, or no reporter at all — leaves timer P1
// running, and when it expires every task is deactivated.
func TestAnUnansweredReportDeactivatesAllTasksWhenP1Expires(t *testing.T) {
	for _, c := range []struct {
		name     string
		reporter func(t *testing.T) *Reporter
	}{
		{"an error answer", func(t *testing.T) *Reporter {
			return NewReporter(refusingADMF(t).URL, "admfID", "neID", nil)
		}},
		{"no ADMF reachable", func(t *testing.T) *Reporter {
			dead := httptest.NewServer(http.NotFoundHandler())
			dead.Close()

			return NewReporter(dead.URL, "admfID", "neID", nil)
		}},
		{"no reporter", func(*testing.T) *Reporter { return nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newProcedureRig(t, c.reporter(t))

			rig.stepAt(61 * time.Minute)
			rig.stepAt(61*time.Minute + testProcedure.TimeP1 - time.Second)
			if !rig.tasked() {
				t.Fatal("tasking was removed before TIME_P1 expired")
			}

			rig.stepAt(61*time.Minute + testProcedure.TimeP1 + time.Second)
			if rig.tasked() {
				t.Fatal("TIME_P1 expired with the report unanswered and tasking was not deactivated")
			}
		})
	}
}

// TestRetainingTaskingRestartsP2 is the element configured "to not allow deactivation of all
// tasks as part of the Keepalive procedure": whether or not the report is acknowledged, it
// resets timer P2 and continues, and P2 expiring again reports again.
func TestRetainingTaskingRestartsP2(t *testing.T) {
	retain := testProcedure
	retain.RetainTasking = true

	t.Run("acknowledged", func(t *testing.T) {
		admf := newCollectingADMF(t)
		rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

		rig.stepWith(retain, 61*time.Minute)
		rig.stepWith(retain, 61*time.Minute+5*time.Hour) // long past P1 and P3
		if !rig.tasked() {
			t.Fatal("an element retaining its tasking deactivated it")
		}
		// P2 restarted at the acknowledgement and has expired again since.
		if got := admf.counting(faultReport9050)[faultReport9050]; got != 2 {
			t.Errorf("%d FaultReports, want 2: P2 was not restarted after the acknowledgement", got)
		}
	})

	t.Run("unanswered", func(t *testing.T) {
		rig := newProcedureRig(t, nil)

		rig.stepWith(retain, 61*time.Minute)
		rig.stepWith(retain, 61*time.Minute+retain.TimeP1+time.Second)
		if !rig.tasked() {
			t.Fatal("an element retaining its tasking deactivated it when TIME_P1 expired")
		}
		rig.mu.Lock()
		phase := rig.srv.keepalive.phase
		rig.mu.Unlock()
		if phase != keepaliveListening {
			t.Errorf("after TIME_P1 the procedure is in phase %d, want P2 running again", phase)
		}
	})
}

// TestARequestDuringP3EndsTheProcedure: an X1 request while timer P3 runs stops it, so the
// deactivation it would have led to does not happen.
func TestARequestDuringP3EndsTheProcedure(t *testing.T) {
	admf := newCollectingADMF(t)
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepAt(61 * time.Minute)
	rig.heardAt(t, 90*time.Minute)
	within(t, "the FaultCleared", func() bool { return len(admf.reports()) == 2 })

	rig.stepAt(61*time.Minute + testProcedure.TimeP3 + time.Second)
	if !rig.tasked() {
		t.Error("tasking was deactivated by a P3 the ADMF's request had stopped")
	}
}

// TestNoFaultIsClearedThatWasNotReported: a request ending a procedure whose report never
// reached the ADMF sends no FaultCleared, because the ADMF holds no fault to clear.
func TestNoFaultIsClearedThatWasNotReported(t *testing.T) {
	admf := newCollectingADMF(t)
	reporter := NewReporter(refusingADMF(t).URL, "admfID", "neID", nil)
	rig := newProcedureRig(t, reporter)

	rig.stepAt(61 * time.Minute)
	rig.heardAt(t, 62*time.Minute)

	// The clearing report would go to the same refusing ADMF; what matters is that none is
	// attempted, which the reporter's record decides.
	time.Sleep(50 * time.Millisecond)
	if reporter.holds(reportKey{scope: scopeElement, condition: NEIssueKeepalivesMissing}) {
		t.Error("the reporter records a keepalive fault the ADMF refused")
	}
	if n := len(admf.reports()); n != 0 {
		t.Errorf("%d reports reached an ADMF that was never told of the fault", n)
	}
}

// TestTheProcedureDefaultsAreTheClauses: a zero timer is the clause 6.6.2 default.
func TestTheProcedureDefaultsAreTheClauses(t *testing.T) {
	got := KeepaliveProcedure{}.withDefaults()
	if got.TimeP1 != time.Minute || got.TimeP2 != time.Hour || got.TimeP3 != 2*time.Hour {
		t.Errorf("defaults are P1 %s, P2 %s, P3 %s; want 1m, 1h, 2h", got.TimeP1, got.TimeP2, got.TimeP3)
	}
	if got.RetainTasking {
		t.Error("the default retains tasking; clause 6.6.2 removes it unless otherwise configured")
	}
}

// TestATimerBelowTheMinimumIsRefused: Validate refuses a timer WatchKeepalive could not tick
// at, and WatchKeepalive returns at once rather than run with one.
func TestATimerBelowTheMinimumIsRefused(t *testing.T) {
	if err := (KeepaliveProcedure{}).Validate(); err != nil {
		t.Errorf("the defaults were refused: %v", err)
	}

	short := KeepaliveProcedure{TimeP2: time.Millisecond}
	if err := short.Validate(); err == nil {
		t.Error("a TIME_P2 of 1ms was accepted")
	}

	srv := testServer(store.New())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.WatchKeepalive(short, nil) // nil stop: only a refusal returns
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchKeepalive ran with a timer below MinKeepaliveWindow")
	}
}

// TestWatchKeepaliveRunsTheProcedureAndStops drives the real watchdog at the shortest timers it
// allows: with no reporter the report is unanswered, so tasking goes once P2 and P1 have
// expired, and closing stop ends the watchdog.
func TestWatchKeepaliveRunsTheProcedureAndStops(t *testing.T) {
	st := store.New()
	st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}})
	srv := testServer(st)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.WatchKeepalive(KeepaliveProcedure{
			TimeP1: MinKeepaliveWindow, TimeP2: MinKeepaliveWindow, TimeP3: MinKeepaliveWindow,
		}, stop)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, held := st.Get(testXID); !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watchdog did not deactivate tasking after P2 and P1 expired")
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchKeepalive did not return when stop closed")
	}
}
