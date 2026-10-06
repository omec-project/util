// Copyright (C) 2026 Intel Corporation
// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/omec-project/util/logger"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	// indexNotFoundErrorCode is returned when an index that is being dropped
	// does not exist.
	indexNotFoundErrorCode = 27
	// indexOptionsConflictErrorCode is returned when an index of the requested
	// name already exists with different options.
	indexOptionsConflictErrorCode = 85
	// indexKeySpecsConflictErrorCode is returned when an index of the requested
	// name already exists and differs from the requested one.
	indexKeySpecsConflictErrorCode = 86
)

type MongoClient struct {
	Client *mongo.Client
	dbName string
	url    string
}

// bson field name and update operator reused across the API implementations below.
const (
	fieldID = "_id"
	opSet   = "$set"
)

func NewMongoClient(url string, dbName string) (*MongoClient, error) {
	c := MongoClient{url: url, dbName: dbName}
	opts := options.Client().
		ApplyURI(c.url).
		SetBSONOptions(&options.BSONOptions{
			DefaultDocumentMap: true,
		})
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("MongoClient Creation err: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = client.Ping(ctx, nil); err != nil {
		if derr := client.Disconnect(context.Background()); derr != nil {
			return nil, fmt.Errorf("MongoClient Ping err: %w (disconnect err: %v)", err, derr)
		}
		return nil, fmt.Errorf("MongoClient Ping err: %w", err)
	}
	c.Client = client
	return &c, nil
}

func findOneAndDecode(collection *mongo.Collection, filter bson.M) (map[string]any, error) {
	var result map[string]any
	if err := collection.FindOne(context.TODO(), filter).Decode(&result); err != nil {
		// ErrNoDocuments means that the filter did not match any documents in
		// the collection.
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return result, nil
}

func getOrigData(collection *mongo.Collection, filter bson.M) (map[string]any, error) {
	result, err := findOneAndDecode(collection, filter)
	if err != nil {
		return nil, err
	}
	if result != nil {
		// Delete "_id" entry which is auto-inserted by MongoDB
		delete(result, fieldID)
	}
	return result, nil
}

func checkDataExisted(collection *mongo.Collection, filter bson.M) (bool, error) {
	result, err := findOneAndDecode(collection, filter)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	return true, nil
}

func (c *MongoClient) GetCollection(collName string) *mongo.Collection {
	collection := c.Client.Database(c.dbName).Collection(collName)
	return collection
}

func (c *MongoClient) RestfulAPIGetOne(collName string, filter bson.M) (map[string]any, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)
	result, err := getOrigData(collection, filter)
	if err != nil {
		return nil, fmt.Errorf("RestfulAPIGetOne err: %w", err)
	}
	return result, nil
}

func (c *MongoClient) RestfulAPIGetMany(collName string, filter bson.M) ([]map[string]any, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cur, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("RestfulAPIGetMany err: %w", err)
	}
	defer func(ctx context.Context) {
		if err := cur.Close(ctx); err != nil {
			return
		}
	}(ctx)

	var resultArray []map[string]any
	for cur.Next(ctx) {
		var result map[string]any
		if err := cur.Decode(&result); err != nil {
			return nil, fmt.Errorf("RestfulAPIGetMany err: %w", err)
		}

		// Delete "_id" entry which is auto-inserted by MongoDB
		delete(result, fieldID)
		resultArray = append(resultArray, result)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("RestfulAPIGetMany err: %w", err)
	}

	return resultArray, nil
}

// if no error happened, return true means data existed and false means data not existed
func (c *MongoClient) RestfulAPIPutOne(collName string, filter bson.M, putData map[string]any) (bool, error) {
	return c.RestfulAPIPutOneWithContext(context.TODO(), collName, filter, putData)
}

