// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"sync"
	"sync/atomic"
)

// Sequencer hands out the Sequence Number conditional attribute's values for one
// interface, in the scope ETSI TS 103 221-2 clause 5.3.9 defines: the number "shall
// start at zero and increment by one for each X2/X3 PDU with the same XID, DID,
// NFID, IPID and Correlation ID context", a separate sequence is kept for X2 and X3
// within one context, and the sequence restarts at zero once the four-octet field is
// exhausted.
//
// This element sends no DID, and its NFID and IPID are constant for the life of a
// process, so the part of that key which varies is (XID, Correlation ID) — which is
// what this indexes. Two consequences are worth stating because the obvious
// implementations get them wrong:
//
//   - It is not per connection. One PDU delivered to two of a task's destinations
//     carries one number, because the number belongs to the context and not to
//     either socket. A counter held on a Client or an AsyncSender would instead
//     number unrelated contexts from one sequence — and the keepalive mechanism,
//     which does count per connection, needs its own counter for exactly that
//     reason: a Keepalive PDU zeroes the XID and Correlation ID, so its context is
//     not any task's.
//   - One Sequencer per point of interception is one sequence per interface, since
//     each POI here delivers on X2 or on X3 and never both. That is why no interface
//     discriminator appears in the key.
//
// Safe for concurrent use: the number is taken on the path that frames a PDU, which in a
// CC-POI may run on several workers at once.
type Sequencer struct {
	mu sync.RWMutex
	// counters holds the next value to hand out for each live context. A pointer to
	// an atomic so the common case — a context that already exists — takes only a
	// read lock and an atomic increment.
	counters map[seqContext]*atomic.Uint32

	// afterLookup is called inside Next, after an existing counter has been found and
	// before it is advanced. Set only by tests; nil otherwise.
	afterLookup func()
}

// seqContext is the varying part of clause 5.3.9's context.
type seqContext struct {
	xid  [xidLength]byte
	corr [CorrelationIDLength]byte
}

// NewSequencer returns a Sequencer holding no contexts.
func NewSequencer() *Sequencer {
	return &Sequencer{counters: make(map[seqContext]*atomic.Uint32)}
}

// Next returns the sequence number for the next PDU in this context, starting at
// zero and wrapping to zero after 2^32-1 as the clause requires.
//
// Take it where the PDU is built, not where it is written. Delivery drops a PDU when
// its queue is full, and a number taken before the queue leaves a gap the mediation
// function can see, which is how loss becomes visible at all; a number taken in the
// delivery worker would close the gap over the missing product.
//
// The increment happens before the read lock is released, so Forget and ForgetContext,
// which take the write lock, cannot fall between finding a counter and advancing it.
// If they could, a Next racing a deactivation would number its PDU from a counter that
// had already been dropped, and the reactivated context would hand out the same number
// again from zero.
func (s *Sequencer) Next(xid [xidLength]byte, corr [CorrelationIDLength]byte) uint32 {
	key := seqContext{xid: xid, corr: corr}

	s.mu.RLock()
	if c, ok := s.counters[key]; ok {
		if s.afterLookup != nil {
			s.afterLookup()
		}

		n := c.Add(1) - 1
		s.mu.RUnlock()

		return n
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	// Another goroutine may have created it while the read lock was released.
	if c, ok := s.counters[key]; ok {
		return c.Add(1) - 1
	}

	c := &atomic.Uint32{}
	c.Store(1)
	s.counters[key] = c

	return 0
}

// Forget drops every context belonging to this XID, so the numbering state does not
// outlive the tasking it belongs to. Wire it to the X1 task-deactivation hook: a
// warrant covering many sessions creates a context per session, and without this the
// element retains one entry per session per warrant for as long as the process runs.
//
// A task deactivated and later activated again under the same XID therefore numbers
// from zero, which is what a new context does.
//
// The POI stops building PDUs for the task before calling it. A Next that runs after Forget
// recreates the context it just dropped and numbers from zero, so a PDU built late in a
// deactivation would carry a number the mediation function has already seen under that
// context, and the entry would outlive the task after all. Forget orders itself against a Next
// already in progress (see Next); it cannot order itself against one that has not started.
// The same holds for ForgetContext.
//
// Only where the whole XID is going. This is the right granularity at an IRI-POI,
// where one task is one warrant. It is the wrong one wherever several live tasks share
// a delivery XID — a triggered CC-POI, where the triggering function allocates one task
// per (warrant, session, UPF) under the warrant's ProductID — because ending one of
// them then restarts the numbering of the rest. See ForgetContext.
func (s *Sequencer) Forget(xid [xidLength]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Scanning is the right cost here: forgetting happens once per deactivation
	// while Next runs per PDU, so the flat key keeps the hot path to one lookup.
	for key := range s.counters {
		if key.xid == xid {
			delete(s.counters, key)
		}
	}
}

// ForgetContext drops exactly one context: this XID's numbering for this correlation
// value, leaving every other context under the same XID numbering where it was.
//
// Which of these two a point of interception needs follows from its tasking model,
// and the two models in this project differ. At an IRI-POI one task is one warrant
// and is deactivated once, so the whole XID really is going and Forget is right. At a
// triggered CC-POI the triggering function allocates one task per (warrant, session,
// UPF) and gives them all the warrant's ProductID as their delivery XID — so the XID on
// the wire is shared by every session that warrant is intercepting at that element, and
// releasing by XID when one session ends restarts the numbering of all the others.
//
// That is not a leak but a forgery. A sequence number is how a mediation function
// detects loss, so numbering state that resets under a live context makes this element
// emit a sequence the receiver must read as either duplication or a gap — the loss
// signal manufactured by the mechanism that governs it.
//
// The failure directions are asymmetric, and worth stating because it decides which to
// reach for when unsure. Calling ForgetContext where Forget was needed leaks entries,
// bounded by live tasking and visible through Identity.Contexts(). Calling Forget where
// ForgetContext was needed corrupts product that is still being delivered.
func (s *Sequencer) ForgetContext(xid [xidLength]byte, corr [CorrelationIDLength]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.counters, seqContext{xid: xid, corr: corr})
}

// Len reports how many contexts are being numbered. It exists so that "the state is
// bounded by live tasking" is assertable from outside this package, which is the only
// way a leak of this kind gets noticed before it matters.
func (s *Sequencer) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.counters)
}
