// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// TestAStaleExpiryReportsNothing: a TIME_P2 deadline that fires for a connection this client
// has already replaced says nothing about the replacement. It must neither mark the
// destination unreachable nor report a fault.
func TestAStaleExpiryReportsNothing(t *testing.T) {
	m := startMDF(t, answerCorrectly)

	var faults atomic.Int64
	c := clientTo(t, m.addr, KeepaliveConfig{OnFault: func(error) { faults.Add(1) }})

	if err := c.Send(product()); err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	stale := &connState{conn: a, stop: make(chan struct{})}

	c.expire(stale)

	if c.Unreachable() {
		t.Error("a deadline on a replaced connection marked the destination unreachable, " +
			"although its live connection is answering")
	}
	if n := faults.Load(); n != 0 {
		t.Errorf("a deadline on a replaced connection reported %d fault(s) about a mediation "+
			"function that is answering", n)
	}

	c.mu.Lock()
	live := c.live != nil
	c.mu.Unlock()

	if !live {
		t.Error("a deadline on a replaced connection dropped the live one")
	}
}

// TestAStaleProtocolErrorDoesNotMarkTheDestination: a reader of a connection already replaced
// that decodes garbage still reports it — a peer sent bytes no X2/X3 implementation should —
// but what it says about the destination is about a connection this element no longer holds.
// Stored anyway, it would overwrite the replacement's conclusion that the destination is
// reachable.
func TestAStaleProtocolErrorDoesNotMarkTheDestination(t *testing.T) {
	var faults atomic.Int64
	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true, OnFault: func(error) { faults.Add(1) }})
	t.Cleanup(func() { _ = c.Close() })

	ok := func(b []byte) (int, error) { return len(b), nil }
	heldConn(c, newScriptedConn(ok))
	stale := &connState{conn: newScriptedConn(ok), stop: make(chan struct{})}

	c.protocolError(stale, errors.New("x2x3: undecodable PDU"))

	if c.Unreachable() {
		t.Error("a protocol error on a replaced connection marked the destination unreachable")
	}
	if n := faults.Load(); n != 1 {
		t.Errorf("the protocol error was reported %d time(s), want once", n)
	}

	c.mu.Lock()
	live := c.live != nil
	c.mu.Unlock()
	if !live {
		t.Error("a protocol error on a replaced connection dropped the live one")
	}
}

// TestAnAcknowledgementPendingAtTheDeadlineIsHonoured: an acknowledgement and the TIME_P2
// deadline can both be ready when the loop next selects — here because the loop was held up
// writing a Keepalive while both arrived. select picks between ready cases at random, so
// without looking for the acknowledgement before expiring, the connection would be torn down
// over an answer already received, about half the time. Repeated, so that a loop that does not
// look fails all but certainly.
//
// Nothing here depends on the scheduler. The loop is parked inside its first Keepalive's write
// until both are ready, so it cannot select early, and the round ends on the loop's decision —
// the waiting acknowledgement taken, or the loop gone without it — not after a fixed time. What
// follows the decision is TIME_P2 running again, which a starved runner may legitimately let
// expire, and is not what this asks.
func TestAnAcknowledgementPendingAtTheDeadlineIsHonoured(t *testing.T) {
	for i := range 20 {
		var faults atomic.Int64
		c := NewClient("mdf.example:1", nil, KeepaliveConfig{
			TimeP1: 5 * time.Millisecond, TimeP2: 40 * time.Millisecond,
			OnFault: func(error) { faults.Add(1) },
		})
		c.dialConn = func(context.Context) (net.Conn, error) { return nil, errors.New("connection refused") }

		entered, release := make(chan struct{}), make(chan struct{})
		var writes atomic.Int64
		st := heldConn(c, newScriptedConn(func(b []byte) (int, error) {
			if writes.Add(1) == 1 {
				close(entered)
				<-release
			}

			return len(b), nil
		}))
		st.acks = make(chan struct{}, 1)

		exited := make(chan struct{})
		st.wg.Add(1)
		go func() {
			c.keepaliveLoop(st, false)
			close(exited)
		}()

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the loop never wrote its first Keepalive", i)
		}
		// The deadline was armed before the first tick, so it has passed once this has.
		time.Sleep(60 * time.Millisecond)
		st.acks <- struct{}{}
		close(release)

		decided := false
		for deadline := time.Now().Add(5 * time.Second); !decided && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			// Taken first: only the loop takes from acks, so an empty channel means it took the
			// acknowledgement, whatever it went on to do afterwards.
			if len(st.acks) == 0 {
				decided = true

				break
			}
			select {
			case <-exited:
				t.Fatalf("round %d: the connection was expired with an acknowledgement waiting "+
					"(faults=%d)", i, faults.Load())
			default:
			}
		}
		if !decided {
			t.Fatalf("round %d: the loop neither took the acknowledgement nor exited", i)
		}

		_ = c.Close()
		<-exited
	}
}
