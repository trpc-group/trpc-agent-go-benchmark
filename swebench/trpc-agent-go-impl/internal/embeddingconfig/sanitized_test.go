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
	"strings"
	"testing"
)

type failingSanitizedEmbedder struct{ err error }

func (e failingSanitizedEmbedder) GetEmbedding(context.Context, string) ([]float64, error) {
	return nil, e.err
}

func (e failingSanitizedEmbedder) GetEmbeddingWithUsage(
	context.Context,
	string,
) ([]float64, map[string]any, error) {
	return nil, nil, e.err
}

func (failingSanitizedEmbedder) GetDimensions() int { return 2 }

func TestSanitizedEmbedderRedactsErrorsAndPreservesCause(t *testing.T) {
	cause := errors.New("POST https://private.invalid/v1 with secret-key failed")
	config := &Config{}
	config.Embedding.APIBase = "https://private.invalid/v1"
	config.Embedding.APIKey = "secret-key"
	wrapped := NewSanitized(failingSanitizedEmbedder{err: cause}, config.ScrubSensitiveText)
	_, _, err := wrapped.GetEmbeddingWithUsage(context.Background(), "input")
	if err == nil || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want wrapped original cause", err)
	}
	for _, forbidden := range []string{config.Embedding.APIBase, config.Embedding.APIKey} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("sanitized error contains %q: %v", forbidden, err)
		}
	}
}
