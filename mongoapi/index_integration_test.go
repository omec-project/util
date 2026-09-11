// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// These tests need a MongoDB to talk to, because what they check is what the
// server does with an index specification, not what the driver sends. Point
// MONGODB_TEST_URI at one to run them:
//
//	docker run -d --rm -p 27055:27017 mongo:7
//	MONGODB_TEST_URI='mongodb://127.0.0.1:27055/?directConnection=true' go test ./mongoapi/
const testURIEnv = "MONGODB_TEST_URI"

func integrationClient(t *testing.T) *MongoClient {
	t.Helper()
	uri := os.Getenv(testURIEnv)
	if uri == "" {
		t.Skipf("set %s to run the index integration tests", testURIEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("could not connect to %s: %v", uri, err)
	}
	if err = client.Ping(ctx, nil); err != nil {
		t.Fatalf("could not reach %s: %v", uri, err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Logf("could not disconnect: %v", err)
		}
	})
	return &MongoClient{Client: client, dbName: "mongoapi_index_test", pools: make(map[string]map[string]int32)}
}

// freshCollection gives a test a collection of its own, dropped afterwards, so
// the tests neither collide nor depend on order.
func freshCollection(t *testing.T, client *MongoClient) (string, *mongo.Collection) {
	t.Helper()
	name := "coll_" + t.Name()
	collection := client.Client.Database(client.dbName).Collection(name)
	t.Cleanup(func() {
		if err := collection.Drop(context.Background()); err != nil {
			t.Logf("could not drop %s: %v", name, err)
		}
	})
	return name, collection
}

func indexNames(t *testing.T, client *MongoClient, collName string) map[string]indexSpecDoc {
	t.Helper()
	specs, err := listIndexSpecs(context.Background(), client.Client.Database(client.dbName).Collection(collName))
	if err != nil {
		t.Fatalf("could not list indexes: %v", err)
	}
	byName := make(map[string]indexSpecDoc, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	return byName
}

func TestEnsureIndexCreatesAndIsIdempotent(t *testing.T) {
	client := integrationClient(t)
	collName, _ := freshCollection(t, client)
	ctx := context.Background()

	spec := IndexSpec{
		Name: "ueId_1_servingPlmnId_1",
		Keys: AscendingKeys(fieldUeId, "servingPlmnId"),
	}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("first EnsureIndex failed: %v", err)
	}

	created, ok := indexNames(t, client, collName)[spec.Name]
	if !ok {
		t.Fatalf("expected the index to exist, got %v", indexNames(t, client, collName))
	}
	if canonicalKey(created.Key) != canonicalKey(spec.Keys) {
		t.Errorf("expected key %v, got %v", spec.Keys, created.Key)
	}
	if created.Unique {
		t.Error("expected the index to be non-unique")
	}

	// Idempotence is the property that makes startup creation safe to repeat on
	// every restart and safe to race between replicas.
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("second EnsureIndex failed: %v", err)
	}
	if count := len(indexNames(t, client, collName)); count != 2 {
		t.Errorf("expected _id_ and one index, got %d", count)
	}
}

func TestEnsureIndexRepairsAnIndexWithTheWrongOptions(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	// What an operator leaves behind: the right key, created by hand, with no options.
	handMade := mongo.IndexModel{Keys: AscendingKeys(fieldUeId), Options: options.Index().SetName(indexUeId)}
	if _, err := collection.Indexes().CreateOne(ctx, handMade); err != nil {
		t.Fatalf("could not create the hand-made index: %v", err)
	}

	spec := IndexSpec{
		Name:          indexUeId,
		Keys:          AscendingKeys(fieldUeId),
		PartialFilter: bson.M{fieldUeId: bson.M{opGt: ""}},
	}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("EnsureIndex did not repair the hand-made index: %v", err)
	}

	repaired := indexNames(t, client, collName)[spec.Name]
	if len(repaired.PartialFilterExpression) == 0 {
		t.Error("expected the repaired index to carry the partial filter")
	}
}

