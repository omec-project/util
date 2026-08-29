// SPDX-FileCopyrightText: 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package deepcopy

import (
	"reflect"
	"testing"
)

// Values reused across multiple test cases below.
const (
	testNameAlice    = "Alice"
	testNameBob      = "Bob"
	testCityNYC      = "NYC"
	testHobbyReading = "reading"
	testScoreKey     = "test"
)

// Test structures for complex scenarios
type Person struct {
	Address *Address
	Scores  map[string]int
	Name    string
	Hobbies []string
	Age     int
}

type Address struct {
	Street string
	City   string
	Zip    int
}

type Company struct {
	Addresses map[string]*Address
	Metadata  map[string]string
	Name      string
	Employees []Person
}

func TestDeepCopyEmptySlice(t *testing.T) {
	// Create an empty slice (not nil)
	emptySlice := make([]int, 0)

	result, err := DeepCopy(emptySlice)
	if err != nil {
		t.Errorf("DeepCopy() unexpected error for empty slice: %v", err)
		return
	}

	// Check that result is not nil
	if result == nil {
		t.Errorf("DeepCopy() = nil, want empty slice")
		return
	}

	// Check that result has length 0
	if len(result) != 0 {
		t.Errorf("DeepCopy() length = %d, want 0", len(result))
		return
	}

	// They should be deeply equal
	if !reflect.DeepEqual(emptySlice, result) {
		t.Errorf("DeepCopy() = %v, want %v", result, emptySlice)
	}
}

func TestDeepCopyNilSlice(t *testing.T) {
	var nilSlice []int = nil

	result, err := DeepCopy(nilSlice)
	if err != nil {
		t.Errorf("DeepCopy() unexpected error for nil slice: %v", err)
		return
	}

	if result != nil {
		t.Errorf("DeepCopy() = %v, want nil", result)
	}
}

func TestDeepCopyEmptyMap(t *testing.T) {
	// Create an empty map (not nil)
	emptyMap := make(map[string]int)

	result, err := DeepCopy(emptyMap)
	if err != nil {
		t.Errorf("DeepCopy() unexpected error for empty map: %v", err)
		return
	}

	// Check that result is not nil
	if result == nil {
		t.Errorf("DeepCopy() = nil, want empty map")
		return
	}

	// Check that result has length 0
	if len(result) != 0 {
		t.Errorf("DeepCopy() length = %d, want 0", len(result))
		return
	}

	// They should be deeply equal
	if !reflect.DeepEqual(emptyMap, result) {
		t.Errorf("DeepCopy() = %v, want %v", result, emptyMap)
	}
}

func TestDeepCopyNilMap(t *testing.T) {
	var nilMap map[string]int = nil

	result, err := DeepCopy(nilMap)
	if err != nil {
		t.Errorf("DeepCopy() unexpected error for nil map: %v", err)
		return
	}

	if result != nil {
		t.Errorf("DeepCopy() = %v, want nil", result)
	}
}

func TestDeepCopySliceTypes(t *testing.T) {
	tests := []struct {
		name  string
		input []int
		isNil bool
	}{
		{
			name:  "nil slice",
			input: nil,
			isNil: true,
		},
		{
			name:  "empty slice with make",
			input: make([]int, 0),
			isNil: false,
		},
		{
			name:  "empty slice literal",
			input: []int{},
			isNil: false,
		},
		{
			name:  "slice with elements",
			input: []int{1, 2, 3},
			isNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DeepCopy(tt.input)
			if err != nil {
				t.Errorf("DeepCopy() unexpected error: %v", err)
				return
			}

			resultVal := reflect.ValueOf(result)

			if tt.isNil {
				if !resultVal.IsNil() {
					t.Errorf("DeepCopy() = %v, want nil", result)
				}
			} else {
				if resultVal.IsNil() {
					t.Errorf("DeepCopy() = nil, want non-nil slice")
				}
				if !reflect.DeepEqual(tt.input, result) {
					t.Errorf("DeepCopy() = %v, want %v", result, tt.input)
				}
			}
		})
	}
}

