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

	// Handle top-level pointer to struct
	if originalVal.Kind() == reflect.Pointer && copiedVal.Kind() == reflect.Pointer {
		if !originalVal.IsNil() && !copiedVal.IsNil() {
			if originalVal.Elem().Kind() == reflect.Struct && copiedVal.Elem().Kind() == reflect.Struct {
				// Create a new pointer that we can modify
				newCopied := reflect.New(copiedVal.Elem().Type())
				newCopied.Elem().Set(copiedVal.Elem())
				restoreNilAndEmptyValuesInStruct(originalVal.Elem(), newCopied.Elem())
				return newCopied.Interface().(T)
			}
		}
		return copied
	}

	// Only process structs
	if originalVal.Kind() != reflect.Struct || copiedVal.Kind() != reflect.Struct {
		return copied
	}

	// Create a new value that we can modify
	newCopied := reflect.New(copiedVal.Type()).Elem()
	newCopied.Set(copiedVal)

	restoreNilAndEmptyValuesInStruct(originalVal, newCopied)

	return newCopied.Interface().(T)
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
