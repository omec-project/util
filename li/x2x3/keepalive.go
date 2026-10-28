// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// The keepalive mechanism of ETSI TS 103 221-2 clause 6.2.4: what a Keepalive PDU
// looks like, how it is numbered, and — from KeepaliveConfig down — the timers, the
// read path and the TIME_P2 disconnect that operate it.
//
// The connection half is written as methods on Client because Client owns the
// connection: it dials it, writes to it, drops it and redials it. A timer that lived
// anywhere else could only ask Client to do those things, and would have to be told
// when the connection underneath it had been replaced.
//
// Clause 5.1 defines the PDU exactly: "A set of mandatory header fields, where the
// Version, PDU Type and Header Length fields are populated as specified and all
// other mandatory fields are set to zero", plus "A Sequence Number - see
// clause 5.3.9", and its NOTE draws the consequence that there is no payload.
//
// Zero means zero, and that is the whole of the difference from a product PDU: no
// XID, no Correlation ID, no payload format, no direction. A Keepalive says nothing
// about any warrant, which is why it needs a counter that is not Sequencer's — see
// keepaliveCounter below.

// Keepalive returns a Keepalive PDU (type 3) carrying sequence.
func Keepalive(sequence uint32) *PDU {
	return keepalivePDU(PDUTypeKeepalive, sequence)
}

// KeepaliveAck returns the Keepalive Acknowledgement (type 4) for sequence.
//
// Clause 6.2.4: "The Sequence Number in the Keepalive Acknowledgement PDU shall be
// equal to the Sequence Number in the Keepalive PDU" — so the caller passes back
// what it received rather than a number of its own. The number in an acknowledgement
// belongs to the sequence of whoever sent the Keepalive.
func KeepaliveAck(sequence uint32) *PDU {
	return keepalivePDU(PDUTypeKeepaliveAck, sequence)
}

func keepalivePDU(t PDUType, sequence uint32) *PDU {
	// Every field left at its zero value is a field clause 5.1 requires to be zero.
	// Setting them explicitly would read as a choice; leaving them is the same bytes
	// and says they are not.
	return &PDU{
		Type:       t,
		Attributes: []TLV{SequenceNumber(sequence)},
	}
}

// KeepaliveSequence returns the Sequence Number a Keepalive or Keepalive
// Acknowledgement carries, and whether it carried a well-formed one.
//
// False for an attribute that is absent or not the four octets clause 5.3.9
// defines. A peer that answers without a usable number has not answered the
// question the mechanism asks, and the caller decides what to do about it — this
// reports what arrived and judges nothing.
func KeepaliveSequence(p *PDU) (uint32, bool) {
	if p == nil {
		return 0, false
	}
	for _, a := range p.Attributes {
		if a.Type == AttrSequenceNumber && len(a.Value) == 4 {
			return binary.BigEndian.Uint32(a.Value), true
		}
	}

	return 0, false
}

// keepaliveCounter numbers the Keepalive PDUs sent on one connection.
//
// It is deliberately not Sequencer. Clause 5.3.9 numbers PDUs within an "XID, DID,
// NFID, IPID and Correlation ID context", and clause 5.1 has a Keepalive zero every
// one of those fields: a Keepalive's context is no task's, so numbering it from a
// task's sequence would advance that task's numbers for a PDU the task has nothing
// to do with. The mediation function reads those numbers to detect lost product.
//
// One per connection, so a new connection numbers from zero — which is what a new
// connection is, and it means a late acknowledgement from a socket that has already
// been dropped cannot match a number in flight on its replacement.
//
// Atomic because the timer that takes numbers and the connection teardown that
// discards them are different goroutines.
type keepaliveCounter struct {
	next atomic.Uint32

	// highest is the largest sequence number an acknowledgement has been accepted for, and
	// answered says whether there has been one — the two together, because zero is a
	// legitimate first number and so cannot stand for "none yet".
	//
	// A mutex rather than another atomic: accept has to read and write the pair together, and
	// a compare-and-swap loop over a packed value would be a lock written by hand.
	mu       sync.Mutex
	highest  uint32
	answered bool
}

// take returns this connection's next Keepalive sequence number, starting at zero
// and wrapping to zero after 2^32-1 — clause 5.3.9's own restart rule, which applies
// to this counter for the same reason it applies to Sequencer's.
func (c *keepaliveCounter) take() uint32 {
	return c.next.Add(1) - 1
}

// issued is one past the highest number handed out, so a caller can tell an
// acknowledgement for a Keepalive this connection sent from one for a number it never
// did. It is how a peer that echoes something of its own invention is noticed.
func (c *keepaliveCounter) issued() uint32 {
	return c.next.Load()
}

