// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"sync"
	"time"

	"github.com/omec-project/util/li/types"
)

// WatchTaskFaults reports both edges of the task-scoped conditions this element can
// re-observe, until stop is closed. It blocks — run it in a goroutine.
//
// TS 103 221-1 clause 6.5.2.1: "If a non-terminating fault is cleared, the NE shall send
// another ReportTaskIssue indicating the fault is cleared." A clearing report needs an owner
// that can see the condition end, and the conditions that can end are the ones the
// WithTaskFaults supplier answers from state — so the watcher samples that supplier for every
// task this element holds, reports a NonTerminatingFault when a state condition appears, and
// an AllClear once it has stayed absent for an interval. The same hold the destination
// watcher applies keeps a condition that flaps from producing a report and a retraction per
// sample.
//
// A fault reported against a task this element no longer holds is forgotten rather than
// retracted: the task's end ends it, and the ADMF has already deactivated the XID an AllClear
// would name.
//
// Events — a record that could not be encoded, for instance — are not the watcher's: they are
// pushed when they happen (NotifyTaskAsync) and have no ending. Without a WithTaskFaults
// supplier there is nothing to sample, and this returns at once. A non-positive interval
// takes the report throttle's.
func (s *Server) WatchTaskFaults(r *Reporter, interval time.Duration, stop <-chan struct{}) {
	if s.taskFaults == nil {
		return
	}
	w := newTaskWatch(s, r, interval)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w.sampleUntil(stop)
		}
	}
}

// taskWatch is WatchTaskFaults' state.
type taskWatch struct {
	srv      *Server
	reporter *Reporter
	interval time.Duration
	// now is the clock the hold is measured on. Monotonic; replaced only by tests.
	now func() time.Time

	// mu guards healthy. It is never held across a report.
	mu sync.Mutex
	// healthy is when each reported fault was first seen absent again; a fault is retracted
	// once it has stayed absent for an interval.
	healthy map[reportKey]time.Time
}

func newTaskWatch(s *Server, r *Reporter, interval time.Duration) *taskWatch {
	if interval <= 0 {
		interval = reportThrottle
	}
	s.mu.Lock()
	s.taskFaultReporter = r
	s.mu.Unlock()

	return &taskWatch{
		srv:      s,
		reporter: r,
		interval: interval,
		now:      monotonicNow,
		healthy:  make(map[reportKey]time.Time),
	}
}

// taskReport is one report a sample decided on.
type taskReport struct {
	key     reportKey
	details string
	clear   bool
	// task is the task the fault was observed on, as the sample saw it.
	task types.InterceptTask
}

// sampleUntil reports the transitions since the last look, making no further report once stop
// is closed.
func (w *taskWatch) sampleUntil(stop <-chan struct{}) {
	w.report(w.plan(w.now()), stop)
}

// report makes a sample's planned reports.
//
// **A fault is reported only if the task it was observed on is still held when the report is
// sent.** The plan is made from a snapshot, and the reports are synchronous round trips, so a
// task can be deactivated — and its XID activated again for a different task — between the two.
// The server forgets a removed task's records as it removes it, but a report not yet begun has
// no record to forget: sent anyway, it reports a fault about a deactivated XID, and its record
// is inherited by the new task, whose fault the watcher would then retract although the ADMF
// was never told of one. So the reporter asks after reserving the key — which is what makes a
// removal after that point mark the send as forgotten — and sends nothing if the task is gone
// or is no longer the one the fault was seen on.
//
// A retraction needs no such check: one for a removed task finds its record already forgotten.
func (w *taskWatch) report(plan []taskReport, stop <-chan struct{}) {
	for _, p := range plan {
		select {
		case <-stop:
			return
		default:
		}
		if p.clear {
			w.reporter.NotifyTaskClear(p.key.id, p.key.condition)
			w.settled(p.key)

			continue
		}
		w.reporter.notifyTaskFaultWhile(p.key.id, p.key.condition, p.details, func() bool {
			held, ok := w.srv.store.Get(p.task.XID)

			return ok && sameTask(held, p.task)
		})
	}
}

// plan decides what this sample reports.
func (w *taskWatch) plan(now time.Time) []taskReport {
	held := make(map[string]bool)
	present := make(map[reportKey]string)

	// The supplier is called without any lock of the watcher's or the server's, as
	// WithTaskFaults documents.
	tasks := make(map[string]types.InterceptTask)
	for _, t := range w.srv.store.Snapshot() {
		held[string(t.XID)] = true
		tasks[string(t.XID)] = t
		for _, f := range w.srv.taskFaults(t.XID) {
			condition, details, ok := strings.Cut(f.ErrorDescription, ": ")
			if !ok || !taskStates[condition] {
				continue
			}
			present[reportKey{scope: scopeTask, id: string(t.XID), condition: condition}] = details
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	var plan []taskReport
	for k, details := range present {
		delete(w.healthy, k)
		plan = append(plan, taskReport{key: k, details: details, task: tasks[k.id]})
	}

	standing := make(map[reportKey]bool)
	for _, k := range w.reporter.standingTaskFaults() {
		standing[k] = true
		if !held[k.id] {
			w.reporter.forget(k)
			delete(w.healthy, k)

			continue
		}
		if _, still := present[k]; still {
			continue
		}
		since, ok := w.healthy[k]
		if !ok {
			w.healthy[k] = now

			continue
		}
		if now.Sub(since) >= w.interval {
			plan = append(plan, taskReport{key: k, clear: true})
		}
	}
	for k := range w.healthy {
		if !standing[k] {
			delete(w.healthy, k)
		}
	}

	return plan
}

// settled forgets the hold of a fault whose retraction arrived. One that did not arrive keeps
// its hold, so the next sample asks again.
func (w *taskWatch) settled(k reportKey) {
	if w.reporter.holds(k) {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.healthy, k)
}
