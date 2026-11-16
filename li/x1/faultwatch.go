// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"slices"
	"sync"
	"time"

	"github.com/omec-project/util/li/types"
)

// DestinationHealth is one delivery destination's identity and whether this element
// can currently reach it.
//
// It is the watcher's supplier shape, and it is deliberately *not* the status
// probe's. MDFUnreachableProbe takes counts so that a status answer cannot name a
// destination — an element's own status says how much is wrong, never whose product
// is affected. A destination-scoped report says which, because the provisioning
// function asked about none in particular and that is exactly what it needs to know.
// Same fact, two questions, two shapes.
type DestinationHealth struct {
	// DID is the identifier the provisioning function assigned. An endpoint this
	// element resolved from its own configuration has none, and is not reportable at
	// destination scope because the ADMF has nothing to act on.
	DID string
	// Address is the endpoint the identifier resolves to. Two identifiers may share
	// one, which is why a fault is reported per identifier and not per address.
	Address     string
	Unreachable bool
}

// DestinationHealthOf joins the destinations a set of tasks delivers to with a
// reachability answer per address.
//
// It exists so the join is written once rather than in each network function. The
// suppliers differ — a Pool for the SMF and AMF, its own sender map for the UPF —
// but both answer the same question about an address, so what varies is one
// function argument.
//
// destinations SHALL be the same resolution the element's delivery path uses, and
// is passed in rather than taken as t.DeliveryAddresses(dt). An IRI-POI delivers a
// task naming no DID to its own configured endpoint, which provisioning never
// resolved — so the task carries no delivery record at all, and a join reading the
// task alone would see nothing to watch and report nothing about the commonest
// configuration there is. Asking the caller for the addresses it actually delivers to
// makes the two impossible to disagree.
//
// Deduplicated by (identifier, address): a destination named by several tasks is one
// destination, and reporting it once per task would tell the provisioning function
// about its own tasking rather than about its endpoint.
func DestinationHealthOf(tasks []types.InterceptTask, dt types.DeliveryType,
	destinations func(types.InterceptTask) []string,
	unreachable func(addr string) bool,
) []DestinationHealth {
	var out []DestinationHealth

	for _, t := range tasks {
		for _, addr := range destinations(t) {
			dids := t.DeliveryDIDs(dt, addr)
			if len(dids) == 0 {
				// An address this element delivers to that provisioning did not name:
				// its own configured endpoint. There is nothing destination-scoped to
				// say about it, and still something to say — reported at element scope
				// by the empty identifier, which is what the watcher keys the scope on.
				dids = []string{""}
			}
			for _, did := range dids {
				if slices.ContainsFunc(out, func(h DestinationHealth) bool {
					return h.DID == did && h.Address == addr
				}) {
					continue
				}
				out = append(out, DestinationHealth{
					DID: did, Address: addr, Unreachable: unreachable(addr),
				})
			}
		}
	}

	return out
}

// minNudgeInterval is the least time between two samples a Nudge asks for. A delivery path
// that fails at packet rate nudges at packet rate, and a sample costs a walk of the tasking
// and, while the ADMF is refusing, a report attempt per unreachable destination — so nudges
// arriving faster than this are coalesced into one sample at the end of the interval.
const minNudgeInterval = time.Second

// DestinationWatcher reports both edges of a delivery destination's reachability:
// that it has become unreachable, and that it has recovered.
//
// **One owner for both edges, and that is the point.** Reachability is re-observable
// — every supplier answers from state its senders already hold — so an ending is
// detectable, which is what makes a clearing report possible at all. Nothing in the
// delivery layer signals recovery, and a callback beside the failure one would put edge
// detection at every delivery site across three network functions, each free to disagree
// about what "recovered" means. An element where one party announces and another retracts
// eventually announces something nobody retracts.
//
// What it deliberately does not cover: a fault the element cannot re-observe. A
// destination that could not be *prepared* is a credential or configuration fault
// rather than a reachability one, and a fault that cannot be observed to hold cannot
// be observed to end. Those are reported where they are noticed, at element scope, and
// are never cleared — the event-versus-condition rule applied rather than a second
// mechanism.
type DestinationWatcher struct {
	health   func() []DestinationHealth
	reporter *Reporter
	interval time.Duration
	// condition is the issue name a fault is reported and retracted under, so the
	// two halves cannot drift apart.
	condition string
	// nudge lets a site that has just seen a delivery fail ask for a sample now
	// rather than at the next tick. Buffered and never blocking: it is called from
	// the delivery path, which must not wait on reporting.
	nudge chan struct{}
	// nudgeGap is minNudgeInterval, held per watcher so a test can shorten it.
	nudgeGap time.Duration
	// now is the clock the hold below is measured on. Monotonic; replaced only by tests.
	now func() time.Time

	// mu guards the record below. It is held while a sample decides what to report and
	// again while it records what the reporter established, and never across a report:
	// those are synchronous round trips to the ADMF.
	mu sync.Mutex
	// raised is every destination whose fault this watcher has reported and not yet seen
	// retracted, with the instant it was first seen healthy again — zero while it is not.
	//
	// **It is what lets a fault end when its destination stops being sampled.** The
	// supplier lists the destinations the live tasking refers to, so deactivating a
	// destination's last task, or a purge, takes it out of every later sample. A fault
	// retracted only when a sample shows it healthy would then stand at the ADMF for as
	// long as nothing referred to the destination. Clause 5.3: "The NE shall also
	// indicate that a fault has been cleared."
	//
	// And it is the hold: a destination that recovers is retracted only once it has stayed
	// healthy for an interval. Reachability is the last exchange's outcome, with no memory,
	// and each failed delivery nudges a sample — so without the hold a destination failing
	// every other send would report a fault and its retraction per failure, as fast as the
	// ADMF answered. A fault is still reported at once; only its ending waits.
	raised map[string]time.Time
	// elementRaised and elementHealthy are the same record for the element-scope fault
	// that stands for every destination with no provisioned identifier.
	elementRaised  bool
	elementHealthy time.Time
}

