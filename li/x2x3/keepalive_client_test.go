// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"crypto/tls"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A mediation function whose answering behaviour each test chooses, because the
// behaviour under test is what this element does when a peer misbehaves — and the
// peers that misbehave are the ones no reading can predict.
type testMDF struct {
	addr string

	mu       sync.Mutex
	got      []*PDU
	conns    []net.Conn
	keepFrom chan *PDU // keepalives received, for tests that assert on them
}

// startMDF listens and runs on for every PDU it receives. on may write to the
// connection, which is how a test plays an MDF that answers, answers wrongly, or
// says something no MDF should.
func startMDF(t *testing.T, on func(conn net.Conn, p *PDU)) *testMDF {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedServer(t)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	m := &testMDF{addr: ln.Addr().String(), keepFrom: make(chan *PDU, 64)}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			m.mu.Lock()
			m.conns = append(m.conns, conn)
			m.mu.Unlock()

			go func(c net.Conn) {
				defer c.Close()
				for {
					p, err := readPDU(c)
					if err != nil {
						return
					}

					m.mu.Lock()
					m.got = append(m.got, p)
					m.mu.Unlock()

					if p.Type == PDUTypeKeepalive {
						select {
						case m.keepFrom <- p:
						default:
						}
					}
					if on != nil {
						on(c, p)
					}
				}
			}(conn)
		}
	}()

	return m
}

// answerCorrectly is the MDF half of clause 6.2.4: acknowledge each Keepalive with
// its own Sequence Number.
func answerCorrectly(c net.Conn, p *PDU) {
	if p.Type != PDUTypeKeepalive {
		return
	}
	seq, _ := KeepaliveSequence(p)
	b, err := KeepaliveAck(seq).Marshal()
	if err != nil {
		return
	}
	//nolint:errcheck // a test MDF that cannot write has already failed the test it serves
	_, _ = c.Write(b)
}

func (m *testMDF) count(typ PDUType) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, p := range m.got {
		if p.Type == typ {
			n++
		}
	}

	return n
}