// if no error happened, return true means data existed and false means data not existed
func (c *MongoClient) RestfulAPIPutOneWithContext(ctx context.Context, collName string, filter bson.M, putData map[string]any) (bool, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)
	opts := options.UpdateOne().SetUpsert(true)
	result, err := collection.UpdateOne(ctx, filter, bson.M{opSet: putData}, opts)
	if err != nil {
		return false, fmt.Errorf("RestfulAPIPutOneWithContext UpdateOne err: %w", err)
	}
	return result.MatchedCount > 0, nil
}

func (c *MongoClient) RestfulAPIPullOne(collName string, filter bson.M, putData map[string]any) error {
	return c.RestfulAPIPullOneWithContext(context.TODO(), collName, filter, putData)
}

func (c *MongoClient) RestfulAPIPullOneWithContext(ctx context.Context, collName string, filter bson.M, putData map[string]any) error {
	collection := c.Client.Database(c.dbName).Collection(collName)
	if _, err := collection.UpdateOne(ctx, filter, bson.M{"$pull": putData}); err != nil {
		return fmt.Errorf("RestfulAPIPullOneWithContext UpdateOne err: %w", err)
	}
	return nil
}

// if no error happened, return true means data existed (not updated) and false means data not existed
func (c *MongoClient) RestfulAPIPutOneNotUpdate(collName string, filter bson.M, putData map[string]any) (bool, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)
	existed, err := checkDataExisted(collection, filter)
	if err != nil {
		return false, fmt.Errorf("RestfulAPIPutOneNotUpdate err: %w", err)
	}

	if existed {
		return true, nil
	}

	if _, err := collection.InsertOne(context.TODO(), putData); err != nil {
		return false, fmt.Errorf("RestfulAPIPutOneNotUpdate InsertOne err: %w", err)
	}
	return false, nil
}

func (c *MongoClient) RestfulAPIPutMany(collName string, filterArray []bson.M, putDataArray []map[string]any) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	for i, putData := range putDataArray {
		filter := filterArray[i]
		existed, err := checkDataExisted(collection, filter)
		if err != nil {
			return fmt.Errorf("RestfulAPIPutMany err: %w", err)
		}

		if existed {
			if _, err := collection.UpdateOne(context.TODO(), filter, bson.M{opSet: putData}); err != nil {
				return fmt.Errorf("RestfulAPIPutMany UpdateOne err: %w", err)
			}
		} else {
			if _, err := collection.InsertOne(context.TODO(), putData); err != nil {
				return fmt.Errorf("RestfulAPIPutMany InsertOne err: %w", err)
			}
		}
	}
	return nil
}

func (c *MongoClient) RestfulAPIDeleteOne(collName string, filter bson.M) error {
	return c.RestfulAPIDeleteOneWithContext(context.TODO(), collName, filter)
}

func (c *MongoClient) RestfulAPIDeleteOneWithContext(ctx context.Context, collName string, filter bson.M) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	if _, err := collection.DeleteOne(ctx, filter); err != nil {
		return fmt.Errorf("RestfulAPIDeleteOneWithContext DeleteOne err: %w", err)
	}
	return nil
}

func (c *MongoClient) RestfulAPIDeleteMany(collName string, filter bson.M) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	if _, err := collection.DeleteMany(context.TODO(), filter); err != nil {
		return fmt.Errorf("RestfulAPIDeleteMany err: %w", err)
	}
	return nil
}

func (c *MongoClient) RestfulAPIMergePatch(collName string, filter bson.M, patchData map[string]any) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	originalData, err := getOrigData(collection, filter)
	if err != nil {
		return fmt.Errorf("RestfulAPIMergePatch getOrigData err: %w", err)
	}

	original, err := json.Marshal(originalData)
	if err != nil {
		return fmt.Errorf("RestfulAPIMergePatch Marshal err: %w", err)
	}

	patchDataByte, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("RestfulAPIMergePatch Marshal err: %w", err)
	}

	modifiedAlternative, err := jsonpatch.MergePatch(original, patchDataByte)
	if err != nil {
		return fmt.Errorf("RestfulAPIMergePatch MergePatch err: %w", err)
	}

	var modifiedData map[string]any
	if err := json.Unmarshal(modifiedAlternative, &modifiedData); err != nil {
		return fmt.Errorf("RestfulAPIMergePatch Unmarshal err: %w", err)
	}
	if _, err := collection.UpdateOne(context.TODO(), filter, bson.M{opSet: modifiedData}); err != nil {
		return fmt.Errorf("RestfulAPIMergePatch UpdateOne err: %w", err)
	}
	return nil
}

