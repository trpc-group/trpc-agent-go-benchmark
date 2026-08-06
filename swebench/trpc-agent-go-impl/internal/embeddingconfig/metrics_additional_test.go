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
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type singleMetricsEmbedder struct {
	mu       sync.Mutex
	inputs   []string
	failText string
}

type invalidMetricsEmbedder struct {
	values     [][]float64
	dimensions int
}

func (e invalidMetricsEmbedder) GetEmbedding(context.Context, string) ([]float64, error) {
	value, _, err := e.GetEmbeddingWithUsage(context.Background(), "")
	return value, err
}

func (e invalidMetricsEmbedder) GetEmbeddingWithUsage(
	context.Context,
	string,
) ([]float64, map[string]any, error) {
	if len(e.values) == 0 {
		return nil, nil, nil
	}
	return e.values[0], nil, nil
}

func (e invalidMetricsEmbedder) GetDimensions() int { return e.dimensions }

func (e invalidMetricsEmbedder) GetEmbeddings(
	_ context.Context,
	_ []string,
) ([][]float64, error) {
	return e.values, nil
}

func (e invalidMetricsEmbedder) GetEmbeddingsWithUsage(
	_ context.Context,
	_ []string,
) ([][]float64, map[string]any, error) {
	return e.values, nil, nil
}

func (e *singleMetricsEmbedder) GetEmbedding(
	ctx context.Context,
	text string,
) ([]float64, error) {
	value, _, err := e.GetEmbeddingWithUsage(ctx, text)
	return value, err
}

func (e *singleMetricsEmbedder) GetEmbeddingWithUsage(
	_ context.Context,
	text string,
) ([]float64, map[string]any, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, text)
	e.mu.Unlock()
	if text == e.failText {
		return nil, nil, errors.New("embedding failed")
	}
	return []float64{float64(len(text))}, map[string]any{
		"prompt_tokens": int32(2),
		"total_tokens":  float64(2),
	}, nil
}

func (*singleMetricsEmbedder) GetDimensions() int { return 1 }

func (e *singleMetricsEmbedder) recordedInputs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.inputs...)
}

func TestMeteredEmbedderFallsBackToPublicSingleAPI(t *testing.T) {
	underlying := &singleMetricsEmbedder{}
	metered := NewMetered(underlying)
	values, usage, err := metered.GetEmbeddingsWithUsage(
		context.Background(),
		[]string{"one", "three"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, [][]float64{{3}, {5}}) {
		t.Fatalf("values = %#v", values)
	}
	if usage["prompt_tokens"] != int64(4) || usage["total_tokens"] != int64(4) {
		t.Fatalf("usage = %#v", usage)
	}
	if !reflect.DeepEqual(underlying.recordedInputs(), []string{"one", "three"}) {
		t.Fatalf("inputs = %#v", underlying.recordedInputs())
	}
	metrics := metered.Snapshot()
	if metrics.Requests != 2 || metrics.BatchRequests != 0 ||
		metrics.Inputs != 2 || metrics.Errors != 0 ||
		metrics.PromptTokens != 4 || metrics.TotalTokens != 4 {
		t.Fatalf("metrics = %+v", metrics)
	}
	if metered.GetDimensions() != 1 {
		t.Fatalf("GetDimensions() = %d", metered.GetDimensions())
	}
}

func TestMeteredEmbedderEmptyBatchAndFallbackError(t *testing.T) {
	underlying := &singleMetricsEmbedder{failText: "bad"}
	metered := NewMetered(underlying)
	values, usage, err := metered.GetEmbeddingsWithUsage(context.Background(), nil)
	if err != nil || len(values) != 0 || usage != nil {
		t.Fatalf("empty batch = %#v, %#v, %v", values, usage, err)
	}
	if got := metered.Snapshot(); got.Requests != 0 || got.Inputs != 0 {
		t.Fatalf("empty batch metrics = %+v", got)
	}

	values, usage, err = metered.GetEmbeddingsWithUsage(
		context.Background(),
		[]string{"ok", "bad", "not-called"},
	)
	if err == nil || !strings.Contains(err.Error(), "index 1") {
		t.Fatalf("fallback error = %v", err)
	}
	if values != nil || usage["prompt_tokens"] != int64(2) {
		t.Fatalf("partial result = %#v, usage = %#v", values, usage)
	}
	if !reflect.DeepEqual(underlying.recordedInputs(), []string{"ok", "bad"}) {
		t.Fatalf("inputs = %#v", underlying.recordedInputs())
	}
	if got := metered.Snapshot(); got.Requests != 2 || got.Errors != 1 || got.Inputs != 2 {
		t.Fatalf("error metrics = %+v", got)
	}
}

func TestMeteredEmbedderConcurrentCounters(t *testing.T) {
	metered := NewMetered(metricsEmbedderStub{})
	const calls = 64
	var wait sync.WaitGroup
	for index := 0; index < calls; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := metered.GetEmbedding(context.Background(), "value"); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if got := metered.Snapshot(); got.Requests != calls ||
		got.Inputs != calls || got.PromptTokens != calls*2 {
		t.Fatalf("concurrent metrics = %+v", got)
	}
}

func TestMeteredEmbedderRejectsInvalidRemoteVectorsWithoutCache(t *testing.T) {
	tests := []struct {
		name   string
		values [][]float64
		inputs []string
		batch  bool
		want   string
	}{
		{name: "single wrong dimension", values: [][]float64{{1}}, want: "dimension 1"},
		{name: "single non finite", values: [][]float64{{1, math.NaN()}}, want: "not finite"},
		{name: "batch count", values: [][]float64{{1, 2}}, inputs: []string{"a", "b"}, batch: true, want: "1 vectors for 2 inputs"},
		{name: "batch wrong dimension", values: [][]float64{{1}, {2, 3}}, inputs: []string{"a", "b"}, batch: true, want: "index 0"},
		{name: "batch non finite", values: [][]float64{{1, 2}, {math.Inf(1), 3}}, inputs: []string{"a", "b"}, batch: true, want: "index 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metered := NewMetered(invalidMetricsEmbedder{values: test.values, dimensions: 2})
			var err error
			if test.batch {
				_, _, err = metered.GetEmbeddingsWithUsage(context.Background(), test.inputs)
			} else {
				_, _, err = metered.GetEmbeddingWithUsage(context.Background(), "input")
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if got := metered.Snapshot(); got.Errors != 1 {
				t.Fatalf("metrics = %+v, want one validation error", got)
			}
		})
	}
}

func TestUsageInt64SupportedTypes(t *testing.T) {
	tests := []struct {
		value any
		want  int64
	}{
		{value: int(1), want: 1},
		{value: int32(2), want: 2},
		{value: int64(3), want: 3},
		{value: float64(4), want: 4},
		{value: "5", want: 0},
		{value: nil, want: 0},
	}
	for _, test := range tests {
		if got := usageInt64(test.value); got != test.want {
			t.Fatalf("usageInt64(%#v) = %d, want %d", test.value, got, test.want)
		}
	}
}
