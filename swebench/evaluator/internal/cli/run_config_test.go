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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

func TestValidateCleanRoomIdentityBindsImageAndAssetDigests(t *testing.T) {
	images := map[string]sweenv.ImageIdentity{
		"example.test/image:tag": {
			Reference: "example.test/image:tag",
			ID:        "sha256:" + strings.Repeat("a", 64),
		},
	}
	imageSetSHA256, err := sweenv.ImageSetSHA256(images)
	if err != nil {
		t.Fatal(err)
	}
	assets := &sweenv.OfflineAssetIdentity{
		Schema:         "swebench-offline-assets-v1",
		SHA256:         strings.Repeat("1", 64),
		ManifestSHA256: strings.Repeat("2", 64),
		FileCount:      1,
	}
	if err := validateCleanRoomIdentity(
		"test",
		true,
		strings.Repeat("3", 64),
		assets,
		imageSetSHA256,
		images,
	); err != nil {
		t.Fatalf("validateCleanRoomIdentity() error = %v", err)
	}
	if err := validateCleanRoomIdentity("test", false, strings.Repeat("3", 64), nil, "", nil); err == nil {
		t.Fatal("validateCleanRoomIdentity() accepted provenance with clean_room=false")
	}
	mutated := cloneDockerImages(images)
	mutated["example.test/image:tag"] = sweenv.ImageIdentity{
		Reference: "example.test/image:tag",
		ID:        "sha256:" + strings.Repeat("b", 64),
	}
	if err := validateCleanRoomIdentity(
		"test", true, strings.Repeat("3", 64), assets, imageSetSHA256, mutated,
	); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("validateCleanRoomIdentity() error = %v, want image-set mismatch", err)
	}
}

