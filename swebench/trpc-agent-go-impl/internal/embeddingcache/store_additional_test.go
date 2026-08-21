//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingcache

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestOpenAndNilStoreBoundaries(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, t.TempDir(), Identity{}); err == nil {
		t.Fatal("Open() accepted invalid identity")
	}
	if _, err := Open(ctx, "", testIdentity()); err == nil {
		t.Fatal("Open() accepted empty directory")
	}
	var store *Store
	if store.Path() != "" || store.Dimensions() != 0 {
		t.Fatalf("nil store metadata = %q, %d", store.Path(), store.Dimensions())
	}
	if err := store.Close(); err != nil {
		t.Fatalf("nil Store.Close() error = %v", err)
	}
}

func TestStoreEmptyAndLargeOperations(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	values, readStats, err := store.GetMany(ctx, nil)
	if err != nil || len(values) != 0 || readStats.BytesRead != 0 {
		t.Fatalf("empty GetMany() = %#v, %+v, %v", values, readStats, err)
	}
	writeStats, err := store.PutMany(ctx, nil)
	if err != nil || writeStats.Rows != 0 || writeStats.BytesWritten != 0 {
		t.Fatalf("empty PutMany() = %+v, %v", writeStats, err)
	}

	const count = maxLookupKeys + 5
	keys := make([]Key, count)
	writes := make(map[Key][]float64, count)
	for index := range keys {
		key := store.Key(fmt.Sprintf("document-%d", index))
		keys[index] = key
		writes[key] = []float64{float64(index), 2, 3}
	}
	if stats, err := store.PutMany(ctx, writes); err != nil || stats.Rows != count {
		t.Fatalf("large PutMany() = %+v, %v", stats, err)
	}
	values, _, err = store.GetMany(ctx, append(keys, keys[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != count {
		t.Fatalf("large GetMany() returned %d vectors, want %d", len(values), count)
	}
}

func TestStoreRejectsInvalidVectors(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tests := []struct {
		name   string
		vector []float64
		want   string
	}{
		{name: "dimensions", vector: []float64{1, 2}, want: "dimension mismatch"},
		{name: "NaN", vector: []float64{1, math.NaN(), 3}, want: "non-finite"},
		{name: "infinity", vector: []float64{1, math.Inf(1), 3}, want: "non-finite"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.PutMany(
				ctx,
				map[Key][]float64{store.Key(test.name): test.vector},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PutMany() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestStoreRejectsTamperedMetadata(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := Open(ctx, directory, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE cache_metadata SET value = ? WHERE name = ?`,
		"tampered",
		"identity_hash",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, directory, testIdentity()); err == nil ||
		!strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("Open() after metadata tamper error = %v", err)
	}
}
