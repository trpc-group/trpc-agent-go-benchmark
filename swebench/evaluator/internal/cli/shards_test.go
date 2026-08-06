//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

func TestSummarizeShardPlanBindsNativeCleanRoomProvenance(t *testing.T) {
	dir := t.TempDir()
	instanceID := "psf__requests-2317"
	batch := batchPlanItem{
		Index: 0, Name: "batch-000", RunID: "native-000", Size: 1,
		InstanceIDs: []string{instanceID},
	}
	plan := testBatchPlan([]batchPlanItem{batch})
	rawDir := filepath.Join(dir, batch.RunID, "raw", "native")
	selectedSHA256, err := selectedInstancesSHA256(batch.InstanceIDs)
	if err != nil {
		t.Fatal(err)
	}
	artifact, images, offlineAssets := cleanRoomNativeArtifact(
		t,
		instanceID,
		"psf/requests",
		strings.Repeat("c", 40),
	)
	imageSetSHA256, err := sweenv.ImageSetSHA256(images)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	manifest := runnerManifest{
		RunID: batch.RunID, RunnerType: "trpc-agent-go-native", ObservationCodec: "xml",
		AgentProtocol:   "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1",
		UpstreamCommit:  strings.Repeat("f", 40),
		FrameworkModule: "trpc.group/trpc-go/trpc-agent-go", FrameworkVersion: "v1.2.3",
		SourceRevision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64),
		CasesSHA256: strings.Repeat("c", 64), ModelConfigSHA256: strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64), SelectedInstancesSHA256: selectedSHA256,
		CommandTimeout: "1m0s", CaseTimeout: "4h0m0s", CleanRoom: true,
		CleanRoomPolicySHA256: strings.Repeat("3", 64), OfflineAssets: offlineAssets,
		ImageSetSHA256: imageSetSHA256, DockerImages: images,
		StartedAt: start, FinishedAt: start.Add(time.Minute), DurationMS: int64(time.Minute / time.Millisecond),
		OutputDir: rawDir, CaseCount: 1, Workers: 1,
		Predictions: filepath.Join(rawDir, "preds.json"), Status: "completed",
		ModelConfig: map[string]string{"MODEL_NAME": "test-model"},
	}
	if err := writeJSON(filepath.Join(rawDir, "native-runner-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	info := artifact["info"].(map[string]any)
	info["run_id"] = manifest.RunID
	info["observation_codec"] = manifest.ObservationCodec
	info["source_revision"] = manifest.SourceRevision
	info["binary_sha256"] = manifest.BinarySHA256
	info["model_config_sha256"] = manifest.ModelConfigSHA256
	info["environment_config_sha256"] = manifest.EnvironmentConfigSHA256
	info["cases_sha256"] = manifest.CasesSHA256
	info["command_timeout"] = manifest.CommandTimeout
	info["case_timeout"] = manifest.CaseTimeout
	info["selected_instances_sha256"] = manifest.SelectedInstancesSHA256
	artifact["model_patch"] = "patch"
	if err := writeJSON(filepath.Join(rawDir, instanceID, instanceID+".native.json"), artifact); err != nil {
		t.Fatal(err)
	}
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		instanceID: {InstanceID: instanceID, ModelPatch: "patch"},
	})

	summary, err := summarizeShardPlan(plan, filepath.Join(dir, "plan.json"), dir, filepath.Join("raw", "native"))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AcceptedCases != 1 || summary.Shards[0].Status != "accepted" ||
		summary.RunnerIdentity.ManifestKind != "native" || !summary.RunnerIdentity.CleanRoom ||
		summary.RunnerIdentity.ImageSetSHA256 != imageSetSHA256 ||
		summary.RunnerIdentity.ModelName != "test-model" ||
		summary.RunnerIdentity.AgentProtocol != manifest.AgentProtocol ||
		summary.RunnerIdentity.UpstreamCommit != manifest.UpstreamCommit {
		t.Fatalf("native clean-room shard summary = %+v", summary)
	}
}