// accept reports whether seq is an acknowledgement this connection can act on, and records
// it if so.
//
// Two questions. A number this connection never issued answers nothing — that is what
// `issued` is for. But a number it *did* issue, replayed, answers nothing either: it was
// already answered, and hearing it again is not evidence that the peer is still there.
// Clause 6.2.4 asks only that an acknowledgement carry the Keepalive's Sequence Number; not
// refreshing the deadline on a duplicate is this implementation's choice, and the harm it
// closes is the one the never-issued case exists to prevent, reached with valid material: a
// middlebox replaying one genuine acknowledgement would hold the fail-safe open indefinitely
// over a mediation function that has stopped taking product.
//
// Monotonic acceptance rather than a set of outstanding numbers. A set would be exact about
// which are in flight and would cost a per-connection allocation and a lock on the reader's
// path to answer a question the highest accepted number already answers: an acknowledgement
// older than one already accepted tells us nothing the newer one did not. The cost is that
// two acknowledgements arriving out of order retire only the newer, which is the stronger
// evidence of the two and refreshes the same deadline.
//
// The wrap is not handled here, and neither is it in `issued`: both read the counter as
// monotonic, which at one Keepalive per TIME_P1 is true for longer than any connection
// lives. Stated so that the assumption is visible rather than implied.
func (c *keepaliveCounter) accept(seq uint32) bool {
	if seq >= c.issued() {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.answered && seq <= c.highest {
		return false
	}
	c.answered, c.highest = true, seq

	return true
}

// The specification's own timers, clause 6.2.4: "by default TIME_P1 shall be 60
// seconds", "by default TIME_P2 shall be 180 seconds".
const (
	// MinKeepaliveTime is the shortest either timer may be. TS 103 221-2 Annex C table
	// C.4-1 gives keepaliveTimeP1 and keepaliveTimeP2 in seconds with a minimum of 1, so
	// anything below one second is not a value the interface can express — and the
	// sub-second end is where the arithmetic around them stops behaving: a window divided
	// to produce a tick interval reaches zero, and a timer that fires continuously is a
	// fault rather than a fast keepalive.
	MinKeepaliveTime = time.Second

	DefaultTimeP1 = 60 * time.Second
	DefaultTimeP2 = 180 * time.Second
)

// maxInboundPDU bounds what this element will hold from a peer before deciding the
// peer is broken.
//
// The read path exists to receive Keepalive Acknowledgements, which are 48 bytes.
// Unmarshal waits for as many bytes as a header claims and imposes no maximum of its
// own, so this is the bound: a peer declaring a four-gigabyte payload would otherwise
// be a peer that can exhaust this process. The margin over 48 is for a peer that carries attributes
// we do not expect, which is allowed; four kilobytes of them is not.
const maxInboundPDU = 4096

// readChunk is how much one read takes from the connection. An acknowledgement is 48 bytes,
// so one read normally holds a whole PDU.
const readChunk = 512

// KeepaliveConfig configures the clause 6.2.4 mechanism on a client's connections.
//
// The zero value is the conformant one: the mechanism running at the specification's
// own timers. That is why the switch is Disabled rather than Enabled — an inverted
// flag is a smell, and a zero value that silently omits a mandatory mechanism is a
// worse one. A caller with no opinion gets what the specification requires.
type KeepaliveConfig struct {
	// Disabled turns the mechanism off: no keepalives are sent, nothing read from the
	// connection is acted on, and no TIME_P2 disconnect can occur. The connection is
	// still watched for the mediation function closing it (see watchClose), because that
	// is not part of the mechanism: it is what stops the next delivery being written into
	// a socket the peer has already shut.
	//
	// It exists because a mediation function that does not implement the MDF half of
	// clause 6.2.4 will never acknowledge, and an element pointed at one would
	// disconnect every TIME_P2 and lose whatever was in flight each time. The
	// reference implementation of the mediation side that this package interoperates
	// with is such a peer. Turning the mechanism off is a deployment's answer to a
	// non-conformant peer; it is not the shipped default.
	//
	// What is lost with it is the evidence a refusal needs. A mediation function refusing
	// this element at the TLS layer is reported each time the refusal is met, nothing stands
	// between refusals, and the next delivery after the one-second backoff dials and writes:
	// the refusing peer accepts the write, so the destination reads reachable until the
	// refusal arrives again, and flaps between the two while traffic flows. Nothing probes an
	// idle destination either: one found unreachable stays so until the next delivery dials.
	Disabled bool

	// TimeP1 is how often a Keepalive is sent on a held connection. Zero means
	// DefaultTimeP1.
	TimeP1 time.Duration

	// TimeP2 is how long the connection may go without an acknowledgement before it
	// is disconnected. Zero means DefaultTimeP2.
	TimeP2 time.Duration

	// OnFault is called when the mechanism establishes that a mediation function is
	// not answering, or that it is answering with something this element cannot
	// parse. It carries the same meaning as a delivery failure — the destination is
	// not working — so a POI wires it to whatever it already reports those with.
	//
	// It runs on the connection's keepalive or read goroutine, or on the probe that
	// follows a peer's close — and NewPool wires the delivery worker's error callback to
	// it too — so it may be called concurrently with itself and with that callback, and
	// must be safe for that. It must not block, and must not call the client's Close:
	// Client.Close waits for these goroutines to exit, so a callback blocked on something
	// the closing goroutine holds would hang the close, and one that calls Close waits for
	// itself.
	OnFault func(error)
}

// Validate reports a configuration that would break the mechanism rather than run it.
//
// It lives here rather than in each network function because it is a rule about the
// mechanism, not about any element's configuration file: three copies of it in three
// repositories would be three chances for one of them to drift into accepting timers
// that disconnect every connection on schedule.
//
// The constructors deliberately do not call it. NewClient, NewPool and
// NewAsyncSender return no error, so a constructor that validated could only clamp an
// out-of-range value to the defaults — which is not enforcement, and would hide exactly
// the mistake this exists to surface — or change its signature, which is an API break
// for every caller to guard a path a validating caller never takes. Callers are expected
// to call this before building anything, so what a constructor check would add is a
// second answer for a configuration that has already been refused.
//
// The specification constrains only that each timer is at least one second. The
// relationship between them is left implied, and the implication is not subtle:
// TIME_P2 is the time allowed for an acknowledgement, TIME_P1 the interval between
// the requests being acknowledged, so a TIME_P2 that does not exceed TIME_P1 expires
// before the keepalive that would refresh it is even sent. Every connection would then
// be torn down on a timer, forever, and the element would report a fault each time —
// the exact failure this mechanism exists to distinguish from a real one.
func (k KeepaliveConfig) Validate() error {
	if k.TimeP1 < 0 || k.TimeP2 < 0 {
		return fmt.Errorf("x2x3: keepalive timers must not be negative (TIME_P1 %s, TIME_P2 %s)", k.TimeP1, k.TimeP2)
	}
	// The one-second minimum MinKeepaliveTime describes. Zero is not a value: it means
	// "unset", and withDefaults supplies the specification's own timer for it — which is
	// why the test is on a positive value below the floor rather than on anything that is
	// not at least a second.
	if (k.TimeP1 > 0 && k.TimeP1 < MinKeepaliveTime) || (k.TimeP2 > 0 && k.TimeP2 < MinKeepaliveTime) {
		return fmt.Errorf(
			"x2x3: keepalive timers must be at least %s, the minimum TS 103 221-2 table C.4-1 allows (TIME_P1 %s, TIME_P2 %s)",
			MinKeepaliveTime, k.TimeP1, k.TimeP2)
	}

	e := k.withDefaults()
	if e.TimeP2 <= e.TimeP1 {
		return fmt.Errorf(
			"x2x3: TIME_P2 (%s) must exceed TIME_P1 (%s), or every connection is disconnected before the keepalive that would keep it is sent",
			e.TimeP2, e.TimeP1)
	}

	return nil
}

func (k KeepaliveConfig) withDefaults() KeepaliveConfig {
	if k.TimeP1 <= 0 {
		k.TimeP1 = DefaultTimeP1
	}
	if k.TimeP2 <= 0 {
		k.TimeP2 = DefaultTimeP2
	}

	return k
}

// connState is everything that belongs to one connection and dies with it: the
// socket, the Keepalive numbering of clause 5.3.9, and the goroutines operating the
// mechanism on it.
//
// Tying all of it to the connection is what makes the hard cases fall out for free.
// A reader cannot outlive the socket it reads. A replacement connection numbers from
// zero, so a late acknowledgement from a dropped socket cannot match a number in
// flight on its successor. And a timer cannot dial, because it does not exist until
// a delivery has dialled.
type connState struct {
	conn net.Conn
	seq  keepaliveCounter

	// acks carries "an acknowledgement arrived" from the reader to the timer. One
	// slot and a non-blocking send: the timer only needs to know that one arrived
	// since it last looked, and the reader must never block on a timer.
	acks chan struct{}

	// stop is closed when this connection is dropped, whoever drops it.
	stop chan struct{}
	wg   sync.WaitGroup

	// mismatches counts acknowledgements carrying a Sequence Number this connection
	// never sent. Clause 6.2.4 requires the MDF to echo the number, so a mismatch is
	// a peer defect worth noticing — but not worth disconnecting a working connection
	// over, which is why it is counted rather than acted on. Counted rather than
	// logged because this package takes no logger; a test asserts it was seen.
	mismatches atomic.Uint64
}

// startKeepaliveLocked starts the mechanism on a freshly dialled connection. The
// caller holds mu. prompt sends the first Keepalive at once rather than after TIME_P1 — see
// installLocked.
func (c *Client) startKeepaliveLocked(st *connState, prompt bool) {
	if c.keepalive.Disabled {
		st.wg.Add(1)

		go c.watchClose(st)

		return
	}

	st.acks = make(chan struct{}, 1)
	st.wg.Add(2)

	go c.keepaliveLoop(st, prompt)
	go c.readLoop(st)
}

// keepaliveLoop sends a Keepalive every TIME_P1 and disconnects if TIME_P2 passes
// with no acknowledgement.
//
// The send is unconditional, not an idle timer. Clause 6.2.4 carries no idle
// qualifier, and product PDUs are not acknowledged: traffic on a connection
// establishes that the socket is open and nothing about whether the mediation
// function behind it is still running. An idle-reset timer would leave the busiest
// connection — where a dead MDF costs the most product — the only one never checked.
func (c *Client) keepaliveLoop(st *connState, prompt bool) {
	defer st.wg.Done()

	if prompt && !c.sendKeepalive(st) {
		return
	}

	send := time.NewTicker(c.keepalive.TimeP1)
	defer send.Stop()

	// One deadline for the connection, refreshed by any acknowledgement, which is
	// what clause 6.2.4 describes: the POI disconnects if it "has not seen a
	// Keepalive Acknowledgement PDU within TIME_P2", not if one particular Keepalive
	// went unanswered. At the default timers that tolerates two lost or late
	// acknowledgements before the third failure disconnects.
	expiry := time.NewTimer(c.keepalive.TimeP2)
	defer expiry.Stop()

	for {
		select {
		case <-st.stop:
			return
		case <-send.C:
			if !c.sendKeepalive(st) {
				return
			}
		case <-st.acks:
			expiry.Stop()
			expiry.Reset(c.keepalive.TimeP2)
		case <-expiry.C:
			// An acknowledgement that arrived as the deadline fired is still within it:
			// select chooses at random between two ready cases, so without this the
			// connection would be torn down half the time over an answer already in hand.
			select {
			case <-st.acks:
				expiry.Reset(c.keepalive.TimeP2)

				continue
			default:
			}

			c.expire(st)

			return
		}
	}
}

// sendKeepalive writes one Keepalive, reporting whether the loop should continue.
func (c *Client) sendKeepalive(st *connState) bool {
	// The number is taken before the lock: it belongs to this connection's sequence
	// whether or not the write finds the connection still current.
	b, err := Keepalive(st.seq.take()).Marshal()
	if err != nil {
		// Cannot happen — a Keepalive's shape is fixed and valid — but if it ever did,
		// keepalives on this connection would stop, so it is reported rather than dropped.
		c.fault(fmt.Errorf("x2x3: building a Keepalive for %s: %w", c.addr, err))

		return false
	}

	// The unreachable conclusion is recorded by writeOn, under mu as it drops the
	// connection: stored here, after the unlock, it could land after a delivery that has
	// since redialled and found the destination reachable, and overwrite that.
	if err := c.writeOn(st, b, true); err != nil {
		// A connection this element replaced is not a fault, and saying so here is the
		// whole point of the distinction: the loop ends because its connection ended,
		// and the replacement has a timer of its own.
		if errors.Is(err, errStaleConn) {
			return false
		}

		if c.afterKeepaliveFailed != nil {
			c.afterKeepaliveFailed()
		}
		// A destination already standing unreachable or refused has been reported. The
		// Keepalive that failed was most likely the prompt one sent on a redial to find out
		// whether that had changed (see installLocked): it has not, and saying so again would
		// report one outage once per redial.
		if !errors.Is(err, errAlreadyUnreachable) {
			c.fault(fmt.Errorf("x2x3: keepalive to %s: %w", c.addr, err))
		}

		return false
	}

	return true
}

// errStaleConn reports that st is no longer the connection this client holds: it was
// dropped and redialled, or closed. It is deliberately distinct from a write failure.
//
// A write failure says something about the mediation function. This says something about
// us — the connection went because *this element* replaced it, which the one-shot redial
// in deliverLocked does on any write error and Close does at shutdown. Reporting it as a fault
// would mark the destination unreachable and report it to the ADMF every time an ordinary
// redial happened to race the keepalive timer, in the one mechanism whose purpose is
// telling a dead mediation function from a live one.
var errStaleConn = errors.New("x2x3: connection replaced")

// errAlreadyUnreachable marks a keepalive write that failed on a destination this element had
// already concluded was unreachable or refused: the failure is real, and the conclusion it
// supports is one already drawn and reported.
var errAlreadyUnreachable = errors.New("x2x3: destination already unreachable")

// writeOn writes to st, but only while st is still the connection this client holds.
// When the write fails it drops st and, if markUnreachable is set, records the destination
// unreachable in the same critical section — a keepalive that went unwritten is a statement
// about the destination; an acknowledgement this element failed to send is not.
//
// The check is what makes a keepalive safe to write from its own goroutine: between
// the timer firing and the lock being acquired, a delivery may have failed, dropped
// this connection and dialled another. Writing then would put a keepalive numbered
// for one connection onto a different one.
func (c *Client) writeOn(st *connState, b []byte, markUnreachable bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != st {
		return errStaleConn
	}
	if _, err := c.writeLocked(b); err != nil {
		c.dropLocked()
		if !markUnreachable {
			// An acknowledgement this element failed to send says nothing about the
			// destination, but the connection is gone with its keepalives, and nothing else
			// would dial an idle destination again: a probe follows, as a peer's close is
			// followed by one.
			c.probeLocked()

			return err
		}

		already := c.unreachable.Load() || c.rejection != nil
		c.unreachable.Store(true)
		// Followed by a probe, as a peer's close is: nothing else redials a destination that
		// has nothing to deliver, so without one a mediation function that comes back — or
		// stops refusing this element — would stay reported unreachable until traffic happened
		// to arrive. That includes a refusal the reader never got to classify: a peer that
		// resets a refused connection fails this write before the alert is read, and this is
		// then the only party left to keep the redials going.
		c.probeLocked()
		if already {
			return fmt.Errorf("%w: %w", errAlreadyUnreachable, err)
		}

		return err
	}

	return nil
}

// readLoop is the only thing in this project that reads an X2/X3 socket.
//
// It exists for the acknowledgement, and it accepts exactly the two PDU types the
// mechanism defines. Anything else is a peer speaking something this element does
// not: the connection goes, and the next delivery redials.
func (c *Client) readLoop(st *connState) {
	defer st.wg.Done()

	var (
		buf []byte
		tmp = make([]byte, readChunk)
	)

	for {
		n, err := st.conn.Read(tmp)
		if err != nil {
			// Either the peer closed the connection, or this element did — routine,
			// every redial does it — or the peer's TLS layer refused it. readFailed tells
			// them apart.
			c.readFailed(st, err)

			return
		}

		buf = append(buf, tmp[:n]...)
		// Refuse a PDU larger than this mechanism can be asked to hold before
		// accumulating it, rather than after: the length fields are the peer's
		// claim, and this is the point at which the claim is cheap to refuse.
		if len(buf) >= sizePrefixLength {
			declared := uint64(binary.BigEndian.Uint32(buf[4:8])) + uint64(binary.BigEndian.Uint32(buf[8:12]))
			if declared > maxInboundPDU {
				c.protocolError(st, fmt.Errorf("x2x3: %s declared a %d-byte PDU, over the %d-byte limit", c.addr, declared, maxInboundPDU))

				return
			}
		}

		for {
			p, used, err := Unmarshal(buf)
			if errors.Is(err, ErrIncomplete) {
				break
			}
			if err != nil {
				c.protocolError(st, fmt.Errorf("x2x3: undecodable PDU from %s: %w", c.addr, err))

				return
			}

			buf = buf[used:]
			if !c.handleInbound(st, p) {
				return
			}
		}
	}
}

// watchClose is the read path for a connection the keepalive mechanism does not run on: it
// reads only to learn that the peer has closed the connection, and discards what it reads.
// A mediation function that does not implement clause 6.2.4 — the reason the mechanism can
// be disabled — still closes idle connections, and without a reader nothing would notice.
func (c *Client) watchClose(st *connState) {
	defer st.wg.Done()

	tmp := make([]byte, readChunk)
	for {
		if _, err := st.conn.Read(tmp); err != nil {
			c.readFailed(st, err)

			return
		}
	}
}

// readFailed acts on a connection whose read side has failed: a refusal at the TLS layer is
// refused's to handle, and anything else is a close, peerClosed's.
func (c *Client) readFailed(st *connState, err error) {
	if refusedAtTLS(err) {
		c.refused(st, err)

		return
	}

	c.peerClosed(st)
}

// refusedAtTLS reports whether a read failed because the peer's TLS layer sent an alert, or
// because what arrived was not TLS — rather than because the connection closed.
//
// The two must not be confused, and TLS 1.3 is what makes them easy to confuse. The client's
// half of the handshake completes before the server has verified the client's certificate,
// so a mediation function that does not trust this element's certificate accepts the dial,
// takes the first write into its socket buffer, and refuses only in an alert, which the next
// read returns. Read as a close, that refusal is redialled, accepted, written to, and refused
// again — each delivery "succeeding", the destination reported reachable, and every unit lost.
//
// An ordinary close reads as io.EOF — a peer's close_notify included — or, for a connection
// this element closed, net.ErrClosed. A reset is a close too: it is how TCP ends a connection
// with data unread, and how a load balancer expires an idle one, and the probe that follows a
// close establishes whether the mediation function is still there. What crypto/tls reports
// for an alert is a *net.OpError whose Op is "remote error" — the peer's alert — or "local
// error" — the alert this side sent over records it could not accept.
func refusedAtTLS(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && (op.Op == "remote error" || op.Op == "local error") {
		return true
	}

	var header tls.RecordHeaderError
	if errors.As(err, &header) {
		return true
	}

	var alert tls.AlertError

	return errors.As(err, &alert)
}

// refused handles a connection the mediation function refused at the TLS layer: it is
// dropped, the destination is marked unreachable and the refusal reported, as a protocol error
// would be, and deliveries back off from it as from a failed dial.
//
// With the keepalive mechanism running, the refusal also stands until the mediation function
// acknowledges a Keepalive — see Client.rejection. A dial that succeeds is no evidence: the
// refusing peer's dials succeed too. So deliveries are refused rather than written into a
// connection that will be refused in turn, and lost as reported failures instead of silent
// ones; the redial after the refusal (probeAfterPeerClose) sends a Keepalive at once, so a
// mediation function that has been fixed is found within a round trip of that dial.
//
// The refusal is reported once per episode — from the refusal to the acknowledgement that
// ends it — rather than on every redial that meets it again: the prompt Keepalives that fail
// on those redials, and probe dials that fail, confirm what has already been reported and are
// not reported again (see sendKeepalive and probeAfterPeerClose). Deliveries refused in the
// meantime are returned to their caller as failures, and through a Pool each reaches the
// delivery worker's error callback, which NewPool shares with OnFault.
//
// A refused destination is redialled once per redialInterval for as long as the refusal
// stands, which is for the life of the client if it never ends: one TLS handshake a second,
// bounded, and the only way a fix is found with nothing to deliver. The probe keeps dialling
// through a mediation function that cannot be dialled at all in the meantime — restarted,
// say, with its certificate store fixed — so the fix is found when it comes back. A mediation function that
// stops refusing but never acknowledges a Keepalive — one that does not implement clause 6.2.4
// — is never found accepting, so deliveries to it stay refused. With the mechanism running
// such a peer is disconnected every TIME_P2 in any case; Disabled, a start-up choice, is the
// configuration for it.
//
// The refusal may arrive after its connection has gone: a peer that resets a refused
// connection fails the next write, and the writer drops the connection before this reader has
// read the alert. It is recorded all the same, since the reader is the only party that saw it,
// unless a newer connection is already held, which then speaks for itself.
//
// With the mechanism disabled nothing can establish that the peer has accepted this element,
// so nothing stands: each refusal is reported, the next delivery after the backoff dials and
// writes, and a write that succeeds marks the destination reachable until the refusal
// arrives again. Disabling the mechanism removes the only evidence there is.
func (c *Client) refused(st *connState, err error) {
	err = fmt.Errorf("x2x3: %s refused the connection at the TLS layer: %w", c.addr, err)

	c.mu.Lock()
	switch {
	case c.closed:
		c.mu.Unlock()

		return
	case c.live == st:
		c.dropLocked()
	case c.live != nil:
		// A newer connection is held. It is refused or accepted on its own account, and
		// whichever party notices first — its reader, or a writer — speaks for it.
		c.mu.Unlock()

		return
	}
	// No connection is held: st was dropped just now, or a writer dropped it first. A peer
	// that resets a refused connection fails the next write — a prompt Keepalive, a
	// delivery — before this reader has read the alert, and the writer then drops the
	// connection without knowing why. The reader is the only party that saw the refusal, so
	// it is recorded here either way; returning because st is no longer held would lose it.
	//
	// Stored before the fault is reported, as expire does.
	c.unreachable.Store(true)
	c.lastDialFailure, c.lastDialErr = time.Now(), err

	report := c.rejection == nil
	if !c.keepalive.Disabled {
		c.rejection = err
		c.probeLocked()
	}
	c.mu.Unlock()

	if report {
		c.fault(err)
	}
}

// peerClosed drops st once its read side has failed, if it is still the connection this
// client holds, so the next delivery dials a fresh one instead of writing into it.
//
// A connection the mediation function has closed is not one a write can be trusted on.
// The kernel accepts the next write into a socket whose peer has gone, the peer answers it
// with a reset, and only the write after that fails and redials — so the first delivery after
// an idle close would be lost with a nil error: no drop, no error, no fault, and a gap in the
// sequence the mediation function could see and this element could not account for. The
// reader is the first to know, so it drops the connection.
//
// Dropping the connection also stops its keepalive timer, which is what finds a dead
// mediation function while nothing is being delivered. Left at that, a mediation function
// that crashed while idle would be reported reachable until the next delivery failed — and by
// then a unit is already lost. So where the keepalive mechanism runs, the close is followed by
// a probe (probeAfterPeerClose): a mediation function that answers gets a fresh connection with
// its own keepalives, and one that cannot be dialled is marked unreachable and reported, as a
// failed keepalive would be, and dialled again once per redialInterval until it answers.
//
// The close itself is not a fault, and the destination is not marked unreachable over it: a
// mediation function closing an idle connection — a restart, a load balancer's idle timeout —
// is ordinary. With the mechanism disabled nothing probes an idle connection, and the next
// delivery's dial establishes whether it can be reached. A read failure this element
// caused by closing the connection itself finds st already replaced and does nothing.
//
// A window remains, between the peer's close arriving and this goroutine observing it and
// taking the lock: a delivery in that interval is still written into the closed socket.
// It is the time a read takes to return, rather than the time until the next delivery happens
// to fail.
func (c *Client) peerClosed(st *connState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.live != st {
		return
	}

	c.dropLocked()
	c.probeLocked()
}

// probeLocked starts a probe (see probeAfterPeerClose), unless one is already in flight or the
// mechanism is disabled. The caller holds mu. Every path that leaves this client with no
// connection and nothing else to redial calls it: a peer's close, a refusal, and a connection
// that failed under a keepalive, an acknowledgement, a delivery, a protocol error or an
// expiry, as well as a delivery whose dial failed.
func (c *Client) probeLocked() {
	// Added under mu, where closed is read: Close sets closed under mu before it waits, so
	// no probe can be added once that wait has begun.
	//
	// One probe at a time. A probe already in flight re-checks for a held connection when
	// it wakes, and keeps dialling until it installs one, so it answers for this close too; a second one would wait out the same spacing
	// computed from the same instant, wake beside it and dial again, and a peer that keeps
	// closing what deliveries reconnect would then be dialled once per close rather than once
	// per redialInterval — and two failing probes would report one outage twice.
	if !c.keepalive.Disabled && !c.closed && !c.probing {
		c.probing = true
		c.probes.Add(1)

		go c.probeAfterPeerClose()
	}
}

// probeAfterPeerClose redials a mediation function whose connection ended with nothing left to
// deliver — closed by the peer, refused at the TLS layer, failed under a keepalive, a delivery or
// a protocol error, or never made — to find out whether it can be reached without waiting for
// the next delivery.
//
// It keeps dialling until a dial succeeds or the client closes. A dial that fails leaves the
// destination unreachable, and with nothing to deliver nothing else would dial it again, so a
// probe that stopped there would leave the destination reported unreachable — its fault
// standing over a mediation function that has since come back — until traffic happened to
// arrive. A connection it installs is then the next probe's concern: it is refused, closed or
// fails under its prompt Keepalive in turn (each of which starts a probe), or is acknowledged,
// which retracts the conclusion. So a destination standing unreachable or refused is dialled
// for as long as that lasts, and one whose mediation function never recovers is dialled for the
// life of the client.
//
// One dial per redialInterval, at most. It waits out the backoff a failed dial imposes,
// and it spaces itself from the previous probe dial by the same interval: a peer that accepts
// and immediately closes would otherwise be redialled as fast as the handshake completes.
//
// The dial happens without mu, so deliveries are not held behind it, and its outcome is
// installed only if nothing else connected in the meantime: a delivery that dialled first
// already holds the connection, and this one is closed unused. Close cancels the wait and the
// dial, and waits for this goroutine.
//
// A dial that fails is reported only if it is news — the first time the destination is found
// unreachable. Every later failure confirms what has already been reported, and saying it once
// per redialInterval would report one outage once a second.
//
// probing is cleared in the same critical section that ends the probe, on every exit. Clearing
// it afterwards, under a second acquisition of mu, would leave a window in which the connection
// this probe just installed can be closed by its peer and its reader find probing still set —
// so no probe would follow that close.
func (c *Client) probeAfterPeerClose() {
	defer c.probes.Done()

	for {
		c.mu.Lock()
		wait := c.probeWaitLocked()
		c.mu.Unlock()

		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-c.lifetime.Done():
				t.Stop()
			case <-t.C:
			}
		}

		c.mu.Lock()
		if c.closed || c.live != nil || c.lifetime.Err() != nil {
			c.probing = false
			c.mu.Unlock()

			return
		}
		c.lastProbe = time.Now()
		c.mu.Unlock()

		conn, err := c.dial()

		c.mu.Lock()
		if c.closed || c.live != nil {
			c.probing = false
			c.mu.Unlock()
			if conn != nil {
				// Never installed: nothing was written on it and nothing reads from it, so
				// a failure to close it has nothing to report.
				_ = conn.Close()
			}

			return
		}
		if err != nil {
			c.lastDialFailure, c.lastDialErr = time.Now(), err
			// A destination already standing unreachable or refused has been reported, by
			// whichever failure started the probe or by this probe's first failed dial; a
			// dial that fails as well confirms it.
			already := c.unreachable.Load() || c.rejection != nil
			// Set before the fault is reported, so a status request racing this cannot be
			// answered "reachable" by an element that has just concluded otherwise — the
			// rule expire follows.
			c.unreachable.Store(true)
			// probing stays set: this probe goes on dialling, and no other is started
			// beside it.
			c.mu.Unlock()

			if !already {
				c.fault(fmt.Errorf("x2x3: %s dropped the connection and could not be reached again: %w", c.addr, err))
			}

			continue
		}
		c.probing = false
		// Not marked reachable here. A dial that succeeds says the mediation function accepts
		// connections, which one that refuses this element at the TLS layer also does — see
		// refusedAtTLS. A fault standing for this destination is retracted by the first valid
		// acknowledgement on the new connection instead, and installLocked has it send a
		// Keepalive at once, so that is a round trip away rather than TIME_P1.
		c.installLocked(conn)
		c.mu.Unlock()

		return
	}
}

