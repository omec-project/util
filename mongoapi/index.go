// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// idIndexName is the index MongoDB maintains on _id. It cannot be dropped, so
// it is never a candidate for repair.
const idIndexName = "_id_"

// IndexSpec describes an index a caller wants a collection to have.
//
// Every property of the index is stated here rather than inferred, the name
// included: the name is the one handle that survives a change to the index
// itself. When a later version alters the key or the options, the name is what
// lets it find and replace what the previous version made — and two versions
// that both let the server name the index collide on IndexOptionsConflict
// instead of replacing each other.
//
// Fields are ordered to keep the struct's pointer data compact, not by
// importance: Name and Keys are the two a caller always sets.
type IndexSpec struct {
	// PartialFilter indexes only the documents it matches. Reach for it when
	// the records a query never seeks carry a placeholder *value* rather than
	// omitting the field — Sparse cannot help there, and without a filter every
	// such record sits in one large bucket of the index that nothing ever
	// seeks. Mutually exclusive with Sparse.
	PartialFilter bson.M

	// Name identifies the index, and is required.
	Name string

	// Keys are the indexed fields in order, each with direction 1 or -1, and
	// at least one is required. Order matters: an index over (a, b) serves a
	// query on a alone, but nothing serves a query on b alone.
	Keys bson.D

	// Unique rejects a second document carrying the same key. Leave it false
	// unless the constraint is wanted for its own sake: an index needed only so
	// that a lookup seeks does not need it, and asserting a uniqueness the data
	// does not have turns a harmless duplicate into a failed write.
	Unique bool

	// Sparse omits documents that do not carry the indexed field. Use it where
	// the field is absent from records a query never seeks: a unique index over
	// an absent field otherwise reads every such document as null and collides
	// on the second one.
	Sparse bool
}

// AscendingKeys builds the Keys of an index whose fields are all ascending,
// which most are.
func AscendingKeys(fields ...string) bson.D {
	keys := make(bson.D, 0, len(fields))
	for _, field := range fields {
		keys = append(keys, bson.E{Key: field, Value: 1})
	}
	return keys
}

func (s IndexSpec) validate() error {
	if s.Name == "" {
		return errors.New("index name is required")
	}
	if s.Name == idIndexName {
		return fmt.Errorf("index %q is maintained by MongoDB and cannot be managed here", s.Name)
	}
	if len(s.Keys) == 0 {
		return fmt.Errorf("index %q has no keys", s.Name)
	}
	for _, key := range s.Keys {
		if key.Key == "" {
			return fmt.Errorf("index %q has a key with an empty field name", s.Name)
		}
		if direction, ok := indexDirection(key.Value); !ok || (direction != 1 && direction != -1) {
			return fmt.Errorf("index %q: field %q has direction %v, want 1 or -1", s.Name, key.Key, key.Value)
		}
	}
	if s.Sparse && s.PartialFilter != nil {
		return fmt.Errorf("index %q sets both Sparse and PartialFilter, which MongoDB rejects", s.Name)
	}
	if s.PartialFilter != nil {
		// Checked here, before EnsureIndex drops anything. A filter that cannot
		// be marshalled fails at creation, and by then the index it was meant to
		// replace is already gone — leaving the collection with neither.
		if _, err := bson.Marshal(s.PartialFilter); err != nil {
			return fmt.Errorf("index %q has a partial filter that cannot be encoded: %w", s.Name, err)
		}
	}
	return nil
}

func (s IndexSpec) model() mongo.IndexModel {
	opts := options.Index().SetName(s.Name).SetUnique(s.Unique)
	if s.Sparse {
		opts = opts.SetSparse(true)
	}
	if s.PartialFilter != nil {
		opts = opts.SetPartialFilterExpression(s.PartialFilter)
	}
	return mongo.IndexModel{Keys: s.Keys, Options: opts}
}