func TestNativeShardRunnerIdentityBindsModelAndProtocol(t *testing.T) {
	manifest := runnerManifest{
		RunnerType:              "trpc-agent-go-native",
		AgentProtocol:           "mini-swe-agent-v2.1-on-trpc-agent-go",
		UpstreamCommit:          strings.Repeat("f", 40),
		ObservationCodec:        "xml",
		FrameworkModule:         "trpc.group/trpc-go/trpc-agent-go",
		FrameworkVersion:        "v1.2.3",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		CasesSHA256:             strings.Repeat("c", 64),
		ModelConfigSHA256:       strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64),
		CommandTimeout:          "1m",
		CaseTimeout:             "4h",
		ModelConfig:             map[string]string{"MODEL_NAME": "model-a"},
	}
	identity, err := normalizeShardRunnerIdentity(nativeRunnerIdentity(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if identity.ModelName != "model-a" || identity.AgentProtocol != manifest.AgentProtocol ||
		identity.UpstreamCommit != manifest.UpstreamCommit {
		t.Fatalf("native identity = %+v", identity)
	}

	changed := cloneShardRunnerIdentity(identity)
	changed.ModelName = "model-b"
	if mismatch := shardRunnerIdentityMismatch(identity, changed); !strings.Contains(mismatch, "model_name") {
		t.Fatalf("model mismatch = %q, want model_name", mismatch)
	}
	changed = cloneShardRunnerIdentity(identity)
	changed.AgentProtocol += "+changed"
	if mismatch := shardRunnerIdentityMismatch(identity, changed); !strings.Contains(mismatch, "agent_protocol") {
		t.Fatalf("protocol mismatch = %q, want agent_protocol", mismatch)
	}
	changed = cloneShardRunnerIdentity(identity)
	changed.UpstreamCommit = strings.Repeat("1", 40)
	if mismatch := shardRunnerIdentityMismatch(identity, changed); !strings.Contains(mismatch, "upstream_commit") {
		t.Fatalf("upstream mismatch = %q, want upstream_commit", mismatch)
	}
}

func TestNativeShardRunnerIdentityRejectsMissingOrNonCanonicalModel(t *testing.T) {
	base := shardRunnerIdentity{
		ManifestKind:            "native",
		RunnerType:              "trpc-agent-go-native",
		ObservationCodec:        "xml",
		FrameworkModule:         "trpc.group/trpc-go/trpc-agent-go",
		FrameworkVersion:        "v1.2.3",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		CasesSHA256:             strings.Repeat("c", 64),
		ModelConfigSHA256:       strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64),
		CommandTimeout:          "1m",
		CaseTimeout:             "4h",
	}
	for _, modelName := range []string{"", " model-a", "model-a\nmodel-b"} {
		identity := cloneShardRunnerIdentity(base)
		identity.ModelName = modelName
		if _, err := normalizeShardRunnerIdentity(identity); err == nil || !strings.Contains(err.Error(), "model_name") {
			t.Fatalf("model_name %q error = %v, want model_name rejection", modelName, err)
		}
	}
}

func TestNativeShardRunnerIdentityRequiresProtocolAndUpstreamCommit(t *testing.T) {
	identity := defaultMiniGoIdentity()
	identity.ManifestKind = "native"
	identity.RunnerType = "trpc-agent-go-native"
	identity.ModelName = "model-a"
	identity.AgentProtocol = "mini-swe-agent-v2.1-on-trpc-agent-go"
	identity.UpstreamCommit = strings.Repeat("f", 40)
	if _, err := normalizeShardRunnerIdentity(identity); err != nil {
		t.Fatal(err)
	}

	missingProtocol := cloneShardRunnerIdentity(identity)
	missingProtocol.AgentProtocol = ""
	if _, err := normalizeShardRunnerIdentity(missingProtocol); err == nil ||
		!strings.Contains(err.Error(), "agent_protocol") {
		t.Fatalf("missing protocol error = %v", err)
	}
	missingUpstream := cloneShardRunnerIdentity(identity)
	missingUpstream.UpstreamCommit = ""
	if _, err := normalizeShardRunnerIdentity(missingUpstream); err == nil ||
		!strings.Contains(err.Error(), "upstream_commit") {
		t.Fatalf("missing upstream error = %v", err)
	}
}

