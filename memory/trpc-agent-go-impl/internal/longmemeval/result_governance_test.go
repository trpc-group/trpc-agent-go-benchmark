//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
)

func TestReadLMEBoundedResultFile(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "result.json")
		if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		data, err := readLMEBoundedResultFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "{}" {
			t.Fatalf("data = %q, want {}", data)
		}
	})
	t.Run("directory", func(t *testing.T) {
		_, err := readLMEBoundedResultFile(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "bounded regular file") {
			t.Fatalf("error = %v, want bounded regular file", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized.json")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(lmeResultArtifactMaxFileBytes + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = readLMEBoundedResultFile(path)
		if err == nil || !strings.Contains(err.Error(), "bounded regular file") {
			t.Fatalf("error = %v, want bounded regular file", err)
		}
	})
}

func TestAggregateLMERunResultPreservesPartialDenominators(t *testing.T) {
	result := &lmeRunResult{Cases: []*lmeCaseResult{
		{Status: lmeCaseStatusPending, QuestionType: "type-a"},
		{
			Status:       lmeCaseStatusSucceeded,
			QuestionType: "type-a",
			Correct:      true,
			Metrics:      metrics.AnswerMetrics{Accuracy: 1},
		},
		{Status: lmeCaseStatusPending, QuestionType: "type-b"},
	}}
	aggregateLMERunResult(result, time.Second, 3)
	if result.Summary.CompletedCases != 1 ||
		!closeLMEMetric(result.Summary.Overall.Accuracy, 1.0/3.0) ||
		!closeLMEMetric(result.Summary.TaskAveragedAccuracy, 1) {
		t.Fatalf("partial summary = %+v", result.Summary)
	}
	typeA := result.ByType["type-a"]
	if typeA == nil || typeA.Count != 2 || !closeLMEMetric(typeA.Metrics.Accuracy, 0.5) {
		t.Fatalf("type-a summary = %+v", typeA)
	}
	if _, ok := result.ByType["type-b"]; ok {
		t.Fatal("pending-only type unexpectedly appeared in by_type")
	}
}

func TestValidateLMESummaryConsistencyCountsUsageOnce(t *testing.T) {
	usage := &scenarios.TokenUsage{
		PromptTokens:     101,
		CompletionTokens: 17,
		TotalTokens:      118,
		CachedTokens:     23,
		LLMCalls:         3,
	}
	result := &lmeRunResult{Cases: []*lmeCaseResult{{
		Status:       lmeCaseStatusSucceeded,
		QuestionID:   "case-1",
		QuestionType: "type-a",
		Correct:      true,
		Metrics:      metrics.AnswerMetrics{Accuracy: 1},
		TokenUsage:   usage,
	}}}
	aggregateLMERunResult(result, time.Second, 1)
	if blockers := validateLMESummaryConsistency(result, 1); len(blockers) != 0 {
		t.Fatalf("validateLMESummaryConsistency() blockers = %v", blockers)
	}
}

func TestValidateLMEMemoryBuildIdentity(t *testing.T) {
	manifest := &lmeRunManifest{
		Run: lmeRunIdentity{
			Scenario:        "mem0_oss",
			BuildProtocol:   lmeBuildProtocol,
			TemporalContext: lmeMem0TemporalContext,
		},
		Config: map[string]any{
			"temporal_reference_source":    lmeTemporalReferenceSource,
			"temporal_reference_format":    lmeTemporalReferenceFormat,
			"mem0_preflight_digest":        "sha256:preflight",
			"mem0_environment_lock_digest": "sha256:environment",
		},
	}
	metadata := &lmeMetadata{MemoryBuild: map[string]any{
		"protocol":                    lmeBuildProtocol,
		"temporal_context":            lmeMem0TemporalContext,
		"temporal_reference_source":   lmeTemporalReferenceSource,
		"temporal_reference_format":   lmeTemporalReferenceFormat,
		"custom_extraction_prompt":    true,
		"observation_prompt_verified": true,
		"preflight_digest":            "sha256:preflight",
		"environment_lock_digest":     "sha256:environment",
	}}
	if blockers := validateLMEMemoryBuildIdentity(manifest, metadata); len(blockers) != 0 {
		t.Fatalf("validateLMEMemoryBuildIdentity() blockers = %v", blockers)
	}

	metadata.MemoryBuild["temporal_context"] = "storage_metadata_only"
	metadata.MemoryBuild["preflight_digest"] = "sha256:other"
	blockers := strings.Join(validateLMEMemoryBuildIdentity(manifest, metadata), "\n")
	for _, want := range []string{"temporal context", "preflight_digest"} {
		if !strings.Contains(blockers, want) {
			t.Fatalf("blockers = %q, want %q", blockers, want)
		}
	}
}

func TestValidateLMEBuildTracePublishesOnlyValidatedAttempt(t *testing.T) {
	dir := t.TempDir()
	caseID := "case-1"
	result := testLMEResult([]string{caseID}, []lmeCaseStatus{lmeCaseStatusSucceeded})
	prepareLMEPublicationFixture(t, dir, result, []string{caseID}, nil)
	base := filepath.Join(dir, "build_trace", lmeTraceFileName(caseID))
	if err := os.WriteFile(base+".attempt-0002.jsonl", []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact, blockers := validateLMEBuildTrace(dir, result, []string{caseID})
	if len(blockers) != 0 || !strings.HasPrefix(artifact.Path, "build_trace/maintained-") ||
		artifact.SHA256 == "" {
		t.Fatalf("artifact/blockers = %#v/%v", artifact, blockers)
	}
	if artifact.Purpose != lmeTraceArtifactPurpose ||
		artifact.Comparability != lmeTraceArtifactComparable ||
		artifact.ContentMode != string(lmeTraceContentHash) || artifact.SelectedCases != 1 {
		t.Fatalf("trace artifact declaration = %#v", artifact)
	}
	entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(artifact.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("maintained trace entries = %d, want manifest plus selected case", len(entries))
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".attempt-") {
			t.Fatalf("maintained trace leaked attempt history: %s", entry.Name())
		}
	}
}

func TestPrepareLMECaseRecordsOrdersAndNormalizesCheckpoint(t *testing.T) {
	instances := []*dataset.LongMemEvalInstance{
		{
			QuestionID:       "case-1",
			QuestionType:     "type-a",
			Question:         "question 1",
			Answer:           "answer 1",
			HaystackSessions: [][]dataset.LongMemEvalTurn{{{Role: "user"}}},
		},
		{
			QuestionID:       "case-2",
			QuestionType:     "type-b",
			Question:         "question 2",
			Answer:           "answer 2",
			HaystackSessions: [][]dataset.LongMemEvalTurn{{{Role: "user"}, {Role: "assistant"}}},
		},
	}
	result := &lmeRunResult{Cases: []*lmeCaseResult{{
		QuestionID:   "case-2",
		QuestionType: "type-b",
		JudgeError:   "invalid label",
	}}}
	if err := prepareLMECaseRecords(result, instances); err != nil {
		t.Fatalf("prepareLMECaseRecords() error = %v", err)
	}
	if got := []string{result.Cases[0].QuestionID, result.Cases[1].QuestionID}; !equalLMEStrings(got, []string{"case-1", "case-2"}) {
		t.Fatalf("case order = %v", got)
	}
	if result.Cases[0].Status != lmeCaseStatusPending ||
		result.Cases[0].TotalSessions != 1 || result.Cases[0].TotalTurns != 1 {
		t.Fatalf("pending case = %+v", result.Cases[0])
	}
	if result.Cases[1].Status != lmeCaseStatusJudgeFailed {
		t.Fatalf("legacy judge case = %+v", result.Cases[1])
	}
	if count := countLMEProcessedCases(result.Cases); count != 1 {
		t.Fatalf("processed cases = %d", count)
	}
	if isLMECheckpointCompleted(result.Cases[1]) {
		t.Fatal("judge failure must remain retryable on resume")
	}
	if got := lmeExpectedCaseIDs(instances); !equalLMEStrings(got, []string{"case-1", "case-2"}) {
		t.Fatalf("expected IDs = %v", got)
	}

	failed := newLMEFailedCase(instances[0], errors.New("api_key=secret"))
	if failed.Status != lmeCaseStatusFailed || strings.Contains(failed.Error, "secret") {
		t.Fatalf("failed case = %+v", failed)
	}
	if err := replaceLMECaseRecord(result, failed); err != nil {
		t.Fatalf("replaceLMECaseRecord() error = %v", err)
	}
	if err := replaceLMECaseRecord(result, &lmeCaseResult{QuestionID: "extra"}); err == nil {
		t.Fatal("replaceLMECaseRecord() expected unknown-ID error")
	}
}

func TestPrepareLMECaseRecordsRejectsDuplicateAndExtraCheckpoint(t *testing.T) {
	instances := []*dataset.LongMemEvalInstance{{QuestionID: "case-1"}}
	tests := []struct {
		name   string
		result *lmeRunResult
	}{
		{
			name: "duplicate",
			result: &lmeRunResult{Cases: []*lmeCaseResult{
				{QuestionID: "case-1"},
				{QuestionID: "case-1"},
			}},
		},
		{
			name: "extra",
			result: &lmeRunResult{Cases: []*lmeCaseResult{
				{QuestionID: "extra"},
			}},
		},
		{
			name:   "nil",
			result: &lmeRunResult{Cases: []*lmeCaseResult{nil}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := prepareLMECaseRecords(test.result, instances); err == nil {
				t.Fatal("prepareLMECaseRecords() expected error")
			}
		})
	}
}

func TestRedactLMERunConfigRemovesEndpointCredentials(t *testing.T) {
	config := redactLMERunConfig(lmeRunConfig{
		Mem0Host: "https://user:pass@example.com/api?token=secret#fragment",
	})
	if config.Mem0Host != "https://example.com/api" {
		t.Fatalf("redacted endpoint = %q", config.Mem0Host)
	}
	invalid := redactLMERunConfig(lmeRunConfig{
		Mem0Host: "not an endpoint?token=secret",
	})
	if invalid.Mem0Host != lmeRedactedValue {
		t.Fatalf("invalid endpoint = %q, want redacted", invalid.Mem0Host)
	}
	message := sanitizeLMEResultText(
		"request https://alice:super-secret@example.test failed",
		2048,
	)
	if strings.Contains(message, "super-secret") {
		t.Fatalf("sanitized error = %q", message)
	}
}

func TestSafeLMECaseLogNameRejectsTraversal(t *testing.T) {
	name := safeLMECaseLogName("../../case/one")
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		t.Fatalf("safe case log name = %q", name)
	}
}

