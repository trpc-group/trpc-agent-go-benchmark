//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package miniswe wraps mini-SWE-agent baseline execution and artifacts.
package miniswe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/prediction"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
)

// RunRequest configures a mini-SWE-agent baseline run.
type RunRequest struct {
	RunID       string
	Model       string
	Instances   []dataset.Instance
	OutputDir   string
	StepLimit   int
	TokenLimit  int
	Timeout     time.Duration
	DryRun      bool
	CommandLine string
}

// Run executes mini-SWE-agent or writes dry-run placeholder artifacts.
func Run(ctx context.Context, req RunRequest) error {
	runDir, err := result.EnsureDir(result.RunDir(req.OutputDir, req.RunID))
	if err != nil {
		return err
	}
	cfg := result.RunConfig{
		RunID:       req.RunID,
		Runner:      string(result.RunnerMini),
		Model:       req.Model,
		InstanceCnt: len(req.Instances),
		StartedAt:   time.Now().UTC(),
		StepLimit:   req.StepLimit,
		TokenLimit:  req.TokenLimit,
		Timeout:     req.Timeout.String(),
		DryRun:      req.DryRun,
		CommandLine: req.CommandLine,
	}
	if err := result.WriteJSON(filepath.Join(runDir, "run_config.json"), cfg); err != nil {
		return err
	}

	if req.CommandLine != "" && !req.DryRun {
		return runCommand(ctx, runDir, req.CommandLine)
	}
	if !req.DryRun {
		return fmt.Errorf("mini-SWE-agent command is required unless -dry-run is set")
	}
	return writeDryRun(runDir, req)
}

func runCommand(ctx context.Context, runDir, commandLine string) error {
	args := strings.Fields(commandLine)
	if len(args) == 0 {
		return fmt.Errorf("empty mini-SWE-agent command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = runDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeDryRun(runDir string, req RunRequest) error {
	if _, err := result.EnsureDir(filepath.Join(runDir, "patches")); err != nil {
		return err
	}
	if _, err := result.EnsureDir(filepath.Join(runDir, "trajectories")); err != nil {
		return err
	}
	preds := make([]prediction.Prediction, 0, len(req.Instances))
	cases := make([]result.CaseResult, 0, len(req.Instances))
	for _, inst := range req.Instances {
		patchPath := filepath.Join(runDir, "patches", sanitize(inst.InstanceID)+".patch")
		tracePath := filepath.Join(runDir, "trajectories", sanitize(inst.InstanceID)+".traj.json")
		if err := os.WriteFile(patchPath, nil, 0644); err != nil {
			return err
		}
		trace := map[string]any{
			"instance_id": inst.InstanceID,
			"runner":      result.RunnerMini,
			"status":      "dry_run_placeholder",
			"message":     "mini-SWE-agent not executed",
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
			Runner:     string(result.RunnerMini),
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
	summary := result.Summarize(req.RunID, string(result.RunnerMini), cases)
	return result.WriteJSON(filepath.Join(runDir, "summary.json"), summary)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			out = append(out, '_')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
