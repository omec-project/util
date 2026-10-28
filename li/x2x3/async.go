// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"errors"
	"sync"
	"sync/atomic"
)

// Sender is the minimal delivery contract an X2/X3 POI depends on. *Client
// satisfies it, and so does *AsyncSender, so a POI can wrap one in the other
// without changing its call sites.
type Sender interface {
	Send(*PDU) error
	Close() error
}

// Reachability is implemented by a Sender that knows whether it can currently reach its
// destination — from what its last delivery attempt established, never by dialling.
// *Client and *AsyncSender both do.
//
// It is separate from Sender because a POI asks it from a fault probe rather than from the
// delivery path, and because a test double delivering into a slice has no destination to
// have an opinion about.
type Reachability interface {
	Unreachable() bool
}

// AsyncSender decouples X2/X3 delivery from the caller's goroutine. Send enqueues
// the PDU onto a bounded buffer drained by a single background worker and returns
// immediately, so a slow or unreachable MDF never blocks a signalling or
// data-plane path — the target-observable timing side channel and availability
// risk that synchronous delivery introduces. Delivery must stay decoupled from
// signalling and data-plane processing.
//
// A single worker preserves per-POI delivery order (the MDF correlates by
// Correlation ID and sequence number, not arrival, so strict ordering is not
// required, but it is free and avoids reordering within one POI). When the buffer
// is full Send drops the PDU rather than block — LI product may be lost under sustained MDF outage, but
// the target's service is never delayed — and invokes onDrop so the fault can be
// surfaced to the ADMF over X1. Worker delivery errors invoke onError for the
// same purpose, each carrying the number of units it cost (LostUnits). Both
// callbacks run on the worker goroutine, and onDrop on whichever goroutine called
// Send as well, so they must be safe for concurrent use and must not block.
type AsyncSender struct {
	inner   Sender
	queue   chan *PDU
	onError func(error)
	onDrop  func()

	wg        sync.WaitGroup
	closeOnce sync.Once
	// closeErr is what the inner sender's Close returned, cached so a second Close
	// answers the same and does not close it again. Written inside closeOnce and read
	// after it, which is the happens-before that makes it safe without a lock.
	closeErr error

	// closeMu guards closed, and is held for reading across Send's check-and-
	// enqueue so a Close cannot land between the two. An atomic flag would leave
	// exactly that window open, which is the send-on-closed-channel panic. Send
	// takes it for reading and never blocks under it, so concurrent offers still
	// do not serialise on each other; the only writer is Close, once.
	closeMu sync.RWMutex
	closed  bool

	// batch is reused by the delivery worker so coalescing costs no allocation.
	// Only the worker touches it.
	batch []*PDU

	// abandoned tells the worker to stop delivering and account for what is still queued
	// as dropped. Set by abandon, when whoever is closing this sender stops waiting for it.
	abandoned atomic.Bool
}

// defaultQueueDepth bounds buffered-but-undelivered PDUs. Sized generously for
// LI product volume; a full queue means the MDF has been unreachable long enough
// that dropping, and reporting each drop, is the right response — blocking would
// delay the subscriber's own traffic, which a subject could observe.
const defaultQueueDepth = 1024

// batchSender is a Sender that can deliver several PDUs in one write. *Client
// implements it; a plain Sender (a test double, say) is driven one at a time.
type batchSender interface {
	SendBatch([]*PDU) error
}

// maxDeliveryBatch bounds how many PDUs share one write. Larger batches amortise
// the syscall and TLS record overhead further but hold delivery back while they
// fill, so this is deliberately modest: it only ever batches what is *already*
// queued, never waits for more.
const maxDeliveryBatch = 32

