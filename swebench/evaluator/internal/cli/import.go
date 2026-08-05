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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

const importSchemaVersion = 1

type importedCase struct {
	SchemaVersion int          `json:"schema_version"`
	InstanceID    string       `json:"instance_id"`
	Repo          string       `json:"repo,omitempty"`
	BaseCommit    string       `json:"base_commit,omitempty"`
	Target        string       `json:"target"`
	Result        targetResult `json:"result"`
}

type targetResult struct {
	MainStatus        string              `json:"main_status"`
	FailureReason     string              `json:"failure_reason,omitempty"`
	ModelNameOrPath   string              `json:"model_name_or_path,omitempty"`
	PatchPath         string              `json:"patch_path,omitempty"`
	TracePath         string              `json:"trace_path,omitempty"`
	VerifierResultRef string              `json:"verifier_result_ref,omitempty"`
	PatchStats        artifact.PatchStats `json:"patch_stats"`
	Usage             usageStats          `json:"usage"`
}

type usageStats struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	APICalls         int `json:"api_calls"`
}

type importSummary struct {
	SchemaVersion int            `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Target        string         `json:"target"`
	Total         int            `json:"total"`
	Counts        map[string]int `json:"counts"`
}

func runImport(args []string) error {
	fs := newFlagSet("import")
	target := fs.String("target", "baseline", "path-safe target label")
	casesPath := fs.String("cases", "", "optional canonical cases.jsonl")
	predsPath := fs.String("predictions", "", "mini preds.json path")
	rawDir := fs.String("raw-dir", "", "mini raw output directory containing per-case trajectories")
	shardsManifestPath := fs.String("shards-manifest", "", "optional summarize-shards output path for sharded trajectories")
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
	if err := validateTargetLabel(*target); err != nil {
		return err
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
	if strings.TrimSpace(*casesPath) != "" {
		if err := validateImportInputs(cases, preds); err != nil {
			return err
		}
	}
	traceRawDirs, err := traceRawDirsByCase(*shardsManifestPath)
	if err != nil {
		return err
	}
	harness, err := readHarnessReport(*harnessReport)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*harnessReport) != "" {
		if err := validateHarnessIndex(harness, cases); err != nil {
			return fmt.Errorf("validate harness report: %w", err)
		}
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
	var rows bytes.Buffer

	summary := importSummary{
		SchemaVersion: importSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Target:        *target,
		Total:         len(cases),
		Counts:        map[string]int{},
	}

	for _, c := range cases {
		if err := validateArtifactName("instance id", c.InstanceID); err != nil {
			return err
		}
		pred, hasPred := preds[c.InstanceID]
		result := targetResult{PatchStats: artifact.PatchStats{ChangedFiles: []string{}}}
		if hasPred {
			result.ModelNameOrPath = pred.ModelNameOrPath
			if strings.TrimSpace(pred.ModelPatch) != "" {
				patchPath := filepath.Join(patchDir, c.InstanceID+".patch")
				if err := artifact.WriteFileAtomic(patchPath, []byte(pred.ModelPatch), 0o644); err != nil {
					return err
				}
				result.PatchPath = relPath(*output, patchPath)
				result.PatchStats = artifact.ComputePatchStats(pred.ModelPatch)
			}
			caseRawDir := *rawDir
			if strings.TrimSpace(caseRawDir) == "" {
				caseRawDir = traceRawDirs[c.InstanceID]
			}
			if caseRawDir != "" {
				tracePath, usage, err := copyScrubbedTrace(caseRawDir, traceDir, c.InstanceID)
				if err != nil {
					return fmt.Errorf("copy trace for %s: %w", c.InstanceID, err)
				}
				result.TracePath = relPath(*output, tracePath)
				result.Usage = usage
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
			SchemaVersion: importSchemaVersion,
			InstanceID:    c.InstanceID,
			Repo:          c.Repo,
			BaseCommit:    c.BaseCommit,
			Target:        *target,
			Result:        result,
		}
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		rows.Write(data)
		rows.WriteByte('\n')
	}
	if err := artifact.WriteFileAtomic(outPath, rows.Bytes(), 0o644); err != nil {
		return err
	}
	return writeJSON(filepath.Join(*output, "summary", *target+".json"), summary)
}

func readPredictions(path string) (map[string]contract.Prediction, error) {
	return artifact.ReadPredictions(path)
}

func readCases(path string, preds map[string]contract.Prediction) ([]contract.Case, error) {
	if strings.TrimSpace(path) == "" {
		ids := make([]string, 0, len(preds))
		for id := range preds {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		cases := make([]contract.Case, 0, len(ids))
		for _, id := range ids {
			if err := validateArtifactName("instance id", id); err != nil {
				return nil, err
			}
			cases = append(cases, contract.Case{InstanceID: id})
		}
		return cases, nil
	}
	cases, err := artifact.ReadCasesJSONL(path)
	if err != nil {
		return nil, err
	}
	for _, c := range cases {
		if err := validateArtifactName("instance id", c.InstanceID); err != nil {
			return nil, fmt.Errorf("validate %s: %w", path, err)
		}
	}
	return cases, nil
}

func validateImportInputs(cases []contract.Case, preds map[string]contract.Prediction) error {
	seenCases := map[string]bool{}
	for _, c := range cases {
		if strings.TrimSpace(c.InstanceID) == "" {
			return fmt.Errorf("case with empty instance_id")
		}
		if seenCases[c.InstanceID] {
			return fmt.Errorf("duplicate case instance_id %q", c.InstanceID)
		}
		seenCases[c.InstanceID] = true
	}
	for id := range preds {
		if !seenCases[id] {
			return fmt.Errorf("prediction %q is not present in case manifest", id)
		}
	}
	return nil
}

func traceRawDirsByCase(path string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return out, nil
	}
	var manifest shardsManifest
	if err := readJSONFile(path, &manifest); err != nil {
		return nil, fmt.Errorf("read shards manifest: %w", err)
	}
	for _, shard := range manifest.Shards {
		if strings.TrimSpace(shard.RawDir) == "" {
			continue
		}
		for _, c := range shard.Cases {
			if c.Status == "accepted" && c.InstanceID != "" {
				out[c.InstanceID] = shard.RawDir
			}
		}
	}
	return out, nil
}

func readHarnessReport(path string) (contract.HarnessIndex, error) {
	idx := contract.NewHarnessIndex()
	if strings.TrimSpace(path) == "" {
		return idx, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return idx, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return idx, err
	}
	addIDs := func(key string, dst map[string]bool) error {
		value, ok := raw[key]
		if !ok {
			return nil
		}
		var ids []string
		if err := json.Unmarshal(value, &ids); err != nil {
			return fmt.Errorf("field %s: %w", key, err)
		}
		for _, id := range ids {
			if err := validateArtifactName("harness instance id", id); err != nil {
				return fmt.Errorf("field %s: %w", key, err)
			}
			if dst[id] {
				return fmt.Errorf("field %s contains duplicate instance id %q", key, id)
			}
			dst[id] = true
		}
		return nil
	}
	for key, dst := range map[string]map[string]bool{
		"resolved_ids":   idx.Resolved,
		"unresolved_ids": idx.Unresolved,
		"error_ids":      idx.Errors,
		"completed_ids":  idx.Completed,
	} {
		if err := addIDs(key, dst); err != nil {
			return idx, err
		}
	}
	for id, v := range raw {
		var row map[string]any
		if err := json.Unmarshal(v, &row); err == nil {
			resolved, hasResolved := row["resolved"].(bool)
			if resolved {
				idx.Resolved[id] = true
			} else if hasResolved {
				idx.Unresolved[id] = true
			}
		}
	}
	if len(idx.Resolved)+len(idx.Unresolved)+len(idx.Errors) == 0 {
		return idx, fmt.Errorf("harness report %s contains no terminal outcomes", path)
	}
	return idx, nil
}

func validateHarnessIndex(index contract.HarnessIndex, cases []contract.Case) error {
	known := make(map[string]struct{}, len(cases))
	for _, c := range cases {
		known[c.InstanceID] = struct{}{}
	}
	for id := range index.Resolved {
		if index.Unresolved[id] || index.Errors[id] {
			return fmt.Errorf("instance %q appears in multiple terminal outcome sets", id)
		}
		if _, ok := known[id]; !ok {
			return fmt.Errorf("resolved instance %q is not present in case manifest", id)
		}
	}
	for id := range index.Unresolved {
		if index.Errors[id] {
			return fmt.Errorf("instance %q appears in multiple terminal outcome sets", id)
		}
		if _, ok := known[id]; !ok {
			return fmt.Errorf("unresolved instance %q is not present in case manifest", id)
		}
	}
	for id := range index.Errors {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("error instance %q is not present in case manifest", id)
		}
	}
	for id := range index.Completed {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("completed instance %q is not present in case manifest", id)
		}
		if !index.Resolved[id] && !index.Unresolved[id] {
			return fmt.Errorf("completed instance %q has no resolved or unresolved outcome", id)
		}
	}
	return nil
}

func classify(instanceID string, hasPred bool, patch string, harness contract.HarnessIndex) (string, string) {
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

func copyScrubbedTrace(rawDir, traceDir, instanceID string) (string, usageStats, error) {
	src := filepath.Join(rawDir, instanceID, instanceID+".traj.json")
	data, err := os.ReadFile(src)
	if err != nil {
		return "", usageStats{}, err
	}
	usage := extractUsage(data)
	dst := filepath.Join(traceDir, instanceID+".json")
	if err := artifact.WriteFileAtomic(dst, redactJSONBytes(data), 0o644); err != nil {
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