func (m *testMDF) firstConn(t *testing.T) net.Conn {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		if len(m.conns) > 0 {
			c := m.conns[0]
			m.mu.Unlock()

			return c
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the element never connected")

	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fastKeepalive is the mechanism at test speed. The ratio of the two timers is the
// specification's own — TIME_P2 is three TIME_P1s — because the behaviour that
// matters is how many acknowledgements may be missed before a connection goes.
func fastKeepalive(onFault func(error)) KeepaliveConfig {
	return KeepaliveConfig{TimeP1: 20 * time.Millisecond, TimeP2: 60 * time.Millisecond, OnFault: onFault}
}

// steadyKeepalive is the mechanism at a speed a loaded test runner can keep up with, for the
// tests that assert a connection is *not* torn down. Their failure mode is a test peer that
// answers late because the machine is busy, so they get a TIME_P2 that a scheduling delay
// cannot reach; fastKeepalive stays for the tests that wait for an expiry. Same ratio.
func steadyKeepalive(onFault func(error)) KeepaliveConfig {
	return KeepaliveConfig{TimeP1: 100 * time.Millisecond, TimeP2: 300 * time.Millisecond, OnFault: onFault}
}

func clientTo(t *testing.T, addr string, ka KeepaliveConfig) *Client {
	t.Helper()

	c := NewClient(addr, &tls.Config{InsecureSkipVerify: true}, ka) //nolint:gosec // test transport
	t.Cleanup(func() { _ = c.Close() })

	return c
}

func product() *PDU {
	return &PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable, Payload: []byte("x")}
}

// TestKeepaliveNeverDials: the keepalive runs only on a connection a delivery dialled, so a
// client that has delivered nothing opens no connection to an agency however many TIME_P1
// periods pass, and its fault probe answers "not looked" rather than "unreachable". It fails
// if constructing a client, rather than delivering through it, starts the mechanism.
func TestKeepaliveNeverDials(t *testing.T) {
	t.Parallel()

	m := startMDF(t, answerCorrectly)
	c := clientTo(t, m.addr, fastKeepalive(nil))

	// Several TIME_P1 periods with nothing sent.
	time.Sleep(200 * time.Millisecond)

	m.mu.Lock()
	conns := len(m.conns)
	m.mu.Unlock()

	if conns != 0 {
		t.Errorf("the keepalive timer opened %d connections; it must only run on a connection a delivery dialled", conns)
	}
	if c.Unreachable() {
		t.Error("Unreachable() is true before anything was sent — an element that has not looked has not found its MDF unreachable")
	}
}

// TestKeepaliveIsSentOnAnIdleConnection is clause 6.2.4's "at least every TIME_P1".
func TestKeepaliveIsSentOnAnIdleConnection(t *testing.T) {
	m := startMDF(t, answerCorrectly)
	c := clientTo(t, m.addr, fastKeepalive(nil))

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	eventually(t, "two keepalives on an idle connection", func() bool { return m.count(PDUTypeKeepalive) >= 2 })
}

// TestKeepaliveIsSentWhileProductFlows pins why the timer is not an idle timer: product PDUs are not acknowledged, so a busy
// connection proves nothing about the mediation function behind it. An idle-reset
// timer passes every other test here and fails this one.
func TestKeepaliveIsSentWhileProductFlows(t *testing.T) {
	m := startMDF(t, answerCorrectly)
	c := clientTo(t, m.addr, fastKeepalive(nil))

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			//nolint:errcheck // a send that fails mid-run is the redial path, not this test's subject
			_ = c.Send(product())
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { close(stop); <-done }()

	eventually(t, "keepalives on a connection carrying product continuously", func() bool {
		return m.count(PDUTypeKeepalive) >= 2 && m.count(PDUTypeX2) > 10
	})
}

// TestKeepaliveSurvivesASaturatedDeliveryQueue is the failure the mechanism most needs
// not to have: a mediation function that is merely busy must not be judged dead.
//
// The delivery queue drops when it is full, by design. If a keepalive travelled that
// path it would be dropped exactly when the element is busiest, the acknowledgement
// would never come, and TIME_P2 would tear down a connection that was working — losing
// product because delivery was working hard.
func TestKeepaliveSurvivesASaturatedDeliveryQueue(t *testing.T) {
	m := startMDF(t, answerCorrectly)
	c := clientTo(t, m.addr, steadyKeepalive(nil))

	var drops atomic.Int64
	a := NewAsyncSender(c, 1, nil, func() { drops.Add(1) })
	defer a.Close()

	for range 500 {
		// Never blocks: a full queue drops, which is the point. The error is ErrNilPDU or
		// nothing, and the unit here is never nil.
		//nolint:errcheck // see above
		_ = a.Send(product())
	}

	if drops.Load() == 0 {
		t.Skip("the delivery queue never filled, so this run cannot say anything about a saturated one")
	}

	eventually(t, "keepalives while the delivery queue is dropping", func() bool { return m.count(PDUTypeKeepalive) >= 1 })

	if c.Unreachable() {
		t.Error("a busy mediation function that answers keepalives was reported unreachable")
	}
}

// TestKeepaliveExpiryDisconnectsAndReports is the TIME_P2 rule: disconnect, reconnect,
// report over X1 — the third of which arrives here as the fault callback a POI wires to
// its destination-unreachable report.
func TestKeepaliveExpiryDisconnectsAndReports(t *testing.T) {
	m := startMDF(t, nil) // an MDF that never acknowledges

	faults := make(chan error, 8)
	c := clientTo(t, m.addr, fastKeepalive(func(err error) {
		select {
		case faults <- err:
		default:
		}
	}))

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "TIME_P2") {
			t.Errorf("fault = %v, want the TIME_P2 expiry", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no fault reported for a mediation function that never acknowledged")
	}

	if !c.Unreachable() {
		t.Error("Unreachable() is false after a TIME_P2 expiry — the fault probe would answer that all is well")
	}
	// "shall attempt to reconnect": a second connection to the MDF, without any
	// further delivery having been asked for.
	eventually(t, "the reconnect clause 6.2.4 requires", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()

		return len(m.conns) >= 2
	})
}

