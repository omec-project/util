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

// TestAnActivationRacingTheFailSafePurgeLeavesNoOrphan drives the interleaving the
// transition lock exists for, rather than reasoning about it.
//
// A purge reads the tasks it holds and then empties the store. A task activated in
// between is removed from the store without its teardown ever running, so three things
// are true at once: the ADMF has been told the task is active, the element holds
// nothing, and the POI is still applying it. At a triggering function that is content
// interception surviving a purge meant to stop everything — kept alive by the
// keepalives the forgotten trigger goes on earning, and ended only by session release.
//
// The invariant asserted is the one that admits both legal outcomes and excludes the
// orphan: either the store holds the task and the POI was told to apply it, or the
// store holds nothing and the POI was told it was gone. Never a store holding nothing
// while the POI still applies it.
func TestAnActivationRacingTheFailSafePurgeLeavesNoOrphan(t *testing.T) {
	st := store.New()

	var (
		mu      sync.Mutex
		applied bool // what the POI was last told: applied, or removed
		told    bool
	)
	srv := NewServer(st, "neID", OnTaskChange(func(_, next *types.InterceptTask) {
		mu.Lock()
		defer mu.Unlock()
		told = true
		applied = next != nil
	}))

	// The purge is held open between its snapshot and its clear, because that window
	// is a few instructions wide: racing two goroutines at it passes against the
	// defect, which is how this property would have been "verified" without ever
	// reaching the interleaving.
	inWindow := make(chan struct{}, 1)
	proceed := make(chan struct{}, 1)
	srv.afterPurgeSnapshot = func() { inWindow <- struct{}{}; <-proceed }

	// Lapsed by construction: any idle time exceeds a zero window. Driven through
	// purgeIfLapsed so the path under test is the one the watchdog takes.
	srv.mu.Lock()
	srv.lastSeen = srv.now().Add(-time.Hour)
	srv.mu.Unlock()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.purgeIfLapsed(0)
	}()

	<-inWindow // the purge has read what it will tear down and has not cleared yet

	peer := admfPeer(t) // built here: t.Fatal is not valid off the test goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		//nolint:errcheck // the acknowledgement is not what this asserts on
		_, _ = srv.Process([]byte(activateXML), peer)
	}()

	// Long enough for the activation to land if nothing stops it. Under the transition
	// lock it does not land here at all -- it waits, and runs after the purge. A slow
	// machine can only make this pass when it should not, never fail it.
	time.Sleep(100 * time.Millisecond)
	proceed <- struct{}{}
	wg.Wait()

	_, held := st.Get(testXID)

	mu.Lock()
	lastApplied, wasTold := applied, told
	mu.Unlock()

	switch {
	case held && (!wasTold || !lastApplied):
		t.Fatalf("the element holds the task and the POI was not told to apply it "+
			"(told=%v applied=%v)", wasTold, lastApplied)
	case !held && wasTold && lastApplied:
		t.Fatal("the store holds nothing and the POI is still applying the task — content " +
			"interception that survived a purge meant to stop everything, with the ADMF " +
			"acknowledged and nothing left to withdraw it")
	}
}