func TestSaveLMECheckpointDoesNotReplaceResults(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, lmeResultsFileName)
	if err := os.WriteFile(resultsPath, []byte("maintained"), 0644); err != nil {
		t.Fatal(err)
	}
	result := testLMEResult([]string{"case-1"}, []lmeCaseStatus{lmeCaseStatusPending})
	if err := saveLMECheckpoint(dir, result); err != nil {
		t.Fatalf("saveLMECheckpoint() error = %v", err)
	}
	data, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "maintained" {
		t.Fatalf("results.json was replaced with %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, lmeCheckpointFileName)); err != nil {
		t.Fatalf("checkpoint.json not written: %v", err)
	}
}

func TestFinalizeLMERunResultPublishesFixedDenominator(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"case-1", "case-2"}
	result := testLMEResult(ids, []lmeCaseStatus{
		lmeCaseStatusSucceeded,
		lmeCaseStatusFailed,
	})
	prepareLMEPublicationFixture(t, dir, result, ids, nil)
	if err := finalizeLMERunResult(dir, result, ids); err != nil {
		t.Fatalf("finalizeLMERunResult() error = %v", err)
	}
	if !result.Publication.Eligible {
		t.Fatalf("publication = %+v", result.Publication)
	}
	if result.Publication.Origin != lmeResultOriginNativeRunner {
		t.Fatalf("publication origin = %q", result.Publication.Origin)
	}
	if result.Summary.SuccessfulCases != 1 || result.Summary.FailedCases != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	loaded, err := readLMEOfficialRunResult(filepath.Join(dir, lmeResultsFileName))
	if err != nil {
		t.Fatalf("readLMEOfficialRunResult() error = %v", err)
	}
	if len(loaded.Cases) != len(ids) || loaded.Cases[1].Status != lmeCaseStatusFailed {
		t.Fatalf("cases = %+v", loaded.Cases)
	}
	for name, artifact := range result.Publication.Artifacts {
		if _, err := os.Stat(filepath.Join(dir, artifact.Path)); err != nil {
			t.Fatalf("required artifact %s (%s): %v", name, artifact.Path, err)
		}
	}
}

