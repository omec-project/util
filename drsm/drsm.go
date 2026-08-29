// SPDX-FileCopyrightText: 2022 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0
package drsm

import (
	"fmt"
	"sync"
	"time"

	"github.com/omec-project/util/logger"
	MongoDBLibrary "github.com/omec-project/util/mongoapi"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type chunkState int

const (
	Invalid chunkState = iota + 1
	Owned
	PeerOwned
	Orphan
	Scanning
)

// bson field names and document type values shared by the chunk/keepalive documents.
const (
	fieldID          = "_id"
	fieldType        = "type"
	fieldPodID       = "podId"
	fieldPodIP       = "podIp"
	fieldPodInstance = "podInstance"
	docTypeChunk     = "chunk"
	docTypeKeepalive = "keepalive"
)

type chunk struct {
	AllocIds        map[int32]bool
	stopScan        chan bool
	resourceValidCb func(int32) bool
	Owner           PodId
	FreeIds         []int32
	ScanIds         []int32
	State           chunkState
	ownerMutex      sync.Mutex
	Id              int32
}

type podData struct {
	Timestamp     time.Time        `bson:"time,omitempty" json:"time,omitempty"`
	PrevTimestamp time.Time        `bson:"-" json:"-"`
	podChunks     map[int32]*chunk `bson:"-" json:"-"`
	PodId         PodId            `bson:"podId,omitempty" json:"podId,omitempty"`
}

type Drsm struct {
	scanChunks          map[int32]*chunk
	podDown             chan string
	localChunkTbl       map[int32]*chunk
	globalChunkTbl      map[int32]*chunk
	podMap              map[string]*podData
	resourceValidCb     func(int32) bool
	mongo               *MongoDBLibrary.MongoClient
	clientId            PodId
	db                  DbInfo
	sharedPoolName      string
	mode                DrsmMode
	globalChunkTblMutex sync.Mutex
	podMapMutex         sync.Mutex
	resIdSize           int32
	chunkIdRange        int32
}

func (d *Drsm) DeletePod(podInstance string) {
	filter := bson.M{fieldType: docTypeKeepalive, fieldPodInstance: podInstance}
	if err := d.mongo.RestfulAPIDeleteMany(d.sharedPoolName, filter); err != nil {
		logger.DrsmLog.Errorf("failed to delete PodId from DB: %v, err: %v", podInstance, err)
		return
	}
	logger.DrsmLog.Infoln("deleted PodId from DB:", podInstance)
}

func (d *Drsm) ConstuctDrsm(opt *Options) error {
	if opt != nil {
		d.mode = opt.Mode
		logger.DrsmLog.Debugln("drsm mode set to", d.mode)
		if opt.ResIdSize > 0 {
			d.resIdSize = opt.ResIdSize
		} else {
			d.resIdSize = 24
		}
		d.resourceValidCb = opt.ResourceValidCb
	}
	d.chunkIdRange = 1 << (d.resIdSize - 10)
	logger.DrsmLog.Debugf("chunkId in the range of 0 to %v", d.chunkIdRange)
	d.localChunkTbl = make(map[int32]*chunk)
	d.globalChunkTbl = make(map[int32]*chunk)
	d.podMap = make(map[string]*podData)
	d.podDown = make(chan string, 10)
	d.scanChunks = make(map[int32]*chunk)
	d.globalChunkTblMutex = sync.Mutex{}

	// connect to DB — retry until MongoDB is reachable so that the goroutines
	// spawned below are never handed a nil client.
	const (
		retryInterval = 2 * time.Second
		maxWait       = 120 * time.Second
	)
	deadline := time.Now().Add(maxWait)
	for {
		var err error
		d.mongo, err = MongoDBLibrary.NewMongoClient(d.db.Url, d.db.Name)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			logger.DrsmLog.Errorf("drsm: mongodb not reachable after %v; goroutines will not be started", maxWait)
			return fmt.Errorf("drsm: mongodb not reachable after %v", maxWait)
		}
		logger.DrsmLog.Warnf("drsm: waiting for mongodb, retrying in %v", retryInterval)
		time.Sleep(retryInterval)
	}
	logger.DrsmLog.Debugln("mongoClient is created", d.db.Name)

	go d.handleDbUpdates()
	go d.punchLiveness()
	go d.podDownDetected()
	go d.checkAllChunks()
	return nil
}
