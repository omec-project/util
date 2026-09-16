// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// resolveIndexOptions applies a builder's setters so a test can read back the
// options the driver would send.
func resolveIndexOptions(t *testing.T, builder *options.IndexOptionsBuilder) *options.IndexOptions {
	t.Helper()
	resolved := &options.IndexOptions{}
	for _, set := range builder.Opts {
		if err := set(resolved); err != nil {
			t.Fatalf("could not apply an index option: %v", err)
		}
	}
	return resolved
}

// Field, operator and index names repeated across these tests.
const (
	fieldUeId        = "ueId"
	fieldSupi        = "supi"
	fieldAmfUeNgapId = "customFieldsAmfUe.amfUeNgapId"
	fieldRanUeNgapId = "customFieldsAmfUe.ranUeNgapId"
	fieldRanId       = "customFieldsAmfUe.ranId"
	indexUeId        = "ueId_1"
	indexByHand      = "by_hand"
	subscriberOne    = "imsi-1"
	keyTypeText      = "text"
	commandDropIndex = "dropIndexes"
	testDatabaseName = "mongoapi_index_test"
	opGt             = "$gt"
	opLt             = "$lt"
	opIn             = "$in"
	opAnd            = "$and"

	// errBadDirection is the validation message several key shapes share.
	errBadDirection = "want 1 or -1"
)

// rawInt32 builds the raw value listIndexes would report for a TTL.
func rawInt32(t *testing.T, value int32) bson.RawValue {
	t.Helper()
	return mustMarshal(t, bson.M{"v": value}).Lookup("v")
}

func mustMarshal(t *testing.T, value any) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(value)
	if err != nil {
		t.Fatalf("could not marshal %v: %v", value, err)
	}
	return raw
}

func TestIndexSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		spec    IndexSpec
	}{
		{
			name: "a compound ascending index is valid",
			spec: IndexSpec{Name: "ueId_1_servingPlmnId_1", Keys: AscendingKeys("ueId", "servingPlmnId")},
		},
		{
			name: "a descending key is valid",
			spec: IndexSpec{Name: "createdAt_-1", Keys: bson.D{{Key: "createdAt", Value: -1}}},
		},
		{
			name:    "the name is required, because an index cannot be repaired without it",
			spec:    IndexSpec{Keys: AscendingKeys(fieldUeId)},
			wantErr: "index name is required",
		},
		{
			name:    "the _id index cannot be managed",
			spec:    IndexSpec{Name: idIndexName, Keys: AscendingKeys("_id")},
			wantErr: "maintained by MongoDB",
		},
		{
			name:    "an index needs at least one key",
			spec:    IndexSpec{Name: "empty"},
			wantErr: "has no keys",
		},
		{
			name:    "a key needs a field name",
			spec:    IndexSpec{Name: "blank", Keys: bson.D{{Key: "", Value: 1}}},
			wantErr: "empty field name",
		},
		{
			name:    "a direction other than 1 or -1 is rejected",
			spec:    IndexSpec{Name: "sideways", Keys: bson.D{{Key: fieldUeId, Value: 2}}},
			wantErr: errBadDirection,
		},
		{
			name:    "a non-numeric direction is rejected",
			spec:    IndexSpec{Name: "texty", Keys: bson.D{{Key: fieldUeId, Value: keyTypeText}}},
			wantErr: errBadDirection,
		},
		{
			// Truncating this to 1 would accept a direction the server does not
			// have, and would make it compare equal to a genuine ascending key.
			name:    "a fractional direction is rejected rather than rounded",
			spec:    IndexSpec{Name: "fractional", Keys: bson.D{{Key: fieldUeId, Value: 1.5}}},
			wantErr: errBadDirection,
		},
		{
			name:    "a partial filter that cannot be encoded is rejected",
			wantErr: "cannot be encoded",
			spec: IndexSpec{
				Name:          "unencodable",
				Keys:          AscendingKeys(fieldUeId),
				PartialFilter: bson.M{fieldUeId: make(chan int)},
			},
		},
		{
			name:    "a wildcard key is rejected, because its projection cannot be described",
			wantErr: "wildcard key",
			spec:    IndexSpec{Name: "wild", Keys: AscendingKeys("$**")},
		},
		{
			name:    "a wildcard key nested under a path is rejected too",
			wantErr: "wildcard key",
			spec:    IndexSpec{Name: "wild_path", Keys: AscendingKeys("customFieldsAmfUe.$**")},
		},
		{
			name:    "a direction outside int32 is rejected rather than wrapped",
			spec:    IndexSpec{Name: "huge", Keys: bson.D{{Key: fieldUeId, Value: int64(1) << 40}}},
			wantErr: errBadDirection,
		},
		{
			name: "sparse and a partial filter cannot both be set",
			spec: IndexSpec{
				Name:          "both",
				Keys:          AscendingKeys(fieldUeId),
				Sparse:        true,
				PartialFilter: bson.M{fieldUeId: bson.M{opGt: ""}},
			},
			wantErr: "MongoDB rejects",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the spec to be valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected an error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestAscendingKeysPreservesOrder(t *testing.T) {
	keys := AscendingKeys("ueId", "servingPlmnId")
	want := bson.D{{Key: fieldUeId, Value: 1}, {Key: "servingPlmnId", Value: 1}}
	if canonicalKey(keys) != canonicalKey(want) {
		t.Errorf("expected %v, got %v", want, keys)
	}
}

func TestIndexSpecModelCarriesEveryOption(t *testing.T) {
	spec := IndexSpec{
		Name:          "ranUeNgapId_1_ranId_1",
		Keys:          AscendingKeys(fieldRanUeNgapId, fieldRanId),
		PartialFilter: bson.M{fieldRanId: bson.M{opGt: ""}},
	}
	opts := resolveIndexOptions(t, spec.model().Options)

	if opts.Name == nil || *opts.Name != spec.Name {
		t.Errorf("expected the model to carry name %q, got %v", spec.Name, opts.Name)
	}
	if opts.Unique == nil || *opts.Unique {
		t.Error("expected the model to be explicitly non-unique")
	}
	if opts.Sparse != nil {
		t.Error("expected sparse to be left unset when it was not asked for")
	}
	if opts.PartialFilterExpression == nil {
		t.Error("expected the partial filter to reach the model")
	}
	if canonicalKey(spec.model().Keys.(bson.D)) != canonicalKey(spec.Keys) {
		t.Error("expected the model to carry the spec's keys in order")
	}
}

func TestIndexSpecModelSetsSparse(t *testing.T) {
	spec := IndexSpec{Name: "guti_1", Keys: AscendingKeys("guti"), Unique: true, Sparse: true}
	opts := resolveIndexOptions(t, spec.model().Options)
	if opts.Sparse == nil || !*opts.Sparse {
		t.Error("expected sparse to be set")
	}
	if opts.Unique == nil || !*opts.Unique {
		t.Error("expected unique to be set")
	}
}

// TestEnsureIndexIsReachableThroughTheSharedHandle is a compile-time pin. The
// package's shared client is a DBInterface, so a method only on *MongoClient
// cannot be called by the network functions this API is for — they would need a
// type assertion. If EnsureIndex ever leaves the interface, this stops building.
func TestEnsureIndexIsReachableThroughTheSharedHandle(t *testing.T) {
	var db DBInterface = newUnreachableClient(t)
	err := db.EnsureIndex(canceledContext(t), "amf.data.amfState",
		IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldSupi)})
	if err == nil {
		t.Fatal("expected an error from a client with no server to reach")
	}
}

