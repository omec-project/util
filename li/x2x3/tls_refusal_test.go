// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// refusingMDF is a mediation function that requires a client certificate and, until trust is
// set, trusts none. TLS 1.3 completes the client's half of the handshake before the server has
// checked the certificate, so the element's dial succeeds and its first write is taken into the
// socket; the refusal arrives only as an alert. Once trust is set it accepts any certificate
// and acknowledges Keepalives.
type refusingMDF struct {
	addr     string
	trust    atomic.Bool
	accepted atomic.Int64
	// product counts the X2 PDUs received over trusted connections.
	product atomic.Int64
	// reset closes a refused connection with a reset rather than a FIN: no drain, and a zero
	// linger, so whatever the element wrote is still unread when the socket goes.
	reset bool
}

func newRefusingMDF(t *testing.T) *refusingMDF {
	t.Helper()

	return startRefusingMDF(t, false)
}

// newResettingMDF is a refusingMDF that closes each refused connection with a reset. The
// element's next write then fails with ECONNRESET or EPIPE, and the writer can drop the
// connection before the reader has seen the alert: the refusal has to survive that.
func newResettingMDF(t *testing.T) *refusingMDF {
	t.Helper()

	return startRefusingMDF(t, true)
}

func startRefusingMDF(t *testing.T, reset bool) *refusingMDF {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	m := &refusingMDF{addr: ln.Addr().String(), reset: reset}
	cert := selfSignedServer(t)

	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			m.accepted.Add(1)

			go m.serve(raw, cert)
		}
	}()

	return m
}

func (m *refusingMDF) serve(raw net.Conn, cert tls.Certificate) {
	defer raw.Close()

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		// An empty pool: no client certificate verifies.
		ClientAuth: tls.RequireAndVerifyClientCert,
	}
	if m.trust.Load() {
		cfg.ClientAuth = tls.RequireAnyClientCert
	}

	conn := tls.Server(raw, cfg)
	if err := conn.HandshakeContext(context.Background()); err != nil {
		if m.reset {
			if tcp, ok := raw.(*net.TCPConn); ok {
				//nolint:errcheck // a linger the socket will not take only softens the close
				_ = tcp.SetLinger(0)
			}

			return
		}
		// The alert has been sent. Drain what the client already wrote before closing, so
		// the close is a FIN and the client reads the alert rather than a reset.
		//nolint:errcheck // a drain that fails has nothing left to drain
		_ = raw.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		//nolint:errcheck // as above
		_, _ = io.Copy(io.Discard, raw)

		return
	}

	for {
		p, err := readPDU(conn)
		if err != nil {
			return
		}
		if p.Type == PDUTypeX2 {
			m.product.Add(1)
		}
		answerCorrectly(conn, p)
	}
}

// clientWithCert is a client presenting a certificate the refusing mediation function does
// not trust, with a short redial interval so the test sees several redials.
func clientWithCert(t *testing.T, addr string, ka KeepaliveConfig) *Client {
	t.Helper()

	c := NewClient(addr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test transport: the server's side is under test
		Certificates:       []tls.Certificate{selfSignedServer(t)},
		MinVersion:         tls.VersionTLS13,
	}, ka)
	c.redialInterval = 100 * time.Millisecond
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// TestAMediationFunctionThatRefusesTheCertificateIsReported: the refusal is an alert on the
// first read, not a close. Read as a close it would be redialled, accepted, written to and
// refused again — every delivery returning nil, the destination reported reachable, and every
// unit lost. It must be reported once, mark the destination unreachable for as long as it
// stands, and turn every delivery into a reported loss.
func TestAMediationFunctionThatRefusesTheCertificateIsReported(t *testing.T) {
	t.Parallel()

	m := newRefusingMDF(t)

	var faults faultLog
	c := clientWithCert(t, m.addr, KeepaliveConfig{OnFault: faults.record})

	// The first delivery is written before the refusal can arrive, so it may well report
	// success: nothing on this side can know otherwise yet.
	_ = c.Send(product()) //nolint:errcheck // see above

	eventually(t, "the refusal to be reported", func() bool { return c.Unreachable() && faults.count() == 1 })

	const watch = time.Second
	sends, delivered := 0, 0
	for start := time.Now(); time.Since(start) < watch; time.Sleep(20 * time.Millisecond) {
		sends++
		if c.Send(product()) == nil {
			delivered++
		}
		if !c.Unreachable() {
			t.Fatal("a destination refusing this element was reported reachable")
		}
	}

	if delivered != 0 {
		t.Errorf("%d of %d deliveries to a mediation function refusing this element reported "+
			"success: each was lost without a trace", delivered, sends)
	}
	if n := faults.count(); n != 1 {
		t.Errorf("the refusal was reported %d times, want once while it stands", n)
	}
	// It is redialled, so a fix is found, but spaced by the redial interval rather than once
	// per delivery: the probe after each refusal dials at most once per interval, and so do the
	// deliveries, which may each win the race to dial in an interval.
	limit := 2*int64(watch/c.redialInterval) + 4
	if n := m.accepted.Load(); n < 3 || n > limit {
		t.Errorf("%d connections in %s against a refusing peer, want between 3 and %d", n, watch, limit)
	}
}

