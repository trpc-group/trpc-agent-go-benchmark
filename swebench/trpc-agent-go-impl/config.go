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
	"flag"
	"fmt"
	"strings"
	"time"
)

type cliConfig struct {
	Mode               string
	RunID              string
	DatasetPath        string
	Instances          string
	MaxInstances       int
	Model              string
	OutputDir          string
	Predictions        string
	MiniDir            string
	NativeDir          string
	MiniVerifier       string
	NativeVerifier     string
	NativeEnv          string
	NativeRuntime      string
	WorkspaceRoot      string
	RepoCacheRoot      string
	KeepWorkspace      bool
	MaxOutputChars     int
	StepLimit          int
	TokenLimit         int
	Timeout            time.Duration
	DryRun             bool
	GLMAPIBase         string
	GLMAPIKey          string
	GLMRoutingKey      string
	GLMAgentName       string
	GLMModelName       string
	GLMReasoningEffort string
	Command            string
	Verifier           string
	SBCLIBin           string
	SBCLIArgs          string
	LocalCommand       string
	LocalReport        string
	DockerHost         string
}

func parseFlags() cliConfig {
	var timeoutMinutes int
	cfg := cliConfig{}

	flag.StringVar(&cfg.Mode, "mode", "", "mode: run-mini, run-native, verify, report, import, doctor")
	flag.StringVar(&cfg.RunID, "run-id", "", "stable run identifier")
	flag.StringVar(&cfg.DatasetPath, "dataset", "../data/swebench_verified_cases.jsonl", "SWE-Bench-Verified JSONL dataset path")
	flag.StringVar(&cfg.Instances, "instances", "all", "instance selector: all, comma-separated IDs, or a file containing IDs")
	flag.IntVar(&cfg.MaxInstances, "max-instances", 0, "maximum instances to run after selection (0=all)")
	flag.StringVar(&cfg.Model, "model", "glm5", "model alias or model name")
	flag.StringVar(&cfg.OutputDir, "output", "../results/runs", "output directory")
	flag.StringVar(&cfg.Predictions, "predictions", "", "predictions file for verify/import modes")
	flag.StringVar(&cfg.MiniDir, "mini", "", "mini-SWE-agent run directory for report mode")
	flag.StringVar(&cfg.NativeDir, "native", "", "native run directory for report mode")
	flag.StringVar(&cfg.MiniVerifier, "mini-verifier", "", "mini verifier directory for report mode")
	flag.StringVar(&cfg.NativeVerifier, "native-verifier", "", "native verifier directory for report mode")
	flag.StringVar(&cfg.NativeEnv, "native-env", "docker-testbed", "native SWE execution environment: docker-testbed or local-clone")
	flag.StringVar(&cfg.NativeRuntime, "native-runtime", "trpc", "native SWE agent runtime: trpc or legacy")
	flag.StringVar(&cfg.WorkspaceRoot, "workspace-root", "../results/runs/workspaces", "workspace root for native runs")
	flag.StringVar(&cfg.RepoCacheRoot, "repo-cache", "../results/runs/repo-cache", "repository cache root for native runs")
	flag.BoolVar(&cfg.KeepWorkspace, "keep-workspace", false, "keep native workspaces after each run")
	flag.IntVar(&cfg.MaxOutputChars, "max-output-chars", 12000, "maximum command output characters retained per step")
	flag.IntVar(&cfg.StepLimit, "step-limit", 100, "per-instance step limit")
	flag.IntVar(&cfg.TokenLimit, "token-limit", 0, "per-instance token hard limit (0=disabled, mini-compatible)")
	flag.IntVar(&timeoutMinutes, "timeout-minutes", 60, "per-instance timeout in minutes")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "write deterministic placeholder artifacts without calling external services")
	flag.StringVar(&cfg.GLMAPIBase, "glm-api-base", "", "GLM-5 OpenAI-compatible API base or chat completions URL (env GLM5_API_BASE)")
	flag.StringVar(&cfg.GLMAPIKey, "glm-api-key", "", "GLM-5 API key (env GLM5_API_KEY)")
	flag.StringVar(&cfg.GLMRoutingKey, "glm-routing-key", "", "GLM-5 routing key header (env GLM5_ROUTING_KEY)")
	flag.StringVar(&cfg.GLMAgentName, "glm-agent-name", "", "GLM-5 agent name header (env GLM5_AGENT_NAME, default trpc-agent-go-benchmark)")
	flag.StringVar(&cfg.GLMModelName, "glm-model-name", "", "GLM-5 actual model id (env GLM5_MODEL, default glm50)")
	flag.StringVar(&cfg.GLMReasoningEffort, "glm-reasoning-effort", "", "GLM-5 reasoning effort (env GLM5_REASONING_EFFORT, default high)")
	flag.StringVar(&cfg.Command, "command", "", "external command template for run-mini/import integration")
	flag.StringVar(&cfg.Verifier, "verifier", "local-harness", "verifier backend: local-harness or sb-cli")
	flag.StringVar(&cfg.SBCLIBin, "sb-cli-bin", "sb-cli", "sb-cli executable path")
	flag.StringVar(&cfg.SBCLIArgs, "sb-cli-args", "", "extra sb-cli args, split on whitespace")
	flag.StringVar(&cfg.LocalCommand, "local-command", "", "local harness command template for verify mode")
	flag.StringVar(&cfg.LocalReport, "local-report", "", "local harness report path after command completes")
	flag.StringVar(&cfg.DockerHost, "docker-host", "", "Docker host for local harness verifier (env DOCKER_HOST)")
	flag.Parse()

	cfg.Mode = strings.TrimSpace(cfg.Mode)
	cfg.RunID = strings.TrimSpace(cfg.RunID)
	cfg.DatasetPath = strings.TrimSpace(cfg.DatasetPath)
	cfg.Instances = strings.TrimSpace(cfg.Instances)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.OutputDir = strings.TrimSpace(cfg.OutputDir)
	cfg.NativeEnv = strings.TrimSpace(cfg.NativeEnv)
	cfg.NativeRuntime = strings.TrimSpace(cfg.NativeRuntime)
	cfg.WorkspaceRoot = strings.TrimSpace(cfg.WorkspaceRoot)
	cfg.RepoCacheRoot = strings.TrimSpace(cfg.RepoCacheRoot)
	cfg.GLMAPIBase = strings.TrimSpace(cfg.GLMAPIBase)
	cfg.GLMAPIKey = strings.TrimSpace(cfg.GLMAPIKey)
	cfg.GLMRoutingKey = strings.TrimSpace(cfg.GLMRoutingKey)
	cfg.GLMAgentName = strings.TrimSpace(cfg.GLMAgentName)
	cfg.GLMModelName = strings.TrimSpace(cfg.GLMModelName)
	cfg.GLMReasoningEffort = strings.TrimSpace(cfg.GLMReasoningEffort)
	cfg.Verifier = strings.TrimSpace(cfg.Verifier)
	cfg.LocalCommand = strings.TrimSpace(cfg.LocalCommand)
	cfg.LocalReport = strings.TrimSpace(cfg.LocalReport)
	cfg.DockerHost = strings.TrimSpace(cfg.DockerHost)
	cfg.Timeout = time.Duration(timeoutMinutes) * time.Minute
	return cfg
}