// TestConcurrentOperationsOnOneXIDEndConsistent is the same property for the ordinary
// case: two operations naming one task, both acknowledged, and a POI whose last
// instruction has to describe the tasking the element actually ended up with.
//
// **What this is evidence of, and what it is not.** It runs the two operations against
// each other many times and holds under -race, which is worth having. It is not
// mutation evidence: with the lock removed it still passes, because the interleaving
// that breaks the invariant is a few instructions wide and racing goroutines do not
// reliably land in it. The deterministic evidence for the lock is
// TestAnActivationRacingTheFailSafePurgeLeavesNoOrphan, which holds the window open.
func TestConcurrentOperationsOnOneXIDEndConsistent(t *testing.T) {
	const rounds = 100

	deactivateXML := strings.Replace(
		strings.Replace(activateXML, "ActivateTaskRequest", "DeactivateTaskRequest", 1),
		"ActivateTaskRequest", "DeactivateTaskRequest", 1)

	for round := range rounds {
		st := store.New()

		var (
			mu      sync.Mutex
			applied bool
			told    bool
		)
		srv := NewServer(st, "neID", OnTaskChange(func(_, next *types.InterceptTask) {
			mu.Lock()
			defer mu.Unlock()
			told = true
			applied = next != nil
		}))

		if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
			t.Fatalf("seeding the task: %v", err)
		}

		var wg sync.WaitGroup

		peer := admfPeer(t) // built here: t.Fatal is not valid off the test goroutine
		wg.Add(2)
		go func() {
			defer wg.Done()
			//nolint:errcheck // acknowledgement not asserted on
			_, _ = srv.Process([]byte(activateXML), peer)
		}()
		go func() {
			defer wg.Done()
			//nolint:errcheck // acknowledgement not asserted on
			_, _ = srv.Process([]byte(deactivateXML), peer)
		}()
		wg.Wait()

		_, held := st.Get(testXID)

		mu.Lock()
		lastApplied, wasTold := applied, told
		mu.Unlock()

		if !wasTold {
			continue
		}
		if held != lastApplied {
			t.Fatalf("round %d: store holds=%v but the POI was last told applied=%v; "+
				"the notification does not describe the tasking the element holds", round, held, lastApplied)
		}
	}
}

// TestAProbeIsReachableFromInsideACallback pins the risk the transition lock
// introduces, from the side that can actually be asserted.
//
// The POI callbacks run under the lock, so anything a callback calls that also takes it
// deadlocks the provisioning interface outright. Two things follow, and only the second
// is testable here. A callback must not call a Server *transition* method — no callback
// does, they reach the POI's own state — and that would hang rather than fail, which is
// why it is stated in transitionMu's comment rather than asserted. But the fault probes
// and recordActivity must stay reachable while a transition is in flight, because they
// are documented as safe to call from the X1 request goroutine and a GetNEStatus that
// queued behind a DeactivateTask's session walk would break that contract. This drives
// exactly that: both are called from inside a callback, with the transition lock held.
//
// It is the kind of property that holds until somebody adds a convenient-looking lock.
func TestAProbeIsReachableFromInsideACallback(t *testing.T) {
	st := store.New()

	reached := make(chan struct{}, 1)

	var srv *Server

	srv = testServer(st,
		WithFaultProbes(func() *X1Error { return nil }),
		OnTaskChange(func(_, _ *types.InterceptTask) {
			// Under the transition lock, on the same server. Both of these take mu and
			// must not take transitionMu.
			srv.recordActivity()
			_ = srv.unresolvedFaults()
			reached <- struct{}{}
		}))

	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("activate: %v", err)
	}

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("a fault probe or recordActivity could not run while a transition was in " +
			"flight; they are behind the transition lock and an interrogation now queues " +
			"behind a POI callback")
	}
}

