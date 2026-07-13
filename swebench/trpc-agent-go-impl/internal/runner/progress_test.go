//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"path/filepath"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/sweagent"
)

func TestProgressReporterPersistsFinishedUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	reporter := newProgressReporter(path, "run-1")
	reporter.Update(sweagent.ProgressUpdate{InstanceID: "repo__repo-1", Phase: "running", LastEventAt: time.Now().UTC()})
	reporter.Update(sweagent.ProgressUpdate{
		InstanceID: "repo__repo-1", Phase: "finished", ExitStatus: "Submitted", LLMCalls: 3,
	})
	var document progressDocument
	if err := artifact.ReadJSONFile(path, &document); err != nil {
		t.Fatal(err)
	}
	if document.RunID != "run-1" || document.Cases["repo__repo-1"].Phase != "finished" || document.Cases["repo__repo-1"].LLMCalls != 3 {
		t.Fatalf("document = %#v", document)
	}
}
