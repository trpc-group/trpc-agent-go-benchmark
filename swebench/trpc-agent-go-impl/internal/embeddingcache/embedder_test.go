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
	"reflect"
	"sync"
	"testing"
)

type recordingEmbedder struct {
	mu           sync.Mutex
	singleInputs []string
	batchInputs  [][]string
	dimensions   int
}

func (e *recordingEmbedder) GetEmbedding(
	_ context.Context,
	text string,
) ([]float64, error) {
	value, _, err := e.GetEmbeddingWithUsage(context.Background(), text)
	return value, err
}

func (e *recordingEmbedder) GetEmbeddingWithUsage(
	_ context.Context,
	text string,
) ([]float64, map[string]any, error) {
	e.mu.Lock()
	e.singleInputs = append(e.singleInputs, text)
	e.mu.Unlock()
	return testVector(text), map[string]any{"prompt_tokens": 1}, nil
}

func (e *recordingEmbedder) GetDimensions() int {
	return e.dimensions
}

func (e *recordingEmbedder) GetEmbeddings(
	ctx context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := e.GetEmbeddingsWithUsage(ctx, texts)
	return values, err
}

func (e *recordingEmbedder) GetEmbeddingsWithUsage(
	_ context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	e.mu.Lock()
	e.batchInputs = append(e.batchInputs, append([]string(nil), texts...))
	e.mu.Unlock()
	values := make([][]float64, len(texts))
	for index, text := range texts {
		values[index] = testVector(text)
	}
	return values, map[string]any{"prompt_tokens": len(texts)}, nil
}

func (e *recordingEmbedder) calls() ([]string, [][]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	singles := append([]string(nil), e.singleInputs...)
	batches := make([][]string, len(e.batchInputs))
	for index, batch := range e.batchInputs {
		batches[index] = append([]string(nil), batch...)
	}
	return singles, batches
}

func TestEmbedderColdWarmAndPartialBatch(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := Open(ctx, directory, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	remote := &recordingEmbedder{dimensions: 3}
	cached, err := New(store, remote)
	if err != nil {
		t.Fatal(err)
	}

	coldTexts := []string{"a", "b", "a"}
	cold, usage, err := cached.GetEmbeddingsWithUsage(ctx, coldTexts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cold, vectorsFor(coldTexts)) {
		t.Fatalf("cold vectors = %#v", cold)
	}
	if usage["prompt_tokens"] != 2 {
		t.Fatalf("cold usage = %#v, want two unique remote inputs", usage)
	}
	_, batches := remote.calls()
	if !reflect.DeepEqual(batches, [][]string{{"a", "b"}}) {
		t.Fatalf("remote batches = %#v", batches)
	}
	if got := cached.Snapshot(); got.Inputs != 3 || got.Hits != 0 ||
		got.Misses != 3 || got.Writes != 2 {
		t.Fatalf("cold metrics = %+v", got)
	}

	partialTexts := []string{"b", "c", "a"}
	partial, usage, err := cached.GetEmbeddingsWithUsage(ctx, partialTexts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(partial, vectorsFor(partialTexts)) {
		t.Fatalf("partial vectors = %#v", partial)
	}
	if usage["prompt_tokens"] != 1 {
		t.Fatalf("partial usage = %#v, want one remote input", usage)
	}
	_, batches = remote.calls()
	if !reflect.DeepEqual(batches, [][]string{{"a", "b"}, {"c"}}) {
		t.Fatalf("remote batches = %#v", batches)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, directory, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	warmRemote := &recordingEmbedder{dimensions: 3}
	warm, err := New(reopened, warmRemote)
	if err != nil {
		t.Fatal(err)
	}
	warmTexts := []string{"c", "a", "b", "a"}
	values, warmUsage, err := warm.GetEmbeddingsWithUsage(ctx, warmTexts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, vectorsFor(warmTexts)) || warmUsage != nil {
		t.Fatalf("warm values = %#v, usage = %#v", values, warmUsage)
	}
	singles, batches := warmRemote.calls()
	if len(singles) != 0 || len(batches) != 0 {
		t.Fatalf("warm remote calls = singles %#v, batches %#v", singles, batches)
	}
	if got := warm.Snapshot(); got.Inputs != 4 || got.Hits != 4 ||
		got.Misses != 0 || got.Writes != 0 {
		t.Fatalf("warm metrics = %+v", got)
	}
}

func TestEmbedderCachesSingleInput(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	remote := &recordingEmbedder{dimensions: 3}
	cached, err := New(store, remote)
	if err != nil {
		t.Fatal(err)
	}
	first, usage, err := cached.GetEmbeddingWithUsage(ctx, "query")
	if err != nil {
		t.Fatal(err)
	}
	second, warmUsage, err := cached.GetEmbeddingWithUsage(ctx, "query")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || usage["prompt_tokens"] != 1 || warmUsage != nil {
		t.Fatalf("first = %#v, second = %#v, usage = %#v, warm = %#v", first, second, usage, warmUsage)
	}
	singles, _ := remote.calls()
	if !reflect.DeepEqual(singles, []string{"query"}) {
		t.Fatalf("single remote calls = %#v", singles)
	}
	if got := cached.Snapshot(); got.Hits != 1 || got.Misses != 1 || got.Writes != 1 {
		t.Fatalf("metrics = %+v", got)
	}
}

func TestEmbedderSupportsConcurrentWarmReads(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := store.Key("shared")
	if _, err := store.PutMany(ctx, map[Key][]float64{key: testVector("shared")}); err != nil {
		t.Fatal(err)
	}
	remote := &recordingEmbedder{dimensions: 3}
	cached, err := New(store, remote)
	if err != nil {
		t.Fatal(err)
	}

	const readers = 32
	var wg sync.WaitGroup
	errors := make(chan error, readers)
	for index := 0; index < readers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := cached.GetEmbedding(ctx, "shared")
			if err != nil {
				errors <- err
				return
			}
			if !reflect.DeepEqual(value, testVector("shared")) {
				errors <- fmt.Errorf("unexpected vector %#v", value)
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	singles, batches := remote.calls()
	if len(singles) != 0 || len(batches) != 0 {
		t.Fatalf("warm remote calls = singles %#v, batches %#v", singles, batches)
	}
	if got := cached.Snapshot(); got.Hits != readers || got.Misses != 0 {
		t.Fatalf("metrics = %+v", got)
	}
}

func TestEmbedderRejectsDimensionMismatch(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := New(store, &recordingEmbedder{dimensions: 4}); err == nil {
		t.Fatal("dimension mismatch succeeded")
	}
}

func testVector(text string) []float64 {
	var sum int
	for _, value := range []byte(text) {
		sum += int(value)
	}
	return []float64{float64(sum), float64(len(text)), float64(sum + len(text))}
}

func vectorsFor(texts []string) [][]float64 {
	values := make([][]float64, len(texts))
	for index, text := range texts {
		values[index] = testVector(text)
	}
	return values
}
