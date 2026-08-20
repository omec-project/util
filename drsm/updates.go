// SPDX-FileCopyrightText: 2022 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0
package drsm

import (
	"context"
	"time"

	"github.com/omec-project/util/logger"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type UpdatedFields struct {
	ExpireAt    time.Time `bson:"expireAt,omitempty"`
	PodId       string    `bson:"podId,omitempty"`
	PodIp       string    `bson:"podIp,omitempty"`
	PodInstance string    `bson:"podInstance,omitempty"`
}

type UpdatedDesc struct {
	UpdFields UpdatedFields `bson:"updatedFields,omitempty"`
}

type FullStream struct {
	Id          string    `bson:"_id"`
	ChunkId     string    `bson:"chunkId"`
	PodId       string    `bson:"podId,omitempty"`
	PodIp       string    `bson:"podIp,omitempty"`
	PodInstance string    `bson:"podInstance,omitempty"`
	ExpireAt    time.Time `bson:"expireAt,omitempty"`
	Type        string    `bson:"type,omitempty"`
}

type DocKey struct {
	Id string `bson:"_id,omitempty"`
}

type streamDoc struct {
	DId    DocKey      `bson:"documentKey,omitempty"`
	OpType string      `bson:"operationType,omitempty"`
	Full   FullStream  `bson:"fullDocument,omitempty"`
	Update UpdatedDesc `bson:"updateDescription,omitempty"`
}

/*
 map[
        _id:map[_data:826306F004000000032B022C0100296E5A1004EC0A378B4B3044C28DF4F18548BC3974463C5F6964003C6462746573746170702D6262346334636462342D6A687A6C7A000004]
        clusterTime:{1661399044 3}
        documentKey:map[_id:dbtestapp-bb4c4cdb4-jhzlz]
        ns:map[coll:ngapid db:sdcore]
        operationType:insert
        fullDocument:map[_id:dbtestapp-bb4c4cdb4-jhzlz expireAt:1661399064504 podId:dbtestapp-bb4c4cdb4-jhzlz time:1661399044 type:keepalive]
    ]

map[
        _id:map[_data:826306FE49000000012B022C0100296E5A10045287202787774B43958F3929CFD344D0463C5F6964003C6462746573746170702D3862396634383866372D6337347366000004]
        clusterTime:{1661402697 1}
        documentKey:map[_id:dbtestapp-8b9f488f7-c74sf]
        ns:map[coll:ngapid db:sdcore]
        operationType:update
        updateDescription:map[removedFields:[] updatedFields:map[expireAt:1661402717758 time:1661402697]]
   ]

map[
        _id:map[_data:82630701E5000000012B022C0100296E5A10045287202787774B43958F3929CFD344D0463C5F6964003C6462746573746170702D3862396634383866372D6E64327470000004]
        clusterTime:{1661403621 1}
        documentKey:map[_id:dbtestapp-8b9f488f7-nd2tp]
        ns:map[coll:ngapid db:sdcore]
        operationType:delete
   ]

map[
        _id:map[_data:826307FF400000000B2B022C0100296E5A1004020E4568089B4D8889A42D53E225B5AE463C5F6964003C6368756E6B69642D3131353638000004]
        clusterTime:{1661468480 11}
        documentKey:map[_id:chunkid-11568]
        fullDocument:map[_id:chunkid-11568 podId:dbtestapp-8644b5b7d6-qdk54 type:chunk]
        ns:map[coll:ngapid db:sdcore]
        operationType:insert]


map[
        _id:map[_data:8263085773000000022B022C0100296E5A1004E23062383C624633BDEE5B9B5FEAB2B8463C5F6964003C6368756E6B69642D38333332000004]
        clusterTime:{1661491059 2}
        documentKey:map[_id:chunkid-8332]
        ns:map[coll:ngapid db:sdcore]
        operationType:update
        updateDescription:map[removedFields:[] updatedFields:map[podId:dbtestapp-6dc68f9f68-7fwj8]]]

*/

// handle incoming db notification and update
func (d *Drsm) handleDbUpdates() {
	collection := d.mongo.GetCollection(d.sharedPoolName)

	// TODO : 2 go routines to monitor 2 pipelines
	pipeline := mongo.Pipeline{}

	for {
		// create stream to monitor actions on the collection
		updateStream, err := collection.Watch(context.TODO(), pipeline)
		if err != nil {
			time.Sleep(5000 * time.Millisecond)
			continue
		}
		routineCtx, cancel := context.WithCancel(context.Background())
		// run routine to get messages from stream
		iterateChangeStream(d, routineCtx, updateStream)
		cancel()
	}
}

// ensurePodChunksInitialized allocates podD.podChunks on first use. Callers must hold
// podMapMutex.
func (d *Drsm) ensurePodChunksInitialized(podD *podData) {
	if podD.podChunks == nil {
		podD.podChunks = make(map[int32]*chunk)
	}
}

func iterateChangeStream(d *Drsm, routineCtx context.Context, stream *mongo.ChangeStream) {
	logger.DrsmLog.Debugf("iterate change stream for podData: %v", d)

	// step 1: Get Pod Keepalive triggers and create POD table
	// case 2: Update Global Chunk Table.
	// case 3: New POD addition
	// case 4: New chunk addition
	// case 5: POD down - keepalive doc deleted. Then inform Claim go routine.
	// case 6: Chunk owner change - claim

	defer stream.Close(routineCtx)
	for stream.Next(routineCtx) {
		var data bson.M
		if err := stream.Decode(&data); err != nil {
			logger.DrsmLog.Errorf("failed to decode stream data: %v", err)
			continue
		}
		var s streamDoc
		bsonBytes, _ := bson.Marshal(data)
		if err := bson.Unmarshal(bsonBytes, &s); err != nil {
			logger.DrsmLog.Errorf("failed to unmarshal stream data: %v", err)
			continue
		}
		// logger.DrsmLog.Debugf("iterate stream : ", data)
		// logger.DrsmLog.Debugf("\ndecoded stream bson %+v \n", s)
		switch s.OpType {
		case "insert":
			full := &s.Full
			switch full.Type {
			case "keepalive":
				// logger.DrsmLog.Debugf("insert keepalive document")
				d.ensurePod(full)
			case "chunk":
				// logger.DrsmLog.Debugln("insert chunk document")
				d.addChunk(full)
			}
		case "update":
			// chunk ownership changed..update chunk owner
			// logger.DrsmLog.Debugln("update operations")
			if isChunkDoc(s.DId.Id) {
				// update on chunkId..
				// looks like chunk owner getting change
				owner := s.Update.UpdFields.PodId
				if owner == "" {
					logger.DrsmLog.Warnf("stream(Update): missing owner in update for doc %s, operation: %+v", s.DId.Id, s.Update)
					continue
				}
				c := getChunkIdFromDocId(s.DId.Id)
				d.globalChunkTblMutex.Lock()
				cp, found := d.globalChunkTbl[c]
				d.globalChunkTblMutex.Unlock()
				if !found {
					logger.DrsmLog.Warnf("stream(Update): chunk %d not found in global table for owner %s - will be corrected by periodic resync", c, owner)
					// Without a chunk reference there is nothing to update; skip to avoid panic.
					// The periodic checkAllChunks() will resync state from MongoDB.
					continue
				}
				// TODO update IP address as well.
				cp.Owner.PodName = owner
				cp.Owner.PodIp = s.Update.UpdFields.PodIp
				cp.Owner.PodInstance = s.Update.UpdFields.PodInstance
				if !d.recordChunkOwner(owner, c, cp) {
					logger.DrsmLog.Warnf("stream(Update): pod %s not in local map for chunk %d update - will be corrected when keepalive arrives or during periodic resync", owner, c)
					// Wait for proper pod initialization via keepalive. Eventual consistency will be maintained by periodic resync and proper keepalive events.
					continue
				}
			}
		case "delete":
			logger.DrsmLog.Debugln("delete operations")
			if !isChunkDoc(s.DId.Id) {
				// not chunk type doc. So its POD doc.
				// delete only gets document id
				if d.podDownCandidate(s.DId.Id) {
					d.podDown <- s.DId.Id
				}
			}
		}
	}
}

// periodic task
func (d *Drsm) punchLiveness() {
	// write to DB - signature every 5 second
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	logger.DrsmLog.Debugln("document expiry enabled")
	ret := d.mongo.RestfulAPICreateTTLIndex(d.sharedPoolName, 0, "expireAt")
	if ret {
		logger.DrsmLog.Debugln("ttl index created for Field: expireAt in Collection")
	} else {
		logger.DrsmLog.Debugln("ttl index exists for Field: expireAt in Collection")
	}

	for range ticker.C {
		// logger.DrsmLog.Debugln("update keepalive time")
		filter := bson.M{"_id": d.clientId.PodName}

		timein := time.Now().Local().Add(20 * time.Second)

		update := bson.D{
			{Key: "_id", Value: d.clientId.PodName},
			{Key: "type", Value: "keepalive"},
			{Key: "podIp", Value: d.clientId.PodIp},
			{Key: "podId", Value: d.clientId.PodName},
			{Key: "podInstance", Value: d.clientId.PodInstance},
			{Key: "expireAt", Value: timein},
		}

		_, err := d.mongo.PutOneCustomDataStructure(d.sharedPoolName, filter, update)
		if err != nil {
			logger.DrsmLog.Errorf("put data failed: %v", err)
			// TODO : should we panic ?
			continue
		}
	}
}

// periodic task
func (d *Drsm) checkAllChunks() {
	// go through all pods to see if any pod is showing same old counter
	// Mark it down locally
	// Claiming the chunks can be reactive
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		filter := bson.M{"type": "chunk"}
		result, err := d.mongo.RestfulAPIGetMany(d.sharedPoolName, filter)
		logger.DrsmLog.Debugf("chunk entry: %v", result)
		if err == nil && result != nil {
			for _, v := range result {
				var s FullStream
				bsonBytes, _ := bson.Marshal(v)
				bson.Unmarshal(bsonBytes, &s)
				logger.DrsmLog.Debugf("individual Chunk bson Element %v", s)
				d.addChunk(&s)
			}
		}
	}
}

