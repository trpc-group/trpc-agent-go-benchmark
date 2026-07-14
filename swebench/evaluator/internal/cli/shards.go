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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

type shardsManifest struct {
	GeneratedAt          time.Time      `json:"generated_at"`
	PlanPath             string         `json:"plan_path"`
	RunsRoot             string         `json:"runs_root"`
	RawSubdir            string         `json:"raw_subdir"`
	ExpectedCases        int            `json:"expected_cases"`
	AcceptedCases        int            `json:"accepted_cases"`
	MissingCases         int            `json:"missing_cases"`
	InvalidCases         int            `json:"invalid_cases"`
	DuplicateCases       int            `json:"duplicate_cases"`
	MissingIDs           []string       `json:"missing_ids"`
	InvalidIDs           []string       `json:"invalid_ids"`
	DuplicateIDs         []string       `json:"duplicate_ids"`
	StartedAt            string         `json:"started_at,omitempty"`
	FinishedAt           string         `json:"finished_at,omitempty"`
	WallDurationMS       int64          `json:"wall_duration_ms,omitempty"`
	CumulativeDurationMS int64          `json:"cumulative_duration_ms"`
	ExitStatusCounts     map[string]int `json:"exit_status_counts"`
	Shards               []shardSummary `json:"shards"`
}

type shardSummary struct {
	Index            int                `json:"index"`
	Name             string             `json:"name"`
	RunID            string             `json:"run_id"`
	RawDir           string             `json:"raw_dir"`
	Status           string             `json:"status"`
	FailureReason    string             `json:"failure_reason,omitempty"`
	ExpectedCount    int                `json:"expected_count"`
	PredictionsCount int                `json:"predictions_count"`
	AcceptedCount    int                `json:"accepted_count"`
	MissingCount     int                `json:"missing_count"`
	InvalidCount     int                `json:"invalid_count"`
	EmptyPatchCount  int                `json:"empty_patch_count"`
	Workers          int                `json:"workers,omitempty"`
	StartedAt        string             `json:"started_at,omitempty"`
	FinishedAt       string             `json:"finished_at,omitempty"`
	DurationMS       int64              `json:"duration_ms,omitempty"`
	ExitStatusCounts map[string]int     `json:"exit_status_counts"`
	ExpectedIDs      []string           `json:"expected_ids"`
	Cases            []shardCaseSummary `json:"cases"`
}

type shardCaseSummary struct {
	InstanceID    string `json:"instance_id"`
	Status        string `json:"status"`
	ExitStatus    string `json:"exit_status,omitempty"`
	Reason        string `json:"reason,omitempty"`
	HasPrediction bool   `json:"has_prediction"`
	EmptyPatch    bool   `json:"empty_patch,omitempty"`
	PatchChars    int    `json:"patch_chars,omitempty"`
	TracePath     string `json:"trace_path,omitempty"`
}

func runSummarizeShards(args []string) error {
	fs := flag.NewFlagSet("summarize-shards", flag.ExitOnError)
	planPath := fs.String("plan", "", "batch plan.json path")
	runsRoot := fs.String("runs-root", "results/runs", "directory containing shard run directories")
	rawSubdir := fs.String("raw-subdir", filepath.Join("raw", "mini"), "raw output subdirectory under each run")
	output := fs.String("output", "", "output shards manifest path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "plan", *planPath); err != nil {
		return err
	}
	if err := required(fs, "output", *output); err != nil {
		return err
	}
	var plan batchPlan
	if err := readJSONFile(*planPath, &plan); err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	manifest, err := summarizeShardPlan(plan, *planPath, *runsRoot, *rawSubdir)
	if err != nil {
		return err
	}
	if err := writeJSON(*output, manifest); err != nil {
		return err
	}
	fmt.Printf("accepted=%d missing=%d invalid=%d duplicate=%d shards=%d\nmanifest=%s\n",
		manifest.AcceptedCases, manifest.MissingCases, manifest.InvalidCases, manifest.DuplicateCases, len(manifest.Shards), *output)
	return nil
}

