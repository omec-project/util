// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// scriptedConn is a net.Conn whose writes answer from write, and whose reads block until it is
// closed — enough for a client to hold it as its live connection.
type scriptedConn struct {
	write  func(b []byte) (int, error)
	once   sync.Once
	closed chan struct{}
}

func newScriptedConn(write func(b []byte) (int, error)) *scriptedConn {
	return &scriptedConn{write: write, closed: make(chan struct{})}
}

func (s *scriptedConn) Write(b []byte) (int, error) { return s.write(b) }

func (s *scriptedConn) Read([]byte) (int, error) {
	<-s.closed

	return 0, net.ErrClosed
}

func (s *scriptedConn) Close() error {
	s.once.Do(func() { close(s.closed) })

	return nil
}

func (*scriptedConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*scriptedConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*scriptedConn) SetDeadline(time.Time) error      { return nil }
func (*scriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (*scriptedConn) SetWriteDeadline(time.Time) error { return nil }

// TestADroppedTailIsCountedAndLeavesTheDestinationReachable drives the one path that knowingly
// loses product, without depending on how much a kernel's socket buffers absorb: the write of a
// three-unit batch stops inside the second unit, the client reconnects, and the resend of the
// second and third units fails on the fresh connection too.
//
// The first unit reached the mediation function, so the destination is reachable and the loss
// is ErrUnitDropped — two units, from the one the write stopped inside to the end of the batch.
// An ordinary error here would mark a working mediation function unreachable.
func TestADroppedTailIsCountedAndLeavesTheDestinationReachable(t *testing.T) {
	pdus := []*PDU{product(), product(), product()}
	first, err := pdus[0].Marshal()
	if err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("connection reset")
	held := newScriptedConn(func([]byte) (int, error) { return len(first) + 5, writeErr })
	fresh := newScriptedConn(func([]byte) (int, error) { return 0, writeErr })

	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	c.dialConn = func(context.Context) (net.Conn, error) { return fresh, nil }
	t.Cleanup(func() { _ = c.Close() })

	c.mu.Lock()
	c.live = &connState{conn: held, stop: make(chan struct{})}
	c.mu.Unlock()

	err = c.SendBatch(pdus)

	if !errors.Is(err, ErrUnitDropped) {
		t.Fatalf("SendBatch() = %v, want ErrUnitDropped: the first unit landed, so this is a "+
			"dropped tail and not a failed delivery", err)
	}
	if got := LostUnits(err); got != 2 {
		t.Errorf("the error counts %d lost unit(s), want 2: the unit the write stopped inside "+
			"and the one after it", got)
	}
	if c.Unreachable() {
		t.Error("a destination that took the first unit was marked unreachable over the tail " +
			"it did not take")
	}
}

// droppedTail runs a three-unit batch whose write on the held connection takes heldTook bytes
// and fails, and whose resend on a fresh connection takes freshTook bytes and fails with
// freshErr. It returns the client and the error SendBatch reported.
func droppedTail(t *testing.T, heldTook func(first int) int, freshTook int, freshErr error) (*Client, error) {
	t.Helper()

	pdus := []*PDU{product(), product(), product()}
	first, err := pdus[0].Marshal()
	if err != nil {
		t.Fatal(err)
	}

	held := newScriptedConn(func([]byte) (int, error) { return heldTook(len(first)), errors.New("connection reset") })
	fresh := newScriptedConn(func([]byte) (int, error) { return freshTook, freshErr })

	c := NewClient("mdf.example:1", nil, KeepaliveConfig{Disabled: true})
	c.dialConn = func(context.Context) (net.Conn, error) { return fresh, nil }
	t.Cleanup(func() { _ = c.Close() })

	c.mu.Lock()
	c.live = &connState{conn: held, stop: make(chan struct{})}
	c.mu.Unlock()

	return c, c.SendBatch(pdus)
}

// TestAResendThatTimesOutMarksTheDestinationUnreachable: the mediation function accepted the
// fresh connection and then read nothing within the write timeout. It has stalled, and every
// later batch would end the same way, redialled before TIME_P2 could expire — so a stall
// called "reachable" would never be reported at all.
func TestAResendThatTimesOutMarksTheDestinationUnreachable(t *testing.T) {
	timeout := &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	c, err := droppedTail(t, func(first int) int { return first + 5 }, 0, timeout)

	if !errors.Is(err, ErrUnitDropped) {
		t.Fatalf("SendBatch() = %v, want ErrUnitDropped: the first unit landed", err)
	}
	if got := LostUnits(err); got != 2 {
		t.Errorf("the error counts %d lost unit(s), want 2", got)
	}
	if !c.Unreachable() {
		t.Error("a mediation function that took nothing on a fresh connection within the write " +
			"timeout was left reported reachable")
	}
}

// TestADroppedTailAtAUnitBoundaryIsClassifiedTheSame: a write that stopped exactly at a unit
// boundary landed whole units, as one that stopped inside a unit did. A failed resend is the
// same dropped tail either way, and was once an ordinary failure that marked the destination
// unreachable only because of where the first write happened to stop.
func TestADroppedTailAtAUnitBoundaryIsClassifiedTheSame(t *testing.T) {
	c, err := droppedTail(t, func(first int) int { return first }, 0, errors.New("connection reset"))

	if !errors.Is(err, ErrUnitDropped) {
		t.Fatalf("SendBatch() = %v, want ErrUnitDropped: the first unit landed whole", err)
	}
	if got := LostUnits(err); got != 2 {
		t.Errorf("the error counts %d lost unit(s), want 2", got)
	}
	if c.Unreachable() {
		t.Error("a destination that took the first unit was marked unreachable over the tail")
	}
}

// TestAResendThatTookAUnitDoesNotCountItLost: the resend handed one unit over whole before it
// failed. That unit was delivered, and the count is what the caller reports as missing from
// the mediation function's sequence.
func TestAResendThatTookAUnitDoesNotCountItLost(t *testing.T) {
	second, err := product().Marshal()
	if err != nil {
		t.Fatal(err)
	}

	_, err = droppedTail(t, func(first int) int { return first + 5 }, len(second), errors.New("connection reset"))

	if got := LostUnits(err); got != 1 {
		t.Errorf("the error counts %d lost unit(s), want 1: the resend took the second unit whole", got)
	}
}