func (d *Drsm) addChunk(full *FullStream) {
	did := full.Id
	if did == "" {
		did = full.ChunkId
	}
	logger.DrsmLog.Debugf("received Chunk Doc: %v", full)
	cid := getChunkIdFromDocId(did)
	o := PodId{PodName: full.PodId, PodInstance: full.PodInstance, PodIp: full.PodIp}
	c := &chunk{Id: cid, Owner: o}
	c.resourceValidCb = d.resourceValidCb

	d.podMapMutex.Lock()
	pod, found := d.podMap[full.PodId]
	if !found {
		pod = d.addPodLocked(full)
	}
	pod.podChunks[cid] = c
	logger.DrsmLog.Debugf("chunk id %v, podChunks %v", cid, pod.podChunks)
	// Released before globalChunkTblMutex is taken: no path holds both locks at once.
	d.podMapMutex.Unlock()

	d.globalChunkTblMutex.Lock()
	d.globalChunkTbl[cid] = c
	d.globalChunkTblMutex.Unlock()
}

// ensurePod adds the pod described by full to podMap unless it is already known.
func (d *Drsm) ensurePod(full *FullStream) {
	d.podMapMutex.Lock()
	defer d.podMapMutex.Unlock()
	if pod, found := d.podMap[full.PodId]; found {
		logger.DrsmLog.Debugln("keepalive insert document: found existing podId", pod)
		return
	}
	d.addPodLocked(full)
}

