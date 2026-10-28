// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"crypto/tls"
	"errors"
	"sync"
	"time"
)

// Pool holds one delivery client per destination address, created on first use.
//
// A POI needs this rather than a single client because destinations arrive per task over
// X1: they are not known at startup, and several agencies' endpoints may be in use at
// once. A single client per process would make two warrants provisioned to two agencies
// both deliver to whichever address configuration happened to name.
//
// Each client is wrapped in an AsyncSender, because delivery must not run on the
// signalling goroutine that produced the record: a slow or unreachable MDF would delay a
// targeted subscriber's signalling, which is both an availability risk and a
// target-observable timing side channel.
//
// A destination, once created, is kept until Close: nothing releases one when the last task
// delivering to it ends, so its client, its worker goroutine and — once something has been
// delivered — its connection and keepalives stay for the life of the pool. That is bounded by
// the destinations ever provisioned, which X1 sets and which are few; an element whose
// destinations change often closes the pool and builds another.
//
// Safe for concurrent use.
type Pool struct {
	tlsConfig *tls.Config
	keepalive KeepaliveConfig
	onError   func(error)
	onDrop    func()

	mu      sync.Mutex
	senders map[string]Sender
	// closed stops For building a sender after Close has run. Close empties the map
	// rather than marking the pool, so without this a late delivery is indistinguishable
	// from a first one.
	closed bool
}

// NewPool returns a pool that dials with tlsConfig, running the clause 6.2.4 keepalive
// mechanism on every connection it opens as keepalive describes — the zero value being
// the specification's own defaults.
//
// onError is called with each worker delivery error and onDrop when a PDU is dropped before
// any delivery was attempted for it — a full queue, a closed sender, or a close that gave up
// waiting; both may be nil. A delivery error says how many product units it cost (LostUnits),
// so a failed batch is accounted per unit and not per call.
//
// A keepalive fault means the same thing as a delivery error — this destination is not
// working — so it is reported through onError rather than through a callback of its own.
// onError therefore hears both, and they are not deduplicated against each other: the
// keepalive mechanism reports a condition once, when it begins, and the delivery worker
// reports every batch it could not deliver — while a TLS refusal stands, every batch, since
// deliveries are then refused rather than written. A POI that turns these into reports to an
// ADMF throttles them per condition.
//
// onError is therefore called from more than one goroutine at once: each destination's
// delivery worker, each connection's keepalive and read goroutines, and the probe that
// follows a peer's close. It must be safe for concurrent use, must not block and must not
// close the pool — Close waits for those goroutines, so a callback blocked on something the
// closing goroutine holds would stall the close (bounded here by closeTimeout, and unbounded
// for a caller closing a sender directly).
func NewPool(tlsConfig *tls.Config, keepalive KeepaliveConfig, onError func(error), onDrop func()) *Pool {
	if keepalive.OnFault == nil {
		keepalive.OnFault = onError
	}

	return &Pool{
		tlsConfig: tlsConfig,
		keepalive: keepalive,
		onError:   onError,
		onDrop:    onDrop,
		senders:   make(map[string]Sender),
	}
}

// discardSender is what a closed pool hands out: product offered to it is dropped, and
// nothing is dialled or queued.
//
// The drop is reported. A closed pool usually belongs to an element shutting down,
// where a report may well not arrive; but a pool is also closed by a reconfiguration, where
// the ADMF is reachable and this is simply product the element produced and did not deliver.
// Staying silent would decide on the caller's behalf that nobody wants to be told. onDrop is
// the POI's own hook, it already means "product was lost, not a destination fault", and what
// a POI does with it during teardown is the POI's business.
//
// It reports itself reachable, because Unreachable answers what the last exchange
// with a destination established and this has had none. Answering true would put a
// closed pool's phantom destinations into an element's fault status.
type discardSender struct {
	onDrop func()
}

// Send counts pdu as dropped. A nil pdu is refused with ErrNilPDU and not counted, as every
// other entry point refuses one: no unit existed to lose.
func (d discardSender) Send(pdu *PDU) error {
	if pdu == nil {
		return ErrNilPDU
	}
	if d.onDrop != nil {
		d.onDrop()
	}

	return nil
}

func (discardSender) Close() error      { return nil }
func (discardSender) Unreachable() bool { return false }

// For returns the delivery client for addr ("host:port"), creating it on first use.
//
// Each call takes the pool's lock for a map lookup. That is cheap at the rate of
// interception-related records, where the IRI-POIs call it once per record and destination; a
// caller delivering at packet rate keeps the Sender instead, or every delivery of every task
// would serialise on the lock.
//
// It never returns nil: see the closed case below.
func (p *Pool) For(addr string) Sender {
	p.mu.Lock()
	defer p.mu.Unlock()

	if s, ok := p.senders[addr]; ok {
		return s
	}
	// A closed pool builds nothing. Close empties the map, so a delivery racing it
	// would otherwise miss the cache, construct a fresh sender with a worker
	// goroutine behind it, and store it in a pool nobody will close again — a worker
	// running past the shutdown that was supposed to end it, holding a connection to
	// a mediation function this element no longer answers for.
	//
	// It returns a sender that discards rather than a nil one. Both callers check for
	// nil today, so nothing is broken; what the check costs is that the *next* caller
	// must know to make it, and the failure if it does not is a nil-interface panic on
	// a delivery path — which is a signalling or data-plane path whose contract is that
	// delivery can neither block it nor fault it. A discarding sender keeps that
	// contract without asking anything of the caller.
	if p.closed {
		return discardSender{onDrop: p.onDrop}
	}

	s := NewAsyncSender(NewClient(addr, p.tlsConfig, p.keepalive), 0, p.onError, p.onDrop)
	p.senders[addr] = s

	return s
}