func TestIndexDirectionReadsEveryNumericTypeTheDriverEncodes(t *testing.T) {
	ascending := canonicalKey(bson.D{{Key: fieldUeId, Value: int32(1)}})
	for _, value := range []any{
		1, int8(1), int16(1), int32(1), int64(1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1),
		float32(1), float64(1),
	} {
		if got := canonicalKey(bson.D{{Key: fieldUeId, Value: value}}); got != ascending {
			t.Errorf("expected %T(%v) to read as the direction 1, got %q", value, value, got)
		}
	}

	// And the ones that are not a direction still must not become one.
	for _, value := range []any{float32(1.5), uint64(1) << 40, int64(1) << 40, "1"} {
		if got := canonicalKey(bson.D{{Key: fieldUeId, Value: value}}); got == ascending {
			t.Errorf("expected %T(%v) not to read as the direction 1", value, value)
		}
	}
}

func TestCanonicalKeyDistinguishesOrderAndDirection(t *testing.T) {
	ab := canonicalKey(AscendingKeys("a", "b"))
	ba := canonicalKey(AscendingKeys("b", "a"))
	if ab == ba {
		t.Error("expected (a, b) and (b, a) to be different indexes")
	}

	descending := canonicalKey(bson.D{{Key: "a", Value: 1}, {Key: "b", Value: -1}})
	if ab == descending {
		t.Error("expected an ascending and a descending key to differ")
	}
}

func TestCanonicalKeyNormalisesTheDirectionTypeMongoReturns(t *testing.T) {
	// listIndexes decodes a direction as int32, a caller writes it as int, and
	// an extended-JSON round trip can make it a float64. All three are the same
	// index, and comparing them as written would rebuild it on every startup.
	asInt := canonicalKey(bson.D{{Key: fieldUeId, Value: 1}})
	for _, value := range []any{int32(1), int64(1), float64(1)} {
		if got := canonicalKey(bson.D{{Key: fieldUeId, Value: value}}); got != asInt {
			t.Errorf("expected direction %T(%v) to canonicalise like int(1), got %q vs %q", value, value, got, asInt)
		}
	}
}

func TestCanonicalKeyDoesNotRoundAFractionalDirectionIntoADirection(t *testing.T) {
	ascending := canonicalKey(bson.D{{Key: fieldUeId, Value: 1}})
	fractional := canonicalKey(bson.D{{Key: fieldUeId, Value: 1.5}})
	if ascending == fractional {
		t.Errorf("expected 1.5 to render differently from 1, both gave %q", ascending)
	}

	wrapped := canonicalKey(bson.D{{Key: fieldUeId, Value: int64(1) << 40}})
	if wrapped == canonicalKey(bson.D{{Key: fieldUeId, Value: 0}}) {
		t.Error("expected a direction outside int32 not to wrap onto 0")
	}
}

func TestCanonicalKeyDistinguishesAStringFromADirection(t *testing.T) {
	// A key MongoDB reports in a form this package does not model must never
	// render as a direction it does not have.
	asDirection := canonicalKey(bson.D{{Key: fieldUeId, Value: 1}})
	asString := canonicalKey(bson.D{{Key: fieldUeId, Value: "1"}})
	if asDirection == asString {
		t.Errorf("expected the string \"1\" to render differently from the direction 1, both gave %q", asDirection)
	}
	if text := canonicalKey(bson.D{{Key: fieldUeId, Value: keyTypeText}}); text == asDirection {
		t.Error("expected a text index key to render differently from an ascending key")
	}
}

func TestIndexSpecDocDecodesATTLOfAnyNumericType(t *testing.T) {
	// Only the presence of expireAfterSeconds matters here, and it reaches us
	// as whatever the server chose to store. Decoding it as a fixed numeric
	// type fails the whole listing, so an unrelated index on the same
	// collection could never be ensured either.
	for name, value := range map[string]any{
		"int32":  int32(60),
		"int64":  int64(60),
		"double": 60.0,
	} {
		t.Run(name, func(t *testing.T) {
			raw := mustMarshal(t, bson.M{
				"name":               "t_1",
				"key":                bson.M{"t": 1},
				"expireAfterSeconds": value,
			})
			var doc indexSpecDoc
			if err := bson.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("expected a TTL written as %T to decode, got %v", value, err)
			}
			if doc.ExpireAfterSeconds.IsZero() {
				t.Errorf("expected the TTL to be seen, got a zero value")
			}
		})
	}
}

