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
	fmt.Println("Started Pod Down goroutine")
	for {
		select {
		case p := <-d.podDown:
			logger.AppLog.Infoln("Pod Down detected ", p)
			// Given Pod find out current Chunks owned by this POD
			pd := d.podMap[p]
			for k, _ := range pd.podChunks {
				d.globalChunkTblMutex.Lock()
				c, found := d.globalChunkTbl[k]
				d.globalChunkTblMutex.Unlock()
				logger.AppLog.Debugf("Found : %v chunk : %v ", found, c)
				go c.claimChunk(d)
			}
		}
	}
}

func (c *chunk) claimChunk(d *Drsm) {
	if d.mode != ResourceClient {
		logger.AppLog.Infof("claimChunk ignored demux mode ")
		return
	}
	// try to claim. If success then notification will update owner.
	logger.AppLog.Debugf("claimChunk started")
	docId := fmt.Sprintf("chunkid-%d", c.Id)
	update := bson.M{"_id": docId, "type": "chunk", "podId": d.clientId.PodName, "podInstance": d.clientId.PodInstance, "podIp": d.clientId.PodIp}
	filter := bson.M{"_id": docId, "podId": c.Owner.PodName}
	updated := d.mongo.RestfulAPIPutOnly(d.sharedPoolName, filter, update)
	if updated == nil {
		// TODO : don't add to local pool yet. We can add it only if scan is done.
		logger.AppLog.Debugf("claimChunk success")
		c.Owner.PodName = d.clientId.PodName
		c.Owner.PodIp = d.clientId.PodIp
		go c.scanChunk(d)
	} else {
		logger.AppLog.Debugf("claimChunk failure ")
	}
}
