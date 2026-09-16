// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// idIndexName is the index MongoDB maintains on _id. It cannot be dropped, so
// it is never a candidate for repair.
const idIndexName = "_id_"

// wildcardKeySuffix marks a wildcard index key, such as "$**" or "a.b.$**".
const wildcardKeySuffix = "$**"

// restoreTimeout bounds putting dropped indexes back. It is deliberately short:
// the restore runs on a context detached from the caller's, so nothing else
// would stop it.
const restoreTimeout = 30 * time.Second

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
		// A wildcard index carries a wildcardProjection that IndexSpec cannot
		// describe, so managing one here would mean reporting an index as
		// matching a specification that never mentioned its projection.
		if strings.Contains(key.Key, wildcardKeySuffix) {
			return fmt.Errorf("index %q: field %q is a wildcard key, which IndexSpec cannot describe", s.Name, key.Key)
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
		raw, err := bson.Marshal(s.PartialFilter)
		if err != nil {
			return fmt.Errorf("index %q has a partial filter that cannot be encoded: %w", s.Name, err)
		}
		if path := orderSensitiveField(raw, true); path != "" {
			return fmt.Errorf("index %q: partial filter field %q holds an embedded document with more "+
				"than one field. MongoDB matches such a document exactly, field order included, and a "+
				"Go map has no field order to match against — so whether this index needs rebuilding "+
				"could not be answered the same way twice", s.Name, path)
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
// compared against the collection's own, which MongoDB stamps onto every index
// built on it and which _id_ therefore always carries: one that merely equals
// the default is still the index asked for, one set deliberately is not. A
// wildcard key is refused outright by validate, because its projection is
// another option IndexSpec cannot describe.
//
// If a call fails after it has dropped indexes to make room — creation refused
// because a Unique spec meets data that already holds duplicates, or a later
// drop in the same call failing — it attempts to put the dropped ones back
// before returning, on a context detached from the caller's, so a failed call
// does not leave the collection with neither the old index nor the new.
//
// That attempt is best effort, not a guarantee, and it cannot be otherwise:
// MongoDB has no atomic index replacement, so between the drop and the restore
// the collection is without that index. If it was unique, writes admitted in
// that window can be the very reason the restore then fails. A restore that
// fails is reported alongside the original error rather than hidden, but a
// caller for which that window matters has to keep writes off the collection
// itself.
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
		if !conflict.ExpireAfterSeconds.IsZero() {
			// A conflict is either the same name or the same key, and the
			// message has to say which — sending an operator to look for a key
			// collision that does not exist wastes the one thing this error is
			// for.
			collision := "has the same name as"
			if canonicalKey(conflict.Key) == canonicalKey(spec.Keys) {
				collision = "covers the same key as"
			}
			return fmt.Errorf("index %q of collection %s %s TTL index %q; "+
				"drop that index deliberately if the collection should stop expiring documents",
				spec.Name, collName, collision, conflict.Name)
		}
	}

	var dropped []indexSpecDoc
	for _, conflict := range state.conflictsWith {
		// A drop that finds nothing lost a race with another caller doing the
		// same repair, which is the outcome this wanted anyway.
		if dropErr := collection.Indexes().DropOne(ctx, conflict.Name); dropErr != nil && !IsIndexNotFound(dropErr) {
			dropErr = fmt.Errorf("drop index %q of collection %s, which conflicts with %q, failed: %w",
				conflict.Name, collName, spec.Name, dropErr)
			// The conflicts removed before this one go back, and so does this
			// one: a drop that reports an error may still have been applied
			// before the client stopped waiting for the answer. Recreating an
			// index that is in fact still there is a no-op, so covering the
			// ambiguous case costs nothing and leaving it uncovered risks the
			// index this call was asked to protect.
			return joinRestore(ctx, collection, collName, append(dropped, conflict), dropErr)
		}
		dropped = append(dropped, conflict)
	}

	if !state.matches {
		if _, err = collection.Indexes().CreateOne(ctx, spec.model()); err != nil {
			// A conflict here appeared between the listing and the create —
			// another process starting at the same moment. The next attempt
			// lists the collection again and repairs what it finds, so the
			// error is reported rather than worked around here.
			if IsIndexOptionsConflict(err) || IsIndexKeySpecsConflict(err) {
				err = fmt.Errorf("create index %q on collection %s conflicts with an index created concurrently, retry: %w",
					spec.Name, collName, err)
			} else {
				err = fmt.Errorf("create index %q on collection %s failed: %w", spec.Name, collName, err)
			}
			// The indexes this call dropped were the collection's only cover for
			// those keys. Creation can fail for reasons the specification alone
			// cannot rule out -- asking for Unique over data that already holds
			// duplicates is the plain one -- so put them back rather than leave
			// the collection with neither the old index nor the new.
			return joinRestore(ctx, collection, collName, dropped, err)
		}
	}

	if existing, err = listIndexSpecs(ctx, collection); err != nil {
		err = fmt.Errorf("verify index %q of collection %s failed: %w", spec.Name, collName, err)
		// The collection cannot be read, so whether the key is covered is
		// unknown. A call that created an index almost certainly left one
		// behind; a call that created nothing was relying on an index it can no
		// longer see, so what it dropped in that index's favour goes back.
		if state.matches {
			return joinRestore(ctx, collection, collName, dropped, err)
		}
		return err
	}

	final := classifyIndex(spec, existing)
	if final.matches && len(final.conflictsWith) == 0 {
		return nil
	}
	err = fmt.Errorf("index %q of collection %s is absent, differs from its specification, or is shadowed after creation",
		spec.Name, collName)
	// What decides the restore is whether the index this call was for is there
	// now, not whether this call is what put it there. It can be missing on
	// either branch: another writer can remove one this call created just as
	// easily as one it found. If it is present and merely shadowed, putting its
	// predecessors back would add to the shadowing rather than repair it.
	if final.matches {
		return err
	}
	return joinRestore(ctx, collection, collName, dropped, err)
}

