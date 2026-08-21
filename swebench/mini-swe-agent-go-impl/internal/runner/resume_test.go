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

func testRunIdentity(t *testing.T, instanceIDs ...string) runIdentity {
	t.Helper()
	selectedHash, err := selectedInstancesSHA256(instanceIDs)
	if err != nil {
		t.Fatal(err)
	}
	return runIdentity{
		RunID:                   "run-1",
		ObservationCodec:        "xml",
		SourceRevision:          "source-revision",
		BinarySHA256:            "binary-hash",
		ModelConfigHash:         "model-hash",
		EnvironmentConfigSHA256: "environment-hash",
		CasesHash:               "cases-hash",
		CommandTimeout:          "1m0s",
		CaseTimeout:             "2h0m0s",
		SelectedInstancesSHA256: selectedHash,
	}
}

func testCaseResult(instanceID, status string, retryable bool, identity runIdentity) sweagent.CaseResult {
	return sweagent.CaseResult{
		InstanceID: instanceID,
		Info: sweagent.CaseInfo{
			RunID:                   identity.RunID,
			ObservationCodec:        identity.ObservationCodec,
			SourceRevision:          identity.SourceRevision,
			SourceModified:          identity.SourceModified,
			BinarySHA256:            identity.BinarySHA256,
			ModelConfigHash:         identity.ModelConfigHash,
			EnvironmentConfigSHA256: identity.EnvironmentConfigSHA256,
			CasesHash:               identity.CasesHash,
			CommandTimeout:          identity.CommandTimeout,
			CaseTimeout:             identity.CaseTimeout,
			SelectedInstancesSHA256: identity.SelectedInstancesSHA256,
			ExitStatus:              status,
			Retryable:               retryable,
		},
	}
}

func writeResumeFixture(
	t *testing.T,
	output string,
	predictions map[string]contract.Prediction,
	results ...sweagent.CaseResult,
) string {
	t.Helper()
	predictionsPath := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(predictionsPath, predictions); err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		path := filepath.Join(output, result.InstanceID, result.InstanceID+".traj.json")
		if err := artifact.WriteJSON(path, result); err != nil {
			t.Fatal(err)
		}
	}
	return predictionsPath
}

func TestPrepareResumeSkipsCompleteAndRetriesRetryable(t *testing.T) {
	output := t.TempDir()
	selected := []contract.Case{{InstanceID: "repo__repo-1"}, {InstanceID: "repo__repo-2"}}
	identity := testRunIdentity(t, "repo__repo-1", "repo__repo-2")
	predictionsPath := writeResumeFixture(t, output, map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "one"},
		"repo__repo-2": {InstanceID: "repo__repo-2", ModelPatch: "two"},
	},
		testCaseResult("repo__repo-1", "Submitted", false, identity),
		testCaseResult("repo__repo-2", "Error", true, identity),
	)

	state, err := prepareResume(output, predictionsPath, selected, false, resumePolicyRetryable, identity)
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

	redo, err := prepareResume(output, predictionsPath, selected, true, resumePolicyRetryable, identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(redo.Pending) != 2 || len(redo.Skipped) != 0 || len(redo.Predictions) != 0 {
		t.Fatalf("redo state = %#v", redo)
	}
}

func TestPrepareResumeUpstreamKeyPresenceRequiresMatchingProvenance(t *testing.T) {
	output := t.TempDir()
	selected := []contract.Case{{InstanceID: "repo__repo-1"}}
	identity := testRunIdentity(t, "repo__repo-1")
	predictionsPath := writeResumeFixture(t, output, map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1"},
	}, testCaseResult("repo__repo-1", "", false, identity))

	state, err := prepareResume(output, predictionsPath, selected, false, resumePolicyUpstream, identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Pending) != 0 || state.Skipped["repo__repo-1"].Info.ExitStatus != "ExistingPrediction" {
		t.Fatalf("state = %#v", state)
	}
}