// probeWaitLocked is how long a post-close probe must wait before dialling: the remainder of
// a failed dial's backoff, or of the spacing from the previous probe, whichever is longer.
// The caller holds mu.
func (c *Client) probeWaitLocked() time.Duration {
	var wait time.Duration

	if c.lastDialErr != nil {
		wait = c.redialInterval - time.Since(c.lastDialFailure)
	}
	if !c.lastProbe.IsZero() {
		if since := c.redialInterval - time.Since(c.lastProbe); since > wait {
			wait = since
		}
	}

	return wait
}

// handleInbound acts on one decoded PDU, reporting whether the reader continues.
func (c *Client) handleInbound(st *connState, p *PDU) bool {
	switch p.Type {
	case PDUTypeKeepaliveAck:
		seq, ok := KeepaliveSequence(p)
		if !ok || !st.seq.accept(seq) {
			// An acknowledgement this connection cannot act on does not answer the
			// question TIME_P2 asks. Either it carries no usable sequence number, or one
			// beyond what has been handed out, or one that was already answered — see
			// keepaliveCounter.accept. Clause 6.2.4 numbers the acknowledgement from the
			// Keepalive it answers, so this is the only evidence available that the peer is
			// answering *us*, now, rather than emitting traffic or repeating itself.
			//
			// Something is alive at the other end, but that is not the property: a peer
			// stuck in a loop, a middlebox replaying, or an endpoint a misroute put in the
			// path all satisfy "something is answering", and each would hold the fail-safe
			// open over a mediation function that has stopped taking product.
			//
			// The connection is *not* torn down over it — a peer's numbering defect must
			// not end a connection that is demonstrably carrying product — so the reader
			// continues and TIME_P2 decides, which is the fail-safe doing its job rather
			// than a protocol error.
			st.mismatches.Add(1)

			return true
		}

		// A valid acknowledgement of something this connection sent: the exchange
		// succeeded, which is the whole question TIME_P2 asks.
		//
		// Only while this is still the connection the client holds. The reader runs on
		// its own goroutine, so an acknowledgement decoded from a connection that has since
		// been dropped and replaced would otherwise clear a reachability conclusion the
		// replacement has already reached — a store of false landing after expire's or
		// protocolError's store of true, over a destination that is now failing. The two
		// paths order themselves this way already (`if c.live == st`), and this is the
		// third writer of the same flag taking the same rule.
		//
		// The deadline itself is still refreshed, because that channel belongs to st: a
		// stale connection's watchdog has nothing left to postpone.
		//
		// It also ends a refusal standing from an earlier connection (see refused): a
		// mediation function that answers a Keepalive has accepted this element.
		c.mu.Lock()
		if c.live == st {
			c.unreachable.Store(false)
			c.rejection = nil
		}
		c.mu.Unlock()

		select {
		case st.acks <- struct{}{}:
		default:
		}

		return true

	case PDUTypeKeepalive:
		return c.answerKeepalive(st, p)

	default:
		// Product (X2 or X3) included: a delivery connection carries it one way only.
		c.protocolError(st, fmt.Errorf("x2x3: %s sent PDU type %d on a delivery connection", c.addr, p.Type))

		return false
	}
}