// joinRestore returns cause, having first tried to put back the indexes this
// call had already dropped. A restore that itself fails is reported alongside
// the cause rather than in place of it, because the cause is what the caller
// has to act on.
func joinRestore(ctx context.Context, collection *mongo.Collection, collName string,
	dropped []indexSpecDoc, cause error,
) error {
	if restoreErr := restoreIndexes(ctx, collection, dropped); restoreErr != nil {
		return errors.Join(cause, fmt.Errorf("restoring the indexes of collection %s that were dropped first: %w",
			collName, restoreErr))
	}
	return cause
}

// restoreIndexes puts back indexes that were dropped to make room for one that
// could not then be created.
//
// It runs on a context detached from the caller's, with its own short deadline.
// The reason a drop or a create failed is often the caller's context itself — a
// deadline reached, or the process told to stop — and that is precisely when
// the collection must not be left uncovered.
//
// Each index is recreated from the specification listIndexes reported, verbatim,
// so an option IndexSpec does not model — a collation, a storage engine, a text
// index's weights — comes back with it. A conflict is reached by name as well as
// by key, so the index being put back can be of a shape this package could never
// have created, and rebuilding one from the fields it understands would silently
// drop the rest or fail outright. A TTL index is never among these, because
// EnsureIndex refuses rather than drops one.
func restoreIndexes(ctx context.Context, collection *mongo.Collection, dropped []indexSpecDoc) error {
	if len(dropped) == 0 {
		return nil
	}
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()

	var failures []error
	for _, doc := range dropped {
		if len(doc.raw) == 0 {
			failures = append(failures, fmt.Errorf("index %q: no specification was recorded to restore it from", doc.Name))
			continue
		}
		command := bson.D{
			{Key: "createIndexes", Value: collection.Name()},
			{Key: "indexes", Value: bson.A{doc.raw}},
		}
		if err := collection.Database().RunCommand(restoreCtx, command).Err(); err != nil {
			failures = append(failures, fmt.Errorf("index %q: %w", doc.Name, err))
		}
	}
	return errors.Join(failures...)
}

// IsIndexKeySpecsConflict reports whether err is the server refusing to create
// an index because one of the requested name already exists and differs from
// the requested one (code 86).
func IsIndexKeySpecsConflict(err error) bool {
	var serverErr mongo.ServerError
	return errors.As(err, &serverErr) && serverErr.HasErrorCode(indexKeySpecsConflictErrorCode)
}

