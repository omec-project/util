// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"context"
	"crypto/tls"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// probeMDF is a mediation function whose behaviour after the first connection the test
// chooses. It counts the connections it accepted.
type probeMDF struct {
	ln       net.Listener
	addr     string
	accepted atomic.Int64
	// firstClosed is closed once the first connection has been read from and closed.
	firstClosed chan struct{}
	// later counts the X2 PDUs later connections received.
	later atomic.Int64
	// silent stops later connections acknowledging Keepalives, which they otherwise do.
	silent atomic.Bool
}

// newProbeMDF starts a mediation function that reads one PDU on its first connection and
// closes it — after closing its listener too when die is set, so it is gone before the
// element can see the close. Later connections are closed straight after the handshake when
// closeLater is set — a peer that accepts and immediately hangs up — and otherwise read until
// the client goes, acknowledging each Keepalive unless silent is set.
func newProbeMDF(t *testing.T, closeLater, die bool) *probeMDF {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	m := &probeMDF{ln: ln, addr: ln.Addr().String(), firstClosed: make(chan struct{})}

	go func() {
		first := true
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			m.accepted.Add(1)

			switch {
			case first:
				first = false

				go func(c net.Conn) {
					//nolint:errcheck // the close below is what this peer does either way
					_, _ = readPDU(c)
					if die {
						_ = ln.Close()
					}
					_ = c.Close()
					close(m.firstClosed)
				}(conn)
			case closeLater:
				go func(c net.Conn) {
					//nolint:errcheck // completing the handshake is all this peer does
					_ = c.(*tls.Conn).HandshakeContext(context.Background())
					_ = c.Close()
				}(conn)
			default:
				go func(c net.Conn) {
					defer c.Close()
					for {
						p, err := readPDU(c)
						if err != nil {
							return
						}
						if p.Type == PDUTypeX2 {
							m.later.Add(1)
						}
						if !m.silent.Load() {
							answerCorrectly(c, p)
						}
					}
				}(conn)
			}
		}
	}()

	return m
}

func (m *probeMDF) waitFirstClosed(t *testing.T) {
	t.Helper()

	select {
	case <-m.firstClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("the mediation function never closed the connection")
	}
}

type faultLog struct {
	mu     sync.Mutex
	faults []error
}

func (f *faultLog) record(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.faults = append(f.faults, err)
}

func (f *faultLog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.faults)
}

func (f *faultLog) all() []error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]error(nil), f.faults...)
}

// TestAMediationFunctionThatDiesWhileIdleIsFound: the mediation function closes the connection
// and stops listening — it crashed, or its pod went — while nothing was being delivered. The
// element must find that out from the close, not from the next delivery failing: dropping the
// connection also stops its keepalive timer, which is what would otherwise notice, and without
// a redial the destination would stay "reachable" until a unit had already been lost.
func TestAMediationFunctionThatDiesWhileIdleIsFound(t *testing.T) {
	t.Parallel()

	m := newProbeMDF(t, false, true)

	var faults faultLog
	// The specification's own timers: detection must come from the close, not from a
	// keepalive deadline that a short test timer would also satisfy.
	c := clientTo(t, m.addr, KeepaliveConfig{OnFault: faults.record})

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}

	// The mediation function goes away entirely: no listener to redial.
	m.waitFirstClosed(t)

	eventually(t, "the dead mediation function to be found unreachable", func() bool {
		return c.Unreachable() && faults.count() == 1
	})

	// And reported once, not once per something.
	time.Sleep(300 * time.Millisecond)
	if n := faults.count(); n != 1 {
		t.Errorf("the dead mediation function was reported %d times, want once", n)
	}
}

// TestAMediationFunctionThatClosesButStaysUpIsNoFault: an idle close by a mediation function
// that is still running — a load balancer's idle timeout — is ordinary. The element reconnects
// without being asked to deliver anything, raises nothing, and the next delivery arrives.
func TestAMediationFunctionThatClosesButStaysUpIsNoFault(t *testing.T) {
	m := newProbeMDF(t, false, false)

	var faults faultLog
	c := clientTo(t, m.addr, KeepaliveConfig{OnFault: faults.record})

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	m.waitFirstClosed(t)

	eventually(t, "the element to reconnect on its own", func() bool { return m.accepted.Load() == 2 })

	if err := c.Send(product()); err != nil {
		t.Fatalf("the delivery after the close failed: %v", err)
	}
	eventually(t, "the delivery to arrive", func() bool { return m.later.Load() == 1 })

	if c.Unreachable() || faults.count() != 0 {
		t.Errorf("a mediation function that closed an idle connection and answered the redial "+
			"was reported: unreachable=%v faults=%d", c.Unreachable(), faults.count())
	}
}

// TestAPeerThatHangsUpAtOnceIsNotRedialledInALoop: a peer that accepts and immediately closes
// would turn the redial after a close into a reconnect loop running as fast as the handshake.
// The probes are spaced by the redial interval.
func TestAPeerThatHangsUpAtOnceIsNotRedialledInALoop(t *testing.T) {
	t.Parallel()

	m := newProbeMDF(t, true, false)

	c := clientTo(t, m.addr, KeepaliveConfig{})
	c.redialInterval = 200 * time.Millisecond

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	m.waitFirstClosed(t)

	const watch = 1500 * time.Millisecond
	time.Sleep(watch)

	// One probe straight after the first close, then at most one per interval.
	limit := int64(watch/c.redialInterval) + 3
	if n := m.accepted.Load(); n > limit {
		t.Errorf("%d connections in %s against a peer that hangs up at once, want at most %d: "+
			"the redial after a close is not spaced", n, watch, limit)
	}
	if m.accepted.Load() < 3 {
		t.Errorf("only %d connections: the peer's closes were not followed by probes, so this "+
			"test is not exercising the spacing", m.accepted.Load())
	}
}

