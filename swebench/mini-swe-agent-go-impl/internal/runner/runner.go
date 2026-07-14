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
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/mini-swe-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/mini-swe-agent-go-impl/internal/sweagent"
)

type manifest struct {
	RunID                      string            `json:"run_id"`
	RunnerType                 string            `json:"runner_type"`
	FrameworkVersion           string            `json:"framework_version"`
	AgentProtocol              string            `json:"agent_protocol"`
	UpstreamCommit             string            `json:"upstream_commit"`
	StartedAt                  time.Time         `json:"started_at"`
	FinishedAt                 time.Time         `json:"finished_at"`
	DurationMS                 int64             `json:"duration_ms"`
	Cases                      string            `json:"cases"`
	OutputDir                  string            `json:"output_dir"`
	Filter                     string            `json:"filter,omitempty"`
	CaseCount                  int               `json:"case_count"`
	AttemptedCount             int               `json:"attempted_count"`
	SkippedExisting            int               `json:"skipped_existing"`
	CompletedCount             int               `json:"completed_count"`
	PredictionCount            int               `json:"prediction_count"`
	Workers                    int               `json:"workers"`
	RedoExisting               bool              `json:"redo_existing"`
	ResumePolicy               string            `json:"resume_policy"`
	Predictions                string            `json:"predictions"`
	Progress                   string            `json:"progress"`
	ModelConfig                map[string]string `json:"model_config,omitempty"`
	Environment                string            `json:"environment_config"`
	CommandTimeout             string            `json:"command_timeout"`
	CaseTimeout                string            `json:"case_timeout"`
	ExitStatusCounts           map[string]int    `json:"exit_status_counts"`
	ErrorCategories            map[string]int    `json:"error_category_counts,omitempty"`
	ServiceErrors              map[string]int    `json:"service_error_counts,omitempty"`
	CumulativeExitStatusCounts map[string]int    `json:"cumulative_exit_status_counts"`
	CumulativeErrorCategories  map[string]int    `json:"cumulative_error_category_counts,omitempty"`
	CumulativeServiceErrors    map[string]int    `json:"cumulative_service_error_counts,omitempty"`
	Status                     string            `json:"status"`
	Notes                      []string          `json:"notes,omitempty"`
}