func TestValidatePublishedLMEResultRejectsDenominatorTotalMismatch(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"case-1"}
	result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
	prepareLMEPublicationFixture(t, dir, result, ids, nil)
	if err := finalizeLMERunResult(dir, result, ids); err != nil {
		t.Fatal(err)
	}
	result.Publication.FixedDenominator.TotalCases++
	err := validatePublishedLMEResult(filepath.Join(dir, lmeResultsFileName), result)
	if err == nil || !strings.Contains(err.Error(), "fixed denominator total") {
		t.Fatalf("validatePublishedLMEResult() error = %v", err)
	}
}

func TestValidateLMEResultArtifactRejectsUnsafeFileTypes(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular.json")
	if err := os.WriteFile(regular, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := digestLMEPath(regular)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(root, "linked.json")
		if err := os.Symlink(regular, path); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		err := validateLMEResultArtifact(root, lmeResultArtifact{
			Path: filepath.Base(path), SHA256: digest,
		})
		if err == nil {
			t.Fatal("validateLMEResultArtifact() accepted a symbolic link")
		}
	})

	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(root, "artifact.fifo")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Skipf("create FIFO: %v", err)
		}
		err := validateLMEResultArtifact(root, lmeResultArtifact{
			Path: filepath.Base(path), SHA256: digest,
		})
		if err == nil {
			t.Fatal("validateLMEResultArtifact() accepted a FIFO")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(root, "oversized.bin")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(lmeResultArtifactMaxFileBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := digestLMEPath(path); err == nil {
			t.Fatal("digestLMEPath() accepted an oversized file")
		}
	})
}

func TestFinalizeLMERunResultRejectsTamperedActualInputs(t *testing.T) {
	tests := []struct {
		name string
		get  func(lmeArtifactSet) lmeArtifactProvenance
	}{
		{name: "canonical replay", get: func(set lmeArtifactSet) lmeArtifactProvenance { return set.CanonicalReplay }},
		{name: "build plan", get: func(set lmeArtifactSet) lmeArtifactProvenance { return set.BuildPlan }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ids := []string{"case-1"}
			result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
			prepareLMEPublicationFixture(t, dir, result, ids, nil)
			manifest, err := readLMERunManifest(filepath.Join(dir, lmeRunManifestResultFileName))
			if err != nil {
				t.Fatal(err)
			}
			artifact := test.get(manifest.Artifacts)
			path, err := resolveLMEInputLocator(filepath.Dir(dir), artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.IsDir() {
				path = filepath.Join(path, lmeReplayIndexFile)
			}
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n{}\n"); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			err = finalizeLMERunResult(dir, result, ids)
			if err == nil || !strings.Contains(err.Error(), "artifact digest does not match the actual content") {
				t.Fatalf("finalize error = %v, want actual digest rejection", err)
			}
		})
	}
}

