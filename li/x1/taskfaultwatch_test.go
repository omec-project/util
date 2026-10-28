// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// taskRig is an element holding one task whose faults a test sets, a reporter to a collecting
// ADMF, and a task watcher on a clock the test moves.
type taskRig struct {
	st       *store.Store
	srv      *Server
	reporter *Reporter
	admf     *collectingADMF
	w        *taskWatch
	at       time.Time

	mu     sync.Mutex
	faults []X1Error
}

func newTaskRig(t *testing.T) *taskRig {
	t.Helper()

	rig := &taskRig{st: store.New(), admf: newCollectingADMF(t), at: zeroTailInstant}
	rig.st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}})
	rig.srv = testServer(rig.st, WithTaskFaults(func(xid types.XID) []X1Error {
		if xid != testXID {
			return nil
		}
		rig.mu.Lock()
		defer rig.mu.Unlock()

		return append([]X1Error(nil), rig.faults...)
	}))
	rig.reporter = NewReporter(rig.admf.srv.URL, "admfID", "neID", nil)
	rig.w = newTaskWatch(rig.srv, rig.reporter, time.Minute)
	rig.w.now = func() time.Time { return rig.at }

	return rig
}

func (rig *taskRig) set(faults ...X1Error) {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	rig.faults = faults
}

func (rig *taskRig) sampleAt(d time.Duration) {
	rig.at = zeroTailInstant.Add(d)
	rig.w.sampleUntil(nil)
}

// taskID is testXID as the reporter's string-keyed API takes it.
const taskID = string(testXID)

const (
	nonTerminating = `<ns1:taskReportType>NonTerminatingFault</ns1:taskReportType>`
	allClear       = `<ns1:taskReportType>AllClear</ns1:taskReportType>`
)

// TestATaskFaultIsReportedAndClearedAfterTheHold is clause 6.5.2.1: "If a non-terminating fault
// is cleared, the NE shall send another ReportTaskIssue indicating the fault is cleared." The
// fault is reported when it appears, and its AllClear once it has stayed absent an interval.
func TestATaskFaultIsReportedAndClearedAfterTheHold(t *testing.T) {
	rig := newTaskRig(t)

	rig.set(TaskFault(TaskIssueDuplicationNotProgrammed, "0 of 1 rules"))
	rig.sampleAt(0)
	if got := rig.admf.counting(nonTerminating, TaskIssueDuplicationNotProgrammed+": 0 of 1 rules"); got[nonTerminating] != 1 ||
		got[TaskIssueDuplicationNotProgrammed+": 0 of 1 rules"] != 1 {
		t.Fatalf("the fault was reported as %v, want one NonTerminatingFault carrying the condition", got)
	}

	rig.set()
	rig.sampleAt(10 * time.Second)
	if got := rig.admf.counting(allClear)[allClear]; got != 0 {
		t.Fatal("the fault was retracted on its first absent sample, inside the hold")
	}

	rig.sampleAt(10*time.Second + time.Minute)
	reports := rig.admf.reports()
	if len(reports) != 2 {
		t.Fatalf("%d reports, want the fault and its AllClear", len(reports))
	}
	for _, want := range []string{allClear, taskID, TaskIssueDuplicationNotProgrammed + ": resolved"} {
		if !strings.Contains(reports[1], want) {
			t.Errorf("the clearing report lacks %q:\n%s", want, reports[1])
		}
	}
	if rig.reporter.holds(reportKey{scope: scopeTask, id: taskID, condition: TaskIssueDuplicationNotProgrammed}) {
		t.Error("the reporter still records a fault it has retracted")
	}
	if len(rig.w.healthy) != 0 {
		t.Errorf("the watcher still holds %d recoveries after the retraction", len(rig.w.healthy))
	}
}

