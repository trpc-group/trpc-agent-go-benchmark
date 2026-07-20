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
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
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

func TestWorkspaceRepresentationsCoverageAndSemantics(t *testing.T) {
	root := writeRepresentationWorkspace(t)
	representations := []WorkspaceRepresentation{
		WorkspaceRepresentationCurrentFixed,
		WorkspaceRepresentationFixedRaw,
		WorkspaceRepresentationASTCode,
		WorkspaceRepresentationASTStructured,
	}
	for _, representation := range representations {
		t.Run(string(representation), func(t *testing.T) {
			index, stats, err := NewWorkspaceIndexFromDirectory(
				context.Background(),
				root,
				"owner/project",
				&WorkspaceSearchConfig{
					SearchMode:     vectorstore.SearchModeKeyword,
					Representation: representation,
				},
			)
			if err != nil {
				t.Fatalf("NewWorkspaceIndexFromDirectory() error = %v", err)
			}
			defer func() { _ = index.Close() }()

			if stats.EligibleFiles != 5 || stats.IndexedFiles != 5 ||
				stats.FileCoverage != 1 || len(stats.MissingFiles) != 0 {
				t.Fatalf("coverage stats = %+v, want 5/5", stats)
			}
			if stats.Representation != string(representation) ||
				stats.RepresentationSchema == "" ||
				stats.RepresentationSHA256 == "" ||
				stats.DocumentSetSHA256 == "" {
				t.Fatalf("representation provenance missing: %+v", stats)
			}

			result, err := index.Search(context.Background(), "find_user_by_email", 10)
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			userDocument := findResultContaining(result, "find_user_by_email")
			if userDocument == nil {
				t.Fatalf("search result has no user lookup document: %+v", result)
			}
			switch representation {
			case WorkspaceRepresentationCurrentFixed:
				if strings.Contains(userDocument.Content, "\n    def find_user_by_email") {
					t.Fatalf("current-fixed unexpectedly preserved indentation: %q", userDocument.Content)
				}
			case WorkspaceRepresentationFixedRaw:
				if !strings.Contains(userDocument.Content, "\n    def find_user_by_email") {
					t.Fatalf("fixed-raw lost indentation: %q", userDocument.Content)
				}
			case WorkspaceRepresentationASTCode:
				if userDocument.EmbeddingText != "" {
					t.Fatalf("ast-code EmbeddingText = %q, want Content fallback", userDocument.EmbeddingText)
				}
				if stats.FallbackDocuments != 2 {
					t.Fatalf("ast-code fallbacks = %d, want parse_error + no_nodes", stats.FallbackDocuments)
				}
			case WorkspaceRepresentationASTStructured:
				if !strings.Contains(userDocument.EmbeddingText, `"code":`) ||
					!strings.Contains(userDocument.EmbeddingText, "find_user_by_email") {
					t.Fatalf("ast-structured embedding = %q, want structure plus code", userDocument.EmbeddingText)
				}
				if stats.FallbackReasons["parse_error"] != 1 ||
					stats.FallbackReasons["no_nodes"] != 1 {
					t.Fatalf("fallback reasons = %v", stats.FallbackReasons)
				}
			}
		})
	}
}

func TestWorkspaceRepresentationHashesStableAcrossCheckoutRoots(t *testing.T) {
	firstRoot := writeRepresentationWorkspace(t)
	secondRoot := writeRepresentationWorkspace(t)
	for _, representation := range []WorkspaceRepresentation{
		WorkspaceRepresentationCurrentFixed,
		WorkspaceRepresentationFixedRaw,
		WorkspaceRepresentationASTCode,
		WorkspaceRepresentationASTStructured,
	} {
		config := &WorkspaceSearchConfig{
			SearchMode:     vectorstore.SearchModeKeyword,
			Representation: representation,
		}
		firstIndex, firstStats, err := NewWorkspaceIndexFromDirectory(
			context.Background(), firstRoot, "owner/project", config,
		)
		if err != nil {
			t.Fatalf("%s first index: %v", representation, err)
		}
		_ = firstIndex.Close()
		secondIndex, secondStats, err := NewWorkspaceIndexFromDirectory(
			context.Background(), secondRoot, "owner/project", config,
		)
		if err != nil {
			t.Fatalf("%s second index: %v", representation, err)
		}
		_ = secondIndex.Close()

		if firstStats.EligibleFileSetSHA256 != secondStats.EligibleFileSetSHA256 ||
			firstStats.IndexedFileSetSHA256 != secondStats.IndexedFileSetSHA256 ||
			firstStats.DocumentSetSHA256 != secondStats.DocumentSetSHA256 {
			t.Fatalf("%s hashes depend on checkout root:\nfirst=%+v\nsecond=%+v",
				representation, firstStats, secondStats)
		}
	}
}

func TestWorkspaceIndexCoverageCountsOnlyEligibleFiles(t *testing.T) {
	stats := buildWorkspaceIndexStats(
		WorkspaceRepresentationASTCode,
		[]string{"expected.py"},
		[]*document.Document{
			{
				Content: "def expected():\n    pass\n",
				Metadata: map[string]any{
					"trpc_ast_file_path": "expected.py",
				},
			},
			{
				Content: "def extra():\n    pass\n",
				Metadata: map[string]any{
					"trpc_ast_file_path": "extra.py",
				},
			},
		},
	)
	if stats.EligibleFiles != 1 || stats.IndexedFiles != 2 || stats.FileCoverage != 1 {
		t.Fatalf("coverage stats = %+v, want one eligible file covered by two indexed files", stats)
	}
	if len(stats.MissingFiles) != 1 || stats.MissingFiles[0] != "unexpected:extra.py" {
		t.Fatalf("missing files = %v, want unexpected extra.py", stats.MissingFiles)
	}
}

func TestParseWorkspaceRepresentation(t *testing.T) {
	got, err := ParseWorkspaceRepresentation("")
	if err != nil || got != WorkspaceRepresentationCurrentFixed {
		t.Fatalf("empty representation = %q, %v", got, err)
	}
	if _, err := ParseWorkspaceRepresentation("ast-ish"); err == nil {
		t.Fatal("unknown representation succeeded")
	}
}

func writeRepresentationWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"pkg/users.py": `class UserStore:
    def find_user_by_email(self, email):
        return self.users.get(email)
`,
		"pkg/test_users.py": `def test_find_user_by_email():
    assert True
`,
		"pkg/broken.py":    "def broken(:\n",
		"pkg/constants.py": "# constants only\n",
		"pkg/empty.py":     "",
		".ci/check.py":     "def check():\n    return True\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func findResultContaining(
	result *knowledge.SearchResult,
	content string,
) *document.Document {
	for _, item := range result.Documents {
		if item != nil && item.Document != nil &&
			strings.Contains(item.Document.Content, content) {
			return item.Document
		}
	}
	return nil
}
