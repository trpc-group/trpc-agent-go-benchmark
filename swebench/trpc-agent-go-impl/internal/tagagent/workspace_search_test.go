//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tagagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	frameworktool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type snapshotEnvironment struct{}

func (snapshotEnvironment) Execute(context.Context, string) sweenv.CommandResult {
	return sweenv.CommandResult{}
}

func (snapshotEnvironment) Close(context.Context) error { return nil }

func (snapshotEnvironment) SnapshotWorkspace(_ context.Context, destination string) error {
	return os.WriteFile(filepath.Join(destination, "users.py"), []byte(`
class UserStore:
    def find_user_by_email(self, email):
        return self.users.get(email)
`), 0o600)
}

func TestWorkspaceCodeSearchIndexesAndRetrievesPython(t *testing.T) {
	search, closeSearch, stats, preloaded, err := NewWorkspaceCodeSearch(
		context.Background(), snapshotEnvironment{}, "repo__repo-1", "find user by email", nil,
	)
	if err != nil {
		t.Fatalf("NewWorkspaceCodeSearch() error = %v", err)
	}
	defer func() { _ = closeSearch() }()
	if stats.Documents == 0 {
		t.Fatalf("workspace stats = %+v", stats)
	}
	if stats.PreloadedDocuments == 0 || stats.PreloadedChars == 0 || preloaded == "" {
		t.Fatalf("preloaded context = %q, stats = %+v", preloaded, stats)
	}
	callable, ok := search.(frameworktool.CallableTool)
	if !ok {
		t.Fatalf("search tool type = %T, want CallableTool", search)
	}
	result, err := callable.Call(context.Background(), []byte(`{"query":"find_user_by_email"}`))
	if err != nil {
		t.Fatalf("code_search error = %v", err)
	}
	if result == nil {
		t.Fatal("expected code search result")
	}
}

type batchEmbedderStub struct{}

func (batchEmbedderStub) GetEmbedding(context.Context, string) ([]float64, error) {
	return []float64{1, 0}, nil
}

func (batchEmbedderStub) GetEmbeddingWithUsage(
	context.Context,
	string,
) ([]float64, map[string]any, error) {
	return []float64{1, 0}, nil, nil
}

func (batchEmbedderStub) GetDimensions() int { return 2 }

func (batchEmbedderStub) GetEmbeddings(
	_ context.Context,
	texts []string,
) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i := range vectors {
		vectors[i] = []float64{1, 0}
	}
	return vectors, nil
}

func (e batchEmbedderStub) GetEmbeddingsWithUsage(
	ctx context.Context,
	texts []string,
) ([][]float64, map[string]any, error) {
	vectors, err := e.GetEmbeddings(ctx, texts)
	return vectors, nil, err
}

func TestWorkspaceCodeSearchSupportsHybridBatchEmbedding(t *testing.T) {
	_, closeSearch, stats, preloaded, err := NewWorkspaceCodeSearch(
		context.Background(),
		snapshotEnvironment{},
		"repo__repo-1",
		"locate account lookup behavior",
		&WorkspaceSearchConfig{
			Embedder:       batchEmbedderStub{},
			SearchMode:     vectorstore.SearchModeHybrid,
			BatchSize:      8,
			DocConcurrency: 2,
			MaxResults:     2,
			MaxChars:       1000,
		},
	)
	if err != nil {
		t.Fatalf("NewWorkspaceCodeSearch() error = %v", err)
	}
	defer func() { _ = closeSearch() }()
	if stats.RetrievalMode != "hybrid" || stats.PreloadedDocuments == 0 || preloaded == "" {
		t.Fatalf("stats = %+v, preloaded = %q", stats, preloaded)
	}
}
