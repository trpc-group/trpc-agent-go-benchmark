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
	search, closeSearch, stats, err := NewWorkspaceCodeSearch(
		context.Background(), snapshotEnvironment{}, "repo__repo-1",
	)
	if err != nil {
		t.Fatalf("NewWorkspaceCodeSearch() error = %v", err)
	}
	defer func() { _ = closeSearch() }()
	if stats.Documents == 0 {
		t.Fatalf("workspace stats = %+v", stats)
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
