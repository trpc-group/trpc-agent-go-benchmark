//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package verifier wraps official SWE-Bench verification artifacts.
package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/prediction"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
)

// RunRequest configures a verifier run.
type RunRequest struct {
	RunID           string
	Model           string
	PredictionsPath string
	OutputDir       string
	Verifier        string
	SBCLIBin        string
	SBCLIArgs       string
	LocalCommand    string
	LocalReportPath string
	DockerHost      string
	DryRun          bool
}

// ImportRequest imports predictions into benchmark artifacts without verifying.
type ImportRequest struct {
	RunID           string
	Model           string
	PredictionsPath string
	OutputDir       string
}

// Run verifies predictions with the selected verifier or writes dry-run
// verifier artifacts.
func Run(ctx context.Context, req RunRequest) error {
	if req.PredictionsPath == "" {
		return fmt.Errorf("-predictions is required")
	}
	if req.Verifier == "" {
		req.Verifier = "local-harness"
	}
	switch req.Verifier {
	case "local-harness":
		return runLocalHarness(ctx, req)
	case "sb-cli":
		return runSBCLI(ctx, req)
	default:
		return fmt.Errorf("unsupported verifier %q", req.Verifier)
	}
}

func runSBCLI(ctx context.Context, req RunRequest) error {
	runDir, err := result.EnsureDir(result.RunDir(req.OutputDir, req.RunID))
	if err != nil {
		return err
	}
	cfg := result.RunConfig{
		RunID:       req.RunID,
		Model:       req.Model,
		StartedAt:   time.Now().UTC(),
		DryRun:      req.DryRun,
		Verifier:    "sb-cli",
		Source:      req.PredictionsPath,
		CommandLine: strings.TrimSpace(req.SBCLIBin + " " + req.SBCLIArgs),
	}
	if err := result.WriteJSON(filepath.Join(runDir, "run_config.json"), cfg); err != nil {
		return err
	}
	if req.DryRun {
		return writePendingCases(runDir, req.RunID, req.PredictionsPath)
	}
	args := strings.Fields(req.SBCLIArgs)
	args = append(args, req.PredictionsPath)
	cmd := exec.CommandContext(ctx, req.SBCLIBin, args...)
	cmd.Dir = runDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runLocalHarness(ctx context.Context, req RunRequest) error {
	runDir, err := result.EnsureDir(result.RunDir(req.OutputDir, req.RunID))
	if err != nil {
		return err
	}
	reportPath := req.LocalReportPath
	if reportPath == "" {
		reportPath = filepath.Join(runDir, "report.json")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return err
	}
	absPredictionsPath, err := filepath.Abs(req.PredictionsPath)
	if err != nil {
		return err
	}
	absReportPath, err := filepath.Abs(reportPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absReportPath), 0755); err != nil {
		return err
	}
	commandLine := expandLocalCommand(req.LocalCommand, absPredictionsPath, req.RunID, absRunDir, absReportPath)
	cfg := result.RunConfig{
		RunID:       req.RunID,
		Model:       req.Model,
		StartedAt:   time.Now().UTC(),
		DryRun:      req.DryRun,
		Verifier:    "local-harness",
		Source:      req.PredictionsPath,
		CommandLine: commandLine,
	}
	if err := result.WriteJSON(filepath.Join(runDir, "run_config.json"), cfg); err != nil {
		return err
	}
	if req.DryRun {
		return writePendingCases(runDir, req.RunID, req.PredictionsPath)
	}
	if commandLine == "" {
		return fmt.Errorf("-local-command is required for -verifier local-harness")
	}
	if err := runLocalCommand(ctx, runDir, commandLine, req.DockerHost); err != nil {
		return err
	}
	return importVerifierReport(runDir, req.RunID, req.PredictionsPath, absReportPath)
}

func runLocalCommand(ctx context.Context, runDir, commandLine, dockerHost string) error {
	logDir := filepath.Join(runDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "local-harness.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.CommandContext(ctx, "sh", "-lc", commandLine)
	cmd.Dir = runDir
	cmd.Env = os.Environ()
	if dockerHost != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+dockerHost)
	}
	writer := io.MultiWriter(os.Stdout, logFile)
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run local harness command: %w (log: %s)", err, logPath)
	}
	return nil
}

// Import creates normalized artifacts from an existing prediction file.
func Import(req ImportRequest) error {
	if req.PredictionsPath == "" {
		return fmt.Errorf("-predictions is required")
	}
	runDir, err := result.EnsureDir(result.RunDir(req.OutputDir, req.RunID))
	if err != nil {
		return err
	}
	cfg := result.RunConfig{
		RunID:     req.RunID,
		Model:     req.Model,
		StartedAt: time.Now().UTC(),
		Source:    req.PredictionsPath,
	}
	if err := result.WriteJSON(filepath.Join(runDir, "run_config.json"), cfg); err != nil {
		return err
	}
	return writePendingCases(runDir, req.RunID, req.PredictionsPath)
}

func writePendingCases(runDir, runID, predictionsPath string) error {
	preds, err := prediction.Read(predictionsPath)
	if err != nil {
		return err
	}
	cases := make([]result.CaseResult, 0, len(preds))
	for _, pred := range preds {
		status := "pending_verifier"
		if strings.TrimSpace(pred.ModelPatch) == "" {
			status = "incomplete"
		}
		cases = append(cases, result.CaseResult{
			InstanceID: pred.InstanceID,
			Status:     status,
			Resolved:   false,
			ReportRef:  predictionsPath,
		})
	}
	if err := result.WriteCasesJSONL(filepath.Join(runDir, "cases.jsonl"), cases); err != nil {
		return err
	}
	summary := result.Summarize(runID, "verifier", cases)
	if err := result.WriteJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
		return err
	}
	return result.WriteJSON(filepath.Join(runDir, "report.json"), map[string]any{
		"status":      "pending_official_verification",
		"predictions": predictionsPath,
		"cases":       len(cases),
	})
}

