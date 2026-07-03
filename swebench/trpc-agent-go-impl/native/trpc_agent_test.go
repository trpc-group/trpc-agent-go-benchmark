//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
	aeven "trpc.group/trpc-go/trpc-agent-go/event"
	amodel "trpc.group/trpc-go/trpc-agent-go/model"
)

func TestTRPCTraceSnapshotFlushesPendingToolCallAndResult(t *testing.T) {
	collector := newTRPCTraceCollector(dataset.Instance{
		InstanceID: "case-1",
		Repo:       "example/repo",
	}, RunRequest{Model: "glm5"}, workspace{Image: "test-image"})

	collector.observe(&aeven.Event{
		Response: &amodel.Response{
			Done: true,
			Choices: []amodel.Choice{{
				Message: amodel.Message{
					Content: "Inspect the repo.",
					ToolCalls: []amodel.ToolCall{{
						ID: "call-1",
						Function: amodel.FunctionDefinitionParam{
							Name:      bashToolName,
							Arguments: []byte(`{"command":"ls -la"}`),
						},
					}},
				},
			}},
		},
	})

	snap := collector.snapshot(nil)
	if len(snap.Steps) != 1 {
		t.Fatalf("snapshot steps = %d, want 1", len(snap.Steps))
	}
	if snap.Steps[0].Action.Command != "ls -la" {
		t.Fatalf("snapshot command = %q", snap.Steps[0].Action.Command)
	}
	if snap.Steps[0].Command.Command != "" {
		t.Fatalf("snapshot should not have command result yet: %+v", snap.Steps[0].Command)
	}

	snap = collector.snapshot([]commandResult{{
		Command:  "ls -la",
		Output:   "ok",
		ExitCode: 0,
	}})
	if snap.Steps[0].Command.Output != "ok" {
		t.Fatalf("snapshot command result = %+v", snap.Steps[0].Command)
	}

	partialPath := filepath.Join(t.TempDir(), "case-1.partial.json")
	newPartialTraceFlusher(partialPath).flush(snap)
	data, err := os.ReadFile(partialPath)
	if err != nil {
		t.Fatalf("read partial trace: %v", err)
	}
	var flushed instanceTrace
	if err := json.Unmarshal(data, &flushed); err != nil {
		t.Fatalf("decode partial trace: %v", err)
	}
	if len(flushed.Steps) != 1 || flushed.Steps[0].Command.Output != "ok" {
		t.Fatalf("flushed trace = %+v", flushed)
	}
}
