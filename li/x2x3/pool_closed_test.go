// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"crypto/tls"
	"testing"
	"time"
)

// TestForAfterCloseBuildsNothing: Close empties the sender map, so without the closed
// flag a delivery arriving after it would miss the cache and construct a fresh sender —
// with a worker goroutine behind it — into a pool nobody will close again: a worker
// outliving the shutdown meant to end it, still holding a connection to a mediation
// function this element no longer answers for.
//
// What a closed pool hands back is a sender that discards, not nil. The assertion
// is on the *type* rather than on nil-ness, because "builds nothing" is the property
// under test and a discarding sender satisfies it: nothing is dialled and no worker
// is started.
func TestForAfterCloseBuildsNothing(t *testing.T) {
	p := NewPool(nil, KeepaliveConfig{}, nil, nil)

	if s := p.For("192.0.2.1:42069"); s == nil {
		t.Fatal("a live pool returned no sender")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, addr := range []string{
		"192.0.2.2:42069", // an address the pool never held
		"192.0.2.1:42069", // one it held and closed
	} {
		s := p.For(addr)
		if s == nil {
			t.Fatalf("a closed pool returned nil for %s; the delivery path may neither "+
				"block nor fault, and a nil interface faults it", addr)
		}
		if _, discarding := s.(discardSender); !discarding {
			t.Errorf("a closed pool built a real sender for %s, and with it a worker nothing will stop", addr)
		}
	}
}

// TestClosedPoolSenderIsSafeToUse is the property the nil return did not have. A
// delivery racing a shutdown is the ordinary case rather than the exotic one — a
// purge, a reconfiguration and an element shutting down are exactly the moments
// product is still being offered — and the offering path is a signalling or
// data-plane path that delivery may neither block nor fault.
func TestClosedPoolSenderIsSafeToUse(t *testing.T) {
	p := NewPool(nil, KeepaliveConfig{}, nil, nil)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s := p.For("192.0.2.1:42069")
	if err := s.Send(&PDU{Type: PDUTypeX2}); err != nil {
		t.Errorf("Send on a closed pool's sender returned %v, want nil", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close on a closed pool's sender returned %v, want nil", err)
	}
}

// TestClosedPoolReportsNoFault: a destination nothing has been sent to is not
// unreachable — the element has not looked. A closed pool's phantom senders must
// not put a fault into an element's status answer.
func TestClosedPoolReportsNoFault(t *testing.T) {
	p := NewPool(nil, KeepaliveConfig{}, nil, nil)
	const addr = "192.0.2.1:42069"
	p.For(addr)

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if p.UnreachableAt(addr) {
		t.Error("a closed pool reported a destination as unreachable")
	}
	if unreachable, _ := p.UnreachableAmong([]string{addr}); unreachable != 0 {
		t.Errorf("a closed pool counted %d unreachable destinations, want 0", unreachable)
	}
}

// TestAClosedPoolReportsItsLateProduct: product offered to a closed pool is dropped, and
// the drop is reported through the POI's own hook.
//
// A closed pool usually belongs to an element shutting down, where a report may not arrive;
// but a pool is also closed by a reconfiguration, where the ADMF is reachable and this is
// simply product the element produced and did not deliver. Staying silent would decide on
// the caller's behalf that nobody wants to be told. What a POI does with the hook during its
// own teardown is the POI's business.
func TestAClosedPoolReportsItsLateProduct(t *testing.T) {
	drops := 0
	p := NewPool(nil, KeepaliveConfig{}, nil, func() { drops++ })
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s := p.For("10.0.60.122:42069")
	if err := s.Send(&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable}); err != nil {
		t.Fatalf("a closed pool's sender returned %v; delivery must neither block nor fault the "+
			"signalling path that offered the product", err)
	}

	if drops != 1 {
		t.Errorf("a closed pool dropped product and reported it %d times, want 1: an xIRI the "+
			"element produced went nowhere and nothing said so", drops)
	}
}

// TestClosingABlackholedDestinationIsBounded is the shutdown half.
//
// Closing a sender waits for its worker to drain the queue, and each unit is bounded only by
// the client's own write deadline — so one destination that accepts a connection and then
// reads nothing costs (queue depth × write timeout), which is minutes. A network function
// shutting down inside a container's grace period is SIGKILLed part-way through its LI
// teardown instead, and what a SIGKILL leaves is a POI whose X1 tasking was never withdrawn.
//
// Worse than slow, it is observable: an element serving a tasked subscriber whose MDF is
// blackholed takes minutes to stop where an untasked one takes none.
func TestClosingABlackholedDestinationIsBounded(t *testing.T) {
	t.Parallel()

	cert := selfSignedServer(t)

	// A peer that completes the handshake and never reads: the queue cannot drain.
	mdf := newHalfOpenMDF(t, cert, 0, true)

	p := NewPool(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // test peer
		KeepaliveConfig{Disabled: true}, nil, nil)

	s := p.For(mdf.addr)
	// Comfortably more than one write can carry, so the worker is still draining when Close
	// arrives.
	for range 64 {
		//nolint:errcheck // enqueueing cannot fail in a way this test acts on
		_ = s.Send(&PDU{
			Type:          PDUTypeX2,
			PayloadFormat: PayloadFormat3GPP33128,
			Direction:     DirectionNotApplicable,
			Payload:       make([]byte, 256*1024),
		})
	}

	done := make(chan error, 1)
	go func() { done <- p.Close() }()

	select {
	case <-done:
		// Either outcome is acceptable: what is asserted is the bound, not the verdict.
	case <-time.After(closeTimeout + 5*time.Second):
		t.Fatalf("Close did not return within %s of its own deadline: a blackholed mediation "+
			"function decides how long this element takes to stop, and a teardown that overruns a "+
			"grace period is SIGKILLed with its X1 tasking never withdrawn", closeTimeout)
	}
}
