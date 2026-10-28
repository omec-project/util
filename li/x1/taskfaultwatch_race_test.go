// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"
	"time"

	"github.com/omec-project/util/li/types"
)

// TestATaskEndedAfterTheSampleIsNotReported: the watcher decides on a fault from a snapshot and
// reports it afterwards, and the task can end in between. The server forgets a removed task's
// records as it removes it, but a report not yet begun has none to forget: sent anyway, the
// ADMF is told of a fault on an XID it has deactivated, and — when the XID is activated again
// for another task before the next sample — that task inherits the record, so the watcher later
// retracts a fault the ADMF was never told the new task had.
func TestATaskEndedAfterTheSampleIsNotReported(t *testing.T) {
	for _, c := range []struct {
		name       string
		reactivate bool
	}{
		{"deactivated", false},
		{"deactivated and its XID re-used", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newTaskRig(t)
			rig.set(TaskFault(TaskIssueDuplicationNotProgrammed, "0 of 1 rules"))

			plan := rig.w.plan(rig.at)
			if len(plan) != 1 {
				t.Fatalf("the sample planned %d reports, want the fault", len(plan))
			}

			// The task ends between the sample and its report.
			if _, err := rig.srv.deactivate(taskID); err != nil {
				t.Fatalf("deactivate: %v", err)
			}
			if c.reactivate {
				rig.st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("2")}})
			}

			rig.w.report(plan, nil)

			if n := len(rig.admf.reports()); n != 0 {
				t.Errorf("%d reports about a task that ended before they were sent", n)
			}
			k := reportKey{scope: scopeTask, id: taskID, condition: TaskIssueDuplicationNotProgrammed}
			if rig.reporter.holds(k) {
				t.Error("the reporter records a fault for the XID of a task that ended before it was reported")
			}
		})
	}
}

// TestATaskStillHeldIsReported: the check costs nothing to a task that did not change between
// the sample and its report.
func TestATaskStillHeldIsReported(t *testing.T) {
	rig := newTaskRig(t)
	rig.set(TaskFault(TaskIssueDuplicationNotProgrammed, "0 of 1 rules"))

	rig.w.report(rig.w.plan(rig.at.Add(time.Second)), nil)
	if n := rig.admf.counting(nonTerminating)[nonTerminating]; n != 1 {
		t.Errorf("%d fault reports for a task held throughout, want 1", n)
	}
}
