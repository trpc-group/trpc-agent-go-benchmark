//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package runner provides the TAG SWE-Bench command-line runner.
package runner

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/minicompat"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
)

const frameworkVersion = "v1.10.1-0.20260616104537-c6c3bb29ab60"

type manifest struct {
	RunID             string            `json:"run_id"`
	RunnerType        string            `json:"runner_type"`
	FrameworkVersion  string            `json:"framework_version"`
	AgentProtocol     string            `json:"agent_protocol"`
	UpstreamCommit    string            `json:"upstream_commit"`
	ObservationCodec  string            `json:"observation_codec"`
	BillingAgentName  string            `json:"billing_agent_name,omitempty"`
	BillingTag        string            `json:"billing_tag,omitempty"`
	ExperimentID      string            `json:"experiment_id,omitempty"`
	SourceRevision    string            `json:"source_revision,omitempty"`
	SourceModified    bool              `json:"source_modified"`
	BinarySHA256      string            `json:"binary_sha256,omitempty"`
	CasesSHA256       string            `json:"cases_sha256"`
	ModelConfigSHA256 string            `json:"model_config_sha256"`
	StartedAt         time.Time         `json:"started_at"`
	FinishedAt        time.Time         `json:"finished_at"`
	DurationMS        int64             `json:"duration_ms"`
	Cases             string            `json:"cases"`
	OutputDir         string            `json:"output_dir"`
	Filter            string            `json:"filter,omitempty"`
	CaseCount         int               `json:"case_count"`
	AttemptedCount    int               `json:"attempted_count"`
	SkippedExisting   int               `json:"skipped_existing"`
	PredictionCount   int               `json:"prediction_count"`
	Workers           int               `json:"workers"`
	RedoExisting      bool              `json:"redo_existing"`
	Predictions       string            `json:"predictions"`
	Progress          string            `json:"progress"`
	ModelConfig       map[string]string `json:"model_config,omitempty"`
	Environment       string            `json:"environment_config"`
	CommandTimeout    string            `json:"command_timeout"`
	CaseTimeout       string            `json:"case_timeout"`
	CodeSearch        bool              `json:"code_search"`
	ExitStatusCounts  map[string]int    `json:"exit_status_counts"`
	LLMCalls          int               `json:"llm_calls"`
	ToolCalls         int               `json:"tool_calls"`
	Usage             usageSummary      `json:"usage"`
	Status            string            `json:"status"`
	Notes             []string          `json:"notes,omitempty"`
}

