// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"errors"
	"fmt"
	"time"
)

// The clause 6.6.2 timer defaults: "by default TIME_P1 is 1 minute", "by default TIME_P2 is
// 1 hour", "by default TIME P3 is 2 hours".
const (
	DefaultKeepaliveTimeP1 = time.Minute
	DefaultKeepaliveTimeP2 = time.Hour
	DefaultKeepaliveTimeP3 = 2 * time.Hour
)

// MinKeepaliveWindow is the shortest value any of the procedure's timers may take.
//
// The watchdog samples at a quarter of the shortest timer, and integer division reaches
// zero: a timer under four nanoseconds would panic time.NewTicker on a goroutine and take
// the network function down. A second is far below any value a deployment would choose and
// far above the arithmetic's floor, which is what a guard against a configuration mistake
// should be.
const MinKeepaliveWindow = time.Second

// KeepaliveProcedure configures the keepalive procedure of TS 103 221-1 clause 6.6.2 on this
// element. A zero timer takes the clause default.
//
// The clause makes the procedure enabled by default — "It is for prior agreement to determine
// whether Keepalives are enabled or disabled. By default (with no prior agreement) they are
// enabled" — so a network function runs WatchKeepalive unless its configuration says the
// operator and the agency agreed otherwise.
type KeepaliveProcedure struct {
	// TimeP2 is how long the element waits without an X1 request from the ADMF before it
	// reports that keepalives have stopped. Default one hour.
	TimeP2 time.Duration
	// TimeP1 bounds the wait for the ADMF to answer that report. Default one minute.
	TimeP1 time.Duration
	// TimeP3 is how long after the ADMF acknowledged the report the element waits for
	// any X1 request before deactivating all tasks. Default two hours.
	TimeP3 time.Duration
	// RetainTasking configures the element "to not allow deactivation of all tasks as part
	// of the Keepalive procedure". The zero value allows it, which is the clause's
	// intent: the procedure exists to "remove all tasking information as a security
	// measure" when the ADMF is gone, "unless otherwise configured".
	RetainTasking bool
	// TriggeredOverLIT3 runs the procedure at a POI triggered over LI_T3, where the party
	// whose silence it watches is not the party its reports reach. TS 33.128 clause 5.2.6:
	// "For the purposes of realising LI_T3 between a CC-TF and a triggered CC-POI, the CC-TF
	// plays the role of the "ADMF" as defined in the ETSI TS 103 221-1 reference model …
	// and the triggered CC-POI plays the role of the "NE"." The triggering function accepts
	// no NE-initiated message, so the 9050 report, the FaultCleared and the 10000 Alert go
	// to the real ADMF for its information only — and the real ADMF answering OK says
	// nothing about whether the triggering function is still there.
	//
	// So in this mode no answer stands for the peer: timer P3 never runs, and when TIME_P1
	// expires the element deactivates all tasks (or, retaining tasking, restarts TIME_P2) —
	// the clause's "ReportNEIssue response is not received" case, which is the truth of an
	// LI_T3 peer. Without it, an ADMF that is alive keeps an orphaned trigger intercepting
	// for TIME_P3 after the triggering function that installed it has gone. TimeP3 must be
	// zero, since a configured value would be silently ignored.
	TriggeredOverLIT3 bool
}

// withDefaults returns p with each unset timer at its clause default.
func (p KeepaliveProcedure) withDefaults() KeepaliveProcedure {
	if p.TimeP1 == 0 {
		p.TimeP1 = DefaultKeepaliveTimeP1
	}
	if p.TimeP2 == 0 {
		p.TimeP2 = DefaultKeepaliveTimeP2
	}
	if p.TimeP3 == 0 {
		p.TimeP3 = DefaultKeepaliveTimeP3
	}

	return p
}

// Validate reports whether every timer, after defaults, is at least MinKeepaliveWindow.
// A network function checks its configured values with it and reports an unusable one to
// the ADMF; WatchKeepalive refuses to run with one.
func (p KeepaliveProcedure) Validate() error {
	if p.TriggeredOverLIT3 && p.TimeP3 != 0 {
		return errors.New("x1: keepalive TIME_P3 is never started at a POI triggered over LI_T3: " +
			"no answer from the ADMF stands for the triggering function, so tasking is " +
			"deactivated when TIME_P1 expires; leave TIME_P3 unset")
	}
	p = p.withDefaults()
	for _, t := range []struct {
		name string
		d    time.Duration
	}{{"TIME_P1", p.TimeP1}, {"TIME_P2", p.TimeP2}, {"TIME_P3", p.TimeP3}} {
		if t.d < MinKeepaliveWindow {
			return fmt.Errorf("x1: keepalive %s %s is shorter than %s", t.name, t.d, MinKeepaliveWindow)
		}
	}

	return nil
}

