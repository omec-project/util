// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"sync"
	"testing"
)

var (
	otherXID = [xidLength]byte{9, 9, 9}
	otherCID = [CorrelationIDLength]byte{9}
)

// TestSequencerStartsAtZeroPerContext is clause 5.3.9's first sentence: the number
// starts at zero and increments by one within a context.
func TestSequencerStartsAtZeroPerContext(t *testing.T) {
	s := NewSequencer()
	for want := range uint32(3) {
		if got := s.Next(sampleXID, sampleCID); got != want {
			t.Errorf("Next() = %d, want %d", got, want)
		}
	}
}

// TestSequencerContextsAreIndependent covers the case a per-connection counter gets
// wrong: two sessions of one warrant, or two warrants, sharing a delivery
// connection. Each is numbered from zero in its own context.
func TestSequencerContextsAreIndependent(t *testing.T) {
	s := NewSequencer()
	s.Next(sampleXID, sampleCID)
	s.Next(sampleXID, sampleCID)

	if got := s.Next(sampleXID, otherCID); got != 0 {
		t.Errorf("a second correlation id starts at %d, want 0", got)
	}
	if got := s.Next(otherXID, sampleCID); got != 0 {
		t.Errorf("a second XID starts at %d, want 0", got)
	}
	if got := s.Next(sampleXID, sampleCID); got != 2 {
		t.Errorf("the first context continued at %d, want 2", got)
	}
	if n := s.Len(); n != 3 {
		t.Errorf("holding %d contexts, want 3", n)
	}
}

// TestSequencerWraps: "once the maximum sequence number is reached, the POI shall
// restart the sequence number from zero".
func TestSequencerWraps(t *testing.T) {
	s := NewSequencer()
	s.Next(sampleXID, sampleCID) // create the context, returning 0

	s.mu.RLock()
	c := s.counters[seqContext{xid: sampleXID, corr: sampleCID}]
	s.mu.RUnlock()
	c.Store(0xFFFFFFFF) // the next number handed out is the last one available

	if got := s.Next(sampleXID, sampleCID); got != 0xFFFFFFFF {
		t.Fatalf("Next() = %d, want 0xFFFFFFFF", got)
	}
	if got := s.Next(sampleXID, sampleCID); got != 0 {
		t.Errorf("Next() after the maximum = %d, want 0", got)
	}
}

// TestSequencerForgetDropsATasksContexts is the leak guard. A warrant covering many
// sessions creates a context per session, so numbering state that outlives tasking
// grows for as long as the process runs — worst in the UPF, which holds the most.
func TestSequencerForgetDropsATasksContexts(t *testing.T) {
	s := NewSequencer()
	s.Next(sampleXID, sampleCID)
	s.Next(sampleXID, otherCID)
	s.Next(otherXID, sampleCID)

	s.Forget(sampleXID)

	if n := s.Len(); n != 1 {
		t.Errorf("holding %d contexts after forgetting one XID, want 1", n)
	}
	if got := s.Next(sampleXID, sampleCID); got != 0 {
		t.Errorf("a reactivated XID resumes at %d, want 0 — a new context starts at zero", got)
	}
	if got := s.Next(otherXID, sampleCID); got != 1 {
		t.Errorf("an untouched context restarted at %d, want 1", got)
	}
}

// TestSequencerIsConcurrent: the number is taken where a PDU is framed, which in a CC-POI
// may run on several workers at once. Run under -race; the assertion is that every number in a
// context is handed out exactly once.
func TestSequencerIsConcurrent(t *testing.T) {
	const workers, each = 8, 200
	s := NewSequencer()

	var mu sync.Mutex
	seen := make(map[uint32]int, workers*each)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				n := s.Next(sampleXID, sampleCID)
				mu.Lock()
				seen[n]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != workers*each {
		t.Errorf("%d distinct numbers for %d PDUs: a number was handed out twice", len(seen), workers*each)
	}
	for n := range uint32(workers * each) {
		if seen[n] != 1 {
			t.Errorf("number %d handed out %d times, want once", n, seen[n])
		}
	}
}

