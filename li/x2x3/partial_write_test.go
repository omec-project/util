// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// halfOpenMDF accepts one connection, reads a bounded number of bytes and then stops
// reading, so a write large enough to fill the socket buffer fails part-way. The
// second connection reads everything, which is what the retry lands on.
//
// It records what each connection received, because that is the only place the
// property can be asserted: what the destination got, not what the client attempted.
type halfOpenMDF struct {
	addr string

	mu       sync.Mutex
	streams  [][]byte
	accepted int
}

// newHalfOpenMDF is a mediation function that accepts a connection and then stops reading
// after stallAfter bytes, so a delivery lands in part and no further.
//
// stallEvery makes every connection stall rather than only the first, so the retry the
// client makes on a fresh connection fails too. It is what distinguishes a unit that is
// recovered from one that is lost.
func newHalfOpenMDF(t *testing.T, cert tls.Certificate, stallAfter int, stallEvery ...bool) *halfOpenMDF {
	t.Helper()

	always := len(stallEvery) > 0 && stallEvery[0]

	// Accepted sockets inherit the listener's capped receive buffer, so how much a stalled
	// connection lets the element write does not depend on the platform's autotuning.
	raw, err := (&net.ListenConfig{Control: capBuffer(false)}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := tls.NewListener(raw, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck // test cleanup

	m := &halfOpenMDF{addr: ln.Addr().String()}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			m.mu.Lock()
			m.accepted++
			first := always || m.accepted == 1
			m.streams = append(m.streams, nil)
			idx := len(m.streams) - 1
			m.mu.Unlock()

			go func() {
				defer conn.Close() //nolint:errcheck // test

				buf := make([]byte, 4096)
				read := 0
				for {
					if first && read >= stallAfter {
						// Stop reading and hold the connection open: the client's
						// write fills the socket buffer and trips its deadline.
						time.Sleep(30 * time.Second)

						return
					}
					if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
						return
					}
					n, err := conn.Read(buf)
					if n > 0 {
						read += n
						m.mu.Lock()
						m.streams[idx] = append(m.streams[idx], buf[:n]...)
						m.mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	return m
}

// stallingClient is a client of a halfOpenMDF whose sockets have a capped send buffer too, so
// that a stalled connection takes a few hundred kilobytes rather than whatever autotuning
// grants. How many is still the platform's — macOS loopback does not hold to the caps — so a
// test sizes its batch from stallCapacity rather than from a constant.
func stallingClient(t *testing.T, addr string) *Client {
	t.Helper()

	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // test peer
	c := NewClient(addr, cfg, KeepaliveConfig{Disabled: true})
	c.writeTimeout = time.Second
	c.dialConn = func(ctx context.Context) (net.Conn, error) {
		d := tls.Dialer{NetDialer: &net.Dialer{Control: capBuffer(true)}, Config: cfg}

		return d.DialContext(ctx, "tcp", addr)
	}
	t.Cleanup(func() { c.Close() }) //nolint:errcheck // test cleanup

	return c
}

// stallCapacity measures how many bytes one stalled connection to a halfOpenMDF takes before
// a write blocks, through the same capped sockets and TLS a stallingClient uses. The kernel
// takes them at once, so a short deadline is enough to find the edge.
func stallCapacity(t *testing.T) int {
	t.Helper()

	mdf := newHalfOpenMDF(t, selfSignedServer(t), 64*1024, true)
	c := stallingClient(t, mdf.addr)
	c.writeTimeout = 300 * time.Millisecond

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.dialLocked(); err != nil {
		t.Fatal(err)
	}
	n, err := c.writeLocked(make([]byte, 16<<20))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("measuring a stalled connection: wrote %d bytes, err = %v; want a timeout", n, err)
	}

	return n
}

// stallingBatch is a batch sized against capacity, what one stalled connection takes: six
// units of half of it. The first write lands at least one whole unit before it stalls, and
// what is left after that is more than a second stalled connection takes.
func stallingBatch(capacity int, fill byte) []*PDU {
	pdus := make([]*PDU, 6)
	for i := range pdus {
		pdus[i] = &PDU{
			Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable,
			Payload: bytes.Repeat([]byte{fill}, capacity/2),
		}
	}

	return pdus
}

func (m *halfOpenMDF) received() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	var all []byte
	for _, s := range m.streams {
		all = append(all, s...)
	}

	return all
}

// TestARetryDoesNotRedeliverWhatThePeerAlreadyHas is the delivery-integrity property,
// asserted on what the destination received rather than on what the client attempted.
//
// PDUs are self-delimiting and concatenated, so a batch shares one write and a write can
// end in the middle of one. Retrying the whole buffer sends the leading complete PDUs a
// second time — and because the retry follows a reconnection, they arrive on a fresh
// stream where nothing marks them as repeats. Duplicate product under one warrant is
// exactly what deduplicating destinations by address exists to prevent, reached from the
// delivery mechanism instead of the destination list.
//
// Sequence numbers are assigned at framing, so a careful mediation function could
// recognise the duplicates. That is a property of the peer and not something this
// element may rely on.
func TestARetryDoesNotRedeliverWhatThePeerAlreadyHas(t *testing.T) {
	t.Parallel()

	// More than a stalled connection takes, so the first write stalls part-way through.
	pdus := stallingBatch(stallCapacity(t), 0xAB)

	mdf := newHalfOpenMDF(t, selfSignedServer(t), 64*1024)
	c := stallingClient(t, mdf.addr)

	//nolint:errcheck // a partially-written unit is dropped and reported; that is the contract
	_ = c.SendBatch(pdus)

	// Let the retry's write land at the second connection.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mdf.mu.Lock()
		done := mdf.accepted >= 2
		mdf.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	mdf.mu.Lock()
	retried := mdf.accepted >= 2
	mdf.mu.Unlock()
	if !retried {
		t.Fatal("the first write did not stall, so no retry happened to examine: the capped " +
			"socket buffers took the whole batch")
	}

	got := mdf.received()

	// One marshalled PDU is the unit of duplication, so count how many whole copies of
	// one arrived. Three were offered; the destination must never see more than three,
	// and a retry that restarted the buffer sends four or five.
	one, err := pdus[0].Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(got, one); n > len(pdus) {
		t.Errorf("the destination received %d copies of a product unit for %d offered: "+
			"the retry resent what the peer already had", n, len(pdus))
	}
	if len(got) > len(one)*len(pdus) {
		t.Errorf("the destination received %d bytes for %d offered; the retry did not resume "+
			"at a product-unit boundary", len(got), len(one)*len(pdus))
	}
}

// pausingMDF reads a bounded number of bytes, stops reading for long enough that the
// client's write deadline trips, and then reads everything that follows — on that
// connection and on the one the client dials to retry.
//
// Resuming the read is what makes the property observable. A peer that stalls forever
// leaves the bytes the kernel accepted unread, so nothing can distinguish a unit that was
// sent and never read from one that was never sent — which is the difference under test.
// Once it drains, every byte this element wrote is counted, on both streams.
type pausingMDF struct {
	addr string

	mu      sync.Mutex
	streams [][]byte
}

func newPausingMDF(t *testing.T, cert tls.Certificate, readBeforePause int, pause time.Duration) *pausingMDF {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck // test cleanup

	m := &pausingMDF{addr: ln.Addr().String()}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			m.mu.Lock()
			first := len(m.streams) == 0
			m.streams = append(m.streams, nil)
			idx := len(m.streams) - 1
			m.mu.Unlock()

			go func() {
				defer conn.Close() //nolint:errcheck // test

				buf := make([]byte, 32*1024)
				read := 0
				paused := false
				for {
					if first && !paused && read >= readBeforePause {
						paused = true
						time.Sleep(pause)
					}
					if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
						return
					}
					n, err := conn.Read(buf)
					if n > 0 {
						read += n
						m.mu.Lock()
						m.streams[idx] = append(m.streams[idx], buf[:n]...)
						m.mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	return m
}

func (m *pausingMDF) all() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []byte
	for _, s := range m.streams {
		out = append(out, s...)
	}

	return out
}

// TestAPartiallyWrittenUnitIsResentWhole is what a partial write costs once the retry goes
// out on a fresh connection: nothing.
//
// Skipping the unit would be right when resuming mid-unit on the same stream, where a
// receiver reading on would take the tail of a unit as the start of one. It is wrong for the
// connection the retry actually uses: a TCP
// connection is its own stream, and this one begins at a frame boundary, so a whole unit
// sent on it cannot be read as anybody's tail. Nor can it duplicate — a unit the kernel did
// not accept in full is one the peer cannot have received in full, because a write reports
// what it took and never more.
//
// The two properties together are the whole claim, and each unit carries its own fill byte
// so both are countable: every unit offered reaches the destination, and none reaches it
// twice.
func TestAPartiallyWrittenUnitIsResentWhole(t *testing.T) {
	t.Parallel()

	cert := selfSignedServer(t)

	pdus := make([]*PDU, 0, 4)
	for i := range 4 {
		pdus = append(pdus, &PDU{
			Type:          PDUTypeX2,
			PayloadFormat: PayloadFormat3GPP33128,
			Direction:     DirectionNotApplicable,
			Payload:       bytes.Repeat([]byte{byte(0xA0 + i)}, 512*1024),
		})
	}

	// The peer reads a little, pauses for twice the write deadline so the write trips it
	// however late it started, and then drains.
	mdf := newPausingMDF(t, cert, 32*1024, 2*time.Second)

	c := NewClient(mdf.addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // test peer
		KeepaliveConfig{Disabled: true})
	c.writeTimeout = time.Second
	t.Cleanup(func() { c.Close() }) //nolint:errcheck // test cleanup

	if err := c.SendBatch(pdus); err != nil {
		t.Fatalf("SendBatch after a recoverable partial write returned %v, want nil", err)
	}

	// Wait for both streams to drain.
	marshalled := make([][]byte, len(pdus))
	total := 0
	for i, p := range pdus {
		b, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		marshalled[i] = b
		total += len(b)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(mdf.all()) < total {
		time.Sleep(50 * time.Millisecond)
	}

	got := mdf.all()
	for i, one := range marshalled {
		switch n := bytes.Count(got, one); {
		case n == 0:
			t.Errorf("product unit %d never reached the destination whole: the unit the write "+
				"stopped inside was skipped rather than resent from its own start, so an xIRI the "+
				"element produced was silently lost to a mediation function that is working", i)
		case n > 1:
			t.Errorf("product unit %d reached the destination %d times: the retry resent something "+
				"the peer already had, which is duplicate product under one warrant", i, n)
		}
	}
}

// TestADroppedUnitIsTheOneThatCouldNotBeResent is the surviving drop: this element has one
// reconnect, it spent it, and the resend did not land either.
//
// Earlier units did land, so the loss is what ErrUnitDropped says — delivery succeeded except
// for the tail. But this peer stalls on every connection, so the resend failed by timing out
// on a connection the mediation function accepted and then did not read: it has stalled, and
// is reported unreachable. Called reachable, it never would be — every later batch would be
// redialled and time out the same way, replacing the keepalive timer before TIME_P2 could
// expire. TestADroppedTailIsCountedAndLeavesTheDestinationReachable covers a resend that
// failed in some other way.
func TestADroppedUnitIsTheOneThatCouldNotBeResent(t *testing.T) {
	t.Parallel()

	// Every connection stalls, so the resend fails as the first write did. The capped buffers
	// let at least one unit land on the first connection, and not the rest on the second.
	mdf := newHalfOpenMDF(t, selfSignedServer(t), 64*1024, true)
	c := stallingClient(t, mdf.addr)

	err := c.SendBatch(stallingBatch(stallCapacity(t), 0xCD))
	if !errors.Is(err, ErrUnitDropped) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("SendBatch() = %v, want a dropped tail from a resend that timed out", err)
	}
	if !c.Unreachable() {
		t.Error("a mediation function that stalled on a fresh connection is reported reachable: " +
			"every later batch would stall the same way, and nothing would ever report it")
	}
}