// KeepaliveReporter gives the keepalive procedure the reporter its clause 6.6.2 messages
// travel on: the FaultReport with code 9050 when keepalives stop, the FaultCleared when the
// ADMF is heard again, and the Alert with code 10000 after the procedure deactivates every
// task.
//
// Without one the procedure still runs. The fault report then cannot be answered, which is
// the clause's "ReportNEIssue response is not received" case: when TIME_P1 has passed the
// element deactivates all tasks, unless it retains tasking.
func KeepaliveReporter(r *Reporter) Option {
	return func(s *Server) { s.keepaliveReporter = r }
}

// keepalivePhase is where the clause 6.6.2 procedure stands.
type keepalivePhase int

const (
	// keepaliveListening: timer P2 is running from the last X1 request or reset.
	keepaliveListening keepalivePhase = iota
	// keepaliveAwaitingAnswer: P2 expired, the 9050 report went out, and timer P1 bounds
	// the wait for its answer.
	keepaliveAwaitingAnswer
	// keepaliveAcknowledged: the ADMF answered the report, and timer P3 bounds the wait
	// for any X1 request before tasking is deactivated.
	keepaliveAcknowledged
	// keepalivePurged: every task was deactivated. Nothing further happens until an X1
	// request arrives.
	keepalivePurged
)

// keepaliveState is the procedure's state, guarded by Server.mu.
type keepaliveState struct {
	phase keepalivePhase
	// reset is when timer P2 was last restarted without an X1 request having arrived:
	// after a report the ADMF did not act on, by an element retaining its tasking.
	reset time.Time
	// faultAt is when the current 9050 report was made; an X1 request after it ends the
	// procedure.
	faultAt time.Time
	// deadline is when timer P1 or P3, whichever is running, expires.
	deadline time.Time
}

// errNoKeepaliveReporter stands for the report that could not be sent because the element
// has nowhere to send it.
var errNoKeepaliveReporter = errors.New("x1: no reporter for the keepalive procedure")

// recordActivity notes an authenticated X1 request from the ADMF.
//
// It is what resets timer P2, and it ends the procedure wherever it stands: "if at any point
// in time the NE receives a X1 request (including Keepalive request) from the ADMF, unless
// otherwise configured, the NE shall clear the error by sending a ReportNEIssue request to
// the ADMF indicating 'FaultCleared'". The clearing report goes on its own goroutine because
// this runs on the X1 request goroutine, and only when a fault is owed, so an ordinary
// request costs a lock acquisition.
//
// A clear is owed for as long as the reporter holds the 9050 fault, not only on the first
// request after it: a FaultCleared that does not arrive leaves the fault recorded, so the
// next request sends it again, and the ADMF is not left holding a keepalive fault for an
// element it is talking to. One the ADMF refuses is settled, not resent (settleClear): the
// refusal is its answer, and asking again on every request would get the same one. The reporter holds the fault whenever the ADMF may — including a
// report whose answer was lost, see raise — and sends no clear for a fault it does not hold
// and none while one is already in flight, so asking again costs nothing when there is
// nothing to clear.
func (s *Server) recordActivity() {
	s.mu.Lock()
	s.lastSeen = s.now()
	s.heard++
	s.keepalive = keepaliveState{}
	reporter := s.keepaliveReporter
	s.mu.Unlock()

	if reporter.holds(reportKey{scope: scopeElement, condition: NEIssueKeepalivesMissing}) {
		go reporter.NotifyElementClear(NEIssueKeepalivesMissing)
	}
}

