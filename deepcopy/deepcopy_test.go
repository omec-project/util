// SPDX-FileCopyrightText: 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package deepcopy

import (
	"reflect"
	"testing"
)

// Test structures for complex scenarios
type Person struct {
	Name    string
	Age     int
	Address *Address
	Hobbies []string
	Scores  map[string]int
}

type Address struct {
	Street string
	City   string
	Zip    int
}

type Company struct {
	Name      string
	Employees []Person
	Addresses map[string]*Address
	Metadata  map[string]string
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
	resultVal := reflect.ValueOf(result)
	if resultVal.IsNil() {
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
	resultVal := reflect.ValueOf(result)
	if resultVal.IsNil() {
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
		name  string
		input map[string]int
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
				Name:    "Alice",
				Age:     30,
				Address: &Address{Street: "123 Main St", City: "NYC", Zip: 10001},
				Hobbies: []string{"reading"},
				Scores:  map[string]int{"math": 95},
			},
		},
		Addresses: map[string]*Address{
			"office": {Street: "456 Office Blvd", City: "NYC", Zip: 10002},
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
	input.Employees[0].Name = "Bob"
	if result.Employees[0].Name == "Bob" {
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

// TestDeepCopyMutationIndependence verifies that modifying the copy doesn't affect original
func TestDeepCopyMutationIndependence(t *testing.T) {
	original := Person{
		Name:    "Original",
		Age:     30,
		Address: &Address{Street: "Original St", City: "NYC", Zip: 10001},
		Hobbies: []string{"reading", "coding"},
		Scores:  map[string]int{"test": 100},
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
	copied.Scores["test"] = 50

	// Verify original is unchanged
	if original.Name != "Original" {
		t.Errorf("Original Name was modified: %s", original.Name)
	}
	if original.Age != 30 {
		t.Errorf("Original Age was modified: %d", original.Age)
	}
	if original.Address.Street != "Original St" {
		t.Errorf("Original Address was modified: %s", original.Address.Street)
	}
	if original.Hobbies[0] != "reading" {
		t.Errorf("Original Hobbies was modified: %s", original.Hobbies[0])
	}
	if original.Scores["test"] != 100 {
		t.Errorf("Original Scores was modified: %d", original.Scores["test"])
	}
}