// TestAFlappingTaskFaultIsRetractedOncePerHold: a fault that comes back inside the hold
// restarts it, so a condition that flaps is not reported cleared and raised again per sample.
func TestAFlappingTaskFaultIsRetractedOncePerHold(t *testing.T) {
	rig := newTaskRig(t)
	fault := TaskFault(TaskIssueNoTrafficSelected, "no session")

	for i, present := range []bool{true, false, true, false, false} {
		if present {
			rig.set(fault)
		} else {
			rig.set()
		}
		rig.sampleAt(time.Duration(i) * 20 * time.Second) // every sample inside one interval of the last change
	}
	if got := rig.admf.counting(allClear)[allClear]; got != 0 {
		t.Errorf("a fault absent for less than an interval was retracted %d times", got)
	}
}

// TestAnEventIsNeitherSampledNorRetracted: a task-scoped event has no ending. The watcher reports
// only the conditions that are states, and an event pushed by the element stays unrecorded.
func TestAnEventIsNeitherSampledNorRetracted(t *testing.T) {
	rig := newTaskRig(t)

	rig.set(TaskFault(TaskIssueRecordNotEncoded, "an AMFRegistration"))
	rig.sampleAt(0)
	if n := len(rig.admf.reports()); n != 0 {
		t.Errorf("the watcher reported an event %d times", n)
	}

	rig.reporter.NotifyTaskAsync(taskID, TaskReportNonTerminatingFault, TaskIssueRecordNotEncoded, "pushed")
	within(t, "the pushed event", func() bool { return len(rig.admf.reports()) == 1 })
	awaitSettled(t, rig.reporter, reportKey{scope: scopeTask, id: taskID, condition: TaskIssueRecordNotEncoded})
	if got := rig.reporter.standingTaskFaults(); len(got) != 0 {
		t.Errorf("a task-scoped event is recorded as a standing fault: %v", got)
	}
}

// TestAFaultOfATaskNoLongerHeldIsForgotten: the task's end ends its faults, so the record goes
// without an AllClear for an XID the ADMF has deactivated.
func TestAFaultOfATaskNoLongerHeldIsForgotten(t *testing.T) {
	rig := newTaskRig(t)

	rig.set(TaskFault(TaskIssueTriggerNotRunning, "refused"))
	rig.sampleAt(0)
	rig.st.Deactivate(testXID)
	rig.sampleAt(2 * time.Minute)

	if got := rig.admf.counting(allClear)[allClear]; got != 0 {
		t.Errorf("a fault of a deactivated task was retracted %d times", got)
	}
	if got := rig.reporter.standingTaskFaults(); len(got) != 0 {
		t.Errorf("the record of a deactivated task's fault was kept: %v", got)
	}
}

// TestAFaultTheElementPushedIsRetractedByTheWatcher: a state reported by the element itself is
// retractable too, because the watcher retracts whatever the reporter holds that has ended.
func TestAFaultTheElementPushedIsRetractedByTheWatcher(t *testing.T) {
	rig := newTaskRig(t)

	rig.reporter.NotifyTaskFault(taskID, TaskIssueTriggerNotRunning, "pushed")
	rig.sampleAt(0)
	rig.sampleAt(time.Minute)

	if got := rig.admf.counting(allClear)[allClear]; got != 1 {
		t.Errorf("a pushed fault that ended was retracted %d times, want 1", got)
	}
}

// TestWatchTaskFaultsNeedsASupplier: with nothing to sample the watcher returns at once.
func TestWatchTaskFaultsNeedsASupplier(t *testing.T) {
	srv := testServer(store.New())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.WatchTaskFaults(nil, time.Second, nil)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchTaskFaults ran with no WithTaskFaults supplier")
	}
}

// TestWatchTaskFaultsStops: the running watcher returns when stop closes.
func TestWatchTaskFaultsStops(t *testing.T) {
	rig := newTaskRig(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.WatchTaskFaults(rig.reporter, 10*time.Millisecond, stop)
	}()
	rig.set(TaskFault(TaskIssueDuplicationNotProgrammed, "0 of 1 rules"))
	within(t, "the watcher's report", func() bool { return len(rig.admf.reports()) == 1 })

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchTaskFaults did not return when stop closed")
	}
}