func (c *MongoClient) RestfulAPIJSONPatch(collName string, filter bson.M, patchJSON []byte) error {
	return c.RestfulAPIJSONPatchWithContext(context.TODO(), collName, filter, patchJSON)
}

func (c *MongoClient) RestfulAPIJSONPatchWithContext(ctx context.Context, collName string, filter bson.M, patchJSON []byte) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	originalData, err := getOrigData(collection, filter)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatch getOrigData err: %w", err)
	}

	original, err := json.Marshal(originalData)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatch Marshal err: %w", err)
	}

	patch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatch DecodePatch err: %w", err)
	}

	modified, err := patch.Apply(original)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatch Apply err: %w", err)
	}

	var modifiedData map[string]any
	if err := json.Unmarshal(modified, &modifiedData); err != nil {
		return fmt.Errorf("RestfulAPIJSONPatch Unmarshal err: %w", err)
	}
	if _, err := collection.UpdateOne(ctx, filter, bson.M{opSet: modifiedData}); err != nil {
		return fmt.Errorf("RestfulAPIJSONPatch UpdateOne err: %w", err)
	}
	return nil
}

func (c *MongoClient) RestfulAPIJSONPatchExtend(collName string, filter bson.M, patchJSON []byte, dataName string) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	originalDataCover, err := getOrigData(collection, filter)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatchExtend getOrigData err: %w", err)
	}

	originalData := originalDataCover[dataName]
	original, err := json.Marshal(originalData)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatchExtend Marshal err: %w", err)
	}

	patch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatchExtend DecodePatch err: %w", err)
	}

	modified, err := patch.Apply(original)
	if err != nil {
		return fmt.Errorf("RestfulAPIJSONPatchExtend Apply err: %w", err)
	}

	var modifiedData map[string]any
	if err := json.Unmarshal(modified, &modifiedData); err != nil {
		return fmt.Errorf("RestfulAPIJSONPatchExtend Unmarshal err: %w", err)
	}
	if _, err := collection.UpdateOne(context.TODO(), filter, bson.M{opSet: bson.M{dataName: modifiedData}}); err != nil {
		return fmt.Errorf("RestfulAPIJSONPatchExtend UpdateOne err: %w", err)
	}
	return nil
}

func (c *MongoClient) RestfulAPIPost(collName string, filter bson.M, postData map[string]any) (bool, error) {
	return c.RestfulAPIPutOne(collName, filter, postData)
}

func (c *MongoClient) RestfulAPIPostWithContext(ctx context.Context, collName string, filter bson.M, postData map[string]any) (bool, error) {
	return c.RestfulAPIPutOneWithContext(ctx, collName, filter, postData)
}

func (c *MongoClient) RestfulAPIPostMany(collName string, filter bson.M, postDataArray []any) error {
	return c.RestfulAPIPostManyWithContext(context.TODO(), collName, filter, postDataArray)
}

func (c *MongoClient) RestfulAPIPostManyWithContext(ctx context.Context, collName string, filter bson.M, postDataArray []any) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	if _, err := collection.InsertMany(ctx, postDataArray); err != nil {
		return fmt.Errorf("RestfulAPIPostManyWithContext InsertMany err: %w", err)
	}
	return nil
}

func (c *MongoClient) RestfulAPICount(collName string, filter bson.M) (int64, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)
	result, err := collection.CountDocuments(context.TODO(), filter)
	if err != nil {
		return 0, fmt.Errorf("RestfulAPICount err: %w", err)
	}
	return result, nil
}