func TestNativeCleanRoomIdentityRequiresProtocolAndUpstreamCommit(t *testing.T) {
	identity := defaultMiniGoIdentity()
	identity.ManifestKind = "native"
	identity.RunnerType = "trpc-agent-go-native"
	identity.ModelName = "model-a"
	identity.AgentProtocol = "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1"
	identity.UpstreamCommit = strings.Repeat("f", 40)
	identity.CleanRoom = true
	identity.CleanRoomPolicySHA256 = strings.Repeat("1", 64)
	identity.OfflineAssets = &sweenv.OfflineAssetIdentity{
		Schema: "swebench-offline-assets-v1", SHA256: strings.Repeat("2", 64),
		ManifestSHA256: strings.Repeat("3", 64), FileCount: 1,
	}
	identity.DockerImages = map[string]sweenv.ImageIdentity{
		"example/image:latest": {
			Reference: "example/image:latest",
			ID:        "sha256:" + strings.Repeat("4", 64),
		},
	}
	imageSetSHA256, err := sweenv.ImageSetSHA256(identity.DockerImages)
	if err != nil {
		t.Fatal(err)
	}
	identity.ImageSetSHA256 = imageSetSHA256
	if _, err := normalizeShardRunnerIdentity(identity); err != nil {
		t.Fatal(err)
	}

	missingProtocol := cloneShardRunnerIdentity(identity)
	missingProtocol.AgentProtocol = ""
	if _, err := normalizeShardRunnerIdentity(missingProtocol); err == nil ||
		!strings.Contains(err.Error(), "agent_protocol") {
		t.Fatalf("missing protocol error = %v", err)
	}
	missingUpstream := cloneShardRunnerIdentity(identity)
	missingUpstream.UpstreamCommit = ""
	if _, err := normalizeShardRunnerIdentity(missingUpstream); err == nil ||
		!strings.Contains(err.Error(), "upstream_commit") {
		t.Fatalf("missing upstream error = %v", err)
	}
}