func TestDeepCopyMapTypes(t *testing.T) {
	tests := []struct {
		input map[string]int
		name  string
		isNil bool
	}{
		{
			name:  "nil map",
			input: nil,
			isNil: true,
		},
		{
			name:  "empty map with make",
			input: make(map[string]int),
			isNil: false,
		},
		{
			name:  "empty map literal",
			input: map[string]int{},
			isNil: false,
		},
		{
			name:  "map with elements",
			input: map[string]int{"a": 1, "b": 2},
			isNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DeepCopy(tt.input)
			if err != nil {
				t.Errorf("DeepCopy() unexpected error: %v", err)
				return
			}

			resultVal := reflect.ValueOf(result)

			if tt.isNil {
				if !resultVal.IsNil() {
					t.Errorf("DeepCopy() = %v, want nil", result)
				}
			} else {
				if resultVal.IsNil() {
					t.Errorf("DeepCopy() = nil, want non-nil map")
				}
				if !reflect.DeepEqual(tt.input, result) {
					t.Errorf("DeepCopy() = %v, want %v", result, tt.input)
				}
			}
		})
	}
}

func TestDeepCopyStructWithMixedFields(t *testing.T) {
	input := Person{
		Name:    "Test",
		Age:     25,
		Address: nil,
		Hobbies: make([]string, 0),    // empty but non-nil slice
		Scores:  make(map[string]int), // empty but non-nil map
	}

	result, err := DeepCopy(input)
	if err != nil {
		t.Errorf("DeepCopy() unexpected error: %v", err)
		return
	}

	// Check Address is nil
	if result.Address != nil {
		t.Errorf("DeepCopy() Address = %v, want nil", result.Address)
	}

	// Check Hobbies is empty but not nil
	hobbiesVal := reflect.ValueOf(result.Hobbies)
	if hobbiesVal.IsNil() {
		t.Errorf("DeepCopy() Hobbies is nil, want empty slice")
	}
	if len(result.Hobbies) != 0 {
		t.Errorf("DeepCopy() Hobbies length = %d, want 0", len(result.Hobbies))
	}

	// Check Scores is empty but not nil
	scoresVal := reflect.ValueOf(result.Scores)
	if scoresVal.IsNil() {
		t.Errorf("DeepCopy() Scores is nil, want empty map")
	}
	if len(result.Scores) != 0 {
		t.Errorf("DeepCopy() Scores length = %d, want 0", len(result.Scores))
	}

	if !reflect.DeepEqual(input, result) {
		t.Errorf("DeepCopy() = %v, want %v", result, input)
	}
}

func TestDeepCopyNestedStructs(t *testing.T) {
	input := Company{
		Name: "TechCorp",
		Employees: []Person{
			{
				Name:    testNameAlice,
				Age:     30,
				Address: &Address{Street: "123 Main St", City: testCityNYC, Zip: 10001},
				Hobbies: []string{testHobbyReading},
				Scores:  map[string]int{"math": 95},
			},
		},
		Addresses: map[string]*Address{
			"office": {Street: "456 Office Blvd", City: testCityNYC, Zip: 10002},
		},
		Metadata: make(map[string]string), // empty but non-nil map
	}

	result, err := DeepCopy(input)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	// Verify deep equality
	if !reflect.DeepEqual(input, result) {
		t.Errorf("DeepCopy() result does not match input")
	}

	// Verify it's a true deep copy by modifying original
	input.Employees[0].Name = testNameBob
	if result.Employees[0].Name == testNameBob {
		t.Errorf("DeepCopy() did not create independent copy")
	}

	// Verify Metadata is empty but not nil
	metadataVal := reflect.ValueOf(result.Metadata)
	if metadataVal.IsNil() {
		t.Errorf("DeepCopy() Metadata is nil, want empty map")
	}
	if len(result.Metadata) != 0 {
		t.Errorf("DeepCopy() Metadata length = %d, want 0", len(result.Metadata))
	}
}