// Nudge asks for a sample at the next opportunity.
//
// Delivery sites call it when a delivery fails, so the ADMF learns of a failed
// destination promptly rather than up to an interval later. The watcher still decides
// whether anything is a transition — one owner for both edges.
//
// Never blocks and never fails. A nudge that arrives while one is already pending is
// dropped, because two nudges and one imply the same work, and nudge-driven samples are at
// least minNudgeInterval apart.
func (w *DestinationWatcher) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// NewDestinationWatcher returns a watcher over the destinations health reports.
//
// health must not perform I/O: it is called on a timer, and a supplier that grew a
// network call would put delivery latency on a schedule inside the LI plane. Every
// supplier this package is used with answers from state each sender already holds,
// which is the same property MDFUnreachableProbe documents for its own.
//
// One watcher per Reporter. The watcher keeps its own record of what it raised, and the
// reporter's is keyed by condition and scope alone, so a second watcher on the same reporter
// shares every key with the first: each retracts the element-scope fault the other raised, and
// each reads the other's report in flight as its own. Several delivery paths feed one watcher
// through one health supplier instead.
func NewDestinationWatcher(health func() []DestinationHealth, reporter *Reporter, interval time.Duration) *DestinationWatcher {
	if interval <= 0 {
		interval = reportThrottle
	}

	return &DestinationWatcher{
		health:    health,
		reporter:  reporter,
		interval:  interval,
		condition: NEIssueMDFUnreachable,
		nudge:     make(chan struct{}, 1),
		nudgeGap:  minNudgeInterval,
		now:       monotonicNow,
		raised:    make(map[string]time.Time),
	}
}