func TestValidateNativeAgentProtocolBindsToolLoopWarning(t *testing.T) {
	tests := []struct {
		name            string
		protocol        string
		cleanRoom       bool
		toolLoopWarning bool
		wantError       bool
	}{
		{name: "legacy default", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go"},
		{name: "warning", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+tool-loop-warning-v1", toolLoopWarning: true},
		{name: "clean warning", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1+tool-loop-warning-v1", cleanRoom: true, toolLoopWarning: true},
		{name: "missing warning suffix", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go", toolLoopWarning: true, wantError: true},
		{name: "unexpected warning suffix", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+tool-loop-warning-v1", wantError: true},
		{name: "wrong suffix order", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+tool-loop-warning-v1+clean-room-v1", cleanRoom: true, toolLoopWarning: true, wantError: true},
		{name: "duplicate clean suffix enabled", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1+clean-room-v1", cleanRoom: true, wantError: true},
		{name: "duplicate clean suffix disabled", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1+clean-room-v1", wantError: true},
		{name: "duplicate warning suffix", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+tool-loop-warning-v1+tool-loop-warning-v1", toolLoopWarning: true, wantError: true},
		{name: "duplicate warning suffix disabled", protocol: "mini-swe-agent-v2.1-on-trpc-agent-go+tool-loop-warning-v1+tool-loop-warning-v1", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNativeAgentProtocol(tt.protocol, tt.cleanRoom, tt.toolLoopWarning)
			if (err != nil) != tt.wantError {
				t.Fatalf("validateNativeAgentProtocol() error = %v, wantError=%t", err, tt.wantError)
			}
		})
	}
}

func TestValidateToolLoopWarningManifest(t *testing.T) {
	for _, tt := range []struct {
		name                string
		enabled             bool
		count, cases, total int
		wantError           bool
	}{
		{name: "legacy missing fields", total: 500},
		{name: "enabled no events", enabled: true, total: 500},
		{name: "enabled events", enabled: true, count: 3, cases: 2, total: 500},
		{name: "disabled telemetry", count: 1, cases: 1, total: 500, wantError: true},
		{name: "negative", enabled: true, count: -1, total: 500, wantError: true},
		{name: "zero mismatch", enabled: true, count: 1, total: 500, wantError: true},
		{name: "too many cases", enabled: true, count: 2, cases: 2, total: 1, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToolLoopWarningManifest("test manifest", tt.enabled, tt.count, tt.cases, tt.total)
			if (err != nil) != tt.wantError {
				t.Fatalf("validateToolLoopWarningManifest() error = %v, wantError=%t", err, tt.wantError)
			}
		})
	}
}

func TestSummarizeShardPlanAcceptsCaseLevelResults(t *testing.T) {
	dir := t.TempDir()
	plan := testBatchPlan([]batchPlanItem{
		{
			Index:       0,
			Name:        "batch-000",
			RunID:       "run-000",
			InstanceIDs: []string{"case-a", "case-b", "case-c"},
		},
	})
	rawDir := filepath.Join(dir, "run-000", "raw", "mini")
	writeTestRunMiniManifest(t, rawDir, 15)
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {ModelNameOrPath: "model", InstanceID: "case-a", ModelPatch: "diff --git a/a b/a\n+++ b/a\n+ok\n"},
		"case-b": {ModelNameOrPath: "model", InstanceID: "case-b", ModelPatch: ""},
	})
	writeTestTrajectory(t, rawDir, "case-a", "Submitted")
	writeTestTrajectory(t, rawDir, "case-b", "LimitsExceeded")

	manifest, err := summarizeShardPlan(plan, filepath.Join(dir, "plan.json"), dir, filepath.Join("raw", "mini"))
	if err != nil {
		t.Fatalf("summarizeShardPlan() error = %v", err)
	}
	if manifest.AcceptedCases != 2 {
		t.Fatalf("AcceptedCases = %d, want 2", manifest.AcceptedCases)
	}
	if manifest.MissingCases != 1 {
		t.Fatalf("MissingCases = %d, want 1", manifest.MissingCases)
	}
	if got := manifest.Shards[0].Status; got != "partial" {
		t.Fatalf("shard status = %q, want partial", got)
	}
	if got := manifest.Shards[0].ExitStatusCounts["LimitsExceeded"]; got != 1 {
		t.Fatalf("LimitsExceeded count = %d, want 1", got)
	}
	if got := manifest.Shards[0].EmptyPatchCount; got != 1 {
		t.Fatalf("EmptyPatchCount = %d, want 1", got)
	}
}

func TestAcceptedPredictionsRejectsDuplicateCases(t *testing.T) {
	dir := t.TempDir()
	for _, runID := range []string{"run-000", "run-001"} {
		rawDir := filepath.Join(dir, runID, "raw", "mini")
		writeTestPreds(t, rawDir, map[string]contract.Prediction{
			"case-a": {ModelNameOrPath: "model", InstanceID: "case-a", ModelPatch: "patch"},
		})
	}
	manifest := shardsManifest{
		Shards: []shardSummary{
			{
				RunID:             "run-000",
				RawDir:            filepath.Join(dir, "run-000", "raw", "mini"),
				Status:            "accepted",
				AcceptedCount:     1,
				PredictionsSHA256: testFileSHA256(t, filepath.Join(dir, "run-000", "raw", "mini", "preds.json")),
				Cases:             []shardCaseSummary{{InstanceID: "case-a", Status: "accepted"}},
			},
			{
				RunID:             "run-001",
				RawDir:            filepath.Join(dir, "run-001", "raw", "mini"),
				Status:            "accepted",
				AcceptedCount:     1,
				PredictionsSHA256: testFileSHA256(t, filepath.Join(dir, "run-001", "raw", "mini", "preds.json")),
				Cases:             []shardCaseSummary{{InstanceID: "case-a", Status: "accepted"}},
			},
		},
	}
	_, err := acceptedPredictions(manifest)
	if err == nil || !strings.Contains(err.Error(), "duplicate accepted prediction") {
		t.Fatalf("acceptedPredictions() error = %v, want duplicate error", err)
	}
}

func TestValidateBatchPlanRejectsDuplicateCases(t *testing.T) {
	plan := testBatchPlan([]batchPlanItem{
		{Index: 0, Name: "batch-000", RunID: "run-000", InstanceIDs: []string{"case-a"}},
		{Index: 1, Name: "batch-001", RunID: "run-001", InstanceIDs: []string{"case-a"}},
	})
	if err := validateBatchPlan(plan); err == nil || !strings.Contains(err.Error(), "duplicate planned") {
		t.Fatalf("validateBatchPlan() error = %v, want duplicate case error", err)
	}
}

func TestSummarizeShardRejectsMismatchedRunManifest(t *testing.T) {
	dir := t.TempDir()
	batch := batchPlanItem{
		Index:       0,
		Name:        "batch-000",
		RunID:       "run-000",
		Size:        1,
		InstanceIDs: []string{"case-a"},
	}
	rawDir := filepath.Join(dir, "run-000", "raw", "mini")
	writeTestRunMiniManifest(t, rawDir, 1)
	var manifest runMiniManifest
	manifestPath := filepath.Join(rawDir, "run-mini-manifest.json")
	if err := readJSONFile(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.RunID = "other-run"
	if err := writeJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
	})
	writeTestTrajectory(t, rawDir, "case-a", "Submitted")
	shard := summarizeShard(batch, dir, filepath.Join("raw", "mini"))
	if shard.Status != "failed" || !strings.Contains(shard.FailureReason, "does not match") {
		t.Fatalf("shard = %+v", shard)
	}
}

func TestSummarizeShardAcceptsMiniGoManifestWithAgentError(t *testing.T) {
	dir := t.TempDir()
	batch := batchPlanItem{
		Index:       0,
		Name:        "batch-000",
		RunID:       "run-000",
		Size:        2,
		InstanceIDs: []string{"case-a", "case-b"},
	}
	rawDir := filepath.Join(dir, "run-000", "raw", "mini-go")
	writeTestMiniGoManifest(t, rawDir, batch, func(manifest *runnerManifest) {
		manifest.Status = "completed_with_errors"
	})
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
		"case-b": {InstanceID: "case-b", ModelPatch: ""},
	})
	writeTestTrajectory(t, rawDir, "case-a", "Submitted")
	writeTestTrajectory(t, rawDir, "case-b", "Error")

	shard := summarizeShard(batch, dir, filepath.Join("raw", "mini-go"))
	if shard.Status != "accepted" || shard.FailureReason != "" {
		t.Fatalf("shard = %+v, want accepted", shard)
	}
	if shard.Workers != 4 {
		t.Fatalf("Workers = %d, want 4", shard.Workers)
	}
	if shard.ExitStatusCounts["Error"] != 1 {
		t.Fatalf("Error count = %d, want 1", shard.ExitStatusCounts["Error"])
	}
	if want := testFileSHA256(t, filepath.Join(rawDir, "preds.json")); shard.PredictionsSHA256 != want {
		t.Fatalf("PredictionsSHA256 = %q, want %q", shard.PredictionsSHA256, want)
	}
}

func TestAcceptedPredictionsAcceptsMatchingSnapshot(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "run-000", "raw", "mini-go")
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
	})
	manifest := shardsManifest{Shards: []shardSummary{{
		RunID:             "run-000",
		RawDir:            rawDir,
		Status:            "accepted",
		AcceptedCount:     1,
		PredictionsSHA256: testFileSHA256(t, filepath.Join(rawDir, "preds.json")),
		Cases:             []shardCaseSummary{{InstanceID: "case-a", Status: "accepted"}},
	}}}

	predictions, err := acceptedPredictions(manifest)
	if err != nil {
		t.Fatalf("acceptedPredictions() error = %v", err)
	}
	if got := predictions["case-a"].ModelPatch; got != "patch" {
		t.Fatalf("ModelPatch = %q, want patch", got)
	}
}

