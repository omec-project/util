// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
)

// heldConn makes conn the client's live connection without dialling, the way a delivery that
// had already connected leaves it.
func heldConn(c *Client, conn net.Conn) *connState {
	c.mu.Lock()
	defer c.mu.Unlock()

	st := &connState{conn: conn, stop: make(chan struct{})}
	c.live = st

	return st
}

// TestADeliveryOutcomeDoesNotOverwriteANewerOne: a delivery that failed records the destination
// unreachable, and a writer that reaches a newer conclusion afterwards — a probe that reconnected,
// another delivery that wrote — records it reachable. The newer conclusion must stand. Recorded
// after the delivery released mu, the failed delivery's outcome could land last and report a
// working mediation function unreachable until the next delivery.
func TestADeliveryOutcomeDoesNotOverwriteANewerOne(t *testing.T) {
	writeErr := errors.New("connection reset")
	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	c.dialConn = func(context.Context) (net.Conn, error) { return nil, errors.New("connection refused") }
	t.Cleanup(func() { _ = c.Close() })
	heldConn(c, newScriptedConn(func([]byte) (int, error) { return 0, writeErr }))

	c.afterDelivery = func() {
		// A newer writer, after the delivery's outcome was decided.
		c.mu.Lock()
		c.unreachable.Store(false)
		c.mu.Unlock()
	}

	if err := c.Send(product()); err == nil {
		t.Fatal("the delivery was reported as succeeding over a connection whose write failed")
	}

	if c.Unreachable() {
		t.Error("a failed delivery's outcome overwrote a newer conclusion that the destination is " +
			"reachable")
	}
}

// TestAFailedKeepaliveDoesNotOverwriteANewerConnectionsOutcome: a keepalive that could not be
// written drops its connection and records the destination unreachable. A delivery that then
// dials a new connection and writes over it records the destination reachable, and that must
// stand. Recorded after writeOn released mu, the keepalive's conclusion could land after the
// delivery's and report a mediation function that has just taken product as unreachable.
func TestAFailedKeepaliveDoesNotOverwriteANewerConnectionsOutcome(t *testing.T) {
	writeErr := errors.New("broken pipe")
	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	fresh := newScriptedConn(func(b []byte) (int, error) { return len(b), nil })
	c.dialConn = func(context.Context) (net.Conn, error) { return fresh, nil }
	t.Cleanup(func() { _ = c.Close() })
	st := heldConn(c, newScriptedConn(func([]byte) (int, error) { return 0, writeErr }))

	c.afterKeepaliveFailed = func() {
		// A delivery after the keepalive's failure: it dials the fresh connection and writes.
		if err := c.Send(product()); err != nil {
			t.Errorf("the delivery after the keepalive failed: %v", err)
		}
	}

	if c.sendKeepalive(st) {
		t.Fatal("the keepalive loop was told to continue on a connection whose write failed")
	}

	if c.Unreachable() {
		t.Error("a failed keepalive's outcome overwrote a later delivery's conclusion that the " +
			"destination is reachable")
	}
}

// TestAWriteThatTookEveryByteIsADelivery: a write can report an error after the connection
// accepted every byte — a deadline that fired once the last record was flushed. Every unit was
// written, so nothing is lost and the destination took them: no error, not unreachable, and no
// redial, since there is nothing left to send. The suspect connection is dropped, and the next
// delivery dials.
func TestAWriteThatTookEveryByteIsADelivery(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(c *Client) error
	}{
		{"Send", func(c *Client) error { return c.Send(product()) }},
		{"SendBatch", func(c *Client) error { return c.SendBatch([]*PDU{product(), product()}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
			dials := 0
			c.dialConn = func(context.Context) (net.Conn, error) {
				dials++

				return nil, errors.New("connection refused")
			}
			t.Cleanup(func() { _ = c.Close() })
			heldConn(c, newScriptedConn(func(b []byte) (int, error) {
				return len(b), errors.New("i/o timeout")
			}))

			if err := tc.send(c); err != nil {
				t.Errorf("a write that took every byte was reported as a failure: %v", err)
			}
			if c.Unreachable() {
				t.Error("a destination that took every unit was marked unreachable")
			}
			if dials != 0 {
				t.Errorf("redialled %d time(s) with nothing left to send", dials)
			}

			c.mu.Lock()
			live := c.live
			c.mu.Unlock()
			if live != nil {
				t.Error("the connection whose write reported an error was kept")
			}
		})
	}
}

