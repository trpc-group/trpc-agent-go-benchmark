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
	_ "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/python"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source/repo"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// WorkspaceIndexStats captures per-case local indexing cost separately from
// model token usage.
type WorkspaceIndexStats struct {
	Documents  int   `json:"documents"`
	DurationMS int64 `json:"duration_ms"`
}

// NewWorkspaceCodeSearch snapshots and indexes one environment, returning a
// compact lexical code-search tool and a cleanup function.
func NewWorkspaceCodeSearch(
	ctx context.Context,
	environment sweenv.Environment,
	instanceID string,
) (tool.Tool, func() error, WorkspaceIndexStats, error) {
	started := time.Now()
	snapshotter, ok := environment.(sweenv.WorkspaceSnapshotter)
	if !ok {
		return nil, nil, WorkspaceIndexStats{}, fmt.Errorf("environment does not support workspace snapshots")
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
	kb := knowledge.New(
		knowledge.WithVectorStore(store),
		knowledge.WithSources([]source.Source{src}),
	)
	if err := kb.Load(ctx, knowledge.WithShowProgress(false), knowledge.WithShowStats(false)); err != nil {
		_ = kb.Close()
		return nil, nil, WorkspaceIndexStats{}, fmt.Errorf("index workspace: %w", err)
	}
	count, err := store.Count(ctx)
	if err != nil {
		_ = kb.Close()
		return nil, nil, WorkspaceIndexStats{}, fmt.Errorf("count workspace documents: %w", err)
	}
	search := knowledgetool.NewCompactCodeSearchTool(
		kb,
		knowledgetool.WithCodeSearchMode(vectorstore.SearchModeKeyword),
		knowledgetool.WithCodeSearchMaxResults(6),
		knowledgetool.WithCodeSearchMinScore(0),
		knowledgetool.WithCodeSearchExtraExcludeMetadataKeys(
			"file_mode", "file_size", "modified_at", "repo_path", "source", "source_name",
		),
	)
	return search, kb.Close, WorkspaceIndexStats{Documents: count, DurationMS: time.Since(started).Milliseconds()}, nil
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