func summarizeShardPlan(plan batchPlan, planPath, runsRoot, rawSubdir string) (shardsManifest, error) {
	manifest := shardsManifest{
		GeneratedAt:      time.Now().UTC(),
		PlanPath:         absPath(planPath),
		RunsRoot:         absPath(runsRoot),
		RawSubdir:        rawSubdir,
		ExpectedCases:    plan.CaseCount,
		MissingIDs:       []string{},
		InvalidIDs:       []string{},
		DuplicateIDs:     []string{},
		ExitStatusCounts: map[string]int{},
	}
	acceptedSeen := map[string]int{}
	var earliest, latest time.Time
	for _, batch := range plan.Batches {
		shard := summarizeShard(batch, runsRoot, rawSubdir)
		manifest.Shards = append(manifest.Shards, shard)
		manifest.MissingCases += shard.MissingCount
		manifest.InvalidCases += shard.InvalidCount
		manifest.CumulativeDurationMS += shard.DurationMS
		for status, count := range shard.ExitStatusCounts {
			manifest.ExitStatusCounts[status] += count
		}
		for _, c := range shard.Cases {
			switch c.Status {
			case "accepted":
				acceptedSeen[c.InstanceID]++
			case "missing":
				manifest.MissingIDs = append(manifest.MissingIDs, c.InstanceID)
			default:
				manifest.InvalidIDs = append(manifest.InvalidIDs, c.InstanceID)
			}
		}
		if shard.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, shard.StartedAt); err == nil {
				if earliest.IsZero() || t.Before(earliest) {
					earliest = t
				}
			}
		}
		if shard.FinishedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, shard.FinishedAt); err == nil {
				if latest.IsZero() || t.After(latest) {
					latest = t
				}
			}
		}
	}
	for id, count := range acceptedSeen {
		if count > 1 {
			manifest.DuplicateIDs = append(manifest.DuplicateIDs, id)
		}
	}
	sort.Strings(manifest.MissingIDs)
	sort.Strings(manifest.InvalidIDs)
	sort.Strings(manifest.DuplicateIDs)
	manifest.AcceptedCases = len(acceptedSeen)
	manifest.DuplicateCases = len(manifest.DuplicateIDs)
	if !earliest.IsZero() {
		manifest.StartedAt = earliest.Format(time.RFC3339Nano)
	}
	if !latest.IsZero() {
		manifest.FinishedAt = latest.Format(time.RFC3339Nano)
	}
	if !earliest.IsZero() && !latest.IsZero() && latest.After(earliest) {
		manifest.WallDurationMS = latest.Sub(earliest).Milliseconds()
	}
	return manifest, nil
}

func summarizeShard(batch batchPlanItem, runsRoot, rawSubdir string) shardSummary {
	rawDir := filepath.Join(runsRoot, batch.RunID, rawSubdir)
	shard := shardSummary{
		Index:            batch.Index,
		Name:             batch.Name,
		RunID:            batch.RunID,
		RawDir:           absPath(rawDir),
		Status:           "failed",
		ExpectedCount:    len(batch.InstanceIDs),
		ExitStatusCounts: map[string]int{},
		ExpectedIDs:      append([]string(nil), batch.InstanceIDs...),
	}
	var miniManifest runMiniManifest
	manifestPath := filepath.Join(rawDir, "run-mini-manifest.json")
	if err := readJSONFile(manifestPath, &miniManifest); err == nil {
		shard.Workers = miniManifest.Config.Workers
		shard.StartedAt = miniManifest.StartedAt.UTC().Format(time.RFC3339Nano)
		shard.FinishedAt = miniManifest.FinishedAt.UTC().Format(time.RFC3339Nano)
		shard.DurationMS = miniManifest.DurationMS
		if miniManifest.Command.ExitCode != 0 {
			shard.FailureReason = fmt.Sprintf("run-mini exit code %d", miniManifest.Command.ExitCode)
		}
	} else {
		var miniGoManifest runnerManifest
		miniGoPath := filepath.Join(rawDir, "mini-go-runner-manifest.json")
		if miniGoErr := readJSONFile(miniGoPath, &miniGoManifest); miniGoErr == nil {
			shard.Workers = miniGoManifest.Workers
			shard.StartedAt = miniGoManifest.StartedAt.UTC().Format(time.RFC3339Nano)
			shard.FinishedAt = miniGoManifest.FinishedAt.UTC().Format(time.RFC3339Nano)
			shard.DurationMS = miniGoManifest.DurationMS
			if miniGoManifest.Status == "completed_with_errors" {
				shard.FailureReason = "mini-go runner completed with errors"
			}
		} else {
			shard.FailureReason = "missing or invalid run-mini-manifest.json and mini-go-runner-manifest.json"
		}
	}
	preds, err := readPredictions(filepath.Join(rawDir, "preds.json"))
	if err == nil {
		shard.PredictionsCount = len(preds)
	} else if shard.FailureReason == "" {
		shard.FailureReason = "missing or invalid preds.json"
	}
	for _, id := range batch.InstanceIDs {
		caseSummary := summarizeShardCase(rawDir, id, preds)
		shard.Cases = append(shard.Cases, caseSummary)
		switch caseSummary.Status {
		case "accepted":
			shard.AcceptedCount++
			if caseSummary.EmptyPatch {
				shard.EmptyPatchCount++
			}
			shard.ExitStatusCounts[caseSummary.ExitStatus]++
		case "missing":
			shard.MissingCount++
		default:
			shard.InvalidCount++
		}
	}
	switch {
	case shard.MissingCount == 0 && shard.InvalidCount == 0:
		shard.Status = "accepted"
	case shard.AcceptedCount > 0:
		shard.Status = "partial"
	default:
		shard.Status = "failed"
	}
	return shard
}

