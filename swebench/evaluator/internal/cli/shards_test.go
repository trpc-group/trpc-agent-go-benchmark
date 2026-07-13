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

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

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
				RunID:         "run-000",
				RawDir:        filepath.Join(dir, "run-000", "raw", "mini"),
				Status:        "accepted",
				AcceptedCount: 1,
				Cases:         []shardCaseSummary{{InstanceID: "case-a", Status: "accepted"}},
			},
			{
				RunID:         "run-001",
				RawDir:        filepath.Join(dir, "run-001", "raw", "mini"),
				Status:        "accepted",
				AcceptedCount: 1,
				Cases:         []shardCaseSummary{{InstanceID: "case-a", Status: "accepted"}},
			},
		},
	}
	_, err := acceptedPredictions(manifest)
	if err == nil || !strings.Contains(err.Error(), "duplicate accepted prediction") {
		t.Fatalf("acceptedPredictions() error = %v, want duplicate error", err)
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
	for _, item := range items {
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
		StartedAt:  start,
		FinishedAt: start.Add(time.Minute),
		DurationMS: int64(time.Minute / time.Millisecond),
		Command:    commandResult{ExitCode: 0},
		Config:     runMiniConfig{Workers: workers},
	}
	if err := writeJSON(filepath.Join(rawDir, "run-mini-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
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