// WatchKeepalive runs the keepalive procedure of TS 103 221-1 clause 6.6.2 until stop is
// closed. It blocks — run it in a goroutine.
//
// When no X1 request has arrived from the ADMF for TIME_P2, the element reports a
// FaultReport with code 9050 "Keepalives not received" and starts timer P1. Then:
//
//   - any X1 request, at any point, ends the procedure: the element clears the fault with a
//     FaultCleared and returns to normal operation;
//   - an OK answer to the report within TIME_P1 starts timer P3, and when P3 expires with no
//     X1 request the element deactivates all tasks and sends an Alert with code 10000
//     "Database cleared";
//   - no answer, or an error answer, leaves P1 running, and its expiry deactivates all tasks,
//     followed by the same Alert. An error answer is not acted on before P1 expires: the
//     clause conditions deactivation on an error answer "and no X1 request was received while
//     timer P1 was running", which can only be known once P1 has run.
//
// An element configured with RetainTasking restarts P2 instead of deactivating anything,
// as the clause specifies; the fault stays reported until an X1 request clears it, and the
// 9050 report is repeated each time P2 expires again while the ADMF stays silent.
//
// An element with no KeepaliveReporter runs the same procedure: its report cannot be
// answered, so it deactivates when P1 expires, and the Alert has nowhere to go.
//
// Deactivation is the same purge DeactivateAllTasks performs, reported to OnPurge as
// PurgeKeepaliveLapse, and it is re-checked under the transition lock so that an X1 request
// arriving in the meantime — a returning ADMF's recovery activation — is never purged by a
// decision taken before it arrived.
//
// The timers are sampled at a quarter of the shortest of them, so each expiry is acted on
// within that quarter. A procedure whose timers are below MinKeepaliveWindow does not run;
// the network function checks its values with Validate first and reports an unusable one.
//
// A network function passes a nil stop channel, since the procedure must run for as long as
// the element can hold tasking; a nil channel never becomes ready.
func (s *Server) WatchKeepalive(p KeepaliveProcedure, stop <-chan struct{}) {
	if p.Validate() != nil {
		return
	}
	p = p.withDefaults()

	s.recordActivity() // seed, so a freshly started element does not report at once

	ticker := time.NewTicker(min(p.TimeP1, p.TimeP2, p.TimeP3) / 4)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.keepaliveStep(p)
		case <-stop:
			return
		}
	}
}

// keepaliveStep advances the procedure to the present. It is the watchdog's whole body,
// separate so the procedure can be driven on an injected clock.
func (s *Server) keepaliveStep(p KeepaliveProcedure) {
	s.mu.Lock()
	ka := s.keepalive
	now := s.now()
	s.mu.Unlock()

	switch ka.phase {
	case keepaliveListening:
		s.keepaliveListen(p, now)
	case keepaliveAwaitingAnswer, keepaliveAcknowledged:
		if now.Before(ka.deadline) {
			return
		}
		if ka.phase == keepaliveAwaitingAnswer && p.RetainTasking {
			// "If instead the NE is configured to not allow deactivation of all tasks …
			// the NE shall reset the timer P2 and continue normal operation."
			s.restartP2(ka.faultAt, now)

			return
		}
		s.keepaliveDeactivate(ka.faultAt)
	case keepalivePurged:
	}
}

// keepaliveListen is the procedure while timer P2 runs: when it has expired, the 9050 report
// goes out and the answer decides which timer runs next.
func (s *Server) keepaliveListen(p KeepaliveProcedure, now time.Time) {
	s.mu.Lock()
	since := s.lastSeen
	if s.keepalive.reset.After(since) {
		since = s.keepalive.reset
	}
	if now.Sub(since) <= p.TimeP2 {
		s.mu.Unlock()

		return
	}
	s.keepalive = keepaliveState{
		phase:    keepaliveAwaitingAnswer,
		faultAt:  now,
		deadline: now.Add(p.TimeP1),
	}
	reporter := s.keepaliveReporter
	s.mu.Unlock()

	err := errNoKeepaliveReporter
	if reporter != nil {
		err = reporter.raise(NEIssueKeepalivesMissing,
			"no X1 request has been received from the ADMF within TIME_P2")
	}

	s.mu.Lock()
	answered := s.now()
	if s.lastSeen.After(now) {
		// The ADMF was heard while the report was on the wire. recordActivity has ended the
		// procedure, but its clearing report may have found the fault report still in flight
		// and had nothing to clear; this one finds it settled.
		s.mu.Unlock()
		if reporter != nil {
			reporter.NotifyElementClear(NEIssueKeepalivesMissing)
		}

		return
	}
	// At a POI triggered over LI_T3 the answer came from the real ADMF, not from the
	// triggering function whose silence this is (see TriggeredOverLIT3), so it stops nothing.
	if err == nil && !p.TriggeredOverLIT3 && !answered.After(s.keepalive.deadline) {
		// "If a ReportNEIssue OK response is received from the ADMF before the expiry of
		// timer P1, the NE shall stop timer P1."
		if p.RetainTasking {
			s.keepalive.phase, s.keepalive.reset = keepaliveListening, answered
		} else {
			s.keepalive.phase, s.keepalive.deadline = keepaliveAcknowledged, answered.Add(p.TimeP3)
		}
	}
	// Otherwise P1 runs to its deadline: keepaliveStep acts on its expiry.
	s.mu.Unlock()
}

