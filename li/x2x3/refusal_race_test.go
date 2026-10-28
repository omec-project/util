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

// These tests drive, without a socket, the orders a resetting refusal produces at random on a
// real one: a writer that drops the connection before the reader has read the alert. The
// socket-level tests in tls_refusal_test.go meet those orders only some of the time.

var errReset = errors.New("write: connection reset by peer")

// countingDialer hands out connections from next and counts the dials.
type countingDialer struct {
	dials atomic.Int64
	next  func(n int64) (net.Conn, error)
}

func (d *countingDialer) dial(context.Context) (net.Conn, error) {
	return d.next(d.dials.Add(1))
}

func idleKeepalive(onFault func(error)) KeepaliveConfig {
	return KeepaliveConfig{TimeP1: time.Hour, TimeP2: 2 * time.Hour, OnFault: onFault}
}

// TestADeliveryWhoseResendFailsIsFollowedByAProbe: both connections of a delivery fail under
// the write, as against a peer that resets each connection it refuses. Nothing is left to
// deliver, so unless a probe follows, nothing dials the destination again until traffic does,
// and a mediation function that recovers stays reported unreachable.
func TestADeliveryWhoseResendFailsIsFollowedByAProbe(t *testing.T) {
	var keepalives atomic.Int64
	d := &countingDialer{next: func(n int64) (net.Conn, error) {
		if n <= 2 {
			return newScriptedConn(func([]byte) (int, error) { return 0, errReset }), nil
		}

		return newScriptedConn(func(b []byte) (int, error) {
			keepalives.Add(1)

			return len(b), nil
		}), nil
	}}

	c := NewClient("mdf.example:1", nil, idleKeepalive(nil))
	c.dialConn = d.dial
	c.redialInterval = 10 * time.Millisecond
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Send(product()); err == nil {
		t.Fatal("Send() = nil over two connections that both failed")
	}

	eventually(t, "a probe to dial again", func() bool { return d.dials.Load() >= 3 })
	// The probe's connection was installed while the destination stood unreachable, so it
	// asks at once whether that has changed rather than after TIME_P1.
	eventually(t, "the probe's prompt Keepalive", func() bool { return keepalives.Load() >= 1 })
}

// TestAFailedKeepaliveIsFollowedByAProbe: a keepalive whose write fails drops the connection.
// The next delivery used to be what redialled, so an idle destination stayed unreachable
// however soon its mediation function came back.
func TestAFailedKeepaliveIsFollowedByAProbe(t *testing.T) {
	d := &countingDialer{next: func(int64) (net.Conn, error) {
		return newScriptedConn(func(b []byte) (int, error) { return len(b), nil }), nil
	}}

	var faults faultLog
	c := NewClient("mdf.example:1", nil, idleKeepalive(faults.record))
	c.dialConn = d.dial
	t.Cleanup(func() { _ = c.Close() })

	st := heldConn(c, newScriptedConn(func([]byte) (int, error) { return 0, errReset }))

	if c.sendKeepalive(st) {
		t.Fatal("sendKeepalive reported success on a failed write")
	}

	eventually(t, "a probe to dial", func() bool { return d.dials.Load() == 1 })
	if n := faults.count(); n != 1 {
		t.Errorf("a failed keepalive on a reachable destination was reported %d times, want once", n)
	}
}

