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
	opGt             = "$gt"

	// errBadDirection is the validation message several key shapes share.
	errBadDirection = "want 1 or -1"
)

func ptrTo[T any](value T) *T {
	return &value
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
			spec:    IndexSpec{Name: "texty", Keys: bson.D{{Key: fieldUeId, Value: "text"}}},
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
	if text := canonicalKey(bson.D{{Key: fieldUeId, Value: "text"}}); text == asDirection {
		t.Error("expected a text index key to render differently from an ascending key")
	}
}

func TestClassifyIndex(t *testing.T) {
	spec := IndexSpec{
		Name:          indexUeId,
		Keys:          AscendingKeys(fieldUeId),
		PartialFilter: bson.M{fieldUeId: bson.M{opGt: ""}},
	}
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
					ExpireAfterSeconds:      ptrTo(int32(0)),
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
			// MongoDB stamps a collection's default collation onto every index
			// built on it, so an index this package created comes back carrying
			// one. Treating that as a difference would rebuild it for ever.
			name: "a collation does not make an otherwise identical index a conflict",
			existing: []indexSpecDoc{
				{
					Name:                    spec.Name,
					Key:                     bson.D{{Key: fieldUeId, Value: int32(1)}},
					PartialFilterExpression: matching.PartialFilterExpression,
				},
			},
			wantMatch: true,
		},
		{
			name: "two indexes stand in the way and both are collected",
			existing: []indexSpecDoc{
				{Name: spec.Name, Key: bson.D{{Key: fieldSupi, Value: int32(1)}}},
				{Name: "by_hand", Key: bson.D{{Key: fieldUeId, Value: int32(1)}}},
			},
			wantConflicts: []string{spec.Name, "by_hand"},
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
	if canonicalFilter(stored) != canonicalWantedFilter(wanted) {
		t.Errorf("expected the same filter written in a different order to compare equal:\n stored %s\n wanted %s",
			canonicalFilter(stored), canonicalWantedFilter(wanted))
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
	if absent != canonicalFilter(nil) {
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

func TestPartialFilterComparisonKeepsArrayOrder(t *testing.T) {
	first := canonicalWantedFilter(bson.M{"nfType": bson.M{"$in": bson.A{"AMF", "SMF"}}})
	second := canonicalWantedFilter(bson.M{"nfType": bson.M{"$in": bson.A{"SMF", "AMF"}}})
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