// TestAStatePushedAsAnEventIsNotRecorded: NotifyTaskAsync is the form for an event, which has
// no ending, so even a condition that is a state is not recorded when pushed that way. Recorded,
// it would wait for a retraction only the task watcher sends, and an element running no watcher
// would keep one record per task forever.
func TestAStatePushedAsAnEventIsNotRecorded(t *testing.T) {
	rig := newTaskRig(t)

	rig.reporter.NotifyTaskAsync(taskID, TaskReportNonTerminatingFault, TaskIssueTriggerNotRunning, "pushed")
	within(t, "the pushed report", func() bool { return len(rig.admf.reports()) == 1 })
	awaitSettled(t, rig.reporter, reportKey{scope: scopeTask, id: taskID, condition: TaskIssueTriggerNotRunning})
	if got := rig.reporter.standingTaskFaults(); len(got) != 0 {
		t.Errorf("a state pushed with NotifyTaskAsync is recorded as standing: %v", got)
	}
}

// TestADeactivatedTaskLeavesNoFaultForItsSuccessor: a task deactivated and activated again under
// the same XID within one watcher interval must not inherit the old task's fault. Forgotten only
// at the next sample, the record would still stand when that sample finds the XID held again,
// and the watcher would retract a fault the new task never had.
func TestADeactivatedTaskLeavesNoFaultForItsSuccessor(t *testing.T) {
	rig := newTaskRig(t)

	rig.set(TaskFault(TaskIssueTriggerNotRunning, "refused"))
	rig.sampleAt(0)
	k := reportKey{scope: scopeTask, id: taskID, condition: TaskIssueTriggerNotRunning}
	if !rig.reporter.holds(k) {
		t.Fatal("precondition: the fault was not recorded")
	}

	deactivate := string(request("DeactivateTaskRequest", `
    <ns1:xId>`+taskID+`</ns1:xId>`))
	if m := serve(t, rig.srv, deactivate); m.OK != ackOK {
		t.Fatalf("deactivate: %+v", m.ErrorInformation)
	}
	if rig.reporter.holds(k) {
		t.Error("the deactivated task's fault is still recorded")
	}

	// Its successor, healthy, under the same XID before the next sample.
	rig.set()
	rig.st.Activate(types.InterceptTask{XID: testXID, Targets: []types.TargetIdentifier{supiTarget("1")}})
	rig.sampleAt(30 * time.Second)
	rig.sampleAt(2 * time.Minute)
	if got := rig.admf.counting(allClear)[allClear]; got != 0 {
		t.Errorf("the successor task was sent %d AllClears for a fault it never had", got)
	}
}

// TestARecordNotProducedIsAnEvent: the condition an element pushes when the content a mandated
// record must carry was not available is task-scoped — TaskFault renders it rather than
// refusing it as undeclared — and an event, which the watcher neither samples nor retracts.
func TestARecordNotProducedIsAnEvent(t *testing.T) {
	f := TaskFault(TaskIssueRecordNotProduced, "a UE policy COMPLETE with no COMMAND held")
	if !strings.HasPrefix(f.ErrorDescription, TaskIssueRecordNotProduced+": ") {
		t.Fatalf("recordNotProduced is not a declared task-scoped condition: %q", f.ErrorDescription)
	}

	rig := newTaskRig(t)
	rig.set(f)
	rig.sampleAt(0)
	if n := len(rig.admf.reports()); n != 0 {
		t.Errorf("the watcher reported an event %d times", n)
	}

	rig.reporter.NotifyTaskAsync(taskID, TaskReportNonTerminatingFault, TaskIssueRecordNotProduced, "pushed")
	within(t, "the pushed event", func() bool { return len(rig.admf.reports()) == 1 })
	awaitSettled(t, rig.reporter, reportKey{scope: scopeTask, id: taskID, condition: TaskIssueRecordNotProduced})
	if got := rig.reporter.standingTaskFaults(); len(got) != 0 {
		t.Errorf("a task-scoped event is recorded as a standing fault: %v", got)
	}
}