func TestRunConfigSupportsGenericNativeRunnerManifest(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	caseListHash, err := selectedInstancesSHA256([]string{"case-a"})
	if err != nil {
		t.Fatal(err)
	}

	casesDir := filepath.Join(dir, "data")
	casesManifestPath := filepath.Join(casesDir, "cases.manifest.json")
	casesJSONLPath := filepath.Join(casesDir, "cases.jsonl")
	if err := os.MkdirAll(casesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(casesJSONLPath, []byte("{\"instance_id\":\"case-a\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	casesSHA256 := testFileSHA256(t, casesJSONLPath)
	if err := writeJSON(casesManifestPath, prepareDataManifest{
		Dataset:          defaultDatasetName,
		Split:            defaultSplit,
		CaseCount:        1,
		CaseListHash:     caseListHash,
		CasesJSONLSHA256: casesSHA256,
		HintsTextPolicy:  "excluded",
		OutputDir:        casesDir,
	}); err != nil {
		t.Fatal(err)
	}

	runnerDir := filepath.Join(dir, "native")
	predictionsPath := filepath.Join(runnerDir, "preds.json")
	runnerManifestPath := filepath.Join(runnerDir, "run-mini-looking-generic.json")
	nativeManifest := runnerManifest{
		RunID:                   "native-run",
		RunnerType:              "trpc-agent-go-native",
		AgentProtocol:           "mini-swe-agent-v2.1-on-trpc-agent-go",
		UpstreamCommit:          strings.Repeat("f", 40),
		ObservationCodec:        "xml",
		FrameworkModule:         "trpc.group/trpc-go/trpc-agent-go",
		FrameworkVersion:        "v1.10.1-0.20260728070417-4237accb70cb",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		CasesSHA256:             casesSHA256,
		ModelConfigSHA256:       strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64),
		SelectedInstancesSHA256: caseListHash,
		CommandTimeout:          "1m",
		CaseTimeout:             "2h",
		StartedAt:               start,
		FinishedAt:              start.Add(2 * time.Minute),
		DurationMS:              int64((2 * time.Minute) / time.Millisecond),
		OutputDir:               runnerDir,
		CaseCount:               1,
		Workers:                 1,
		Predictions:             predictionsPath,
		Status:                  "completed",
		ModelConfig:             map[string]string{"MODEL_NAME": "test-model"},
	}
	if err := writeJSON(runnerManifestPath, nativeManifest); err != nil {
		t.Fatal(err)
	}
	prediction := contract.Prediction{
		InstanceID:      "case-a",
		ModelNameOrPath: "trpc-agent-go/test-model",
		ModelPatch:      "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n",
	}
	if err := writeJSON(predictionsPath, map[string]contract.Prediction{"case-a": prediction}); err != nil {
		t.Fatal(err)
	}
	nativeResult := writeNativeBindingArtifacts(
		t,
		nativeManifest,
		filepath.Join(dir, "imported"),
		"native",
		prediction,
	)

	verifyDir := filepath.Join(dir, "verify")
	harnessReportPath := filepath.Join(verifyDir, "test-model.native-run-native.json")
	if err := os.MkdirAll(verifyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(harnessReportPath, map[string]any{"unresolved_ids": []string{"case-a"}}); err != nil {
		t.Fatal(err)
	}
	verifyManifestPath := filepath.Join(dir, "verify", "verifier_manifest.json")
	verifier := testVerifierWithHarnessReport(t, "native-run", "native", harnessReportPath)
	verifier.StartedAt = start.Add(3 * time.Minute)
	verifier.FinishedAt = start.Add(4 * time.Minute)
	verifier.DurationMS = int64(time.Minute / time.Millisecond)
	verifier.Harness = harnessIdentity{
		Version:  "4.0.3",
		Revision: "abc123",
	}
	verifier.Config = verifyConfig{
		OutputDir:   verifyDir,
		InstanceIDs: []string{"case-a"},
		Workers:     8,
		TimeoutSec:  1800,
		CacheLevel:  "instance",
		Dataset:     defaultDatasetName,
		Split:       defaultSplit,
		Predictions: predictionsPath,
		DockerHost:  defaultDockerHost,
		Python:      "python",
		Clean:       false,
	}
	attestVerifierPredictions(t, &verifier, predictionsPath)
	if err := writeJSON(verifyManifestPath, verifier); err != nil {
		t.Fatal(err)
	}

	importSummaryPath := filepath.Join(dir, "imported", "summary", "native.json")
	if err := os.MkdirAll(filepath.Join(dir, "imported"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportedCasesJSONL(t, filepath.Join(dir, "imported", "cases.jsonl"), []importedCase{{
		SchemaVersion: importSchemaVersion,
		InstanceID:    "case-a",
		Target:        "native",
		Result: func() targetResult {
			nativeResult.MainStatus = "unresolved"
			nativeResult.FailureReason = "failed official harness"
			nativeResult.VerifierResultRef = absPath(harnessReportPath)
			return nativeResult
		}(),
	}})
	if err := writeJSON(importSummaryPath, importSummary{
		SchemaVersion: importSchemaVersion,
		GeneratedAt:   start.Add(5 * time.Minute),
		Target:        "native",
		Total:         1,
		Counts:        map[string]int{"unresolved": 1},
	}); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "run_config.json")
	err = runRunConfig([]string{
		"--run-id", "native-run",
		"--target", "native",
		"--cases-manifest", casesManifestPath,
		"--runner-manifest", runnerManifestPath,
		"--verifier-manifest", verifyManifestPath,
		"--import-summary", importSummaryPath,
		"--harness-report", harnessReportPath,
		"--model-name", "test-model",
		"--output", outputPath,
	})
	if err != nil {
		t.Fatalf("runRunConfig() error = %v", err)
	}

	var doc runConfigDocument
	if err := readJSONFile(outputPath, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Runner.Type != "trpc-agent-go-native" {
		t.Fatalf("Runner.Type = %q, want trpc-agent-go-native", doc.Runner.Type)
	}
	if doc.Selection.CaseCount != 1 || doc.Selection.CaseListHash != caseListHash {
		t.Fatalf("Selection = %+v, want one selected case with hash %q", doc.Selection, caseListHash)
	}
	if doc.Concurrency.AgentGenerationWorkers != 1 {
		t.Fatalf("AgentGenerationWorkers = %d, want 1 from native manifest", doc.Concurrency.AgentGenerationWorkers)
	}
	if doc.Artifacts.RunnerOutputDir != runnerDir {
		t.Fatalf("RunnerOutputDir = %q, want %q", doc.Artifacts.RunnerOutputDir, runnerDir)
	}
	if doc.Artifacts.MiniRawDir != "" {
		t.Fatalf("MiniRawDir = %q, want empty for native", doc.Artifacts.MiniRawDir)
	}
	if doc.SourceFiles.RunMiniManifest != "" {
		t.Fatalf("RunMiniManifest = %q, want empty for native", doc.SourceFiles.RunMiniManifest)
	}
	if doc.Verifier.HarnessRunID != "native-run-native" {
		t.Fatalf("Verifier.HarnessRunID = %q, want native-run-native", doc.Verifier.HarnessRunID)
	}
	if doc.Artifacts.HarnessReportSHA256 != testFileSHA256(t, harnessReportPath) {
		t.Fatalf("HarnessReportSHA256 = %q, want bound report digest", doc.Artifacts.HarnessReportSHA256)
	}
	if doc.Artifacts.PredictionsSHA256 != testFileSHA256(t, predictionsPath) {
		t.Fatalf("PredictionsSHA256 = %q, want bound predictions digest", doc.Artifacts.PredictionsSHA256)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected run_config.json: %v", err)
	}
}

func TestRunConfigSupportsFilteredNativeImportWithFullCasesManifest(t *testing.T) {
	for _, tt := range []struct {
		name      string
		cleanRoom bool
	}{
		{name: "default-off"},
		{name: "clean-room", cleanRoom: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testRunConfigSupportsFilteredNativeImportWithFullCasesManifest(t, tt.cleanRoom)
		})
	}
}

func testRunConfigSupportsFilteredNativeImportWithFullCasesManifest(t *testing.T, cleanRoom bool) {
	t.Helper()
	dir := t.TempDir()
	instanceID := "psf__requests-2317"
	repo := "psf/requests"
	baseCommit := strings.Repeat("c", 40)
	fullIDs := []string{instanceID, "sympy__sympy-20590"}
	fixture := writeRunConfigSelectionFixture(t, dir, fullIDs, []string{instanceID}, "native")
	casesJSONLPath := filepath.Join(fixture.cases.OutputDir, "cases.jsonl")
	writeCasesJSONL(t, casesJSONLPath, []contract.Case{
		{InstanceID: instanceID, Repo: repo, BaseCommit: baseCommit},
		{InstanceID: fullIDs[1], Repo: "sympy/sympy", BaseCommit: strings.Repeat("d", 40)},
	})
	fixture.cases.CasesJSONLSHA256 = testFileSHA256(t, casesJSONLPath)
	if err := writeJSON(fixture.casesManifestPath, fixture.cases); err != nil {
		t.Fatal(err)
	}
	selectedHash, err := selectedInstancesSHA256([]string{instanceID})
	if err != nil {
		t.Fatal(err)
	}
	nativeArtifact := validNativeArtifact()
	nativeArtifact["instance_id"] = instanceID
	nativeInfo := nativeArtifact["info"].(map[string]any)
	agentProtocol := "mini-swe-agent-v2.1-on-trpc-agent-go"
	var dockerImages map[string]sweenv.ImageIdentity
	var offlineAssets *sweenv.OfflineAssetIdentity
	imageSetSHA256 := ""
	cleanRoomPolicySHA256 := ""
	if cleanRoom {
		nativeArtifact, dockerImages, offlineAssets = cleanRoomNativeArtifact(t, instanceID, repo, baseCommit)
		nativeInfo = nativeArtifact["info"].(map[string]any)
		agentProtocol += "+clean-room-v1"
		cleanRoomPolicySHA256 = strings.Repeat("3", 64)
		imageSetSHA256, err = sweenv.ImageSetSHA256(dockerImages)
		if err != nil {
			t.Fatal(err)
		}
	}

	runnerDir := filepath.Join(dir, "native")
	predictionsPath := fixture.predictionsPath
	runnerManifestPath := filepath.Join(runnerDir, "native-runner-manifest.json")
	nativeManifest := runnerManifest{
		RunID:                   "native-filtered",
		RunnerType:              "trpc-agent-go-native",
		AgentProtocol:           agentProtocol,
		UpstreamCommit:          strings.Repeat("f", 40),
		ObservationCodec:        "xml",
		FrameworkModule:         "trpc.group/trpc-go/trpc-agent-go",
		FrameworkVersion:        "v1.10.1-0.20260728070417-4237accb70cb",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		CasesSHA256:             fixture.cases.CasesJSONLSHA256,
		ModelConfigSHA256:       strings.Repeat("d", 64),
		EnvironmentConfigSHA256: strings.Repeat("e", 64),
		SelectedInstancesSHA256: selectedHash,
		CommandTimeout:          "1m",
		CaseTimeout:             "2h",
		CleanRoom:               cleanRoom,
		CleanRoomPolicySHA256:   cleanRoomPolicySHA256,
		OfflineAssets:           offlineAssets,
		ImageSetSHA256:          imageSetSHA256,
		DockerImages:            dockerImages,
		OutputDir:               runnerDir,
		CaseCount:               1,
		Workers:                 1,
		Predictions:             predictionsPath,
		Status:                  "completed",
		ModelConfig:             map[string]string{"MODEL_NAME": "test-model"},
	}
	if err := writeJSON(runnerManifestPath, nativeManifest); err != nil {
		t.Fatal(err)
	}
	prediction := contract.Prediction{
		InstanceID:      instanceID,
		ModelNameOrPath: "trpc-agent-go/test-model",
		ModelPatch:      "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+filtered\n",
	}
	if err := writeJSON(predictionsPath, map[string]contract.Prediction{instanceID: prediction}); err != nil {
		t.Fatal(err)
	}
	caseDir := filepath.Join(runnerDir, instanceID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(caseDir, instanceID+".responses.json"), []any{}); err != nil {
		t.Fatal(err)
	}
	nativeArtifact["model_patch"] = prediction.ModelPatch
	for key, value := range map[string]any{
		"run_id":                    nativeManifest.RunID,
		"observation_codec":         nativeManifest.ObservationCodec,
		"source_revision":           nativeManifest.SourceRevision,
		"source_modified":           nativeManifest.SourceModified,
		"binary_sha256":             nativeManifest.BinarySHA256,
		"model_config_sha256":       nativeManifest.ModelConfigSHA256,
		"environment_config_sha256": nativeManifest.EnvironmentConfigSHA256,
		"cases_sha256":              nativeManifest.CasesSHA256,
		"command_timeout":           nativeManifest.CommandTimeout,
		"case_timeout":              nativeManifest.CaseTimeout,
		"selected_instances_sha256": nativeManifest.SelectedInstancesSHA256,
		"workers":                   nativeManifest.Workers,
	} {
		nativeInfo[key] = value
	}
	if err := writeJSON(filepath.Join(caseDir, instanceID+".native.json"), nativeArtifact); err != nil {
		t.Fatal(err)
	}
	verifyDir := filepath.Join(dir, "verify")
	harnessReportPath := filepath.Join(verifyDir, "test-model.native-filtered-native.json")
	if err := os.MkdirAll(verifyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(harnessReportPath, map[string]any{"unresolved_ids": []string{instanceID}}); err != nil {
		t.Fatal(err)
	}
	importedDir := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", "native",
		"--predictions", predictionsPath,
		"--raw-dir", runnerDir,
		"--harness-report", harnessReportPath,
		"--output", importedDir,
	}); err != nil {
		t.Fatalf("runImport() error = %v", err)
	}
	importedRows, err := readAndValidateImportedCases(
		filepath.Join(importedDir, "cases.jsonl"),
		importSummary{
			SchemaVersion: importSchemaVersion,
			Target:        "native",
			Total:         1,
			Counts:        map[string]int{"unresolved": 1},
		},
		"native",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(importedRows) != 1 {
		t.Fatalf("filtered Native import rows = %+v, want one row", importedRows)
	}
	if cleanRoom && (importedRows[0].Repo != repo || importedRows[0].BaseCommit != baseCommit) {
		t.Fatalf("filtered clean-room import row = %+v, want trace-bound canonical metadata", importedRows[0])
	}
	if !cleanRoom && (importedRows[0].Repo != "" || importedRows[0].BaseCommit != "") {
		t.Fatalf("filtered default-off import row = %+v, want legacy empty metadata", importedRows[0])
	}

	verifierManifestPath := filepath.Join(dir, "verify", "verifier_manifest.json")
	verifier := testVerifierWithHarnessReport(t, "native-filtered", "native", harnessReportPath)
	verifier.Harness = harnessIdentity{
		Version: "4.0.3",
	}
	verifier.Config = verifyConfig{
		Dataset:     defaultDatasetName,
		Split:       defaultSplit,
		InstanceIDs: []string{instanceID},
		Predictions: predictionsPath,
		OutputDir:   verifyDir,
		Workers:     1,
		TimeoutSec:  1800,
	}
	attestVerifierPredictions(t, &verifier, predictionsPath)
	if err := writeJSON(verifierManifestPath, verifier); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "run_config.json")
	if err := runRunConfig([]string{
		"--run-id", "native-filtered",
		"--target", "native",
		"--cases-manifest", fixture.casesManifestPath,
		"--runner-manifest", runnerManifestPath,
		"--verifier-manifest", verifierManifestPath,
		"--import-summary", fixture.importSummaryPath,
		"--harness-report", harnessReportPath,
		"--model-name", "test-model",
		"--output", outputPath,
	}); err != nil {
		t.Fatalf("runRunConfig() error = %v", err)
	}

	var doc runConfigDocument
	if err := readJSONFile(outputPath, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Dataset.CaseCount != 2 || doc.Dataset.CaseListHash != fixture.cases.CaseListHash {
		t.Fatalf("Dataset = %+v, want full two-case panel identity", doc.Dataset)
	}
	if doc.Dataset.CasesJSONLSHA256 != fixture.cases.CasesJSONLSHA256 {
		t.Fatalf("Dataset.CasesJSONLSHA256 = %q, want %q", doc.Dataset.CasesJSONLSHA256, fixture.cases.CasesJSONLSHA256)
	}
	if doc.Selection.CaseCount != 1 || doc.Selection.CaseListHash != selectedHash {
		t.Fatalf("Selection = %+v, want filtered case identity %q", doc.Selection, selectedHash)
	}
	if doc.Runner.CleanRoom != cleanRoom || doc.Runner.CleanRoomPolicySHA256 != nativeManifest.CleanRoomPolicySHA256 {
		t.Fatalf("Runner = %+v, want clean_room=%t with matching provenance", doc.Runner, cleanRoom)
	}
}

func TestValidateRunConfigSelectionRejectsVerifierMismatch(t *testing.T) {
	dir := t.TempDir()
	fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a", "case-b"}, []string{"case-a"}, "native")
	selectedHash, err := selectedInstancesSHA256([]string{"case-a"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateRunConfigSelection(
		fixture.cases,
		runnerManifest{CaseCount: 1, SelectedInstancesSHA256: selectedHash},
		shardsManifest{},
		verifyManifest{Config: verifyConfig{InstanceIDs: []string{"case-b"}, Predictions: fixture.predictionsPath}},
		fixture.importSummaryPath,
		fixture.importSummary,
		"native",
		"",
		false,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "verifier instance_ids hash") {
		t.Fatalf("validateRunConfigSelection() error = %v, want verifier selection mismatch", err)
	}
}

func TestValidateRunConfigSelectionRejectsInstanceOutsideFullManifest(t *testing.T) {
	dir := t.TempDir()
	fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a", "case-b"}, []string{"case-z"}, "native")
	selectedHash, err := selectedInstancesSHA256([]string{"case-z"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateRunConfigSelection(
		fixture.cases,
		runnerManifest{CaseCount: 1, SelectedInstancesSHA256: selectedHash},
		shardsManifest{},
		verifyManifest{Config: verifyConfig{InstanceIDs: []string{"case-z"}, Predictions: fixture.predictionsPath}},
		fixture.importSummaryPath,
		fixture.importSummary,
		"native",
		"",
		false,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "not present in full cases manifest") {
		t.Fatalf("validateRunConfigSelection() error = %v, want full-manifest subset error", err)
	}
}

func TestValidateRunConfigSelectionSupportsFilteredLegacyMini(t *testing.T) {
	dir := t.TempDir()
	fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a", "case-b"}, []string{"case-a"}, "mini")
	selection, err := validateRunConfigSelection(
		fixture.cases,
		runnerManifest{},
		shardsManifest{},
		verifyManifest{Config: verifyConfig{InstanceIDs: []string{"case-a"}, Predictions: fixture.predictionsPath}},
		fixture.importSummaryPath,
		fixture.importSummary,
		"mini",
		"",
		true,
		false,
	)
	if err != nil {
		t.Fatalf("validateRunConfigSelection() error = %v", err)
	}
	if selection.CaseCount != 1 {
		t.Fatalf("selection = %+v, want one filtered legacy Mini case", selection)
	}
}

func TestValidateRunConfigSelectionSupportsShards(t *testing.T) {
	dir := t.TempDir()
	fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a", "case-b"}, []string{"case-a"}, "mini-go")
	selection, err := validateRunConfigSelection(
		fixture.cases,
		runnerManifest{},
		shardsManifest{
			ExpectedCases: 1,
			Shards: []shardSummary{{
				ExpectedIDs: []string{"case-a"},
			}},
		},
		verifyManifest{Config: verifyConfig{InstanceIDs: []string{"case-a"}, Predictions: fixture.predictionsPath}},
		fixture.importSummaryPath,
		fixture.importSummary,
		"mini-go",
		"",
		false,
		true,
	)
	if err != nil {
		t.Fatalf("validateRunConfigSelection() error = %v", err)
	}
	if selection.CaseCount != 1 {
		t.Fatalf("selection = %+v, want one sharded case", selection)
	}
}

func TestValidateImportedCaseBindingsValidatesCaseProvenance(t *testing.T) {
	prediction := contract.Prediction{
		InstanceID:      "case-a",
		ModelNameOrPath: "test-model",
	}
	expectedCases := map[string]contract.Case{
		"case-a": {
			InstanceID: "case-a",
			Repo:       "owner/repo",
			BaseCommit: "0123456789abcdef",
		},
	}
	baseRow := importedCase{
		SchemaVersion: importSchemaVersion,
		InstanceID:    "case-a",
		Target:        "native",
		Result: targetResult{
			MainStatus:      "empty_patch",
			FailureReason:   "empty model_patch",
			ModelNameOrPath: "test-model",
			PatchStats:      artifact.PatchStats{ChangedFiles: []string{}},
		},
	}
	tests := []struct {
		name         string
		repo         string
		baseCommit   string
		requireExact bool
		wantErr      bool
	}{
		{name: "full exact", repo: "owner/repo", baseCommit: "0123456789abcdef", requireExact: true},
		{name: "full empty", requireExact: true, wantErr: true},
		{name: "filtered exact", repo: "owner/repo", baseCommit: "0123456789abcdef"},
		{name: "filtered both empty"},
		{name: "filtered wrong repo", repo: "other/repo", baseCommit: "0123456789abcdef", wantErr: true},
		{name: "filtered partial metadata", repo: "owner/repo", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := baseRow
			row.Repo = tt.repo
			row.BaseCommit = tt.baseCommit
			err := validateImportedCaseBindings(
				[]importedCase{row},
				map[string]contract.Prediction{"case-a": prediction},
				expectedCases,
				tt.requireExact,
				t.TempDir(),
				"native",
				"",
			)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "repo/base_commit") {
					t.Fatalf("validateImportedCaseBindings() error = %v, want provenance mismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateImportedCaseBindings() error = %v", err)
			}
		})
	}
}

func TestValidateRunConfigSelectionRejectsUnboundImportedRows(t *testing.T) {
	tests := []struct {
		name        string
		writeRows   func(*testing.T, string)
		editSummary func(*importSummary)
		want        string
	}{
		{
			name: "wrong target",
			writeRows: func(t *testing.T, path string) {
				writeImportedCasesJSONL(t, path, []importedCase{{
					SchemaVersion: importSchemaVersion,
					InstanceID:    "case-a",
					Target:        "other",
					Result:        targetResult{MainStatus: "empty_patch"},
				}})
			},
			want: "target \"other\" does not match",
		},
		{
			name: "wrong schema",
			writeRows: func(t *testing.T, path string) {
				writeImportedCasesJSONL(t, path, []importedCase{{
					SchemaVersion: importSchemaVersion + 1,
					InstanceID:    "case-a",
					Target:        "native",
					Result:        targetResult{MainStatus: "empty_patch"},
				}})
			},
			want: "schema_version 2",
		},
		{
			name: "duplicate row",
			writeRows: func(t *testing.T, path string) {
				row := importedCase{
					SchemaVersion: importSchemaVersion,
					InstanceID:    "case-a",
					Target:        "native",
					Result:        targetResult{MainStatus: "empty_patch"},
				}
				writeImportedCasesJSONL(t, path, []importedCase{row, row})
			},
			want: "duplicate imported instance id",
		},
		{
			name: "missing status",
			writeRows: func(t *testing.T, path string) {
				writeImportedCasesJSONL(t, path, []importedCase{{
					SchemaVersion: importSchemaVersion,
					InstanceID:    "case-a",
					Target:        "native",
				}})
			},
			want: "empty result.main_status",
		},
		{
			name: "unknown status",
			writeRows: func(t *testing.T, path string) {
				writeImportedCasesJSONL(t, path, []importedCase{{
					SchemaVersion: importSchemaVersion,
					InstanceID:    "case-a",
					Target:        "native",
					Result:        targetResult{MainStatus: "mystery"},
				}})
			},
			want: "unsupported result.main_status",
		},
		{
			name: "malformed row",
			writeRows: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "decode line 1",
		},
		{
			name: "count mismatch",
			writeRows: func(t *testing.T, path string) {
				writeImportedCasesJSONL(t, path, []importedCase{{
					SchemaVersion: importSchemaVersion,
					InstanceID:    "case-a",
					Target:        "native",
					Result:        targetResult{MainStatus: "resolved"},
				}})
			},
			want: "status counts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a"}, []string{"case-a"}, "native")
			rowsPath := filepath.Join(dir, "imported", "cases.jsonl")
			tt.writeRows(t, rowsPath)
			summary := fixture.importSummary
			if tt.editSummary != nil {
				tt.editSummary(&summary)
			}
			selectedHash, err := selectedInstancesSHA256([]string{"case-a"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = validateRunConfigSelection(
				fixture.cases,
				runnerManifest{CaseCount: 1, SelectedInstancesSHA256: selectedHash},
				shardsManifest{},
				verifyManifest{Config: verifyConfig{InstanceIDs: []string{"case-a"}, Predictions: fixture.predictionsPath}},
				fixture.importSummaryPath,
				summary,
				"native",
				"",
				false,
				false,
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRunConfigSelection() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateRunConfigSelectionRejectsPatchSplice(t *testing.T) {
	dir := t.TempDir()
	fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a"}, []string{"case-a"}, "tag")
	prediction := contract.Prediction{
		InstanceID:      "case-a",
		ModelNameOrPath: "test-model",
		ModelPatch:      "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n",
	}
	if err := writeJSON(fixture.predictionsPath, map[string]contract.Prediction{"case-a": prediction}); err != nil {
		t.Fatal(err)
	}
	patchRel := filepath.Join("patches", "tag", "case-a.patch")
	patchPath := filepath.Join(dir, "imported", patchRel)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchPath, []byte("spliced"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeImportedCasesJSONL(t, filepath.Join(dir, "imported", "cases.jsonl"), []importedCase{{
		SchemaVersion: importSchemaVersion,
		InstanceID:    "case-a",
		Target:        "tag",
		Result: targetResult{
			MainStatus:      "incomplete",
			FailureReason:   "missing harness result",
			ModelNameOrPath: "test-model",
			PatchPath:       patchRel,
			PatchStats:      artifact.ComputePatchStats(prediction.ModelPatch),
		},
	}})
	fixture.importSummary.Counts = map[string]int{"incomplete": 1}
	selectionHash, _ := selectedInstancesSHA256([]string{"case-a"})
	_, err := validateRunConfigSelection(
		fixture.cases,
		runnerManifest{CaseCount: 1, SelectedInstancesSHA256: selectionHash},
		shardsManifest{},
		verifyManifest{Config: verifyConfig{InstanceIDs: []string{"case-a"}, Predictions: fixture.predictionsPath}},
		fixture.importSummaryPath,
		fixture.importSummary,
		"tag",
		"",
		false,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "does not exactly match prediction model_patch") {
		t.Fatalf("validateRunConfigSelection() error = %v, want patch splice rejection", err)
	}
}

func TestValidateRunConfigSelectionRejectsSameCountStatusSwap(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"case-a", "case-b"}
	fixture := writeRunConfigSelectionFixture(t, dir, ids, ids, "tag")
	predictions := map[string]contract.Prediction{}
	rows := make([]importedCase, 0, len(ids))
	harnessPath := filepath.Join(dir, "verify", "test-model.run-1-tag.json")
	if err := os.MkdirAll(filepath.Dir(harnessPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(harnessPath, map[string]any{
		"resolved_ids":   []string{"case-a"},
		"unresolved_ids": []string{"case-b"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		prediction := contract.Prediction{
			InstanceID:      id,
			ModelNameOrPath: "test-model",
			ModelPatch:      "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+" + id + "\n",
		}
		predictions[id] = prediction
		patchRel := filepath.Join("patches", "tag", id+".patch")
		patchPath := filepath.Join(dir, "imported", patchRel)
		if err := os.MkdirAll(filepath.Dir(patchPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(patchPath, []byte(prediction.ModelPatch), 0o644); err != nil {
			t.Fatal(err)
		}
		status, reason := "resolved", ""
		if id == "case-a" {
			status, reason = "unresolved", "failed official harness"
		}
		rows = append(rows, importedCase{
			SchemaVersion: importSchemaVersion,
			InstanceID:    id,
			Target:        "tag",
			Result: targetResult{
				MainStatus:        status,
				FailureReason:     reason,
				ModelNameOrPath:   "test-model",
				PatchPath:         patchRel,
				VerifierResultRef: absPath(harnessPath),
				PatchStats:        artifact.ComputePatchStats(prediction.ModelPatch),
			},
		})
	}
	if err := writeJSON(fixture.predictionsPath, predictions); err != nil {
		t.Fatal(err)
	}
	writeImportedCasesJSONL(t, filepath.Join(dir, "imported", "cases.jsonl"), rows)
	fixture.importSummary.Counts = map[string]int{"resolved": 1, "unresolved": 1}
	selectionHash, _ := selectedInstancesSHA256(ids)
	verifier := testVerifierWithHarnessReport(t, "run-1", "tag", harnessPath)
	verifier.Config.InstanceIDs = ids
	verifier.Config.Predictions = fixture.predictionsPath
	_, err := validateRunConfigSelection(
		fixture.cases,
		runnerManifest{CaseCount: 2, SelectedInstancesSHA256: selectionHash},
		shardsManifest{},
		verifier,
		fixture.importSummaryPath,
		fixture.importSummary,
		"tag",
		harnessPath,
		false,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "does not match recomputed") {
		t.Fatalf("validateRunConfigSelection() error = %v, want per-case status swap rejection", err)
	}
}

func TestValidateHarnessReportBinding(t *testing.T) {
	tests := []struct {
		name              string
		requireAttested   bool
		removeAttestation bool
		mutate            func(*testing.T, *verifyManifest, string)
		want              string
	}{
		{name: "attested native", requireAttested: true},
		{name: "legacy mini", removeAttestation: true},
		{name: "native requires attestation", requireAttested: true, removeAttestation: true, want: "no verify-time report attestation"},
		{
			name:            "recorded report error",
			requireAttested: true,
			mutate: func(_ *testing.T, verifier *verifyManifest, _ string) {
				verifier.ReportError = "ambiguous report candidates"
			},
			want: "verifier report attestation failed",
		},
		{
			name:            "report modified after verify",
			requireAttested: true,
			mutate: func(t *testing.T, _ *verifyManifest, path string) {
				if err := os.WriteFile(path, []byte(`{"resolved_ids":["case-b"]}`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "does not match verifier manifest",
		},
		{
			name:            "other harness run id",
			requireAttested: true,
			mutate: func(_ *testing.T, verifier *verifyManifest, _ string) {
				verifier.Report.HarnessRunID = "other-run-tag"
			},
			want: "harness_run_id",
		},
		{
			name:            "other report path",
			requireAttested: true,
			mutate: func(t *testing.T, verifier *verifyManifest, path string) {
				other := filepath.Join(filepath.Dir(path), "other.run-1-tag.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(other, data, 0o644); err != nil {
					t.Fatal(err)
				}
				verifier.Report.Path = other
			},
			want: "does not match verifier-bound report",
		},
		{
			name:            "wrong command run id",
			requireAttested: true,
			mutate: func(_ *testing.T, verifier *verifyManifest, _ string) {
				verifier.Command.Command[len(verifier.Command.Command)-1] = "other-run-tag"
			},
			want: "run-id binding",
		},
		{
			name:            "malformed attested sha",
			requireAttested: true,
			mutate: func(_ *testing.T, verifier *verifyManifest, _ string) {
				verifier.Report.SHA256 = "not-a-digest"
			},
			want: "is not a SHA-256 digest",
		},
		{
			name:            "report outside verifier output",
			requireAttested: true,
			mutate: func(t *testing.T, verifier *verifyManifest, path string) {
				outsideDir := t.TempDir()
				outside := filepath.Join(outsideDir, filepath.Base(path))
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(outside, data, 0o644); err != nil {
					t.Fatal(err)
				}
				verifier.Report.Path = outside
			},
			want: "does not match verifier-bound report",
		},
		{
			name:            "symlink report",
			requireAttested: true,
			mutate: func(t *testing.T, verifier *verifyManifest, path string) {
				realPath := filepath.Join(filepath.Dir(path), "real.json")
				if err := os.Rename(path, realPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realPath, path); err != nil {
					t.Fatal(err)
				}
				verifier.Report.Path = path
			},
			want: "symbolic link",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "test-model.run-1-tag.json")
			if err := writeJSON(path, map[string]any{"resolved_ids": []string{"case-a"}}); err != nil {
				t.Fatal(err)
			}
			verifier := testVerifierWithHarnessReport(t, "run-1", "tag", path)
			if tt.removeAttestation {
				verifier.Report = verifyReport{}
			}
			if tt.mutate != nil {
				tt.mutate(t, &verifier, path)
			}
			sha, err := validateHarnessReportBinding(verifier, path, tt.requireAttested)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateHarnessReportBinding() error = %v", err)
				}
				if sha != testFileSHA256(t, path) {
					t.Fatalf("SHA-256 = %q, want %q", sha, testFileSHA256(t, path))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateHarnessReportBinding() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateHarnessReportBindingRejectsNativeOmission(t *testing.T) {
	_, err := validateHarnessReportBinding(verifyManifest{}, "", true)
	if err == nil || !strings.Contains(err.Error(), "requires --harness-report") {
		t.Fatalf("validateHarnessReportBinding() error = %v, want Native omission rejection", err)
	}
}

func TestValidatePredictionsBinding(t *testing.T) {
	newFixture := func(t *testing.T) (verifyManifest, string) {
		t.Helper()
		dir := t.TempDir()
		source := filepath.Join(dir, "preds.json")
		writePredictionIDsJSON(t, source, []string{"case-a"})
		outputDir := filepath.Join(dir, "verify")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		verifier := verifyManifest{
			Config:  verifyConfig{OutputDir: outputDir, Predictions: source},
			Command: commandResult{Command: []string{"python"}},
		}
		attestVerifierPredictions(t, &verifier, source)
		return verifier, source
	}

	t.Run("valid native", func(t *testing.T) {
		verifier, _ := newFixture(t)
		got, err := validatePredictionsBinding(verifier, true)
		if err != nil {
			t.Fatal(err)
		}
		if got != verifier.Config.PredictionsSHA256 {
			t.Fatalf("validatePredictionsBinding() = %q, want %q", got, verifier.Config.PredictionsSHA256)
		}
	})
	t.Run("legacy unattested", func(t *testing.T) {
		if got, err := validatePredictionsBinding(verifyManifest{}, false); err != nil || got != "" {
			t.Fatalf("validatePredictionsBinding() = %q, %v", got, err)
		}
	})
	t.Run("native unattested", func(t *testing.T) {
		_, err := validatePredictionsBinding(verifyManifest{}, true)
		if err == nil || !strings.Contains(err.Error(), "no verify-time snapshot") {
			t.Fatalf("validatePredictionsBinding() error = %v", err)
		}
	})
	t.Run("source changed", func(t *testing.T) {
		verifier, source := newFixture(t)
		if err := os.WriteFile(source, []byte(`{"case-b":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := validatePredictionsBinding(verifier, true)
		if err == nil || !strings.Contains(err.Error(), "runner predictions SHA-256") {
			t.Fatalf("validatePredictionsBinding() error = %v", err)
		}
	})
	t.Run("snapshot changed", func(t *testing.T) {
		verifier, _ := newFixture(t)
		if err := os.WriteFile(verifier.Config.PredictionsSnapshot, []byte(`{"case-b":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := validatePredictionsBinding(verifier, true)
		if err == nil || !strings.Contains(err.Error(), "snapshot SHA-256") {
			t.Fatalf("validatePredictionsBinding() error = %v", err)
		}
	})
	t.Run("command changed", func(t *testing.T) {
		verifier, _ := newFixture(t)
		verifier.Command.Command[len(verifier.Command.Command)-1] = filepath.Join(t.TempDir(), "other.json")
		_, err := validatePredictionsBinding(verifier, true)
		if err == nil || !strings.Contains(err.Error(), "command predictions binding") {
			t.Fatalf("validatePredictionsBinding() error = %v", err)
		}
	})
	t.Run("malformed digest", func(t *testing.T) {
		verifier, _ := newFixture(t)
		verifier.Config.PredictionsSHA256 = "not-a-digest"
		_, err := validatePredictionsBinding(verifier, true)
		if err == nil || !strings.Contains(err.Error(), "not a SHA-256 digest") {
			t.Fatalf("validatePredictionsBinding() error = %v", err)
		}
	})
	t.Run("gold native", func(t *testing.T) {
		_, err := validatePredictionsBinding(verifyManifest{Config: verifyConfig{Predictions: "gold"}}, true)
		if err == nil || !strings.Contains(err.Error(), "not gold") {
			t.Fatalf("validatePredictionsBinding() error = %v", err)
		}
	})
}

func TestValidateRunConfigSelectionRejectsUnboundNativeBundles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "other run trace",
			mutate: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "native", "case-a", "case-a.native.json")
				var raw map[string]any
				if err := readJSONFile(path, &raw); err != nil {
					t.Fatal(err)
				}
				raw["info"].(map[string]any)["run_id"] = "other-run"
				if err := writeJSON(path, raw); err != nil {
					t.Fatal(err)
				}
			},
			want: "info.run_id",
		},
		{
			name: "response sha mismatch",
			mutate: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "native", "case-a", "case-a.responses.json")
				if err := os.WriteFile(path, []byte("[ ]\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "SHA-256",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fixture := writeRunConfigSelectionFixture(t, dir, []string{"case-a"}, []string{"case-a"}, "native")
			selectionHash, _ := selectedInstancesSHA256([]string{"case-a"})
			manifest := runnerManifest{
				RunID:                   "native-run",
				RunnerType:              "trpc-agent-go-native",
				ObservationCodec:        "xml",
				SourceRevision:          strings.Repeat("a", 40),
				BinarySHA256:            strings.Repeat("b", 64),
				CasesSHA256:             fixture.cases.CasesJSONLSHA256,
				ModelConfigSHA256:       strings.Repeat("d", 64),
				EnvironmentConfigSHA256: strings.Repeat("e", 64),
				SelectedInstancesSHA256: selectionHash,
				CommandTimeout:          "1m",
				CaseTimeout:             "2h",
				OutputDir:               filepath.Join(dir, "native"),
				CaseCount:               1,
				Workers:                 1,
				ModelConfig:             map[string]string{"MODEL_NAME": "test-model"},
			}
			prediction := contract.Prediction{InstanceID: "case-a", ModelNameOrPath: "trpc-agent-go/test-model"}
			if err := writeJSON(
				fixture.predictionsPath,
				map[string]contract.Prediction{"case-a": prediction},
			); err != nil {
				t.Fatal(err)
			}
			result := writeNativeBindingArtifacts(t, manifest, filepath.Join(dir, "imported"), "native", prediction)
			harnessDir := filepath.Join(dir, "verify")
			harnessPath := filepath.Join(harnessDir, "test-model.native-run-native.json")
			if err := os.MkdirAll(harnessDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(harnessPath, map[string]any{"error_ids": []string{"case-a"}}); err != nil {
				t.Fatal(err)
			}
			result.VerifierResultRef = absPath(harnessPath)
			writeImportedCasesJSONL(t, filepath.Join(dir, "imported", "cases.jsonl"), []importedCase{{
				SchemaVersion: importSchemaVersion,
				InstanceID:    "case-a",
				Target:        "native",
				Result:        result,
			}})
			tt.mutate(t, dir)
			verifier := testVerifierWithHarnessReport(t, "native-run", "native", harnessPath)
			verifier.Config.InstanceIDs = []string{"case-a"}
			verifier.Config.Predictions = fixture.predictionsPath
			attestVerifierPredictions(t, &verifier, fixture.predictionsPath)
			_, err := validateRunConfigSelection(
				fixture.cases,
				manifest,
				shardsManifest{},
				verifier,
				fixture.importSummaryPath,
				fixture.importSummary,
				"native",
				harnessPath,
				false,
				false,
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRunConfigSelection() error = %v, want native bundle error containing %q", err, tt.want)
			}
		})
	}
}

func TestRunConfigRequiresExactlyOneRunnerSource(t *testing.T) {
	err := runRunConfig([]string{
		"--run-id", "run-1",
		"--cases-manifest", "cases.json",
		"--run-mini-manifest", "mini.json",
		"--runner-manifest", "runner.json",
		"--verifier-manifest", "verify.json",
		"--import-summary", "summary.json",
		"--model-name", "model",
	})
	if err == nil {
		t.Fatal("runRunConfig() accepted multiple runner sources")
	}
}

func TestValidateRunConfigInputsRejectsTargetMismatch(t *testing.T) {
	err := validateRunConfigInputs(
		"run-1",
		"tag",
		prepareDataManifest{Dataset: defaultDatasetName, Split: defaultSplit, CaseCount: 1, CaseListHash: "hash"},
		runMiniManifest{},
		runnerManifest{
			RunID:       "run-1",
			CaseCount:   1,
			Predictions: "/tmp/preds.json",
			Status:      "completed",
		},
		shardsManifest{},
		verifyManifest{
			RunID:  "run-1",
			Target: "mini-go",
			Harness: harnessIdentity{
				Version: "test",
			},
			Config: verifyConfig{
				Dataset:     defaultDatasetName,
				Split:       defaultSplit,
				Predictions: "/tmp/preds.json",
				Workers:     1,
				TimeoutSec:  1800,
			},
		},
		importSummary{
			SchemaVersion: importSchemaVersion,
			Target:        "tag",
			Total:         1,
			Counts:        map[string]int{"resolved": 1},
		},
		false,
		false,
	)
	if err == nil {
		t.Fatal("validateRunConfigInputs() accepted mismatched verifier target")
	}
}

func TestValidateRunConfigInputsRejectsInconsistentCounts(t *testing.T) {
	err := validateRunConfigInputs(
		"run-1",
		"tag",
		prepareDataManifest{Dataset: defaultDatasetName, Split: defaultSplit, CaseCount: 1, CaseListHash: "hash"},
		runMiniManifest{},
		runnerManifest{
			RunID:       "run-1",
			CaseCount:   1,
			Predictions: "/tmp/preds.json",
			Status:      "completed",
		},
		shardsManifest{},
		verifyManifest{
			RunID:  "run-1",
			Target: "tag",
			Harness: harnessIdentity{
				Version: "test",
			},
			Config: verifyConfig{
				Dataset:     defaultDatasetName,
				Split:       defaultSplit,
				Predictions: "/tmp/preds.json",
				Workers:     1,
				TimeoutSec:  1800,
			},
		},
		importSummary{
			SchemaVersion: importSchemaVersion,
			Target:        "tag",
			Total:         1,
			Counts:        map[string]int{"resolved": 2},
		},
		false,
		false,
	)
	if err == nil {
		t.Fatal("validateRunConfigInputs() accepted inconsistent counts")
	}
}

func TestValidateRunConfigInputsAcceptsCompletedWithErrors(t *testing.T) {
	cases, generic, verifier, summary := validGenericRunConfigInputs("completed_with_errors")
	if err := validateRunConfigInputs(
		"run-1",
		"tag",
		cases,
		runMiniManifest{},
		generic,
		shardsManifest{},
		verifier,
		summary,
		false,
		false,
	); err != nil {
		t.Fatalf("validateRunConfigInputs() error = %v, want nil", err)
	}
}

func TestValidateRunConfigInputsRejectsNonterminalGenericStatus(t *testing.T) {
	for _, status := range []string{"running", "failed"} {
		t.Run(status, func(t *testing.T) {
			cases, generic, verifier, summary := validGenericRunConfigInputs(status)
			err := validateRunConfigInputs(
				"run-1",
				"tag",
				cases,
				runMiniManifest{},
				generic,
				shardsManifest{},
				verifier,
				summary,
				false,
				false,
			)
			if err == nil || !strings.Contains(err.Error(), "runner status") {
				t.Fatalf("validateRunConfigInputs() error = %v, want runner status error", err)
			}
		})
	}
}

func TestValidateRunConfigInputsAcceptsGenericMiniGoIdentity(t *testing.T) {
	cases, generic, verifier, summary := validGenericMiniGoRunConfigInputs(t)
	if err := validateRunConfigInputs(
		"run-1",
		"tag",
		cases,
		runMiniManifest{},
		generic,
		shardsManifest{},
		verifier,
		summary,
		false,
		false,
	); err != nil {
		t.Fatalf("validateRunConfigInputs() error = %v, want nil", err)
	}
}

func TestValidateRunConfigInputsRejectsInvalidGenericMiniGoProvenance(t *testing.T) {
	tests := []struct {
		name string
		edit func(*runnerManifest)
		want string
	}{
		{name: "environment hash", edit: func(m *runnerManifest) { m.EnvironmentConfigSHA256 = "" }, want: "environment_config_sha256"},
		{name: "workers", edit: func(m *runnerManifest) { m.Workers = 0 }, want: "workers"},
		{name: "command timeout", edit: func(m *runnerManifest) { m.CommandTimeout = "invalid" }, want: "command_timeout"},
		{name: "case timeout", edit: func(m *runnerManifest) { m.CaseTimeout = "0s" }, want: "case_timeout"},
		{name: "cases content hash", edit: func(m *runnerManifest) { m.CasesSHA256 = strings.Repeat("9", 64) }, want: "cases_sha256"},
		{name: "selection hash", edit: func(m *runnerManifest) { m.SelectedInstancesSHA256 = strings.Repeat("9", 64) }, want: "selected_instances_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases, generic, verifier, summary := validGenericMiniGoRunConfigInputs(t)
			tt.edit(&generic)
			err := validateRunConfigInputs(
				"run-1",
				"tag",
				cases,
				runMiniManifest{},
				generic,
				shardsManifest{},
				verifier,
				summary,
				false,
				false,
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRunConfigInputs() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateRunConfigInputsRevalidatesShardedMiniGoProvenance(t *testing.T) {
	tests := []struct {
		name string
		edit func(*shardsManifest)
		want string
	}{
		{name: "valid", edit: func(*shardsManifest) {}, want: ""},
		{name: "environment mismatch", edit: func(s *shardsManifest) { s.Shards[0].RunnerIdentity.EnvironmentConfigSHA256 = strings.Repeat("6", 64) }, want: "environment_config_sha256"},
		{name: "command timeout mismatch", edit: func(s *shardsManifest) { s.Shards[0].RunnerIdentity.CommandTimeout = "2m" }, want: "command_timeout"},
		{name: "case timeout mismatch", edit: func(s *shardsManifest) { s.Shards[0].RunnerIdentity.CaseTimeout = "3h" }, want: "case_timeout"},
		{name: "invalid canonical timeout", edit: func(s *shardsManifest) { s.RunnerIdentity.CommandTimeout = "invalid" }, want: "shards runner identity is invalid"},
		{name: "selection mismatch", edit: func(s *shardsManifest) { s.Shards[0].SelectedInstancesSHA256 = strings.Repeat("9", 64) }, want: "selected_instances_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases, _, verifier, summary := validGenericMiniGoRunConfigInputs(t)
			identity := defaultMiniGoIdentity()
			shards := shardsManifest{
				ExpectedCases:  1,
				AcceptedCases:  1,
				RunnerIdentity: identity,
				Shards: []shardSummary{{
					RunID:                   "shard-000",
					Status:                  "accepted",
					ExpectedCount:           1,
					AcceptedCount:           1,
					RunnerIdentity:          identity,
					ExpectedIDs:             []string{"case-a"},
					SelectedInstancesSHA256: cases.CaseListHash,
				}},
			}
			tt.edit(&shards)
			err := validateRunConfigInputs(
				"run-1",
				"tag",
				cases,
				runMiniManifest{},
				runnerManifest{},
				shards,
				verifier,
				summary,
				false,
				true,
			)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateRunConfigInputs() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateRunConfigInputs() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func validGenericRunConfigInputs(status string) (prepareDataManifest, runnerManifest, verifyManifest, importSummary) {
	cases := prepareDataManifest{
		Dataset:      defaultDatasetName,
		Split:        defaultSplit,
		CaseCount:    1,
		CaseListHash: "hash",
	}
	generic := runnerManifest{
		RunID:       "run-1",
		CaseCount:   1,
		Predictions: "/tmp/preds.json",
		Status:      status,
	}
	verifier := verifyManifest{
		RunID:  "run-1",
		Target: "tag",
		Harness: harnessIdentity{
			Version: "test",
		},
		Config: verifyConfig{
			Dataset:     defaultDatasetName,
			Split:       defaultSplit,
			Predictions: "/tmp/preds.json",
			Workers:     1,
			TimeoutSec:  1800,
		},
	}
	summary := importSummary{
		SchemaVersion: importSchemaVersion,
		Target:        "tag",
		Total:         1,
		Counts:        map[string]int{"resolved": 1},
	}
	return cases, generic, verifier, summary
}

func validGenericMiniGoRunConfigInputs(t *testing.T) (prepareDataManifest, runnerManifest, verifyManifest, importSummary) {
	t.Helper()
	cases, generic, verifier, summary := validGenericRunConfigInputs("completed_with_errors")
	caseListHash, err := selectedInstancesSHA256([]string{"case-a"})
	if err != nil {
		t.Fatal(err)
	}
	cases.CaseListHash = caseListHash
	cases.CasesJSONLSHA256 = strings.Repeat("c", 64)
	generic.RunnerType = "mini-swe-agent-go"
	generic.ObservationCodec = "xml"
	generic.FrameworkModule = "trpc.group/trpc-go/trpc-agent-go"
	generic.FrameworkVersion = "v1.2.3"
	generic.SourceRevision = strings.Repeat("a", 40)
	generic.BinarySHA256 = strings.Repeat("b", 64)
	generic.CasesSHA256 = strings.Repeat("c", 64)
	generic.ModelConfigSHA256 = strings.Repeat("d", 64)
	generic.EnvironmentConfigSHA256 = strings.Repeat("e", 64)
	generic.SelectedInstancesSHA256 = caseListHash
	generic.CommandTimeout = "60s"
	generic.CaseTimeout = "2h"
	generic.Workers = 1
	return cases, generic, verifier, summary
}

func TestValidateGenericRunnerModelName(t *testing.T) {
	manifest := runnerManifest{
		RunnerType:  "trpc-agent-go-native",
		ModelConfig: map[string]string{"MODEL_NAME": "model-a"},
	}
	if err := validateGenericRunnerModelName(manifest, "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := validateGenericRunnerModelName(manifest, "model-b"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
	if err := validateGenericRunnerModelName(manifest, " model-a "); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("CLI whitespace error = %v", err)
	}
	manifest.ModelConfig["MODEL_NAME"] = " model-a "
	if err := validateGenericRunnerModelName(manifest, "model-a"); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("manifest whitespace error = %v", err)
	}
	manifest.ModelConfig["MODEL_NAME"] = "model-a"
	delete(manifest.ModelConfig, "MODEL_NAME")
	if err := validateGenericRunnerModelName(manifest, "model-a"); err == nil || !strings.Contains(err.Error(), "no MODEL_NAME") {
		t.Fatalf("missing error = %v", err)
	}
}

func TestValidateShardedNativeRunnerModelName(t *testing.T) {
	shards := shardsManifest{
		RunnerIdentity: shardRunnerIdentity{
			RunnerType: "trpc-agent-go-native",
			ModelName:  "model-a",
		},
	}
	if err := validateShardedNativeRunnerModelName(shards, true, "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := validateShardedNativeRunnerModelName(shards, true, "model-b"); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("model mismatch error = %v", err)
	}
	if err := validateShardedNativeRunnerModelName(shards, true, " model-a "); err == nil ||
		!strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("CLI whitespace error = %v", err)
	}
	shards.RunnerIdentity.ModelName = ""
	if err := validateShardedNativeRunnerModelName(shards, true, "model-a"); err == nil ||
		!strings.Contains(err.Error(), "no MODEL_NAME") {
		t.Fatalf("missing model error = %v", err)
	}
	shards.RunnerIdentity.RunnerType = "mini-swe-agent-go"
	if err := validateShardedNativeRunnerModelName(shards, true, "model-a"); err != nil {
		t.Fatalf("Mini-Go compatibility error = %v", err)
	}
}

func TestRunnerManifestForNativeShardPreservesModelAndProtocolIdentity(t *testing.T) {
	identity := shardRunnerIdentity{
		RunnerType:     "trpc-agent-go-native",
		AgentProtocol:  "mini-swe-agent-v2.1-on-trpc-agent-go",
		UpstreamCommit: strings.Repeat("f", 40),
		ModelName:      "model-a",
	}
	manifest := runnerManifestForNativeShard(shardSummary{
		RunID:                   "shard-000",
		RawDir:                  "/tmp/native-shard",
		ExpectedCount:           1,
		SelectedInstancesSHA256: strings.Repeat("a", 64),
		Workers:                 4,
		RunnerIdentity:          identity,
	})
	if manifest.ModelConfig["MODEL_NAME"] != identity.ModelName ||
		manifest.AgentProtocol != identity.AgentProtocol ||
		manifest.UpstreamCommit != identity.UpstreamCommit {
		t.Fatalf("runner manifest = %+v, want model/protocol/upstream identity", manifest)
	}
	predictions := map[string]contract.Prediction{
		"case-a": {
			InstanceID:      "case-a",
			ModelNameOrPath: "trpc-agent-go/model-a",
		},
	}
	if err := validateNativePredictionModelNames(predictions, manifest); err != nil {
		t.Fatal(err)
	}
	predictions["case-a"] = contract.Prediction{
		InstanceID:      "case-a",
		ModelNameOrPath: "trpc-agent-go/model-b",
	}
	if err := validateNativePredictionModelNames(predictions, manifest); err == nil ||
		!strings.Contains(err.Error(), "does not match runner identity") {
		t.Fatalf("prediction attribution error = %v", err)
	}
}

func TestValidateNativePredictionModelNames(t *testing.T) {
	manifest := runnerManifest{
		RunnerType:  "trpc-agent-go-native",
		ModelConfig: map[string]string{"MODEL_NAME": "model-a"},
	}
	predictions := map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelNameOrPath: "trpc-agent-go/model-a"},
	}
	if err := validateNativePredictionModelNames(predictions, manifest); err != nil {
		t.Fatal(err)
	}
	predictions["case-a"] = contract.Prediction{InstanceID: "case-a", ModelNameOrPath: "model-a"}
	err := validateNativePredictionModelNames(predictions, manifest)
	if err == nil || !strings.Contains(err.Error(), "does not match runner identity") {
		t.Fatalf("validateNativePredictionModelNames() error = %v, want attribution mismatch", err)
	}
}

func TestValidateCasesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cases.jsonl")
	if err := os.WriteFile(path, []byte("case-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := prepareDataManifest{OutputDir: dir, CasesJSONLSHA256: testFileSHA256(t, path)}
	if err := validateCasesContent(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.CasesJSONLSHA256 = strings.Repeat("0", 64)
	if err := validateCasesContent(manifest); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

type runConfigSelectionFixture struct {
	cases             prepareDataManifest
	casesManifestPath string
	importSummaryPath string
	importSummary     importSummary
	predictionsPath   string
}

func writeRunConfigSelectionFixture(
	t *testing.T,
	dir string,
	fullIDs []string,
	importedIDs []string,
	target string,
) runConfigSelectionFixture {
	t.Helper()
	fullHash, err := selectedInstancesSHA256(fullIDs)
	if err != nil {
		t.Fatal(err)
	}
	casesDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(casesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	casesJSONLPath := filepath.Join(casesDir, "cases.jsonl")
	writeCaseIDsJSONL(t, casesJSONLPath, fullIDs)
	cases := prepareDataManifest{
		Dataset:          defaultDatasetName,
		Split:            defaultSplit,
		CaseCount:        len(fullIDs),
		CaseListHash:     fullHash,
		CasesJSONLSHA256: testFileSHA256(t, casesJSONLPath),
		HintsTextPolicy:  "excluded",
		OutputDir:        casesDir,
	}
	casesManifestPath := filepath.Join(casesDir, "cases.manifest.json")
	if err := writeJSON(casesManifestPath, cases); err != nil {
		t.Fatal(err)
	}

	importedDir := filepath.Join(dir, "imported")
	if err := os.MkdirAll(importedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := make([]importedCase, 0, len(importedIDs))
	for _, id := range importedIDs {
		rows = append(rows, importedCase{
			SchemaVersion: importSchemaVersion,
			InstanceID:    id,
			Target:        target,
			Result: targetResult{
				MainStatus:      "empty_patch",
				FailureReason:   "empty model_patch",
				ModelNameOrPath: "test-model",
				PatchStats:      artifact.PatchStats{ChangedFiles: []string{}},
			},
		})
	}
	writeImportedCasesJSONL(t, filepath.Join(importedDir, "cases.jsonl"), rows)
	predictionsPath := filepath.Join(dir, "preds.json")
	writePredictionIDsJSON(t, predictionsPath, importedIDs)
	importSummaryPath := filepath.Join(importedDir, "summary", target+".json")
	summary := importSummary{
		SchemaVersion: importSchemaVersion,
		Target:        target,
		Total:         len(importedIDs),
		Counts:        map[string]int{"empty_patch": len(importedIDs)},
	}
	if err := writeJSON(importSummaryPath, summary); err != nil {
		t.Fatal(err)
	}
	return runConfigSelectionFixture{
		cases:             cases,
		casesManifestPath: casesManifestPath,
		importSummaryPath: importSummaryPath,
		importSummary:     summary,
		predictionsPath:   predictionsPath,
	}
}

func writeImportedCasesJSONL(t *testing.T, path string, rows []importedCase) {
	t.Helper()
	var data strings.Builder
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCaseIDsJSONL(t *testing.T, path string, ids []string) {
	t.Helper()
	var data strings.Builder
	for _, id := range ids {
		data.WriteString("{\"instance_id\":\"")
		data.WriteString(id)
		data.WriteString("\"}\n")
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCasesJSONL(t *testing.T, path string, cases []contract.Case) {
	t.Helper()
	var data strings.Builder
	for _, c := range cases {
		encoded, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePredictionIDsJSON(t *testing.T, path string, ids []string) {
	t.Helper()
	var data strings.Builder
	data.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			data.WriteByte(',')
		}
		data.WriteString("\"")
		data.WriteString(id)
		data.WriteString("\":{\"instance_id\":\"")
		data.WriteString(id)
		data.WriteString("\",\"model_patch\":\"\",\"model_name_or_path\":\"test-model\"}")
	}
	data.WriteString("}\n")
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeNativeBindingArtifacts(
	t *testing.T,
	manifest runnerManifest,
	importedRoot string,
	target string,
	prediction contract.Prediction,
) targetResult {
	t.Helper()
	caseDir := filepath.Join(manifest.OutputDir, prediction.InstanceID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	responsesPath := filepath.Join(caseDir, prediction.InstanceID+".responses.json")
	if err := writeJSON(responsesPath, []any{}); err != nil {
		t.Fatal(err)
	}
	responsesSHA256 := testFileSHA256(t, responsesPath)
	rawPath := filepath.Join(caseDir, prediction.InstanceID+".native.json")
	raw := map[string]any{
		"instance_id": prediction.InstanceID,
		"info": map[string]any{
			"run_id":                    manifest.RunID,
			"observation_codec":         manifest.ObservationCodec,
			"source_revision":           manifest.SourceRevision,
			"source_modified":           manifest.SourceModified,
			"binary_sha256":             manifest.BinarySHA256,
			"model_config_sha256":       manifest.ModelConfigSHA256,
			"environment_config_sha256": manifest.EnvironmentConfigSHA256,
			"cases_sha256":              manifest.CasesSHA256,
			"command_timeout":           manifest.CommandTimeout,
			"case_timeout":              manifest.CaseTimeout,
			"selected_instances_sha256": manifest.SelectedInstancesSHA256,
			"workers":                   manifest.Workers,
			"exit_status":               "Submitted",
		},
		"model_patch": prediction.ModelPatch,
		"duration_ms": 1,
		"llm_calls":   0,
		"tool_calls":  0,
		"usage": map[string]any{
			"prompt_tokens":             0,
			"completion_tokens":         0,
			"total_tokens":              0,
			"prompt_tokens_details":     map[string]any{"cached_tokens": 0},
			"completion_tokens_details": map[string]any{},
		},
		"response_count":   0,
		"responses_sha256": responsesSHA256,
	}
	if err := writeJSON(rawPath, raw); err != nil {
		t.Fatal(err)
	}
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	traceRel := filepath.Join("traces", target, prediction.InstanceID+".json")
	tracePath := filepath.Join(importedRoot, traceRel)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracePath, redactJSONBytes(rawData), 0o644); err != nil {
		t.Fatal(err)
	}
	patchStats := artifact.ComputePatchStats(prediction.ModelPatch)
	patchRel := ""
	if strings.TrimSpace(prediction.ModelPatch) != "" {
		patchRel = filepath.Join("patches", target, prediction.InstanceID+".patch")
		patchPath := filepath.Join(importedRoot, patchRel)
		if err := os.MkdirAll(filepath.Dir(patchPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(patchPath, []byte(prediction.ModelPatch), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return targetResult{
		MainStatus:      "empty_patch",
		FailureReason:   "empty model_patch",
		ModelNameOrPath: prediction.ModelNameOrPath,
		PatchPath:       patchRel,
		TracePath:       traceRel,
		PatchStats:      patchStats,
	}
}

func testVerifierWithHarnessReport(
	t *testing.T,
	runID string,
	target string,
	reportPath string,
) verifyManifest {
	t.Helper()
	outputDir := filepath.Dir(absPath(reportPath))
	harnessRunID := runID + "-" + target
	return verifyManifest{
		RunID:  runID,
		Target: target,
		Command: commandResult{
			Command: []string{
				"python",
				"-m", "swebench.harness.run_evaluation",
				"--report_dir", outputDir,
				"-id", harnessRunID,
			},
			Dir: outputDir,
		},
		Config: verifyConfig{OutputDir: outputDir},
		Report: verifyReport{
			HarnessRunID: harnessRunID,
			Path:         absPath(reportPath),
			SHA256:       testFileSHA256(t, reportPath),
		},
	}
}

func attestVerifierPredictions(t *testing.T, verifier *verifyManifest, sourcePath string) {
	t.Helper()
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	outputDir := verifier.Config.OutputDir
	if strings.TrimSpace(outputDir) == "" {
		outputDir = verifier.Command.Dir
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(outputDir, "predictions.snapshot.json")
	if err := os.WriteFile(snapshotPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	verifier.Config.Predictions = absPath(sourcePath)
	verifier.Config.PredictionsSnapshot = absPath(snapshotPath)
	verifier.Config.PredictionsSHA256 = testFileSHA256(t, sourcePath)
	verifier.Command.Command = append(verifier.Command.Command, "-p", absPath(snapshotPath))
}
