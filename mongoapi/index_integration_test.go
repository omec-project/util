// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
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
	return &MongoClient{Client: client, dbName: testDatabaseName, pools: make(map[string]map[string]int32)}
}

// freshCollection gives a test a collection of its own, dropped before and
// after, so the tests neither collide nor depend on order — and so a run killed
// part way through does not leave documents and indexes behind for the next one
// to inherit. Dropping a collection that is not there is a no-op.
func freshCollection(t *testing.T, client *MongoClient) (string, *mongo.Collection) {
	t.Helper()
	name := "coll_" + t.Name()
	collection := client.Client.Database(client.dbName).Collection(name)
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatalf("could not clear %s before the test: %v", name, err)
	}
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
	if _, err := collection.InsertOne(ctx, detached(subscriberOne)); err != nil {
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
	if _, err = collection.InsertOne(ctx, detached(subscriberOne)); err != nil {
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
	if got := indexNames(t, client, collName)[spec.Name]; got.ExpireAfterSeconds.IsZero() {
		t.Error("expected the TTL index to be left in place")
	}
}

// TestEnsureIndexIsIdempotentOnACollatedCollection pins the loop that a
// too-strict comparison would cause: MongoDB stamps a collection's default
// collation onto every index built on it, including the ones created here.
func TestEnsureIndexIsIdempotentOnACollatedCollection(t *testing.T) {
	uri := os.Getenv(testURIEnv)
	if uri == "" {
		t.Skipf("set %s to run the index integration tests", testURIEnv)
	}
	// Counting indexes cannot tell "left alone" from "dropped and rebuilt", and
	// rebuilding on every startup is the failure this test exists for.
	var drops atomic.Int64
	monitor := &event.CommandMonitor{
		Started: func(_ context.Context, e *event.CommandStartedEvent) {
			if e.CommandName == commandDropIndex {
				drops.Add(1)
			}
		},
	}
	rawClient, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(monitor))
	if err != nil {
		t.Fatalf("could not connect: %v", err)
	}
	t.Cleanup(func() {
		if disconnectErr := rawClient.Disconnect(context.Background()); disconnectErr != nil {
			t.Logf("could not disconnect: %v", disconnectErr)
		}
	})
	client := &MongoClient{Client: rawClient, dbName: testDatabaseName, pools: make(map[string]map[string]int32)}
	ctx := context.Background()
	database := client.Client.Database(client.dbName)
	collName := "coll_" + t.Name()

	// Cleared before and registered for after, as freshCollection does: a run
	// killed between the create and the cleanup would otherwise leave the
	// collection behind and fail every later run with NamespaceExists.
	if err := database.Collection(collName).Drop(context.Background()); err != nil {
		t.Fatalf("could not clear %s before the test: %v", collName, err)
	}
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
	if dropped := drops.Load(); dropped != 0 {
		t.Errorf("expected the index to be left alone, but it was dropped %d times", dropped)
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
		Options: options.Index().SetName(indexByHand),
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
	if _, stillThere := indexNames(t, client, collName)[indexByHand]; !stillThere {
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

// TestEnsureIndexRestoresWhatItDroppedWhenCreationFails covers the failure a
// specification cannot be checked against on its own: asking for Unique over
// data that already holds duplicates. The create fails after the index it was
// replacing has been dropped, and without a restore the collection would be
// left with neither.
func TestEnsureIndexRestoresWhatItDroppedWhenCreationFails(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	if _, err := collection.InsertMany(ctx, []any{
		bson.M{fieldUeId: subscriberOne},
		bson.M{fieldUeId: subscriberOne},
	}); err != nil {
		t.Fatalf("could not insert the duplicate documents: %v", err)
	}
	original := mongo.IndexModel{
		Keys:    AscendingKeys(fieldUeId),
		Options: options.Index().SetName(indexUeId),
	}
	if _, err := collection.Indexes().CreateOne(ctx, original); err != nil {
		t.Fatalf("could not create the original index: %v", err)
	}

	unique := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId), Unique: true}
	err := client.EnsureIndex(ctx, collName, unique)
	if err == nil {
		t.Fatal("expected a unique index over duplicate data to be refused by the server")
	}

	restored, ok := indexNames(t, client, collName)[indexUeId]
	if !ok {
		t.Fatalf("expected the original index to be restored, got %v", indexNames(t, client, collName))
	}
	if restored.Unique {
		t.Error("expected the restored index to be the original non-unique one")
	}
	if canonicalKey(restored.Key) != canonicalKey(unique.Keys) {
		t.Errorf("expected the original key, got %v", restored.Key)
	}
}

// TestEnsureIndexRestoresWhenTheDropLoopIsInterrupted covers the other way a
// call can stop partway through destroying things: the second drop fails after
// the first succeeded. It also covers the reason the restore cannot run on the
// caller's context — here the caller's context is itself what failed the drop.
func TestEnsureIndexRestoresWhenTheDropLoopIsInterrupted(t *testing.T) {
	uri := os.Getenv(testURIEnv)
	if uri == "" {
		t.Skipf("set %s to run the index integration tests", testURIEnv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel the caller's context the moment the first index has been dropped,
	// so the second drop fails and the restore has to outlive the cancellation.
	monitor := &event.CommandMonitor{
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) {
			if e.CommandName == commandDropIndex {
				cancel()
			}
		},
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(monitor))
	if err != nil {
		t.Fatalf("could not connect: %v", err)
	}
	t.Cleanup(func() {
		if disconnectErr := client.Disconnect(context.Background()); disconnectErr != nil {
			t.Logf("could not disconnect: %v", disconnectErr)
		}
	})
	mongoClient := &MongoClient{Client: client, dbName: testDatabaseName, pools: make(map[string]map[string]int32)}

	collName := "coll_" + t.Name()
	collection := client.Database(mongoClient.dbName).Collection(collName)
	if dropErr := collection.Drop(context.Background()); dropErr != nil {
		t.Fatalf("could not clear %s before the test: %v", collName, dropErr)
	}
	t.Cleanup(func() {
		if dropErr := collection.Drop(context.Background()); dropErr != nil {
			t.Logf("could not drop %s: %v", collName, dropErr)
		}
	})

	holdsTheName := mongo.IndexModel{Keys: AscendingKeys(fieldSupi), Options: options.Index().SetName(indexUeId)}
	holdsTheKey := mongo.IndexModel{Keys: AscendingKeys(fieldUeId), Options: options.Index().SetName(indexByHand)}
	if _, err = collection.Indexes().CreateMany(context.Background(), []mongo.IndexModel{holdsTheName, holdsTheKey}); err != nil {
		t.Fatalf("could not create the conflicting indexes: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err = mongoClient.EnsureIndex(ctx, collName, spec); err == nil {
		t.Fatal("expected the cancelled context to fail the call")
	}

	present := indexNames(t, mongoClient, collName)
	for _, name := range []string{indexUeId, indexByHand} {
		if _, ok := present[name]; !ok {
			t.Errorf("expected index %q to be present after an interrupted call, got %v", name, present)
		}
	}
}

// TestEnsureIndexRestoresACollationVerbatim checks that a restore puts a
// collation back exactly as listIndexes reported it, whether or not it happens
// to equal the collection's default. Putting an index back without the
// collation it had would change how the collection compares strings, on a call
// that reported an error and changed nothing else.
func TestEnsureIndexRestoresACollationVerbatim(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	collated := mongo.IndexModel{
		Keys: AscendingKeys(fieldUeId),
		Options: options.Index().SetName(indexUeId).
			SetCollation(&options.Collation{Locale: "en", Strength: 2}),
	}
	if _, err := collection.Indexes().CreateOne(ctx, collated); err != nil {
		t.Fatalf("could not create the collated index: %v", err)
	}
	if _, err := collection.InsertMany(ctx, []any{
		bson.M{fieldUeId: subscriberOne},
		bson.M{fieldUeId: subscriberOne},
	}); err != nil {
		t.Fatalf("could not insert the duplicate documents: %v", err)
	}

	unique := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId), Unique: true}
	if err := client.EnsureIndex(ctx, collName, unique); err == nil {
		t.Fatal("expected a unique index over duplicate data to be refused")
	}

	restored, ok := indexNames(t, client, collName)[indexUeId]
	if !ok {
		t.Fatalf("expected the collated index to be restored, got %v", indexNames(t, client, collName))
	}
	collation := restored.raw.Lookup("collation")
	if collation.Type != bson.TypeEmbeddedDocument {
		t.Fatalf("expected the restored index to keep its collation, got %v", restored.raw)
	}
	if locale := collation.Document().Lookup("locale").StringValue(); locale != "en" {
		t.Errorf("expected the restored collation to keep locale en, got %q", locale)
	}
	if strength := collation.Document().Lookup("strength").AsInt64(); strength != 2 {
		t.Errorf("expected the restored collation to keep strength 2, got %d", strength)
	}
}

// TestEnsureIndexRestoresATextIndexItCouldNeverHaveCreated is the case that a
// field-by-field rebuild cannot survive: a conflict is reached by name as well
// as by key, so the index being put back may be of a shape IndexSpec cannot
// describe. A text index rebuilt without its weights is not merely degraded —
// the server refuses to create it, and the index is lost.
func TestEnsureIndexRestoresATextIndexItCouldNeverHaveCreated(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	text := mongo.IndexModel{
		Keys:    bson.D{{Key: "body", Value: keyTypeText}},
		Options: options.Index().SetName(indexUeId).SetDefaultLanguage("english"),
	}
	if _, err := collection.Indexes().CreateOne(ctx, text); err != nil {
		t.Fatalf("could not create the text index: %v", err)
	}
	if _, err := collection.InsertMany(ctx, []any{
		bson.M{fieldUeId: subscriberOne},
		bson.M{fieldUeId: subscriberOne},
	}); err != nil {
		t.Fatalf("could not insert the duplicate documents: %v", err)
	}

	// Same name, different key, and a Unique spec the duplicates will refuse.
	unique := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId), Unique: true}
	if err := client.EnsureIndex(ctx, collName, unique); err == nil {
		t.Fatal("expected a unique index over duplicate data to be refused")
	}

	restored, ok := indexNames(t, client, collName)[indexUeId]
	if !ok {
		t.Fatalf("expected the text index to be restored, got %v", indexNames(t, client, collName))
	}
	if weights := restored.raw.Lookup("weights"); weights.Type != bson.TypeEmbeddedDocument {
		t.Errorf("expected the restored index to keep its weights, got %v", restored.raw)
	}
	if language := restored.raw.Lookup("default_language").StringValue(); language != "english" {
		t.Errorf("expected the restored index to keep its default language, got %q", language)
	}
}