// EnsureIndex makes a collection carry the index described by spec, and reports
// success only once that index has been observed through listIndexes — never on
// the strength of the create call alone, which cannot distinguish "created"
// from "refused" for a caller that only checks for a nil error.
//
// It is idempotent: an index that already matches spec is left as it is. An
// index occupying spec's name, or spec's key pattern under a different name, is
// dropped and recreated — so an index made by hand, or by an earlier version of
// the caller, is repaired rather than silently kept in place.
//
// One key pattern in one collection must have a single owner. Two callers that
// ask for different indexes over the same key under different names will each
// replace the other's on every startup, and both will report success.
//
// An index carrying an option IndexSpec cannot express — a TTL, or hidden — is
// never treated as a match. A TTL is refused rather than dropped, since
// removing one silently stops the collection expiring documents. A collation is
// not compared at all: MongoDB stamps a collection's default collation onto
// every index built on it, so treating that as a difference would rebuild the
// index for ever.
//
// It makes one attempt. A caller that starts alongside MongoDB should retry it
// with backoff: creating an index is a write, so it can fail while the replica
// set has no writable primary, and ordinary writes succeeding later does not
// undo that.
func (c *MongoClient) EnsureIndex(ctx context.Context, collName string, spec IndexSpec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	collection := c.Client.Database(c.dbName).Collection(collName)

	existing, err := listIndexSpecs(ctx, collection)
	if err != nil {
		return fmt.Errorf("list indexes of collection %s failed: %w", collName, err)
	}
	state := classifyIndex(spec, existing)
	if state.matches && len(state.conflictsWith) == 0 {
		return nil
	}

	// Every refusal is decided before any index is dropped. Deciding them one
	// conflict at a time would drop the first and refuse on the second, leaving
	// the collection without either — the same way an unencodable partial
	// filter used to, which is why validate now runs first too.
	for _, conflict := range state.conflictsWith {
		if conflict.Name == idIndexName {
			return fmt.Errorf("index %q of collection %s conflicts with the _id index, which cannot be dropped",
				spec.Name, collName)
		}
		// A TTL index is never an earlier version of the caller's index, and
		// dropping one stops the collection expiring documents with no sign
		// that anything changed. Refuse instead, and let whoever wants the key
		// back take it deliberately.
		if conflict.ExpireAfterSeconds != nil {
			return fmt.Errorf("index %q of collection %s covers the same key as TTL index %q; "+
				"drop that index deliberately if the collection should stop expiring documents",
				spec.Name, collName, conflict.Name)
		}
	}

	for _, conflict := range state.conflictsWith {
		// A drop that finds nothing lost a race with another caller doing the
		// same repair, which is the outcome this wanted anyway.
		if dropErr := collection.Indexes().DropOne(ctx, conflict.Name); dropErr != nil && !IsIndexNotFound(dropErr) {
			return fmt.Errorf("drop index %q of collection %s, which conflicts with %q, failed: %w",
				conflict.Name, collName, spec.Name, dropErr)
		}
	}

	if !state.matches {
		if _, err = collection.Indexes().CreateOne(ctx, spec.model()); err != nil {
			// A conflict here appeared between the listing and the create —
			// another process starting at the same moment. The next attempt
			// lists the collection again and repairs what it finds, so the
			// error is reported rather than worked around here.
			if IsIndexOptionsConflict(err) || IsIndexKeySpecsConflict(err) {
				return fmt.Errorf("create index %q on collection %s conflicts with an index created concurrently, retry: %w",
					spec.Name, collName, err)
			}
			return fmt.Errorf("create index %q on collection %s failed: %w", spec.Name, collName, err)
		}
	}

	if existing, err = listIndexSpecs(ctx, collection); err != nil {
		return fmt.Errorf("verify index %q of collection %s failed: %w", spec.Name, collName, err)
	}
	if final := classifyIndex(spec, existing); !final.matches || len(final.conflictsWith) > 0 {
		return fmt.Errorf("index %q of collection %s is absent, differs from its specification, or is shadowed after creation",
			spec.Name, collName)
	}
	return nil
}

// IsIndexKeySpecsConflict reports whether err is the server refusing to create
// an index because one of the same name already exists over different keys
// (code 86).
func IsIndexKeySpecsConflict(err error) bool {
	var serverErr mongo.ServerError
	return errors.As(err, &serverErr) && serverErr.HasErrorCode(indexKeySpecsConflictErrorCode)
}

// indexSpecDoc is one entry of the listIndexes reply. Its key is decoded as a
// bson.D because the order of a compound key is part of the index's identity
// and a map would lose it.
type indexSpecDoc struct {
	// ExpireAfterSeconds and Hidden are read but never written: IndexSpec
	// cannot express either, so an existing index carrying one is not the index
	// the caller asked for, however well the rest of it matches. A TTL index
	// deletes the caller's documents; reporting it as a plain index on the same
	// key would be the worst answer this package could give.
	//
	// A collation is deliberately not read. MongoDB stamps a collection's
	// default collation onto every index built on it, so an index this package
	// created on such a collection comes back carrying one — and treating that
	// as a difference would drop and rebuild the index on every startup, for
	// ever. The cost of leaving it out is that an index whose collation was set
	// deliberately reads as a match.
	ExpireAfterSeconds      *int32   `bson:"expireAfterSeconds"`
	Name                    string   `bson:"name"`
	Key                     bson.D   `bson:"key"`
	PartialFilterExpression bson.Raw `bson:"partialFilterExpression"`
	Unique                  bool     `bson:"unique"`
	Sparse                  bool     `bson:"sparse"`
	Hidden                  bool     `bson:"hidden"`
}

func listIndexSpecs(ctx context.Context, collection *mongo.Collection) ([]indexSpecDoc, error) {
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	var specs []indexSpecDoc
	if err = cursor.All(ctx, &specs); err != nil {
		return nil, err
	}
	return specs, nil
}

// indexState says what a collection's existing indexes mean for a spec: either
// one of them already is that index, or some of them stand in its way, or
// neither and the index is simply absent.
type indexState struct {
	conflictsWith []indexSpecDoc
	matches       bool
}