// answerKeepalive is the MDF-facing half of clause 6.2.4, which makes both PDU types
// supported by POIs *and* MDFs. A peer that keepalives us gets an acknowledgement
// carrying its own Sequence Number.
//
// It does not touch this connection's counter. The number in an acknowledgement
// belongs to the sequence of whoever sent the Keepalive, and advancing ours here
// would put a gap in the numbering the peer is watching for lost keepalives.
func (c *Client) answerKeepalive(st *connState, p *PDU) bool {
	seq, ok := KeepaliveSequence(p)
	if !ok {
		// Clause 6.2.4 requires the acknowledgement to carry the same number, and a
		// number that was not sent cannot be echoed. Counted and ignored — refusing
		// the connection over it would punish a peer defect with lost product.
		st.mismatches.Add(1)

		return true
	}

	b, err := KeepaliveAck(seq).Marshal()
	if err != nil {
		// Cannot happen — the shape is fixed and valid — but if it ever did, the reader
		// would stop, so it is reported rather than dropped.
		c.fault(fmt.Errorf("x2x3: building a Keepalive Acknowledgement for %s: %w", c.addr, err))

		return false
	}

	// A failed write is deliberately not reported here: writeOn drops the connection, and
	// this element's own keepalives — sent on the next connection a delivery dials — are
	// what establish whether the mediation function is reachable. An acknowledgement this
	// element owes the peer says nothing about that.
	return c.writeOn(st, b, false) == nil
}