// TestARefusalEndsWithTheFirstAcknowledgement: once the mediation function trusts the element,
// the redial's prompt Keepalive is acknowledged, the refusal is lifted, and product flows again
// — without waiting TIME_P1, which here is the specification's minute.
func TestARefusalEndsWithTheFirstAcknowledgement(t *testing.T) {
	m := newRefusingMDF(t)

	var faults faultLog
	c := clientWithCert(t, m.addr, KeepaliveConfig{OnFault: faults.record})

	_ = c.Send(product()) //nolint:errcheck // the refusal is what is under test
	eventually(t, "the refusal to be reported", func() bool { return c.Unreachable() && faults.count() == 1 })

	m.trust.Store(true)

	eventually(t, "the destination to be found reachable again", func() bool { return !c.Unreachable() })
	eventually(t, "a delivery to succeed", func() bool { return c.Send(product()) == nil })
	eventually(t, "the product to arrive", func() bool { return m.product.Load() > 0 })
}

// TestARefusalIsReportedWithTheMechanismDisabled: without keepalives nothing can show that the
// mediation function has accepted the element, so nothing stands between refusals — but each
// is still a refusal, reported and marked, and not an idle close.
func TestARefusalIsReportedWithTheMechanismDisabled(t *testing.T) {
	m := newRefusingMDF(t)

	var faults faultLog
	c := clientWithCert(t, m.addr, KeepaliveConfig{Disabled: true, OnFault: faults.record})

	_ = c.Send(product()) //nolint:errcheck // the refusal is what is under test

	eventually(t, "the refusal to be reported", func() bool { return c.Unreachable() && faults.count() >= 1 })
}

// TestOnlyATLSFailureIsARefusal pins the classification: an ordinary close, this element's own
// close and a reset are closes, which the probe that follows them sorts out; an alert, either
// way, and bytes that are not TLS are refusals.
func TestOnlyATLSFailureIsARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"end of stream", io.EOF, false},
		{"this element's own close", fmt.Errorf("read: %w", net.ErrClosed), false},
		{"a reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, false},
		{"the peer's alert", &net.OpError{Op: "remote error", Err: errors.New("tls: unknown certificate authority")}, true},
		{"this side's alert", &net.OpError{Op: "local error", Err: errors.New("tls: bad record MAC")}, true},
		{"not TLS", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusedAtTLS(tc.err); got != tc.want {
				t.Errorf("refusedAtTLS(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestARefusalClosedWithAResetIsNotLost: the mediation function refuses and then resets the
// connection, so the element's next write — the redial's prompt Keepalive — can fail and drop
// the connection before the reader has read the alert. The refusal must survive that race:
// something must still redial, so a mediation function that comes to trust the element is
// found again with no traffic offered at all.
func TestARefusalClosedWithAResetIsNotLost(t *testing.T) {
	t.Parallel()

	m := newResettingMDF(t)
	c := clientWithCert(t, m.addr, KeepaliveConfig{})

	_ = c.Send(product()) //nolint:errcheck // the refusal is what is under test
	eventually(t, "the refusal to mark the destination unreachable", c.Unreachable)

	// Several redials against the refusal, so a race lost on any of them shows.
	time.Sleep(5 * c.redialInterval)
	m.trust.Store(true)

	eventually(t, "the destination to be found reachable again without traffic", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()

		return !c.Unreachable() && c.rejection == nil
	})
}

// TestARefusalClosedWithAResetIsReportedOnce: under continuous traffic a resetting refusal is
// met by writers and readers alike — deliveries, prompt Keepalives, the redials between them.
// Whichever of them notices it first, the refusal is one episode and is reported once.
func TestARefusalClosedWithAResetIsReportedOnce(t *testing.T) {
	t.Parallel()

	m := newResettingMDF(t)

	var faults faultLog
	c := clientWithCert(t, m.addr, KeepaliveConfig{OnFault: faults.record})

	for start := time.Now(); time.Since(start) < 1500*time.Millisecond; time.Sleep(20 * time.Millisecond) {
		_ = c.Send(product()) //nolint:errcheck // the refusal is what is under test
	}

	if n := faults.count(); n != 1 {
		t.Errorf("one refusal was reported %d times, want once: %v", n, faults.all())
	}
}
