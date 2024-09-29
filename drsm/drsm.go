// SPDX-FileCopyrightText: 2022 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0
package drsm

import (
	"log"
	"sync"
	"time"

	"github.com/omec-project/util/logger"
	MongoDBLibrary "github.com/omec-project/util/mongoapi"
	ipam "github.com/thakurajayL/go-ipam"
	"go.mongodb.org/mongo-driver/bson"
)

type chunkState int

const (
	Invalid chunkState = iota + 1
	Owned
	PeerOwned
	Orphan
	Scanning
)

type chunk struct {
	Id              int32
	Owner           PodId
	State           chunkState
	FreeIds         []int32
	AllocIds        map[int32]bool
	ScanIds         []int32
	stopScan        chan bool
	resourceValidCb func(int32) bool
}

type podData struct {
	PodId         PodId            `bson:"podId,omitempty" json:"podId,omitempty"`
	Timestamp     time.Time        `bson:"time,omitempty" json:"time,omitempty"`
	PrevTimestamp time.Time        `bson:"-" json:"-"`
	podChunks     map[int32]*chunk `bson:"-" json:"-"` // chunkId to Chunk
}

type Drsm struct {
	sharedPoolName      string
	clientId            PodId
	db                  DbInfo
	mode                DrsmMode
	resIdSize           int32
	localChunkTbl       map[int32]*chunk    // chunkid to chunk
	globalChunkTbl      map[int32]*chunk    // chunkid to chunk
	podMap              map[string]*podData // podId to podData
	podDown             chan string
	scanChunks          map[int32]*chunk
	chunkIdRange        int32
	resourceValidCb     func(int32) bool
	ipModule            ipam.Ipamer
	prefix              map[string]*ipam.Prefix
	mongo               *MongoDBLibrary.MongoClient
	globalChunkTblMutex sync.RWMutex
	punchLivenessTime   int
}

func (d *Drsm) DeletePod(podInstance string) {
	filter := bson.M{"type": "keepalive", "podInstance": podInstance}
	d.mongo.RestfulAPIDeleteMany(d.sharedPoolName, filter)
	logger.AppLog.Infoln("Deleted PodId from DB: ", podInstance)
}

func (d *Drsm) ConstuctDrsm(opt *Options) {
	if opt != nil {
		d.mode = opt.Mode
		logger.AppLog.Debugln("drsm mode set to ", d.mode)
		if opt.ResIdSize > 0 {
			d.resIdSize = opt.ResIdSize
		} else {
			d.resIdSize = 24
		}
		d.resourceValidCb = opt.ResourceValidCb
	}
	d.chunkIdRange = 1 << (d.resIdSize - 10)
	log.Printf("ChunkId in the range of 0 to %v ", d.chunkIdRange)
	d.localChunkTbl = make(map[int32]*chunk)
	d.globalChunkTbl = make(map[int32]*chunk)
	d.podMap = make(map[string]*podData)
	d.podDown = make(chan string, 10)
	d.scanChunks = make(map[int32]*chunk)
	d.globalChunkTblMutex = sync.RWMutex{}
	d.initIpam(opt)

	//connect to DB
	d.mongo, _ = MongoDBLibrary.NewMongoClient(d.db.Url, d.db.Name)
	logger.AppLog.Debugln("MongoClient is created.", d.db.Name)
	_, err := d.mongo.CreateIndex(d.sharedPoolName, "_id")

	if err != nil {
		logger.AppLog.Infof("Failed to create id index: %v", err)
	}
	go d.handleDbUpdates()
	go d.punchLiveness()
	go d.podDownDetected()
	go d.checkAllChunks()
}
