// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x2x3

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// recordingSender is a test double for the Sender interface. When block is
// non-nil, Send blocks until it is closed, letting a test hold the worker so the
// bounded queue fills.
type recordingSender struct {
	mu     sync.Mutex
	sent   int
	closed bool
	// closeCount is how many times Close reached this sender. A bool cannot express
	// idempotence, which is what AsyncSender.Close claims and did not have.
	closeCount int
	err        error
	block      chan struct{}
	// entered, when non-nil, receives once per Send as it starts, before any block, so a
	// test can wait until the worker is holding a PDU.
	entered chan struct{}
}

func (r *recordingSender) Send(*PDU) error {
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent++
	return r.err
}

func (r *recordingSender) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.closeCount++
	return nil
}

func (r *recordingSender) closes() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closeCount
}

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent
}

// TestAsyncSenderDelivers checks enqueued PDUs are delivered to the inner Sender
// and that Close drains the queue and closes the inner Sender.
func TestAsyncSenderDelivers(t *testing.T) {
	rec := &recordingSender{}
	a := NewAsyncSender(rec, 8, nil, nil)
	for range 3 {
		if err := a.Send(&PDU{Type: PDUTypeX2}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rec.count(); got != 3 {
		t.Errorf("delivered %d PDUs, want 3", got)
	}
	if !rec.closed {
		t.Error("inner Sender was not closed")
	}
}

// TestAsyncSenderErrorCallback checks a worker delivery error invokes onError.
func TestAsyncSenderErrorCallback(t *testing.T) {
	rec := &recordingSender{err: errors.New("mdf down")}
	var mu sync.Mutex
	errs := 0
	a := NewAsyncSender(rec, 8, func(error) { mu.Lock(); errs++; mu.Unlock() }, nil)
	//nolint:errcheck // test
	_ = a.Send(&PDU{Type: PDUTypeX2})
	_ = a.Close()
	mu.Lock()
	defer mu.Unlock()
	if errs != 1 {
		t.Errorf("onError ran %d times, want 1", errs)
	}
}

// TestAsyncSenderDropsWhenFull checks that Send never blocks and drops (invoking
// onDrop) once the buffer is full — the key undetectability property: a stalled
// MDF must not block the caller. With depth 1 and the worker held, at most two
// PDUs are accepted (one in-flight in the worker, one queued), so the rest drop.
func TestAsyncSenderDropsWhenFull(t *testing.T) {
	block := make(chan struct{})
	rec := &recordingSender{block: block, entered: make(chan struct{}, 1)}
	var mu sync.Mutex
	drops := 0
	a := NewAsyncSender(rec, 1, nil, func() { mu.Lock(); drops++; mu.Unlock() })

	// The worker drains whatever is already queued into one batch, so the sends below only
	// meet a full queue once it is blocked delivering. Wait for that first, rather than
	// race it.
	if err := a.Send(&PDU{Type: PDUTypeX2}); err != nil {
		t.Fatalf("Send must never return an error: %v", err)
	}
	<-rec.entered

	// The worker holds the first PDU and the queue holds one more; every other send drops.
	const n = 10
	for range n {
		if err := a.Send(&PDU{Type: PDUTypeX2}); err != nil {
			t.Fatalf("Send must never return an error: %v", err)
		}
	}
	mu.Lock()
	d := drops
	mu.Unlock()
	if d != n-1 {
		t.Errorf("dropped %d of %d with the worker blocked and depth 1, want %d", d, n, n-1)
	}

	close(block) // let the worker drain so Close can finish
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestAsyncSenderBatchesWhatIsAlreadyQueued covers delivery coalescing: PDUs that are
// already waiting share one write, which is where the syscall and TLS-record saving comes
// from. It must never wait for a batch to fill, or delivery latency would depend on how
// idle the target is.
func TestAsyncSenderBatchesWhatIsAlreadyQueued(t *testing.T) {
	rec := &batchRecorder{}
	// Depth well above the number queued, so nothing is dropped.
	a := NewAsyncSender(rec, 256, nil, nil)

	const n = 50
	for i := 0; i < n; i++ {
		if err := a.Send(&PDU{Type: PDUTypeX3}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	a.Close()

	total, calls := rec.totals()
	if total != n {
		t.Errorf("delivered %d PDUs, want %d", total, n)
	}
	// The point of batching: fewer writes than PDUs. Exactly how many depends on
	// scheduling, so this asserts the property rather than a number.
	if calls >= n {
		t.Errorf("took %d writes for %d PDUs; nothing was batched", calls, n)
	}
}

// closeRaceChildEnv names the test whose fault-reproducing body this process is
// running. Both faults land where no test can recover them — a nil dereference on
// the delivery worker, and a send on a closed channel on whichever goroutine
// offered the PDU — so the body runs in a child process and the parent reads its
// exit status.
const closeRaceChildEnv = "X2X3_CLOSE_RACE_CHILD"

// inChildProcess reports whether this process is the child spawned for the named test.
func inChildProcess(name string) bool { return os.Getenv(closeRaceChildEnv) == name }

// runInSubprocess re-runs the test binary for t's test alone, with the marker set,
// and fails t with the child's output if it did not exit cleanly.
func runInSubprocess(t *testing.T) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), closeRaceChildEnv+"="+t.Name())

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("delivery faulted under Close: %v\n%s", err, out)
	}
}

// TestAsyncSenderCloseDoesNotFaultRealClient closes a sender while its worker is
// draining into the *Client the network functions actually deliver through. A
// closed channel is immediately readable and yields the zero value, so a drain
// that does not check the closed result appends a nil *PDU to the batch and
// SendBatch dereferences it. The test doubles elsewhere in this file survive that
// — they only count — which is why this one uses the real client.
//
// One queued PDU and an immediate Close is the shape that hits it every time: the
// worker is parked on the receive, the send hands the PDU straight to it, and the
// close lands while it is still being scheduled — so it enters the drain with the
// channel already closed.
//
// The client's address is never reached: the fault is in Marshal, which SendBatch
// performs before any I/O.
func TestAsyncSenderCloseDoesNotFaultRealClient(t *testing.T) {
	t.Parallel()

	if !inChildProcess(t.Name()) {
		runInSubprocess(t)
		return
	}

	for range 100 {
		a := NewAsyncSender(NewClient("127.0.0.1:1", nil, KeepaliveConfig{}), 8, nil, nil)
		//nolint:errcheck // Send returns an error only for a nil PDU
		_ = a.Send(&PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable})
		//nolint:errcheck // nothing was ever connected
		_ = a.Close()
	}
}

