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
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

var _ embedder.Embedder = (*MeteredEmbedder)(nil)
var _ embedder.BatchEmbedder = (*MeteredEmbedder)(nil)

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
	m.record(started, 1, false, usage, err)
	return value, usage, err
}

// GetDimensions implements embedder.Embedder.
func (m *MeteredEmbedder) GetDimensions() int {
	return m.underlying.GetDimensions()
}

// GetEmbeddings implements embedder.BatchEmbedder.
func (m *MeteredEmbedder) GetEmbeddings(
	ctx context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := m.GetEmbeddingsWithUsage(ctx, texts)
	return values, err
}

// GetEmbeddingsWithUsage implements embedder.BatchEmbedder.
func (m *MeteredEmbedder) GetEmbeddingsWithUsage(
	ctx context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	batch, ok := m.underlying.(embedder.BatchEmbedder)
	if !ok {
		return nil, nil, fmt.Errorf("underlying embedder does not support batching")
	}
	started := time.Now()
	values, usage, err := batch.GetEmbeddingsWithUsage(ctx, texts)
	m.record(started, len(texts), true, usage, err)
	return values, usage, err
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