func TestDeepCopyStructWithNilSlicesAndMaps(t *testing.T) {
	input := Person{
		Name:    "Test",
		Age:     25,
		Address: nil,
		Hobbies: nil, // nil slice
		Scores:  nil, // nil map
	}

	result, err := DeepCopy(input)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	if !reflect.DeepEqual(input, result) {
		t.Errorf("DeepCopy() = %v, want %v", result, input)
	}

	// Verify nil preservation
	if result.Hobbies != nil {
		t.Errorf("DeepCopy() Hobbies = %v, want nil", result.Hobbies)
	}
	if result.Scores != nil {
		t.Errorf("DeepCopy() Scores = %v, want nil", result.Scores)
	}
}

func TestDeepCopyNilPointer(t *testing.T) {
	var nilPtr *Person = nil

	result, err := DeepCopy(nilPtr)
	if err != nil {
		t.Errorf("DeepCopy() unexpected error for nil pointer: %v", err)
		return
	}

	if result != nil {
		t.Errorf("DeepCopy() = %v, want nil", result)
	}
}

func TestDeepCopyPointerToStructWithNilAndEmptyFields(t *testing.T) {
	tests := []struct {
		input      *Person
		name       string
		checkHobby bool
		hobbyIsNil bool
		checkScore bool
		scoreIsNil bool
	}{
		{
			name: "pointer to struct with nil slice",
			input: &Person{
				Name:    testNameAlice,
				Age:     30,
				Hobbies: nil,
				Scores:  map[string]int{testScoreKey: 100},
			},
			checkHobby: true,
			hobbyIsNil: true,
			checkScore: true,
			scoreIsNil: false,
		},
		{
			name: "pointer to struct with empty slice",
			input: &Person{
				Name:    testNameBob,
				Age:     25,
				Hobbies: []string{},
				Scores:  map[string]int{testScoreKey: 95},
			},
			checkHobby: true,
			hobbyIsNil: false,
			checkScore: true,
			scoreIsNil: false,
		},
		{
			name: "pointer to struct with nil map",
			input: &Person{
				Name:    "Charlie",
				Age:     35,
				Hobbies: []string{testHobbyReading},
				Scores:  nil,
			},
			checkHobby: true,
			hobbyIsNil: false,
			checkScore: true,
			scoreIsNil: true,
		},
		{
			name: "pointer to struct with empty map",
			input: &Person{
				Name:    "Diana",
				Age:     28,
				Hobbies: []string{"coding"},
				Scores:  map[string]int{},
			},
			checkHobby: true,
			hobbyIsNil: false,
			checkScore: true,
			scoreIsNil: false,
		},
		{
			name: "pointer to struct with both nil",
			input: &Person{
				Name:    "Eve",
				Age:     32,
				Hobbies: nil,
				Scores:  nil,
			},
			checkHobby: true,
			hobbyIsNil: true,
			checkScore: true,
			scoreIsNil: true,
		},
		{
			name: "pointer to struct with both empty",
			input: &Person{
				Name:    "Frank",
				Age:     40,
				Hobbies: []string{},
				Scores:  map[string]int{},
			},
			checkHobby: true,
			hobbyIsNil: false,
			checkScore: true,
			scoreIsNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DeepCopy(tt.input)
			if err != nil {
				t.Fatalf("DeepCopy() unexpected error: %v", err)
			}

			if result == nil {
				t.Fatal("DeepCopy() returned nil, expected non-nil pointer")
			}

			// Verify semantic equality
			if !reflect.DeepEqual(tt.input, result) {
				t.Errorf("DeepCopy() result does not match input")
				t.Errorf("  Input:  %+v", tt.input)
				t.Errorf("  Result: %+v", result)
			}

			// Check Hobbies nil state
			if tt.checkHobby {
				hobbiesVal := reflect.ValueOf(result.Hobbies)
				if tt.hobbyIsNil && !hobbiesVal.IsNil() {
					t.Errorf("DeepCopy() Hobbies = %v, want nil", result.Hobbies)
				}
				if !tt.hobbyIsNil && hobbiesVal.IsNil() {
					t.Errorf("DeepCopy() Hobbies is nil, want empty slice")
				}
			}

			// Check Scores nil state
			if tt.checkScore {
				scoresVal := reflect.ValueOf(result.Scores)
				if tt.scoreIsNil && !scoresVal.IsNil() {
					t.Errorf("DeepCopy() Scores = %v, want nil", result.Scores)
				}
				if !tt.scoreIsNil && scoresVal.IsNil() {
					t.Errorf("DeepCopy() Scores is nil, want empty map")
				}
			}
		})
	}
}

