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
		name string
		// successor is the task activated under the XID once it ends, nil for none.
		successor *types.InterceptTask
	}{
		{"deactivated", nil},
		{"deactivated and its XID re-used", &types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("2")}}},
		// The successor is identical to the task the fault was observed on, so comparing the
		// two cannot tell them apart.
		{"deactivated and its XID re-used for an identical task", &types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}}},
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
			if c.successor != nil {
				rig.st.Activate(*c.successor)
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

// TestARetractionDecidedForAnEndedTaskIsNotSent: a sample decides to retract a fault, and before
// the retraction is sent the task ends and its XID is activated again, for an identical task
// whose own fault is recorded in the meantime. The retraction was decided on the old task's
// absence, so sent it would tell the ADMF the new task's fault had cleared, a fault the
// element still observes and so never reports again.
func TestARetractionDecidedForAnEndedTaskIsNotSent(t *testing.T) {
	rig := newTaskRig(t)
	k := reportKey{scope: scopeTask, id: taskID, condition: TaskIssueDuplicationNotProgrammed}

	rig.set(TaskFault(TaskIssueDuplicationNotProgrammed, "0 of 1 rules"))
	rig.sampleAt(0)
	rig.set()
	rig.sampleAt(10 * time.Second)

	plan := rig.w.plan(rig.at.Add(time.Minute))
	if len(plan) != 1 || !plan[0].clear {
		t.Fatalf("the sample planned %+v, want the retraction", plan)
	}

	if _, err := rig.srv.deactivate(taskID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	rig.st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}})
	rig.reporter.NotifyTaskFault(taskID, TaskIssueDuplicationNotProgrammed, "0 of 1 rules")

	rig.w.report(plan, nil)

	if n := rig.admf.counting(allClear)[allClear]; n != 0 {
		t.Errorf("%d AllClear sent for the new task's fault on the strength of the old task's absence", n)
	}
	if !rig.reporter.holds(k) {
		t.Error("the new task's fault is no longer recorded, so it can never be retracted")
	}

	// The new task's fault keeps its own hold: absent from now on, it is retracted an interval
	// later, not at the next sample, by which the old task's hold, begun at 10 s, has lapsed.
	rig.sampleAt(71 * time.Second)
	if n := rig.admf.counting(allClear)[allClear]; n != 0 {
		t.Fatal("the new task's fault was retracted on its first absent sample, on the old task's hold")
	}
	rig.sampleAt(71*time.Second + time.Minute)
	if n := rig.admf.counting(allClear)[allClear]; n != 1 {
		t.Errorf("%d AllClear once the new task's fault stayed absent an interval, want 1", n)
	}
}
