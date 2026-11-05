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

// recoveringDialer fails every dial until up is set, then hands out connections to a mediation
// function that acknowledges every Keepalive.
type recoveringDialer struct {
	dials atomic.Int64
	up    atomic.Bool
}

func (d *recoveringDialer) dial(context.Context) (net.Conn, error) {
	d.dials.Add(1)
	if !d.up.Load() {
		return nil, errors.New("connection refused")
	}

	client, server := net.Pipe()
	go func() {
		defer server.Close()
		for {
			p, err := readPDU(server)
			if err != nil {
				return
			}
			answerCorrectly(server, p)
		}
	}()

	return client, nil
}

// TestAnIdleDestinationIsRedialledUntilItAnswers: every path that leaves a client with no
// connection and nothing to deliver is followed by a probe, and the probe keeps dialling while
// the mediation function cannot be reached. A probe that dialled once and stopped left the
// destination reported unreachable — its fault standing over a mediation function that had
// come back — until traffic happened to arrive, which for an idle subject may be never.
//
// Each case: the mediation function is down, the probe's dials fail one per redialInterval and
// no more, the outage is reported once and not once per failed dial, and when the mediation
// function comes back the client reconnects, is acknowledged and stops reporting it
// unreachable — with nothing delivered.
func TestAnIdleDestinationIsRedialledUntilItAnswers(t *testing.T) {
	t.Parallel()

	const interval = 20 * time.Millisecond
	ok := func(b []byte) (int, error) { return len(b), nil }
	reset := func([]byte) (int, error) { return 0, errReset }

	for _, tc := range []struct {
		name string
		// drop leaves c with no connection and nothing to deliver.
		drop func(c *Client)
		// faults is how many times the outage is reported, however many dials fail.
		faults int
	}{
		{"a keepalive expired and its redial failed", func(c *Client) {
			c.expire(heldConn(c, newScriptedConn(ok)))
		}, 1},
		{"the peer sent something that is not X2/X3", func(c *Client) {
			c.protocolError(heldConn(c, newScriptedConn(ok)), errors.New("x2x3: garbage"))
		}, 1},
		{"a delivery's write and its redial both failed", func(c *Client) {
			heldConn(c, newScriptedConn(reset))
			_ = c.Send(product()) //nolint:errcheck // the failure is the point; the caller saw it
		}, 0},
		{"a delivery's dial failed", func(c *Client) {
			_ = c.Send(product()) //nolint:errcheck // as above
		}, 0},
		{"a keepalive's write failed", func(c *Client) {
			c.sendKeepalive(heldConn(c, newScriptedConn(reset)))
		}, 1},
		{"an acknowledgement's write failed", func(c *Client) {
			c.answerKeepalive(heldConn(c, newScriptedConn(reset)), Keepalive(7))
		}, 1},
		{"the peer closed the connection", func(c *Client) {
			c.peerClosed(heldConn(c, newScriptedConn(ok)))
		}, 1},
		{"a refusal was read after a writer dropped the connection", func(c *Client) {
			st := heldConn(c, newScriptedConn(ok))
			c.mu.Lock()
			c.dropLocked()
			c.mu.Unlock()
			c.refused(st, &net.OpError{Op: "remote error", Err: errors.New("tls: unknown certificate authority")})
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := &recoveringDialer{}
			var faults faultLog
			c := NewClient("mdf.example:1", nil, idleKeepalive(faults.record))
			c.dialConn = d.dial
			c.redialInterval = interval
			t.Cleanup(func() { _ = c.Close() })

			tc.drop(c)
			base := d.dials.Load()

			// It goes on dialling past a failed dial — which a probe that stopped at one
			// does not — and no faster than once per interval.
			start := time.Now()
			eventually(t, "the probe to keep dialling", func() bool { return d.dials.Load() >= base+3 })
			time.Sleep(10 * interval)
			elapsed, dials := time.Since(start), d.dials.Load()-base
			if limit := int64(elapsed/interval) + 2; dials > limit {
				t.Errorf("%d dials in %s at a %s interval, want at most %d", dials, elapsed, interval, limit)
			}
			if !c.Unreachable() {
				t.Error("the destination is not unreachable while every dial fails")
			}

			d.up.Store(true)

			eventually(t, "the recovered mediation function to be found with nothing delivered", func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()

				return c.live != nil && c.rejection == nil && !c.Unreachable()
			})
			if n := faults.count(); n != tc.faults {
				t.Errorf("the outage was reported %d times, want %d: %v", n, tc.faults, faults.all())
			}
		})
	}
}

