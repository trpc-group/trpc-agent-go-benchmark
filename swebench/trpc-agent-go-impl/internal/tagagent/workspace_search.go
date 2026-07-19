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
	"fmt"
	"os"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	docreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader"
	textreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/text"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source/repo"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func init() {
	docreader.RegisterReader([]string{".py"}, textreader.New)
}

// WorkspaceIndexStats captures per-case local indexing cost separately from
// model token usage.
type WorkspaceIndexStats struct {
	Documents          int    `json:"documents"`
	DurationMS         int64  `json:"duration_ms"`
	PreloadedDocuments int    `json:"preloaded_documents,omitempty"`
	PreloadedChars     int    `json:"preloaded_chars,omitempty"`
	PreloadInjected    bool   `json:"preload_injected"`
	RetrievalMode      string `json:"retrieval_mode,omitempty"`
}

// WorkspaceSearchConfig controls optional dense retrieval and context bounds.
type WorkspaceSearchConfig struct {
	Embedder       embedder.Embedder
	SearchMode     vectorstore.SearchMode
	BatchSize      int
	DocConcurrency int
	MaxResults     int
	MaxChars       int
}

// NewWorkspaceCodeSearch snapshots and indexes one environment, returning a
// compact lexical code-search tool and a cleanup function.
func NewWorkspaceCodeSearch(
	ctx context.Context,
	environment sweenv.Environment,
	instanceID string,
	query string,
	config *WorkspaceSearchConfig,
) (tool.Tool, func() error, WorkspaceIndexStats, string, error) {
	started := time.Now()
	config = normalizeWorkspaceSearchConfig(config)
	snapshotter, ok := environment.(sweenv.WorkspaceSnapshotter)
	if !ok {
		return nil, nil, WorkspaceIndexStats{}, "", fmt.Errorf("environment does not support workspace snapshots")
	}
	src := repo.New(
		repo.WithName("swebench-workspace"),
		repo.WithRepository(repo.Repository{RepoName: instanceID}),
		repo.WithMaterializer(&workspaceMaterializer{snapshotter: snapshotter, instanceID: instanceID}),
		repo.WithFileExtensions([]string{".py"}),
		repo.WithSkipDirs([]string{".git", ".tox", ".venv", "venv", "node_modules", "build", "dist"}),
		repo.WithSkipSuffixes([]string{".pyc"}),
	)
	store := inmemory.New(inmemory.WithBM25(true))
	knowledgeOptions := []knowledge.Option{
		knowledge.WithVectorStore(store),
		knowledge.WithSources([]source.Source{src}),
	}
	if config.Embedder != nil {
		knowledgeOptions = append(knowledgeOptions, knowledge.WithEmbedder(config.Embedder))
	}
	kb := knowledge.New(knowledgeOptions...)
	if err := kb.Load(
		ctx,
		knowledge.WithShowProgress(false),
		knowledge.WithShowStats(false),
		knowledge.WithDocConcurrency(config.DocConcurrency),
		knowledge.WithEmbeddingBatchSize(config.BatchSize),
	); err != nil {
		_ = kb.Close()
		return nil, nil, WorkspaceIndexStats{}, "", fmt.Errorf("index workspace: %w", err)
	}
	count, err := store.Count(ctx)
	if err != nil {
		_ = kb.Close()
		return nil, nil, WorkspaceIndexStats{}, "", fmt.Errorf("count workspace documents: %w", err)
	}
	preloaded, err := knowledge.BuildContext(ctx, kb, &knowledge.ContextRequest{
		Query:      query,
		MaxResults: config.MaxResults,
		MaxChars:   config.MaxChars,
		SearchMode: int(config.SearchMode),
	})
	if err != nil {
		_ = kb.Close()
		return nil, nil, WorkspaceIndexStats{}, "", fmt.Errorf("preload workspace context: %w", err)
	}
	search := knowledgetool.NewCompactCodeSearchTool(
		kb,
		knowledgetool.WithCodeSearchMode(config.SearchMode),
		knowledgetool.WithCodeSearchMaxResults(6),
		knowledgetool.WithCodeSearchMinScore(0),
		knowledgetool.WithCodeSearchExtraExcludeMetadataKeys(
			"file_mode", "file_size", "modified_at", "repo_path", "source", "source_name",
		),
	)
	stats := WorkspaceIndexStats{
		Documents: count, DurationMS: time.Since(started).Milliseconds(),
		PreloadedDocuments: preloaded.Documents, PreloadedChars: preloaded.Chars,
		RetrievalMode: searchModeName(config.SearchMode),
	}
	return search, kb.Close, stats, preloaded.Text, nil
}

func normalizeWorkspaceSearchConfig(config *WorkspaceSearchConfig) *WorkspaceSearchConfig {
	if config == nil {
		config = &WorkspaceSearchConfig{SearchMode: vectorstore.SearchModeKeyword}
	}
	normalized := *config
	if normalized.BatchSize <= 0 {
		normalized.BatchSize = 1
	}
	if normalized.DocConcurrency <= 0 {
		normalized.DocConcurrency = 1
	}
	if normalized.MaxResults <= 0 {
		normalized.MaxResults = 4
	}
	if normalized.MaxChars <= 0 {
		normalized.MaxChars = 6000
	}
	return &normalized
}

func searchModeName(mode vectorstore.SearchMode) string {
	switch mode {
	case vectorstore.SearchModeHybrid:
		return "hybrid"
	case vectorstore.SearchModeVector:
		return "vector"
	case vectorstore.SearchModeKeyword:
		return "keyword"
	default:
		return fmt.Sprintf("mode-%d", mode)
	}
}

type workspaceMaterializer struct {
	snapshotter sweenv.WorkspaceSnapshotter
	instanceID  string
}

func (m *workspaceMaterializer) Materialize(ctx context.Context) (*repo.MaterializedRepository, error) {
	dir, err := os.MkdirTemp("", "tag-swe-workspace-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace snapshot: %w", err)
	}
	if err := m.snapshotter.SnapshotWorkspace(ctx, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &repo.MaterializedRepository{
		Root:      dir,
		Name:      m.instanceID,
		StableURI: "workspace://" + m.instanceID,
		Cleanup:   func() { _ = os.RemoveAll(dir) },
	}, nil
}
