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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	frameworktool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type workspaceSnapshotEnvironment struct{}

type blockingWorkspaceReader struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (r blockingWorkspaceReader) ReadFromReader(string, io.Reader) ([]*document.Document, error) {
	close(r.started)
	<-r.release
	return nil, nil
}

func (blockingWorkspaceReader) ReadFromFile(string) ([]*document.Document, error) { return nil, nil }
func (blockingWorkspaceReader) ReadFromURL(string) ([]*document.Document, error)  { return nil, nil }
func (blockingWorkspaceReader) Name() string                                      { return "blocking" }
func (blockingWorkspaceReader) SupportedExtensions() []string                     { return []string{".py"} }

func (workspaceSnapshotEnvironment) Execute(context.Context, string) sweenv.CommandResult {
	return sweenv.CommandResult{}
}

func (workspaceSnapshotEnvironment) Close(context.Context) error { return nil }

func (workspaceSnapshotEnvironment) SnapshotWorkspace(_ context.Context, destination string) error {
	return os.WriteFile(filepath.Join(destination, "users.py"), []byte(`
class UserStore:
    def find_user_by_email(self, email):
        return self.users.get(email)
`), 0o600)
}

func TestWorkspaceIndexLoadsKeywordModeWithoutEmbedding(t *testing.T) {
	index, stats, err := NewWorkspaceIndexFromEnvironment(
		context.Background(), workspaceSnapshotEnvironment{}, "repo__repo-1", nil,
	)
	if err != nil {
		t.Fatalf("NewWorkspaceIndexFromEnvironment() error = %v", err)
	}
	defer func() { _ = index.Close() }()
	if stats.Documents == 0 || stats.RetrievalMode != "keyword" ||
		stats.InvocationDedup != CodeSearchInvocationDedup || stats.PreloadInjected {
		t.Fatalf("workspace stats = %+v", stats)
	}
	callable, ok := index.Tool().(frameworktool.CallableTool)
	if !ok {
		t.Fatalf("Tool() type = %T, want CallableTool", index.Tool())
	}
	result, err := callable.Call(context.Background(), []byte(`{"query":"find_user_by_email"}`))
	if err != nil || result == nil {
		t.Fatalf("code_search result = %#v, err = %v", result, err)
	}
}

func TestWorkspaceRepresentationsArePortableAndComplete(t *testing.T) {
	firstRoot := writeWorkspaceRepresentationFixture(t)
	secondRoot := writeWorkspaceRepresentationFixture(t)
	for _, representation := range []WorkspaceRepresentation{
		WorkspaceRepresentationCurrentFixed,
		WorkspaceRepresentationFixedRaw,
		WorkspaceRepresentationASTCode,
		WorkspaceRepresentationASTStructured,
	} {
		t.Run(string(representation), func(t *testing.T) {
			config := &WorkspaceSearchConfig{
				SearchMode:     vectorstore.SearchModeKeyword,
				Representation: representation,
			}
			firstIndex, firstStats, err := NewWorkspaceIndexFromDirectory(
				context.Background(), firstRoot, "owner/project", config,
			)
			if err != nil {
				t.Fatalf("first index: %v", err)
			}
			defer func() { _ = firstIndex.Close() }()
			secondIndex, secondStats, err := NewWorkspaceIndexFromDirectory(
				context.Background(), secondRoot, "owner/project", config,
			)
			if err != nil {
				t.Fatalf("second index: %v", err)
			}
			defer func() { _ = secondIndex.Close() }()

			if firstStats.EligibleFiles != 5 || firstStats.IndexedFiles != 5 ||
				firstStats.FileCoverage != 1 || len(firstStats.MissingFiles) != 0 {
				t.Fatalf("coverage = %+v, want 5/5", firstStats)
			}
			if firstStats.EligibleFileSetSHA256 != secondStats.EligibleFileSetSHA256 ||
				firstStats.EligibleContentSHA256 != secondStats.EligibleContentSHA256 ||
				firstStats.DocumentSetSHA256 != secondStats.DocumentSetSHA256 {
				t.Fatalf("%s hashes depend on checkout root:\nfirst=%+v\nsecond=%+v",
					representation, firstStats, secondStats)
			}
			if representation == WorkspaceRepresentationASTCode ||
				representation == WorkspaceRepresentationASTStructured {
				if firstStats.ParserDependency == "" || firstStats.ParserRuntime == "" ||
					len(firstStats.ParserRuntimeSHA256) != 64 ||
					firstStats.ParserRuntimeSHA256 != secondStats.ParserRuntimeSHA256 {
					t.Fatalf("%s parser identity = first=%+v second=%+v",
						representation, firstStats, secondStats)
				}
			}

			result, err := firstIndex.Search(context.Background(), "find_user_by_email", 10)
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			userDocument := findWorkspaceResult(result, "find_user_by_email")
			if userDocument == nil {
				t.Fatalf("search result has no lookup document: %+v", result)
			}
			switch representation {
			case WorkspaceRepresentationASTCode:
				if userDocument.EmbeddingText != userDocument.Content {
					t.Fatalf("ast-code embedding differs from source")
				}
			case WorkspaceRepresentationASTStructured:
				if !strings.Contains(userDocument.EmbeddingText, `"code":`) ||
					!strings.Contains(userDocument.EmbeddingText, "find_user_by_email") {
					t.Fatalf("ast-structured embedding = %q", userDocument.EmbeddingText)
				}
			}
		})
	}
}