// Watch samples until stop is closed. A nil stop channel runs for the life of the
// process, which is the right lifetime for an element that can hold tasking.
//
// A sample in progress when stop closes makes no further reports, so Watch returns once
// the report already on the wire has been answered — within x1RequestTimeout.
func (w *DestinationWatcher) Watch(stop <-chan struct{}) {
	if w == nil || w.health == nil {
		return
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	var (
		last     time.Time
		deferred *time.Timer
	)
	// deferredC is the deferred nudge's channel, nil while none is pending.
	deferredC := func() <-chan time.Time {
		if deferred == nil {
			return nil
		}

		return deferred.C
	}
	// sample takes a sample, which answers any deferred nudge too: one pending past a
	// sample would sample again sooner than the gap allows.
	sample := func() {
		if deferred != nil {
			deferred.Stop()
			deferred = nil
		}
		last = w.now()
		w.sampleUntil(stop)
	}

	for {
		select {
		case <-stop:
			if deferred != nil {
				deferred.Stop()
			}

			return
		case <-ticker.C:
			sample()
		case <-deferredC():
			deferred = nil
			sample()
		case <-w.nudge:
			// A site has just seen a destination fail. Sampling now keeps the report
			// prompt; the decision about whether this is a transition still belongs here.
			// A nudge inside the gap is deferred to its end rather than dropped, so the
			// failure that prompted it is still looked at.
			if wait := w.nudgeGap - w.now().Sub(last); wait > 0 {
				if deferred == nil {
					deferred = time.NewTimer(wait)
				}

				continue
			}
			sample()
		}
	}
}

// watchReport is one report a sample decided on, made once w.mu is released.
type watchReport struct {
	kind watchReportKind
	did  string // empty at element scope
}

type watchReportKind int

const (
	reportFault   watchReportKind = iota // report a fault
	reportClear                          // ask for a retraction the reporter answers only if one is owed
	reportRetract                        // retract a fault this watcher raised, and forget it once retracted
)

// sampleUntil reports the transitions since the last look, making no further report once
// stop is closed.
//
// Both directions go through the reporter's own record of what it has told the
// provisioning function, so a destination that is unreachable at every sample is
// reported once for the outage rather than once per sample or per throttle window, and one
// that recovers without ever having been reported produces nothing.
func (w *DestinationWatcher) sampleUntil(stop <-chan struct{}) {
	plan := w.plan(w.now())

	made := plan[:0:0]
	for _, p := range plan {
		select {
		case <-stop:
			w.record(made)

			return
		default:
		}
		w.send(p)
		made = append(made, p)
	}

	w.record(made)
}

// plan decides what this sample reports, under w.mu. It records the faults it is about to
// report and the instants destinations were first seen healthy; what a retraction
// established is recorded afterwards, by record.
func (w *DestinationWatcher) plan(now time.Time) []watchReport {
	w.mu.Lock()
	defer w.mu.Unlock()

	// **The destinations with no provisioned identifier are one element state, not
	// several**, and that is a property of the scope rather than a convenience.
	//
	// An element-scoped report names no destination by construction — MDFUnreachableProbe
	// takes counts precisely so the answer *cannot* name one — so reporting each of them
	// separately would send the ADMF several reports carrying identical text about
	// endpoints it cannot tell apart, and each one's retraction would retract the others:
	// with one such endpoint down and one healthy, whichever the iteration reached last
	// would decide whether the fault stood.
	//
	// What the element can honestly say at this scope is the aggregate: a fault while any
	// un-identified destination is unreachable, and a clear only when none is.
	var (
		plan        []watchReport
		unnamed     bool
		unnamedDown bool
		sampled     = make(map[string]bool)
	)

	for _, h := range w.health() {
		// An endpoint this element resolved from its own configuration has no
		// identifier the provisioning function assigned, so there is nothing
		// *destination-scoped* to say about it — but there is still something to
		// say. It is a delivery this element cannot make, which is what the
		// network-element-level condition is for, and saying nothing would leave an
		// undeliverable endpoint unreported.
		//
		// Both edges either way. The scope changes with what can be named; whether
		// the fault is reported at all does not.
		if h.DID == "" {
			unnamed = true
			unnamedDown = unnamedDown || h.Unreachable

			continue
		}
		sampled[h.DID] = true

		if h.Unreachable {
			plan = append(plan, watchReport{kind: reportFault, did: h.DID})
			w.raised[h.DID] = time.Time{}

			continue
		}

		healthySince, ours := w.raised[h.DID]
		if !ours {
			// Not a fault this watcher raised: a retraction is still asked for, and the
			// reporter answers it only if something reported one.
			plan = append(plan, watchReport{kind: reportClear, did: h.DID})

			continue
		}
		if healthySince.IsZero() {
			w.raised[h.DID] = now

			continue
		}
		if now.Sub(healthySince) >= w.interval {
			plan = append(plan, watchReport{kind: reportRetract, did: h.DID})
		}
	}

	// A destination that has left the health set is no longer tasked, so there is no
	// recovery to wait for: its fault is retracted now.
	for did := range w.raised {
		if !sampled[did] {
			plan = append(plan, watchReport{kind: reportRetract, did: did})
		}
	}

	return append(plan, w.planElementLocked(now, unnamed, unnamedDown)...)
}

// planElementLocked is plan's element-scope half, for the destinations with no provisioned
// identifier. w.mu must be held.
func (w *DestinationWatcher) planElementLocked(now time.Time, unnamed, unnamedDown bool) []watchReport {
	if unnamedDown {
		w.elementRaised, w.elementHealthy = true, time.Time{}

		return []watchReport{{kind: reportFault}}
	}

	if !w.elementRaised {
		// Only where this element has such a destination at all: an element with none has
		// nothing to say at this scope, and saying "all clear" would retract a fault raised
		// by whatever else uses the same condition.
		if unnamed {
			return []watchReport{{kind: reportClear}}
		}

		return nil
	}

	// This watcher's own fault. While un-identified destinations remain, it is retracted
	// once they have stayed healthy for an interval; when none remains, at once, because
	// there is nothing left to recover.
	if unnamed {
		if w.elementHealthy.IsZero() {
			w.elementHealthy = now

			return nil
		}
		if now.Sub(w.elementHealthy) < w.interval {
			return nil
		}
	}

	return []watchReport{{kind: reportRetract}}
}

// send makes one planned report. It holds no lock of the watcher's: a report is a
// synchronous round trip to the ADMF.
func (w *DestinationWatcher) send(p watchReport) {
	switch {
	case p.did == "" && p.kind == reportFault:
		w.reporter.NotifyElementFault(w.condition, "a delivery destination is unreachable")
	case p.did == "":
		w.reporter.NotifyElementClear(w.condition)
	case p.kind == reportFault:
		w.reporter.NotifyDestinationFault(p.did, w.condition, "delivery destination is unreachable")
	default:
		w.reporter.NotifyDestinationClear(p.did, w.condition)
	}
}

// record forgets the faults whose retraction the reporter no longer holds, under w.mu. A
// retraction that did not arrive keeps the entry, so the next sample asks again.
func (w *DestinationWatcher) record(made []watchReport) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, p := range made {
		if p.kind != reportRetract {
			continue
		}
		if p.did == "" {
			if !w.reporter.holds(reportKey{scope: scopeElement, condition: w.condition}) {
				w.elementRaised, w.elementHealthy = false, time.Time{}
			}

			continue
		}
		if !w.reporter.holds(reportKey{scope: scopeDestination, id: p.did, condition: w.condition}) {
			delete(w.raised, p.did)
		}
	}
}