func (c cliConfig) validate() error {
	if c.Mode == "" {
		return fmt.Errorf("-mode is required")
	}
	switch c.Mode {
	case "run-mini", "run-native", "verify", "report", "import", "doctor":
	default:
		return fmt.Errorf("unsupported -mode %q", c.Mode)
	}
	if c.RunID == "" && c.Mode != "report" && c.Mode != "doctor" {
		return fmt.Errorf("-run-id is required for mode %s", c.Mode)
	}
	if c.Model == "" {
		return fmt.Errorf("-model is required")
	}
	if c.StepLimit < 1 {
		return fmt.Errorf("-step-limit must be >= 1")
	}
	if c.TokenLimit < 0 {
		return fmt.Errorf("-token-limit must be >= 0")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("-timeout-minutes must be > 0")
	}
	if c.MaxOutputChars < 1024 {
		return fmt.Errorf("-max-output-chars must be >= 1024")
	}
	if c.Mode == "verify" {
		switch c.Verifier {
		case "local-harness", "sb-cli":
		default:
			return fmt.Errorf("unsupported -verifier %q", c.Verifier)
		}
	}
	if c.Mode == "run-native" {
		switch c.NativeEnv {
		case "docker-testbed", "local-clone":
		default:
			return fmt.Errorf("unsupported -native-env %q", c.NativeEnv)
		}
		switch c.NativeRuntime {
		case "trpc", "legacy":
		default:
			return fmt.Errorf("unsupported -native-runtime %q", c.NativeRuntime)
		}
	}
	return nil
}