// UnreachableAt answers for one address what UnreachableAmong counts over many: is
// this destination currently unreachable.
//
// An address nothing has been sent to answers false, on the same reasoning
// UnreachableAmong applies — an element that has delivered nothing has not found a
// mediation function unreachable, it has not looked.
//
// It exists because a destination-scoped fault report names one destination, so the
// question the counting form answers ("how many") is not the one being asked. Both
// read the same state and neither performs I/O.
func (p *Pool) UnreachableAt(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.senders[addr]
	if !ok {
		return false
	}
	r, ok := s.(Reachability)

	return ok && r.Unreachable()
}

// UnreachableAmong reports how many of addrs cannot currently be reached, and how many of
// them this pool has attempted a delivery to at all.
//
// Both numbers, because an element may deliver to several agencies and "one of three" and
// "three of three" call for different responses from a provisioning function. Neither names
// an address: which destination is failing belongs to that destination's own status, not to
// the element's, and a probe is the one place that distinction is easy to lose.
//
// The caller passes the destinations its *tasking* currently names, and that is the whole
// point of the argument. A pool never forgets a client, so a destination whose last delivery
// failed and whose warrant was then withdrawn would otherwise count for the life of the
// process — nothing will ever deliver there again to clear it, and an element holding no
// tasking at all would report itself faulty. Asking only about destinations still in use is
// what prevents that.
//
// A destination nothing has been sent to is not counted either way — see Client.Unreachable —
// so an element that has delivered nothing reports nothing. A destination named twice counts
// once: two warrants to one agency are one place product goes, and counting it twice would
// make "2 of 2 unreachable" out of a single failing endpoint.
//
// It performs no I/O and answers from state each client already holds, so it is safe to call
// from a fault probe on the X1 request goroutine.
func (p *Pool) UnreachableAmong(addrs []string) (unreachable, inUse int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	counted := make(map[string]bool, len(addrs))
	for _, addr := range addrs {
		if counted[addr] {
			continue
		}
		counted[addr] = true

		s, ok := p.senders[addr]
		if !ok {
			continue
		}
		inUse++
		if r, ok := s.(Reachability); ok && r.Unreachable() {
			unreachable++
		}
	}

	return unreachable, inUse
}

// closeTimeout bounds how long Close waits for the pool's senders to drain.
//
// A blackholed mediation function must not decide how long this element takes to stop.
// Closing a sender waits for its worker to finish the queue, and each unit is bounded only
// by the client's own write deadline (defaultWriteTimeout) — so one destination that accepts a connection and
// then reads nothing costs (queue depth × write timeout), which is minutes. A network
// function shutting down inside a container's grace period is SIGKILLed part-way through its
// LI teardown instead, and what a SIGKILL leaves is a POI whose X1 tasking was never
// withdrawn and whose peers are left to their own fail-safes.
//
// Worse than slow, it is *observable*: an element serving a tasked subscriber whose MDF is
// blackholed takes minutes to stop where an untasked one takes none.
//
// Two seconds is far below any grace period a deployment sets and far above the time a
// working destination needs to drain a queue it is reading.
const closeTimeout = 2 * time.Second

// Close drains and closes every client. It returns the first error, having closed the
// rest regardless: a half-closed pool would leave delivery workers running with nothing
// left to deliver to.
//
// The drain is bounded (see closeTimeout) and the senders are closed concurrently, so the
// bound is per pool rather than per destination. A sender still draining when the bound
// expires is left to its goroutine: this runs at teardown, the process is about to go, and
// the alternative is being killed part-way through the same work with less of it done. What
// that sender has not yet started to deliver is abandoned and reported to onDrop, one call per
// unit, so the product the process takes with it is counted rather than silently lost.
func (p *Pool) Close() error {
	p.mu.Lock()
	senders := p.senders
	p.senders = make(map[string]Sender)
	p.closed = true
	p.mu.Unlock()

	// Concurrently: closed in line, the pool's shutdown would take as long as the sum of its
	// unreachable destinations.
	type closed struct {
		addr string
		err  error
	}

	// n sizes results and bounds the collection loop. That loop deletes each sender that has
	// answered, so what is left in the map when the deadline fires is exactly the senders to
	// abandon.
	n := len(senders)
	results := make(chan closed, n)
	for addr, s := range senders {
		go func(addr string, s Sender) { results <- closed{addr, s.Close()} }(addr, s)
	}

	var firstErr error

	deadline := time.After(closeTimeout)
	for range n {
		select {
		case r := <-results:
			delete(senders, r.addr)

			if r.err != nil && firstErr == nil {
				firstErr = r.err
			}
		case <-deadline:
			if firstErr == nil {
				firstErr = errCloseTimedOut
			}

			// What these senders still hold will not be delivered before the process
			// that is closing the pool goes, so it is counted as dropped rather than left
			// to drain unaccounted for. See AsyncSender.abandon.
			for _, s := range senders {
				if a, ok := s.(*AsyncSender); ok {
					a.abandon()
				}
			}

			return firstErr
		}
	}

	return firstErr
}

// errCloseTimedOut says the pool stopped waiting for a destination to drain. It is
// returned rather than logged because the caller is the network function's own teardown,
// which is the one party that can decide whether it matters.
var errCloseTimedOut = errors.New("x2x3: delivery pool did not drain within its close deadline")
