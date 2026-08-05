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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunConfigSupportsGenericNativeRunnerManifest(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)

	casesDir := filepath.Join(dir, "data")
	casesManifestPath := filepath.Join(casesDir, "cases.manifest.json")
	if err := writeJSON(casesManifestPath, prepareDataManifest{
		Dataset:         defaultDatasetName,
		Split:           defaultSplit,
		CaseCount:       1,
		CaseListHash:    "hash",
		HintsTextPolicy: "excluded",
		OutputDir:       casesDir,
	}); err != nil {
		t.Fatal(err)
	}

	runnerDir := filepath.Join(dir, "native")
	predictionsPath := filepath.Join(runnerDir, "preds.json")
	runnerManifestPath := filepath.Join(runnerDir, "run-mini-looking-generic.json")
	if err := writeJSON(runnerManifestPath, runnerManifest{
		RunID:       "native-run",
		RunnerType:  "trpc-agent-go-native",
		StartedAt:   start,
		FinishedAt:  start.Add(2 * time.Minute),
		DurationMS:  int64((2 * time.Minute) / time.Millisecond),
		OutputDir:   runnerDir,
		CaseCount:   1,
		Predictions: predictionsPath,
		Status:      "completed",
	}); err != nil {
		t.Fatal(err)
	}

	verifyManifestPath := filepath.Join(dir, "verify", "verifier_manifest.json")
	if err := writeJSON(verifyManifestPath, verifyManifest{
		RunID:      "native-run",
		Target:     "native",
		StartedAt:  start.Add(3 * time.Minute),
		FinishedAt: start.Add(4 * time.Minute),
		DurationMS: int64(time.Minute / time.Millisecond),
		Harness: harnessIdentity{
			Version:  "4.0.3",
			Revision: "abc123",
		},
		Config: verifyConfig{
			OutputDir:   filepath.Join(dir, "verify"),
			Workers:     8,
			TimeoutSec:  1800,
			CacheLevel:  "instance",
			Dataset:     defaultDatasetName,
			Split:       defaultSplit,
			Predictions: predictionsPath,
			DockerHost:  defaultDockerHost,
			Python:      "python",
			Clean:       false,
		},
	}); err != nil {
		t.Fatal(err)
	}

	importSummaryPath := filepath.Join(dir, "imported", "summary", "native.json")
	if err := writeJSON(importSummaryPath, importSummary{
		SchemaVersion: importSchemaVersion,
		GeneratedAt:   start.Add(5 * time.Minute),
		Target:        "native",
		Total:         1,
		Counts:        map[string]int{"empty_patch": 1},
	}); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "run_config.json")
	err := runRunConfig([]string{
		"--run-id", "native-run",
		"--target", "native",
		"--cases-manifest", casesManifestPath,
		"--runner-manifest", runnerManifestPath,
		"--verifier-manifest", verifyManifestPath,
		"--import-summary", importSummaryPath,
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
	if doc.Concurrency.AgentGenerationWorkers != 0 {
		t.Fatalf("AgentGenerationWorkers = %d, want 0 for generic manifest", doc.Concurrency.AgentGenerationWorkers)
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
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected run_config.json: %v", err)
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
