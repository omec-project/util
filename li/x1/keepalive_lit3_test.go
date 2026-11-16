// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"
	"time"
)

// lit3Procedure is testProcedure at a POI triggered over LI_T3: no TIME_P3, since none can run.
var lit3Procedure = KeepaliveProcedure{
	TimeP1:            testProcedure.TimeP1,
	TimeP2:            testProcedure.TimeP2,
	TriggeredOverLIT3: true,
}

// TestAnADMFsOKDoesNotKeepAnOrphanedTriggerAlive: over LI_T3 the triggering function plays the
// ADMF (TS 33.128 clause 5.2.6), and the real ADMF answering the 9050 report says nothing about
// it. So the answer starts no TIME_P3: when TIME_P1 expires every task is deactivated — and the
// 9050 report, the purge's Alert and the PurgeKeepaliveLapse still reach the real ADMF, for its
// information.
func TestAnADMFsOKDoesNotKeepAnOrphanedTriggerAlive(t *testing.T) {
	admf := newCollectingADMF(t) // answers every report AcknowledgedAndCompleted
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepWith(lit3Procedure, 61*time.Minute)
	if n := len(admf.reports()); n != 1 || !strings.Contains(admf.reports()[0], code9050) {
		t.Fatalf("%d reports when TIME_P2 expired, want the 9050 FaultReport", n)
	}
	rig.stepWith(lit3Procedure, 61*time.Minute+lit3Procedure.TimeP1-time.Second)
	if !rig.tasked() {
		t.Fatal("tasking was removed before TIME_P1 expired")
	}

	rig.stepWith(lit3Procedure, 61*time.Minute+lit3Procedure.TimeP1+time.Second)
	if rig.tasked() {
		t.Fatal("the ADMF's OK kept the tasking past TIME_P1: an answer from the real ADMF was " +
			"taken as the triggering function's, so an orphaned trigger keeps intercepting for TIME_P3")
	}
	if got := rig.purges(); len(got) != 1 || got[0] != PurgeKeepaliveLapse {
		t.Errorf("the purge was reported as %v, want one PurgeKeepaliveLapse", got)
	}
	reports := admf.reports()
	if len(reports) != 2 {
		t.Fatalf("%d reports, want the FaultReport and the Alert", len(reports))
	}
	for _, want := range []string{alert, code10000} {
		if !strings.Contains(reports[1], want) {
			t.Errorf("the report after the purge lacks %q:\n%s", want, reports[1])
		}
	}
}

// TestATriggeringFunctionHeardAgainClearsTheFaultAtTheADMF: the procedure ends on the
// triggering function's next request exactly as on an ADMF's, and the 9050 the real ADMF holds
// is cleared — the mode changes whose answer counts, not what is reported.
func TestATriggeringFunctionHeardAgainClearsTheFaultAtTheADMF(t *testing.T) {
	admf := newCollectingADMF(t)
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepWith(lit3Procedure, 61*time.Minute)
	rig.heardAt(t, 61*time.Minute+lit3Procedure.TimeP1/2)
	within(t, "the FaultCleared", func() bool { return len(admf.reports()) == 2 })
	if !strings.Contains(admf.reports()[1], faultCleared) {
		t.Errorf("the second report is not the FaultCleared:\n%s", admf.reports()[1])
	}

	rig.stepWith(lit3Procedure, 61*time.Minute+lit3Procedure.TimeP1+time.Second)
	if !rig.tasked() {
		t.Error("tasking was purged although the triggering function was heard within TIME_P1")
	}
}

// TestRetainingTaskingOverLIT3RestartsP2: retaining tasking still means retaining it; with no
// answer that counts, P2 restarts when TIME_P1 expires.
func TestRetainingTaskingOverLIT3RestartsP2(t *testing.T) {
	retain := lit3Procedure
	retain.RetainTasking = true
	admf := newCollectingADMF(t)
	rig := newProcedureRig(t, NewReporter(admf.srv.URL, "admfID", "neID", nil))

	rig.stepWith(retain, 61*time.Minute)
	rig.stepWith(retain, 61*time.Minute+retain.TimeP1+time.Second)
	if !rig.tasked() {
		t.Fatal("an element retaining its tasking deactivated it")
	}
	rig.mu.Lock()
	phase := rig.srv.keepalive.phase
	rig.mu.Unlock()
	if phase != keepaliveListening {
		t.Errorf("after TIME_P1 the procedure is in phase %d, want P2 running again", phase)
	}
}

// TestATimeP3OverLIT3IsRefused: a TIME_P3 at a POI triggered over LI_T3 would be silently
// ignored, so it is refused rather than accepted.
func TestATimeP3OverLIT3IsRefused(t *testing.T) {
	p := lit3Procedure
	p.TimeP3 = 4 * time.Hour
	if err := p.Validate(); err == nil {
		t.Error("a TIME_P3 at a POI triggered over LI_T3 was accepted, and would never run")
	}
	if err := lit3Procedure.Validate(); err != nil {
		t.Errorf("the LI_T3 procedure without TIME_P3 was refused: %v", err)
	}
}