func (c *MongoClient) Drop(collName string) error {
	collection := c.Client.Database(c.dbName).Collection(collName)
	return collection.Drop(context.TODO())
}

/* Get unique identity from counter collection. */
func (c *MongoClient) GetUniqueIdentity(idName string) int32 {
	counterCollection := c.Client.Database(c.dbName).Collection("counter")

	counterFilter := bson.M{}
	counterFilter[fieldID] = idName

	for {
		count := counterCollection.FindOneAndUpdate(context.TODO(), counterFilter, bson.M{"$inc": bson.M{"count": 1}})

		if count.Err() != nil {
			counterData := bson.M{}
			counterData["count"] = 1
			counterData[fieldID] = idName
			if _, err := counterCollection.InsertOne(context.TODO(), counterData); err != nil {
				logger.MongoapiLog.Errorf("GetUniqueIdentity: failed to insert counter %v: %v", idName, err)
			}

			continue
		} else {
			data := bson.M{}
			if err := count.Decode(&data); err != nil {
				logger.MongoapiLog.Errorf("GetUniqueIdentity: failed to decode counter %v: %v", idName, err)
				continue
			}
			decodedCount := data["count"].(int32)
			return decodedCount
		}
	}
}

func (c *MongoClient) PutOneCustomDataStructure(collName string, filter bson.M, putData any) (bool, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)

	var checkItem map[string]any
	if err := collection.FindOne(context.TODO(), filter).Decode(&checkItem); err != nil && err != mongo.ErrNoDocuments {
		return false, fmt.Errorf("PutOneCustomDataStructure FindOne err: %w", err)
	}

	if checkItem == nil {
		_, err := collection.InsertOne(context.TODO(), putData)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := collection.UpdateOne(context.TODO(), filter, bson.M{opSet: putData}); err != nil {
		return false, err
	}
	return true, nil
}

func (c *MongoClient) CreateIndex(collName string, keyField string) (bool, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)

	index := mongo.IndexModel{
		Keys:    bson.D{{Key: keyField, Value: 1}},
		Options: options.Index().SetUnique(true),
	}

	_, err := collection.Indexes().CreateOne(context.Background(), index)
	if err != nil {
		// logger.MongoDBLog.Error("Create Index failed : ", keyField, err)
		return false, err
	}

	// logger.MongoDBLog.Println("Created index : ", idx, " on keyField : ", keyField, " for Collection : ", collName)

	return true, nil
}