func TestReadLMEOfficialRunResultUsesImmutableReplayAfterSourceChanges(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"case-1"}
	result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
	prepareLMEPublicationFixture(t, dir, result, ids, nil)
	if err := finalizeLMERunResult(dir, result, ids); err != nil {
		t.Fatal(err)
	}
	datasetPath := filepath.Join(dir, "inputs", "dataset.json")
	if err := os.WriteFile(datasetPath, []byte("tampered dataset"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLMEOfficialRunResult(filepath.Join(dir, lmeResultsFileName)); err != nil {
		t.Fatalf("readLMEOfficialRunResult() error = %v", err)
	}
}

func TestFinalizeLMERunResultRejectsCaseSetProblems(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*lmeRunResult)
		blocker string
	}{
		{
			name: "duplicate",
			mutate: func(result *lmeRunResult) {
				result.Cases[1].QuestionID = result.Cases[0].QuestionID
			},
			blocker: "appears 2 times",
		},
		{
			name: "missing",
			mutate: func(result *lmeRunResult) {
				result.Cases = result.Cases[:1]
				result.Summary.CompletedCases = 1
			},
			blocker: "missing case IDs",
		},
		{
			name: "missing status",
			mutate: func(result *lmeRunResult) {
				result.Cases[1].Status = lmeCaseStatusMissing
				result.Summary.CompletedCases = 1
			},
			blocker: "non-terminal status",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ids := []string{"case-1", "case-2"}
			result := testLMEResult(ids, []lmeCaseStatus{
				lmeCaseStatusSucceeded,
				lmeCaseStatusSucceeded,
			})
			prepareLMEPublicationFixture(t, dir, result, ids, nil)
			test.mutate(result)
			err := finalizeLMERunResult(dir, result, ids)
			if err == nil || !strings.Contains(err.Error(), test.blocker) {
				t.Fatalf("finalize error = %v, want blocker %q", err, test.blocker)
			}
			if _, statErr := os.Stat(filepath.Join(dir, lmeResultsFileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("ineligible result was published: %v", statErr)
			}
		})
	}
}

func TestFinalizeLMERunResultEligibilityBlockers(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		blocker string
	}{
		{
			name: "schema",
			mutate: func(manifest map[string]any) {
				manifest["schema_version"] = 1
			},
			blocker: "run manifest schema version is 1, want 5",
		},
		{
			name: "dirty provenance",
			mutate: func(manifest map[string]any) {
				manifest["code"].(map[string]any)["benchmark"].(map[string]any)["dirty_state"] = "dirty"
			},
			blocker: "worktree is dirty",
		},
		{
			name: "missing digest",
			mutate: func(manifest map[string]any) {
				artifacts := manifest["artifacts"].(map[string]any)
				artifacts["build_plan"].(map[string]any)["digest"] = ""
			},
			blocker: "build plan digest is unavailable",
		},
		{
			name: "top k",
			mutate: func(manifest map[string]any) {
				manifest["run"].(map[string]any)["effective_top_k"] = 30
			},
			blocker: "effective retrieval top-k is 30",
		},
		{
			name: "protocol",
			mutate: func(manifest map[string]any) {
				manifest["run"].(map[string]any)["build_protocol"] = "unknown"
			},
			blocker: "unsupported build protocol",
		},
		{
			name: "config mismatch",
			mutate: func(manifest map[string]any) {
				manifest["run"].(map[string]any)["model_name"] = "other-model"
			},
			blocker: "result model does not match run manifest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ids := []string{"case-1"}
			result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
			prepareLMEPublicationFixture(t, dir, result, ids, test.mutate)
			err := finalizeLMERunResult(dir, result, ids)
			if err == nil || !strings.Contains(err.Error(), test.blocker) {
				t.Fatalf("finalize error = %v, want blocker %q", err, test.blocker)
			}
		})
	}
}

func TestReadLMEOfficialRunResultRejectsDiagnosticAndMissingArtifact(t *testing.T) {
	t.Run("diagnostic", func(t *testing.T) {
		dir := t.TempDir()
		result := testLMEResult(
			[]string{"case-1"},
			[]lmeCaseStatus{lmeCaseStatusSucceeded},
		)
		result.Publication = &lmePublication{
			SchemaVersion:  lmeResultSchemaVersion,
			Classification: "diagnostic",
			Eligible:       false,
		}
		data, err := marshalLMEJSON(result)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, lmeResultsFileName)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readLMEOfficialRunResult(path); err == nil ||
			!strings.Contains(err.Error(), "diagnostic") {
			t.Fatalf("read error = %v", err)
		}
	})

	t.Run("recovered from logs", func(t *testing.T) {
		dir := t.TempDir()
		ids := []string{"case-1"}
		result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
		prepareLMEPublicationFixture(t, dir, result, ids, nil)
		if err := finalizeLMERunResult(dir, result, ids); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, lmeResultsFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		raw["metadata"].(map[string]any)["recovered_from_logs"] = true
		data, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readLMEOfficialRunResult(path); err == nil ||
			!strings.Contains(err.Error(), "diagnostic only") {
			t.Fatalf("read error = %v", err)
		}
	})

	t.Run("missing bad case markdown", func(t *testing.T) {
		dir := t.TempDir()
		ids := []string{"case-1"}
		result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
		prepareLMEPublicationFixture(t, dir, result, ids, nil)
		if err := finalizeLMERunResult(dir, result, ids); err != nil {
			t.Fatal(err)
		}
		badCasesPath := result.Publication.Artifacts["bad_cases_en"].Path
		if err := os.Remove(filepath.Join(dir, badCasesPath)); err != nil {
			t.Fatal(err)
		}
		_, err := readLMEOfficialRunResult(filepath.Join(dir, lmeResultsFileName))
		if err == nil || !strings.Contains(err.Error(), "bad_cases_en") {
			t.Fatalf("read error = %v", err)
		}
	})

	t.Run("result does not match aggregate", func(t *testing.T) {
		dir := t.TempDir()
		ids := []string{"case-1"}
		result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
		prepareLMEPublicationFixture(t, dir, result, ids, nil)
		if err := finalizeLMERunResult(dir, result, ids); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, lmeResultsFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		raw["summary"].(map[string]any)["successful_cases"] = float64(0)
		data, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readLMEOfficialRunResult(path); err == nil ||
			!strings.Contains(err.Error(), "aggregate artifact does not match") {
			t.Fatalf("read error = %v", err)
		}
	})
}

