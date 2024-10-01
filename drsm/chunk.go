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
	"go.mongodb.org/mongo-driver/bson"
)

func (c *chunk) GetOwner() *PodId {
	return &c.Owner

}

func (d *Drsm) GetNewChunk() (*chunk, error) {
	// Get new Chunk
	// We got to allocate new Chunk. We should select
	// probable chunk number

	logger.DrsmLog.Debugln("allocate new chunk")
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
			logger.DrsmLog.Debugln("found chunk Id block", cn)
			break
		}
		// Let's confirm if this gets updated in DB
		docId := fmt.Sprintf("chunkid-%d", cn)
		filter := bson.M{"_id": docId}
		update := bson.M{"_id": docId, "type": "chunk", "chunkId": docId, "podId": d.clientId.PodName, "podInstance": d.clientId.PodInstance, "podIp": d.clientId.PodIp}
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
	var i int32
	for i = 0; i < 1000; i++ {
		c.FreeIds = append(c.FreeIds, i)
	}
	c.State = Owned
	c.resourceValidCb = d.resourceValidCb
	d.localChunkTbl[cn] = c

	// add Ids to freeIds
	return c, nil
}

func (c *chunk) AllocateIntID() int32 {
	if len(c.FreeIds) == 0 {
		logger.DrsmLog.Debugln("freeIds in chunk 0")
		return 0
	}
	id := c.FreeIds[len(c.FreeIds)-1]
	c.FreeIds = c.FreeIds[:len(c.FreeIds)-1]
	return (c.Id << 10) | id
}

func (c *chunk) ReleaseIntID(id int32) {
	i := id & 0x3ff
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

func getChunIdFromDocId(id string) int32 {
	logger.DrsmLog.Infof("id received: %v value", id)
	z := strings.Split(id, "-")
	if len(z) == 2 && z[0] == "chunkid" {
		cid, _ := strconv.ParseInt(z[1], 10, 32)
		c := int32(cid)
		return c
	}
	return 0
}
func isChunkDoc(id string) bool {
	z := strings.Split(id, "-")
	if len(z) == 2 && z[0] == "chunkid" {
		return true
	}
	return false
}
