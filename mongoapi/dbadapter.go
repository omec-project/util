// SPDX-FileCopyrightText: 2024 Open Networking Foundation <info@opennetworking.org>
// Copyright 2019 free5GC.org
//
// SPDX-License-Identifier: Apache-2.0
package mongoapi

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type DBInterface interface {
	RestfulAPIGetOne(collName string, filter bson.M) (map[string]any, error)
	RestfulAPIGetMany(collName string, filter bson.M) ([]map[string]any, error)
	RestfulAPIPutOneTimeout(collName string, filter bson.M, putData map[string]any, timeout int32, timeField string) bool
	RestfulAPIPutOne(collName string, filter bson.M, putData map[string]any) (bool, error)
	RestfulAPIPutOneWithContext(context context.Context, collName string, filter bson.M, putData map[string]any) (bool, error)
	RestfulAPIPutOneNotUpdate(collName string, filter bson.M, putData map[string]any) (bool, error)
	RestfulAPIPutMany(collName string, filterArray []primitive.M, putDataArray []map[string]any) error
	RestfulAPIDeleteOne(collName string, filter bson.M) error
	RestfulAPIDeleteOneWithContext(context context.Context, collName string, filter bson.M) error
	RestfulAPIDeleteMany(collName string, filter bson.M) error
	RestfulAPIMergePatch(collName string, filter bson.M, patchData map[string]any) error
	RestfulAPIJSONPatch(collName string, filter bson.M, patchJSON []byte) error
	RestfulAPIJSONPatchWithContext(context context.Context, collName string, filter bson.M, patchJSON []byte) error
	RestfulAPIJSONPatchExtend(collName string, filter bson.M, patchJSON []byte, dataName string) error
	RestfulAPIPost(collName string, filter bson.M, postData map[string]any) (bool, error)
	RestfulAPIPostWithContext(context context.Context, collName string, filter bson.M, postData map[string]any) (bool, error)
	RestfulAPIPostMany(collName string, filter bson.M, postDataArray []any) error
	RestfulAPIPostManyWithContext(context context.Context, collName string, filter bson.M, postDataArray []any) error
	GetUniqueIdentity(idName string) int32
	CreateIndex(collName string, keyField string) (bool, error)
	StartSession() (mongo.Session, error)
	SupportsTransactions() (bool, error)
}

var CommonDBClient DBInterface

type MongoDBClient struct {
	MongoClient
}

// Set CommonDBClient
func setCommonDBClient(url string, dbname string) error {
	mClient, errConnect := NewMongoClient(url, dbname)
	if mClient.Client != nil {
		CommonDBClient = mClient
		CommonDBClient.(*MongoClient).Client.Database(dbname)
	}
	return errConnect
}

func ConnectMongo(url string, dbname string) {
	// Connect to MongoDB
	ticker := time.NewTicker(2 * time.Second)
	defer func() { ticker.Stop() }()
	timer := time.After(180 * time.Second)
ConnectMongo:
	for {
		commonDbErr := setCommonDBClient(url, dbname)
		if commonDbErr == nil {
			break ConnectMongo
		}
		select {
		case <-ticker.C:
			continue
		case <-timer:
			return
		}
	}
}
