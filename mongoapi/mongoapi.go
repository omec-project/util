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
	"math/rand"
	"os"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/omec-project/util/logger"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoClient struct {
	Client *mongo.Client
	pools  map[string]map[string]int32
	dbName string
	url    string
}

// bson field name and update operator reused across the API implementations below.
const (
	fieldID = "_id"
	opSet   = "$set"
)

func NewMongoClient(url string, dbName string) (*MongoClient, error) {
	c := MongoClient{url: url, dbName: dbName, pools: make(map[string]map[string]int32)}
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

/* Get a unique id within a given range. */
func (c *MongoClient) GetUniqueIdentityWithinRange(pool string, minimum int32, maximum int32) int32 {
	rangeCollection := c.Client.Database(c.dbName).Collection("range")

	rangeFilter := bson.M{}
	rangeFilter[fieldID] = pool

	for {
		count := rangeCollection.FindOneAndUpdate(context.TODO(), rangeFilter, bson.M{"$inc": bson.M{"count": 1}})

		if count.Err() != nil {
			counterData := bson.M{}
			counterData["count"] = minimum
			counterData[fieldID] = pool
			if _, err := rangeCollection.InsertOne(context.TODO(), counterData); err != nil {
				logger.MongoapiLog.Errorf("GetUniqueIdentityWithinRange: failed to insert range %v: %v", pool, err)
			}

			continue
		} else {
			data := bson.M{}
			if err := count.Decode(&data); err != nil {
				logger.MongoapiLog.Errorf("GetUniqueIdentityWithinRange: failed to decode range %v: %v", pool, err)
				continue
			}
			decodedCount := data["count"].(int32)

			if decodedCount >= maximum || decodedCount <= minimum {
				return -1
			}
			return decodedCount
		}
	}
}

/* Initialize pool of ids with maximum and minimum values and chunk size and amount of retries to get a chunk. */
func (c *MongoClient) InitializeChunkPool(poolName string, minimum int32, maximum int32, retries int32, chunkSize int32) {
	// logger.MongoDBLog.Println("ENTERING InitializeChunkPool")
	poolData := map[string]int32{}
	poolData["min"] = minimum
	poolData["max"] = maximum
	poolData["retries"] = retries
	poolData["chunkSize"] = chunkSize

	c.pools[poolName] = poolData
	// logger.MongoDBLog.Println("Pools: ", pools)
}

/* Get id by inserting into collection. If insert succeeds, that id is available. Else, it isn't available so retry. */
func (c *MongoClient) GetChunkFromPool(poolName string) (int32, int32, int32, error) {
	// logger.MongoDBLog.Println("ENTERING GetChunkFromPool")

	pool := c.pools[poolName]

	if pool == nil {
		err := errors.New("this pool has not been initialized yet. Initialize by calling InitializeChunkPool")
		return -1, -1, -1, err
	}

	minimum := pool["min"]
	maximum := pool["max"]
	retries := pool["retries"]
	chunkSize := pool["chunkSize"]
	totalChunks := (maximum - minimum) / chunkSize

	var i int32 = 0
	for i < retries {
		random := rand.Int31n(totalChunks)
		lower := minimum + (random * chunkSize)
		upper := lower + chunkSize
		poolCollection := c.Client.Database(c.dbName).Collection(poolName)

		// Create an instance of an options and set the desired options
		data := bson.M{}
		data[fieldID] = random
		data["lower"] = lower
		data["upper"] = upper
		data["owner"] = os.Getenv("HOSTNAME")
		result := poolCollection.FindOneAndUpdate(context.TODO(), bson.M{fieldID: random}, bson.M{"$setOnInsert": data}, options.FindOneAndUpdate().SetUpsert(true))

		if result.Err() != nil {
			// means that there was no document with that id, so the upsert should have been successful
			if result.Err() == mongo.ErrNoDocuments {
				// logger.MongoDBLog.Println("Assigned chunk # ", random, " with range ", lower, " - ", upper)
				return random, lower, upper, nil
			}

			return -1, -1, -1, result.Err()
		}
		// means there was a document before the update and result contains that document.
		// logger.MongoDBLog.Println("Chunk", random, " has already been assigned. ", retries-i-1, " retries left.")
		i++
	}

	err := errors.New("no id found after retries")
	return -1, -1, -1, err
}

/* Release the provided id to the provided pool. */
func (c *MongoClient) ReleaseChunkToPool(poolName string, id int32) {
	// logger.MongoDBLog.Println("ENTERING ReleaseChunkToPool")
	poolCollection := c.Client.Database(c.dbName).Collection(poolName)

	// only want to delete if the currentApp is the owner of this id.
	currentApp := os.Getenv("HOSTNAME")

	if _, err := poolCollection.DeleteOne(context.TODO(), bson.M{fieldID: id, "owner": currentApp}); err != nil {
		logger.MongoapiLog.Errorf("ReleaseChunkToPool: failed to release id %v: %v", id, err)
	}
}

/* Initialize pool of ids with maximum and minimum values. */
func (c *MongoClient) InitializeInsertPool(poolName string, minimum int32, maximum int32, retries int32) {
	// logger.MongoDBLog.Println("ENTERING InitializeInsertPool")
	poolData := map[string]int32{}
	poolData["min"] = minimum
	poolData["max"] = maximum
	poolData["retries"] = retries

	c.pools[poolName] = poolData
	// logger.MongoDBLog.Println("Pools: ", pools)
}

/* Get id by inserting into collection. If insert succeeds, that id is available. Else, it isn't available so retry. */
func (c *MongoClient) GetIDFromInsertPool(poolName string) (int32, error) {
	// logger.MongoDBLog.Println("ENTERING GetIDFromInsertPool")

	pool := c.pools[poolName]

	if pool == nil {
		err := errors.New("this pool has not been initialized yet. Initialize by calling InitializeInsertPool")
		return -1, err
	}

	minimum := pool["min"]
	maximum := pool["max"]
	retries := pool["retries"]
	var i int32 = 0
	for i < retries {
		random := rand.Int31n(maximum-minimum) + minimum // returns random int in [0, maximum-minimum-1] + minimum
		poolCollection := c.Client.Database(c.dbName).Collection(poolName)

		// Create an instance of an options and set the desired options
		result := poolCollection.FindOneAndUpdate(context.TODO(), bson.M{fieldID: random}, bson.M{opSet: bson.M{fieldID: random}}, options.FindOneAndUpdate().SetUpsert(true))

		if result.Err() != nil {
			// means that there was no document with that id, so the upsert should have been successful
			if result.Err().Error() == "mongo: no documents in result" {
				// logger.MongoDBLog.Println("Assigned id: ", random)
				return random, nil
			}

			return -1, result.Err()
		}
		// means there was a document before the update and result contains that document.
		doc := bson.M{}
		if err := result.Decode(&doc); err != nil {
			return -1, fmt.Errorf("GetIDFromInsertPool decode err: %w", err)
		}

		i++
	}

	err := errors.New("no id found after retries")
	return -1, err
}

/* Release the provided id to the provided pool. */
func (c *MongoClient) ReleaseIDToInsertPool(poolName string, id int32) {
	// logger.MongoDBLog.Println("ENTERING ReleaseIDToInsertPool")
	poolCollection := c.Client.Database(c.dbName).Collection(poolName)

	if _, err := poolCollection.DeleteOne(context.TODO(), bson.M{fieldID: id}); err != nil {
		logger.MongoapiLog.Errorf("ReleaseIDToInsertPool: failed to release id %v: %v", id, err)
	}
}

/* Initialize pool of ids with maximum and minimum values. */
func (c *MongoClient) InitializePool(poolName string, minimum int32, maximum int32) {
	// logger.MongoDBLog.Println("ENTERING InitializePool")
	poolCollection := c.Client.Database(c.dbName).Collection(poolName)
	names, err := c.Client.Database(c.dbName).ListCollectionNames(context.TODO(), bson.M{})
	if err != nil {
		// logger.MongoDBLog.Println(err)
		return
	}

	// logger.MongoDBLog.Println(names)

	exists := false
	for _, name := range names {
		if name == poolName {
			// logger.MongoDBLog.Println("The collection exists!")
			exists = true
			break
		}
	}
	if !exists {
		// logger.MongoDBLog.Println("Creating collection")

		array := []int32{}
		for i := minimum; i < maximum; i++ {
			array = append(array, i)
		}
		poolData := bson.M{}
		poolData["ids"] = array
		poolData[fieldID] = poolName

		// collection is created when inserting document.
		// "If a collection does not exist, MongoDB creates the collection when you first store data for that collection."
		if _, err := poolCollection.InsertOne(context.TODO(), poolData); err != nil {
			logger.MongoapiLog.Errorf("InitializePool: failed to insert pool %v: %v", poolName, err)
		}
	}
}

/* For example IP addresses need to be assigned and then returned to be used again. */
func (c *MongoClient) GetIDFromPool(poolName string) (int32, error) {
	// logger.MongoDBLog.Println("ENTERING GetIDFromPool")
	poolCollection := c.Client.Database(c.dbName).Collection(poolName)

	result := bson.M{}
	if err := poolCollection.FindOneAndUpdate(context.TODO(), bson.M{fieldID: poolName}, bson.M{"$pop": bson.M{"ids": 1}}).Decode(&result); err != nil {
		return -1, fmt.Errorf("GetIDFromPool decode err: %w", err)
	}

	idsRaw, ok := result["ids"]
	if !ok || idsRaw == nil {
		return -1, errors.New("there are no available ids")
	}
	ids, ok := idsRaw.(bson.A)
	if !ok {
		return -1, fmt.Errorf("GetIDFromPool: unexpected type for ids: %T", idsRaw)
	}

	var array []int32
	for _, s := range ids {
		switch v := s.(type) {
		case int32:
			array = append(array, v)
		case int64:
			const maxInt32 = int64(^uint32(0) >> 1)
			const minInt32 = -maxInt32 - 1
			if v > maxInt32 || v < minInt32 {
				return -1, fmt.Errorf("GetIDFromPool: id out of int32 range: %d", v)
			}
			array = append(array, int32(v))
		default:
			return -1, fmt.Errorf("GetIDFromPool: unexpected element type %T", s)
		}
	}

	// logger.MongoDBLog.Println("Array of ids: ", array)
	if len(array) > 0 {
		return array[len(array)-1], nil
	}
	return -1, errors.New("there are no available ids")
}

/* Release the provided id to the provided pool. */
func (c *MongoClient) ReleaseIDToPool(poolName string, id int32) {
	// logger.MongoDBLog.Println("ENTERING ReleaseIDToPool")
	poolCollection := c.Client.Database(c.dbName).Collection(poolName)

	if _, err := poolCollection.UpdateOne(context.TODO(), bson.M{fieldID: poolName}, bson.M{"$push": bson.M{"ids": id}}); err != nil {
		logger.MongoapiLog.Errorf("ReleaseIDToPool: failed to release id %v to pool %v: %v", id, poolName, err)
	}
}

func (c *MongoClient) GetOneCustomDataStructure(collName string, filter bson.M) (bson.M, error) {
	collection := c.Client.Database(c.dbName).Collection(collName)

	val := collection.FindOne(context.TODO(), filter)

	if val.Err() != nil {
		return bson.M{}, val.Err()
	}

	var result bson.M
	err := val.Decode(&result)
	return result, err
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

// To create Index with common timeout for all documents, set timeout to desired value
// To create Index with custom timeout per document, set timeout to 0.
// To create Index with common timeout use timefield name like : updatedAt
// To create Index with custom timeout use timefield name like : expireAt
func (c *MongoClient) RestfulAPICreateTTLIndex(collName string, timeout int32, timeField string) bool {
	collection := c.Client.Database(c.dbName).Collection(collName)
	index := mongo.IndexModel{
		Keys:    bson.D{{Key: timeField, Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(timeout).SetName(timeField),
	}

	_, err := collection.Indexes().CreateOne(context.Background(), index)
	return err == nil
}

// Use this API to drop TTL Index.
func (c *MongoClient) RestfulAPIDropTTLIndex(collName string, timeField string) bool {
	collection := c.Client.Database(c.dbName).Collection(collName)
	err := collection.Indexes().DropOne(context.Background(), timeField)
	return err == nil
}

// Use this API to update timeout value for TTL Index.
func (c *MongoClient) RestfulAPIPatchTTLIndex(collName string, timeout int32, timeField string) bool {
	collection := c.Client.Database(c.dbName).Collection(collName)
	err := collection.Indexes().DropOne(context.Background(), timeField)
	if err != nil {
		// Ignore "index not found" (code 27): the index may not exist yet,
		// but we should still proceed to create the new TTL index.
		var cmdErr mongo.CommandError
		if !errors.As(err, &cmdErr) || cmdErr.Code != 27 {
			// logger.MongoDBLog.Println("Drop Index on field (", timeField, ") for collection (", collName, ") failed : ", err)
			return false
		}
	}

	// create new index with new timeout
	index := mongo.IndexModel{
		Keys:    bson.D{{Key: timeField, Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(timeout).SetName(timeField),
	}

	_, err = collection.Indexes().CreateOne(context.Background(), index)
	return err == nil
}

// This API adds document to collection with name : "collName"
// This API should be used when we wish to update the timeout value for the TTL index
// It checks if an Index with name "indexName" exists on the collection.
// If such an Index is "indexName" is found, we drop the index and then
// add new Index with new timeout value.
func (c *MongoClient) RestfulAPIPatchOneTimeout(collName string, filter bson.M, putData map[string]any, timeout int32, timeField string) bool {
	collection := c.Client.Database(c.dbName).Collection(collName)
	var checkItem map[string]any

	// fetch all Indexes on collection
	cursor, err := collection.Indexes().List(context.TODO())
	if err != nil {
		// logger.MongoDBLog.Println("RestfulAPIPatchOneTimeout : List Index failed for collection (", collName, ") : ", err)
		return false
	}

	var result []bson.M
	// convert to map
	if err = cursor.All(context.TODO(), &result); err != nil {
		// logger.MongoDBLog.Println("RestfulAPIPatchOneTimeout : Cursor decode failed for collection (", collName, ") : ", err)
		return false
	}

	// loop through the map and check for entry with key as name
	// for every entry with key as name,check if the value string contains the timeField string.
	// the Indexes are generally named such as follows :
	// field name : createdAt, index name : createdAt_1
	// drop the index if found.
	drop := false
	for _, v := range result {
		for k1, v1 := range v {
			valStr := fmt.Sprint(v1)
			if (k1 == "name") && strings.Contains(valStr, timeField) {
				err = collection.Indexes().DropOne(context.Background(), valStr)
				if err != nil {
					// logger.MongoDBLog.Println("Drop Index on field (", timeField, ") for collection (", collName, ") failed : ", err)
					return false
				}
				drop = true
				break
			}
		}
		if drop {
			break
		}
	}

	// create new index with new timeout
	index := mongo.IndexModel{
		Keys:    bson.D{{Key: timeField, Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(timeout),
	}

	if _, err := collection.Indexes().CreateOne(context.Background(), index); err != nil {
		// index may already exist with the desired timeout
		logger.MongoapiLog.Debugf("RestfulAPIPatchOneTimeout: create index on %v failed (may already exist): %v", timeField, err)
	}

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