func TestAcceptedPredictionsRejectsChangedSnapshot(t *testing.T) {
	dir := t.TempDir()
	plan := testBatchPlan([]batchPlanItem{{
		Index: 0, Name: "batch-000", RunID: "run-000", InstanceIDs: []string{"case-a"},
	}})
	writeTestMiniGoShard(t, dir, plan.Batches[0], nil)
	manifest, err := summarizeShardPlan(plan, filepath.Join(dir, "plan.json"), dir, filepath.Join("raw", "mini-go"))
	if err != nil {
		t.Fatalf("summarizeShardPlan() error = %v", err)
	}
	rawDir := filepath.Join(dir, "run-000", "raw", "mini-go")
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "tampered"},
	})

	_, err = acceptedPredictions(manifest)
	if err == nil || !strings.Contains(err.Error(), "does not match summarized") {
		t.Fatalf("acceptedPredictions() error = %v, want SHA mismatch", err)
	}
}

func TestSummarizeShardRejectsInvalidMiniGoManifest(t *testing.T) {
	tests := []struct {
		name string
		edit func(*runnerManifest)
		want string
	}{
		{name: "run id", edit: func(m *runnerManifest) { m.RunID = "other-run" }, want: "run_id"},
		{name: "runner type", edit: func(m *runnerManifest) { m.RunnerType = "other" }, want: "runner_type"},
		{name: "output directory", edit: func(m *runnerManifest) { m.OutputDir += "-other" }, want: "output_dir"},
		{name: "predictions", edit: func(m *runnerManifest) { m.Predictions += "-other" }, want: "predictions"},
		{name: "case count", edit: func(m *runnerManifest) { m.CaseCount++ }, want: "case_count"},
		{name: "workers", edit: func(m *runnerManifest) { m.Workers = 0 }, want: "workers"},
		{name: "missing time", edit: func(m *runnerManifest) { m.StartedAt = time.Time{} }, want: "times must be present"},
		{name: "reversed time", edit: func(m *runnerManifest) { m.FinishedAt = m.StartedAt.Add(-time.Second) }, want: "precedes"},
		{name: "duration", edit: func(m *runnerManifest) { m.DurationMS++ }, want: "duration_ms"},
		{name: "status", edit: func(m *runnerManifest) { m.Status = "failed" }, want: "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			batch := batchPlanItem{
				Index:       0,
				Name:        "batch-000",
				RunID:       "run-000",
				Size:        1,
				InstanceIDs: []string{"case-a"},
			}
			rawDir := filepath.Join(dir, "run-000", "raw", "mini-go")
			writeTestMiniGoManifest(t, rawDir, batch, tt.edit)
			writeTestPreds(t, rawDir, map[string]contract.Prediction{
				"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
			})
			writeTestTrajectory(t, rawDir, "case-a", "Submitted")

			shard := summarizeShard(batch, dir, filepath.Join("raw", "mini-go"))
			if shard.Status != "failed" || !strings.Contains(shard.FailureReason, tt.want) {
				t.Fatalf("shard = %+v, want failure containing %q", shard, tt.want)
			}
		})
	}
}

