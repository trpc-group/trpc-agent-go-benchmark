//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
)

func TestParseRepresentationsDeduplicates(t *testing.T) {
	got, err := parseRepresentations(" ast-code,current-fixed,ast-code ")
	if err != nil {
		t.Fatalf("parseRepresentations() error = %v", err)
	}
	want := []tagagent.WorkspaceRepresentation{
		tagagent.WorkspaceRepresentationASTCode,
		tagagent.WorkspaceRepresentationCurrentFixed,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("representations = %v, want %v", got, want)
	}
}

func TestLoadCaseIDsAndSelectReplayCases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.txt")
	if err := os.WriteFile(path, []byte("# diagnostic panel\ncase-b\ncase-a\n"), 0o600); err != nil {
		t.Fatalf("write case list: %v", err)
	}
	ids, err := loadCaseIDs(path)
	if err != nil {
		t.Fatalf("loadCaseIDs() error = %v", err)
	}
	selected, err := selectReplayCases([]contract.Case{
		{InstanceID: "case-c"},
		{InstanceID: "case-a"},
		{InstanceID: "case-b"},
	}, "case-[ab]", ids)
	if err != nil {
		t.Fatalf("selectReplayCases() error = %v", err)
	}
	if len(selected) != 2 ||
		selected[0].InstanceID != "case-a" ||
		selected[1].InstanceID != "case-b" {
		t.Fatalf("selected = %+v", selected)
	}

	if _, err := selectReplayCases(
		[]contract.Case{{InstanceID: "case-a"}},
		"",
		ids,
	); err == nil {
		t.Fatal("missing case-list instance succeeded")
	}
}

func TestLoadGoldLabelsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	data := "{\"instance_id\":\"case-a\",\"patch\":\"diff --git a/a.py b/a.py\"}\n" +
		"{\"instance_id\":\"case-b\",\"patch\":\"diff --git a/b.py b/b.py\"}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write labels: %v", err)
	}
	labels, err := loadGoldLabels(path)
	if err != nil {
		t.Fatalf("loadGoldLabels() error = %v", err)
	}
	if len(labels) != 2 || labels["case-a"].Patch == "" || labels["case-b"].Patch == "" {
		t.Fatalf("labels = %#v", labels)
	}
}

type replayFactoryStub struct {
	starts    int
	snapshots int
	closes    int
}

func (f *replayFactoryStub) Start(
	context.Context,
	string,
) (sweenv.Environment, error) {
	f.starts++
	return &replayEnvironmentStub{factory: f}, nil
}

type replayEnvironmentStub struct {
	factory *replayFactoryStub
}

func (*replayEnvironmentStub) Execute(context.Context, string) sweenv.CommandResult {
	return sweenv.CommandResult{}
}

func (e *replayEnvironmentStub) Close(context.Context) error {
	e.factory.closes++
	return nil
}

func (e *replayEnvironmentStub) SnapshotWorkspace(
	_ context.Context,
	destination string,
) error {
	e.factory.snapshots++
	return os.WriteFile(
		filepath.Join(destination, "service.py"),
		[]byte("def old():\n    return 1\n"),
		0o600,
	)
}

func TestRunReplayCaseUsesOneSnapshotForAllRepresentations(t *testing.T) {
	factory := &replayFactoryStub{}
	representations := []tagagent.WorkspaceRepresentation{
		tagagent.WorkspaceRepresentationCurrentFixed,
		tagagent.WorkspaceRepresentationFixedRaw,
		tagagent.WorkspaceRepresentationASTCode,
		tagagent.WorkspaceRepresentationASTStructured,
	}
	result := runReplayCase(
		context.Background(),
		factory,
		contract.Case{
			InstanceID:       "owner__repo-1",
			Repo:             "owner/repo",
			ProblemStatement: "old return behavior",
		},
		patchTargets{
			TargetFiles: []string{"service.py"},
			Anchors: []patchAnchor{{
				File: "service.py",
				Text: "def old(): return 1",
			}},
		},
		representations,
		nil,
		nil,
		6,
		time.Minute,
	)
	if result.Error != "" {
		t.Fatalf("runReplayCase() error = %s", result.Error)
	}
	if factory.starts != 1 || factory.snapshots != 1 || factory.closes != 1 {
		t.Fatalf("factory calls = starts:%d snapshots:%d closes:%d",
			factory.starts, factory.snapshots, factory.closes)
	}
	if len(result.Arms) != len(representations) {
		t.Fatalf("arms = %d, want %d", len(result.Arms), len(representations))
	}
	for _, arm := range result.Arms {
		if arm.Error != "" || arm.Metrics.TargetFileRecallAt6 != 1 ||
			arm.Metrics.HunkAnchorRecallAt6 != 1 {
			t.Fatalf("arm = %+v", arm)
		}
	}
}
