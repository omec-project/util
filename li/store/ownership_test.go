// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"reflect"
	"testing"

	"github.com/omec-project/util/li/types"
)

// ownedTask is a task with every slice field populated, so a test can overwrite each of
// them and see whether the store noticed.
func ownedTask() types.InterceptTask {
	return types.InterceptTask{
		XID:            "warrant-1",
		Targets:        []types.TargetIdentifier{{Type: types.TargetSUPI, Value: "262019876543210"}},
		TargetElements: []string{"supiimsi"},
		DIDs:           []string{"7d1c2f60-8a4e-4a1e-9f3b-2c5d6e7f8091"},
		Products:       []types.ProductType{types.ProductIRI},
		Deliveries:     []types.DeliveryEndpoint{{Type: types.DeliveryX2, Address: "mdf2:1"}},
	}
}

// scribble overwrites the first element of every slice field of t in place.
func scribble(t types.InterceptTask) {
	t.Targets[0] = types.TargetIdentifier{Type: types.TargetSUPI, Value: "999999999999999"}
	t.TargetElements[0] = "supinai"
	t.DIDs[0] = "not-the-did-that-was-provisioned"
	t.Products[0] = types.ProductCC
	t.Deliveries[0].Address = "attacker:0"
}

// assertIntact fails unless held carries every value ownedTask provisioned.
func assertIntact(t *testing.T, held types.InterceptTask, how string) {
	t.Helper()

	want := ownedTask()
	want.State = types.TaskActive
	if !reflect.DeepEqual(held, want) {
		t.Errorf("%s changed what the store holds:\n got %+v\nwant %+v", how, held, want)
	}
}

// TestActivateDoesNotShareTheCallersArrays: a caller holding the store's backing arrays
// can mutate them outside the lock, which is a data race and — since the arrays are a
// warrant's targets — cross-warrant corruption. The caller reuses its own slices, as any
// caller is entitled to, and the store must not see it.
func TestActivateDoesNotShareTheCallersArrays(t *testing.T) {
	s := New()
	task := ownedTask()
	if !s.Activate(task) {
		t.Fatal("Activate failed")
	}

	scribble(task)

	held, ok := s.Get("warrant-1")
	if !ok {
		t.Fatal("the task is gone")
	}
	assertIntact(t, held, "the caller writing to the slices it passed to Activate")

	// And the index has to agree with the record, or Match and Get answer differently.
	if n := len(s.Match(types.TargetIdentifier{Type: types.TargetSUPI, Value: "262019876543210"})); n != 1 {
		t.Errorf("Match found %d tasks for the provisioned subject, want 1", n)
	}
}

// TestReadsDoNotShareTheStoresArrays: every read returns its own copy, so a caller
// writing to what Get, Snapshot or Match returned changes nothing in the store.
func TestReadsDoNotShareTheStoresArrays(t *testing.T) {
	for name, read := range map[string]func(*Store) types.InterceptTask{
		"Get": func(s *Store) types.InterceptTask {
			held, _ := s.Get("warrant-1")
			return held
		},
		"Snapshot": func(s *Store) types.InterceptTask { return s.Snapshot()[0] },
		"Match": func(s *Store) types.InterceptTask {
			return s.Match(types.TargetIdentifier{Type: types.TargetSUPI, Value: "262019876543210"})[0]
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := New()
			s.Activate(ownedTask())

			scribble(read(s))

			held, _ := s.Get("warrant-1")
			assertIntact(t, held, "writing to the result of "+name)
		})
	}
}

// TestEveryReferenceFieldIsCloned: cloneTask copies the slice fields it names, so a field
// added to InterceptTask later that shares storage when copied by value — a slice, a map,
// a pointer — would be shared with callers silently. This fails until it is named.
func TestEveryReferenceFieldIsCloned(t *testing.T) {
	cloned := map[string]bool{
		"Targets": true, "TargetElements": true, "DIDs": true, "Products": true, "Deliveries": true,
	}

	typ := reflect.TypeFor[types.InterceptTask]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
			if !cloned[f.Name] {
				t.Errorf("InterceptTask.%s shares storage when copied and cloneTask does not copy it", f.Name)
			}
		default:
		}
	}
}