// Run executes the source-aligned mini-go runner CLI.
func Run(args []string) error {
	fs := flag.NewFlagSet("mini-swe-agent-go-impl", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run id")
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases.jsonl")
	modelConfigPath := fs.String("model-config", "", "model config YAML/env path")
	environmentConfigPath := fs.String("environment-config", "config/environments/swebench-testbed.yaml", "environment YAML path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>")
	filter := fs.String("filter", "", "optional instance id regexp")
	workers := fs.Int("agent-workers", 1, "parallel agent cases")
	commandTimeout := fs.Duration("command-timeout", time.Minute, "timeout for each bash tool call")
	caseTimeout := fs.Duration("case-timeout", 2*time.Hour, "timeout for each case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	redoExisting := fs.Bool("redo-existing", false, "rerun selected cases even when complete artifacts exist")
	resumePolicy := fs.String("resume-policy", resumePolicyUpstream, "resume policy: upstream or retryable")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		return fmt.Errorf("-run-id is required")
	}
	if strings.TrimSpace(*casesPath) == "" {
		return fmt.Errorf("-cases is required")
	}
	if strings.TrimSpace(*modelConfigPath) == "" {
		return fmt.Errorf("-model-config is required")
	}
	if *workers <= 0 {
		return fmt.Errorf("-agent-workers must be positive")
	}
	if *resumePolicy != resumePolicyUpstream && *resumePolicy != resumePolicyRetryable {
		return fmt.Errorf("-resume-policy must be %q or %q", resumePolicyUpstream, resumePolicyRetryable)
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
	modelCfg, err := modelconfig.Load(*modelConfigPath)
	if err != nil {
		return fmt.Errorf("load model config: %w", err)
	}
	if strings.TrimSpace(modelCfg["MODEL_NAME"]) == "" {
		return fmt.Errorf("model config has no model.model_name or MODEL_NAME")
	}
	envCfg, err := environment.LoadConfig(*environmentConfigPath)
	if err != nil {
		return err
	}
	factory := environment.DockerFactory{
		Config:         envCfg,
		DockerHost:     *dockerHost,
		CommandTimeout: *commandTimeout,
		CaseTimeout:    *caseTimeout,
		Labels:         map[string]string{"mini-swe-agent-go.run_id": *runID},
	}
	predictionsPath := filepath.Join(*output, "preds.json")
	resume, err := prepareResume(*output, predictionsPath, selected, *redoExisting, *resumePolicy)
	if err != nil {
		return fmt.Errorf("prepare resume state: %w", err)
	}
	preds := resume.Predictions
	if err := artifact.WriteJSONAtomic(predictionsPath, preds); err != nil {
		return err
	}
	progressPath := filepath.Join(*output, "mini-go-runner-progress.json")
	progress := newProgressReporter(progressPath, *runID)
	for id, result := range resume.Skipped {
		progress.MarkSkipped(id, result)
	}
	executor := sweagent.Executor{
		Factory: factory, ModelConfig: modelCfg, CaseTimeout: *caseTimeout, Progress: progress.Update,
	}
	exitCounts := map[string]int{}
	errorCategories := map[string]int{}
	serviceErrors := map[string]int{}
	for _, result := range resume.Skipped {
		countResult(result, exitCounts, errorCategories, serviceErrors)
	}
	var mu sync.Mutex
	jobs := make(chan contract.Case)
	var wg sync.WaitGroup
	ctx := context.Background()
	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				caseResult := executor.Execute(ctx, c)
				responsesPath := filepath.Join(*output, c.InstanceID, c.InstanceID+".trpc-responses.json")
				if writeErr := artifact.WriteJSON(responsesPath, caseResult.TRPCResponses); writeErr != nil {
					caseResult.Info.ExitStatus = "ArtifactError"
					caseResult.Info.Error = writeErr.Error()
					caseResult.Info.ErrorCategory = sweagent.ErrorCategoryArtifact
					caseResult.Info.Retryable = true
				}
				tracePath := filepath.Join(*output, c.InstanceID, c.InstanceID+".traj.json")
				if writeErr := artifact.WriteJSON(tracePath, caseResult); writeErr != nil {
					caseResult.Info.ExitStatus = "ArtifactError"
					caseResult.Info.Error = writeErr.Error()
					caseResult.Info.ErrorCategory = sweagent.ErrorCategoryArtifact
					caseResult.Info.Retryable = true
				}
				mu.Lock()
				preds[c.InstanceID] = contract.Prediction{
					ModelNameOrPath: "mini-swe-agent-go/" + modelCfg["MODEL_NAME"],
					InstanceID:      c.InstanceID,
					ModelPatch:      caseResult.ModelPatch,
				}
				countResult(caseResult, exitCounts, errorCategories, serviceErrors)
				writeErr := artifact.WriteJSONAtomic(predictionsPath, preds)
				mu.Unlock()
				if writeErr != nil {
					fmt.Printf("instance=%s status=ArtifactError error=%q\n", c.InstanceID, writeErr)
					continue
				}
				fmt.Printf("instance=%s status=%s patch_bytes=%d\n", c.InstanceID, caseResult.Info.ExitStatus, len(caseResult.ModelPatch))
			}
		}()
	}
	for _, c := range resume.Pending {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	finish := time.Now()
	status := "completed"
	if exitCounts["Error"] > 0 || exitCounts["ArtifactError"] > 0 || len(serviceErrors) > 0 {
		status = "completed_with_errors"
	}
	completedCount := 0
	for _, c := range selected {
		if _, ok := preds[c.InstanceID]; ok {
			completedCount++
		}
	}
	cumulativeExitCounts, cumulativeErrorCategories, cumulativeServiceErrors := aggregateResultCounts(*output, preds)
	notes := []string{
		"Each case uses an official SWE-Bench Docker image and an independent source-aligned control loop over the tRPC model transport.",
		"Predictions are updated atomically after every completed case.",
	}
	if *resumePolicy == resumePolicyUpstream {
		notes = append(notes, "Upstream resume policy skips every instance already present in preds.json.")
	} else {
		notes = append(notes, "Extension resume policy retries missing, incomplete, and retryable trajectories.")
	}
	doc := manifest{
		RunID:                      *runID,
		RunnerType:                 "mini-swe-agent-go",
		FrameworkVersion:           "v1.10.1-0.20260616104537-c6c3bb29ab60",
		AgentProtocol:              "mini-swe-agent-v2.1-source-aligned",
		UpstreamCommit:             sweagent.UpstreamCommit,
		StartedAt:                  start.UTC(),
		FinishedAt:                 finish.UTC(),
		DurationMS:                 finish.Sub(start).Milliseconds(),
		Cases:                      artifact.AbsPath(*casesPath),
		OutputDir:                  artifact.AbsPath(*output),
		Filter:                     *filter,
		CaseCount:                  len(selected),
		AttemptedCount:             len(resume.Pending),
		SkippedExisting:            len(resume.Skipped),
		CompletedCount:             completedCount,
		PredictionCount:            len(preds),
		Workers:                    *workers,
		RedoExisting:               *redoExisting,
		ResumePolicy:               *resumePolicy,
		Predictions:                artifact.AbsPath(predictionsPath),
		Progress:                   artifact.AbsPath(progressPath),
		ModelConfig:                modelconfig.RedactSecrets(modelCfg),
		Environment:                artifact.AbsPath(*environmentConfigPath),
		CommandTimeout:             commandTimeout.String(),
		CaseTimeout:                caseTimeout.String(),
		ExitStatusCounts:           exitCounts,
		ErrorCategories:            errorCategories,
		ServiceErrors:              serviceErrors,
		CumulativeExitStatusCounts: cumulativeExitCounts,
		CumulativeErrorCategories:  cumulativeErrorCategories,
		CumulativeServiceErrors:    cumulativeServiceErrors,
		Status:                     status,
		Notes:                      notes,
	}
	manifestPath := filepath.Join(*output, "mini-go-runner-manifest.json")
	if err := artifact.WriteJSON(manifestPath, doc); err != nil {
		return err
	}
	fmt.Printf("selected=%d attempted=%d skipped_existing=%d completed=%d\npredictions=%s\nprogress=%s\nmanifest=%s\n", len(selected), len(resume.Pending), len(resume.Skipped), completedCount, predictionsPath, progressPath, manifestPath)
	return nil
}

func countResult(result sweagent.CaseResult, exitCounts, errorCategories, serviceErrors map[string]int) {
	exitCounts[result.Info.ExitStatus]++
	if result.Info.ErrorCategory == "" {
		return
	}
	errorCategories[result.Info.ErrorCategory]++
	if strings.HasPrefix(result.Info.ErrorCategory, "endpoint_") {
		serviceErrors[result.Info.ErrorCategory]++
	}
}

func aggregateResultCounts(output string, predictions map[string]contract.Prediction) (map[string]int, map[string]int, map[string]int) {
	exitCounts := map[string]int{}
	errorCategories := map[string]int{}
	serviceErrors := map[string]int{}
	for instanceID := range predictions {
		var result sweagent.CaseResult
		tracePath := filepath.Join(output, instanceID, instanceID+".traj.json")
		if err := artifact.ReadJSONFile(tracePath, &result); err != nil {
			result.Info.ExitStatus = "ArtifactError"
			result.Info.ErrorCategory = sweagent.ErrorCategoryArtifact
		}
		countResult(result, exitCounts, errorCategories, serviceErrors)
	}
	return exitCounts, errorCategories, serviceErrors
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
