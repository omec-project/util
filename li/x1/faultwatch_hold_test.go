// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"
	"time"
)

// holdClock is a watcher clock a test moves past the recovery hold by hand.
type holdClock struct {
	at time.Time
}

func holdClockOn(w *DestinationWatcher) *holdClock {
	c := &holdClock{at: zeroTailInstant}
	w.now = func() time.Time { return c.at }

	return c
}

// recover samples a destination that has just become healthy. The first look starts the
// hold and must retract nothing; a look one interval later retracts.
func (c *holdClock) recover(t *testing.T, w *DestinationWatcher, admf *collectingADMF) {
	t.Helper()

	before := len(admf.reports())
	w.sample()
	if n := len(admf.reports()) - before; n != 0 {
		t.Fatalf("the first healthy sample sent %d reports, want 0: a recovery was retracted "+
			"before the destination had stayed healthy for an interval", n)
	}

	c.at = c.at.Add(w.interval)
	w.sample()
}

// TestADestinationFaultIsRetractedWhenItsDestinationLeavesTheHealthSet: a destination fails,
// and then its last task is deactivated, so it is no longer among the destinations the
// supplier lists. Nothing will sample that destination again, so a fault retracted only on a
// healthy sample would stand at the ADMF indefinitely, against clause 5.3's "the NE shall also
// indicate that a fault has been cleared".
func TestADestinationFaultIsRetractedWhenItsDestinationLeavesTheHealthSet(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := []DestinationHealth{{DID: didAgencyA, Address: endpointA, Unreachable: true}}
	w := NewDestinationWatcher(func() []DestinationHealth { return health }, r, time.Minute)
	holdClockOn(w)

	w.sample()
	if n := admf.counting(TaskReportNonTerminatingFault)[TaskReportNonTerminatingFault]; n != 1 {
		t.Fatalf("an unreachable destination was reported %d times, want 1", n)
	}

	// The last task referring to it is deactivated.
	health = nil
	w.sample()

	if n := admf.counting(TaskReportAllClear)[TaskReportAllClear]; n != 1 {
		t.Errorf("a faulted destination that left the health set was retracted %d times, "+
			"want 1: the ADMF goes on holding a fault about a destination nothing samples", n)
	}

	// And only once.
	w.sample()
	if n := admf.counting(TaskReportAllClear)[TaskReportAllClear]; n != 1 {
		t.Errorf("the retraction was sent %d times in all, want 1", n)
	}

	// The watcher forgets a fault once its retraction arrived, or its record would grow with
	// every destination that ever failed.
	w.mu.Lock()
	defer w.mu.Unlock()
	if n := len(w.raised); n != 0 {
		t.Errorf("the watcher still records %d raised destinations after the retraction", n)
	}
}

