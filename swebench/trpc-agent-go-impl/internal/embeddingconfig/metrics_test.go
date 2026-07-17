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
	"testing"
)

type metricsEmbedderStub struct{}

func (metricsEmbedderStub) GetEmbedding(context.Context, string) ([]float64, error) {
	return []float64{1}, nil
}

func (metricsEmbedderStub) GetEmbeddingWithUsage(
	context.Context,
	string,
) ([]float64, map[string]any, error) {
	return []float64{1}, map[string]any{"prompt_tokens": int64(2), "total_tokens": int64(2)}, nil
}

func (metricsEmbedderStub) GetDimensions() int { return 1 }

func (metricsEmbedderStub) GetEmbeddings(
	_ context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := metricsEmbedderStub{}.GetEmbeddingsWithUsage(context.Background(), texts)
	return values, err
}

func (metricsEmbedderStub) GetEmbeddingsWithUsage(
	_ context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	values := make([][]float64, len(texts))
	for i := range values {
		values[i] = []float64{float64(i)}
	}
	return values, map[string]any{"prompt_tokens": int64(5), "total_tokens": int64(5)}, nil
}

func TestMeteredEmbedderCountsSingleAndBatchRequests(t *testing.T) {
	metered := NewMetered(metricsEmbedderStub{})
	if _, err := metered.GetEmbedding(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := metered.GetEmbeddings(context.Background(), []string{"two", "three"}); err != nil {
		t.Fatal(err)
	}
	got := metered.Snapshot()
	if got.Requests != 2 || got.BatchRequests != 1 || got.Inputs != 3 {
		t.Fatalf("metrics = %+v", got)
	}
	if got.PromptTokens != 7 || got.TotalTokens != 7 {
		t.Fatalf("usage metrics = %+v", got)
	}
}
