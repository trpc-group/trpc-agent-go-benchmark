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

	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

var _ embedder.Embedder = (*Embedder)(nil)
var _ embedder.BatchEmbedder = (*Embedder)(nil)

// Embedder is a read-through/write-through persistent embedding decorator.
type Embedder struct {
	store      *Store
	underlying embedder.Embedder
	metrics    metricCounters
}

// New wraps an embedder with the shared persistent store.
func New(store *Store, underlying embedder.Embedder) (*Embedder, error) {
	if store == nil {
		return nil, fmt.Errorf("embedding cache store is required")
	}
	if underlying == nil {
		return nil, fmt.Errorf("underlying embedder is required")
	}
	if dimensions := underlying.GetDimensions(); dimensions > 0 && dimensions != store.Dimensions() {
		return nil, fmt.Errorf(
			"embedding cache dimension mismatch: embedder=%d cache=%d",
			dimensions,
			store.Dimensions(),
		)
	}
	return &Embedder{store: store, underlying: underlying}, nil
}

// GetEmbedding implements embedder.Embedder.
func (e *Embedder) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	value, _, err := e.GetEmbeddingWithUsage(ctx, text)
	return value, err
}

// GetEmbeddingWithUsage implements embedder.Embedder.
func (e *Embedder) GetEmbeddingWithUsage(
	ctx context.Context,
	text string,
) ([]float64, map[string]any, error) {
	e.recordRequest(1, false)
	key := e.store.Key(text)
	cached, readStats, err := e.store.GetMany(ctx, []Key{key})
	e.recordRead(readStats)
	if err != nil {
		e.metrics.errors.Add(1)
		return nil, nil, err
	}
	if vector, ok := cached[key]; ok {
		e.metrics.hits.Add(1)
		return cloneVector(vector), nil, nil
	}
	e.metrics.misses.Add(1)

	vector, usage, err := e.underlying.GetEmbeddingWithUsage(ctx, text)
	if err != nil {
		e.metrics.errors.Add(1)
		return nil, usage, err
	}
	if err := validateVector(vector, e.store.Dimensions()); err != nil {
		e.metrics.errors.Add(1)
		return nil, usage, err
	}
	writeStats, err := e.store.PutMany(ctx, map[Key][]float64{key: vector})
	e.recordWrite(writeStats)
	if err != nil {
		e.metrics.errors.Add(1)
		return nil, usage, err
	}
	return cloneVector(vector), usage, nil
}

// GetDimensions implements embedder.Embedder.
func (e *Embedder) GetDimensions() int {
	return e.store.Dimensions()
}

// GetEmbeddings implements embedder.BatchEmbedder.
func (e *Embedder) GetEmbeddings(
	ctx context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := e.GetEmbeddingsWithUsage(ctx, texts)
	return values, err
}

// GetEmbeddingsWithUsage implements embedder.BatchEmbedder.
func (e *Embedder) GetEmbeddingsWithUsage(
	ctx context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	e.recordRequest(len(texts), true)
	if len(texts) == 0 {
		return [][]float64{}, nil, nil
	}

	keys := make([]Key, len(texts))
	for index, text := range texts {
		keys[index] = e.store.Key(text)
	}
	cached, readStats, err := e.store.GetMany(ctx, keys)
	e.recordRead(readStats)
	if err != nil {
		e.metrics.errors.Add(1)
		return nil, nil, err
	}

	missingKeys := make([]Key, 0)
	missingTexts := make([]string, 0)
	seenMissing := make(map[Key]struct{})
	var hits, misses int64
	for index, key := range keys {
		if _, ok := cached[key]; ok {
			hits++
			continue
		}
		misses++
		if _, ok := seenMissing[key]; ok {
			continue
		}
		seenMissing[key] = struct{}{}
		missingKeys = append(missingKeys, key)
		missingTexts = append(missingTexts, texts[index])
	}
	e.metrics.hits.Add(hits)
	e.metrics.misses.Add(misses)

	resolved := cached
	var usage map[string]any
	if len(missingTexts) > 0 {
		batch, ok := e.underlying.(embedder.BatchEmbedder)
		if !ok {
			e.metrics.errors.Add(1)
			return nil, nil, fmt.Errorf("underlying embedder does not support batching")
		}
		remote, remoteUsage, err := batch.GetEmbeddingsWithUsage(ctx, missingTexts)
		usage = remoteUsage
		if err != nil {
			e.metrics.errors.Add(1)
			return nil, usage, err
		}
		if len(remote) != len(missingTexts) {
			e.metrics.errors.Add(1)
			return nil, usage, fmt.Errorf(
				"embedding cache miss response count mismatch: got %d, want %d",
				len(remote),
				len(missingTexts),
			)
		}
		writes := make(map[Key][]float64, len(remote))
		for index, vector := range remote {
			if err := validateVector(vector, e.store.Dimensions()); err != nil {
				e.metrics.errors.Add(1)
				return nil, usage, fmt.Errorf("embedding cache miss at index %d: %w", index, err)
			}
			key := missingKeys[index]
			resolved[key] = vector
			writes[key] = vector
		}
		writeStats, err := e.store.PutMany(ctx, writes)
		e.recordWrite(writeStats)
		if err != nil {
			e.metrics.errors.Add(1)
			return nil, usage, err
		}
	}

	values := make([][]float64, len(keys))
	for index, key := range keys {
		vector, ok := resolved[key]
		if !ok {
			e.metrics.errors.Add(1)
			return nil, usage, fmt.Errorf("embedding cache did not resolve input at index %d", index)
		}
		values[index] = cloneVector(vector)
	}
	return values, usage, nil
}

// Snapshot returns the current counters.
func (e *Embedder) Snapshot() Metrics {
	return e.metrics.snapshot()
}

func (e *Embedder) recordRequest(inputs int, batch bool) {
	e.metrics.requests.Add(1)
	if batch {
		e.metrics.batchRequests.Add(1)
	}
	e.metrics.inputs.Add(int64(inputs))
}

func (e *Embedder) recordRead(stats ReadStats) {
	e.metrics.bytesRead.Add(stats.BytesRead)
	e.metrics.corruptions.Add(stats.Corruptions)
	e.metrics.readDurationNanos.Add(stats.Duration.Nanoseconds())
}

func (e *Embedder) recordWrite(stats WriteStats) {
	e.metrics.writes.Add(stats.Rows)
	e.metrics.bytesWritten.Add(stats.BytesWritten)
	e.metrics.writeDurationNanos.Add(stats.Duration.Nanoseconds())
}

func validateVector(vector []float64, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf("embedding vector dimension mismatch: got %d, want %d", len(vector), dimensions)
	}
	for index, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("embedding vector contains non-finite value at index %d", index)
		}
	}
	return nil
}

func cloneVector(vector []float64) []float64 {
	return append([]float64(nil), vector...)
}