// restartP2 returns to normal operation without an X1 request having arrived, leaving the
// fault reported, for an element that retains its tasking. It does nothing if an X1 request
// has arrived since the report, which has already ended the procedure.
func (s *Server) restartP2(faultAt, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastSeen.After(faultAt) {
		return
	}
	s.keepalive.phase, s.keepalive.reset = keepaliveListening, now
}

// keepaliveDeactivate performs the procedure's DeactivateAllTasks and sends the Alert that
// follows it: "the NE shall perform a DeactivateAllTasks command, i.e. deactivate all XIDs on
// the NE, and should send a ReportNEIssue to the ADMF indicating 'Alert' with an issue code
// 10000 'Database cleared'".
//
// The Alert is best-effort, as the clause's "should" allows, and is sent once: it goes
// through Notify, which is throttled and does not retry, and an Alert has no ending for a
// later request to clear. An ADMF that misses it learns of the purge from what the element
// then holds — a GetAllDetails, or the 2020 its next DeactivateTask is answered with.
func (s *Server) keepaliveDeactivate(faultAt time.Time) {
	if !s.purgeIfSilentSince(faultAt) {
		return
	}

	s.mu.Lock()
	reporter := s.keepaliveReporter
	s.mu.Unlock()

	reporter.Notify(NEIssueTaskingPurged,
		"all tasking was deactivated: no X1 request was received from the ADMF")
}

// purgeIfSilentSince clears all tasking when no X1 request has arrived since t, and moves the
// procedure to keepalivePurged.
func (s *Server) purgeIfSilentSince(t time.Time) bool {
	return s.purgeIfSilent(func(lastSeen, _ time.Time) bool { return !lastSeen.After(t) })
}

// purgeIfSilent clears all tasking if silent holds of the last X1 request, and reports
// whether it did.
//
// It clears the store first, then runs the deactivation hook over the tasks that were
// present, so a POI re-evaluating against the now empty task set tears its product down —
// the SMF withdrawing UPF content triggers, for instance. Clearing the store alone is not a
// complete purge.
//
// **Re-checked under the transition lock, and that is not belt-and-braces.** The first test
// and the purge are separate steps, and an X1 request arriving between them is the recovery
// case: a returning ADMF's ActivateTask takes the transition lock, records activity, stores
// the task and acknowledges it — and a purge already past its own test would then remove the
// tasking it has just acknowledged. The decision and the purge are therefore one hold of the
// transition lock.
func (s *Server) purgeIfSilent(silent func(lastSeen, now time.Time) bool) bool {
	if !s.silent(silent) {
		return false
	}

	// nil in production. The window between deciding the ADMF has gone quiet and acting on
	// it is where a returning ADMF's recovery activation lands — see
	// TestAnActivationInTheLapseWindowSurvives.
	if s.afterLapseDecision != nil {
		s.afterLapseDecision()
	}

	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	s.mu.Lock()
	heard := s.heard
	decided := silent(s.lastSeen, s.now())
	s.mu.Unlock()
	if !decided {
		return false
	}
	s.purgeAllTaskingLocked(PurgeKeepaliveLapse)

	// The procedure ends here only if the ADMF stayed silent while the purge ran. A request
	// in that interval has already reset the procedure, and recordActivity takes only mu, so
	// it does not wait for the purge; marking the procedure purged over that reset would stop
	// it for good, leaving whatever the ADMF tasks next held with no fail-safe behind it.
	s.mu.Lock()
	if s.heard == heard {
		s.keepalive.phase = keepalivePurged
	}
	s.mu.Unlock()

	return true
}

// silent evaluates a silence test against the last X1 request, under mu.
func (s *Server) silent(test func(lastSeen, now time.Time) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return test(s.lastSeen, s.now())
}