// RestfulAPICreateTTLIndexWithContext creates a TTL index named timeField on
// the timeField of a collection and reports why it could not be created.
//
// To create Index with common timeout for all documents, set timeout to desired value
// To create Index with custom timeout per document, set timeout to 0.
// To create Index with common timeout use timefield name like : updatedAt
// To create Index with custom timeout use timefield name like : expireAt
//
// Creating an index that already exists with the same options is a no-op. If an
// index of the same name exists with different options, MongoDB rejects the
// command with IndexOptionsConflict.
func (c *MongoClient) RestfulAPICreateTTLIndexWithContext(ctx context.Context, collName string, timeout int32, timeField string) error {
	collection := c.Client.Database(c.dbName).Collection(collName)
	index := mongo.IndexModel{
		Keys:    bson.D{{Key: timeField, Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(timeout).SetName(timeField),
	}

	if _, err := collection.Indexes().CreateOne(ctx, index); err != nil {
		return fmt.Errorf("create TTL index on field %s of collection %s failed: %w", timeField, collName, err)
	}
	return nil
}

// RestfulAPIDropTTLIndexWithContext drops the index named timeField and reports
// why it could not be dropped. Dropping an index that does not exist is not an
// error, so the call is safe to repeat and safe to race with another process.
func (c *MongoClient) RestfulAPIDropTTLIndexWithContext(ctx context.Context, collName string, timeField string) error {
	collection := c.Client.Database(c.dbName).Collection(collName)
	if err := collection.Indexes().DropOne(ctx, timeField); err != nil {
		if IsIndexNotFound(err) {
			return nil
		}
		return fmt.Errorf("drop index %q of collection %s failed: %w", timeField, collName, err)
	}
	return nil
}

// RestfulAPIListIndexes returns the index specifications of a collection, as
// reported by the listIndexes command. It lets a caller confirm that an index
// it created is really there instead of trusting the result of the create call.
// listIndexes is always served by the primary, so a specification returned here
// is one the collection actually has.
func (c *MongoClient) RestfulAPIListIndexes(ctx context.Context, collName string) ([]bson.M, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list indexes of collection %s failed: %w", collName, err)
	}

	var specs []bson.M
	if err = cursor.All(ctx, &specs); err != nil {
		return nil, fmt.Errorf("decode index specifications of collection %s failed: %w", collName, err)
	}
	return specs, nil
}

// IsIndexNotFound reports whether err is the server saying that an index does
// not exist (code 27).
func IsIndexNotFound(err error) bool {
	var cmdErr mongo.CommandError
	return errors.As(err, &cmdErr) && cmdErr.Code == indexNotFoundErrorCode
}

// IsIndexOptionsConflict reports whether err is the server refusing to create
// an index because one of the same name exists with different options
// (code 85).
func IsIndexOptionsConflict(err error) bool {
	var serverErr mongo.ServerError
	return errors.As(err, &serverErr) && serverErr.HasErrorCode(indexOptionsConflictErrorCode)
}

// This API adds document to collection with name : "collName"
// It also creates an Index with Time to live (TTL) on the collection.
// All Documents in the collection will have the the same TTL. The timestamps
// each document can be different and can be updated as per procedure.
// If the Index with same timeout value is present already then it
// does not create a new one.
// If the Index exists on the same "timeField" with a different timeout,
// then API will return error saying Index already exists.
func (c *MongoClient) RestfulAPIPutOneTimeout(collName string, filter bson.M, putData map[string]any, timeout int32, timeField string) bool {
	collection := c.Client.Database(c.dbName).Collection(collName)
	var checkItem map[string]any

	if err := collection.FindOne(context.TODO(), filter).Decode(&checkItem); err != nil && err != mongo.ErrNoDocuments {
		return false
	}

	if checkItem == nil {
		if _, err := collection.InsertOne(context.TODO(), putData); err != nil {
			return false
		}
		return true
	}
	if _, err := collection.UpdateOne(context.TODO(), filter, bson.M{opSet: putData}); err != nil {
		return false
	}
	return true
}

func (c *MongoClient) RestfulAPIPostOnly(collName string, filter bson.M, postData map[string]any) bool {
	collection := c.Client.Database(c.dbName).Collection(collName)

	_, err := collection.InsertOne(context.TODO(), postData)
	return err == nil
}

func (c *MongoClient) RestfulAPIPutOnly(collName string, filter bson.M, putData map[string]any) error {
	collection := c.Client.Database(c.dbName).Collection(collName)

	result, err := collection.UpdateOne(context.TODO(), filter, bson.M{opSet: putData})
	if err != nil {
		return fmt.Errorf("failed to update document: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("failed to update document: no matching document found")
	}
	// logger.MongoDBLog.Println("matched and replaced an existing document")
	return nil
}

func (c *MongoClient) StartSession() (*mongo.Session, error) {
	return c.Client.StartSession()
}

func (c *MongoClient) SupportsTransactions() (bool, error) {
	command := bson.D{{Key: "hello", Value: 1}}
	result := c.Client.Database(c.dbName).RunCommand(context.Background(), command)
	var status bson.M
	if err := result.Decode(&status); err != nil {
		return false, fmt.Errorf("failed to get server status: %v", err)
	}
	if msg, ok := status["msg"]; ok && msg == "isdbgrid" {
		return true, nil // Sharded clusters support transactions
	}
	if _, ok := status["setName"]; ok {
		return true, nil
	}
	return false, nil
}