func TestSummarizeShardRejectsAmbiguousRunnerManifests(t *testing.T) {
	dir := t.TempDir()
	batch := batchPlanItem{
		Index:       0,
		Name:        "batch-000",
		RunID:       "run-000",
		Size:        1,
		InstanceIDs: []string{"case-a"},
	}
	rawDir := filepath.Join(dir, "run-000", "raw", "mini-go")
	writeTestRunMiniManifest(t, rawDir, 1)
	writeTestMiniGoManifest(t, rawDir, batch, nil)
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
	})
	writeTestTrajectory(t, rawDir, "case-a", "Submitted")

	shard := summarizeShard(batch, dir, filepath.Join("raw", "mini-go"))
	if shard.Status != "failed" || !strings.Contains(shard.FailureReason, "ambiguous runner manifests") {
		t.Fatalf("shard = %+v, want ambiguous-manifest failure", shard)
	}
}

func TestSummarizeShardPlanRejectsMiniGoIdentityMismatch(t *testing.T) {
	tests := []struct {
		name string
		edit func(*runnerManifest)
		want string
	}{
		{name: "codec", edit: func(m *runnerManifest) { m.ObservationCodec = "json" }, want: "observation_codec"},
		{name: "binary", edit: func(m *runnerManifest) { m.BinarySHA256 = strings.Repeat("2", 64) }, want: "binary_sha256"},
		{name: "model", edit: func(m *runnerManifest) { m.ModelConfigSHA256 = strings.Repeat("3", 64) }, want: "model_config_sha256"},
		{name: "cases", edit: func(m *runnerManifest) { m.CasesSHA256 = strings.Repeat("4", 64) }, want: "cases_sha256"},
		{name: "framework module", edit: func(m *runnerManifest) { m.FrameworkModule = "example.com/other/framework" }, want: "framework_module"},
		{name: "framework version", edit: func(m *runnerManifest) { m.FrameworkVersion = "v9.9.9" }, want: "framework_version"},
		{name: "source", edit: func(m *runnerManifest) { m.SourceRevision = strings.Repeat("5", 40) }, want: "source_revision"},
		{name: "dirty", edit: func(m *runnerManifest) { m.SourceModified = true }, want: "source_modified"},
		{name: "environment", edit: func(m *runnerManifest) { m.EnvironmentConfigSHA256 = strings.Repeat("6", 64) }, want: "environment_config_sha256"},
		{name: "command timeout", edit: func(m *runnerManifest) { m.CommandTimeout = "2m" }, want: "command_timeout"},
		{name: "case timeout", edit: func(m *runnerManifest) { m.CaseTimeout = "3h" }, want: "case_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			plan := testBatchPlan([]batchPlanItem{
				{Index: 0, Name: "batch-000", RunID: "run-000", InstanceIDs: []string{"case-a"}},
				{Index: 1, Name: "batch-001", RunID: "run-001", InstanceIDs: []string{"case-b"}},
			})
			writeTestMiniGoShard(t, dir, plan.Batches[0], nil)
			writeTestMiniGoShard(t, dir, plan.Batches[1], tt.edit)

			manifest, err := summarizeShardPlan(plan, filepath.Join(dir, "plan.json"), dir, filepath.Join("raw", "mini-go"))
			if err != nil {
				t.Fatalf("summarizeShardPlan() error = %v", err)
			}
			if got := manifest.RunnerIdentity; !reflect.DeepEqual(got, defaultMiniGoIdentity()) {
				t.Fatalf("RunnerIdentity = %+v, want canonical %+v", got, defaultMiniGoIdentity())
			}
			shard := manifest.Shards[1]
			if shard.Status != "failed" || !strings.Contains(shard.FailureReason, tt.want) {
				t.Fatalf("shard = %+v, want identity failure containing %q", shard, tt.want)
			}
			if _, err := acceptedPredictions(manifest); err == nil || !strings.Contains(err.Error(), "not mergeable") {
				t.Fatalf("acceptedPredictions() error = %v, want non-mergeable shard failure", err)
			}
		})
	}
}