// TestAnAcknowledgedActivationIsNeverAnEmptyTask is the other scenario the transition
// lock owes: an element that answers "activated" holds that task, and the point of
// interception was notified with it rather than with an empty one.
//
// The two halves of that are one mechanism. activate re-reads the store to answer with
// the task as this element holds it; were the found bool discarded, a removal landing in
// between would produce the *zero* InterceptTask, with no XID and no targets, handed to
// the POI as a successful activation and acknowledged to the ADMF. Honouring found
// closes that; the lock makes it unreachable.
//
// **What this test can and cannot fail against.** With the lock in place a concurrent
// removal cannot land between the store write and the read, so discarding found alone is
// unobservable — this pins the *pair*, and it fails if either the lock or the guard goes.
// It is driven through the purge window rather than by racing goroutines, for the reason
// the sibling test records: the window is a few instructions wide.
func TestAnAcknowledgedActivationIsNeverAnEmptyTask(t *testing.T) {
	st := store.New()

	var (
		mu      sync.Mutex
		applied []types.InterceptTask
	)
	srv := NewServer(st, "neID", OnTaskChange(func(_, next *types.InterceptTask) {
		if next == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, *next)
	}))

	inWindow := make(chan struct{}, 1)
	proceed := make(chan struct{}, 1)
	srv.afterPurgeSnapshot = func() { inWindow <- struct{}{}; <-proceed }

	srv.mu.Lock()
	srv.lastSeen = srv.now().Add(-time.Hour)
	srv.mu.Unlock()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.purgeIfLapsed(0)
	}()

	<-inWindow

	var (
		acknowledged bool
		ackMu        sync.Mutex
	)

	peer := admfPeer(t) // built here: t.Fatal is not valid off the test goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := srv.Process([]byte(activateXML), peer)
		if err != nil || len(resp.Messages) == 0 {
			return
		}
		ackMu.Lock()
		acknowledged = resp.Messages[0].OK == ackOK
		ackMu.Unlock()
	}()

	// As above: a slow machine can only make this pass when it should not.
	time.Sleep(100 * time.Millisecond)
	proceed <- struct{}{}
	wg.Wait()

	mu.Lock()
	told := append([]types.InterceptTask(nil), applied...)
	mu.Unlock()

	for i, task := range told {
		if task.XID == "" {
			t.Errorf("instruction %d handed the point of interception a task with no identifier: "+
				"it has been told to apply nothing and this element called that a success", i)
		}
		if len(task.Targets) == 0 {
			t.Errorf("instruction %d handed the point of interception a task with no targets", i)
		}
	}

	ackMu.Lock()
	ack := acknowledged
	ackMu.Unlock()

	// And an acknowledgement means the element holds it.
	if ack {
		if _, held := st.Get(testXID); !held {
			t.Error("the activation was acknowledged and the element holds no such task")
		}
		if len(told) == 0 {
			t.Error("the activation was acknowledged and the point of interception was never told to apply it")
		}
	}
}

// TestACreateRacingAnActivationResolvesOneWayOrTheOther is the destination-registry half
// of the atomicity the transition lock provides for tasks.
//
// createDestination's two refusals are both decided from *task* state: whether the DID is
// already provisioned, and whether a task references it while configuration declares it.
// The reference check ran outside the transition lock, so an activation naming the DID
// could land between the question and the answer — and the element would then create a
// destination under a configured identifier a live task depends on, while every task
// activated before that moment kept delivering to the configured address. A provisioning
// function could read the new destination back from an element still sending a live
// warrant's product to the old one.
//
// Driven through the deterministic hold rather than by racing goroutines: the window is a
// few instructions wide, and a property this consequential asserted by hope is one that
// passes against the defect.
func TestACreateRacingAnActivationResolvesOneWayOrTheOther(t *testing.T) {
	st := store.New()

	// The DID is declared in configuration and is what activateXML's task names, so the
	// "declared and referenced" refusal is exactly the guard in question.
	srv := testServer(st)

	inWindow := make(chan struct{}, 1)
	proceed := make(chan struct{}, 1)
	srv.afterDestinationGuard = func() { inWindow <- struct{}{}; <-proceed }

	var wg sync.WaitGroup

	created := make(chan X1ResponseMessage, 1)

	peer := admfPeer(t) // built here: t.Fatal is not valid off the test goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		// X2andX3, as the configured entry is: activateXML orders both, and a destination
		// serving one would have the activation refused for that instead.
		resp, err := srv.Process([]byte(createDestinationXML(testDIDInActivateXML, deliveryX2andX3,
			"10.0.60.199", tcpPort("42069"), "")), peer)
		if err != nil {
			t.Errorf("create: %v", err)

			return
		}
		created <- resp.Messages[0]
	}()

	<-inWindow // the guards are decided and nothing is stored yet

	activated := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		//nolint:errcheck // the acknowledgement is not what this asserts on
		_, _ = srv.Process([]byte(activateXML), peer)
		close(activated)
	}()

	// Long enough for the activation to land if nothing stops it. Under the transition
	// lock it does not land here at all -- it waits, and runs after the create. A slow
	// machine can only make this pass when it should not, never fail it.
	select {
	case <-activated:
		proceed <- struct{}{}
		wg.Wait()
		t.Fatal("an activation naming the DID completed while a create for it was between its " +
			"reference check and its mutation: the element can now hold a destination created " +
			"under a configured identifier that a live task is already delivering past")
	case <-time.After(100 * time.Millisecond):
	}

	proceed <- struct{}{}
	wg.Wait()

	// One of the two orders, and only one. Either the create won — no task referenced the
	// DID, so it is stored and the activation then resolves to it — or the activation won
	// and the create is refused for the reason the specification gives. What must not
	// happen is the element holding a created destination *and* a task that resolved to the
	// configured one.
	m := <-created
	_, held := st.Get(testXID)
	if !held {
		t.Fatal("the activation did not complete after the create released")
	}
	task, _ := st.Get(testXID)
	addrs := task.DeliveryAddresses(types.DeliveryX2)
	if m.ErrorInformation == nil {
		// The create won: the task must resolve to what the element now holds.
		if len(addrs) != 1 || addrs[0] != "10.0.60.199:42069" {
			t.Errorf("the create was acknowledged and the task delivers to %v, want the created "+
				"destination: the element answers with one address and delivers to another", addrs)
		}
	} else {
		// The activation won: the task keeps the configured address and the create is
		// refused rather than silently redirecting it.
		if len(addrs) != 1 || addrs[0] != "10.0.60.122:42069" {
			t.Errorf("the create was refused and the task delivers to %v, want the configured "+
				"destination", addrs)
		}
	}
}