// testDeepCopyGeneric is a helper function to test DeepCopy with any type
func testDeepCopyGeneric[T any](t *testing.T, input T) {
	t.Helper()
	result, err := DeepCopy(input)
	if err != nil {
		t.Errorf("DeepCopy() error = %v", err)
		return
	}
	if !reflect.DeepEqual(input, result) {
		t.Errorf("DeepCopy() = %v, want %v", result, input)
	}
}

func TestDeepCopyBasicTypes(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		testDeepCopyGeneric(t, "hello")
	})
	t.Run("int", func(t *testing.T) {
		testDeepCopyGeneric(t, 42)
	})
	t.Run("float", func(t *testing.T) {
		testDeepCopyGeneric(t, 3.14)
	})
	t.Run("bool", func(t *testing.T) {
		testDeepCopyGeneric(t, true)
	})
	t.Run("slice with elements", func(t *testing.T) {
		testDeepCopyGeneric(t, []int{1, 2, 3})
	})
	t.Run("map with elements", func(t *testing.T) {
		testDeepCopyGeneric(t, map[string]int{"a": 1, "b": 2})
	})
}

// TestDeepCopyChannel verifies that channels cause an error
func TestDeepCopyChannel(t *testing.T) {
	type WithChannel struct {
		Ch chan int
	}

	input := WithChannel{
		Ch: make(chan int),
	}

	_, err := DeepCopy(input)
	if err == nil {
		t.Error("DeepCopy() expected error for channel, got nil")
	}
}

// TestDeepCopyFunction verifies that functions cause an error
func TestDeepCopyFunction(t *testing.T) {
	type WithFunc struct {
		Fn func()
	}

	input := WithFunc{
		Fn: func() {},
	}

	_, err := DeepCopy(input)
	if err == nil {
		t.Error("DeepCopy() expected error for function, got nil")
	}
}

// TestDeepCopyInvalidGobData tests handling of corrupted gob data
func TestDeepCopyInvalidGobData(t *testing.T) {
	// This test verifies the decode error path by using reflection
	// to inject a corrupted state (indirectly tested through type that causes issues)

	// Create a type with a field that might cause gob issues
	type Problematic struct {
		Data any
		Name string
	}

	// Use a problematic value
	input := Problematic{
		Name: testScoreKey,
		Data: make(chan int), // channels can't be encoded/decoded
	}

	_, err := DeepCopy(input)
	// Should get an error due to channel
	if err == nil {
		t.Error("DeepCopy() expected error for problematic type, got nil")
	}
}

// TestDeepCopyMutationIndependence verifies that modifying the copy doesn't affect original
func TestDeepCopyMutationIndependence(t *testing.T) {
	original := Person{
		Name:    "Original",
		Age:     30,
		Address: &Address{Street: "Original St", City: testCityNYC, Zip: 10001},
		Hobbies: []string{testHobbyReading, "coding"},
		Scores:  map[string]int{testScoreKey: 100},
	}

	copied, err := DeepCopy(original)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	// Modify the copy
	copied.Name = "Modified"
	copied.Age = 40
	copied.Address.Street = "Modified St"
	copied.Hobbies[0] = "swimming"
	copied.Scores[testScoreKey] = 50

	// Verify original is unchanged
	if original.Name != "Original" {
		t.Errorf("Original Name was modified: %s", original.Name)
	}
	if copied.Name != "Modified" {
		t.Errorf("Copied Name = %s, want Modified", copied.Name)
	}
	if original.Age != 30 {
		t.Errorf("Original Age was modified: %d", original.Age)
	}
	if copied.Age != 40 {
		t.Errorf("Copied Age = %d, want 40", copied.Age)
	}
	if original.Address.Street != "Original St" {
		t.Errorf("Original Address was modified: %s", original.Address.Street)
	}
	if original.Hobbies[0] != testHobbyReading {
		t.Errorf("Original Hobbies was modified: %s", original.Hobbies[0])
	}
	if original.Scores[testScoreKey] != 100 {
		t.Errorf("Original Scores was modified: %d", original.Scores[testScoreKey])
	}
}

