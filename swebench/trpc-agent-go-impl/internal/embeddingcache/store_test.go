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
	"os"
	"reflect"
	"sync"
	"testing"
)

func TestStorePersistsAcrossReopenAndIsolatesIdentity(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	identity := testIdentity()
	store, err := Open(ctx, directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	key := store.Key("document")
	vector := []float64{0.25, 0.5, 0.75}
	writeStats, err := store.PutMany(ctx, map[Key][]float64{key: vector})
	if err != nil {
		t.Fatal(err)
	}
	if writeStats.Rows != 1 || writeStats.BytesWritten == 0 {
		t.Fatalf("write stats = %+v", writeStats)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Path() != path {
		t.Fatalf("reopened path = %q, want %q", reopened.Path(), path)
	}
	values, readStats, err := reopened.GetMany(ctx, []Key{key})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values[key], vector) || readStats.BytesRead == 0 {
		t.Fatalf("values = %#v, stats = %+v", values, readStats)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o, want 600", info.Mode().Perm())
	}

	otherIdentity := identity
	otherIdentity.ModelFingerprint = "weights-v2"
	other, err := Open(ctx, directory, otherIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Path() == path {
		t.Fatal("different model fingerprint reused the same database")
	}
	otherValues, _, err := other.GetMany(ctx, []Key{other.Key("document")})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherValues) != 0 {
		t.Fatalf("isolated store returned values: %#v", otherValues)
	}
}

func TestStoreTreatsCorruptRowAsMissAndCanRepairIt(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := store.Key("document")
	vector := []float64{1, 2, 3}
	if _, err := store.PutMany(ctx, map[Key][]float64{key: vector}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE embeddings SET vector = ? WHERE cache_key = ?`,
		[]byte{0xff},
		key[:],
	); err != nil {
		t.Fatal(err)
	}
	values, stats, err := store.GetMany(ctx, []Key{key})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 || stats.Corruptions != 1 {
		t.Fatalf("values = %#v, stats = %+v", values, stats)
	}

	if _, err := store.PutMany(ctx, map[Key][]float64{key: vector}); err != nil {
		t.Fatal(err)
	}
	values, stats, err = store.GetMany(ctx, []Key{key})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values[key], vector) || stats.Corruptions != 0 {
		t.Fatalf("repaired values = %#v, stats = %+v", values, stats)
	}
}

func TestStoresCoordinateConcurrentWritersThroughSQLite(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	first, err := Open(ctx, directory, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, directory, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	const writers = 24
	var wg sync.WaitGroup
	errors := make(chan error, writers)
	keys := make([]Key, writers)
	for index := 0; index < writers; index++ {
		keys[index] = first.Key(fmt.Sprintf("document-%d", index))
		store := first
		if index%2 == 1 {
			store = second
		}
		wg.Add(1)
		go func(index int, store *Store) {
			defer wg.Done()
			if _, err := store.PutMany(
				ctx,
				map[Key][]float64{keys[index]: {float64(index), 2, 3}},
			); err != nil {
				errors <- err
			}
		}(index, store)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	values, _, err := first.GetMany(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != writers {
		t.Fatalf("stored values = %d, want %d", len(values), writers)
	}
}

func testIdentity() Identity {
	return Identity{
		Provider:           "openai",
		Model:              "bge-m3",
		ModelFingerprint:   "weights-v1",
		BackendFingerprint: "backend-v1",
		Dimensions:         3,
	}
}