// recordChunkOwner records cp against the pod that now owns it. It reports whether that pod
// is known locally.
func (d *Drsm) recordChunkOwner(owner string, chunkId int32, cp *chunk) bool {
	d.podMapMutex.Lock()
	defer d.podMapMutex.Unlock()
	podD, found := d.podMap[owner]
	if !found {
		return false
	}
	// Defensive: should never happen if the pod went through addPodLocked, but prevents panic
	d.ensurePodChunksInitialized(podD)
	podD.podChunks[chunkId] = cp // add chunk to pod
	logger.DrsmLog.Infof("stream(Update): pod to chunk map %v", podD.podChunks)
	return true
}

// podDownCandidate reports whether podName is still known locally, logging the chunks it
// owned. The caller signals podDown outside the lock: podDownDetected takes podMapMutex,
// so signalling while holding it would deadlock once the channel buffer is full.
func (d *Drsm) podDownCandidate(podName string) bool {
	d.podMapMutex.Lock()
	defer d.podMapMutex.Unlock()
	pod, found := d.podMap[podName]
	if found {
		logger.DrsmLog.Infof("Stream(Delete): Pod %v and found %v. Chunks owned by crashed pod = %v", pod, found, pod.podChunks)
	}
	return found
}

// podChunkIds returns the ids of the chunks currently recorded against podName, together
// with the owner name recorded for that pod, which claimChunk needs to build its filter.
func (d *Drsm) podChunkIds(podName string) ([]int32, string) {
	d.podMapMutex.Lock()
	defer d.podMapMutex.Unlock()
	pd, found := d.podMap[podName]
	if !found {
		return nil, ""
	}
	ids := make([]int32, 0, len(pd.podChunks))
	for k := range pd.podChunks {
		ids = append(ids, k)
	}
	return ids, pd.PodId.PodName
}

// addPodLocked adds the pod described by full to podMap. Callers must hold podMapMutex.
func (d *Drsm) addPodLocked(full *FullStream) *podData {
	podI := PodId{PodName: full.PodId, PodInstance: full.PodInstance, PodIp: full.PodIp}
	pod := &podData{PodId: podI}
	d.ensurePodChunksInitialized(pod)
	d.podMap[full.PodId] = pod
	logger.DrsmLog.Infof("keepalive insert d.podMaps %v", d.podMap)
	return pod
}