// protocolError drops a connection whose peer is not speaking X2/X3, and reports it.
//
// Reported rather than dropped quietly: a mediation function sending something this
// element cannot parse is a fault in the delivery plane, and the ADMF is the only
// party that can act on it.
//
// It reports even when the connection has already been replaced, which is deliberate and
// is *not* the errStaleConn case above. There, the connection went because this element
// replaced it and nothing had gone wrong; here a peer sent bytes no X2/X3 implementation
// should send, and that stays true whichever socket carried them.
//
// It marks the destination unreachable, as the TIME_P2 expiry and a failed delivery both
// do. This is the same conclusion arrived at by a third route — the exchange with this
// mediation function did not work — and marking it keeps the pushed fault and the status
// answer the ADMF can ask for in agreement about the destination. An element whose two
// answers contradict each other is one whose status answer stops being read.
//
// Only while st is still the connection this client holds, and under mu, as every other
// writer of the flag: a stale connection's reader storing it after a delivery has redialled
// and found the destination reachable would overwrite the newer conclusion. The fault is
// reported either way, for the reason above — except on a destination already standing
// unreachable or refused, which has been reported: the probe that follows a protocol error
// reconnects once per redialInterval, and a peer that sends garbage on every connection would
// otherwise be reported once per reconnect.
func (c *Client) protocolError(st *connState, err error) {
	c.mu.Lock()
	report := true
	if c.live == st {
		// A destination already standing unreachable or refused has been reported. The
		// probe that follows reconnects once per redialInterval, and a peer that answers each
		// connection with something this element cannot parse would otherwise be reported once
		// per reconnect.
		report = !c.unreachable.Load() && c.rejection == nil
		// Stored before the fault is reported, so a status request racing this cannot be
		// answered "reachable" by an element that has just concluded otherwise — the
		// ordering expire already uses.
		c.unreachable.Store(true)
		c.dropLocked()
		// Nothing else would dial this destination again while it is idle.
		c.probeLocked()
	}
	c.mu.Unlock()

	if report {
		c.fault(err)
	}
}