// NewAsyncSender wraps inner with a bounded async delivery queue of the given
// depth (<=0 uses a default). onError is called with each worker delivery error;
// onDrop is called once per PDU that is dropped before delivery was attempted —
// a full buffer, a closed sender, or an abandoned queue. Either may be nil. The
// worker starts immediately; call Close to stop it and close inner.
func NewAsyncSender(inner Sender, depth int, onError func(error), onDrop func()) *AsyncSender {
	if depth <= 0 {
		depth = defaultQueueDepth
	}
	a := &AsyncSender{
		inner:   inner,
		queue:   make(chan *PDU, depth),
		onError: onError,
		onDrop:  onDrop,
	}
	a.batch = make([]*PDU, 0, maxDeliveryBatch)
	a.wg.Add(1)
	go a.run()
	return a
}

func (a *AsyncSender) run() {
	defer a.wg.Done()
	for pdu := range a.queue {
		// Take whatever else is already waiting and deliver it in one go. A PDU is
		// self-delimiting, so a batch needs no extra framing, and the saving is
		// real: one write and one set of TLS records instead of one per PDU. Under
		// light load the drain finds nothing queued and each PDU is written on its
		// own.
		batch := append(a.batch[:0], pdu)
	drain:
		for len(batch) < cap(a.batch) {
			select {
			case next, ok := <-a.queue:
				// A closed channel is immediately readable and yields the zero
				// value, so without this the batch collects a nil *PDU that the
				// delivery client then dereferences. Closed and empty means this
				// batch is the last one; the range above ends it.
				if !ok {
					break drain
				}
				batch = append(batch, next)
			default:
			}

			if len(a.queue) == 0 {
				break
			}
		}
		a.batch = batch

		if a.abandoned.Load() {
			for range batch {
				a.dropped()
			}
		} else if err := a.send(batch); err != nil && a.onError != nil {
			a.onError(err)
		}

		// The reused array would otherwise keep the last batch's PDUs, payloads included,
		// alive until the next batch overwrote them.
		clear(batch)
	}
}

func (a *AsyncSender) dropped() {
	if a.onDrop != nil {
		a.onDrop()
	}
}

// abandon tells the worker to deliver nothing more: every PDU it has not yet started on is
// reported to onDrop instead. A batch already being written finishes, and its outcome is
// reported as usual.
//
// It exists for a close that will not wait for the queue to drain (see Pool.Close). A worker
// left to drain on its own after the closer has given up would leave the queued product
// accounted for by nothing: not delivered before the process exits, and not reported as lost
// either. Abandoning it turns that into a count — one drop per unit — which is what the
// mediation function will see missing.
func (a *AsyncSender) abandon() { a.abandoned.Store(true) }

// Send enqueues pdu for asynchronous delivery and returns immediately. It never
// blocks; a full buffer drops the PDU and invokes onDrop. The caller must not
// mutate pdu after Send (the worker reads it later). The returned error is nil for
// any PDU, delivered or dropped — delivery happens on the worker, and its outcome is
// reported through onError and onDrop — and ErrNilPDU for a nil one.
//
// A nil pdu is not counted through onDrop: no product unit existed, so none was lost.
// It is a defect in the caller, and the error returned to that caller is the one place
// it can be seen and fixed.
//
// A sender that has been closed drops too, and reports the drop the same way. The
// events that close a sender — a purge, a reconfiguration, an element shutting
// down — are exactly the events during which product is still being offered, and
// the caller is a signalling or data-plane goroutine that delivery may neither
// block nor fault.
func (a *AsyncSender) Send(pdu *PDU) error {
	// Before the queue, and without dereferencing it. This is the entry point a POI
	// calls from a signalling goroutine or a framing worker, and a nil enqueued here would
	// fault the delivery worker rather than the caller — taking the network function down
	// over invalid input, on a goroutine whose panic no caller can recover. Reported as the
	// defined error and enqueued as nothing; batch semantics are unchanged.
	if pdu == nil {
		return ErrNilPDU
	}

	if a.enqueue(pdu) {
		return nil
	}

	a.dropped()

	return nil
}

// enqueue offers pdu to the queue and reports whether it was accepted. onDrop is
// left to the caller so it never runs under closeMu: it is supplied by the POI and
// nothing here should decide what it may touch.
func (a *AsyncSender) enqueue(pdu *PDU) bool {
	a.closeMu.RLock()
	defer a.closeMu.RUnlock()

	if a.closed {
		return false
	}

	select {
	case a.queue <- pdu:
		return true
	default:
		return false
	}
}