// indexSpecDoc is one entry of the listIndexes reply. Its key is decoded as a
// bson.D because the order of a compound key is part of the index's identity
// and a map would lose it.
type indexSpecDoc struct {
	// ExpireAfterSeconds, Hidden, StorageEngine and PrepareUnique are read but
	// never written: IndexSpec cannot express any of them, so an existing index
	// carrying one is not the index the caller asked for, however well the rest
	// of it matches. A TTL index deletes the caller's documents; reporting it as
	// a plain index on the same key would be the worst answer this package could
	// give.
	//
	// StorageEngine is the same: it can sit on an otherwise ordinary index, so
	// unlike a text or wildcard key it cannot be ruled out by the key alone.
	//
	// Collation is read for a different purpose: it is compared against the
	// collection's own, which _id_ always carries, so that an index which
	// merely inherited the default is not mistaken for one whose collation was
	// set deliberately. See collectionDefaultCollation and sameOptionsAs.
	//
	// raw is the entry exactly as listIndexes reported it, which is what an
	// index is restored from. Rebuilding one field by field can only carry the
	// fields this struct models, and an index reached by name can be of any
	// shape at all — a text index rebuilt without its weights is not merely
	// degraded, it cannot be created.
	raw bson.Raw
	// Decoded as a raw value rather than a number: only its presence matters,
	// and a TTL written by an older server or by hand can be any numeric type
	// or outside int32. Decoding it as an int32 would fail the whole listing,
	// so an unrelated index on the same collection could never be ensured.
	ExpireAfterSeconds      bson.RawValue `bson:"expireAfterSeconds"`
	Name                    string        `bson:"name"`
	Key                     bson.D        `bson:"key"`
	PartialFilterExpression bson.Raw      `bson:"partialFilterExpression"`
	StorageEngine           bson.Raw      `bson:"storageEngine"`
	Collation               bson.Raw      `bson:"collation"`
	Unique                  bool          `bson:"unique"`
	Sparse                  bool          `bson:"sparse"`
	Hidden                  bool          `bson:"hidden"`
	PrepareUnique           bool          `bson:"prepareUnique"`
}