// TestKeepaliveToleratesTwoMissedAcknowledgements pins the arithmetic of the default
// timers: TIME_P2 is three TIME_P1s, so two lost acknowledgements are survivable and
// the deadline runs from the last one seen rather than from each Keepalive.
func TestKeepaliveToleratesTwoMissedAcknowledgements(t *testing.T) {
	var (
		answer  atomic.Bool
		acked   atomic.Int64
		lastAck atomic.Int64 // UnixNano of the most recent acknowledgement sent
	)
	answer.Store(true)

	m := startMDF(t, func(c net.Conn, p *PDU) {
		if p.Type == PDUTypeKeepalive && answer.Load() {
			answerCorrectly(c, p)
			lastAck.Store(time.Now().UnixNano())
			acked.Add(1)
		}
	})

	faults := make(chan error, 4)
	ka := steadyKeepalive(func(err error) {
		select {
		case faults <- err:
		default:
		}
	})
	c := clientTo(t, m.addr, ka)

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	// An acknowledgement actually sent, not merely a keepalive received: waiting on the
	// latter could stop the answers before the first one went out, and the deadline would
	// then run from the connection rather than from an acknowledgement.
	eventually(t, "an acknowledgement to be sent", func() bool { return acked.Load() >= 1 })

	// Two periods of silence, measured from the last acknowledgement: less than TIME_P2,
	// so nothing should happen. Measured rather than slept, so a slow machine cannot turn
	// "two periods" into three.
	answer.Store(false)
	time.Sleep(time.Until(time.Unix(0, lastAck.Load()).Add(2 * ka.TimeP1)))

	select {
	case err := <-faults:
		t.Fatalf("disconnected after two missed acknowledgements: %v", err)
	default:
	}

	// Keep quiet past TIME_P2 and it must go.
	select {
	case <-faults:
	case <-time.After(3 * time.Second):
		t.Fatal("silence past TIME_P2 did not disconnect")
	}
}

// TestAReplayedAcknowledgementDoesNotPostponeTheDeadline is the fail-safe of
// TestKeepaliveWrongSequenceDoesNotPostponeTheDeadline against material that is genuine
// rather than invented.
//
// That test answers with a number this element never sent, which is the easy half: an
// acknowledgement of nothing acknowledges nothing. The hard half is a number it *did* send,
// returned again — a middlebox that repeats, a peer stuck in a loop, a recorded exchange
// replayed. Every one of those satisfies "something is answering", so a duplicate does not
// refresh the deadline either. A guard on `seq >= issued()` alone would let a replay of a
// real acknowledgement through, which is why acceptance is monotonic.
//
// The MDF here answers the first Keepalive correctly and then repeats that first
// acknowledgement for every Keepalive after it. So the peer is answering, continuously, with
// something it was genuinely given — and the element must still let TIME_P2 expire.
func TestAReplayedAcknowledgementDoesNotPostponeTheDeadline(t *testing.T) {
	var (
		mu    sync.Mutex
		first []byte
	)

	m := startMDF(t, func(c net.Conn, p *PDU) {
		if p.Type != PDUTypeKeepalive {
			return
		}

		mu.Lock()
		if first == nil {
			seq, ok := KeepaliveSequence(p)
			if !ok {
				mu.Unlock()

				return
			}
			b, err := KeepaliveAck(seq).Marshal()
			if err != nil {
				mu.Unlock()

				return
			}
			first = b
		}
		replay := first
		mu.Unlock()

		//nolint:errcheck // test MDF; a failed write is the connection going away
		_, _ = c.Write(replay)
	})

	faults := make(chan error, 4)
	c := clientTo(t, m.addr, fastKeepalive(func(err error) {
		select {
		case faults <- err:
		default:
		}
	}))

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "TIME_P2") {
			t.Errorf("the connection ended with %v, want the TIME_P2 fail-safe", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("an MDF replaying one genuine acknowledgement held the fail-safe open " +
			"indefinitely: the numbers were real, so the never-issued check let them through, " +
			"and a mediation function that has stopped taking product looks alive for as long " +
			"as something repeats itself")
	}

	if !c.Unreachable() {
		t.Error("the destination is still reported reachable after TIME_P2 expired over it")
	}
}