func TestSummarizeShardPlanRejectsMixedRunnerManifestKinds(t *testing.T) {
	dir := t.TempDir()
	plan := testBatchPlan([]batchPlanItem{
		{Index: 0, Name: "batch-000", RunID: "run-000", InstanceIDs: []string{"case-a"}},
		{Index: 1, Name: "batch-001", RunID: "run-001", InstanceIDs: []string{"case-b"}},
	})
	writeTestMiniGoShard(t, dir, plan.Batches[0], nil)
	rawDir := filepath.Join(dir, "run-001", "raw", "mini-go")
	writeTestRunMiniManifest(t, rawDir, 1)
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-b": {InstanceID: "case-b", ModelPatch: "patch"},
	})
	writeTestTrajectory(t, rawDir, "case-b", "Submitted")

	manifest, err := summarizeShardPlan(plan, filepath.Join(dir, "plan.json"), dir, filepath.Join("raw", "mini-go"))
	if err != nil {
		t.Fatalf("summarizeShardPlan() error = %v", err)
	}
	shard := manifest.Shards[1]
	if shard.Status != "failed" || !strings.Contains(shard.FailureReason, "manifest_kind") {
		t.Fatalf("shard = %+v, want mixed-kind identity failure", shard)
	}
}

func TestSummarizeShardRejectsInvalidTimeoutsAndSelectedInstancesHash(t *testing.T) {
	tests := []struct {
		name string
		edit func(*runnerManifest)
		want string
	}{
		{name: "invalid command timeout", edit: func(m *runnerManifest) { m.CommandTimeout = "later" }, want: "command_timeout"},
		{name: "zero command timeout", edit: func(m *runnerManifest) { m.CommandTimeout = "0s" }, want: "command_timeout"},
		{name: "invalid case timeout", edit: func(m *runnerManifest) { m.CaseTimeout = "tomorrow" }, want: "case_timeout"},
		{name: "zero case timeout", edit: func(m *runnerManifest) { m.CaseTimeout = "0s" }, want: "case_timeout"},
		{name: "selection hash mismatch", edit: func(m *runnerManifest) { m.SelectedInstancesSHA256 = strings.Repeat("9", 64) }, want: "selected_instances_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			batch := batchPlanItem{
				Index:       0,
				Name:        "batch-000",
				RunID:       "run-000",
				Size:        2,
				InstanceIDs: []string{"case-b", "case-a"},
			}
			rawDir := filepath.Join(dir, batch.RunID, "raw", "mini-go")
			writeTestMiniGoManifest(t, rawDir, batch, tt.edit)
			writeTestPreds(t, rawDir, map[string]contract.Prediction{
				"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
				"case-b": {InstanceID: "case-b", ModelPatch: "patch"},
			})
			writeTestTrajectory(t, rawDir, "case-a", "Submitted")
			writeTestTrajectory(t, rawDir, "case-b", "Submitted")

			shard := summarizeShard(batch, dir, filepath.Join("raw", "mini-go"))
			if shard.Status != "failed" || !strings.Contains(shard.FailureReason, tt.want) {
				t.Fatalf("shard = %+v, want failure containing %q", shard, tt.want)
			}
		})
	}
}

