// SPDX-FileCopyrightText: 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package deepcopy

import (
	"bytes"
	"encoding/gob"
	"reflect"
)

// DeepCopy creates a deep copy of any type using gob encoding.
// This function preserves the semantic difference between nil and empty slices/maps,
// which is important for correct serialization (e.g., JSON null vs []).
// Returns the deep copy and any error encountered during encoding/decoding.
func DeepCopy[T any](src T) (T, error) {
	var zero T

	// Handle nil pointers and nil interfaces
	srcValue := reflect.ValueOf(src)
	if !srcValue.IsValid() || (srcValue.Kind() == reflect.Pointer && srcValue.IsNil()) {
		return zero, nil
	}

	// Handle nil slices and maps by preserving their nil state
	if srcValue.Kind() == reflect.Slice && srcValue.IsNil() {
		return zero, nil
	}
	if srcValue.Kind() == reflect.Map && srcValue.IsNil() {
		return zero, nil
	}

	// Special handling for empty (non-nil) slices and maps
	if srcValue.Kind() == reflect.Slice && !srcValue.IsNil() && srcValue.Len() == 0 {
		// Create a new empty slice of the same type
		newSlice := reflect.MakeSlice(srcValue.Type(), 0, 0)
		return newSlice.Interface().(T), nil
	}

	if srcValue.Kind() == reflect.Map && !srcValue.IsNil() && srcValue.Len() == 0 {
		// Create a new empty map of the same type
		newMap := reflect.MakeMap(srcValue.Type())
		return newMap.Interface().(T), nil
	}

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	dec := gob.NewDecoder(&buf)

	err := enc.Encode(src)
	if err != nil {
		return zero, err
	}

	var dst T
	err = dec.Decode(&dst)
	if err != nil {
		return zero, err
	}

	// Post-process to restore nil slices and maps in structs
	dst = restoreNilAndEmptyValues(src, dst)

	return dst, nil
}

// restoreNilAndEmptyValues recursively restores nil and empty slices/maps in structs
func restoreNilAndEmptyValues[T any](original, copied T) T {
	originalVal := reflect.ValueOf(original)
	copiedVal := reflect.ValueOf(copied)

	if !originalVal.IsValid() || !copiedVal.IsValid() {
		return copied
	}

	// Unwrap pointers and interfaces to find the underlying struct
	unwrappedOriginal, ptrDepth := unwrapToStruct(originalVal)
	unwrappedCopied, _ := unwrapToStruct(copiedVal)

	// If we didn't find a struct after unwrapping, return as-is
	if !unwrappedOriginal.IsValid() || unwrappedOriginal.Kind() != reflect.Struct {
		return copied
	}

	if !unwrappedCopied.IsValid() || unwrappedCopied.Kind() != reflect.Struct {
		return copied
	}

	// Create a new value that we can modify
	newUnwrapped := reflect.New(unwrappedCopied.Type()).Elem()
	newUnwrapped.Set(unwrappedCopied)

	// Restore nil and empty values in the struct
	restoreNilAndEmptyValuesInStruct(unwrappedOriginal, newUnwrapped)

	// Re-wrap to the original type T by reconstructing the pointer chain
	result := rewrapFromStruct(newUnwrapped, ptrDepth)
	return result.Interface().(T)
}

// unwrapToStruct unwraps pointers and interfaces to get to the underlying struct.
// Returns the unwrapped value and the depth of pointer indirection.
func unwrapToStruct(v reflect.Value) (reflect.Value, int) {
	depth := 0
	for v.IsValid() {
		kind := v.Kind()
		if kind == reflect.Struct {
			return v, depth
		}
		if kind == reflect.Pointer || kind == reflect.Interface {
			if v.IsNil() {
				return reflect.Value{}, depth
			}
			depth++
			v = v.Elem()
		} else {
			// Not a pointer, interface, or struct - stop unwrapping
			return reflect.Value{}, depth
		}
	}
	return reflect.Value{}, depth
}

// rewrapFromStruct wraps a struct value back through the specified number of pointer levels.
func rewrapFromStruct(v reflect.Value, ptrDepth int) reflect.Value {
	result := v
	for i := 0; i < ptrDepth; i++ {
		// Create a new pointer to the current value
		ptr := reflect.New(result.Type())
		ptr.Elem().Set(result)
		result = ptr
	}
	return result
}

// restoreNilAndEmptyValuesInStruct recursively restores nil and empty values in struct fields
func restoreNilAndEmptyValuesInStruct(original, copied reflect.Value) {
	if original.Type() != copied.Type() {
		return
	}

	for i := 0; i < original.NumField(); i++ {
		originalField := original.Field(i)
		copiedField := copied.Field(i)

		if !copiedField.CanSet() {
			continue
		}

		switch originalField.Kind() {
		case reflect.Slice:
			if originalField.IsNil() && !copiedField.IsNil() {
				// Restore nil slice
				copiedField.Set(reflect.Zero(originalField.Type()))
			} else if !originalField.IsNil() && originalField.Len() == 0 && copiedField.IsNil() {
				// Restore empty (non-nil) slice
				newSlice := reflect.MakeSlice(originalField.Type(), 0, 0)
				copiedField.Set(newSlice)
			}
		case reflect.Map:
			if originalField.IsNil() && !copiedField.IsNil() {
				// Restore nil map
				copiedField.Set(reflect.Zero(originalField.Type()))
			} else if !originalField.IsNil() && originalField.Len() == 0 && copiedField.IsNil() {
				// Restore empty (non-nil) map
				newMap := reflect.MakeMap(originalField.Type())
				copiedField.Set(newMap)
			}
		case reflect.Struct:
			restoreNilAndEmptyValuesInStruct(originalField, copiedField)
		case reflect.Pointer:
			if !originalField.IsNil() && !copiedField.IsNil() {
				if originalField.Elem().Kind() == reflect.Struct {
					restoreNilAndEmptyValuesInStruct(originalField.Elem(), copiedField.Elem())
				}
			}
		}
	}
}