// TestKeepaliveWrongSequenceDoesNotPostponeTheDeadline is the two halves of what an
// unusable acknowledgement means, and they pull in opposite directions.
//
// It is not a protocol error: an MDF whose numbering is wrong is still carrying
// product, and tearing the connection down over it would cost product to punish a defect
// that costs none. So the reader continues and the mismatch is counted.
//
// But it does not refresh TIME_P2 either. Clause 6.2.4 numbers an acknowledgement from
// the Keepalive it answers, so the number is the only evidence available that the peer is
// answering *us*; without it, "something is alive at the other end" is satisfied equally by
// a peer stuck in a loop, a middlebox replaying, and an endpoint a misroute put in the path —
// each of which would hold the fail-safe open over a mediation function that has stopped
// taking product.
//
// So the outcome is the fail-safe doing its job: the connection ends at TIME_P2, and the
// fault says so rather than naming a protocol error.
func TestKeepaliveWrongSequenceDoesNotPostponeTheDeadline(t *testing.T) {
	m := startMDF(t, func(c net.Conn, p *PDU) {
		if p.Type != PDUTypeKeepalive {
			return
		}
		b, err := KeepaliveAck(0xDEADBEEF).Marshal() // a number this element never sent
		if err != nil {
			return
		}
		//nolint:errcheck // as above
		_, _ = c.Write(b)
	})

	faults := make(chan error, 4)
	c := clientTo(t, m.addr, fastKeepalive(func(err error) {
		select {
		case faults <- err:
		default:
		}
	}))

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	eventually(t, "the mismatch to be noticed", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()

		return c.live != nil && c.live.mismatches.Load() > 0
	})

	// The deadline is not postponed by traffic that acknowledges nothing this connection
	// sent, so the fail-safe fires.
	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "TIME_P2") {
			t.Errorf("the connection ended with %v, want the TIME_P2 fail-safe: an unusable "+
				"acknowledgement must not be treated as a protocol error, only as evidence of "+
				"nothing", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("an MDF answering only with numbers this element never sent held the fail-safe " +
			"open indefinitely: a peer stuck in a loop, a replaying middlebox and a misrouted " +
			"endpoint all satisfy \"something is answering\", and each of them takes no product")
	}

	if !c.Unreachable() {
		t.Error("the destination is still reported reachable after TIME_P2 expired over it")
	}
}

// TestAValidAcknowledgementDoesPostponeTheDeadline is the other side of the same rule, and
// the one that keeps the sequence check from causing an outage: a peer answering with the
// number it was sent holds the connection open indefinitely.
func TestAValidAcknowledgementDoesPostponeTheDeadline(t *testing.T) {
	m := startMDF(t, func(c net.Conn, p *PDU) {
		if p.Type != PDUTypeKeepalive {
			return
		}
		seq, ok := KeepaliveSequence(p)
		if !ok {
			return
		}
		b, err := KeepaliveAck(seq).Marshal() // the number this element actually sent
		if err != nil {
			return
		}
		//nolint:errcheck // as above
		_, _ = c.Write(b)
	})

	faults := make(chan error, 4)
	ka := steadyKeepalive(func(err error) {
		select {
		case faults <- err:
		default:
		}
	})
	c := clientTo(t, m.addr, ka)

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// Four TIME_P2 periods: long enough that a deadline the acknowledgements did not
	// postpone would have expired several times over.
	select {
	case err := <-faults:
		t.Errorf("a peer answering with the numbers it was sent was disconnected: %v", err)
	case <-time.After(4 * ka.TimeP2):
	}
	if c.Unreachable() {
		t.Error("a peer acknowledging every keepalive was reported unreachable")
	}
}