func TestEnsureIndexRepairsTheSameKeyUnderAnotherName(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	handMade := mongo.IndexModel{Keys: AscendingKeys(fieldUeId), Options: options.Index().SetName("made_by_hand")}
	if _, err := collection.Indexes().CreateOne(ctx, handMade); err != nil {
		t.Fatalf("could not create the hand-made index: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("EnsureIndex did not take over the key: %v", err)
	}

	present := indexNames(t, client, collName)
	if _, stillThere := present["made_by_hand"]; stillThere {
		t.Error("expected the hand-made index to be dropped rather than left beside the new one")
	}
	if _, ok := present[spec.Name]; !ok {
		t.Error("expected the named index to exist")
	}
}

// TestEnsureIndexRepairsAUniqueIndexIntoAPartialOne exercises the repair this
// package exists for, on the shape that motivated it. A caller that writes a
// placeholder value into an indexed field for the records it never looks up —
// omec-project/amf stores ranUeNgapId 0 and ranId "" for a UE with no RAN
// association — cannot have a plain unique index over those fields, because the
// second such document collides with the first. Restricting the same index to
// the records the query does seek is the fix, and EnsureIndex has to be able to
// carry an existing index across to it.
func TestEnsureIndexRepairsAUniqueIndexIntoAPartialOne(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	detached := func(supi string) bson.M {
		return bson.M{
			fieldSupi: supi,
			"customFieldsAmfUe": bson.M{
				"ranUeNgapId": 0,
				"ranId":       "",
			},
		}
	}
	keys := AscendingKeys(fieldRanUeNgapId, fieldRanId)

	unique := IndexSpec{Name: "sentinel_unique", Keys: keys, Unique: true}
	if err := client.EnsureIndex(ctx, collName, unique); err != nil {
		t.Fatalf("could not create the unique index: %v", err)
	}
	if _, err := collection.InsertOne(ctx, detached("imsi-1")); err != nil {
		t.Fatalf("the first detached UE should insert: %v", err)
	}
	_, err := collection.InsertOne(ctx, detached("imsi-2"))
	if err == nil {
		t.Fatal("expected the second detached UE to be rejected by the unique index")
	}
	if !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("expected a duplicate key error, got %v", err)
	}

	// The same index, restricted to the UEs a query actually seeks, accepts both.
	if _, err = collection.DeleteMany(ctx, bson.M{}); err != nil {
		t.Fatalf("could not clear the collection: %v", err)
	}
	partial := IndexSpec{
		Name:          "sentinel_unique",
		Keys:          keys,
		PartialFilter: bson.M{fieldRanId: bson.M{opGt: ""}},
		Unique:        true,
	}
	if err = client.EnsureIndex(ctx, collName, partial); err != nil {
		t.Fatalf("could not repair the index into a partial one: %v", err)
	}
	if _, err = collection.InsertOne(ctx, detached("imsi-1")); err != nil {
		t.Fatalf("the first detached UE should insert: %v", err)
	}
	if _, err = collection.InsertOne(ctx, detached("imsi-2")); err != nil {
		t.Fatalf("the second detached UE should insert under a partial index: %v", err)
	}
}

// TestEnsureIndexRefusesToDropATTLIndex is the case where a wrong answer would
// be worst. A TTL index deletes the caller's documents; it must never be
// reported as the plain index that was asked for, and dropping it would stop
// the collection expiring anything with no sign that it had changed. This
// package's own RestfulAPICreateTTLIndexWithContext names its index after the
// time field, so the collision is reachable from inside the package.
func TestEnsureIndexRefusesToDropATTLIndex(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	ttl := mongo.IndexModel{
		Keys:    AscendingKeys("expireAt"),
		Options: options.Index().SetName("expireAt_1").SetExpireAfterSeconds(0),
	}
	if _, err := collection.Indexes().CreateOne(ctx, ttl); err != nil {
		t.Fatalf("could not create the TTL index: %v", err)
	}

	spec := IndexSpec{Name: "expireAt_1", Keys: AscendingKeys("expireAt")}
	err := client.EnsureIndex(ctx, collName, spec)
	if err == nil {
		t.Fatal("expected EnsureIndex to refuse rather than silently disable document expiry")
	}
	if !strings.Contains(err.Error(), "TTL index") {
		t.Errorf("expected the error to name the TTL index, got %q", err.Error())
	}
	if got := indexNames(t, client, collName)[spec.Name]; got.ExpireAfterSeconds == nil {
		t.Error("expected the TTL index to be left in place")
	}
}

// TestEnsureIndexIsIdempotentOnACollatedCollection pins the loop that a
// too-strict comparison would cause: MongoDB stamps a collection's default
// collation onto every index built on it, including the ones created here.
func TestEnsureIndexIsIdempotentOnACollatedCollection(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()
	database := client.Client.Database(client.dbName)
	collName := "coll_" + t.Name()

	// Registered before the collection is created, not after: dropping one that
	// is not there is a no-op, and a run killed between the two would otherwise
	// leave the collection behind and fail every later run with NamespaceExists.
	t.Cleanup(func() {
		if err := database.Collection(collName).Drop(context.Background()); err != nil {
			t.Logf("could not drop %s: %v", collName, err)
		}
	})
	opts := options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 2})
	if err := database.CreateCollection(ctx, collName, opts); err != nil {
		t.Fatalf("could not create a collated collection: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := client.EnsureIndex(ctx, collName, spec); err != nil {
			t.Fatalf("EnsureIndex failed on attempt %d: %v", attempt, err)
		}
	}
	if count := len(indexNames(t, client, collName)); count != 2 {
		t.Errorf("expected _id_ and one index after three calls, got %d", count)
	}
}