// TestEnsureIndexNamesTheRightCollisionForATTLIndex keeps the refusal from
// sending an operator to look for a key collision that does not exist: a
// conflict can be the name rather than the key.
func TestEnsureIndexNamesTheRightCollisionForATTLIndex(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	ttlOnAnotherKey := mongo.IndexModel{
		Keys:    AscendingKeys(fieldSupi),
		Options: options.Index().SetName(indexUeId).SetExpireAfterSeconds(3600),
	}
	if _, err := collection.Indexes().CreateOne(ctx, ttlOnAnotherKey); err != nil {
		t.Fatalf("could not create the TTL index: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	err := client.EnsureIndex(ctx, collName, spec)
	if err == nil {
		t.Fatal("expected the TTL index to be refused")
	}
	if !strings.Contains(err.Error(), "has the same name as") {
		t.Errorf("expected the error to name a collision of names, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "covers the same key") {
		t.Errorf("expected the error not to claim a key collision, got %q", err.Error())
	}
}

// TestEnsureIndexRestoresNothingWhenADropSimplyFailed is the other half of
// treating a failed drop as possibly applied. When it was not applied, putting
// the index back has to be a no-op rather than an error the caller then has to
// read past.
func TestEnsureIndexRestoresNothingWhenADropSimplyFailed(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	handMade := mongo.IndexModel{Keys: AscendingKeys(fieldUeId), Options: options.Index().SetName(indexByHand)}
	if _, err := collection.Indexes().CreateOne(ctx, handMade); err != nil {
		t.Fatalf("could not create the hand-made index: %v", err)
	}

	admin := client.Client.Database("admin")
	failPoint := bson.D{
		{Key: "configureFailPoint", Value: "failCommand"},
		{Key: "mode", Value: bson.D{{Key: "times", Value: 1}}},
		{Key: "data", Value: bson.D{
			{Key: "failCommands", Value: bson.A{commandDropIndex}},
			{Key: "errorCode", Value: 91},
		}},
	}
	if err := admin.RunCommand(ctx, failPoint).Err(); err != nil {
		t.Skipf("this server does not accept failpoints; start mongod with --setParameter enableTestCommands=1: %v", err)
	}
	t.Cleanup(func() {
		off := bson.D{{Key: "configureFailPoint", Value: "failCommand"}, {Key: "mode", Value: "off"}}
		if err := admin.RunCommand(context.Background(), off).Err(); err != nil {
			t.Logf("could not clear the failpoint: %v", err)
		}
	})

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	err := client.EnsureIndex(ctx, collName, spec)
	if err == nil {
		t.Fatal("expected the failed drop to fail the call")
	}
	if strings.Contains(err.Error(), "restoring the indexes") {
		t.Errorf("expected no restore failure for a drop that never applied, got %q", err.Error())
	}
	if _, ok := indexNames(t, client, collName)[indexByHand]; !ok {
		t.Error("expected the index to survive a drop that failed without applying")
	}
}

// TestEnsureIndexRepairsADeliberatelyCollatedIndex is the other side of not
// comparing a collation to nothing: one set deliberately makes a different
// index, which will not serve the queries the caller expects, so it has to be
// repaired rather than reported as already correct.
func TestEnsureIndexRepairsADeliberatelyCollatedIndex(t *testing.T) {
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	deliberate := mongo.IndexModel{
		Keys: AscendingKeys(fieldUeId),
		Options: options.Index().SetName(indexUeId).
			SetCollation(&options.Collation{Locale: "fr"}),
	}
	if _, err := collection.Indexes().CreateOne(ctx, deliberate); err != nil {
		t.Fatalf("could not create the collated index: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("EnsureIndex did not repair the collated index: %v", err)
	}
	if got := indexNames(t, client, collName)[indexUeId]; len(got.Collation) != 0 {
		t.Errorf("expected the repaired index to carry the collection default, got %v", got.Collation)
	}
}

// TestEnsureIndexRestoresShadowsWhenTheMatchVanishes covers the path where
// nothing is created because the listing already held the index: if another
// writer removes that index before the verification reads it, the conflicts
// dropped in its favour have to go back, or the call leaves the collection with
// nothing at all covering the key.
func TestEnsureIndexRestoresShadowsWhenTheMatchVanishes(t *testing.T) {
	uri := os.Getenv(testURIEnv)
	if uri == "" {
		t.Skipf("set %s to run the index integration tests", testURIEnv)
	}
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("could not create the index: %v", err)
	}
	// Unique so the server lets it coexist with the plain index over the same
	// key; an equivalent index under a second name is refused outright.
	shadow := mongo.IndexModel{
		Keys:    AscendingKeys(fieldUeId),
		Options: options.Index().SetName(indexByHand).SetUnique(true),
	}
	if _, err := collection.Indexes().CreateOne(ctx, shadow); err != nil {
		t.Fatalf("could not create the shadowing index: %v", err)
	}

	// A second connection stands in for the concurrent writer, removing the
	// matching index the moment the shadow has been dropped.
	saboteur, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("could not connect the second client: %v", err)
	}
	t.Cleanup(func() {
		if disconnectErr := saboteur.Disconnect(context.Background()); disconnectErr != nil {
			t.Logf("could not disconnect: %v", disconnectErr)
		}
	})
	monitor := &event.CommandMonitor{
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) {
			if e.CommandName != commandDropIndex {
				return
			}
			victim := saboteur.Database(client.dbName).Collection(collName)
			if dropErr := victim.Indexes().DropOne(context.Background(), indexUeId); dropErr != nil {
				t.Logf("the concurrent drop did not apply: %v", dropErr)
			}
		},
	}
	watched, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(monitor))
	if err != nil {
		t.Fatalf("could not connect the watched client: %v", err)
	}
	t.Cleanup(func() {
		if disconnectErr := watched.Disconnect(context.Background()); disconnectErr != nil {
			t.Logf("could not disconnect: %v", disconnectErr)
		}
	})
	watchedClient := &MongoClient{Client: watched, dbName: client.dbName, pools: make(map[string]map[string]int32)}

	if err = watchedClient.EnsureIndex(ctx, collName, spec); err == nil {
		t.Fatal("expected the vanished index to fail the verification")
	}
	if _, ok := indexNames(t, client, collName)[indexByHand]; !ok {
		t.Error("expected the shadow to be restored once the index it yielded to was gone")
	}
}