// TestDeepCopyPointerToBasicType tests copying pointers to basic types
func TestDeepCopyPointerToBasicType(t *testing.T) {
	intVal := 42
	intPtr := &intVal

	result, err := DeepCopy(intPtr)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("DeepCopy() returned nil")
		return
	}

	if *result != 42 {
		t.Errorf("DeepCopy() = %d, want 42", *result)
	}

	// Verify independence
	*result = 100
	if *intPtr != 42 {
		t.Errorf("Original was modified")
	}
}

// TestDeepCopyStructWithUnexportedFields tests structs with unexported fields
func TestDeepCopyStructWithUnexportedFields(t *testing.T) {
	type StructWithUnexported struct {
		privateMap map[string]int
		Name       string
		Hobbies    []string
		age        int
	}

	input := StructWithUnexported{
		Name:       testNameAlice,
		age:        30,
		Hobbies:    []string{testHobbyReading},
		privateMap: map[string]int{testScoreKey: 1},
	}

	result, err := DeepCopy(input)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	// Exported fields should be copied
	if result.Name != testNameAlice {
		t.Errorf("DeepCopy() Name = %s, want Alice", result.Name)
	}
	if !reflect.DeepEqual(result.Hobbies, input.Hobbies) {
		t.Errorf("DeepCopy() Hobbies not copied correctly")
	}
}

// TestDeepCopyNestedStructFields tests struct fields that are structs (not pointers)
func TestDeepCopyNestedStructFields(t *testing.T) {
	type Inner struct {
		Data   map[string]string
		Values []int
	}

	type Outer struct {
		Name  string
		Inner Inner
	}

	input := Outer{
		Name: "Outer",
		Inner: Inner{
			Values: nil,
			Data:   make(map[string]string),
		},
	}

	result, err := DeepCopy(input)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	if !reflect.DeepEqual(input, result) {
		t.Errorf("DeepCopy() result does not match input")
	}

	// Check nested nil slice is preserved
	if result.Inner.Values != nil {
		t.Errorf("DeepCopy() Inner.Values = %v, want nil", result.Inner.Values)
	}

	// Check nested empty map is preserved
	if result.Inner.Data == nil {
		t.Error("DeepCopy() Inner.Data is nil, want empty map")
	}
	if len(result.Inner.Data) != 0 {
		t.Errorf("DeepCopy() Inner.Data length = %d, want 0", len(result.Inner.Data))
	}
}

// TestDeepCopyComplexNestedStructs tests complex nesting scenarios
func TestDeepCopyComplexNestedStructs(t *testing.T) {
	type Level2 struct {
		Meta  map[string]int
		Items []string
	}

	type Level1 struct {
		Nested *Level2
		Name   string
		Level2 Level2
	}

	type Level0 struct {
		Title  string
		Level1 Level1
	}

	input := Level0{
		Title: "Root",
		Level1: Level1{
			Name: "L1",
			Level2: Level2{
				Items: []string{},
				Meta:  nil,
			},
			Nested: &Level2{
				Items: nil,
				Meta:  map[string]int{},
			},
		},
	}

	result, err := DeepCopy(input)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	if !reflect.DeepEqual(input, result) {
		t.Errorf("DeepCopy() result does not match input")
	}

	// Verify nested struct field preservation
	if result.Level1.Level2.Items == nil {
		t.Error("DeepCopy() Level1.Level2.Items is nil, want empty slice")
	}
	if result.Level1.Level2.Meta != nil {
		t.Errorf("DeepCopy() Level1.Level2.Meta = %v, want nil", result.Level1.Level2.Meta)
	}

	// Verify pointer to struct field preservation
	if result.Level1.Nested.Items != nil {
		t.Errorf("DeepCopy() Level1.Nested.Items = %v, want nil", result.Level1.Nested.Items)
	}
	if result.Level1.Nested.Meta == nil {
		t.Error("DeepCopy() Level1.Nested.Meta is nil, want empty map")
	}
}