// TestEnsureIndexClearsAShadowWithoutRebuildingTheMatch covers the case where
// the requested index is already right and something else covers its key.
func TestEnsureIndexClearsAShadowWithoutRebuildingTheMatch(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("could not create the index: %v", err)
	}
	shadow := mongo.IndexModel{
		Keys:    AscendingKeys(fieldUeId),
		Options: options.Index().SetName("shadow").SetUnique(true),
	}
	if _, err := collection.Indexes().CreateOne(ctx, shadow); err != nil {
		t.Fatalf("could not create the shadowing index: %v", err)
	}

	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("EnsureIndex failed with a match and a shadow present: %v", err)
	}
	present := indexNames(t, client, collName)
	if _, stillThere := present["shadow"]; stillThere {
		t.Error("expected the shadowing index to be dropped")
	}
	if _, ok := present[spec.Name]; !ok {
		t.Error("expected the matching index to be kept")
	}
}

// TestEnsureIndexKeepsTheExistingIndexWhenTheSpecIsInvalid pins the ordering
// that matters: validation happens before anything is dropped, so a spec the
// package cannot build never leaves the collection with no index at all.
func TestEnsureIndexKeepsTheExistingIndexWhenTheSpecIsInvalid(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	existing := mongo.IndexModel{
		Keys:    AscendingKeys(fieldUeId),
		Options: options.Index().SetName("by_hand"),
	}
	if _, err := collection.Indexes().CreateOne(ctx, existing); err != nil {
		t.Fatalf("could not create the existing index: %v", err)
	}

	invalid := IndexSpec{
		Name:          indexUeId,
		Keys:          AscendingKeys(fieldUeId),
		PartialFilter: bson.M{fieldUeId: make(chan int)},
	}
	if err := client.EnsureIndex(ctx, collName, invalid); err == nil {
		t.Fatal("expected an unencodable partial filter to be rejected")
	}
	if _, stillThere := indexNames(t, client, collName)["by_hand"]; !stillThere {
		t.Error("expected the existing index to survive a spec that could never have replaced it")
	}
}

// TestEnsureIndexRefusesBeforeDroppingAnything covers the pairing that makes
// per-conflict refusal unsafe: one index holds the requested name and another
// is a TTL over the requested key. Deciding the refusal inside the drop loop
// would remove the first and then refuse on the second, leaving the collection
// with neither.
func TestEnsureIndexRefusesBeforeDroppingAnything(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	holdsTheName := mongo.IndexModel{
		Keys:    AscendingKeys(fieldSupi),
		Options: options.Index().SetName(indexUeId),
	}
	holdsTheKeyAsTTL := mongo.IndexModel{
		Keys:    AscendingKeys(fieldUeId),
		Options: options.Index().SetName("ueId_ttl").SetExpireAfterSeconds(3600),
	}
	if _, err := collection.Indexes().CreateMany(ctx, []mongo.IndexModel{holdsTheName, holdsTheKeyAsTTL}); err != nil {
		t.Fatalf("could not create the conflicting indexes: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err == nil {
		t.Fatal("expected EnsureIndex to refuse because of the TTL index")
	}

	present := indexNames(t, client, collName)
	if _, stillThere := present[indexUeId]; !stillThere {
		t.Error("expected the index holding the name to survive a call that refused")
	}
	if _, stillThere := present["ueId_ttl"]; !stillThere {
		t.Error("expected the TTL index to survive")
	}
}

// TestEnsureIndexClearsEveryConflictInOneCall keeps a caller with no retry loop
// from needing as many startups as the collection has conflicting indexes.
func TestEnsureIndexClearsEveryConflictInOneCall(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	occupiesTheName := mongo.IndexModel{
		Keys:    AscendingKeys(fieldSupi),
		Options: options.Index().SetName(indexUeId),
	}
	occupiesTheKey := mongo.IndexModel{
		Keys:    AscendingKeys(fieldUeId),
		Options: options.Index().SetName("by_hand"),
	}
	if _, err := collection.Indexes().CreateMany(ctx, []mongo.IndexModel{occupiesTheName, occupiesTheKey}); err != nil {
		t.Fatalf("could not create the conflicting indexes: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("expected one call to clear both conflicts, got %v", err)
	}

	present := indexNames(t, client, collName)
	if _, stillThere := present["by_hand"]; stillThere {
		t.Error("expected the index over the same key to be dropped")
	}
	if got, ok := present[spec.Name]; !ok {
		t.Error("expected the requested index to exist")
	} else if canonicalKey(got.Key) != canonicalKey(spec.Keys) {
		t.Errorf("expected the requested key, got %v", got.Key)
	}
}