func TestClassifyIndex(t *testing.T) {
	spec := IndexSpec{
		Name:          indexUeId,
		Keys:          AscendingKeys(fieldUeId),
		PartialFilter: bson.M{fieldUeId: bson.M{opGt: ""}},
	}
	collatedId := indexSpecDoc{Name: idIndexName, Collation: mustMarshal(t, bson.M{"locale": "en"})}
	matching := indexSpecDoc{
		Name:                    spec.Name,
		Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
		PartialFilterExpression: mustMarshal(t, bson.M{fieldUeId: bson.M{opGt: ""}}),
	}

	cases := []struct {
		name          string
		wantConflicts []string
		existing      []indexSpecDoc
		wantMatch     bool
	}{
		{
			name:     "an empty collection has neither",
			existing: nil,
		},
		{
			name:      "an identical index is left alone",
			existing:  []indexSpecDoc{matching},
			wantMatch: true,
		},
		{
			name: "the _id index and unrelated indexes are ignored",
			existing: []indexSpecDoc{
				{Name: idIndexName, Key: bson.D{{Key: "_id", Value: int32(1)}}},
				{Name: "supi_1", Key: bson.D{{Key: fieldSupi, Value: int32(1)}}},
			},
		},
		{
			name: "the same name over different keys conflicts",
			existing: []indexSpecDoc{
				{Name: spec.Name, Key: bson.D{{Key: fieldSupi, Value: int32(1)}}},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			name: "the same key under a hand-made name conflicts, which is what a by-hand index leaves behind",
			existing: []indexSpecDoc{
				{Name: "ueId_1_auto", Key: bson.D{{Key: fieldUeId, Value: int32(1)}}},
			},
			wantConflicts: []string{"ueId_1_auto"},
		},
		{
			name: "the same name and key but no partial filter conflicts",
			existing: []indexSpecDoc{
				{Name: spec.Name, Key: bson.D{{Key: fieldUeId, Value: int32(1)}}},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			name: "a TTL index over the same key is never the index that was asked for",
			existing: []indexSpecDoc{
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
					ExpireAfterSeconds:      rawInt32(t, 0),
				},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			name: "a hidden index over the same key is never the index that was asked for",
			existing: []indexSpecDoc{
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
					Hidden:                  true,
				},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			// _id_ always carries the collection's default collation, so an
			// index that merely inherited it is still the index asked for.
			name: "a collation inherited from the collection is not a difference",
			existing: []indexSpecDoc{collatedId, {
				Name:                    spec.Name,
				Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
				PartialFilterExpression: matching.PartialFilterExpression,
				Collation:               collatedId.Collation,
			}},
			wantMatch: true,
		},
		{
			name: "a collation set deliberately is a different index",
			existing: []indexSpecDoc{
				{Name: idIndexName},
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
					Collation:               mustMarshal(t, bson.M{"locale": "fr"}),
				},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			// storageEngine can sit on an otherwise ordinary index, so unlike a
			// text or wildcard key it cannot be ruled out by the key alone.
			name: "an index carrying a storage engine option is never a match",
			existing: []indexSpecDoc{
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
					StorageEngine:           mustMarshal(t, bson.M{"wiredTiger": bson.M{"configString": ""}}),
				},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			name: "an index being prepared for uniqueness is never a match",
			existing: []indexSpecDoc{
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
					PrepareUnique:           true,
				},
			},
			wantConflicts: []string{spec.Name},
		},
		{
			name: "two indexes stand in the way and both are collected",
			existing: []indexSpecDoc{
				{Name: spec.Name, Key: bson.D{{Key: fieldSupi, Value: int32(1)}}},
				{Name: indexByHand, Key: bson.D{{Key: fieldUeId, Value: int32(1)}}},
			},
			wantConflicts: []string{spec.Name, indexByHand},
		},
		{
			name: "an index that matches can still be shadowed by one under another name",
			existing: []indexSpecDoc{
				matching,
				{Name: "shadow", Key: bson.D{{Key: fieldUeId, Value: int32(1)}}},
			},
			wantMatch:     true,
			wantConflicts: []string{"shadow"},
		},
		{
			name: "the same name and key but unique conflicts",
			existing: []indexSpecDoc{
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
					Unique:                  true,
				},
			},
			wantConflicts: []string{spec.Name},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := classifyIndex(spec, tc.existing)
			if state.matches != tc.wantMatch {
				t.Errorf("expected matches=%v, got %v", tc.wantMatch, state.matches)
			}
			gotConflicts := make([]string, 0, len(state.conflictsWith))
			for _, conflict := range state.conflictsWith {
				gotConflicts = append(gotConflicts, conflict.Name)
			}
			if len(gotConflicts) == 0 && len(tc.wantConflicts) == 0 {
				return
			}
			if !slices.Equal(gotConflicts, tc.wantConflicts) {
				t.Errorf("expected conflicts %v, got %v", tc.wantConflicts, gotConflicts)
			}
		})
	}
}

