// SPDX-FileCopyrightText: 2022-present Intel Corporation
// SPDX-FileCopyrightText: 2021 Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omec-project/util/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func iterateChangeStream(routineCtx context.Context, stream *mongo.ChangeStream) {
	logger.AppLog.Infoln("iterate change stream for timeout")
	defer stream.Close(routineCtx)
	for stream.Next(routineCtx) {
		var data bson.M
		if err := stream.Decode(&data); err != nil {
			panic(err)
		}
		logger.AppLog.Infoln("iterate stream:", data)
	}
}

func TimeoutTest(c *gin.Context) {
	c.String(http.StatusOK, "timeoutTest!")
	logger.AppLog.Infoln("starting timeout document")

	database := mongoHndl.Client.Database("sdcore")
	timeoutColl := database.Collection("timeout")

	// TODO : library should provide this API
	// create stream to monitor actions on the collection
	timeoutStream, err := timeoutColl.Watch(context.TODO(), mongo.Pipeline{})
	if err != nil {
		panic(err)
	}
	routineCtx, _ := context.WithCancel(context.Background())
	// run routine to get messages from stream
	go iterateChangeStream(routineCtx, timeoutStream)
	// createDocumentWithTimeout("timeout", "yak1", 60, "createdAt")
	// createDocumentWithTimeout("timeout", "yak2", 60, "createdAt")
	ret := mongoHndl.RestfulAPICreateTTLIndex("timeout", 20, "updatedAt")
	if ret {
		logger.AppLog.Infoln("ttl index create successful")
	} else {
		logger.AppLog.Infoln("ttl index exists already")
	}

	createDocumentWithCommonTimeout("timeout", "yak1")
	updateDocumentWithCommonTimeout("timeout", "yak1")
	go func() {
		for {
			createDocumentWithCommonTimeout("timeout", "yak2")
			time.Sleep(5 * time.Second)
		}
	}()

	ret = mongoHndl.RestfulAPIDropTTLIndex("timeout", "updatedAt")
	if !ret {
		logger.AppLog.Warnln("ttl index drop failed")
	}
	ret = mongoHndl.RestfulAPIPatchTTLIndex("timeout", 0, "expireAt")
	if ret {
		logger.AppLog.Infoln("ttl index patch successful")
	} else {
		logger.AppLog.Warnln("ttl index patch failed")
	}

	createDocumentWithExpiryTime("timeout", "yak1", 30)
	createDocumentWithExpiryTime("timeout", "yak3", 30)
	updateDocumentWithExpiryTime("timeout", "yak3", 40)
	updateDocumentWithExpiryTime("timeout", "yak1", 50)
	// logger.AppLog.Infoln("sleeping for 120 seconds")
	// time.Sleep(120 * time.Second)
	// updateDocumentWithTimeout("timeout", "yak1", 200, "createdAt")
	c.JSON(http.StatusOK, gin.H{})
}

func createDocumentWithCommonTimeout(collName string, name string) {
	putData := bson.M{}
	putData["name"] = name
	putData["createdAt"] = time.Now()
	// timein := time.Now().Local().Add(time.Second * time.Duration(20))
	// logger.AppLog.Infoln("updated timeout:", timein)
	// putData["updatedAt"] = timein
	putData["updatedAt"] = time.Now()
	filter := bson.M{"name": name}
	mongoHndl.RestfulAPIPutOne(collName, filter, putData)
}

func updateDocumentWithCommonTimeout(collName string, name string) {
	putData := bson.M{}
	putData["name"] = name
	// putData["createdAt"] = time.Now()
	putData["updatedAt"] = time.Now()
	filter := bson.M{"name": name}
	mongoHndl.RestfulAPIPutOne("timeout", filter, putData)
}

func updateDocumentWithExpiryTime(collName string, name string, timeVal int) {
	putData := bson.M{}
	putData["name"] = name
	// putData["createdAt"] = time.Now()
	timein := time.Now().Local().Add(time.Second * time.Duration(timeVal))
	putData["expireAt"] = timein
	filter := bson.M{"name": name}
	mongoHndl.RestfulAPIPutOne(collName, filter, putData)
}

func createDocumentWithExpiryTime(collName string, name string, timeVal int) {
	putData := bson.M{}
	putData["name"] = name
	putData["createdAt"] = time.Now()
	timein := time.Now().Local().Add(time.Second * time.Duration(timeVal))
	// logger.AppLog.Infoln("updated timeout:", timein)
	putData["expireAt"] = timein
	// putData["updatedAt"] = time.Now()
	filter := bson.M{"name": name}
	mongoHndl.RestfulAPIPutOne(collName, filter, putData)
}