func TestValidateLMEReportCompatibilityRejectsMixedRuns(t *testing.T) {
	result := func(digest string, topK int) *lmeRunResult {
		return &lmeRunResult{
			Metadata: &lmeMetadata{Config: lmeRunConfig{RetrievalTopK: topK}},
			Publication: &lmePublication{RunManifest: lmePublishedRunManifest{
				ComparisonDigest: digest,
			}},
		}
	}
	first := result("sha256:"+strings.Repeat("1", 64), lmeRetrievalTopK)
	second := result("sha256:"+strings.Repeat("2", 64), lmeRetrievalTopK)
	if err := validateLMEReportCompatibility([]*lmeRunResult{first, second}); err == nil ||
		!strings.Contains(err.Error(), "mix protocol, top-k, or immutable artifact digests") {
		t.Fatalf("compatibility error = %v", err)
	}
	second.Publication.RunManifest.ComparisonDigest = first.Publication.RunManifest.ComparisonDigest
	second.Metadata.Config.RetrievalTopK = lmeRetrievalTopK + 1
	if err := validateLMEReportCompatibility([]*lmeRunResult{first, second}); err == nil ||
		!strings.Contains(err.Error(), "fixed top-k") {
		t.Fatalf("top-k error = %v", err)
	}
}

func TestLMECostTokensKnownSemantics(t *testing.T) {
	result := testLMEResult(
		[]string{"case-1"},
		[]lmeCaseStatus{lmeCaseStatusSucceeded},
	)
	result.Cost.LLM.QA = lmeCostBucket{Calls: 1, TokensKnown: false}
	result.Cost.LLM.Total = result.Cost.LLM.QA
	if blockers := validateLMECost(result); len(blockers) != 0 {
		t.Fatalf("unknown provider tokens should remain valid: %v", blockers)
	}
	if got := lmeCostTokensLabel(result.Cost.LLM.QA); got != "0?" {
		t.Fatalf("unknown token label = %q", got)
	}
	result.Cost.LLM.QA.TokensKnown = true
	if got := lmeCostTokensLabel(result.Cost.LLM.QA); got != "0" {
		t.Fatalf("known zero token label = %q", got)
	}

	dir := t.TempDir()
	ids := []string{"case-1"}
	result = testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
	prepareLMEPublicationFixture(t, dir, result, ids, nil)
	if err := finalizeLMERunResult(dir, result, ids); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, lmeResultsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	cost := raw["cost"].(map[string]any)
	llm := cost["llm"].(map[string]any)
	delete(llm["qa"].(map[string]any), "tokens_known")
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLMEOfficialRunResult(path); err == nil ||
		!strings.Contains(err.Error(), "tokens_known") {
		t.Fatalf("read error = %v", err)
	}
}

func TestFinalizeLMERunResultAtomicFailurePreservesResults(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"case-1"}
	result := testLMEResult(ids, []lmeCaseStatus{lmeCaseStatusSucceeded})
	prepareLMEPublicationFixture(t, dir, result, ids, nil)
	resultsPath := filepath.Join(dir, lmeResultsFileName)
	if err := os.WriteFile(resultsPath, []byte("previous"), 0644); err != nil {
		t.Fatal(err)
	}
	deps := defaultLMEResultGovernanceDependencies()
	deps.writeAtomic = func(path string, data []byte, mode fs.FileMode) error {
		if filepath.Base(path) == lmeResultsFileName {
			return errors.New("injected publish failure")
		}
		return writeLMEAtomicFile(path, data, mode)
	}
	err := finalizeLMERunResultWithDependencies(dir, result, ids, deps)
	if err == nil || !strings.Contains(err.Error(), "injected publish failure") {
		t.Fatalf("finalize error = %v", err)
	}
	data, readErr := os.ReadFile(resultsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "previous" {
		t.Fatalf("previous result replaced with %q", data)
	}
}