// TestForgetContextLeavesSiblingContextsNumbering is the granularity the CC-POI needs,
// and the assertion is deliberately on the *number* rather than on Len().
//
// A count passes against a release that dropped the wrong entry and then recreated it on
// the next Next() — which is exactly the shape of the defect: the surviving context looks
// present and starts again from zero. What a mediation function sees is a sequence
// restarting under a live context, which it must read as duplication or as a gap. A
// sequence number is how loss is detected on this interface, so the state that governs
// the loss signal is capable of forging it.
func TestForgetContextLeavesSiblingContextsNumbering(t *testing.T) {
	s := NewSequencer()

	// Two contexts under one delivery XID: at a triggered CC-POI this is two of a
	// warrant's PDU sessions, each with its own correlation value and all of them
	// carrying the warrant's ProductID on the wire.
	s.Next(sampleXID, sampleCID)
	s.Next(sampleXID, sampleCID)
	s.Next(sampleXID, sampleCID) // this context's next number is 3
	s.Next(sampleXID, otherCID)
	s.Next(sampleXID, otherCID) // and this one's is 2

	// One session ends. Only its context goes.
	s.ForgetContext(sampleXID, sampleCID)

	if got := s.Next(sampleXID, otherCID); got != 2 {
		t.Errorf("a sibling context under the same XID continued at %d, want 2: ending one of a "+
			"warrant's sessions restarted the numbering of another one it is still intercepting, "+
			"which reaches the mediation function as duplicated numbers or a gap", got)
	}
	if got := s.Next(sampleXID, sampleCID); got != 0 {
		t.Errorf("the released context resumed at %d, want 0 — a new context starts at zero", got)
	}
}

// TestForgetStillDropsEveryContextUnderTheXID pins the other primitive, because the two
// are easy to confuse and the failure directions are not symmetric: a Forget where a
// ForgetContext was needed corrupts live product, and a ForgetContext where a Forget was
// needed leaks entries bounded by live tasking.
func TestForgetStillDropsEveryContextUnderTheXID(t *testing.T) {
	s := NewSequencer()
	s.Next(sampleXID, sampleCID)
	s.Next(sampleXID, otherCID)
	s.Next(otherXID, sampleCID)

	s.Forget(sampleXID)

	if n := s.Len(); n != 1 {
		t.Errorf("holding %d contexts after forgetting one XID, want 1", n)
	}
	if got := s.Next(sampleXID, otherCID); got != 0 {
		t.Errorf("a context under the forgotten XID resumed at %d, want 0", got)
	}
}

// TestForgetContextIsForwardedByIdentity: the points of interception reach the sequencer
// through Identity, so a primitive that exists and is not forwarded is a primitive
// nothing can use.
func TestForgetContextIsForwardedByIdentity(t *testing.T) {
	i := NewIdentity("upf-1", "1")

	i.seq.Next(sampleXID, sampleCID)
	i.seq.Next(sampleXID, otherCID)

	i.ForgetContext(sampleXID, sampleCID)

	if n := i.Contexts(); n != 1 {
		t.Errorf("Identity holds %d contexts after releasing one, want 1", n)
	}
	if got := i.seq.Next(sampleXID, otherCID); got != 1 {
		t.Errorf("the sibling context continued at %d, want 1", got)
	}

	i.Forget(sampleXID)
	if n := i.Contexts(); n != 0 {
		t.Errorf("Identity holds %d contexts after forgetting the XID, want 0", n)
	}
}

// TestAForgetCannotFallBetweenFindingACounterAndAdvancingIt pins the ordering Next relies on:
// the counter is advanced while the read lock is still held, so a deactivation's Forget, which
// needs the write lock, is either wholly before the lookup or wholly after the increment.
//
// Checked at the instant it matters rather than by racing two goroutines and hoping: inside that
// window a writer must not be able to take the lock. If it could, a Next racing a deactivation
// would number its PDU from a counter Forget had just dropped, and the reactivated context would
// then hand the same numbers out again from zero.
func TestAForgetCannotFallBetweenFindingACounterAndAdvancingIt(t *testing.T) {
	s := NewSequencer()
	s.Next(sampleXID, sampleCID) // creates the counter, so the next call takes the lookup path

	reached := false
	s.afterLookup = func() {
		reached = true

		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Error("a writer could take the lock between Next finding the counter and advancing " +
				"it, so Forget can drop the counter in that window and the number is handed out " +
				"from a context that no longer exists")
		}
	}
	if got := s.Next(sampleXID, sampleCID); got != 1 {
		t.Errorf("Next() = %d, want 1", got)
	}

	if !reached {
		t.Fatal("the lookup path was not taken, so this test checked nothing")
	}
}
