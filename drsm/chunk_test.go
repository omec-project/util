// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package drsm

import (
	"sync"
	"testing"
)

const (
	podXName   = "pod-x"
	podXIp     = "10.0.0.11"
	podYName   = "pod-y"
	podYIp     = "10.0.0.22"
	ownerLoops = 2000
)

// ownerPairs is the set of consistent owner records a reader may legitimately observe. A
// PodName paired with the other pod's PodIp means a read caught a half-finished write.
var ownerPairs = map[string]string{podXName: podXIp, podYName: podYIp}

// TestChunkOwnerConcurrentAccess drives the two writers of chunk.Owner against its two
// readers: claimChunk records this pod on a successful claim, the change-stream handler
// records the owner from the update that the claim triggered, scanChunk checks whether it
// still owns the chunk, and FindOwnerInt32ID hands the owner to the caller.
func TestChunkOwnerConcurrentAccess(t *testing.T) {
	c := &chunk{Id: 1, Owner: PodId{PodName: podXName, PodIp: podXIp, PodInstance: podXName + "-1"}}

	var wg sync.WaitGroup
	wg.Add(4)

	// The change-stream handler replaces the whole record.
	go func() {
		defer wg.Done()
		for i := range ownerLoops {
			if i%2 == 0 {
				c.setOwner(PodId{PodName: podXName, PodIp: podXIp, PodInstance: podXName + "-1"})
			} else {
				c.setOwner(PodId{PodName: podYName, PodIp: podYIp, PodInstance: podYName + "-1"})
			}
		}
	}()

	// claimChunk sets only the name and the address.
	go func() {
		defer wg.Done()
		for i := range ownerLoops {
			if i%2 == 0 {
				c.setOwnerAddress(podXName, podXIp)
			} else {
				c.setOwnerAddress(podYName, podYIp)
			}
		}
	}()

	// FindOwnerInt32ID's reader must never see a torn pair.
	go func() {
		defer wg.Done()
		for range ownerLoops {
			owner := c.GetOwner()
			want, known := ownerPairs[owner.PodName]
			if !known {
				t.Errorf("GetOwner returned unknown PodName %q", owner.PodName)
				return
			}
			if owner.PodIp != want {
				t.Errorf("torn owner: PodName %q with PodIp %q, want %q", owner.PodName, owner.PodIp, want)
				return
			}
		}
	}()

	// scanChunk's ownership check.
	go func() {
		defer wg.Done()
		for range ownerLoops {
			if _, known := ownerPairs[c.ownerPodName()]; !known {
				t.Errorf("ownerPodName returned unknown pod %q", c.ownerPodName())
				return
			}
		}
	}()

	wg.Wait()
}

// TestGetOwnerReturnsSnapshot covers the reason GetOwner copies: callers read the fields
// after FindOwnerInt32ID has released globalChunkTblMutex, so they must not hold a pointer
// into the chunk.
func TestGetOwnerReturnsSnapshot(t *testing.T) {
	c := &chunk{Id: 1, Owner: PodId{PodName: podXName, PodIp: podXIp}}

	owner := c.GetOwner()
	owner.PodName = "scribbled"
	if got := c.ownerPodName(); got != podXName {
		t.Errorf("writing to the returned PodId changed the chunk: owner is %q, want %q", got, podXName)
	}

	before := c.GetOwner()
	c.setOwner(PodId{PodName: podYName, PodIp: podYIp})
	if before.PodName != podXName {
		t.Errorf("a later setOwner mutated an earlier snapshot: %q, want %q", before.PodName, podXName)
	}
	if got := c.ownerPodName(); got != podYName {
		t.Errorf("setOwner did not take effect: %q, want %q", got, podYName)
	}
}