// TestASteadyStreamOfBatchesAllocatesNothing: the delivery worker calls SendBatch for every
// batch it drains, at the subject's packet rate on X3. Once the buffers have grown to a batch's
// size, a batch of the same shape must not allocate: each allocation is garbage the data-plane
// host collects at that rate.
func TestASteadyStreamOfBatchesAllocatesNothing(t *testing.T) {
	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	t.Cleanup(func() { _ = c.Close() })
	heldConn(c, newScriptedConn(func(b []byte) (int, error) { return len(b), nil }))

	pdus := make([]*PDU, 32)
	for i := range pdus {
		pdus[i] = product()
	}
	if err := c.SendBatch(pdus); err != nil {
		t.Fatal(err)
	}

	allocs := testing.AllocsPerRun(100, func() {
		if err := c.SendBatch(pdus); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("a steady-state batch of %d PDUs allocates %.0f times, want 0", len(pdus), allocs)
	}
}

// TestReusedBatchBuffersCarryOnlyTheCurrentBatch: SendBatch marshals into the previous batch's
// buffers. A large batch, then a small one, then a larger one: each write must be exactly that
// batch's PDUs — not the tail of a longer predecessor, and not appended after it — and the
// unit boundaries a resend would resume at must be this batch's.
func TestReusedBatchBuffersCarryOnlyTheCurrentBatch(t *testing.T) {
	var wire []byte
	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	t.Cleanup(func() { _ = c.Close() })
	heldConn(c, newScriptedConn(func(b []byte) (int, error) {
		wire = append(wire, b...)

		return len(b), nil
	}))

	unit := func(tag byte, n int) *PDU {
		return &PDU{
			Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable,
			Payload: bytes.Repeat([]byte{tag}, n),
		}
	}

	var want []byte
	for _, batch := range [][]*PDU{
		{unit(1, 4096), unit(2, 4096), unit(3, 4096)},
		{unit(4, 10)},
		{unit(5, 8192), unit(6, 8192), unit(7, 100), unit(8, 1)},
	} {
		for _, p := range batch {
			b, err := p.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, b...)
		}

		if err := c.SendBatch(batch); err != nil {
			t.Fatal(err)
		}

		c.batchMu.Lock()
		ends := len(c.batchEnds)
		c.batchMu.Unlock()
		if ends != len(batch) {
			t.Errorf("a batch of %d units left %d unit boundaries", len(batch), ends)
		}
	}

	if !bytes.Equal(wire, want) {
		t.Errorf("the wire carried %d bytes, want exactly the %d bytes of the three batches", len(wire), len(want))
	}
}

// TestABatchOverTheRetentionBoundIsNotKept: a burst larger than maxRetainedBatchBytes is sent,
// but its buffer is let go rather than pinned in memory for the life of the client, and the
// next batch starts from a buffer of its own size.
func TestABatchOverTheRetentionBoundIsNotKept(t *testing.T) {
	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	t.Cleanup(func() { _ = c.Close() })
	heldConn(c, newScriptedConn(func(b []byte) (int, error) { return len(b), nil }))

	big := make([]*PDU, 5)
	for i := range big {
		big[i] = &PDU{
			Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionFromTarget,
			Payload: make([]byte, 1<<20),
		}
	}
	if err := c.SendBatch(big); err != nil {
		t.Fatal(err)
	}

	c.batchMu.Lock()
	kept, keptEnds := c.batchBuf, c.batchEnds
	c.batchMu.Unlock()
	if kept != nil || keptEnds != nil {
		t.Errorf("a %d-byte buffer was retained after a burst over the %d-byte bound",
			cap(kept), maxRetainedBatchBytes)
	}

	if err := c.SendBatch([]*PDU{product()}); err != nil {
		t.Fatal(err)
	}

	c.batchMu.Lock()
	n := cap(c.batchBuf)
	c.batchMu.Unlock()
	if n > maxRetainedBatchBytes {
		t.Errorf("the batch after the burst kept a %d-byte buffer", n)
	}
}