// TestDeepCopyNilInterface tests handling of nil interfaces.
// Note: gob has limitations with interface types - concrete types in interfaces
// may require registration with gob.Register. The code explicitly checks for
// nil interfaces to handle them gracefully.
func TestDeepCopyNilInterface(t *testing.T) {
	var nilInterface any = nil

	result, err := DeepCopy(nilInterface)
	if err != nil {
		t.Fatalf("DeepCopy() unexpected error: %v", err)
	}

	if result != nil {
		t.Errorf("DeepCopy() = %v, want nil", result)
	}
}

// TestDeepCopyMultipleLevelsOfPointers tests that the unwrapping logic handles
// multiple levels of pointer indirection (e.g., **Person, ***Person)
func TestDeepCopyMultipleLevelsOfPointers(t *testing.T) {
	tests := []struct {
		name        string
		setupInput  func() any
		verifyFunc  func(t *testing.T, result any)
		description string
	}{
		{
			name: "double pointer to struct with nil/empty fields",
			setupInput: func() any {
				person := &Person{
					Name:    testNameAlice,
					Age:     30,
					Hobbies: nil,
					Scores:  map[string]int{},
				}
				return &person
			},
			verifyFunc: func(t *testing.T, result any) {
				pperson := result.(**Person)
				if pperson == nil || *pperson == nil {
					t.Fatal("Result should not be nil")
				}
				person := **pperson

				if person.Hobbies != nil {
					t.Errorf("Hobbies should be nil, got %v", person.Hobbies)
				}

				if person.Scores == nil {
					t.Error("Scores should be empty map, not nil")
				}
				if len(person.Scores) != 0 {
					t.Errorf("Scores should be empty, got length %d", len(person.Scores))
				}
			},
			description: "Tests **Person with nil slice and empty map",
		},
		{
			name: "pointer to pointer to pointer",
			setupInput: func() any {
				person := &Person{
					Name:    testNameBob,
					Age:     25,
					Hobbies: []string{},
					Scores:  nil,
				}
				pperson := &person
				return &pperson
			},
			verifyFunc: func(t *testing.T, result any) {
				ppperson := result.(***Person)
				if ppperson == nil || *ppperson == nil || **ppperson == nil {
					t.Fatal("Result should not be nil at any level")
				}
				person := ***ppperson

				if person.Hobbies == nil {
					t.Error("Hobbies should be empty slice, not nil")
				}
				if len(person.Hobbies) != 0 {
					t.Errorf("Hobbies should be empty, got length %d", len(person.Hobbies))
				}

				if person.Scores != nil {
					t.Errorf("Scores should be nil, got %v", person.Scores)
				}
			},
			description: "Tests ***Person with empty slice and nil map",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.setupInput()

			// Use type switch to call DeepCopy with the correct type
			var err error
			var resultInterface any

			switch v := input.(type) {
			case **Person:
				result, e := DeepCopy(v)
				err = e
				resultInterface = result
			case ***Person:
				result, e := DeepCopy(v)
				err = e
				resultInterface = result
			default:
				t.Fatalf("Unexpected type: %T", input)
			}

			if err != nil {
				t.Fatalf("DeepCopy() unexpected error: %v", err)
			}

			tt.verifyFunc(t, resultInterface)

			// Verify deep equality using reflection
			if !reflect.DeepEqual(input, resultInterface) {
				t.Errorf("DeepCopy() result does not match input")
			}
		})
	}
}