func TestRenderLMEBadCasesEnglishAndChinese(t *testing.T) {
	artifact := &lmeBadCaseArtifact{
		Scenario:    "auto",
		Backend:     "pgvector",
		Denominator: newLMEFixedDenominator([]string{"case-1"}),
		Cases: []lmeBadCase{{
			QuestionID:   "case-1",
			QuestionType: "single-session-user",
			Status:       lmeCaseStatusFailed,
		}},
	}
	if got := renderLMEBadCases(artifact, false); !strings.Contains(got, "Bad Cases") {
		t.Fatalf("English output = %q", got)
	}
	if got := renderLMEBadCases(artifact, true); !strings.Contains(got, "失败样本") {
		t.Fatalf("Chinese output = %q", got)
	}
	result := testLMEResult(
		[]string{"case-1"},
		[]lmeCaseStatus{lmeCaseStatusSucceeded},
	)
	result.Publication = &lmePublication{
		Classification: lmeResultClassMaintained,
		Eligible:       true,
		RunManifest: lmePublishedRunManifest{
			CompatibilityDigest: "sha256:compatibility",
		},
		Artifacts: map[string]lmeResultArtifact{
			"bad_cases_en": {Path: "bad_cases.123456789abc.md"},
		},
	}
	if got := renderLMEReport([]*lmeRunResult{result}, lmeRunConfig{}, false); !strings.Contains(got, "Maintained Result Eligibility") ||
		!strings.Contains(got, "bad_cases.123456789abc.md") {
		t.Fatalf("English report = %q", got)
	}
	if got := renderLMEReport([]*lmeRunResult{result}, lmeRunConfig{}, true); !strings.Contains(got, "正式结果资格") {
		t.Fatalf("Chinese report = %q", got)
	}
}

func TestWriteLMEAtomicFileRenameFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	ops := defaultLMEAtomicFileOperations()
	ops.rename = func(string, string) error { return errors.New("rename failed") }
	err := writeLMEAtomicFileWithOperations(path, []byte("new"), 0644, ops)
	if err == nil {
		t.Fatal("writeLMEAtomicFileWithOperations() expected error")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old" {
		t.Fatalf("destination = %q", data)
	}
}

func testLMEResult(ids []string, statuses []lmeCaseStatus) *lmeRunResult {
	result := newLMERunResult("auto", "pgvector", lmeRunConfig{
		ModelName:                    "test-model",
		EmbedModelName:               "test-embedding",
		LLMEndpointFingerprint:       "provider-default",
		EmbeddingEndpointFingerprint: "provider-default",
		AutoUpdatePolicy:             lmeAutoUpdatePolicyMergeSimilar,
		ConversationExtraction:       string(lmeConversationExtractionDisabled),
		RetrievalTopK:                lmeRetrievalTopK,
		TraceContentMode:             lmeTraceContentHash,
	}, len(ids))
	result.Cost = newLMECostTracker().snapshot()
	result.Cases = make([]*lmeCaseResult, 0, len(ids))
	for i, id := range ids {
		status := lmeCaseStatusSucceeded
		if i < len(statuses) {
			status = statuses[i]
		}
		record := &lmeCaseResult{
			Status:             status,
			QuestionID:         id,
			QuestionType:       "single-session-user",
			BuildObservability: lmeBuildObservabilityUnknown,
		}
		if status == lmeCaseStatusSucceeded {
			record.Correct = true
			record.Metrics.Accuracy = 1
			record.FailureStage = lmeFailureSuccess
		}
		if status == lmeCaseStatusFailed {
			record.Error = "evaluation failed"
			record.FailureStage = lmeFailureAnswerGenerationMiss
		}
		if status == lmeCaseStatusJudgeFailed {
			record.JudgeError = "judge failed"
			record.FailureStage = lmeFailureJudgeError
		}
		result.Cases = append(result.Cases, record)
	}
	aggregateLMERunResult(result, time.Second, len(ids))
	return result
}