func TestPrepareResumeRejectsConfigurationChanges(t *testing.T) {
	base := testRunIdentity(t, "repo__repo-1")
	for _, test := range []struct {
		name   string
		mutate func(*runIdentity)
	}{
		{name: "model config", mutate: func(identity *runIdentity) { identity.ModelConfigHash = "other-model" }},
		{name: "environment config", mutate: func(identity *runIdentity) { identity.EnvironmentConfigSHA256 = "other-environment" }},
		{name: "command timeout", mutate: func(identity *runIdentity) { identity.CommandTimeout = "2m0s" }},
		{name: "case timeout", mutate: func(identity *runIdentity) { identity.CaseTimeout = "3h0m0s" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := t.TempDir()
			predictionsPath := writeResumeFixture(t, output, map[string]contract.Prediction{
				"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "patch"},
			}, testCaseResult("repo__repo-1", "Submitted", false, base))
			changed := base
			test.mutate(&changed)
			_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyUpstream, changed)
			if err == nil {
				t.Fatal("configuration change accepted")
			}
		})
	}
}

func TestPrepareResumeRejectsRunIDChange(t *testing.T) {
	output := t.TempDir()
	base := testRunIdentity(t, "repo__repo-1")
	predictionsPath := writeResumeFixture(t, output, map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "patch"},
	}, testCaseResult("repo__repo-1", "Submitted", false, base))
	changed := base
	changed.RunID = "run-2"
	_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyUpstream, changed)
	if err == nil {
		t.Fatal("run id change accepted")
	}
}

func TestPrepareResumeRejectsSelectionChange(t *testing.T) {
	t.Run("prediction outside selection", func(t *testing.T) {
		output := t.TempDir()
		identity := testRunIdentity(t, "repo__repo-1")
		predictionsPath := writeResumeFixture(t, output, map[string]contract.Prediction{
			"repo__repo-1":  {InstanceID: "repo__repo-1", ModelPatch: "one"},
			"other__repo-2": {InstanceID: "other__repo-2", ModelPatch: "other"},
		}, testCaseResult("repo__repo-1", "Submitted", false, identity))
		_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyUpstream, identity)
		if err == nil {
			t.Fatal("prediction outside current selection accepted")
		}
	})

	t.Run("expanded selection changes hash", func(t *testing.T) {
		output := t.TempDir()
		original := testRunIdentity(t, "repo__repo-1")
		predictionsPath := writeResumeFixture(t, output, map[string]contract.Prediction{
			"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "one"},
		}, testCaseResult("repo__repo-1", "Submitted", false, original))
		expanded := testRunIdentity(t, "repo__repo-1", "repo__repo-2")
		_, err := prepareResume(output, predictionsPath, []contract.Case{
			{InstanceID: "repo__repo-1"}, {InstanceID: "repo__repo-2"},
		}, false, resumePolicyUpstream, expanded)
		if err == nil {
			t.Fatal("expanded selection accepted against old trajectory")
		}
	})
}

func TestPrepareResumeRejectsTrajectoryInstanceIDMismatch(t *testing.T) {
	output := t.TempDir()
	identity := testRunIdentity(t, "repo__repo-1")
	result := testCaseResult("other__repo-2", "Submitted", false, identity)
	predictionsPath := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(predictionsPath, map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(output, "repo__repo-1", "repo__repo-1.traj.json")
	if err := artifact.WriteJSON(tracePath, result); err != nil {
		t.Fatal(err)
	}
	_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyUpstream, identity)
	if err == nil {
		t.Fatal("trajectory instance id mismatch accepted")
	}
}

func TestPrepareResumeRejectsMissingTrajectoryProvenance(t *testing.T) {
	output := t.TempDir()
	identity := testRunIdentity(t, "repo__repo-1")
	predictionsPath := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(predictionsPath, map[string]contract.Prediction{
		"repo__repo-1": {InstanceID: "repo__repo-1", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyUpstream, identity)
	if err == nil {
		t.Fatal("missing trajectory provenance accepted")
	}
}

func TestPrepareResumeRejectsUnsafePredictionID(t *testing.T) {
	output := t.TempDir()
	identity := testRunIdentity(t, "repo__repo-1")
	predictionsPath := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(predictionsPath, map[string]contract.Prediction{
		"../unsafe": {InstanceID: "../unsafe", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := prepareResume(output, predictionsPath, []contract.Case{{InstanceID: "repo__repo-1"}}, false, resumePolicyUpstream, identity)
	if err == nil {
		t.Fatal("unsafe prediction id accepted")
	}
}
