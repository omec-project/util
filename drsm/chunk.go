// SPDX-FileCopyrightText: 2022 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0

package drsm

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"

	"github.com/omec-project/util/logger"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// GetOwner returns a copy of the pod recorded as owning c. Returning &c.Owner would let the
// caller read the fields after FindOwnerInt32ID has dropped its lock, while claimChunk and
// the change-stream handler are still writing them.
func (c *chunk) GetOwner() *PodId {
	c.ownerMutex.Lock()
	defer c.ownerMutex.Unlock()
	owner := c.Owner
	return &owner
}

// setOwner replaces the recorded owner of c.
func (c *chunk) setOwner(owner PodId) {
	c.ownerMutex.Lock()
	defer c.ownerMutex.Unlock()
	c.Owner = owner
}

// setOwnerAddress records this pod as the owner. PodInstance is left as it was, which is what
// claimChunk has always done - the change-stream update that the claim triggers supplies it.
func (c *chunk) setOwnerAddress(podName, podIp string) {
	c.ownerMutex.Lock()
	defer c.ownerMutex.Unlock()
	c.Owner.PodName = podName
	c.Owner.PodIp = podIp
}

// ownerPodName returns the name of the pod recorded as owning c.
func (c *chunk) ownerPodName() string {
	c.ownerMutex.Lock()
	defer c.ownerMutex.Unlock()
	return c.Owner.PodName
}

func (d *Drsm) GetNewChunk() (*chunk, error) {
	// Get new Chunk
	// We got to allocate new Chunk. We should select
	// probable chunk number

	logger.DrsmLog.Infoln("allocate new chunk")
	// 14 bits --- 1,2,4,8,16
	var cn int32 = 1
	for {
		for {
			cn = rand.Int31n(d.chunkIdRange)
			d.globalChunkTblMutex.Lock()
			_, found := d.globalChunkTbl[cn]
			d.globalChunkTblMutex.Unlock()
			if found {
				continue
			}
			logger.DrsmLog.Debugln("found free chunk Id block", cn)
			break
		}
		// Let's confirm if this gets updated in DB
		docId := fmt.Sprintf("chunkid-%d", cn)
		filter := bson.M{fieldID: docId}
		update := bson.M{fieldID: docId, fieldType: docTypeChunk, "chunkId": docId, fieldPodID: d.clientId.PodName, fieldPodInstance: d.clientId.PodInstance, fieldPodIP: d.clientId.PodIp}
		inserted := d.mongo.RestfulAPIPostOnly(d.sharedPoolName, filter, update)
		if !inserted {
			logger.DrsmLog.Errorf("Adding chunk %v failed. Retry again", cn)
			continue
		}
		break
	}

	logger.DrsmLog.Infof("Adding chunk %v success", cn)
	c := &chunk{Id: cn}
	c.AllocIds = make(map[int32]bool)
	for i := range int32(1000) {
		c.FreeIds = append(c.FreeIds, i)
	}
	c.State = Owned
	c.resourceValidCb = d.resourceValidCb
	d.localChunkTbl[cn] = c

	// add Ids to freeIds
	// why we are not adding in global table right away???
	return c, nil
}

// appendScanIds seeds the ids still to be scanned. FreeIds, ScanIds and AllocIds are shared
// with AllocateInt32ID and ReleaseInt32ID, which hold mutex, so the scan goroutine started by
// claimChunk has to hold it too. The slice is built first so the lock covers only the append.
func (c *chunk) appendScanIds(n int32) {
	ids := make([]int32, 0, n)
	for i := range n {
		ids = append(ids, i)
	}
	mutex.Lock()
	defer mutex.Unlock()
	c.ScanIds = append(c.ScanIds, ids...)
}

// nextScanId takes the next id to scan, reporting false once every id has been scanned.
func (c *chunk) nextScanId() (int32, bool) {
	mutex.Lock()
	defer mutex.Unlock()
	if len(c.ScanIds) == 0 {
		return 0, false
	}
	id := c.ScanIds[len(c.ScanIds)-1]
	c.ScanIds = c.ScanIds[:len(c.ScanIds)-1]
	return id, true
}

// recordScanResult files a scanned id as free or as already in use. The caller invokes
// resourceValidCb outside the lock on purpose: it is supplied by the NF and may re-enter
// drsm, which would deadlock on a non-reentrant mutex.
func (c *chunk) recordScanResult(id int32, free bool) {
	mutex.Lock()
	defer mutex.Unlock()
	if free {
		c.FreeIds = append(c.FreeIds, id)
	} else {
		c.AllocIds[id] = true // Id is in use
	}
}

func (c *chunk) AllocateIntID() (int32, error) {
	if len(c.FreeIds) == 0 {
		err := fmt.Errorf("freeIds in chunk 0")
		logger.DrsmLog.Errorf("%v", err)
		return 0, err
	}
	id := c.FreeIds[len(c.FreeIds)-1]
	c.FreeIds = c.FreeIds[:len(c.FreeIds)-1]
	return (c.Id << 10) | id, nil
}

func (c *chunk) ReleaseIntID(id int32) {
	i := id & 0x3ff
	// not efficient but we are doing cross checks
	for _, freeid := range c.FreeIds {
		if freeid == i {
			logger.DrsmLog.Warnf("id %v is already freed", freeid)
			return
		}
	}
	c.FreeIds = append(c.FreeIds, i)
	if c.State == Scanning {
		for k, v := range c.ScanIds {
			if v == i {
				c.ScanIds[k] = c.ScanIds[len(c.ScanIds)-1] // copy last element at index
				c.ScanIds = c.ScanIds[:len(c.ScanIds)-1]   // now shrink list at tail side
				break
			}
		}
	}
}

// chunkid-123456
func getChunkIdFromDocId(id string) int32 {
	logger.DrsmLog.Debugf("id received: %v value", id)
	z := strings.Split(id, "-")
	if len(z) == 2 && z[0] == "chunkid" {
		cid, err := strconv.ParseInt(z[1], 10, 32)
		if err != nil {
			logger.DrsmLog.Errorf("failed to parse chunk id from doc id %v: %v", id, err)
			return 0
		}
		return int32(cid)
	}
	return 0
}

// check the id format and if its matching chunkid doc format then return true
func isChunkDoc(id string) bool {
	z := strings.Split(id, "-")
	if len(z) == 2 && z[0] == "chunkid" {
		return true
	}
	return false
}