func TestPartialFilterComparisonIgnoresFieldOrder(t *testing.T) {
	// bson.M has no field order, so a filter that compares by its serialised
	// bytes would rebuild the index on roughly half of all startups.
	stored := mustMarshal(t, bson.D{
		{Key: "b", Value: bson.D{{Key: opGt, Value: 0}}},
		{Key: "a", Value: bson.D{{Key: "$exists", Value: true}}},
	})
	wanted := bson.M{
		"a": bson.M{"$exists": true},
		"b": bson.M{opGt: 0},
	}
	if canonicalRawDocument(stored) != canonicalWantedFilter(wanted) {
		t.Errorf("expected the same filter written in a different order to compare equal:\n stored %s\n wanted %s",
			canonicalRawDocument(stored), canonicalWantedFilter(wanted))
	}
}

func TestPartialFilterComparisonSeparatesDifferentFilters(t *testing.T) {
	// The AMF's two sentinels: ranId is compared against "" and amfUeNgapId
	// against 0. Confusing them would index the wrong subset.
	byRanId := canonicalWantedFilter(bson.M{fieldRanId: bson.M{opGt: ""}})
	byNgapId := canonicalWantedFilter(bson.M{fieldAmfUeNgapId: bson.M{opGt: 0}})
	if byRanId == byNgapId {
		t.Error("expected filters over different fields to differ")
	}

	absent := canonicalWantedFilter(nil)
	if absent == byRanId {
		t.Error("expected no filter to differ from a filter")
	}
	if absent != canonicalRawDocument(nil) {
		t.Error("expected an absent stored filter and an absent wanted filter to compare equal")
	}
}

func TestPartialFilterComparisonSeparatesAFieldNameThatLooksLikeTwoFields(t *testing.T) {
	// A MongoDB field name may legally contain the characters this rendering
	// uses as separators, so an unquoted key lets one field impersonate two and
	// two different filters compare equal.
	oneField := canonicalWantedFilter(bson.M{"a": 1})
	impersonating := strings.TrimSuffix(strings.TrimPrefix(oneField, "{"), "}") + ",b"

	if canonicalWantedFilter(bson.M{impersonating: 2}) == canonicalWantedFilter(bson.M{"a": 1, "b": 2}) {
		t.Errorf("expected a field named %q to render differently from two fields a and b", impersonating)
	}
}

func TestPartialFilterComparisonIsTheSameEveryTime(t *testing.T) {
	// The comparison decides whether an index is rebuilt, so it has to answer
	// the same way twice. A Go map has no field order, and bson.Marshal walks
	// it in a different order each call, so anything left unsorted here would
	// rebuild the index on a random subset of startups.
	filters := []bson.M{
		{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5},
		{"x": bson.M{opGt: 0, opLt: 9, "$ne": 4}},
		{opAnd: bson.A{bson.M{"a": 1, "b": 2}, bson.M{"c": 3, "d": 4}}},
	}
	for _, filter := range filters {
		first := canonicalWantedFilter(filter)
		for range 50 {
			if again := canonicalWantedFilter(filter); again != first {
				t.Fatalf("expected a stable rendering of %v, got %q then %q", filter, first, again)
			}
		}
	}
}

