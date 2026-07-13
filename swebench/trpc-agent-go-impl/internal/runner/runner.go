//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"flag"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
)

type manifest struct {
	RunID       string            `json:"run_id"`
	RunnerType  string            `json:"runner_type"`
	StartedAt   time.Time         `json:"started_at"`
	FinishedAt  time.Time         `json:"finished_at"`
	DurationMS  int64             `json:"duration_ms"`
	Cases       string            `json:"cases"`
	OutputDir   string            `json:"output_dir"`
	Filter      string            `json:"filter,omitempty"`
	CaseCount   int               `json:"case_count"`
	Predictions string            `json:"predictions"`
	ModelConfig map[string]string `json:"model_config,omitempty"`
	Status      string            `json:"status"`
	Notes       []string          `json:"notes,omitempty"`
}

// Run executes the native runner CLI.
func Run(args []string) error {
	fs := flag.NewFlagSet("trpc-agent-go-impl", flag.ExitOnError)
	runID := fs.String("run-id", "", "run id")
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases.jsonl")
	modelConfigPath := fs.String("model-config", "", "model config YAML/env path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>")
	filter := fs.String("filter", "", "optional instance id regexp")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		return fmt.Errorf("-run-id is required")
	}
	if strings.TrimSpace(*casesPath) == "" {
		return fmt.Errorf("-cases is required")
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID)
	}
	start := time.Now()
	if err := artifact.EnsureDir(*output); err != nil {
		return err
	}

	cases, err := artifact.ReadCasesJSONL(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	selected, err := selectCases(cases, *filter)
	if err != nil {
		return err
	}
	preds := map[string]contract.Prediction{}
	for _, c := range selected {
		preds[c.InstanceID] = contract.Prediction{
			ModelNameOrPath: "trpc-agent-go-native-skeleton",
			InstanceID:      c.InstanceID,
			ModelPatch:      "",
		}
	}
	predictionsPath := filepath.Join(*output, "preds.json")
	if err := artifact.WriteJSON(predictionsPath, preds); err != nil {
		return err
	}

	var redacted modelconfig.EnvConfig
	if strings.TrimSpace(*modelConfigPath) != "" {
		cfg, err := modelconfig.Load(*modelConfigPath)
		if err != nil {
			return fmt.Errorf("load model config: %w", err)
		}
		redacted = modelconfig.RedactSecrets(cfg)
	}
	finish := time.Now()
	doc := manifest{
		RunID:       *runID,
		RunnerType:  "trpc-agent-go-native",
		StartedAt:   start.UTC(),
		FinishedAt:  finish.UTC(),
		DurationMS:  finish.Sub(start).Milliseconds(),
		Cases:       artifact.AbsPath(*casesPath),
		OutputDir:   artifact.AbsPath(*output),
		Filter:      *filter,
		CaseCount:   len(selected),
		Predictions: artifact.AbsPath(predictionsPath),
		ModelConfig: redacted,
		Status:      "skeleton",
		Notes: []string{
			"Native agent loop is not implemented yet; predictions intentionally contain empty patches.",
		},
	}
	if err := artifact.WriteJSON(filepath.Join(*output, "native-runner-manifest.json"), doc); err != nil {
		return err
	}
	fmt.Printf("selected=%d\npredictions=%s\nmanifest=%s\n", len(selected), predictionsPath, filepath.Join(*output, "native-runner-manifest.json"))
	return nil
}

func selectCases(cases []contract.Case, filter string) ([]contract.Case, error) {
	if strings.TrimSpace(filter) == "" {
		return cases, nil
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		return nil, fmt.Errorf("compile filter: %w", err)
	}
	var selected []contract.Case
	for _, c := range cases {
		if re.MatchString(c.InstanceID) {
			selected = append(selected, c)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].InstanceID < selected[j].InstanceID
	})
	if len(selected) == 0 {
		return nil, fmt.Errorf("filter matched zero cases")
	}
	return selected, nil
}