func listIndexSpecs(ctx context.Context, collection *mongo.Collection) ([]indexSpecDoc, error) {
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	// Decoded twice on purpose: once into the fields this package reasons
	// about, and once kept whole, because restoring an index means handing the
	// server back what it gave us rather than what we understood of it.
	var entries []bson.Raw
	if err = cursor.All(ctx, &entries); err != nil {
		return nil, err
	}
	specs := make([]indexSpecDoc, 0, len(entries))
	for _, entry := range entries {
		var doc indexSpecDoc
		if err = bson.Unmarshal(entry, &doc); err != nil {
			return nil, err
		}
		doc.raw = entry
		specs = append(specs, doc)
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
	defaultCollation := collectionDefaultCollation(existing)
	state := indexState{}
	for _, candidate := range existing {
		sameName := candidate.Name == spec.Name
		sameKey := canonicalKey(candidate.Key) == wantKey
		if !sameName && !sameKey {
			continue
		}
		if sameName && sameKey && candidate.sameOptionsAs(spec, defaultCollation) {
			state.matches = true
			continue
		}
		state.conflictsWith = append(state.conflictsWith, candidate)
	}
	return state
}

// sameOptionsAs compares an existing index with a specification. defaultCollation
// is the collection's own, taken from the _id_ index, which always carries it:
// an index that merely inherited it is the index the caller asked for, while one
// whose collation was set deliberately is a different index and must be repaired.
// collectionDefaultCollation reads the collation MongoDB stamps onto every index
// of a collection, which the _id_ index always carries, and which is absent when
// the collection has none.
func collectionDefaultCollation(existing []indexSpecDoc) bson.Raw {
	for _, candidate := range existing {
		if candidate.Name == idIndexName {
			return candidate.Collation
		}
	}
	return nil
}

func (e indexSpecDoc) sameOptionsAs(spec IndexSpec, defaultCollation bson.Raw) bool {
	if canonicalRawDocument(e.Collation) != canonicalRawDocument(defaultCollation) {
		return false
	}
	if !e.ExpireAfterSeconds.IsZero() || e.Hidden || e.PrepareUnique || len(e.StorageEngine) > 0 {
		return false
	}
	if e.Unique != spec.Unique || e.Sparse != spec.Sparse {
		return false
	}
	return canonicalRawDocument(e.PartialFilterExpression) == canonicalWantedFilter(spec.PartialFilter)
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

// indexDirection reads a key's direction. listIndexes reports one as int32, and
// a caller may write any numeric type the driver encodes as a BSON number, so
// all of them are read. A value that does not survive the conversion exactly is
// reported as not a direction at all, so validate rejects it and canonicalKey
// renders it verbatim rather than quietly rounding it into a direction it never
// had.
func indexDirection(value any) (int32, bool) {
	switch typed := value.(type) {
	case int:
		return exactInt32(int64(typed))
	case int8:
		return int32(typed), true
	case int16:
		return int32(typed), true
	case int32:
		return typed, true
	case int64:
		return exactInt32(typed)
	case uint:
		return exactInt32FromUint(uint64(typed))
	case uint8:
		return int32(typed), true
	case uint16:
		return int32(typed), true
	case uint32:
		return exactInt32FromUint(uint64(typed))
	case uint64:
		return exactInt32FromUint(typed)
	case float32:
		return exactInt32FromFloat(float64(typed))
	case float64:
		return exactInt32FromFloat(typed)
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

func exactInt32FromUint(value uint64) (int32, bool) {
	if value > math.MaxInt32 {
		return 0, false
	}
	return int32(value), true
}

func exactInt32FromFloat(value float64) (int32, bool) {
	if value != math.Trunc(value) || value < math.MinInt32 || value > math.MaxInt32 {
		return 0, false
	}
	return int32(value), true
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
	return canonicalRawDocument(raw)
}

func canonicalRawDocument(raw bson.Raw) string {
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
	// Sorting is safe because validate refuses the one shape whose order
	// carries meaning: an embedded literal with more than one field.
	slices.Sort(rendered)
	return "{" + strings.Join(rendered, ",") + "}"
}

// orderSensitiveField finds a field whose value is an embedded literal of more
// than one field, and returns its path. Such a document is matched exactly by
// MongoDB, field order included, so two of them differing only in order are
// different filters — and a Go map, which is how a partial filter is written,
// has no order to compare. Sorting one would call two different filters equal;
// not sorting one makes the comparison depend on Go's map iteration, which is
// randomised per call. Neither is a comparison, so such a filter is refused.
//
// isFilter says whether this document is a set of conditions, whose own field
// order carries no meaning, as opposed to a literal being matched.
//
// Completeness rests on MongoDB's own restriction of what a partial filter may
// contain — equality, $exists, $type, the range operators and a top-level $and,
// but no $or, $nor, $elemMatch or nested arrays. If that restriction is ever
// relaxed, this is the function to re-check against it.
func orderSensitiveField(raw bson.Raw, isFilter bool) string {
	elements, err := raw.Elements()
	if err != nil {
		return ""
	}
	if !isFilter && len(elements) > 1 && !operatorDocument(raw) {
		return "."
	}
	for _, element := range elements {
		switch value := element.Value(); value.Type {
		case bson.TypeEmbeddedDocument:
			// A condition's value is an operator document or a literal; either
			// way it is not a filter in its own right.
			if nested := orderSensitiveField(value.Document(), false); nested != "" {
				return joinFieldPath(element.Key(), nested)
			}
		case bson.TypeArray:
			// Only the logical operators take filters as array elements.
			elementsAreFilters := element.Key() == "$and" || element.Key() == "$or" || element.Key() == "$nor"
			values, valuesErr := value.Array().Values()
			if valuesErr != nil {
				continue
			}
			for _, item := range values {
				if item.Type != bson.TypeEmbeddedDocument {
					continue
				}
				if nested := orderSensitiveField(item.Document(), elementsAreFilters); nested != "" {
					return joinFieldPath(element.Key(), nested)
				}
			}
		}
	}
	return ""
}

func joinFieldPath(key, nested string) string {
	if nested == "." {
		return key
	}
	return key + "." + nested
}

// operatorDocument reports whether every key of a document is an operator, in
// which case its field order carries no meaning.
func operatorDocument(raw bson.Raw) bool {
	elements, err := raw.Elements()
	if err != nil || len(elements) == 0 {
		return false
	}
	for _, element := range elements {
		if !strings.HasPrefix(element.Key(), "$") {
			return false
		}
	}
	return true
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
