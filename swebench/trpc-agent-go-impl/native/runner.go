//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package native runs the Go-native SWE agent implementation.
package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/prediction"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
)

// RunRequest configures a native benchmark run.
type RunRequest struct {
	RunID            string
	Model            string
	Instances        []dataset.Instance
	OutputDir        string
	Environment      string
	AgentRuntime     string
	DockerHost       string
	WorkspaceRoot    string
	RepoCacheRoot    string
	KeepWorkspace    bool
	MaxOutputChars   int
	StepLimit        int
	TokenLimit       int
	Timeout          time.Duration
	DryRun           bool
	GLM              GLMConfig
	PartialTracePath string
}

// Run writes native runner artifacts. In dry-run mode it writes deterministic
// placeholders; otherwise it executes a minimal GLM-5-driven SWE loop.
func Run(ctx context.Context, req RunRequest) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	runDir, err := result.EnsureDir(result.RunDir(req.OutputDir, req.RunID))
	if err != nil {
		return err
	}
	if _, err := result.EnsureDir(filepath.Join(runDir, "patches")); err != nil {
		return err
	}
	if _, err := result.EnsureDir(filepath.Join(runDir, "traces")); err != nil {
		return err
	}

	cfg := result.RunConfig{
		RunID:       req.RunID,
		Runner:      string(result.RunnerNative),
		Model:       req.Model,
		InstanceCnt: len(req.Instances),
		StartedAt:   time.Now().UTC(),
		StepLimit:   req.StepLimit,
		TokenLimit:  req.TokenLimit,
		Timeout:     req.Timeout.String(),
		DryRun:      req.DryRun,
		CommandLine: "native-env=" + nativeEnvironment(req.Environment) + " native-runtime=" + nativeRuntime(req.AgentRuntime),
	}
	if err := result.WriteJSON(filepath.Join(runDir, "run_config.json"), cfg); err != nil {
		return err
	}
	if req.DryRun {
		return writeDryRun(runDir, req)
	}
	return runReal(ctx, runDir, req)
}

func writeDryRun(runDir string, req RunRequest) error {
	preds := make([]prediction.Prediction, 0, len(req.Instances))
	cases := make([]result.CaseResult, 0, len(req.Instances))
	for _, inst := range req.Instances {
		patchPath := filepath.Join(runDir, "patches", sanitize(inst.InstanceID)+".patch")
		tracePath := filepath.Join(runDir, "traces", sanitize(inst.InstanceID)+".json")
		if err := os.WriteFile(patchPath, nil, 0644); err != nil {
			return err
		}
		trace := map[string]any{
			"instance_id": inst.InstanceID,
			"runner":      result.RunnerNative,
			"status":      "dry_run_placeholder",
			"message":     "native SWE loop not executed",
		}
		if err := result.WriteJSON(tracePath, trace); err != nil {
			return err
		}
		preds = append(preds, prediction.Prediction{
			InstanceID:      inst.InstanceID,
			ModelNameOrPath: req.Model,
			ModelPatch:      "",
		})
		cases = append(cases, result.CaseResult{
			InstanceID: inst.InstanceID,
			Runner:     string(result.RunnerNative),
			Status:     "incomplete",
			PatchPath:  patchPath,
			TracePath:  tracePath,
		})
	}
	if err := prediction.WriteJSONL(filepath.Join(runDir, "predictions.jsonl"), preds); err != nil {
		return err
	}
	if err := prediction.WriteJSON(filepath.Join(runDir, "preds.json"), preds); err != nil {
		return err
	}
	if err := result.WriteCasesJSONL(filepath.Join(runDir, "cases.jsonl"), cases); err != nil {
		return err
	}
	summary := result.Summarize(req.RunID, string(result.RunnerNative), cases)
	return result.WriteJSON(filepath.Join(runDir, "summary.json"), summary)
}

func runReal(ctx context.Context, runDir string, req RunRequest) error {
	client, err := newChatClient(req.GLM, req.Timeout)
	if err != nil {
		return err
	}
	workspaceRoot := filepath.Join(req.WorkspaceRoot, req.RunID)
	if err := os.MkdirAll(workspaceRoot, 0755); err != nil {
		return err
	}
	preds := make([]prediction.Prediction, 0, len(req.Instances))
	cases := make([]result.CaseResult, 0, len(req.Instances))
	for _, inst := range req.Instances {
		caseCtx, cancel := context.WithTimeout(ctx, req.Timeout)
		patchPath := filepath.Join(runDir, "patches", sanitize(inst.InstanceID)+".patch")
		tracePath := filepath.Join(runDir, "traces", sanitize(inst.InstanceID)+".json")
		caseResult := result.CaseResult{
			InstanceID: inst.InstanceID,
			Runner:     string(result.RunnerNative),
			Status:     "error",
			PatchPath:  patchPath,
			TracePath:  tracePath,
		}
		var patch string
		ws, err := prepareWorkspace(caseCtx, workspaceRoot, inst, req)
		if err != nil {
			caseResult.Error = err.Error()
			_ = result.WriteJSON(tracePath, instanceTrace{
				InstanceID: inst.InstanceID,
				Repo:       inst.Repo,
				BaseCommit: inst.BaseCommit,
				Model:      req.Model,
				Status:     "error",
				Error:      err.Error(),
			})
			cancel()
			preds = append(preds, prediction.Prediction{
				InstanceID:      inst.InstanceID,
				ModelNameOrPath: req.Model,
				ModelPatch:      "",
			})
			cases = append(cases, caseResult)
			continue
		}
		caseReq := req
		caseReq.PartialTracePath = filepath.Join(runDir, "traces", sanitize(inst.InstanceID)+".partial.json")
		loop := runInstanceWithRuntime(caseCtx, client, inst, ws, caseReq)
		cancel()
		patch = loop.Patch
		if err := os.WriteFile(patchPath, []byte(patch), 0644); err != nil {
			return err
		}
		if err := result.WriteJSON(tracePath, loop.Trace); err != nil {
			return err
		}
		if !req.KeepWorkspace {
			_ = ws.cleanup(context.Background())
		}
		caseResult.Status = loop.Status
		caseResult.Resolved = false
		caseResult.ChangedFiles = loop.ChangedFiles
		caseResult.PatchAdded = loop.PatchAdded
		caseResult.PatchDeleted = loop.PatchDeleted
		caseResult.DurationMs = loop.DurationMs
		caseResult.Usage = loop.Usage
		if loop.Err != nil {
			caseResult.Error = loop.Err.Error()
		}
		preds = append(preds, prediction.Prediction{
			InstanceID:      inst.InstanceID,
			ModelNameOrPath: req.Model,
			ModelPatch:      patch,
		})
		cases = append(cases, caseResult)
	}
	if err := prediction.WriteJSONL(filepath.Join(runDir, "predictions.jsonl"), preds); err != nil {
		return err
	}
	if err := prediction.WriteJSON(filepath.Join(runDir, "preds.json"), preds); err != nil {
		return err
	}
	if err := result.WriteCasesJSONL(filepath.Join(runDir, "cases.jsonl"), cases); err != nil {
		return err
	}
	summary := result.Summarize(req.RunID, string(result.RunnerNative), cases)
	return result.WriteJSON(filepath.Join(runDir, "summary.json"), summary)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '/' || r == '\\' || r == ':' || r == ' ' || r == '\t' || strings.ContainsRune("*?[]", r) {
			out = append(out, '_')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