// TestCloseStopsAProbeThatKeepsDialling: a probe against a mediation function that never
// comes back dials for the life of the client, so Close must end it — promptly, between dials or
// during one — and leave no goroutine behind.
func TestCloseStopsAProbeThatKeepsDialling(t *testing.T) {
	d := &recoveringDialer{}
	c := NewClient("mdf.example:1", nil, idleKeepalive(nil))
	c.dialConn = d.dial
	c.redialInterval = 20 * time.Millisecond

	c.peerClosed(heldConn(c, newScriptedConn(func(b []byte) (int, error) { return len(b), nil })))
	eventually(t, "the probe to keep dialling", func() bool { return d.dials.Load() >= 3 })

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Close took %s with a probe dialling", took)
	}

	after := d.dials.Load()
	eventually(t, "the client's goroutines to exit", func() bool { return clientGoroutines() == 0 })
	time.Sleep(100 * time.Millisecond)
	if n := d.dials.Load(); n != after {
		t.Errorf("%d dials after Close", n-after)
	}
}

// TestARefusingMediationFunctionThatComesBackFixedIsFoundWhileIdle: the mediation function
// refuses this element's certificate, then goes down — restarted, say, with its trust store
// fixed — and comes back accepting it. Nothing is being delivered. The probe dials through the
// outage, so the restarted mediation function is found, acknowledges the prompt Keepalive, and
// the refusal and its fault are retracted, with the refusal reported once throughout.
func TestARefusingMediationFunctionThatComesBackFixedIsFoundWhileIdle(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &refusingMDF{addr: ln.Addr().String()}
	cert := selfSignedServer(t)
	serve := func(ln net.Listener) {
		for {
			raw, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			m.accepted.Add(1)

			go m.serve(raw, cert)
		}
	}
	go serve(ln)

	var faults faultLog
	c := clientWithCert(t, m.addr, KeepaliveConfig{TimeP1: time.Hour, TimeP2: 2 * time.Hour, OnFault: faults.record})

	_ = c.Send(product()) //nolint:errcheck // the refusal may or may not be met by this write
	eventually(t, "the refusal to be recorded", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()

		return c.rejection != nil
	})

	// It goes down, and the probe's dials fail.
	_ = ln.Close()
	eventually(t, "a probe dial to fail", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()

		return c.lastDialErr != nil && c.live == nil
	})
	time.Sleep(600 * time.Millisecond)

	// And comes back fixed, on the same address.
	m.trust.Store(true)
	ln2, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", m.addr)
	if err != nil {
		t.Fatalf("re-listening on %s: %v", m.addr, err)
	}
	t.Cleanup(func() { _ = ln2.Close() })
	go serve(ln2)

	eventually(t, "the fixed mediation function to be found with nothing delivered", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()

		return c.live != nil && c.rejection == nil && !c.Unreachable()
	})
	if n := faults.count(); n != 1 {
		t.Errorf("the refusal and the outage after it were reported %d times, want once: %v", n, faults.all())
	}
}

// TestAPeerSpeakingGarbageOnEveryConnectionIsReportedOnce: the probe that follows a protocol
// error reconnects once per redialInterval, and a peer that answers every connection with
// something that is not X2/X3 meets each reconnect with the same error. Reported each time, one
// broken mediation function would be reported once per reconnect; it is reported once, and the
// destination stands unreachable throughout.
func TestAPeerSpeakingGarbageOnEveryConnectionIsReportedOnce(t *testing.T) {
	t.Parallel()

	garbage, err := product().Marshal()
	if err != nil {
		t.Fatal(err)
	}

	var dials atomic.Int64
	var faults faultLog
	c := NewClient("mdf.example:1", nil, idleKeepalive(faults.record))
	c.redialInterval = 20 * time.Millisecond
	c.dialConn = func(context.Context) (net.Conn, error) {
		dials.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			// Product on a delivery connection: a protocol error at the element.
			_, _ = server.Write(garbage) //nolint:errcheck // the element may close first
			for {
				if _, err := readPDU(server); err != nil {
					return
				}
			}
		}()

		return client, nil
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Send(product()); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}

	eventually(t, "several reconnects to the broken peer", func() bool { return dials.Load() >= 5 })
	if n := faults.count(); n != 1 {
		t.Errorf("a peer speaking garbage on %d connections was reported %d times, want once: %v",
			dials.Load(), n, faults.all())
	}
	if !c.Unreachable() {
		t.Error("a peer that speaks garbage on every connection is not unreachable")
	}
}