func TestOrderPredictionsRequiresCanonicalCompleteness(t *testing.T) {
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "cases.jsonl")
	if err := os.WriteFile(casesPath, []byte(`{"instance_id":"case-a"}`+"\n"+`{"instance_id":"case-b"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := orderPredictions(map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a"},
	}, casesPath, false)
	if err == nil || !strings.Contains(err.Error(), "missing 1 canonical predictions") {
		t.Fatalf("orderPredictions() error = %v, want missing error", err)
	}
}

func testBatchPlan(items []batchPlanItem) batchPlan {
	count := 0
	for i := range items {
		items[i].Size = len(items[i].InstanceIDs)
		item := items[i]
		count += len(item.InstanceIDs)
	}
	return batchPlan{
		GeneratedAt: time.Now().UTC(),
		CaseCount:   count,
		BatchCount:  len(items),
		Batches:     items,
	}
}

func writeTestRunMiniManifest(t *testing.T, rawDir string, workers int) {
	t.Helper()
	start := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	manifest := runMiniManifest{
		RunID:      filepath.Base(filepath.Dir(filepath.Dir(rawDir))),
		StartedAt:  start,
		FinishedAt: start.Add(time.Minute),
		DurationMS: int64(time.Minute / time.Millisecond),
		Command:    commandResult{ExitCode: 0},
		Config:     runMiniConfig{Workers: workers, OutputDir: rawDir},
	}
	if err := writeJSON(filepath.Join(rawDir, "run-mini-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
}

func writeTestMiniGoManifest(t *testing.T, rawDir string, batch batchPlanItem, edit func(*runnerManifest)) {
	t.Helper()
	start := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	selectedSHA256, err := selectedInstancesSHA256(batch.InstanceIDs)
	if err != nil {
		t.Fatal(err)
	}
	manifest := runnerManifest{
		RunID:                   batch.RunID,
		RunnerType:              "mini-swe-agent-go",
		ObservationCodec:        "xml",
		FrameworkModule:         "trpc.group/trpc-go/trpc-agent-go",
		FrameworkVersion:        "v1.2.3",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		CasesSHA256:             strings.Repeat("c", 64),
		ModelConfigSHA256:       strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64),
		SelectedInstancesSHA256: selectedSHA256,
		CommandTimeout:          "1m0s",
		CaseTimeout:             "2h0m0s",
		StartedAt:               start,
		FinishedAt:              start.Add(time.Minute),
		DurationMS:              int64(time.Minute / time.Millisecond),
		OutputDir:               rawDir,
		CaseCount:               len(batch.InstanceIDs),
		Workers:                 4,
		Predictions:             filepath.Join(rawDir, "preds.json"),
		Status:                  "completed",
	}
	if edit != nil {
		edit(&manifest)
	}
	if err := writeJSON(filepath.Join(rawDir, "mini-go-runner-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
}

func writeTestMiniGoShard(t *testing.T, runsRoot string, batch batchPlanItem, edit func(*runnerManifest)) {
	t.Helper()
	rawDir := filepath.Join(runsRoot, batch.RunID, "raw", "mini-go")
	writeTestMiniGoManifest(t, rawDir, batch, edit)
	predictions := make(map[string]contract.Prediction, len(batch.InstanceIDs))
	for _, id := range batch.InstanceIDs {
		predictions[id] = contract.Prediction{InstanceID: id, ModelPatch: "patch"}
		writeTestTrajectory(t, rawDir, id, "Submitted")
	}
	writeTestPreds(t, rawDir, predictions)
}

func defaultMiniGoIdentity() shardRunnerIdentity {
	return shardRunnerIdentity{
		ManifestKind:            "mini-go",
		RunnerType:              "mini-swe-agent-go",
		ObservationCodec:        "xml",
		FrameworkModule:         "trpc.group/trpc-go/trpc-agent-go",
		FrameworkVersion:        "v1.2.3",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		CasesSHA256:             strings.Repeat("c", 64),
		ModelConfigSHA256:       strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64),
		CommandTimeout:          "1m0s",
		CaseTimeout:             "2h0m0s",
	}
}

func testFileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeTestPreds(t *testing.T, rawDir string, preds map[string]contract.Prediction) {
	t.Helper()
	if err := writeJSON(filepath.Join(rawDir, "preds.json"), preds); err != nil {
		t.Fatal(err)
	}
}

func writeTestTrajectory(t *testing.T, rawDir, instanceID, exitStatus string) {
	t.Helper()
	data := map[string]any{
		"instance_id": instanceID,
		"info": map[string]any{
			"exit_status": exitStatus,
		},
		"messages": []map[string]any{
			{"role": "exit", "content": exitStatus, "extra": map[string]any{"exit_status": exitStatus}},
		},
	}
	if err := writeJSON(filepath.Join(rawDir, instanceID, instanceID+".traj.json"), data); err != nil {
		t.Fatal(err)
	}
}
