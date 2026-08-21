//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingconfig

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

var _ embedder.Embedder = (*MeteredEmbedder)(nil)
var _ batchEmbedder = (*MeteredEmbedder)(nil)

type batchEmbedder interface {
	GetEmbeddings(ctx context.Context, texts []string) ([][]float64, error)
	GetEmbeddingsWithUsage(
		ctx context.Context,
		texts []string,
	) ([][]float64, map[string]any, error)
}

// Metrics captures embedding API work for one SWE-Bench case.
type Metrics struct {
	Requests      int64 `json:"requests"`
	BatchRequests int64 `json:"batch_requests"`
	Inputs        int64 `json:"inputs"`
	Errors        int64 `json:"errors"`
	PromptTokens  int64 `json:"prompt_tokens,omitempty"`
	TotalTokens   int64 `json:"total_tokens,omitempty"`
	DurationMS    int64 `json:"duration_ms"`
}

// MeteredEmbedder adds concurrency-safe request and usage counters.
type MeteredEmbedder struct {
	underlying    embedder.Embedder
	requests      atomic.Int64
	batchRequests atomic.Int64
	inputs        atomic.Int64
	errors        atomic.Int64
	promptTokens  atomic.Int64
	totalTokens   atomic.Int64
	durationNanos atomic.Int64
}

// NewMetered wraps an embedder with per-instance metrics.
func NewMetered(underlying embedder.Embedder) *MeteredEmbedder {
	return &MeteredEmbedder{underlying: underlying}
}

// GetEmbedding implements embedder.Embedder.
func (m *MeteredEmbedder) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	value, _, err := m.GetEmbeddingWithUsage(ctx, text)
	return value, err
}

// GetEmbeddingWithUsage implements embedder.Embedder.
func (m *MeteredEmbedder) GetEmbeddingWithUsage(
	ctx context.Context,
	text string,
) ([]float64, map[string]any, error) {
	started := time.Now()
	value, usage, err := m.underlying.GetEmbeddingWithUsage(ctx, text)
	if err == nil {
		err = validateEmbeddingVector(value, m.GetDimensions())
	}
	m.record(started, 1, false, usage, err)
	if err != nil {
		return nil, usage, err
	}
	return value, usage, err
}

// GetDimensions implements embedder.Embedder.
func (m *MeteredEmbedder) GetDimensions() int {
	return m.underlying.GetDimensions()
}

// GetEmbeddings implements the benchmark-local optional batch extension.
func (m *MeteredEmbedder) GetEmbeddings(
	ctx context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := m.GetEmbeddingsWithUsage(ctx, texts)
	return values, err
}

// GetEmbeddingsWithUsage implements the benchmark-local optional batch
// extension. Public TAG 4237 has no BatchEmbedder contract, so a single-only
// embedder is called once per input and each real request is metered.
func (m *MeteredEmbedder) GetEmbeddingsWithUsage(
	ctx context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	if len(texts) == 0 {
		return [][]float64{}, nil, nil
	}
	if batch, ok := m.underlying.(batchEmbedder); ok {
		started := time.Now()
		values, usage, err := batch.GetEmbeddingsWithUsage(ctx, texts)
		if err == nil {
			err = validateEmbeddingBatch(values, len(texts), m.GetDimensions())
		}
		m.record(started, len(texts), true, usage, err)
		if err != nil {
			return nil, usage, err
		}
		return values, usage, err
	}

	values := make([][]float64, len(texts))
	var usage map[string]any
	for index, text := range texts {
		started := time.Now()
		value, itemUsage, err := m.underlying.GetEmbeddingWithUsage(ctx, text)
		if err == nil {
			err = validateEmbeddingVector(value, m.GetDimensions())
		}
		m.record(started, 1, false, itemUsage, err)
		if err != nil {
			return nil, usage, fmt.Errorf("embed input at index %d: %w", index, err)
		}
		values[index] = value
		usage = mergeTokenUsage(usage, itemUsage)
	}
	return values, usage, nil
}

func validateEmbeddingBatch(values [][]float64, inputs, dimensions int) error {
	if len(values) != inputs {
		return fmt.Errorf("embedding batch returned %d vectors for %d inputs", len(values), inputs)
	}
	for index, value := range values {
		if err := validateEmbeddingVector(value, dimensions); err != nil {
			return fmt.Errorf("embedding vector at index %d: %w", index, err)
		}
	}
	return nil
}

func validateEmbeddingVector(value []float64, dimensions int) error {
	if dimensions <= 0 {
		return fmt.Errorf("embedding dimensions must be positive")
	}
	if len(value) != dimensions {
		return fmt.Errorf("embedding dimension %d does not match configured dimension %d", len(value), dimensions)
	}
	for index, component := range value {
		if math.IsNaN(component) || math.IsInf(component, 0) {
			return fmt.Errorf("embedding component %d is not finite", index)
		}
	}
	return nil
}

func (m *MeteredEmbedder) record(
	started time.Time,
	inputs int,
	batch bool,
	usage map[string]any,
	err error,
) {
	m.requests.Add(1)
	if batch {
		m.batchRequests.Add(1)
	}
	m.inputs.Add(int64(inputs))
	m.durationNanos.Add(time.Since(started).Nanoseconds())
	if err != nil {
		m.errors.Add(1)
	}
	m.promptTokens.Add(usageInt64(usage["prompt_tokens"]))
	m.totalTokens.Add(usageInt64(usage["total_tokens"]))
}

func usageInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func mergeTokenUsage(total, item map[string]any) map[string]any {
	for _, name := range []string{"prompt_tokens", "total_tokens"} {
		value := usageInt64(item[name])
		if value == 0 {
			continue
		}
		if total == nil {
			total = make(map[string]any)
		}
		total[name] = usageInt64(total[name]) + value
	}
	return total
}

// Snapshot returns the current counters.
func (m *MeteredEmbedder) Snapshot() Metrics {
	return Metrics{
		Requests:      m.requests.Load(),
		BatchRequests: m.batchRequests.Load(),
		Inputs:        m.inputs.Load(),
		Errors:        m.errors.Load(),
		PromptTokens:  m.promptTokens.Load(),
		TotalTokens:   m.totalTokens.Load(),
		DurationMS:    time.Duration(m.durationNanos.Load()).Milliseconds(),
	}
}