// expire applies clause 6.2.4's TIME_P2 rule: "the POI shall disconnect the
// connection and shall attempt to reconnect to the MDF and report an error through
// the X1 interface".
//
// All three, in that order. The reconnect is one attempt here; if it fails, a probe takes over
// and keeps dialling once per redialInterval (see probeAfterPeerClose), so a mediation function
// that comes back is found while the destination is idle rather than by the next delivery.
//
// Only for the connection this client still holds. A deadline that fires on a connection
// already replaced — a delivery's redial racing the timer, which select resolves either way
// when both are ready — says nothing about the replacement, and marking the destination
// unreachable and reporting a fault over it would announce a dead mediation function that is
// answering perfectly well on the new connection.
func (c *Client) expire(st *connState) {
	c.mu.Lock()
	current := c.live == st
	if current {
		// Set before the fault is reported and before the redial, so that a status
		// request racing this cannot be answered "reachable" by an element that has just
		// concluded otherwise.
		c.unreachable.Store(true)
		c.dropLocked()
		// Failure is not reported here: the dial error says the MDF is unreachable,
		// which is what has just been reported anyway.
		if err := c.dialLocked(); err != nil {
			c.probeLocked()
		}
	}
	c.mu.Unlock()

	if current {
		c.fault(fmt.Errorf("x2x3: no keepalive acknowledgement from %s within %s (TIME_P2)", c.addr, c.keepalive.TimeP2))
	}
}

func (c *Client) fault(err error) {
	if c.keepalive.OnFault != nil {
		c.keepalive.OnFault(err)
	}
}