// TestTheElementFaultIsRetractedWhenItsLastUnnamedDestinationGoes: the same at element scope.
// The un-identified destinations that raised the element fault are gone, so nothing remains
// that could recover. The scope's "say nothing without such a destination" rule is right for
// a fault this watcher never raised, and must not leave its own fault standing for good.
func TestTheElementFaultIsRetractedWhenItsLastUnnamedDestinationGoes(t *testing.T) {
	admf := newCollectingADMF(t)
	rep := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := []DestinationHealth{{DID: "", Unreachable: true}}
	w := NewDestinationWatcher(func() []DestinationHealth { return health }, rep, time.Minute)
	holdClockOn(w)

	w.sample()
	if n := admf.counting(NEIssueMDFUnreachable)[NEIssueMDFUnreachable]; n != 1 {
		t.Fatalf("reported the element fault %d times, want 1", n)
	}

	health = nil
	w.sample()

	if n := admf.counting("FaultCleared")["FaultCleared"]; n != 1 {
		t.Errorf("sent %d retractions after the last un-identified destination went, want 1", n)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.elementRaised {
		t.Error("the watcher still records the element fault after its retraction arrived")
	}
}

// TestARecoveryInsideTheHoldRetractsNothing: a destination healthy for less than an interval is
// still held, however many samples see it healthy; the retraction waits for the interval.
func TestARecoveryInsideTheHoldRetractsNothing(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := []DestinationHealth{{DID: didAgencyA, Address: endpointA, Unreachable: true}}
	w := NewDestinationWatcher(func() []DestinationHealth { return health }, r, time.Minute)
	clock := holdClockOn(w)

	w.sample()
	health[0].Unreachable = false
	w.sample() // healthy from here
	clock.at = clock.at.Add(w.interval / 2)
	w.sample()
	if n := admf.counting(TaskReportAllClear)[TaskReportAllClear]; n != 0 {
		t.Fatalf("a destination healthy for half an interval was retracted %d times", n)
	}

	clock.at = clock.at.Add(w.interval / 2)
	w.sample()
	if n := admf.counting(TaskReportAllClear)[TaskReportAllClear]; n != 1 {
		t.Errorf("a destination healthy for a full interval was retracted %d times, want 1", n)
	}
}

// TestAFlappingDestinationIsReportedOncePerHold: reachability is the last exchange's outcome,
// and each failed delivery nudges a sample, so without a hold a destination failing every
// other send would produce a fault and its retraction per failure. Inside one hold it
// produces the fault once and no retraction; the retraction comes when it has stayed healthy
// for an interval.
func TestAFlappingDestinationIsReportedOncePerHold(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := []DestinationHealth{{DID: didAgencyA, Address: endpointA}}
	w := NewDestinationWatcher(func() []DestinationHealth { return health }, r, time.Minute)
	clock := holdClockOn(w)

	for range 10 {
		health[0].Unreachable = true
		w.sample()
		health[0].Unreachable = false
		w.sample()
	}

	got := admf.counting(TaskReportNonTerminatingFault, TaskReportAllClear)
	if got[TaskReportNonTerminatingFault] != 1 || got[TaskReportAllClear] != 0 {
		t.Errorf("ten flaps inside one hold sent %d faults and %d retractions, want 1 and 0",
			got[TaskReportNonTerminatingFault], got[TaskReportAllClear])
	}

	// Healthy since the last flap; one interval later it is retracted.
	clock.at = clock.at.Add(w.interval)
	w.sample()

	if n := admf.counting(TaskReportAllClear)[TaskReportAllClear]; n != 1 {
		t.Errorf("a destination healthy for a full interval was retracted %d times, want 1", n)
	}
}

// TestTheReportRecordStaysBoundedUnderTaskReports: a triggering function reports per XID, and
// a task-scoped event has no retraction. Neither of the reporter's records may keep an entry
// for every XID that ever had one — reported forever, or lastSent long past the window in
// which it could suppress anything.
func TestTheReportRecordStaysBoundedUnderTaskReports(t *testing.T) {
	r := NewReporter("https://admf.invalid", "admfID", "neID", nil)
	at := zeroTailInstant
	r.now = func() time.Time { return at }

	for i := range 100 {
		k := reportKey{scope: scopeTask, id: newUUID(), condition: "triggerRefused"}
		if !r.reserve(k) {
			t.Fatalf("report %d was refused a reservation", i)
		}
		r.commit(k, false) // an event, as NotifyTaskAsync settles it
		at = at.Add(reportThrottle)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if n := len(r.reported); n != 0 {
		t.Errorf("%d task-scoped reports are recorded as standing, want 0: nothing can retract "+
			"them, so each is an entry that is never removed", n)
	}
	if n := len(r.lastSent); n > 1 {
		t.Errorf("the throttle holds %d entries after 100 reports a window apart, want at most 1", n)
	}
}

// sample reports the transitions since the last look: sampleUntil with no stop, for the tests
// that drive the watcher one sample at a time.
func (w *DestinationWatcher) sample() {
	w.sampleUntil(nil)
}