// TestAFailedKeepaliveOnAnUnreachableDestinationIsNotReportedAgain: a destination already
// standing unreachable — or refused — has been reported. The Keepalive that fails is the
// prompt one a redial sends to ask whether that changed, and a resetting refusal fails it on
// every redial: reported each time, one outage would be reported once per redial interval.
func TestAFailedKeepaliveOnAnUnreachableDestinationIsNotReportedAgain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(c *Client)
	}{
		{"unreachable", func(c *Client) { c.unreachable.Store(true) }},
		{"refused", func(c *Client) { c.rejection = errors.New("tls: unknown certificate authority") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &countingDialer{next: func(int64) (net.Conn, error) { return nil, errors.New("connection refused") }}

			var faults faultLog
			c := NewClient("mdf.example:1", nil, idleKeepalive(faults.record))
			c.dialConn = d.dial
			t.Cleanup(func() { _ = c.Close() })

			st := heldConn(c, newScriptedConn(func([]byte) (int, error) { return 0, errReset }))
			c.mu.Lock()
			tc.setup(c)
			c.mu.Unlock()

			c.redialInterval = 10 * time.Millisecond
			c.sendKeepalive(st)

			// The probe that follows fails its dials too — it keeps dialling — and says
			// nothing new on any of them.
			eventually(t, "the probe to dial several times", func() bool { return d.dials.Load() >= 3 })
			// Close waits for the probe goroutine, which is after anything it reports.
			_ = c.Close()
			if n := faults.count(); n != 0 {
				t.Errorf("a destination already %s was reported again: %v", tc.name, faults.all())
			}
			if !c.Unreachable() {
				t.Error("the destination is not unreachable after a failed keepalive and a failed probe")
			}
		})
	}
}

// TestARefusalReadAfterAWriterDroppedTheConnectionStands: the writer dropped the connection
// first, so by the time the reader reads the alert its connection is no longer held. The
// reader is the only party that saw the refusal; it is recorded, reported, and a probe follows.
func TestARefusalReadAfterAWriterDroppedTheConnectionStands(t *testing.T) {
	d := &countingDialer{next: func(int64) (net.Conn, error) { return nil, errors.New("connection refused") }}

	var faults faultLog
	c := NewClient("mdf.example:1", nil, idleKeepalive(faults.record))
	c.dialConn = d.dial
	t.Cleanup(func() { _ = c.Close() })

	st := heldConn(c, newScriptedConn(func(b []byte) (int, error) { return len(b), nil }))
	c.mu.Lock()
	c.dropLocked() // the writer, first
	c.mu.Unlock()

	c.refused(st, &net.OpError{Op: "remote error", Err: errors.New("tls: unknown certificate authority")})

	c.mu.Lock()
	rejected := c.rejection != nil
	c.mu.Unlock()
	if !rejected || !c.Unreachable() {
		t.Errorf("a refusal read after its connection was dropped was lost: rejection=%v unreachable=%v",
			rejected, c.Unreachable())
	}
	if n := faults.count(); n != 1 {
		t.Errorf("the refusal was reported %d times, want once", n)
	}
	eventually(t, "a probe to follow the refusal", func() bool { return d.dials.Load() >= 1 })
}

// TestARefusalOnAReplacedConnectionLeavesTheNewerOneToSpeak: a newer connection is held. It
// is refused or accepted on its own account, so a stale reader's alert neither drops it nor
// stands a refusal over it — which would refuse deliveries to a mediation function that may
// already have acknowledged the newer connection.
func TestARefusalOnAReplacedConnectionLeavesTheNewerOneToSpeak(t *testing.T) {
	var faults faultLog
	c := NewClient("mdf.example:1", nil, idleKeepalive(faults.record))
	t.Cleanup(func() { _ = c.Close() })

	ok := func(b []byte) (int, error) { return len(b), nil }
	stale := &connState{conn: newScriptedConn(ok), stop: make(chan struct{})}
	newer := heldConn(c, newScriptedConn(ok))

	c.refused(stale, &net.OpError{Op: "remote error", Err: errors.New("tls: unknown certificate authority")})

	c.mu.Lock()
	held, rejected := c.live == newer, c.rejection != nil
	c.mu.Unlock()
	if !held || rejected || c.Unreachable() || faults.count() != 0 {
		t.Errorf("a stale refusal acted over the newer connection: held=%v rejection=%v unreachable=%v faults=%d",
			held, rejected, c.Unreachable(), faults.count())
	}
}
