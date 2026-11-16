// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"testing"
	"time"
)

// TestContentUnderAnotherTaskIsARetractableTaskFault: a warrant whose session's content is
// delivered under another task is an authorised interception producing an incomplete product.
// It is reported against that warrant as a non-terminating fault — not as an element-level
// warning that names no warrant — and retracted once the overlap ends.
func TestContentUnderAnotherTaskIsARetractableTaskFault(t *testing.T) {
	rig := newTaskRig(t)
	const details = "1 session(s) this task selects are delivered under another task"

	rig.set(TaskFault(TaskIssueContentUnderAnotherTask, details))
	rig.sampleAt(0)
	want := TaskIssueContentUnderAnotherTask + ": " + details
	if got := rig.admf.counting(nonTerminating, want, taskID); got[nonTerminating] != 1 || got[want] != 1 || got[taskID] != 1 {
		t.Fatalf("the overlap was reported as %v, want one NonTerminatingFault against the task", got)
	}

	rig.set()
	rig.sampleAt(time.Second)
	rig.sampleAt(time.Second + time.Minute)
	if got := rig.admf.counting(allClear)[allClear]; got != 1 {
		t.Errorf("the overlap's end was retracted %d times, want 1", got)
	}
}