func TestPartialFilterComparisonRefusesWhatItCannotCompare(t *testing.T) {
	// An embedded literal of more than one field is matched exactly by MongoDB,
	// field order included, and a Go map cannot express an order — so the spec
	// is refused rather than compared two different ways.
	cases := map[string]bson.M{
		"addr":       {"addr": bson.M{"city": "X", "zip": "Y"}},
		"outer.x":    {"outer": bson.M{"x": bson.M{"a": 1, "b": 2}}},
		opAnd:        {opAnd: bson.A{bson.M{"x": bson.M{"a": 1, "b": 2}}}},
		"$in inside": {"x": bson.M{opIn: bson.A{bson.M{"a": 1, "b": 2}}}},
	}
	for name, filter := range cases {
		t.Run(name, func(t *testing.T) {
			spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId), PartialFilter: filter}
			err := spec.validate()
			if err == nil {
				t.Fatalf("expected %v to be refused", filter)
			}
			if !strings.Contains(err.Error(), "more than one field") {
				t.Errorf("expected the error to explain the ambiguity, got %q", err.Error())
			}
		})
	}

	// A single-field literal has no order to disagree about, and a filter of
	// conditions is order-free however many it has.
	allowed := []bson.M{
		{"x": bson.M{"a": 1}},
		{"a": 1, "b": 2},
		{"x": bson.M{opGt: 0, opLt: 9}},
		{opAnd: bson.A{bson.M{"a": 1}, bson.M{"b": 2}}},
	}
	for _, filter := range allowed {
		spec := IndexSpec{Name: indexUeId, Keys: AscendingKeys(fieldUeId), PartialFilter: filter}
		if err := spec.validate(); err != nil {
			t.Errorf("expected %v to be accepted, got %v", filter, err)
		}
	}
}

func TestPartialFilterComparisonKeepsArrayOrder(t *testing.T) {
	first := canonicalWantedFilter(bson.M{"nfType": bson.M{opIn: bson.A{"AMF", "SMF"}}})
	second := canonicalWantedFilter(bson.M{"nfType": bson.M{opIn: bson.A{"SMF", "AMF"}}})
	if first == second {
		t.Error("expected array order to be significant in a predicate")
	}
}

func TestIsIndexKeySpecsConflict(t *testing.T) {
	if !IsIndexKeySpecsConflict(mongo.CommandError{Code: indexKeySpecsConflictErrorCode}) {
		t.Error("expected code 86 to be recognised as an index key specs conflict")
	}
	if IsIndexKeySpecsConflict(mongo.CommandError{Code: indexOptionsConflictErrorCode}) {
		t.Error("expected code 85 not to be reported as an index key specs conflict")
	}
	if IsIndexKeySpecsConflict(nil) {
		t.Error("expected a nil error not to be a conflict")
	}
}

func TestEnsureIndexValidatesBeforeReachingTheServer(t *testing.T) {
	client := newUnreachableClient(t)
	err := client.EnsureIndex(canceledContext(t), "amf.data.amfState", IndexSpec{Keys: AscendingKeys(fieldSupi)})
	if err == nil {
		t.Fatal("expected an invalid spec to be rejected")
	}
	if !strings.Contains(err.Error(), "index name is required") {
		t.Errorf("expected the validation error rather than a transport error, got %q", err.Error())
	}
}

func TestEnsureIndexReportsWhyItCouldNotList(t *testing.T) {
	client := newUnreachableClient(t)
	spec := IndexSpec{Name: "supi_1", Keys: AscendingKeys(fieldSupi)}
	err := client.EnsureIndex(canceledContext(t), "amf.data.amfState", spec)
	if err == nil {
		t.Fatal("expected an error when MongoDB cannot be reached")
	}
	if !strings.Contains(err.Error(), "list indexes") {
		t.Errorf("expected the error to say the listing failed, got %q", err.Error())
	}
}