// TestAnActivationInTheLapseWindowSurvives is the fail-safe's own version of the same
// question, and the direction it fails in is the one that matters most.
//
// The lapse decision read lastSeen and only then blocked on the transition lock. So a
// returning ADMF's recovery ActivateTask could take that lock first, record activity, store
// the task and be acknowledged — and the tick, already past its own test, would purge the
// tasking it had just acknowledged. The ADMF holds an acknowledgement for an interception
// that no longer exists, and the report it gets says the ADMF went quiet, which is true of
// the window and false of the ADMF.
func TestAnActivationInTheLapseWindowSurvives(t *testing.T) {
	st := store.New()

	var purged []PurgeReason
	var mu sync.Mutex
	srv := testServer(st, OnPurge(func(_ types.InterceptTask, reason PurgeReason) {
		mu.Lock()
		defer mu.Unlock()
		purged = append(purged, reason)
	}))

	// An hour idle against a one-minute window: lapsed when the tick reads it, and not
	// lapsed once the ADMF's message below records activity. A zero window cannot express
	// the second half — any elapsed nanosecond exceeds it — which is the one thing this
	// test needs the clock to be able to say.
	srv.mu.Lock()
	srv.lastSeen = srv.now().Add(-time.Hour)
	srv.mu.Unlock()

	inWindow := make(chan struct{}, 1)
	proceed := make(chan struct{}, 1)
	srv.afterLapseDecision = func() { inWindow <- struct{}{}; <-proceed }

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.purgeIfLapsed(time.Minute)
	}()

	<-inWindow // the tick has concluded the ADMF is gone and has not acted yet

	// The ADMF is back, and this is its first message: it re-provisions the tasking the
	// element lost, which is exactly what a recovery looks like.
	if _, err := srv.Process([]byte(activateXML), admfPeer(t)); err != nil {
		t.Fatalf("recovery activation: %v", err)
	}
	if _, held := st.Get(testXID); !held {
		t.Fatal("the recovery activation was not acknowledged; this test asserts nothing")
	}

	proceed <- struct{}{}
	wg.Wait()

	if _, held := st.Get(testXID); !held {
		mu.Lock()
		reasons := purged
		mu.Unlock()
		t.Errorf("the tasking acknowledged during the lapse window was purged (%v): the ADMF holds "+
			"an acknowledgement for an interception that no longer exists, and the report it gets "+
			"names the ADMF as the thing that went silent", reasons)
	}
}