// TestAsyncSenderCloseAddsNothingToTheBatch observes the same window through a
// counting double instead of a real client: what the drain appends when the
// channel closes under it is a nil PDU, and the double delivers it as an extra.
// TestAsyncSenderBatchesWhatIsAlreadyQueued sees it too, as `delivered 51 PDUs,
// want 50`, but only when the scheduling happens to line up; this states the
// window directly.
func TestAsyncSenderCloseAddsNothingToTheBatch(t *testing.T) {
	for range 100 {
		rec := &batchRecorder{}
		a := NewAsyncSender(rec, 8, nil, nil)
		if err := a.Send(&PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if err := a.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		if total, _ := rec.totals(); total != 1 {
			t.Fatalf("delivered %d PDUs for the one that was queued, want 1", total)
		}
	}
}

// TestAsyncSenderSendRacingClose covers the second, separate fault: Send and Close
// share no state at all, so a Send that reaches the channel operation after Close
// has closed it panics on the caller's goroutine — a signalling or data-plane
// goroutine, whose contract is that delivery can neither block it nor fail it.
//
// It also asserts the boundary Close draws: the worker is gone when Close returns,
// so a PDU offered afterwards cannot be delivered.
func TestAsyncSenderSendRacingClose(t *testing.T) {
	t.Parallel()

	if !inChildProcess(t.Name()) {
		runInSubprocess(t)
		return
	}

	for range 200 {
		rec := &recordingSender{}
		a := NewAsyncSender(rec, 4, nil, nil)

		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 64 {
					//nolint:errcheck // Send returns an error only for a nil PDU
					_ = a.Send(&PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable})
				}
			}()
		}

		//nolint:errcheck // the double has no connection to release
		_ = a.Close()
		atClose := rec.count()
		wg.Wait()

		if got := rec.count(); got != atClose {
			t.Fatalf("delivered %d PDUs after Close returned", got-atClose)
		}
	}
}

