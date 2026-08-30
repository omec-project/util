// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package drsm

import (
	"sync"
	"testing"
)

const (
	scanSeed   = 1024
	scanRounds = 2000
)

func newScanTestChunk(id int32) *chunk {
	return &chunk{Id: id, State: Scanning, AllocIds: make(map[int32]bool)}
}

// TestScanFieldsConcurrentAccess drives the scan goroutine's handling of ScanIds, FreeIds and
// AllocIds against the two API paths that touch the same fields. api.go calls AllocateIntID
// and ReleaseIntID with mutex held, so the test does the same, which is what makes the scan
// goroutine's accesses the unsynchronised side.
//
// Releasing an id that belongs to a chunk still being scanned is a supported path:
// ReleaseInt32ID looks the chunk up in scanChunks precisely so that can happen, and
// ReleaseIntID then walks ScanIds while the scan goroutine is popping from it.
func TestScanFieldsConcurrentAccess(t *testing.T) {
	c := newScanTestChunk(1)
	c.appendScanIds(scanSeed)

	var (
		seenMu sync.Mutex
		seen   = make(map[int32]bool)
	)

	var wg sync.WaitGroup
	wg.Add(3)

	// The scan goroutine: pop an id, consult the callback outside the lock, record the result.
	go func() {
		defer wg.Done()
		for {
			id, ok := c.nextScanId()
			if !ok {
				return
			}
			seenMu.Lock()
			if seen[id] {
				t.Errorf("id %d was handed out twice by nextScanId", id)
				seenMu.Unlock()
				return
			}
			seen[id] = true
			seenMu.Unlock()

			c.recordScanResult(id, id%3 != 0)
		}
	}()

	// ReleaseInt32ID's inner call. With State == Scanning this also walks ScanIds.
	go func() {
		defer wg.Done()
		for i := range int32(scanRounds) {
			mutex.Lock()
			c.ReleaseIntID(i % scanSeed)
			mutex.Unlock()
		}
	}()

	// AllocateInt32ID's inner call.
	go func() {
		defer wg.Done()
		for range scanRounds {
			mutex.Lock()
			if _, err := c.AllocateIntID(); err != nil {
				// Expected once FreeIds is drained; the point of this goroutine is contention, not allocation.
				mutex.Unlock()
				continue
			}
			mutex.Unlock()
		}
	}()

	wg.Wait()

	// appendScanIds seeds distinct ids and both mutators only remove, so nothing may be left.
	if _, ok := c.nextScanId(); ok {
		t.Error("ScanIds should be drained once the scan goroutine has finished")
	}
}

// TestAppendScanIdsSeedsDistinctIds pins the seeding behaviour the drain assertion relies on.
func TestAppendScanIdsSeedsDistinctIds(t *testing.T) {
	c := newScanTestChunk(2)
	c.appendScanIds(8)

	got := make(map[int32]bool)
	for {
		id, ok := c.nextScanId()
		if !ok {
			break
		}
		if got[id] {
			t.Fatalf("id %d seeded more than once", id)
		}
		got[id] = true
	}
	if len(got) != 8 {
		t.Errorf("seeded %d ids, want 8", len(got))
	}
}