// Unreachable reports whether the sender behind the queue currently cannot reach its
// destination.
//
// Note what this deliberately does not answer: whether the queue is full *right now*, which
// is the state a POI reports as delivery loss — the mediation function is reachable but
// slower than the offered rate, so copies are being dropped as this is asked. It is a state,
// it is observable here without I/O, and a probe for it would be legitimate. It is left out
// because a full queue at one instant is not yet a fault an ADMF can act on: the queue is
// sized to absorb bursts, so it fills and drains under normal load, and a probe sampling it
// would report "faulty" for the bursts the buffer exists to swallow. Distinguishing a burst
// from sustained overload needs a window over time, which a probe answering from one instant
// does not have.
// The drops themselves are reported as they happen. If it is ever added, the condition to
// answer is "the queue has been full since the last time anything drained", not "the queue
// is full".
//
// The answer belongs to the wrapped sender: Send here only enqueues, and enqueueing can
// establish nothing about a destination. A sender that cannot answer — a test double — is
// taken as reachable, because inventing a fault is the failure that gets an element's
// status answer ignored altogether.
func (a *AsyncSender) Unreachable() bool {
	if inner, ok := a.inner.(Reachability); ok {
		return inner.Unreachable()
	}

	return false
}

// Close stops accepting new PDUs, drains those already queued, waits for the
// worker to finish, and closes the inner Sender. Safe to call more than once.
//
// Idempotent in what it does, not only in what it guards. Everything, the inner
// sender's own Close included, happens inside the once, so a second call does not close the
// inner sender again. *Client guards its own second close, but this type takes the
// interface, and what a second Close does must not depend on the implementation behind it.
// Pool.Close can give up on a sender that is still draining, so a sender may be closed more
// than once.
//
// The result is cached rather than recomputed, so every caller is told the same thing about
// the same teardown. A concurrent second caller blocks in the once until the first has
// finished, so it never answers before the teardown is done.
func (a *AsyncSender) Close() error {
	a.closeOnce.Do(func() {
		a.closeMu.Lock()
		a.closed = true
		close(a.queue)
		a.closeMu.Unlock()

		// Inside the once, so the inner sender is closed exactly once — and after the
		// wait, so it is closed with the queue drained.
		a.wg.Wait()
		a.closeErr = a.inner.Close()
	})

	return a.closeErr
}

// send delivers a batch, using one write when the underlying sender supports it.
//
// Without that, every PDU is still attempted when one before it fails, and the failures are
// returned together. Stopping at the first would lose the rest of the batch on the strength of
// one PDU — one that could not be marshalled, say, which says nothing about the others — and
// report it as a single error, so the element's own account would show one failure where the
// mediation function sees a run of missing sequence numbers. Client.SendBatch skips a PDU it
// cannot marshal for the same reason.
//
// Whatever it returns counts the units it cost (see DeliveryError). *Client counts its own; an
// error from a sender that does not is taken to have cost every unit it was given, because a
// sender that says only "failed" has not said that anything arrived.
func (a *AsyncSender) send(batch []*PDU) error {
	if len(batch) == 1 {
		return counted(a.inner.Send(batch[0]), 1)
	}

	if bs, ok := a.inner.(batchSender); ok {
		return counted(bs.SendBatch(batch), len(batch))
	}

	var (
		errs []error
		lost int
	)

	for _, pdu := range batch {
		if err := a.inner.Send(pdu); err != nil {
			errs = append(errs, err)
			lost += LostUnits(counted(err, 1))
		}
	}

	return lostUnits(lost, errors.Join(errs...))
}

// counted returns err as a DeliveryError, taking it to have cost all n units it was given
// when it does not already say how many it did.
func counted(err error, n int) error {
	if err == nil || LostUnits(err) > 0 {
		return err
	}

	return lostUnits(n, err)
}
