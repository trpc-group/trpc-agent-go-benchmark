//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type prediction struct {
	ModelNameOrPath string `json:"model_name_or_path"`
	InstanceID      string `json:"instance_id"`
	ModelPatch      string `json:"model_patch"`
}

type caseManifest struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo,omitempty"`
	BaseCommit       string `json:"base_commit,omitempty"`
	ProblemStatement string `json:"problem_statement,omitempty"`
}

type importedCase struct {
	InstanceID string       `json:"instance_id"`
	Repo       string       `json:"repo,omitempty"`
	BaseCommit string       `json:"base_commit,omitempty"`
	Baseline   targetResult `json:"baseline"`
}

type targetResult struct {
	MainStatus        string     `json:"main_status"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	ModelNameOrPath   string     `json:"model_name_or_path,omitempty"`
	PatchPath         string     `json:"patch_path,omitempty"`
	TracePath         string     `json:"trace_path,omitempty"`
	VerifierResultRef string     `json:"verifier_result_ref,omitempty"`
	PatchStats        patchStats `json:"patch_stats"`
	Usage             usageStats `json:"usage"`
}

type patchStats struct {
	ChangedFiles []string `json:"changed_files"`
	AddedLines   int      `json:"added_lines"`
	DeletedLines int      `json:"deleted_lines"`
	PatchLines   int      `json:"patch_lines"`
}

type usageStats struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	APICalls         int `json:"api_calls"`
}

type importSummary struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Target      string         `json:"target"`
	Total       int            `json:"total"`
	Counts      map[string]int `json:"counts"`
}

func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	target := fs.String("target", "baseline", "target name; first version writes baseline")
	casesPath := fs.String("cases", "", "optional canonical cases.jsonl")
	predsPath := fs.String("predictions", "", "mini preds.json path")
	rawDir := fs.String("raw-dir", "", "mini raw output directory containing per-case trajectories")
	harnessReport := fs.String("harness-report", "", "SWE-Bench harness report JSON path")
	output := fs.String("output", "", "normalized output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "predictions", *predsPath); err != nil {
		return err
	}
	if err := required(fs, "output", *output); err != nil {
		return err
	}
	if *target != "baseline" {
		return fmt.Errorf("only -target baseline is supported in this first importer")
	}
	if err := ensureDir(*output); err != nil {
		return err
	}

	preds, err := readPredictions(*predsPath)
	if err != nil {
		return err
	}
	cases, err := readCases(*casesPath, preds)
	if err != nil {
		return err
	}
	harness, err := readHarnessReport(*harnessReport)
	if err != nil {
		return err
	}

	patchDir := filepath.Join(*output, "patches", *target)
	traceDir := filepath.Join(*output, "traces", *target)
	if err := ensureDir(patchDir); err != nil {
		return err
	}
	if err := ensureDir(traceDir); err != nil {
		return err
	}

	outPath := filepath.Join(*output, "cases.jsonl")
	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outFile.Close()
	writer := bufio.NewWriter(outFile)
	defer writer.Flush()

	summary := importSummary{
		GeneratedAt: time.Now().UTC(),
		Target:      *target,
		Total:       len(cases),
		Counts:      map[string]int{},
	}

	for _, c := range cases {
		pred, hasPred := preds[c.InstanceID]
		result := targetResult{}
		if hasPred {
			result.ModelNameOrPath = pred.ModelNameOrPath
			if strings.TrimSpace(pred.ModelPatch) != "" {
				patchPath := filepath.Join(patchDir, c.InstanceID+".patch")
				if err := os.WriteFile(patchPath, []byte(pred.ModelPatch), 0o644); err != nil {
					return err
				}
				result.PatchPath = relPath(*output, patchPath)
				result.PatchStats = computePatchStats(pred.ModelPatch)
			}
			if *rawDir != "" {
				tracePath, usage, err := copyScrubbedTrace(*rawDir, traceDir, c.InstanceID)
				if err == nil && tracePath != "" {
					result.TracePath = relPath(*output, tracePath)
					result.Usage = usage
				}
			}
		}
		status, reason := classify(c.InstanceID, hasPred, pred.ModelPatch, harness)
		result.MainStatus = status
		result.FailureReason = reason
		if *harnessReport != "" {
			result.VerifierResultRef = absPath(*harnessReport)
		}
		summary.Counts[status]++

		row := importedCase{
			InstanceID: c.InstanceID,
			Repo:       c.Repo,
			BaseCommit: c.BaseCommit,
			Baseline:   result,
		}
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(*output, "summary", *target+".json"), summary)
}

func readPredictions(path string) (map[string]prediction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var byID map[string]prediction
	if err := json.Unmarshal(data, &byID); err == nil && byID != nil {
		for id, pred := range byID {
			if pred.InstanceID == "" {
				pred.InstanceID = id
				byID[id] = pred
			}
		}
		return byID, nil
	}
	var rows []prediction
	if err := json.Unmarshal(data, &rows); err == nil {
		out := map[string]prediction{}
		for _, row := range rows {
			out[row.InstanceID] = row
		}
		return out, nil
	}
	out := map[string]prediction{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row prediction
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		out[row.InstanceID] = row
	}
	return out, scanner.Err()
}

func readCases(path string, preds map[string]prediction) ([]caseManifest, error) {
	if strings.TrimSpace(path) == "" {
		ids := make([]string, 0, len(preds))
		for id := range preds {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		cases := make([]caseManifest, 0, len(ids))
		for _, id := range ids {
			cases = append(cases, caseManifest{InstanceID: id})
		}
		return cases, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cases []caseManifest
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c caseManifest
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		if c.InstanceID == "" {
			return nil, fmt.Errorf("case without instance_id in %s", path)
		}
		cases = append(cases, c)
	}
	return cases, scanner.Err()
}

type harnessIndex struct {
	Resolved   map[string]bool
	Unresolved map[string]bool
	Errors     map[string]bool
	Completed  map[string]bool
}

func readHarnessReport(path string) (harnessIndex, error) {
	idx := harnessIndex{
		Resolved:   map[string]bool{},
		Unresolved: map[string]bool{},
		Errors:     map[string]bool{},
		Completed:  map[string]bool{},
	}
	if strings.TrimSpace(path) == "" {
		return idx, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return idx, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return idx, err
	}
	addIDs := func(key string, dst map[string]bool) {
		if arr, ok := raw[key].([]any); ok {
			for _, v := range arr {
				if id, ok := v.(string); ok {
					dst[id] = true
				}
			}
		}
	}
	addIDs("resolved_ids", idx.Resolved)
	addIDs("unresolved_ids", idx.Unresolved)
	addIDs("error_ids", idx.Errors)
	addIDs("completed_ids", idx.Completed)
	for id, v := range raw {
		if row, ok := v.(map[string]any); ok {
			if b, _ := row["resolved"].(bool); b {
				idx.Resolved[id] = true
			} else if _, has := row["resolved"]; has {
				idx.Unresolved[id] = true
			}
		}
	}
	return idx, nil
}

func classify(instanceID string, hasPred bool, patch string, harness harnessIndex) (string, string) {
	if !hasPred {
		return "incomplete", "missing prediction"
	}
	if strings.TrimSpace(patch) == "" {
		return "empty_patch", "empty model_patch"
	}
	if harness.Errors[instanceID] {
		return "error", "harness error"
	}
	if harness.Resolved[instanceID] {
		return "resolved", ""
	}
	if harness.Unresolved[instanceID] {
		return "unresolved", "failed official harness"
	}
	return "incomplete", "missing harness result"
}

func computePatchStats(patch string) patchStats {
	stats := patchStats{}
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		if line == "" {
			continue
		}
		stats.PatchLines++
		if strings.HasPrefix(line, "+++ b/") {
			file := strings.TrimPrefix(line, "+++ b/")
			if file != "/dev/null" && !seen[file] {
				seen[file] = true
				stats.ChangedFiles = append(stats.ChangedFiles, file)
			}
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			stats.AddedLines++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			stats.DeletedLines++
		}
	}
	sort.Strings(stats.ChangedFiles)
	return stats
}

func copyScrubbedTrace(rawDir, traceDir, instanceID string) (string, usageStats, error) {
	src := filepath.Join(rawDir, instanceID, instanceID+".traj.json")
	data, err := os.ReadFile(src)
	if err != nil {
		return "", usageStats{}, err
	}
	usage := extractUsage(data)
	dst := filepath.Join(traceDir, instanceID+".json")
	if err := os.WriteFile(dst, redactJSONBytes(data), 0o644); err != nil {
		return "", usageStats{}, err
	}
	return dst, usage, nil
}

func extractUsage(data []byte) usageStats {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return usageStats{}
	}
	stats := usageStats{}
	walkUsage(v, &stats)
	return stats
}

func walkUsage(v any, stats *usageStats) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch strings.ToLower(k) {
			case "api_calls":
				stats.APICalls = maxInt(stats.APICalls, jsonNumberToInt(val))
			case "prompt_tokens":
				stats.PromptTokens = maxInt(stats.PromptTokens, jsonNumberToInt(val))
			case "completion_tokens":
				stats.CompletionTokens = maxInt(stats.CompletionTokens, jsonNumberToInt(val))
			case "total_tokens":
				stats.TotalTokens = maxInt(stats.TotalTokens, jsonNumberToInt(val))
			default:
				walkUsage(val, stats)
			}
		}
	case []any:
		for _, elem := range x {
			walkUsage(elem, stats)
		}
	}
}

func jsonNumberToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if b > a {
		return b
	}
	return a
}

func relPath(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}