func importVerifierReport(runDir, runID, predictionsPath, reportPath string) error {
	preds, err := prediction.Read(predictionsPath)
	if err != nil {
		return err
	}
	report, err := readReportMap(reportPath)
	if err != nil {
		return err
	}
	canonicalReportPath := filepath.Join(runDir, "report.json")
	if filepath.Clean(reportPath) != filepath.Clean(canonicalReportPath) {
		data, err := os.ReadFile(reportPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(canonicalReportPath, data, 0644); err != nil {
			return err
		}
	}
	statuses := statusesFromReport(report)
	logPath := filepath.Join(runDir, "logs", "local-harness.log")
	cases := make([]result.CaseResult, 0, len(preds))
	for _, pred := range preds {
		status := statuses[pred.InstanceID]
		if status == "" {
			status = "incomplete"
			if strings.TrimSpace(pred.ModelPatch) == "" {
				status = "empty_patch"
			}
		}
		cases = append(cases, result.CaseResult{
			InstanceID: pred.InstanceID,
			Status:     status,
			Resolved:   status == "resolved",
			LogPath:    logPath,
			ReportRef:  canonicalReportPath,
		})
	}
	sort.Slice(cases, func(i, j int) bool {
		return cases[i].InstanceID < cases[j].InstanceID
	})
	if err := result.WriteCasesJSONL(filepath.Join(runDir, "cases.jsonl"), cases); err != nil {
		return err
	}
	summary := result.Summarize(runID, "verifier", cases)
	return result.WriteJSON(filepath.Join(runDir, "summary.json"), summary)
}

func readReportMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read local harness report %s: %w", path, err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("decode local harness report %s: %w", path, err)
	}
	return report, nil
}

func statusesFromReport(report map[string]any) map[string]string {
	statuses := map[string]string{}
	for _, id := range idsFromAny(firstPresent(report, "resolved_ids", "resolved")) {
		statuses[id] = "resolved"
	}
	for _, id := range idsFromAny(firstPresent(report, "unresolved_ids", "unresolved")) {
		setIfUnset(statuses, id, "unresolved")
	}
	for _, id := range idsFromAny(firstPresent(report, "empty_patch_ids", "empty_patch")) {
		setIfUnset(statuses, id, "empty_patch")
	}
	for _, id := range idsFromAny(firstPresent(report, "error_ids", "errored_ids", "errors")) {
		statuses[id] = "error"
	}
	for _, id := range idsFromAny(firstPresent(report, "incomplete_ids", "pending_ids", "failed_ids")) {
		setIfUnset(statuses, id, "incomplete")
	}
	mergePerInstanceStatuses(statuses, report["results"])
	mergePerInstanceStatuses(statuses, report["instance_results"])
	mergePerInstanceStatuses(statuses, report["instances"])
	return statuses
}

func firstPresent(report map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := report[key]; ok {
			return v
		}
	}
	return nil
}

func idsFromAny(v any) []string {
	switch x := v.(type) {
	case []any:
		ids := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				ids = append(ids, strings.TrimSpace(s))
			}
		}
		return ids
	case []string:
		return x
	case map[string]any:
		ids := make([]string, 0, len(x))
		for key := range x {
			if strings.TrimSpace(key) != "" {
				ids = append(ids, strings.TrimSpace(key))
			}
		}
		return ids
	default:
		return nil
	}
}

func mergePerInstanceStatuses(statuses map[string]string, v any) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := stringField(m, "instance_id", "instanceId")
			status := normalizeStatus(stringField(m, "status", "result"))
			if id == "" && len(m) == 1 {
				for key, raw := range m {
					id = key
					status = normalizeStatus(fmt.Sprint(raw))
				}
			}
			if id != "" && status != "" {
				statuses[id] = status
			}
		}
	case map[string]any:
		for id, raw := range x {
			status := ""
			if m, ok := raw.(map[string]any); ok {
				status = normalizeStatus(stringField(m, "status", "result"))
				if status == "" {
					if resolved, ok := m["resolved"].(bool); ok {
						if resolved {
							status = "resolved"
						} else {
							status = "unresolved"
						}
					}
				}
			} else {
				status = normalizeStatus(fmt.Sprint(raw))
			}
			if strings.TrimSpace(id) != "" && status != "" {
				statuses[strings.TrimSpace(id)] = status
			}
		}
	}
}

func stringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "pass", "passed", "success", "true":
		return "resolved"
	case "unresolved", "fail", "failed", "test_failed", "false":
		return "unresolved"
	case "empty_patch", "empty-patch", "empty patch":
		return "empty_patch"
	case "error", "errored":
		return "error"
	case "incomplete", "pending":
		return "incomplete"
	default:
		return ""
	}
}

func setIfUnset(statuses map[string]string, id, status string) {
	if _, ok := statuses[id]; !ok {
		statuses[id] = status
	}
}

func expandLocalCommand(commandLine, predictionsPath, runID, outputDir, reportPath string) string {
	replacer := strings.NewReplacer(
		"{predictions}", shellQuote(predictionsPath),
		"{run_id}", shellQuote(runID),
		"{output_dir}", shellQuote(outputDir),
		"{report}", shellQuote(reportPath),
	)
	return strings.TrimSpace(replacer.Replace(commandLine))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