// TestAsyncSenderSendAfterCloseDrops is the deterministic form of the same fault:
// product offered to a closed sender is dropped and reported as a drop, by the
// same means as product dropped for any other reason, and the offering path
// proceeds. Delivery is not permitted to fault the path carrying it.
func TestAsyncSenderSendAfterCloseDrops(t *testing.T) {
	rec := &recordingSender{}
	var mu sync.Mutex
	drops := 0
	a := NewAsyncSender(rec, 8, nil, func() { mu.Lock(); drops++; mu.Unlock() })

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	delivered := rec.count()

	for range 3 {
		if err := a.Send(&PDU{Type: PDUTypeX3, PayloadFormat: PayloadFormatIPv4, Direction: DirectionNotApplicable}); err != nil {
			t.Fatalf("Send must never return an error: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if drops != 3 {
		t.Errorf("onDrop ran %d times for 3 PDUs offered to a closed sender, want 3", drops)
	}
	if got := rec.count(); got != delivered {
		t.Errorf("delivered %d PDUs after Close", got-delivered)
	}
}

// batchRecorder counts PDUs and the number of write calls they took.
type batchRecorder struct {
	mu    sync.Mutex
	pdus  int
	calls int
}

func (b *batchRecorder) Send(*PDU) error {
	b.mu.Lock()
	b.pdus++
	b.calls++
	b.mu.Unlock()

	return nil
}

func (b *batchRecorder) SendBatch(pdus []*PDU) error {
	b.mu.Lock()
	b.pdus += len(pdus)
	b.calls++
	b.mu.Unlock()

	return nil
}

// Close satisfies Sender; there is no connection to release.
func (b *batchRecorder) Close() error { return nil }

func (b *batchRecorder) totals() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.pdus, b.calls
}

// TestANilProductUnitIsRefusedAtEveryEntryPoint: invalid input may not terminate the
// hosting network function.
//
// These entry points are called from a signalling goroutine at the AMF and SMF and from a
// framing worker at the UPF, and a nil dereferenced there faults the *element* rather than
// the mediation function it was delivering to — on a goroutine whose panic no caller can
// recover. SendBatch guarded its own slice; Send, AsyncSender.Send and Marshal did not, so
// what happened depended on whether a caller delivered one unit alone or through a batch.
//
// Marshal is where the property is enforced for every path, and each entry point states the
// same answer, so the error a caller gets does not depend on how far in the nil travelled.
func TestANilProductUnitIsRefusedAtEveryEntryPoint(t *testing.T) {
	t.Run("Marshal", func(t *testing.T) {
		var p *PDU
		if _, err := p.Marshal(); !errors.Is(err, ErrNilPDU) {
			t.Errorf("Marshal on a nil receiver returned %v, want ErrNilPDU", err)
		}
	})

	t.Run("Client.Send", func(t *testing.T) {
		// An address nothing is listening on: the point is that it is never dialled.
		c := NewClient("127.0.0.1:1", nil, KeepaliveConfig{Disabled: true})
		t.Cleanup(func() { c.Close() }) //nolint:errcheck // test cleanup

		if err := c.Send(nil); !errors.Is(err, ErrNilPDU) {
			t.Errorf("Send(nil) returned %v, want ErrNilPDU", err)
		}
	})

	t.Run("AsyncSender.Send", func(t *testing.T) {
		inner := &recordingSender{}
		a := NewAsyncSender(inner, 4, nil, nil)
		t.Cleanup(func() { a.Close() }) //nolint:errcheck // test cleanup

		if err := a.Send(nil); !errors.Is(err, ErrNilPDU) {
			t.Errorf("Send(nil) returned %v, want ErrNilPDU", err)
		}
		// Enqueued as nothing, so the worker never sees it.
		if n := inner.count(); n != 0 {
			t.Errorf("the delivery worker was handed %d units, want 0: a nil enqueued here faults "+
				"the worker goroutine, which takes the network function down over invalid input", n)
		}
	})

	t.Run("a closed Pool's sender", func(t *testing.T) {
		drops := 0
		p := NewPool(nil, KeepaliveConfig{Disabled: true}, nil, func() { drops++ })
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}

		if err := p.For("127.0.0.1:1").Send(nil); !errors.Is(err, ErrNilPDU) {
			t.Errorf("Send(nil) after Close returned %v, want ErrNilPDU", err)
		}
		if drops != 0 {
			t.Errorf("a nil unit was counted as %d dropped, want 0: no unit existed to lose", drops)
		}
	})

	t.Run("delivery still works afterwards", func(t *testing.T) {
		inner := &recordingSender{}
		a := NewAsyncSender(inner, 4, nil, nil)
		t.Cleanup(func() { a.Close() }) //nolint:errcheck // test cleanup

		//nolint:errcheck // asserted above
		_ = a.Send(nil)
		if err := a.Send(&PDU{Type: PDUTypeX2, PayloadFormat: PayloadFormat3GPP33128, Direction: DirectionNotApplicable}); err != nil {
			t.Fatalf("Send after a refused nil returned %v", err)
		}
		if err := a.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if n := inner.count(); n != 1 {
			t.Errorf("the destination received %d units after a refused nil, want 1: refusing "+
				"invalid input must not cost the delivery that follows it", n)
		}
	})
}

