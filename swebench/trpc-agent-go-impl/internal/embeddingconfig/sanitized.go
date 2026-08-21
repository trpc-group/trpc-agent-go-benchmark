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

	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

var _ embedder.Embedder = (*SanitizedEmbedder)(nil)
var _ batchEmbedder = (*SanitizedEmbedder)(nil)

// SanitizedEmbedder removes local endpoint, credential, header, and cache-path
// material from every downstream embedding error while preserving errors.Is.
type SanitizedEmbedder struct {
	underlying embedder.Embedder
	scrub      func(string) string
}

// NewSanitized wraps the final embedding stack, including an optional cache.
func NewSanitized(underlying embedder.Embedder, scrub func(string) string) *SanitizedEmbedder {
	return &SanitizedEmbedder{underlying: underlying, scrub: scrub}
}

// GetEmbedding implements embedder.Embedder.
func (e *SanitizedEmbedder) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	value, usage, err := e.GetEmbeddingWithUsage(ctx, text)
	_ = usage
	return value, err
}

// GetEmbeddingWithUsage implements embedder.Embedder.
func (e *SanitizedEmbedder) GetEmbeddingWithUsage(
	ctx context.Context,
	text string,
) ([]float64, map[string]any, error) {
	value, usage, err := e.underlying.GetEmbeddingWithUsage(ctx, text)
	return value, usage, e.sanitize(err)
}

// GetDimensions implements embedder.Embedder.
func (e *SanitizedEmbedder) GetDimensions() int { return e.underlying.GetDimensions() }

// GetEmbeddings implements the benchmark-local optional batch extension.
func (e *SanitizedEmbedder) GetEmbeddings(
	ctx context.Context,
	texts []string,
) ([][]float64, error) {
	values, _, err := e.GetEmbeddingsWithUsage(ctx, texts)
	return values, err
}

// GetEmbeddingsWithUsage implements the benchmark-local optional batch
// extension without requiring a private framework interface.
func (e *SanitizedEmbedder) GetEmbeddingsWithUsage(
	ctx context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	if batch, ok := e.underlying.(batchEmbedder); ok {
		values, usage, err := batch.GetEmbeddingsWithUsage(ctx, texts)
		return values, usage, e.sanitize(err)
	}
	values := make([][]float64, len(texts))
	var usage map[string]any
	for index, text := range texts {
		value, itemUsage, err := e.underlying.GetEmbeddingWithUsage(ctx, text)
		if err != nil {
			return nil, usage, e.sanitize(err)
		}
		values[index] = value
		usage = mergeTokenUsage(usage, itemUsage)
	}
	return values, usage, nil
}

func (e *SanitizedEmbedder) sanitize(err error) error {
	if err == nil || e == nil || e.scrub == nil {
		return err
	}
	message := e.scrub(err.Error())
	if message == err.Error() {
		return err
	}
	return sanitizedEmbeddingError{message: message, cause: err}
}

type sanitizedEmbeddingError struct {
	message string
	cause   error
}

func (e sanitizedEmbeddingError) Error() string { return e.message }
func (e sanitizedEmbeddingError) Unwrap() error { return e.cause }
