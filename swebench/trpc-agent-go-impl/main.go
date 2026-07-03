//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package main provides the SWE-Bench-Verified benchmark CLI for
// mini-SWE-agent baseline runs, native trpc-agent-go runs, official
// verification, and report generation.
package main

import (
	"context"
	"log"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/miniswe"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/native"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/report"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/verifier"
)

func main() {
	cfg := parseFlags()
	if err := cfg.validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	ctx := context.Background()
	switch cfg.Mode {
	case "run-mini":
		instances := mustLoadInstances(cfg)
		req := miniswe.RunRequest{
			RunID:       cfg.RunID,
			Model:       cfg.Model,
			Instances:   instances,
			OutputDir:   cfg.OutputDir,
			StepLimit:   cfg.StepLimit,
			TokenLimit:  cfg.TokenLimit,
			Timeout:     cfg.Timeout,
			DryRun:      cfg.DryRun,
			CommandLine: cfg.Command,
		}
		must(miniswe.Run(ctx, req), "run mini-SWE-agent baseline")
	case "run-native":
		instances := mustLoadInstances(cfg)
		req := native.RunRequest{
			RunID:          cfg.RunID,
			Model:          cfg.Model,
			Instances:      instances,
			OutputDir:      cfg.OutputDir,
			Environment:    cfg.NativeEnv,
			AgentRuntime:   cfg.NativeRuntime,
			DockerHost:     cfg.DockerHost,
			WorkspaceRoot:  cfg.WorkspaceRoot,
			RepoCacheRoot:  cfg.RepoCacheRoot,
			KeepWorkspace:  cfg.KeepWorkspace,
			MaxOutputChars: cfg.MaxOutputChars,
			StepLimit:      cfg.StepLimit,
			TokenLimit:     cfg.TokenLimit,
			Timeout:        cfg.Timeout,
			DryRun:         cfg.DryRun,
			GLM: native.GLMConfig{
				APIBase:         cfg.GLMAPIBase,
				APIKey:          cfg.GLMAPIKey,
				RoutingKey:      cfg.GLMRoutingKey,
				AgentName:       cfg.GLMAgentName,
				ModelName:       cfg.GLMModelName,
				ReasoningEffort: cfg.GLMReasoningEffort,
			},
		}
		must(native.Run(ctx, req), "run native SWE agent")
	case "verify":
		req := verifier.RunRequest{
			RunID:           cfg.RunID,
			Model:           cfg.Model,
			PredictionsPath: cfg.Predictions,
			OutputDir:       cfg.OutputDir,
			Verifier:        cfg.Verifier,
			SBCLIBin:        cfg.SBCLIBin,
			SBCLIArgs:       cfg.SBCLIArgs,
			LocalCommand:    cfg.LocalCommand,
			LocalReportPath: cfg.LocalReport,
			DockerHost:      cfg.DockerHost,
			DryRun:          cfg.DryRun,
		}
		must(verifier.Run(ctx, req), "verify predictions")
	case "report":
		req := report.RunRequest{
			RunID:          cfg.RunID,
			MiniRunDir:     cfg.MiniDir,
			NativeRunDir:   cfg.NativeDir,
			MiniVerifier:   cfg.MiniVerifier,
			NativeVerifier: cfg.NativeVerifier,
			OutputDir:      cfg.OutputDir,
		}
		must(report.Run(req), "generate report")
	case "import":
		req := verifier.ImportRequest{
			RunID:           cfg.RunID,
			Model:           cfg.Model,
			PredictionsPath: cfg.Predictions,
			OutputDir:       cfg.OutputDir,
		}
		must(verifier.Import(req), "import predictions")
	case "doctor":
		must(runDoctor(cfg), "doctor checks")
	}
}

func mustLoadInstances(cfg cliConfig) []dataset.Instance {
	instances, err := dataset.LoadJSONL(cfg.DatasetPath)
	must(err, "load dataset")
	selected, err := dataset.Select(instances, cfg.Instances, cfg.MaxInstances)
	must(err, "select instances")
	return selected
}

func must(err error, msg string) {
	if err == nil {
		return
	}
	log.Fatalf("%s: %v", msg, err)
}