func summarizeShardCase(rawDir, instanceID string, preds map[string]contract.Prediction) shardCaseSummary {
	tracePath := filepath.Join(rawDir, instanceID, instanceID+".traj.json")
	relTrace := relPath(rawDir, tracePath)
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return shardCaseSummary{InstanceID: instanceID, Status: "missing", Reason: "missing trajectory", TracePath: relTrace}
	}
	exitStatus := extractExitStatus(data)
	if strings.TrimSpace(exitStatus) == "" {
		return shardCaseSummary{InstanceID: instanceID, Status: "invalid", Reason: "missing exit status", TracePath: relTrace}
	}
	pred, ok := preds[instanceID]
	if !ok {
		return shardCaseSummary{
			InstanceID: instanceID,
			Status:     "invalid",
			ExitStatus: exitStatus,
			Reason:     "missing prediction",
			TracePath:  relTrace,
		}
	}
	patch := pred.ModelPatch
	return shardCaseSummary{
		InstanceID:    instanceID,
		Status:        "accepted",
		ExitStatus:    exitStatus,
		HasPrediction: true,
		EmptyPatch:    strings.TrimSpace(patch) == "",
		PatchChars:    len(patch),
		TracePath:     relTrace,
	}
}

func extractExitStatus(data []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	if info, ok := raw["info"].(map[string]any); ok {
		if status, ok := info["exit_status"].(string); ok && strings.TrimSpace(status) != "" {
			return status
		}
	}
	if messages, ok := raw["messages"].([]any); ok {
		for i := len(messages) - 1; i >= 0; i-- {
			msg, ok := messages[i].(map[string]any)
			if !ok {
				continue
			}
			if extra, ok := msg["extra"].(map[string]any); ok {
				if status, ok := extra["exit_status"].(string); ok && strings.TrimSpace(status) != "" {
					return status
				}
			}
			if role, _ := msg["role"].(string); role == "exit" {
				if content, ok := msg["content"].(string); ok && strings.TrimSpace(content) != "" {
					return content
				}
			}
		}
	}
	return ""
}

func runMergePredictions(args []string) error {
	fs := flag.NewFlagSet("merge-predictions", flag.ExitOnError)
	shardsPath := fs.String("shards", "", "shards manifest JSON path")
	casesPath := fs.String("cases", "", "optional canonical cases.jsonl path for output order and completeness check")
	output := fs.String("output", "", "merged preds.json output path")
	allowMissing := fs.Bool("allow-missing", false, "allow missing canonical cases")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "shards", *shardsPath); err != nil {
		return err
	}
	if err := required(fs, "output", *output); err != nil {
		return err
	}
	var manifest shardsManifest
	if err := readJSONFile(*shardsPath, &manifest); err != nil {
		return fmt.Errorf("read shards manifest: %w", err)
	}
	preds, err := acceptedPredictions(manifest)
	if err != nil {
		return err
	}
	ordered, err := orderPredictions(preds, *casesPath, *allowMissing)
	if err != nil {
		return err
	}
	out := map[string]contract.Prediction{}
	for _, pred := range ordered {
		out[pred.InstanceID] = pred
	}
	if err := writeJSON(*output, out); err != nil {
		return err
	}
	fmt.Printf("merged=%d\npredictions=%s\n", len(out), *output)
	return nil
}

func acceptedPredictions(manifest shardsManifest) (map[string]contract.Prediction, error) {
	out := map[string]contract.Prediction{}
	for _, shard := range manifest.Shards {
		if shard.Status == "superseded" {
			continue
		}
		preds, err := readPredictions(filepath.Join(shard.RawDir, "preds.json"))
		if err != nil {
			if shard.AcceptedCount == 0 {
				continue
			}
			return nil, fmt.Errorf("read predictions for shard %s: %w", shard.RunID, err)
		}
		for _, c := range shard.Cases {
			if c.Status != "accepted" {
				continue
			}
			pred, ok := preds[c.InstanceID]
			if !ok {
				return nil, fmt.Errorf("accepted case %s is missing from shard %s preds.json", c.InstanceID, shard.RunID)
			}
			if _, exists := out[c.InstanceID]; exists {
				return nil, fmt.Errorf("duplicate accepted prediction for %s", c.InstanceID)
			}
			out[c.InstanceID] = pred
		}
	}
	return out, nil
}

func orderPredictions(preds map[string]contract.Prediction, casesPath string, allowMissing bool) ([]contract.Prediction, error) {
	if strings.TrimSpace(casesPath) == "" {
		ids := make([]string, 0, len(preds))
		for id := range preds {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := make([]contract.Prediction, 0, len(ids))
		for _, id := range ids {
			out = append(out, preds[id])
		}
		return out, nil
	}
	cases, err := readCases(casesPath, nil)
	if err != nil {
		return nil, err
	}
	out := make([]contract.Prediction, 0, len(cases))
	missing := []string{}
	for _, c := range cases {
		pred, ok := preds[c.InstanceID]
		if !ok {
			missing = append(missing, c.InstanceID)
			continue
		}
		out = append(out, pred)
	}
	if len(missing) > 0 && !allowMissing {
		return nil, fmt.Errorf("missing %d canonical predictions: %s", len(missing), strings.Join(firstStrings(missing, 10), ", "))
	}
	return out, nil
}

func firstStrings(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	out := append([]string(nil), values[:n]...)
	out = append(out, "...")
	return out
}
