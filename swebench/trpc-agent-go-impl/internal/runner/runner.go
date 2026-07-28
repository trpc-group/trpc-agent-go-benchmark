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
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/pricing"
)

const frameworkVersion = "v1.10.1-0.20260616104537-c6c3bb29ab60"

type manifest struct {
	RunID                         string                  `json:"run_id"`
	RunnerType                    string                  `json:"runner_type"`
	FrameworkVersion              string                  `json:"framework_version"`
	FrameworkRevision             string                  `json:"framework_revision,omitempty"`
	AgentProtocol                 string                  `json:"agent_protocol"`
	UpstreamCommit                string                  `json:"upstream_commit"`
	ObservationCodec              string                  `json:"observation_codec"`
	BillingAgentName              string                  `json:"billing_agent_name,omitempty"`
	BillingTag                    string                  `json:"billing_tag,omitempty"`
	ExperimentID                  string                  `json:"experiment_id,omitempty"`
	SourceRevision                string                  `json:"source_revision,omitempty"`
	SourceModified                bool                    `json:"source_modified"`
	BinarySHA256                  string                  `json:"binary_sha256,omitempty"`
	CasesSHA256                   string                  `json:"cases_sha256"`
	CaseList                      string                  `json:"case_list,omitempty"`
	CaseListSHA256                string                  `json:"case_list_sha256,omitempty"`
	SelectedCaseSetSHA256         string                  `json:"selected_case_set_sha256"`
	ModelConfigSHA256             string                  `json:"model_config_sha256"`
	EmbeddingConfigSHA256         string                  `json:"embedding_config_sha256,omitempty"`
	EnvironmentConfigSHA256       string                  `json:"environment_config_sha256"`
	StartedAt                     time.Time               `json:"started_at"`
	FinishedAt                    time.Time               `json:"finished_at"`
	DurationMS                    int64                   `json:"duration_ms"`
	Cases                         string                  `json:"cases"`
	OutputDir                     string                  `json:"output_dir"`
	Filter                        string                  `json:"filter,omitempty"`
	CaseCount                     int                     `json:"case_count"`
	AttemptedCount                int                     `json:"attempted_count"`
	SkippedExisting               int                     `json:"skipped_existing"`
	PredictionCount               int                     `json:"prediction_count"`
	Workers                       int                     `json:"workers"`
	RedoExisting                  bool                    `json:"redo_existing"`
	Predictions                   string                  `json:"predictions"`
	Progress                      string                  `json:"progress"`
	ModelConfig                   map[string]string       `json:"model_config,omitempty"`
	EmbeddingConfig               map[string]any          `json:"embedding_config,omitempty"`
	EmbeddingCacheDB              string                  `json:"embedding_cache_db,omitempty"`
	Environment                   string                  `json:"environment_config"`
	CommandTimeout                string                  `json:"command_timeout"`
	CaseTimeout                   string                  `json:"case_timeout"`
	CodeSearch                    bool                    `json:"code_search"`
	WorkspacePreload              bool                    `json:"workspace_preload"`
	WorkspaceRepresentation       string                  `json:"workspace_representation"`
	WorkspaceRepresentationSchema string                  `json:"workspace_representation_schema"`
	WorkspaceRepresentationSHA256 string                  `json:"workspace_representation_sha256"`
	Embedding                     embeddingconfig.Metrics `json:"embedding"`
	EmbeddingCache                *embeddingcache.Metrics `json:"embedding_cache,omitempty"`
	ExitStatusCounts              map[string]int          `json:"exit_status_counts"`
	LLMCalls                      int                     `json:"llm_calls"`
	ToolCalls                     int                     `json:"tool_calls"`
	Usage                         usageSummary            `json:"usage"`
	Pricing                       *pricing.RateCard       `json:"pricing,omitempty"`
	CostEstimate                  *pricing.Estimate       `json:"cost_estimate,omitempty"`
	Status                        string                  `json:"status"`
	Notes                         []string                `json:"notes,omitempty"`
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
	caseListPath := fs.String("case-list", "", "optional newline-delimited exact instance ids")
	modelConfigPath := fs.String("model-config", "", "model config YAML/env path")
	embeddingConfigPath := fs.String("embedding-config", "", "optional workspace embedding YAML path")
	environmentConfigPath := fs.String("environment-config", "config/environments/swebench-testbed.yaml", "environment YAML path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>/raw/tag")
	filter := fs.String("filter", "", "optional instance id regexp")
	workers := fs.Int("agent-workers", 1, "parallel agent cases")
	commandTimeout := fs.Duration("command-timeout", time.Minute, "timeout for each bash tool call")
	caseTimeout := fs.Duration("case-timeout", 2*time.Hour, "timeout for each case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	offlineAssetsDir := fs.String(
		"offline-assets-dir",
		"",
		"host directory prepared by scripts/prepare-offline-assets.sh; required by affected requests cases",
	)
	redoExisting := fs.Bool("redo-existing", false, "rerun selected cases already present in preds.json")
	codecValue := fs.String("observation-codec", string(minicompat.ObservationCodecXML), "observation codec: xml, json, or text")
	billingTag := fs.String("billing-tag", "", "suffix appended to X-SMG-Agent-Name for billing isolation")
	experimentID := fs.String("experiment-id", "", "experiment identifier recorded with billing-tag")
	frameworkRevision := fs.String("framework-revision", "", "framework source revision recorded in the manifest")
	codeSearch := fs.Bool("code-search", false, "enable local workspace code retrieval")
	workspacePreload := fs.Bool("workspace-preload", false, "inject retrieved workspace context into the initial prompt")
	workspaceRepresentationValue := fs.String(
		"workspace-representation",
		string(tagagent.WorkspaceRepresentationCurrentFixed),
		"workspace representation: current-fixed, fixed-raw, ast-code, or ast-structured",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		return fmt.Errorf("-run-id is required")
	}
	if strings.TrimSpace(*modelConfigPath) == "" {
		return fmt.Errorf("-model-config is required")
	}
	if strings.TrimSpace(*caseListPath) != "" && strings.TrimSpace(*filter) != "" {
		return fmt.Errorf("-case-list and -filter are mutually exclusive")
	}
	if *workers <= 0 {
		return fmt.Errorf("-agent-workers must be positive")
	}
	if strings.TrimSpace(*embeddingConfigPath) != "" && !*codeSearch {
		return fmt.Errorf("-embedding-config requires -code-search")
	}
	if *workspacePreload && !*codeSearch {
		return fmt.Errorf("-workspace-preload=true requires -code-search")
	}
	workspaceRepresentation, err := tagagent.ParseWorkspaceRepresentation(*workspaceRepresentationValue)
	if err != nil {
		return err
	}
	if !*codeSearch && workspaceRepresentation != tagagent.WorkspaceRepresentationCurrentFixed {
		return fmt.Errorf("-workspace-representation=%s requires -code-search", workspaceRepresentation)
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
	var caseListHash string
	var caseIDs map[string]struct{}
	if strings.TrimSpace(*caseListPath) != "" {
		caseListHash, err = fileSHA256(*caseListPath)
		if err != nil {
			return fmt.Errorf("hash case list: %w", err)
		}
		caseIDs, err = loadCaseIDs(*caseListPath)
		if err != nil {
			return fmt.Errorf("load case list: %w", err)
		}
	}
	modelHash, err := fileSHA256(*modelConfigPath)
	if err != nil {
		return fmt.Errorf("hash model config: %w", err)
	}
	var embeddingHash string
	var embeddingCfg *embeddingconfig.Config
	if strings.TrimSpace(*embeddingConfigPath) != "" {
		embeddingHash, err = fileSHA256(*embeddingConfigPath)
		if err != nil {
			return fmt.Errorf("hash embedding config: %w", err)
		}
		embeddingCfg, err = embeddingconfig.Load(*embeddingConfigPath)
		if err != nil {
			return fmt.Errorf("load embedding config: %w", err)
		}
	}
	cases, err := artifact.ReadCasesJSONL(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	selected, err := selectCases(cases, *filter, caseIDs)
	if err != nil {
		return err
	}
	selectedIDs := make([]string, 0, len(selected))
	for _, selectedCase := range selected {
		selectedIDs = append(selectedIDs, selectedCase.InstanceID)
	}
	if err := sweenv.ValidateOfflineAssets(*offlineAssetsDir, selectedIDs); err != nil {
		return err
	}
	selectedCaseSetHash := selectedCaseSetSHA256(selected)
	modelCfg, err := modelconfig.Load(*modelConfigPath)
	if err != nil {
		return fmt.Errorf("load model config: %w", err)
	}
	if strings.TrimSpace(modelCfg["MODEL_NAME"]) == "" {
		return fmt.Errorf("model config has no model.model_name or MODEL_NAME")
	}
	rateCardValue, pricingEnabled, err := modelconfig.Pricing(modelCfg)
	if err != nil {
		return fmt.Errorf("load pricing: %w", err)
	}
	var rateCard *pricing.RateCard
	if pricingEnabled {
		rateCard = &rateCardValue
	}
	billingAgentName, err := resolveBillingAgentName(modelCfg["X_SMG_AGENT_NAME"], *billingTag, *experimentID)
	if err != nil {
		return err
	}
	if billingAgentName != "" {
		modelCfg["X_SMG_AGENT_NAME"] = billingAgentName
	}
	environmentHash, err := fileSHA256(*environmentConfigPath)
	if err != nil {
		return fmt.Errorf("hash environment config: %w", err)
	}
	envCfg, err := sweenv.LoadConfig(*environmentConfigPath)
	if err != nil {
		return err
	}
	var embeddingCache *embeddingcache.Store
	if embeddingCfg != nil && embeddingCfg.Cache.Enabled {
		embeddingCache, err = embeddingcache.Open(
			context.Background(),
			embeddingCfg.Cache.Directory,
			embeddingCfg.CacheIdentity(),
		)
		if err != nil {
			return fmt.Errorf("open embedding cache: %w", err)
		}
		defer func() { _ = embeddingCache.Close() }()
	}
	factory := sweenv.DockerFactory{
		Config: envCfg, DockerHost: *dockerHost, CommandTimeout: *commandTimeout, CaseTimeout: *caseTimeout,
		Labels: map[string]string{"tag-swebench.run_id": *runID}, EnableOfflineServices: true,
		OfflineAssetsDir: *offlineAssetsDir, SanitizeGitHistory: true,
	}
	exec := executor.Executor{
		Factory: factory, ModelConfig: modelCfg, ObservationCodec: codec, CaseTimeout: *caseTimeout,
		EnableCodeSearch: *codeSearch, EnableWorkspacePreload: *workspacePreload, EmbeddingConfig: embeddingCfg,
		EmbeddingCache: embeddingCache, WorkspaceRepresentation: workspaceRepresentation,
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
	var embeddingUsage embeddingconfig.Metrics
	var embeddingCacheUsage embeddingcache.Metrics
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
		if result.Embedding != nil {
			embeddingUsage.Requests += result.Embedding.Requests
			embeddingUsage.BatchRequests += result.Embedding.BatchRequests
			embeddingUsage.Inputs += result.Embedding.Inputs
			embeddingUsage.Errors += result.Embedding.Errors
			embeddingUsage.PromptTokens += result.Embedding.PromptTokens
			embeddingUsage.TotalTokens += result.Embedding.TotalTokens
			embeddingUsage.DurationMS += result.Embedding.DurationMS
		}
		if result.EmbeddingCache != nil {
			embeddingCacheUsage.Add(*result.EmbeddingCache)
		}
		if result.Info.ExitStatus == "Error" || result.Info.ExitStatus == "ArtifactError" {
			status = "completed_with_errors"
		}
	}
	var costEstimate *pricing.Estimate
	if rateCard != nil {
		costEstimate, err = estimateUsageCost(*rateCard, usage)
		if err != nil {
			return fmt.Errorf("estimate cost: %w", err)
		}
	}
	doc := manifest{
		RunID: *runID, RunnerType: "tag", FrameworkVersion: frameworkVersion,
		FrameworkRevision: strings.TrimSpace(*frameworkRevision),
		AgentProtocol:     agentProtocol(codec), UpstreamCommit: minicompat.UpstreamCommit,
		ObservationCodec: string(codec), BillingAgentName: billingAgentName,
		BillingTag: strings.TrimSpace(*billingTag), ExperimentID: strings.TrimSpace(*experimentID),
		SourceRevision: build.SourceRevision, SourceModified: build.SourceModified, BinarySHA256: build.BinarySHA256,
		CasesSHA256: casesHash, CaseList: artifact.AbsPath(*caseListPath),
		CaseListSHA256: caseListHash, SelectedCaseSetSHA256: selectedCaseSetHash,
		ModelConfigSHA256: modelHash, EmbeddingConfigSHA256: embeddingHash,
		EnvironmentConfigSHA256: environmentHash, StartedAt: started.UTC(),
		FinishedAt: finished.UTC(), DurationMS: finished.Sub(started).Milliseconds(),
		Cases: artifact.AbsPath(*casesPath), OutputDir: artifact.AbsPath(*output), Filter: *filter,
		CaseCount: len(selected), AttemptedCount: len(pending), SkippedExisting: len(skipped),
		PredictionCount: len(preds), Workers: *workers, RedoExisting: *redoExisting,
		Predictions: artifact.AbsPath(predictionsPath), Progress: artifact.AbsPath(progressPath),
		ModelConfig: modelconfig.RedactSecrets(modelCfg), Environment: artifact.AbsPath(*environmentConfigPath),
		CommandTimeout: commandTimeout.String(), CaseTimeout: caseTimeout.String(),
		CodeSearch: *codeSearch, WorkspacePreload: *codeSearch && *workspacePreload,
		WorkspaceRepresentation:       string(workspaceRepresentation),
		WorkspaceRepresentationSchema: tagagent.WorkspaceRepresentationSchema(workspaceRepresentation),
		WorkspaceRepresentationSHA256: tagagent.WorkspaceRepresentationSHA256(workspaceRepresentation),
		Embedding:                     embeddingUsage,
		ExitStatusCounts:              exitCounts, LLMCalls: llmCalls, ToolCalls: toolCalls, Usage: usage,
		Pricing: rateCard, CostEstimate: costEstimate,
		Status: status,
		Notes: []string{
			"Each case runs through tRPC-Agent-Go llmagent and runner lifecycles in an independent official SWE-Bench container.",
			"OpenAI SDK retry is configured with max retries 9; preds.json is the resume boundary.",
		},
	}
	if embeddingCfg != nil {
		doc.EmbeddingConfig = embeddingCfg.Redacted()
	}
	if embeddingCache != nil {
		doc.EmbeddingCacheDB = artifact.AbsPath(embeddingCache.Path())
		doc.EmbeddingCache = &embeddingCacheUsage
	}
	manifestPath := filepath.Join(*output, "tag-runner-manifest.json")
	if err := artifact.WriteJSON(manifestPath, doc); err != nil {
		return err
	}
	fmt.Printf("selected=%d attempted=%d skipped_existing=%d predictions=%s\nprogress=%s\nmanifest=%s\n",
		len(selected), len(pending), len(skipped), predictionsPath, progressPath, manifestPath)
	return nil
}

func estimateUsageCost(rateCard pricing.RateCard, usage usageSummary) (*pricing.Estimate, error) {
	billableUsage, err := pricing.IncludedCachedInputUsage(&model.Usage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens:         usage.TotalTokens,
		PromptTokensDetails: model.PromptTokensDetails{CachedTokens: usage.CachedTokens},
	})
	if err != nil {
		return nil, err
	}
	return pricing.Calculate(rateCard, billableUsage)
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

func selectCases(
	cases []contract.Case,
	filter string,
	caseIDs map[string]struct{},
) ([]contract.Case, error) {
	if strings.TrimSpace(filter) == "" && len(caseIDs) == 0 {
		return cases, nil
	}
	var re *regexp.Regexp
	var err error
	if strings.TrimSpace(filter) != "" {
		re, err = regexp.Compile(filter)
		if err != nil {
			return nil, fmt.Errorf("compile filter: %w", err)
		}
	}
	selected := make([]contract.Case, 0)
	foundIDs := make(map[string]struct{}, len(caseIDs))
	for _, c := range cases {
		if len(caseIDs) > 0 {
			if _, ok := caseIDs[c.InstanceID]; !ok {
				continue
			}
			foundIDs[c.InstanceID] = struct{}{}
		}
		if re != nil && !re.MatchString(c.InstanceID) {
			continue
		}
		selected = append(selected, c)
	}
	for instanceID := range caseIDs {
		if _, ok := foundIDs[instanceID]; !ok {
			return nil, fmt.Errorf("case list instance %s is absent from cases JSONL", instanceID)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].InstanceID < selected[j].InstanceID })
	if len(selected) == 0 {
		return nil, fmt.Errorf("case selection matched zero cases")
	}
	return selected, nil
}

func loadCaseIDs(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		instanceID := strings.TrimSpace(line)
		if instanceID == "" || strings.HasPrefix(instanceID, "#") {
			continue
		}
		if _, exists := ids[instanceID]; exists {
			return nil, fmt.Errorf("duplicate instance id %s", instanceID)
		}
		ids[instanceID] = struct{}{}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("case list is empty")
	}
	return ids, nil
}

func selectedCaseSetSHA256(cases []contract.Case) string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.InstanceID)
	}
	sort.Strings(ids)
	return stringSHA256(strings.Join(ids, "\n") + "\n")
}

func markArtifactError(result *executor.CaseResult, err error) {
	result.Info.ExitStatus = "ArtifactError"
	result.Info.Error = err.Error()
	result.Info.ErrorCategory = minicompat.ErrorCategoryArtifact
	result.Info.Retryable = true
}