// classifyIndex looks for existing indexes that either are spec or stand in its
// way. An index stands in the way when it takes spec's name, which MongoDB
// refuses outright, or when it covers spec's key pattern under a different
// name.
//
// The second is a policy rather than a server rule: MongoDB will keep a
// partial, unique, sparse or collated index alongside a plain one over the same
// key, refusing only an equivalent one under a second name. (A TTL index is the
// exception — the server refuses that beside a plain index with code 85.)
// Taking the key pattern over is what lets an index made by hand, or by an
// earlier version of the caller, be replaced rather than accumulated beside its
// successor.
//
// Every conflict is collected, not just the first: a collection can hold one
// index under spec's name and another over spec's key, and dropping one per
// call would need as many startups as there are conflicts to converge.
func classifyIndex(spec IndexSpec, existing []indexSpecDoc) indexState {
	wantKey := canonicalKey(spec.Keys)
	state := indexState{}
	for _, candidate := range existing {
		sameName := candidate.Name == spec.Name
		sameKey := canonicalKey(candidate.Key) == wantKey
		if !sameName && !sameKey {
			continue
		}
		if sameName && sameKey && candidate.sameOptionsAs(spec) {
			state.matches = true
			continue
		}
		state.conflictsWith = append(state.conflictsWith, candidate)
	}
	return state
}

func (e indexSpecDoc) sameOptionsAs(spec IndexSpec) bool {
	if e.ExpireAfterSeconds != nil || e.Hidden {
		return false
	}
	if e.Unique != spec.Unique || e.Sparse != spec.Sparse {
		return false
	}
	return canonicalFilter(e.PartialFilterExpression) == canonicalWantedFilter(spec.PartialFilter)
}

// canonicalKey renders a key pattern so that two patterns compare equal exactly
// when MongoDB would treat them as the same index: same fields, same
// directions, same order.
func canonicalKey(keys bson.D) string {
	var out strings.Builder
	for _, key := range keys {
		direction, ok := indexDirection(key.Value)
		if !ok {
			// A key MongoDB reports in a form we do not model — a text or
			// hashed index, say. The type is rendered with the value, because
			// "1" and 1 are different keys and printing the value alone would
			// make the string compare equal to a real ascending direction.
			fmt.Fprintf(&out, "%q:%T(%v);", key.Key, key.Value, key.Value)
			continue
		}
		fmt.Fprintf(&out, "%q:%d;", key.Key, direction)
	}
	return out.String()
}

// indexDirection reads a key's direction, which listIndexes and callers between
// them express as any of Go's numeric types. A value that does not survive the
// conversion exactly is reported as not a direction at all, so validate rejects
// it and canonicalKey renders it verbatim rather than quietly rounding it into
// a direction it never had.
func indexDirection(value any) (int32, bool) {
	switch typed := value.(type) {
	case int:
		return exactInt32(int64(typed))
	case int32:
		return typed, true
	case int64:
		return exactInt32(typed)
	case float64:
		direction := int32(typed)
		if float64(direction) != typed {
			return 0, false
		}
		return direction, true
	default:
		return 0, false
	}
}

func exactInt32(value int64) (int32, bool) {
	direction := int32(value)
	if int64(direction) != value {
		return 0, false
	}
	return direction, true
}

// canonicalWantedFilter renders the filter a caller asked for. bson.M has no
// field order, and neither does the stored filter once it is compared this way,
// so both sides are rendered with their keys sorted.
func canonicalWantedFilter(filter bson.M) string {
	if filter == nil {
		return ""
	}
	raw, err := bson.Marshal(filter)
	if err != nil {
		// A filter that cannot be marshalled will fail at creation with a
		// better message than anything this comparison could produce.
		return fmt.Sprintf("unmarshallable:%v", filter)
	}
	return canonicalFilter(raw)
}

func canonicalFilter(raw bson.Raw) string {
	if len(raw) == 0 {
		return ""
	}
	return canonicalDocument(raw)
}

func canonicalDocument(raw bson.Raw) string {
	elements, err := raw.Elements()
	if err != nil {
		return fmt.Sprintf("undecodable:%q", raw.String())
	}
	rendered := make([]string, 0, len(elements))
	for _, element := range elements {
		// The key is quoted for the same reason canonicalKey quotes one: a
		// MongoDB field name may legally contain the separators used here, and
		// an unquoted "a={...},b" renders exactly as two fields a and b do.
		rendered = append(rendered, fmt.Sprintf("%q=%s", element.Key(), canonicalValue(element.Value())))
	}
	slices.Sort(rendered)
	return "{" + strings.Join(rendered, ",") + "}"
}

func canonicalValue(value bson.RawValue) string {
	switch value.Type {
	case bson.TypeEmbeddedDocument:
		return canonicalDocument(value.Document())
	case bson.TypeArray:
		values, err := value.Array().Values()
		if err != nil {
			return fmt.Sprintf("undecodable:%q", value.String())
		}
		rendered := make([]string, 0, len(values))
		for _, item := range values {
			rendered = append(rendered, canonicalValue(item))
		}
		// Array order is significant in a query predicate, so it is preserved.
		return "[" + strings.Join(rendered, ",") + "]"
	default:
		return value.String()
	}
}