func prepareLMEPublicationFixture(
	t *testing.T,
	dir string,
	result *lmeRunResult,
	ids []string,
	mutate func(map[string]any),
) {
	t.Helper()
	inputRoot := filepath.Join(dir, "inputs")
	if err := os.MkdirAll(inputRoot, 0755); err != nil {
		t.Fatal(err)
	}
	datasetPath, manifestPath, datasetDigest, caseManifestDigest :=
		writeLMEPublicationSourceFixtures(t, inputRoot, ids)
	replay := writeLMEPublicationReplayFixture(
		t,
		inputRoot,
		ids,
		datasetDigest,
		caseManifestDigest,
	)
	plan := writeLMEPublicationBuildPlanFixture(
		t,
		inputRoot,
		replay,
		result.Metadata.Config.ModelName,
	)
	cfg := result.Metadata.Config
	cfg.DatasetPath = datasetPath
	cfg.ManifestPath = manifestPath
	cfg.ReplayRoot = filepath.Join(inputRoot, "replay")
	cfg.BuildPlanRoot = filepath.Join(inputRoot, "build_plan")
	cfg.ReplayDigest = replay.Index.ReplayDigest
	cfg.BuildPlanDigest = plan.Index.BuildPlanDigest
	cfg.BuildTokenizer = plan.Index.Config.Tokenizer
	cfg.BuildTokenizerModel = plan.Index.Config.Model
	cfg.BuildTokenizerEncoding = plan.Index.Config.Encoding
	cfg.BuildMaxTokens = plan.Index.Config.MaxTokens
	cfg.BuildStats = plan.Index.Stats
	cfg.RetrievalTopK = lmeRetrievalTopK
	cfg.TraceContentMode = lmeTraceContentHash
	result.Metadata.Config = cfg
	setLMERunMetadata(result, &lmeAutoEvaluator{})
	runManifest, err := buildLMERunManifestAt(
		context.Background(),
		lmeRunManifestRequest{
			Scenario: result.Metadata.Scenario,
			Backend:  result.Metadata.MemoryBackend,
			Table:    "memory_eval_test",
			Config:   cfg,
			CaseIDs:  append([]string(nil), ids...),
		},
		dir,
		testLMEProvenanceDependencies(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatal(err)
	}
	finalizeLMEPublicationManifestFixture(t, runManifest, mutate)
	writeLMEPublicationRunManifestFixture(t, dir, runManifest)
	bindLMERunManifest(result, runManifest)
	for _, id := range ids {
		writeLMEGovernanceTraceAttempt(t, dir, plan.Cases[id], result, id, 1)
	}
}

func writeLMEPublicationSourceFixtures(
	t *testing.T,
	inputRoot string,
	ids []string,
) (string, string, string, string) {
	t.Helper()
	datasetPath := filepath.Join(inputRoot, "dataset.json")
	manifestPath := filepath.Join(inputRoot, "case_manifest.json")
	datasetData, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(datasetPath, datasetData, 0644); err != nil {
		t.Fatal(err)
	}
	datasetDigest, err := digestLMEFile(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestCases := make([]dataset.LongMemEvalManifestCase, 0, len(ids))
	for _, id := range ids {
		manifestCases = append(manifestCases, dataset.LongMemEvalManifestCase{
			CaseID:       id,
			QuestionType: "single-session-user",
		})
	}
	manifest := &dataset.LongMemEvalManifest{
		SchemaVersion: dataset.LongMemEvalManifestSchemaVersion,
		Method:        dataset.LongMemEvalManifestMethodFullCategory,
		QuestionTypes: []string{"single-session-user"},
		Quotas:        map[string]int{"single-session-user": len(ids)},
		TotalSize:     len(ids),
		CaseIDs:       append([]string(nil), ids...),
		Cases:         manifestCases,
		DatasetDigest: "sha256:" + datasetDigest,
	}
	manifest.ManifestDigest, err = dataset.LongMemEvalManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := dataset.WriteLongMemEvalManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	caseManifestDigest, err := digestLMEFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return datasetPath, manifestPath, datasetDigest, caseManifestDigest
}

func writeLMEPublicationReplayFixture(
	t *testing.T,
	inputRoot string,
	ids []string,
	datasetDigest string,
	caseManifestDigest string,
) *lmeReplayCorpus {
	t.Helper()
	replayCases := make([]*lmeReplayCase, 0, len(ids))
	for index, id := range ids {
		sessionID := fmt.Sprintf("session-%d", index+1)
		replayCases = append(replayCases, &lmeReplayCase{
			Version: lmeReplayVersion,
			CaseID:  id,
			Sessions: []lmeReplaySession{{
				SessionIndex:    0,
				SessionID:       sessionID,
				ObservationTime: "2025-01-01T00:00:00Z",
				Turns: []lmeReplayTurn{
					{
						TurnIndex: 0,
						TurnID:    lmeStableArtifactID("turn", id, sessionID, "0"),
						Role:      "user",
						Content:   "remember " + id,
					},
					{
						TurnIndex: 1,
						TurnID:    lmeStableArtifactID("turn", id, sessionID, "1"),
						Role:      "assistant",
						Content:   "noted",
					},
				},
			}},
		})
	}
	replay, err := newLMEReplayCorpus(datasetDigest, caseManifestDigest, replayCases)
	if err != nil {
		t.Fatal(err)
	}
	replayRoot := filepath.Join(inputRoot, "replay")
	if err := writeLMEReplayCorpus(replayRoot, replay); err != nil {
		t.Fatal(err)
	}
	replay, err = loadLMEReplayCorpus(replayRoot)
	if err != nil {
		t.Fatal(err)
	}
	return replay
}

func writeLMEPublicationBuildPlanFixture(
	t *testing.T,
	inputRoot string,
	replay *lmeReplayCorpus,
	modelName string,
) *lmeBuildPlanCorpus {
	t.Helper()
	config := lmeBuildPlanConfig{
		Tokenizer:    "governance-rune",
		Model:        modelName,
		MaxTokens:    128,
		ReplayDigest: replay.Index.ReplayDigest,
	}
	tokenizer := governanceLMEPlanTokenizer{}
	planner, err := newLMEBuildPlanner(config, tokenizer.Tokenize)
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := lmeJSONDigest(config)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	planRoot := filepath.Join(inputRoot, "build_plan")
	if err := writeLMEBuildPlan(planRoot, plan); err != nil {
		t.Fatal(err)
	}
	plan, err = loadLMEBuildPlan(planRoot)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func finalizeLMEPublicationManifestFixture(
	t *testing.T,
	runManifest *lmeRunManifest,
	mutate func(map[string]any),
) {
	t.Helper()
	if mutate != nil {
		data, err := json.Marshal(runManifest)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		mutate(raw)
		data, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := lmeDecodeStrict(data, runManifest); err != nil {
			t.Fatal(err)
		}
		runManifest.OfficialBlockers = deriveLMERunManifestBlockers(runManifest)
		runManifest.Reproducible = len(runManifest.OfficialBlockers) == 0
		runManifest.OfficialStatus = lmeOfficialStatusBlocked
		if runManifest.Reproducible {
			runManifest.OfficialStatus = lmeOfficialStatusEligible
		}
	}
	var err error
	runManifest.CompatibilityDigest, err = calculateLMERunCompatibilityDigest(runManifest)
	if err != nil {
		t.Fatal(err)
	}
	runManifest.ComparisonDigest, err = calculateLMEComparisonDigest(runManifest)
	if err != nil {
		t.Fatal(err)
	}
}

func writeLMEPublicationRunManifestFixture(
	t *testing.T,
	dir string,
	runManifest *lmeRunManifest,
) {
	t.Helper()
	data, err := marshalLMEJSON(runManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lmeRunManifestResultFileName), data, 0644); err != nil {
		t.Fatal(err)
	}
}

type governanceLMEPlanTokenizer struct{}

func (governanceLMEPlanTokenizer) Tokenize(text string) ([]string, error) {
	pieces := make([]string, 0, len(text))
	for _, value := range text {
		pieces = append(pieces, string(value))
	}
	return pieces, nil
}

func writeLMEGovernanceTraceAttempt(
	t *testing.T,
	dir string,
	casePlan *lmeBuildCasePlan,
	result *lmeRunResult,
	caseID string,
	attempt int,
) string {
	t.Helper()
	path := filepath.Join(
		dir,
		"build_trace",
		fmt.Sprintf("%s.attempt-%04d.jsonl", lmeTraceFileName(caseID), attempt),
	)
	writer, err := newLMEJSONLTraceWriter(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range expectedLMETraceBuildSources(casePlan) {
		source := source
		if err := writer.Write(&lmeTraceRecord{
			CaseID: caseID, ContentMode: lmeTraceContentHash,
			Event: lmeTraceEventExtraction, Source: &source,
			Extraction: &lmeTraceExtraction{
				Input:               traceLMEText(lmeTraceContentHash, "build input", true),
				EffectiveOperations: lmeTraceEffectiveOperationsUnavailable,
				UnavailableReason:   "backend operation details are unavailable",
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := writer.Write(&lmeTraceRecord{
			CaseID: caseID, ContentMode: lmeTraceContentHash,
			Event: lmeTraceEventPersistence, Source: &source,
			Persistence: &lmeTracePersistence{
				Acknowledged:      true,
				ActualOperations:  lmeTraceEffectiveOperationsUnavailable,
				UnavailableReason: "backend persistence details are unavailable",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Write(&lmeTraceRecord{
		CaseID: caseID, ContentMode: lmeTraceContentHash,
		Event: lmeTraceEventGoldJoin,
		Gold:  &lmeTraceGoldJoin{JoinedAfterQA: true},
	}); err != nil {
		t.Fatal(err)
	}
	var record *lmeCaseResult
	for _, candidate := range result.Cases {
		if candidate != nil && candidate.QuestionID == caseID {
			record = candidate
			break
		}
	}
	if record == nil {
		t.Fatalf("missing result case %s", caseID)
	}
	if err := writer.Write(&lmeTraceRecord{
		CaseID: caseID, ContentMode: lmeTraceContentHash,
		Event: lmeTraceEventOutcome,
		Outcome: &lmeTraceOutcome{
			FailureStage:       record.FailureStage,
			BuildObservability: record.BuildObservability,
			GoldSessionRecall:  record.GoldSessionRecall,
			Correct:            record.Correct,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveLMEInputLocatorContainment(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "inputs", "dataset.json")
	if err := os.MkdirAll(filepath.Dir(child), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("dataset"), 0644); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveLMEInputLocator(base, "inputs/dataset.json")
	if err != nil {
		t.Fatalf("resolveLMEInputLocator() error = %v", err)
	}
	if resolved != child {
		t.Fatalf("resolved path = %q, want %q", resolved, child)
	}

	for _, locator := range []string{
		"",
		".",
		"../dataset.json",
		"inputs/../../dataset.json",
		filepath.Join(string(filepath.Separator), "dataset.json"),
		`C:\dataset.json`,
		`\\server\share\dataset.json`,
	} {
		t.Run(strings.ReplaceAll(locator, string(filepath.Separator), "_"), func(t *testing.T) {
			if _, err := resolveLMEInputLocator(base, locator); err == nil {
				t.Fatalf("resolveLMEInputLocator(%q) accepted an unsafe locator", locator)
			}
		})
	}
}

func TestResolveLMEInputLocatorRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "dataset.json"), []byte("dataset"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "linked")); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if _, err := resolveLMEInputLocator(base, "linked/dataset.json"); err == nil ||
		!strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("resolveLMEInputLocator() error = %v, want symlink rejection", err)
	}

	baseLink := filepath.Join(t.TempDir(), "base-link")
	if err := os.Symlink(base, baseLink); err != nil {
		t.Skipf("create base symlink: %v", err)
	}
	if _, err := resolveLMEInputLocator(baseLink, "target/dataset.json"); err == nil {
		t.Fatal("resolveLMEInputLocator() accepted a symlink base")
	}
}
