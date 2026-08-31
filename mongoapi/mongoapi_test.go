// Copyright (C) 2026 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package mongoapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// newUnreachableClient returns a MongoClient pointing at an address where no
// server listens, so index operations fail the way they do when MongoDB has no
// writable primary.
func newUnreachableClient(t *testing.T) *MongoClient {
	t.Helper()
	opts := options.Client().
		ApplyURI("mongodb://127.0.0.1:1").
		SetServerSelectionTimeout(50 * time.Millisecond)
	client, err := mongo.Connect(opts)
	if err != nil {
		t.Fatalf("could not create mongo client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Logf("failed to disconnect mongo client: %v", err)
		}
	})
	return &MongoClient{Client: client, dbName: "testdb", pools: make(map[string]map[string]int32)}
}

func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestRestfulAPICreateTTLIndexWithContextReturnsError(t *testing.T) {
	c := newUnreachableClient(t)

	err := c.RestfulAPICreateTTLIndexWithContext(canceledContext(t), "NfProfile", 0, "expireAt")
	if err == nil {
		t.Fatal("expected an error when the index cannot be created, got nil")
	}
	if !strings.Contains(err.Error(), "expireAt") || !strings.Contains(err.Error(), "NfProfile") {
		t.Errorf("error should name the field and the collection, got: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap the cause, got: %v", err)
	}
}

func TestRestfulAPIDropTTLIndexWithContextReturnsError(t *testing.T) {
	c := newUnreachableClient(t)

	err := c.RestfulAPIDropTTLIndexWithContext(canceledContext(t), "NfProfile", "expireAt")
	if err == nil {
		t.Fatal("expected an error when the index cannot be dropped, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap the cause, got: %v", err)
	}
}

func TestRestfulAPIPatchTTLIndexWithContextReturnsError(t *testing.T) {
	c := newUnreachableClient(t)

	if err := c.RestfulAPIPatchTTLIndexWithContext(canceledContext(t), "NfProfile", 3600, "expireAt"); err == nil {
		t.Fatal("expected an error when the index cannot be updated, got nil")
	}
}

func TestRestfulAPIListIndexesReturnsError(t *testing.T) {
	c := newUnreachableClient(t)

	specs, err := c.RestfulAPIListIndexes(canceledContext(t), "NfProfile")
	if err == nil {
		t.Fatal("expected an error when the indexes cannot be listed, got nil")
	}
	if specs != nil {
		t.Errorf("expected no specifications on error, got: %v", specs)
	}
	if !strings.Contains(err.Error(), "NfProfile") {
		t.Errorf("error should name the collection, got: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap the cause, got: %v", err)
	}
}

func TestIsIndexNotFound(t *testing.T) {
	tests := []struct {
		err      error
		name     string
		expected bool
	}{
		{
			name:     "index not found",
			err:      mongo.CommandError{Code: indexNotFoundErrorCode, Message: "index not found with name [expireAt]"},
			expected: true,
		},
		{
			name:     "wrapped index not found",
			err:      errors.Join(errors.New("drop failed"), mongo.CommandError{Code: indexNotFoundErrorCode}),
			expected: true,
		},
		{
			name:     "index options conflict",
			err:      mongo.CommandError{Code: indexOptionsConflictErrorCode, Message: "index already exists with different options"},
			expected: false,
		},
		{
			name:     "not a command error",
			err:      context.Canceled,
			expected: false,
		},
		{
			name:     "no error",
			err:      nil,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIndexNotFound(tc.err); got != tc.expected {
				t.Errorf("IsIndexNotFound() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestIsIndexOptionsConflict(t *testing.T) {
	tests := []struct {
		err      error
		name     string
		expected bool
	}{
		{
			name:     "index options conflict",
			err:      mongo.CommandError{Code: indexOptionsConflictErrorCode, Message: "index already exists with different options"},
			expected: true,
		},
		{
			name:     "wrapped index options conflict",
			err:      errors.Join(errors.New("create failed"), mongo.CommandError{Code: indexOptionsConflictErrorCode}),
			expected: true,
		},
		{
			name:     "index not found",
			err:      mongo.CommandError{Code: indexNotFoundErrorCode},
			expected: false,
		},
		{
			name:     "not a server error",
			err:      context.Canceled,
			expected: false,
		},
		{
			name:     "no error",
			err:      nil,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIndexOptionsConflict(tc.err); got != tc.expected {
				t.Errorf("IsIndexOptionsConflict() = %v, want %v", got, tc.expected)
			}
		})
	}
}
