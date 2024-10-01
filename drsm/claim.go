// SPDX-FileCopyrightText: 2022 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0
package drsm

import (
	"fmt"

	"github.com/omec-project/util/logger"
	"go.mongodb.org/mongo-driver/bson"
)

func (d *Drsm) podDownDetected() {
	fmt.Println("started Pod Down goroutine")
	for p := range d.podDown {
		logger.DrsmLog.Infoln("pod Down detected", p)
		// Given Pod find out current Chunks owned by this POD
		pd := d.podMap[p]
		for k := range pd.podChunks {
			d.globalChunkTblMutex.Lock()
			c, found := d.globalChunkTbl[k]
			d.globalChunkTblMutex.Unlock()
			logger.DrsmLog.Debugf("found: %v chunk: %v", found, c)
			if found {
				go c.claimChunk(d)
			}
		}
	}
}

func (c *chunk) claimChunk(d *Drsm) {
	if d.mode != ResourceClient {
		logger.DrsmLog.Infoln("claimChunk ignored demux mode")
		return
	}
	// try to claim. If success then notification will update owner.
	logger.DrsmLog.Debugln("claimChunk started")
	docId := fmt.Sprintf("chunkid-%d", c.Id)
	update := bson.M{"_id": docId, "type": "chunk", "podId": d.clientId.PodName, "podInstance": d.clientId.PodInstance, "podIp": d.clientId.PodIp}
	filter := bson.M{"_id": docId, "podId": c.Owner.PodName}
	updated := d.mongo.RestfulAPIPutOnly(d.sharedPoolName, filter, update)
	if updated == nil {
		// TODO : don't add to local pool yet. We can add it only if scan is done.
		logger.DrsmLog.Debugln("claimChunk success")
		c.Owner.PodName = d.clientId.PodName
		c.Owner.PodIp = d.clientId.PodIp
		go c.scanChunk(d)
	} else {
		logger.DrsmLog.Debugln("claimChunk failure")
	}
}