// TestEnsureIndexRestoresWhenTheIndexItCreatedVanishes is the mirror of the
// test above. An index this call created can be removed by another writer just
// as easily as one it found, and the conflicts dropped to make room for it are
// no less gone for the call having been the one to create it.
func TestEnsureIndexRestoresWhenTheIndexItCreatedVanishes(t *testing.T) {
	uri := os.Getenv(testURIEnv)
	if uri == "" {
		t.Skipf("set %s to run the index integration tests", testURIEnv)
	}
	client := integrationClient(t)
	collName, collection := freshCollection(t, client)
	ctx := context.Background()

	handMade := mongo.IndexModel{Keys: AscendingKeys(fieldUeId), Options: options.Index().SetName(indexByHand)}
	if _, err := collection.Indexes().CreateOne(ctx, handMade); err != nil {
		t.Fatalf("could not create the hand-made index: %v", err)
	}

	saboteur, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("could not connect the second client: %v", err)
	}
	t.Cleanup(func() {
		if disconnectErr := saboteur.Disconnect(context.Background()); disconnectErr != nil {
			t.Logf("could not disconnect: %v", disconnectErr)
		}
	})

	// Once only: the restore issues createIndexes too, and a second strike
	// would be removing the index the restore had just put back.
	var once sync.Once
	monitor := &event.CommandMonitor{
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) {
			if e.CommandName != "createIndexes" {
				return
			}
			once.Do(func() {
				victim := saboteur.Database(client.dbName).Collection(collName)
				if dropErr := victim.Indexes().DropOne(context.Background(), indexUeId); dropErr != nil {
					t.Logf("the concurrent drop did not apply: %v", dropErr)
				}
			})
		},
	}
	watched, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(monitor))
	if err != nil {
		t.Fatalf("could not connect the watched client: %v", err)
	}
	t.Cleanup(func() {
		if disconnectErr := watched.Disconnect(context.Background()); disconnectErr != nil {
			t.Logf("could not disconnect: %v", disconnectErr)
		}
	})
	watchedClient := &MongoClient{Client: watched, dbName: client.dbName, pools: make(map[string]map[string]int32)}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err = watchedClient.EnsureIndex(ctx, collName, spec); err == nil {
		t.Fatal("expected the vanished index to fail the verification")
	}
	if _, ok := indexNames(t, client, collName)[indexByHand]; !ok {
		t.Error("expected the index dropped to make room to be restored once the replacement was gone")
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
		Options: options.Index().SetName(indexByHand),
	}
	if _, err := collection.Indexes().CreateMany(ctx, []mongo.IndexModel{occupiesTheName, occupiesTheKey}); err != nil {
		t.Fatalf("could not create the conflicting indexes: %v", err)
	}

	spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId)}
	if err := client.EnsureIndex(ctx, collName, spec); err != nil {
		t.Fatalf("expected one call to clear both conflicts, got %v", err)
	}

	present := indexNames(t, client, collName)
	if _, stillThere := present[indexByHand]; stillThere {
		t.Error("expected the index over the same key to be dropped")
	}
	if got, ok := present[spec.Name]; !ok {
		t.Error("expected the requested index to exist")
	} else if canonicalKey(got.Key) != canonicalKey(spec.Keys) {
		t.Errorf("expected the requested key, got %v", got.Key)
	}
}
