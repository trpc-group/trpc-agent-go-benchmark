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
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type runMiniManifest struct {
	RunID      string        `json:"run_id"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	DurationMS int64         `json:"duration_ms"`
	Command    commandResult `json:"command"`
	Config     runMiniConfig `json:"config"`
}

type runMiniConfig struct {
	Subset       string `json:"subset"`
	Split        string `json:"split"`
	Filter       string `json:"filter,omitempty"`
	Slice        string `json:"slice,omitempty"`
	Workers      int    `json:"workers"`
	OutputDir    string `json:"output_dir"`
	MiniConfig   string `json:"mini_config"`
	BaseConfig   string `json:"base_config"`
	MiniExtra    string `json:"mini_extra"`
	DockerHost   string `json:"docker_host"`
	HFHome       string `json:"hf_home,omitempty"`
	RedoExisting bool   `json:"redo_existing"`
	Timeout      string `json:"timeout,omitempty"`
}

func runMini(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run-mini", flag.ExitOnError)
	runID := fs.String("run-id", "", "run id")
	output := fs.String("output", "", "output directory; defaults to ../results/runs/<run-id>/raw/mini")
	subset := fs.String("subset", "verified", "SWE-Bench subset for mini-extra")
	split := fs.String("split", defaultSplit, "dataset split")
	filterSpec := fs.String("filter", "", "instance regex filter")
	sliceSpec := fs.String("slice", "", "slice expression")
	workers := fs.Int("agent-workers", 15, "mini-SWE-agent worker count")
	baseConfig := fs.String("base-config", "swebench.yaml", "mini-SWE-agent base config")
	miniConfig := fs.String("mini-config", "../config/mini-swe-agent.minimax-m2.5.local.yaml", "private mini-SWE-agent YAML config")
	miniExtra := fs.String("mini-extra", envOrDefault("MINI_EXTRA", "mini-extra"), "mini-extra executable")
	dockerHost := fs.String("docker-host", envOrDefault("DOCKER_HOST", defaultDockerHost), "Docker host")
	hfHome := fs.String("hf-home", os.Getenv("HF_HOME"), "HF_HOME cache path")
	redoExisting := fs.Bool("redo-existing", false, "pass --redo-existing to mini-extra")
	timeout := fs.Duration("timeout", 0, "optional wall timeout for this mini-SWE-agent batch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "run-id", *runID); err != nil {
		return err
	}
	if *output == "" {
		*output = filepath.Join("..", "results", "runs", *runID, "raw", "mini")
	}
	if *workers < 1 {
		return fmt.Errorf("agent-workers must be >= 1")
	}
	if err := ensureDir(*output); err != nil {
		return err
	}

	cmdArgs := []string{
		"swebench",
		"--subset", *subset,
		"--split", *split,
		"--workers", strconv.Itoa(*workers),
		"--output", *output,
		"--config", *baseConfig,
		"--config", *miniConfig,
	}
	if strings.TrimSpace(*filterSpec) != "" {
		cmdArgs = append(cmdArgs, "--filter", *filterSpec)
	}
	if strings.TrimSpace(*sliceSpec) != "" {
		cmdArgs = append(cmdArgs, "--slice", *sliceSpec)
	}
	if *redoExisting {
		cmdArgs = append(cmdArgs, "--redo-existing")
	}

	env := []string{"DOCKER_HOST=" + *dockerHost}
	if strings.TrimSpace(*hfHome) != "" {
		env = append(env, "HF_HOME="+*hfHome)
	}

	runCtx := ctx
	cancel := func() {}
	if *timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, *timeout)
	}
	defer cancel()

	start := time.Now()
	logPath := filepath.Join(*output, "run-mini.log")
	result := runLogged(runCtx, "", env, logPath, *miniExtra, cmdArgs...)
	finish := time.Now()

	manifest := runMiniManifest{
		RunID:      *runID,
		StartedAt:  start.UTC(),
		FinishedAt: finish.UTC(),
		DurationMS: finish.Sub(start).Milliseconds(),
		Command:    result,
		Config: runMiniConfig{
			Subset:       *subset,
			Split:        *split,
			Filter:       *filterSpec,
			Slice:        *sliceSpec,
			Workers:      *workers,
			OutputDir:    absPath(*output),
			MiniConfig:   absPath(*miniConfig),
			BaseConfig:   *baseConfig,
			MiniExtra:    *miniExtra,
			DockerHost:   *dockerHost,
			HFHome:       *hfHome,
			RedoExisting: *redoExisting,
			Timeout:      timeoutString(*timeout),
		},
	}
	if err := writeJSON(filepath.Join(*output, "run-mini-manifest.json"), manifest); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("mini-extra failed with exit code %d; see %s", result.ExitCode, logPath)
	}
	return nil
}

func timeoutString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}