// TestKeepaliveAnswersAnInboundKeepalive is the MDF-facing half of clause 6.2.4:
// both PDU types "shall be supported by POIs and MDFs".
func TestKeepaliveAnswersAnInboundKeepalive(t *testing.T) {
	acks := make(chan *PDU, 4)
	m := startMDF(t, func(c net.Conn, p *PDU) {
		if p.Type == PDUTypeKeepaliveAck {
			acks <- p
		}
	})

	c := clientTo(t, m.addr, KeepaliveConfig{TimeP1: time.Hour, TimeP2: time.Hour})
	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	conn := m.firstConn(t)
	b, err := Keepalive(99).Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := conn.Write(b); err != nil {
		t.Fatalf("write to the element: %v", err)
	}

	select {
	case ack := <-acks:
		if seq, ok := KeepaliveSequence(ack); !ok || seq != 99 {
			t.Errorf("acknowledgement carried %d (%v), want the 99 that was sent", seq, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an inbound Keepalive went unanswered")
	}

	// Answering must not consume this connection's own numbering: the number in an
	// acknowledgement belongs to the peer's sequence.
	c.mu.Lock()
	issued := c.live.seq.issued()
	c.mu.Unlock()

	if issued != 0 {
		t.Errorf("answering advanced this element's counter to %d; it must be untouched", issued)
	}
}

// TestKeepaliveRefusesAnOverLargePDU is the bound on the read path. Unmarshal waits
// for as many bytes as a header claims, so without this an MDF — or anything holding
// its certificate — could ask this element to hold four gigabytes.
func TestKeepaliveRefusesAnOverLargePDU(t *testing.T) {
	faults := make(chan error, 4)
	m := startMDF(t, nil)
	c := clientTo(t, m.addr, KeepaliveConfig{TimeP1: time.Hour, TimeP2: time.Hour, OnFault: func(err error) {
		select {
		case faults <- err:
		default:
		}
	}})

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// A well-formed header claiming the largest payload the field can express,
	// which is one byte under four gigabytes.
	var head [40]byte
	head[1] = MinorVersion
	binary.BigEndian.PutUint16(head[2:4], uint16(PDUTypeKeepaliveAck))
	binary.BigEndian.PutUint32(head[4:8], MandatoryHeaderLength)
	binary.BigEndian.PutUint32(head[8:12], ^uint32(0))
	if _, err := m.firstConn(t).Write(head[:]); err != nil {
		t.Fatalf("write to the element: %v", err)
	}

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "over the") {
			t.Errorf("fault = %v, want the size refusal", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an over-large declared length was not refused")
	}

	eventually(t, "the connection to be dropped", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()

		return c.live == nil
	})
}

// TestKeepaliveRefusesProductInbound: a delivery connection carries product one way.
// An X2 PDU arriving here is a peer speaking something this element is not, and the
// read path deliberately does not grow into a general receiver.
func TestKeepaliveRefusesProductInbound(t *testing.T) {
	faults := make(chan error, 4)
	m := startMDF(t, nil)
	c := clientTo(t, m.addr, KeepaliveConfig{TimeP1: time.Hour, TimeP2: time.Hour, OnFault: func(err error) {
		select {
		case faults <- err:
		default:
		}
	}})

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	b, err := product().Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := m.firstConn(t).Write(b); err != nil {
		t.Fatalf("write to the element: %v", err)
	}

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "PDU type") {
			t.Errorf("fault = %v, want the unexpected-type refusal", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an X2 PDU arriving on a delivery connection was accepted")
	}
}

// TestKeepaliveGoroutinesDieWithTheClient. Two goroutines per connection is two per
// destination in use; a client that leaked them would leak per redial, and a POI
// redials whenever an MDF drops an idle connection.
func TestKeepaliveGoroutinesDieWithTheClient(t *testing.T) {
	m := startMDF(t, answerCorrectly)
	c := NewClient(m.addr, &tls.Config{InsecureSkipVerify: true}, fastKeepalive(nil)) //nolint:gosec // test transport

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	eventually(t, "the mechanism to start", func() bool { return m.count(PDUTypeKeepalive) >= 1 })

	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Counted by function rather than against a baseline, so the test MDF's own goroutines
	// and anything else the binary runs cannot make it pass or fail.
	eventually(t, "the client's goroutines to exit", func() bool { return clientGoroutines() == 0 })
}

// TestKeepaliveDisabledSendsAndReadsNothing: with the mechanism off nothing is sent, nothing
// is read, and no connection is ever torn down for want of an acknowledgement — what a
// deployment needs when its mediation function does not acknowledge keepalives.
func TestKeepaliveDisabledSendsAndReadsNothing(t *testing.T) {
	t.Parallel()

	m := startMDF(t, nil) // never acknowledges

	faults := make(chan error, 4)
	c := clientTo(t, m.addr, KeepaliveConfig{
		Disabled: true,
		TimeP1:   10 * time.Millisecond,
		TimeP2:   30 * time.Millisecond,
		OnFault:  func(err error) { faults <- err },
	})

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	time.Sleep(200 * time.Millisecond) // many TIME_P2s, had it been running

	if n := m.count(PDUTypeKeepalive); n != 0 {
		t.Errorf("%d keepalives sent with the mechanism disabled", n)
	}
	select {
	case err := <-faults:
		t.Errorf("a fault was reported with the mechanism disabled: %v", err)
	default:
	}

	c.mu.Lock()
	live := c.live != nil
	c.mu.Unlock()

	if !live {
		t.Error("the connection was dropped with the mechanism disabled")
	}
}

// TestKeepaliveReaderSurvivesTheRedialItDidNotCause: the existing one-shot redial
// closes the connection on any write error, routinely. The reader for the old
// connection must exit quietly — a connection this element closed itself is not a
// fault, and reporting it would make every ordinary redial look like an MDF failure.
func TestKeepaliveReaderIsQuietOnOurOwnClose(t *testing.T) {
	m := startMDF(t, answerCorrectly)

	faults := make(chan error, 4)
	c := clientTo(t, m.addr, steadyKeepalive(func(err error) { faults <- err }))

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	eventually(t, "the mechanism to start", func() bool { return m.count(PDUTypeKeepalive) >= 1 })

	// Drop it the way a failed write does, then deliver again so it redials.
	c.mu.Lock()
	c.dropLocked()
	c.mu.Unlock()

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() after a drop: %v", err)
	}

	select {
	case err := <-faults:
		t.Errorf("a redial this element performed reported a fault: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestKeepaliveOnAReplacedConnectionIsSilent covers the race the test above cannot make
// happen on demand: a keepalive that has already taken its number and is waiting on the
// mutex while a delivery drops its connection and dials another.
//
// It is written against the connection rather than the clock because that is the only way
// to reach the case deterministically — and the case matters. Reporting it would mark the
// destination unreachable and make the fault probe answer "faulty" every time an ordinary
// redial raced the timer, which is a false fault in the mechanism whose whole purpose is
// telling a dead mediation function from a live one.
//
// It asserts no fault at all: a fault wrapping net.ErrClosed is still a fault.
func TestKeepaliveOnAReplacedConnectionIsSilent(t *testing.T) {
	m := startMDF(t, answerCorrectly)

	faults := make(chan error, 4)
	c := clientTo(t, m.addr, KeepaliveConfig{TimeP1: time.Hour, TimeP2: time.Hour, OnFault: func(err error) {
		select {
		case faults <- err:
		default:
		}
	}})

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// The connection a keepalive is about to be written on...
	c.mu.Lock()
	stale := c.live
	c.mu.Unlock()

	// ...dropped and replaced by this element, exactly as a failed write would.
	c.mu.Lock()
	c.dropLocked()
	c.mu.Unlock()

	if err := c.Send(product()); err != nil {
		t.Fatalf("Send() after a drop: %v", err)
	}

	if c.sendKeepalive(stale) {
		t.Error("sendKeepalive reported success writing to a connection that had been replaced")
	}

	select {
	case err := <-faults:
		t.Errorf("a connection this element replaced was reported as a fault: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if c.Unreachable() {
		t.Error("Unreachable() is true after this element replaced its own connection; " +
			"the mediation function never failed anything")
	}
}

// TestKeepaliveExpiryReachesTheFaultProbe is the other half of what the TIME_P2 rule
// owes: not only the push to the ADMF, but the answer the element gives when asked.
//
// Pool.UnreachableAmong is what a POI's fault probe consults. Without keepalive it could
// only know what a delivery attempt had established, so a destination that died while
// idle would stay "reachable" until something was sent to it. It is asserted through the
// pool rather than the client because the pool is what the probe actually calls.
func TestKeepaliveExpiryReachesTheFaultProbe(t *testing.T) {
	m := startMDF(t, nil) // never acknowledges

	faults := make(chan error, 8)
	p := NewPool(&tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test transport
		fastKeepalive(nil),
		func(err error) {
			select {
			case faults <- err:
			default:
			}
		}, nil)
	defer p.Close()

	s := p.For(m.addr)
	if err := s.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// Delivered successfully, so nothing is faulty yet.
	eventually(t, "the delivery to arrive", func() bool { return m.count(PDUTypeX2) >= 1 })

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "TIME_P2") {
			t.Fatalf("unexpected fault before the expiry: %v", err)
		}
	case <-time.After(30 * time.Millisecond):
	}

	// Now the mediation function's silence is what makes it faulty — nothing was
	// delivered in between, which is precisely the case only keepalive can catch.
	eventually(t, "the probe to report the destination unreachable", func() bool {
		unreachable, inUse := p.UnreachableAmong([]string{m.addr})

		return unreachable == 1 && inUse == 1
	})

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "TIME_P2") {
			t.Errorf("fault = %v, want the TIME_P2 expiry", err)
		}
	case <-time.After(time.Second):
		t.Error("the expiry set the probe's answer but pushed no report; clause 6.2.4 requires both")
	}
}

