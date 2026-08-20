// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package drsm

import (
	"fmt"
	"sync"
	"testing"
)

const (
	testPod    = "amf-test-pod"
	otherPod   = "amf-other-pod"
	iterations = 200
)

// newTestDrsm builds a Drsm with only the in-memory state initialised. Every path exercised
// here stays inside those maps, so no MongoDB connection is needed.
func newTestDrsm() *Drsm {
	return &Drsm{
		mode:           ResourceClient,
		clientId:       PodId{PodName: testPod},
		localChunkTbl:  make(map[int32]*chunk),
		globalChunkTbl: make(map[int32]*chunk),
		podMap:         make(map[string]*podData),
		scanChunks:     make(map[int32]*chunk),
		podDown:        make(chan string, 10),
	}
}

func newTestChunk(id int32, freeIds int32) *chunk {
	c := &chunk{Id: id, Owner: PodId{PodName: testPod}, AllocIds: make(map[int32]bool)}
	for i := int32(0); i < freeIds; i++ {
		c.FreeIds = append(c.FreeIds, i)
	}
	return c
}

// TestScanTablesConcurrentAccess drives the scan goroutines started by claimChunk against
// the AllocateInt32ID/ReleaseInt32ID API paths. scanChunks and localChunkTbl are reachable
// from both, so every access has to hold mutex.
func TestScanTablesConcurrentAccess(t *testing.T) {
	d := newTestDrsm()

	allocatable := newTestChunk(1, 64)
	d.localChunkTbl[allocatable.Id] = allocatable

	scanning := newTestChunk(2, 0)
	d.startScan(scanning)

	completing := newTestChunk(3, 0)

	var wg sync.WaitGroup
	wg.Add(4)

	// Allocation ranges localChunkTbl; releasing the id back keeps FreeIds from running
	// out, which would otherwise reach GetNewChunk and MongoDB.
	go func() {
		defer wg.Done()
		for range iterations {
			id, err := d.AllocateInt32ID()
			if err != nil {
				t.Errorf("AllocateInt32ID: %v", err)
				return
			}
			if err := d.ReleaseInt32ID(id); err != nil {
				t.Errorf("ReleaseInt32ID(%d): %v", id, err)
				return
			}
		}
	}()

	// Releasing an id of a chunk that is still being scanned misses localChunkTbl and then
	// reads scanChunks.
	go func() {
		defer wg.Done()
		for i := range iterations {
			id := scanning.Id<<10 | int32(i%1024)
			if err := d.ReleaseInt32ID(id); err != nil {
				t.Errorf("ReleaseInt32ID(%d): %v", id, err)
				return
			}
		}
	}()

	// A chunk being published as scanning: writes scanChunks.
	go func() {
		defer wg.Done()
		for range iterations {
			d.startScan(scanning)
		}
	}()

	// A completed scan: writes localChunkTbl and deletes from scanChunks.
	go func() {
		defer wg.Done()
		for range iterations {
			d.completeScan(completing)
			d.startScan(completing)
		}
	}()

	wg.Wait()

	if _, found := d.scanChunks[scanning.Id]; !found {
		t.Errorf("chunk %d should still be in scanChunks", scanning.Id)
	}
	if _, found := d.localChunkTbl[completing.Id]; !found {
		t.Errorf("chunk %d should have been moved to localChunkTbl", completing.Id)
	}
}

// TestPodMapConcurrentAccess drives the three goroutines that reach podMap: the change
// stream (addChunk, ensurePod, recordChunkOwner), the checkAllChunks ticker (addChunk) and
// the pod-down handler (podChunkIds).
func TestPodMapConcurrentAccess(t *testing.T) {
	d := newTestDrsm()
	d.ensurePod(&FullStream{PodId: testPod})

	addChunks := func(first int32) {
		for i := range int32(iterations) {
			id := first + i
			d.addChunk(&FullStream{Id: fmt.Sprintf("chunkid-%d", id), PodId: testPod})
		}
	}

	var wg sync.WaitGroup
	wg.Add(5)

	// The change stream and the periodic resync both call addChunk, on disjoint chunk ids.
	go func() {
		defer wg.Done()
		addChunks(1)
	}()
	go func() {
		defer wg.Done()
		addChunks(iterations + 1)
	}()

	// A chunk changing owner writes podChunks through recordChunkOwner.
	go func() {
		defer wg.Done()
		for i := range int32(iterations) {
			id := 2*iterations + 1 + i
			if !d.recordChunkOwner(testPod, id, newTestChunk(id, 0)) {
				t.Errorf("recordChunkOwner(%d): pod %s should be known", id, testPod)
				return
			}
		}
	}()

	// The pod-down handler ranges podChunks, and keepalives insert into podMap.
	go func() {
		defer wg.Done()
		for range iterations {
			d.podChunkIds(testPod)
			d.podDownCandidate(testPod)
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			d.ensurePod(&FullStream{PodId: otherPod})
		}
	}()

	wg.Wait()

	ids, owner := d.podChunkIds(testPod)
	if len(ids) != 3*iterations {
		t.Errorf("podChunks holds %d chunks, want %d", len(ids), 3*iterations)
	}
	if owner != testPod {
		t.Errorf("owner is %q, want %q", owner, testPod)
	}
	if len(d.globalChunkTbl) != 2*iterations {
		t.Errorf("globalChunkTbl holds %d chunks, want %d", len(d.globalChunkTbl), 2*iterations)
	}
}

// TestPodChunkIdsUnknownPod covers the pod-down path for a pod that is no longer in podMap.
// Dereferencing the missing entry used to panic.
func TestPodChunkIdsUnknownPod(t *testing.T) {
	d := newTestDrsm()

	ids, owner := d.podChunkIds("pod-that-never-registered")
	if ids != nil {
		t.Errorf("ids is %v, want nil", ids)
	}
	if owner != "" {
		t.Errorf("owner is %q, want empty", owner)
	}
}
