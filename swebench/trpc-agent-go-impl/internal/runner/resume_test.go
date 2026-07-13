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

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/sweagent"
)

func TestPrepareResumeSkipsCompleteAndRetriesRetryable(t *testing.T) {
	output := t.TempDir()
	predictionsPath := filepath.Join(output, "preds.json")
	predictions := map[string]contract.Prediction{
		"repo__repo-1":  {InstanceID: "repo__repo-1", ModelPatch: "one"},
		"repo__repo-2":  {InstanceID: "repo__repo-2", ModelPatch: "two"},
		"other__repo-3": {InstanceID: "other__repo-3", ModelPatch: "other shard"},
	}
	if err := artifact.WriteJSON(predictionsPath, predictions); err != nil {
		t.Fatal(err)
	}
	writeTrace := func(id string, result sweagent.CaseResult) {
		t.Helper()
		path := filepath.Join(output, id, id+".traj.json")
		if err := artifact.WriteJSON(path, result); err != nil {
			t.Fatal(err)
		}
	}
	writeTrace("repo__repo-1", sweagent.CaseResult{Info: sweagent.CaseInfo{ExitStatus: "Submitted"}})
	writeTrace("repo__repo-2", sweagent.CaseResult{Info: sweagent.CaseInfo{
		ExitStatus: "Error", ErrorCategory: sweagent.ErrorCategoryEndpointTimeout, Retryable: true,
	}})
	selected := []contract.Case{{InstanceID: "repo__repo-1"}, {InstanceID: "repo__repo-2"}}

	state, err := prepareResume(output, predictionsPath, selected, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Skipped) != 1 || state.Skipped["repo__repo-1"].Info.ExitStatus != "Submitted" {
		t.Fatalf("skipped = %#v", state.Skipped)
	}
	if len(state.Pending) != 1 || state.Pending[0].InstanceID != "repo__repo-2" {
		t.Fatalf("pending = %#v", state.Pending)
	}
	if _, ok := state.Predictions["repo__repo-2"]; ok {
		t.Fatal("retryable prediction was retained")
	}
	if _, ok := state.Predictions["other__repo-3"]; !ok {
		t.Fatal("prediction from another shard was removed")
	}

	redo, err := prepareResume(output, predictionsPath, selected, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(redo.Pending) != 2 || len(redo.Skipped) != 0 {
		t.Fatalf("redo state = %#v", redo)
	}
	if _, ok := redo.Predictions["other__repo-3"]; !ok {
		t.Fatal("redo removed another shard")
	}
}
