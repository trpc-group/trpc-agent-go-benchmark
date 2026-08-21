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
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type singleOnlyCacheEmbedder struct {
	mu         sync.Mutex
	inputs     []string
	dimensions int
	failText   string
	vector     []float64
}

func (e *singleOnlyCacheEmbedder) GetEmbedding(
	ctx context.Context,
	text string,
) ([]float64, error) {
	value, _, err := e.GetEmbeddingWithUsage(ctx, text)
	return value, err
}

func (e *singleOnlyCacheEmbedder) GetEmbeddingWithUsage(
	_ context.Context,
	text string,
) ([]float64, map[string]any, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, text)
	e.mu.Unlock()
	if text == e.failText {
		return nil, nil, errors.New("remote embedding failed")
	}
	value := e.vector
	if value == nil {
		value = testVector(text)
	}
	return append([]float64(nil), value...), map[string]any{
		"prompt_tokens": int64(1),
		"total_tokens":  int64(1),
	}, nil
}

func (e *singleOnlyCacheEmbedder) GetDimensions() int { return e.dimensions }

func (e *singleOnlyCacheEmbedder) recordedInputs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.inputs...)
}

type shortBatchCacheEmbedder struct {
	*singleOnlyCacheEmbedder
}

func (e *shortBatchCacheEmbedder) GetEmbeddings(
	ctx context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := e.GetEmbeddingsWithUsage(ctx, texts)
	return values, err
}

func (e *shortBatchCacheEmbedder) GetEmbeddingsWithUsage(
	_ context.Context,
	_ []string,
) ([][]float64, map[string]any, error) {
	return [][]float64{{1, 2, 3}}, nil, nil
}

func TestEmbedderFallsBackToPublicSingleAPI(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	remote := &singleOnlyCacheEmbedder{dimensions: 3}
	cached, err := New(store, remote)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"a", "b", "a"}
	values, usage, err := cached.GetEmbeddingsWithUsage(ctx, texts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, vectorsFor(texts)) {
		t.Fatalf("cold vectors = %#v", values)
	}
	if usage["prompt_tokens"] != int64(2) || usage["total_tokens"] != int64(2) {
		t.Fatalf("cold usage = %#v", usage)
	}
	if !reflect.DeepEqual(remote.recordedInputs(), []string{"a", "b"}) {
		t.Fatalf("remote inputs = %#v", remote.recordedInputs())
	}
	warm, err := cached.GetEmbeddings(ctx, texts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(warm, values) {
		t.Fatalf("warm vectors = %#v", warm)
	}
	if !reflect.DeepEqual(remote.recordedInputs(), []string{"a", "b"}) {
		t.Fatalf("warm request called remote: %#v", remote.recordedInputs())
	}
	if cached.GetDimensions() != 3 {
		t.Fatalf("GetDimensions() = %d", cached.GetDimensions())
	}
	metrics := cached.Snapshot()
	if metrics.Requests != 2 || metrics.BatchRequests != 2 ||
		metrics.Inputs != 6 || metrics.Hits != 3 || metrics.Misses != 3 ||
		metrics.Writes != 2 || metrics.Errors != 0 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestEmbedderBatchFailureDoesNotWritePartialResults(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	remote := &singleOnlyCacheEmbedder{dimensions: 3, failText: "bad"}
	cached, err := New(store, remote)
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := cached.GetEmbeddingsWithUsage(ctx, []string{"ok", "bad"})
	if err == nil || !strings.Contains(err.Error(), "index 1") {
		t.Fatalf("GetEmbeddingsWithUsage() error = %v", err)
	}
	if usage["prompt_tokens"] != int64(1) {
		t.Fatalf("partial usage = %#v", usage)
	}
	values, _, getErr := store.GetMany(ctx, []Key{store.Key("ok")})
	if getErr != nil {
		t.Fatal(getErr)
	}
	if len(values) != 0 {
		t.Fatalf("partial vectors were cached: %#v", values)
	}
	if got := cached.Snapshot(); got.Errors != 1 || got.Writes != 0 {
		t.Fatalf("metrics = %+v", got)
	}
}

func TestEmbedderRejectsInvalidRemoteResponses(t *testing.T) {
	tests := []struct {
		name   string
		remote interface {
			GetEmbedding(context.Context, string) ([]float64, error)
			GetEmbeddingWithUsage(context.Context, string) ([]float64, map[string]any, error)
			GetDimensions() int
		}
		texts []string
		want  string
	}{
		{
			name: "wrong single dimensions",
			remote: &singleOnlyCacheEmbedder{
				dimensions: 3,
				vector:     []float64{1, 2},
			},
			texts: []string{"one"},
			want:  "dimension mismatch",
		},
		{
			name: "non-finite single",
			remote: &singleOnlyCacheEmbedder{
				dimensions: 3,
				vector:     []float64{1, math.NaN(), 3},
			},
			texts: []string{"one"},
			want:  "non-finite",
		},
		{
			name: "short batch",
			remote: &shortBatchCacheEmbedder{
				singleOnlyCacheEmbedder: &singleOnlyCacheEmbedder{dimensions: 3},
			},
			texts: []string{"one", "two"},
			want:  "response count mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, t.TempDir(), testIdentity())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cached, err := New(store, test.remote)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = cached.GetEmbeddingsWithUsage(ctx, test.texts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestEmbedderConstructorAndEmptyBatch(t *testing.T) {
	if _, err := New(nil, &singleOnlyCacheEmbedder{}); err == nil {
		t.Fatal("New() accepted nil store")
	}
	store, err := Open(context.Background(), t.TempDir(), testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := New(store, nil); err == nil {
		t.Fatal("New() accepted nil embedder")
	}
	unknownDimensions, err := New(
		store,
		&singleOnlyCacheEmbedder{dimensions: 0},
	)
	if err != nil {
		t.Fatalf("New() rejected unknown dimensions: %v", err)
	}
	values, usage, err := unknownDimensions.GetEmbeddingsWithUsage(
		context.Background(),
		nil,
	)
	if err != nil || len(values) != 0 || usage != nil {
		t.Fatalf("empty batch = %#v, %#v, %v", values, usage, err)
	}
	if got := unknownDimensions.Snapshot(); got.Requests != 1 ||
		got.BatchRequests != 1 || got.Inputs != 0 {
		t.Fatalf("empty batch metrics = %+v", got)
	}
}