func TestWorkspaceRepresentationEmptyValueIsCanonical(t *testing.T) {
	if got := WorkspaceRepresentationSchema(""); got != WorkspaceRepresentationSchema(WorkspaceRepresentationCurrentFixed) {
		t.Fatalf("empty schema = %q, want current-fixed", got)
	}
	if got := WorkspaceRepresentationSHA256(""); got != WorkspaceRepresentationSHA256(WorkspaceRepresentationCurrentFixed) {
		t.Fatalf("empty SHA = %q, want current-fixed", got)
	}
}

func TestASTDocumentReadReturnsAtCaseContextBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := readWorkspaceDocumentsWithContext(ctx, func() ([]*document.Document, error) {
			<-release
			return nil, nil
		})
		if err == nil || !strings.Contains(err.Error(), "case context") {
			t.Errorf("read error = %v, want case context cancellation", err)
		}
	}()
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("AST document read did not return at case context boundary")
	}
	close(release)
}

func TestASTDocumentBuildDoesNotFallbackAfterCaseCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var documents []*document.Document
	var buildErr error
	go func() {
		defer close(finished)
		documents, buildErr = buildWorkspaceDocumentsWithReader(
			ctx,
			[]eligibleWorkspaceFile{{Path: "blocked.py", Content: []byte("pass\n")}},
			WorkspaceSearchConfig{Representation: WorkspaceRepresentationASTCode},
			blockingWorkspaceReader{started: started, release: release},
		)
	}()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("AST document build did not return at case context boundary")
	}
	close(release)
	if buildErr == nil || !strings.Contains(buildErr.Error(), "case context") {
		t.Fatalf("build error = %v, want case context cancellation", buildErr)
	}
	if len(documents) != 0 {
		t.Fatalf("canceled AST build returned fallback documents: %#v", documents)
	}
}

func writeWorkspaceRepresentationFixture(t *testing.T) string {
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
		".git/ignored.py":  "def ignored():\n    return True\n",
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
	if err := os.Symlink(filepath.Join(root, "pkg", "users.py"), filepath.Join(root, "pkg", "linked.py")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return root
}

func findWorkspaceResult(result *knowledge.SearchResult, content string) *document.Document {
	for _, item := range result.Documents {
		if item != nil && item.Document != nil && strings.Contains(item.Document.Content, content) {
			return item.Document
		}
	}
	return nil
}
