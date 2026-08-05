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
	"strings"
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
		{name: "command timeout", edit: func(m *runnerManifest) { m.CommandTimeout = "invalid" }, want: "command_timeout"},
		{name: "case timeout", edit: func(m *runnerManifest) { m.CaseTimeout = "0s" }, want: "case_timeout"},
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
	return cases, generic, verifier, summary
}
