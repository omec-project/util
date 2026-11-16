// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"
	"time"
)

// The reporter's bookkeeping has guards whose purpose is a sequence of events, not a single
// call, and no end-to-end test happens to produce those sequences. These pin each guard to the
// sequence it exists for, so removing one fails here rather than in an ADMF's fault list.

// TestARetractionDoesNotOvertakeTheReportItRetracts: while a report of a fault is in flight a
// retraction of it is not started. Started, the two would race to the ADMF, and an AllClear that
// arrives before the fault it clears leaves the ADMF holding the fault.
func TestARetractionDoesNotOvertakeTheReportItRetracts(t *testing.T) {
	r := NewReporter("http://127.0.0.1:0", "admfID", "neID", nil)
	k := reportKey{scope: scopeDestination, id: "did", condition: NEIssueMDFUnreachable}

	// Reported once, and a repeat of the report now in flight.
	r.reported[k] = true
	r.sending[k] = true

	if r.clearing(k) {
		t.Error("a retraction was started while a report of the same fault was in flight")
	}
}

// TestAWatcherKeepsAFaultWhoseFirstReportIsInFlight: a watcher that asked for a retraction asks
// the reporter whether it still holds the fault, to know whether to ask again. A first report
// still in flight is not recorded yet, and the retraction found it in flight and sent nothing;
// answered "not held", the watcher forgets the fault, the report then lands, and nothing ever
// retracts it.
func TestAWatcherKeepsAFaultWhoseFirstReportIsInFlight(t *testing.T) {
	r := NewReporter("http://127.0.0.1:0", "admfID", "neID", nil)
	w := NewDestinationWatcher(nil, r, time.Minute)
	k := reportKey{scope: scopeDestination, id: "did", condition: w.condition}

	w.raised["did"] = time.Time{}
	r.sending[k] = true // the first report, not yet settled

	w.record([]watchReport{{did: "did", kind: reportRetract}})
	if _, still := w.raised["did"]; !still {
		t.Error("the watcher forgot a fault whose first report was still in flight")
	}
}

// TestAForgottenTaskDoesNotStayForgottenAfterAFailedReport: a task ends while a report about it
// is in flight, and the report then fails. The mark that made its settlement record nothing must
// go with it: left behind, it swallows the record of the next task activated under that XID, so
// the watcher can never retract that task's fault.
func TestAForgottenTaskDoesNotStayForgottenAfterAFailedReport(t *testing.T) {
	r := NewReporter("http://127.0.0.1:0", "admfID", "neID", nil)
	k := reportKey{scope: scopeTask, id: taskID, condition: TaskIssueNoTrafficSelected}

	if !r.reserve(k) {
		t.Fatal("the first report was not admitted")
	}
	r.forgetTask(taskID)
	r.release(k) // the report did not arrive

	// The XID is activated again, and its own fault is reported and accepted.
	if !r.reserve(k) {
		t.Fatal("the new task's report was not admitted")
	}
	r.commit(k, true)
	if !r.holds(k) {
		t.Error("the new task's accepted fault is not recorded: the old task's forgotten mark swallowed it")
	}
}

// TestForgettingATaskEndsARetractionInFlight: a task ends while the retraction of one of its
// faults is in flight. Whatever that retraction's outcome, the record is gone — the XID it names
// has been deactivated — and a task activated under the same XID inherits nothing.
func TestForgettingATaskEndsARetractionInFlight(t *testing.T) {
	for _, delivered := range []bool{true, false} {
		r := NewReporter("http://127.0.0.1:0", "admfID", "neID", nil)
		k := reportKey{scope: scopeTask, id: taskID, condition: TaskIssueNoTrafficSelected}
		r.reported[k] = true

		if !r.clearing(k) {
			t.Fatal("the retraction was not admitted")
		}
		r.forgetTask(taskID)
		if delivered {
			r.commitClear(k)
		} else {
			r.release(k)
		}

		if r.holds(k) || len(r.forgotten) != 0 {
			t.Errorf("delivered=%v: after the task ended the reporter holds %v, forgotten %v",
				delivered, r.reported, r.forgotten)
		}
	}
}