// TestProtocolErrorReachesTheFaultProbe is the third route to the same conclusion.
//
// A destination becomes unreachable by a failed delivery, by a TIME_P2 expiry with no
// acknowledgement — and by a peer sending bytes no X2/X3 implementation should send.
// The first two set the flag the status probe answers from; the third pushed a fault
// and did not, so an ADMF that received the report and then asked the element for its
// status was told the destination was fine. The two mechanisms are meant to answer
// different questions ("what just went wrong" and "what is wrong now"), not to
// contradict each other about one destination, and an element whose answers disagree is
// one whose status answer stops being read.
func TestProtocolErrorReachesTheFaultProbe(t *testing.T) {
	m := startMDF(t, nil)

	faults := make(chan error, 8)
	p := NewPool(&tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test transport
		KeepaliveConfig{TimeP1: time.Hour, TimeP2: time.Hour},
		func(err error) {
			select {
			case faults <- err:
			default:
			}
		}, nil)
	defer p.Close()

	s := p.For(m.addr)
	if err := s.Send(product()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	eventually(t, "the delivery to arrive", func() bool { return m.count(PDUTypeX2) >= 1 })

	// Delivered, so nothing is faulty yet — this is the state the assertion below has
	// to move away from, or it would pass against a client that reports everything
	// unreachable.
	if unreachable, _ := p.UnreachableAmong([]string{m.addr}); unreachable != 0 {
		t.Fatalf("%d destinations unreachable after a successful delivery, want 0", unreachable)
	}

	// A PDU type that has no business arriving on a delivery connection.
	b, err := product().Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := m.firstConn(t).Write(b); err != nil {
		t.Fatalf("write to the element: %v", err)
	}

	eventually(t, "the probe to report the destination unreachable", func() bool {
		unreachable, inUse := p.UnreachableAmong([]string{m.addr})

		return unreachable == 1 && inUse == 1
	})

	select {
	case err := <-faults:
		if !strings.Contains(err.Error(), "PDU type") {
			t.Errorf("fault = %v, want the unexpected-type refusal", err)
		}
	case <-time.After(time.Second):
		t.Error("the protocol error set the probe's answer but pushed no report; both are owed")
	}
}