// TestCloseDoesNotWaitOutAProbe: a probe waiting out its spacing belongs to no connection, so a
// connection's wait group does not cover it. Close must stop it rather than wait for the
// interval, and leave no goroutine behind.
func TestCloseDoesNotWaitOutAProbe(t *testing.T) {
	m := newProbeMDF(t, true, false)

	c := NewClient(m.addr, &tls.Config{InsecureSkipVerify: true}, KeepaliveConfig{}) //nolint:gosec // test transport
	c.redialInterval = 10 * time.Second

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	m.waitFirstClosed(t)

	// The first probe is immediate and its connection is hung up on, so the second waits
	// out the ten-second spacing.
	eventually(t, "the first probe's connection", func() bool { return m.accepted.Load() == 2 })
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Close took %s: it waited out a probe's spacing instead of stopping it", took)
	}

	// Counted by what they run rather than against a baseline: the mediation function's own
	// goroutines come and go on their own schedule, and a count would have to allow for them.
	eventually(t, "the client's goroutines to exit", func() bool { return clientGoroutines() == 0 })
}

// clientGoroutines counts the goroutines running a Client's own background work: keepalive
// timers, readers and post-close probes.
func clientGoroutines() int {
	buf := make([]byte, 1<<20)
	stacks := string(buf[:runtime.Stack(buf, true)])

	n := 0
	for _, fn := range []string{
		"(*Client).probeAfterPeerClose", "(*Client).keepaliveLoop",
		"(*Client).readLoop", "(*Client).watchClose",
	} {
		n += strings.Count(stacks, "x2x3."+fn+"(")
	}

	return n
}

// TestDeliveriesBetweenProbesDoNotMultiplyThem: every connection the peer closes asks for a
// probe, and deliveries reconnect between them, so closes can arrive while a probe is still
// waiting out its spacing. A probe per close would wake beside the others and dial too, so the
// spacing would hold per probe but not per destination. One probe at a time answers for every
// close that arrives while it is in flight.
func TestDeliveriesBetweenProbesDoNotMultiplyThem(t *testing.T) {
	t.Parallel()

	m := newProbeMDF(t, true, false)

	c := clientTo(t, m.addr, KeepaliveConfig{})
	c.redialInterval = 500 * time.Millisecond

	for range 20 {
		_ = c.Send(product()) //nolint:errcheck // a peer that hangs up may refuse some of these
		time.Sleep(20 * time.Millisecond)
	}

	before, start := m.accepted.Load(), time.Now()
	time.Sleep(1200 * time.Millisecond)
	n, watched := m.accepted.Load()-before, time.Since(start)

	// With no deliveries in the window, only probes dial: at most one per interval, plus one
	// already due when the window opened. Measured against the time that actually passed, so
	// a sleep that overran on a loaded runner allows the probes that overrun legitimately made.
	limit := int64(watched/c.redialInterval) + 1
	if n > limit {
		t.Errorf("%d dials in %s with no deliveries, want at most %d: the closes during the "+
			"deliveries each left a probe behind, and they dialled together", n, watched.Round(time.Millisecond), limit)
	}
}

// TestAProbeThatReconnectsIsAcknowledgedBeforeTheDestinationIsReachable: a probe that
// reconnects to a mediation function that acknowledges its Keepalive is the element finding
// it reachable again. Left unmarked, a fault raised for an earlier outage would stay standing
// until traffic happened to flow. The connection sends that Keepalive at once, so the fault is
// retracted within a round trip rather than after TIME_P1 — which here is the specification's
// minute, longer than the wait.
func TestAProbeThatReconnectsIsAcknowledgedBeforeTheDestinationIsReachable(t *testing.T) {
	m := newProbeMDF(t, false, false)

	c := clientTo(t, m.addr, KeepaliveConfig{})

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	// An outage the element concluded earlier, still standing when the peer closes.
	c.unreachable.Store(true)
	m.waitFirstClosed(t)

	eventually(t, "the element to reconnect on its own", func() bool { return m.accepted.Load() == 2 })
	eventually(t, "the destination to be marked reachable", func() bool { return !c.Unreachable() })
}

// TestADialThatSucceedsIsNotEvidenceOfReachability: a mediation function that refuses this
// element at the TLS layer accepts the dial all the same, so a probe whose dial succeeded has
// not found the destination reachable. Only an acknowledgement says that, and this one never
// acknowledges.
func TestADialThatSucceedsIsNotEvidenceOfReachability(t *testing.T) {
	t.Parallel()

	m := newProbeMDF(t, false, false)
	m.silent.Store(true)

	c := clientTo(t, m.addr, KeepaliveConfig{})

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	c.unreachable.Store(true)
	m.waitFirstClosed(t)

	eventually(t, "the element to reconnect on its own", func() bool { return m.accepted.Load() == 2 })
	time.Sleep(300 * time.Millisecond)

	if !c.Unreachable() {
		t.Error("a probe's successful dial marked the destination reachable, with no Keepalive " +
			"acknowledged on it")
	}
}
