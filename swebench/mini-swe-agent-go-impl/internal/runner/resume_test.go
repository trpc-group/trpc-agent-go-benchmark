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
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/mini-swe-agent-go-impl/internal/sweagent"
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

	state, err := prepareResume(output, predictionsPath, selected, false, resumePolicyRetryable, runIdentity{})
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

	redo, err := prepareResume(output, predictionsPath, selected, true, resumePolicyRetryable, runIdentity{})
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

func TestPrepareResumeUpstreamSkipsEveryExistingPrediction(t *testing.T) {
	output := t.TempDir()
	predictionsPath := filepath.Join(output, "preds.json")
	predictions := map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: ""},
	}
	if err := artifact.WriteJSON(predictionsPath, predictions); err != nil {
		t.Fatal(err)
	}
	selected := []contract.Case{{InstanceID: "repo__repo-1"}}
	state, err := prepareResume(output, predictionsPath, selected, false, resumePolicyUpstream, runIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Pending) != 0 || state.Skipped["repo__repo-1"].Info.ExitStatus != "ExistingPrediction" {
		t.Fatalf("state = %#v", state)
	}
}

func TestPrepareResumeRejectsMixedExperimentIdentity(t *testing.T) {
	output := t.TempDir()
	predictionsPath := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(predictionsPath, map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(output, "repo__repo-1", "repo__repo-1.traj.json")
	if err := artifact.WriteJSON(path, sweagent.CaseResult{Info: sweagent.CaseInfo{
		ExitStatus:       "Submitted",
		ObservationCodec: "json",
		BillingAgentName: "BenchSWE-codec-json-e1",
		ExperimentID:     "e1",
		SourceRevision:   "revision",
		ModelConfigHash:  "model-hash",
		CasesHash:        "cases-hash",
	}}); err != nil {
		t.Fatal(err)
	}
	_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyRetryable, runIdentity{
		ObservationCodec: "text",
		BillingAgentName: "BenchSWE-codec-text-e1",
		ExperimentID:     "e1",
		SourceRevision:   "revision",
		ModelConfigHash:  "model-hash",
		CasesHash:        "cases-hash",
	})
	if err == nil {
		t.Fatal("mixed experiment identity accepted")
	}
}