// TestAsyncCloseIsIdempotentInWhatItDoes: a second Close must not close the inner sender
// again. *Client guards its own second close, but this type takes any Sender, and Pool.Close
// can give up on a sender that is still draining, so a sender may be closed more than once.
func TestAsyncCloseIsIdempotentInWhatItDoes(t *testing.T) {
	inner := &recordingSender{}
	a := NewAsyncSender(inner, 4, nil, nil)

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if n := inner.closes(); n != 1 {
		t.Errorf("the inner sender was closed %d times, want 1: what a second Close does would "+
			"depend on which Sender implementation was behind it", n)
	}
}

// failingFirstSender fails the first PDU it is given and accepts every later one, recording how
// many it was asked to send. It has no SendBatch, so AsyncSender sends a batch to it one PDU at a
// time.
type failingFirstSender struct {
	mu       sync.Mutex
	attempts int
}

func (f *failingFirstSender) Send(*PDU) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.attempts++
	if f.attempts == 1 {
		return errors.New("the first PDU could not be sent")
	}

	return nil
}

func (f *failingFirstSender) Close() error { return nil }

// TestAFailedPDUDoesNotCostTheRestOfItsBatch: delivered one PDU at a time, a batch whose first
// PDU fails must still attempt the others, and the failure must still be returned. Stopping at
// the first error lost the remainder silently: one error reported, a run of sequence numbers
// missing at the mediation function, and nothing in the element's own account for the rest.
func TestAFailedPDUDoesNotCostTheRestOfItsBatch(t *testing.T) {
	inner := &failingFirstSender{}
	a := NewAsyncSender(inner, 4, nil, nil)
	t.Cleanup(func() { _ = a.Close() })

	err := a.send([]*PDU{{Type: PDUTypeX2}, {Type: PDUTypeX2}, {Type: PDUTypeX2}})

	if err == nil {
		t.Error("a batch with a PDU that failed was reported as delivered")
	}

	inner.mu.Lock()
	attempts := inner.attempts
	inner.mu.Unlock()

	if attempts != 3 {
		t.Errorf("%d of the batch's 3 PDUs were attempted; one PDU's failure cost the rest of "+
			"the batch", attempts)
	}
}