type usageSummary struct {
	PromptTokens     int `json:"prompt_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type progressDocument struct {
	RunID     string                  `json:"run_id"`
	UpdatedAt time.Time               `json:"updated_at"`
	Cases     map[string]progressCase `json:"cases"`
}

type progressCase struct {
	Status        string `json:"status"`
	ErrorCategory string `json:"error_category,omitempty"`
	PatchBytes    int    `json:"patch_bytes"`
	LLMCalls      int    `json:"llm_calls"`
	ToolCalls     int    `json:"tool_calls"`
	DurationMS    int64  `json:"duration_ms"`
}

// Run executes the TAG SWE-Bench runner CLI.
func Run(args []string) error {
	fs := flag.NewFlagSet("trpc-agent-go-impl", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run id")
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases.jsonl")
	modelConfigPath := fs.String("model-config", "", "model config YAML/env path")
	environmentConfigPath := fs.String("environment-config", "config/environments/swebench-testbed.yaml", "environment YAML path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>/raw/tag")
	filter := fs.String("filter", "", "optional instance id regexp")
	workers := fs.Int("agent-workers", 1, "parallel agent cases")
	commandTimeout := fs.Duration("command-timeout", time.Minute, "timeout for each bash tool call")
	caseTimeout := fs.Duration("case-timeout", 2*time.Hour, "timeout for each case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	redoExisting := fs.Bool("redo-existing", false, "rerun selected cases already present in preds.json")
	codecValue := fs.String("observation-codec", string(minicompat.ObservationCodecXML), "observation codec: xml, json, or text")
	billingTag := fs.String("billing-tag", "", "suffix appended to X-SMG-Agent-Name for billing isolation")
	experimentID := fs.String("experiment-id", "", "experiment identifier recorded with billing-tag")
	codeSearch := fs.Bool("code-search", false, "enable local BM25 workspace code search")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		return fmt.Errorf("-run-id is required")
	}
	if strings.TrimSpace(*modelConfigPath) == "" {
		return fmt.Errorf("-model-config is required")
	}
	if *workers <= 0 {
		return fmt.Errorf("-agent-workers must be positive")
	}
	codec, err := minicompat.ParseObservationCodec(*codecValue)
	if err != nil {
		return err
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID, "raw", "tag")
	}
	if err := artifact.EnsureDir(*output); err != nil {
		return err
	}

	started := time.Now()
	build, err := currentBuildMetadata()
	if err != nil {
		return err
	}
	casesHash, err := fileSHA256(*casesPath)
	if err != nil {
		return fmt.Errorf("hash cases: %w", err)
	}
	modelHash, err := fileSHA256(*modelConfigPath)
	if err != nil {
		return fmt.Errorf("hash model config: %w", err)
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
	billingAgentName, err := resolveBillingAgentName(modelCfg["X_SMG_AGENT_NAME"], *billingTag, *experimentID)
	if err != nil {
		return err
	}
	if billingAgentName != "" {
		modelCfg["X_SMG_AGENT_NAME"] = billingAgentName
	}
	envCfg, err := sweenv.LoadConfig(*environmentConfigPath)
	if err != nil {
		return err
	}
	factory := sweenv.DockerFactory{
		Config: envCfg, DockerHost: *dockerHost, CommandTimeout: *commandTimeout, CaseTimeout: *caseTimeout,
		Labels: map[string]string{"tag-swebench.run_id": *runID},
	}
	exec := executor.Executor{
		Factory: factory, ModelConfig: modelCfg, ObservationCodec: codec, CaseTimeout: *caseTimeout,
		EnableCodeSearch: *codeSearch,
	}
	if err := exec.Validate(); err != nil {
		return err
	}

	predictionsPath := filepath.Join(*output, "preds.json")
	preds, pending, skipped, err := prepareResume(predictionsPath, selected, *redoExisting)
	if err != nil {
		return err
	}
	if err := artifact.WriteJSONAtomic(predictionsPath, preds); err != nil {
		return err
	}
	progressPath := filepath.Join(*output, "tag-runner-progress.json")
	progress := progressDocument{RunID: *runID, Cases: map[string]progressCase{}}
	for _, id := range skipped {
		progress.Cases[id] = progressCase{Status: "ExistingPrediction"}
	}
	progress.UpdatedAt = time.Now().UTC()
	if err := artifact.WriteJSONAtomic(progressPath, progress); err != nil {
		return err
	}

	var mu sync.Mutex
	results := map[string]executor.CaseResult{}
	loadedSkipped := 0
	for _, id := range skipped {
		var result executor.CaseResult
		resultPath := filepath.Join(*output, id, id+".tag.json")
		if err := artifact.ReadJSONFile(resultPath, &result); err == nil {
			results[id] = result
			loadedSkipped++
			progress.Cases[id] = progressCase{
				Status: result.Info.ExitStatus, ErrorCategory: result.Info.ErrorCategory,
				PatchBytes: len(result.ModelPatch), LLMCalls: result.LLMCalls,
				ToolCalls: result.ToolCalls, DurationMS: result.DurationMS,
			}
		}
	}
	progress.UpdatedAt = time.Now().UTC()
	if err := artifact.WriteJSONAtomic(progressPath, progress); err != nil {
		return err
	}
	jobs := make(chan contract.Case)
	var wg sync.WaitGroup
	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				caseResult := exec.Execute(context.Background(), c)
				caseDir := filepath.Join(*output, c.InstanceID)
				responsesPath := filepath.Join(caseDir, c.InstanceID+".responses.json")
				if writeErr := artifact.WriteJSON(responsesPath, caseResult.Responses); writeErr != nil {
					markArtifactError(&caseResult, writeErr)
				}
				resultPath := filepath.Join(caseDir, c.InstanceID+".tag.json")
				if writeErr := artifact.WriteJSON(resultPath, caseResult); writeErr != nil {
					markArtifactError(&caseResult, writeErr)
				}

				mu.Lock()
				preds[c.InstanceID] = contract.Prediction{
					ModelNameOrPath: "tag/" + modelCfg["MODEL_NAME"],
					InstanceID:      c.InstanceID, ModelPatch: caseResult.ModelPatch,
				}
				results[c.InstanceID] = caseResult
				progress.Cases[c.InstanceID] = progressCase{
					Status: caseResult.Info.ExitStatus, ErrorCategory: caseResult.Info.ErrorCategory,
					PatchBytes: len(caseResult.ModelPatch), LLMCalls: caseResult.LLMCalls,
					ToolCalls: caseResult.ToolCalls, DurationMS: caseResult.DurationMS,
				}
				progress.UpdatedAt = time.Now().UTC()
				predErr := artifact.WriteJSONAtomic(predictionsPath, preds)
				progressErr := artifact.WriteJSONAtomic(progressPath, progress)
				mu.Unlock()
				if predErr != nil || progressErr != nil {
					fmt.Printf("instance=%s status=ArtifactError error=%q\n", c.InstanceID, errors.Join(predErr, progressErr))
					continue
				}
				fmt.Printf("instance=%s status=%s patch_bytes=%d llm_calls=%d tool_calls=%d\n",
					c.InstanceID, caseResult.Info.ExitStatus, len(caseResult.ModelPatch), caseResult.LLMCalls, caseResult.ToolCalls)
			}
		}()
	}
	for _, c := range pending {
		jobs <- c
	}
	close(jobs)
	wg.Wait()

	finished := time.Now()
	exitCounts := map[string]int{}
	if missingSkipped := len(skipped) - loadedSkipped; missingSkipped > 0 {
		exitCounts["ExistingPrediction"] = missingSkipped
	}
	var llmCalls, toolCalls int
	var usage usageSummary
	status := "completed"
	for _, result := range results {
		exitCounts[result.Info.ExitStatus]++
		llmCalls += result.LLMCalls
		toolCalls += result.ToolCalls
		usage.PromptTokens += result.Usage.PromptTokens
		usage.CachedTokens += result.Usage.PromptTokensDetails.CachedTokens
		usage.CompletionTokens += result.Usage.CompletionTokens
		usage.ReasoningTokens += result.Usage.CompletionTokensDetails.ReasoningTokens
		usage.TotalTokens += result.Usage.TotalTokens
		if result.Info.ExitStatus == "Error" || result.Info.ExitStatus == "ArtifactError" {
			status = "completed_with_errors"
		}
	}
	doc := manifest{
		RunID: *runID, RunnerType: "tag", FrameworkVersion: frameworkVersion,
		AgentProtocol: agentProtocol(codec), UpstreamCommit: minicompat.UpstreamCommit,
		ObservationCodec: string(codec), BillingAgentName: billingAgentName,
		BillingTag: strings.TrimSpace(*billingTag), ExperimentID: strings.TrimSpace(*experimentID),
		SourceRevision: build.SourceRevision, SourceModified: build.SourceModified, BinarySHA256: build.BinarySHA256,
		CasesSHA256: casesHash, ModelConfigSHA256: modelHash, StartedAt: started.UTC(),
		FinishedAt: finished.UTC(), DurationMS: finished.Sub(started).Milliseconds(),
		Cases: artifact.AbsPath(*casesPath), OutputDir: artifact.AbsPath(*output), Filter: *filter,
		CaseCount: len(selected), AttemptedCount: len(pending), SkippedExisting: len(skipped),
		PredictionCount: len(preds), Workers: *workers, RedoExisting: *redoExisting,
		Predictions: artifact.AbsPath(predictionsPath), Progress: artifact.AbsPath(progressPath),
		ModelConfig: modelconfig.RedactSecrets(modelCfg), Environment: artifact.AbsPath(*environmentConfigPath),
		CommandTimeout: commandTimeout.String(), CaseTimeout: caseTimeout.String(),
		CodeSearch:       *codeSearch,
		ExitStatusCounts: exitCounts, LLMCalls: llmCalls, ToolCalls: toolCalls, Usage: usage,
		Status: status,
		Notes: []string{
			"Each case runs through tRPC-Agent-Go llmagent and runner lifecycles in an independent official SWE-Bench container.",
			"OpenAI SDK retry is configured with max retries 9; preds.json is the resume boundary.",
		},
	}
	manifestPath := filepath.Join(*output, "tag-runner-manifest.json")
	if err := artifact.WriteJSON(manifestPath, doc); err != nil {
		return err
	}
	fmt.Printf("selected=%d attempted=%d skipped_existing=%d predictions=%s\nprogress=%s\nmanifest=%s\n",
		len(selected), len(pending), len(skipped), predictionsPath, progressPath, manifestPath)
	return nil
}

func agentProtocol(codec minicompat.ObservationCodec) string {
	if codec == minicompat.ObservationCodecXML {
		return "mini-swe-agent-v2.1-on-tag"
	}
	return "mini-swe-agent-v2.1-on-tag+codec-" + string(codec)
}

func prepareResume(path string, selected []contract.Case, redo bool) (map[string]contract.Prediction, []contract.Case, []string, error) {
	preds := map[string]contract.Prediction{}
	existing, err := artifact.ReadPredictions(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, fmt.Errorf("read existing predictions: %w", err)
	}
	for id, prediction := range existing {
		preds[id] = prediction
	}
	var pending []contract.Case
	var skipped []string
	for _, c := range selected {
		if _, ok := preds[c.InstanceID]; ok && !redo {
			skipped = append(skipped, c.InstanceID)
			continue
		}
		delete(preds, c.InstanceID)
		pending = append(pending, c)
	}
	return preds, pending, skipped, nil
}

func selectCases(cases []contract.Case, filter string) ([]contract.Case, error) {
	if strings.TrimSpace(filter) == "" {
		return cases, nil
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		return nil, fmt.Errorf("compile filter: %w", err)
	}
	selected := make([]contract.Case, 0)
	for _, c := range cases {
		if re.MatchString(c.InstanceID) {
			selected = append(selected, c)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].InstanceID < selected[j].InstanceID })
	if len(selected) == 0 {
		return nil, fmt.Errorf("filter matched zero cases")
	}
	return selected, nil
}

func markArtifactError(result *executor.CaseResult, err error) {
	result.Info.ExitStatus = "ArtifactError"
	result.Info.Error = err.Error()
	result.Info.ErrorCategory = minicompat.ErrorCategoryArtifact
	result.Info.Retryable = true
}
