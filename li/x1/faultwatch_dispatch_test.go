// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// unreachableDIDs is a health supplier listing n provisioned destinations, all unreachable.
func unreachableDIDs(n int) func() []DestinationHealth {
	health := make([]DestinationHealth, n)
	for i := range health {
		health[i] = DestinationHealth{DID: NewUUID(), Address: endpointA, Unreachable: true}
	}

	return func() []DestinationHealth { return health }
}

// watching runs w until the test ends, returning the stop channel and a channel closed when
// Watch returns.
func watching(t *testing.T, w *DestinationWatcher) (stop, done chan struct{}) {
	t.Helper()

	stop, done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		w.Watch(stop)
	}()
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		<-done
	})

	return stop, done
}

// TestWatchStopsBetweenReports: a sample is a sequence of synchronous reports, one per
// transition. A watcher told to stop while one is on the wire makes no further reports, so
// stopping takes one round trip rather than one per destination.
func TestWatchStopsBetweenReports(t *testing.T) {
	admf := newBlockingADMF(t)
	admf.hold.Store(true)

	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	w := NewDestinationWatcher(unreachableDIDs(5), r, time.Hour)
	stop, done := watching(t, w)

	w.Nudge()
	admf.awaitTotal(t, 1)
	close(stop)
	admf.unblock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop")
	}
	if n := admf.total.Load(); n != 1 {
		t.Errorf("the watcher made %d reports after being told to stop during the first, want 1", n)
	}
}

// TestWatchHoldsNoLockAcrossAReport: the watcher's record is locked while a sample decides
// and while it records the outcome, never across a report, which is a round trip to the ADMF.
func TestWatchHoldsNoLockAcrossAReport(t *testing.T) {
	admf := newBlockingADMF(t)
	admf.hold.Store(true)

	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	w := NewDestinationWatcher(unreachableDIDs(1), r, time.Hour)
	watching(t, w)

	w.Nudge()
	admf.awaitTotal(t, 1)

	if !w.mu.TryLock() {
		t.Fatal("the watcher holds its lock while a report is on the wire")
	}
	w.mu.Unlock()
	admf.unblock()
}

// TestANudgeBurstIsCoalesced: a delivery path failing at packet rate nudges at packet rate.
// Each nudge sampling at once would make a report attempt per nudge while the ADMF refuses
// them; nudges inside minNudgeInterval are coalesced into one sample at its end — deferred,
// not dropped, so the failure that prompted them is still looked at.
func TestANudgeBurstIsCoalesced(t *testing.T) {
	admf := newBlockingADMF(t)
	admf.status.Store(http.StatusInternalServerError)

	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)
	w := NewDestinationWatcher(unreachableDIDs(1), r, time.Hour)
	w.nudgeGap = 300 * time.Millisecond
	watching(t, w)

	for range 50 {
		w.Nudge()
		time.Sleep(2 * time.Millisecond)
	}
	if n := admf.total.Load(); n > 1 {
		t.Errorf("a burst of nudges inside one gap made %d report attempts, want at most 1", n)
	}

	admf.awaitTotal(t, 2)
	if n := admf.total.Load(); n != 2 {
		t.Errorf("made %d report attempts, want 2: the first nudge and one deferred to the gap's end", n)
	}
}

// TestANudgeNeverBlocks: delivery sites call Nudge on the delivery path, which must not wait
// on reporting — with no watcher running, or no watcher at all.
func TestANudgeNeverBlocks(t *testing.T) {
	w := NewDestinationWatcher(unreachableDIDs(1), nil, time.Hour)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			w.Nudge()
		}
		(*DestinationWatcher)(nil).Nudge()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Nudge blocked with no watcher draining it")
	}
}

// TestAFailedRetractionIsAskedAgain: a destination fault is retracted when the destination
// leaves the health set. A retraction the ADMF does not accept leaves the fault standing
// there, so the watcher keeps it and asks again at the next sample.
func TestAFailedRetractionIsAskedAgain(t *testing.T) {
	admf := newBlockingADMF(t)
	r := NewReporter(admf.srv.URL, "admfID", "neID", nil)

	health := []DestinationHealth{{DID: didAgencyA, Address: endpointA, Unreachable: true}}
	w := NewDestinationWatcher(func() []DestinationHealth { return health }, r, time.Hour)
	holdClockOn(w)

	w.sample()
	admf.awaitTotal(t, 1)

	health = nil
	admf.status.Store(http.StatusInternalServerError)
	w.sample()
	admf.awaitTotal(t, 2)

	admf.status.Store(http.StatusOK)
	w.sample()
	admf.awaitTotal(t, 3)

	before := admf.total.Load()
	w.sample()
	if n := admf.total.Load() - before; n != 0 {
		t.Errorf("a retraction the ADMF accepted was sent again (%d more)", n)
	}
}

// TestTheNudgeGapIsMeasuredOnTheWatchersClock: the gap between nudge-driven samples is read on
// the same clock as the recovery hold. A nudge after that clock has passed the gap samples at
// once rather than waiting on a second, real clock.
func TestTheNudgeGapIsMeasuredOnTheWatchersClock(t *testing.T) {
	var (
		mu      sync.Mutex
		samples int
		at      = zeroTailInstant
	)
	w := NewDestinationWatcher(func() []DestinationHealth {
		mu.Lock()
		defer mu.Unlock()
		samples++

		return nil
	}, nil, time.Hour)
	w.nudgeGap = time.Second
	w.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return at
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()

		return samples
	}
	watching(t, w)

	w.Nudge()
	within(t, "the first nudged sample", func() bool { return count() == 1 })

	mu.Lock()
	at = at.Add(2 * time.Second)
	mu.Unlock()

	start := time.Now()
	w.Nudge()
	within(t, "the second nudged sample", func() bool { return count() == 2 })
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Errorf("the second nudge waited %s on a gap its clock had already passed", waited)
	}
}
