// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"
	"time"
)

// TestAnOutageIsReportedOnceNotOncePerWindow: one mediation function outage is one fault. The
// destination stays unreachable for several throttle windows and the watcher samples it each
// interval; the ADMF, which already holds the fault, is not told again when a window lapses —
// a second NonTerminatingFault for the same outage describes nothing that changed. The
// recovery is still retracted once, and a later, separate outage is reported again.
func TestAnOutageIsReportedOnceNotOncePerWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		did  string
		kind string // what the fault and its retraction look like at this scope
		ok   string
	}{
		{"destination scope", didAgencyA, TaskReportNonTerminatingFault, TaskReportAllClear},
		{"element scope", "", faultReport9050, faultCleared},
	} {
		t.Run(c.name, func(t *testing.T) {
			admf := newCollectingADMF(t)
			r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
			at := zeroTailInstant
			r.now = func() time.Time { return at }

			down := true
			health := func() []DestinationHealth {
				return []DestinationHealth{{DID: c.did, Address: endpointA, Unreachable: down}}
			}
			w := NewDestinationWatcher(health, r, time.Minute)
			clock := holdClockOn(w)

			for range 5 { // five samples, an interval apart: past two throttle windows
				w.sample()
				at, clock.at = at.Add(w.interval), clock.at.Add(w.interval)
			}
			if n := admf.counting(c.kind)[c.kind]; n != 1 {
				t.Fatalf("one outage spanning several throttle windows was reported %d times, want 1", n)
			}

			down = false
			clock.recover(t, w, admf)
			if n := admf.counting(c.ok)[c.ok]; n != 1 {
				t.Fatalf("the recovery was retracted %d times, want 1", n)
			}

			down = true
			at, clock.at = at.Add(w.interval), clock.at.Add(w.interval)
			w.sample()
			if n := admf.counting(c.kind)[c.kind]; n != 2 {
				t.Errorf("a second, separate outage was reported %d times in all, want 2", n)
			}
		})
	}
}

// TestAStandingTaskFaultIsReportedOnce: the task watcher's form is a state too, so a fault the
// ADMF holds against a task is not repeated when the throttle window lapses.
func TestAStandingTaskFaultIsReportedOnce(t *testing.T) {
	admf := newCollectingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	at := zeroTailInstant
	r.now = func() time.Time { return at }

	for range 3 {
		r.NotifyTaskFault(string(testXID), TaskIssueNoTrafficSelected, "selects nothing")
		at = at.Add(time.Minute)
	}
	if n := admf.counting(TaskReportNonTerminatingFault)[TaskReportNonTerminatingFault]; n != 1 {
		t.Errorf("a standing task fault was reported %d times across three windows, want 1", n)
	}
}
