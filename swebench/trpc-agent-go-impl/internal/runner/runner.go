//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package runner provides the tRPC-Agent-Go SWE-Bench command-line runner.
package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

var artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const offlineHTTPBinImageReference = "docker.io/kennethreitz/httpbin:latest"

type manifest struct {
	RunID                     string                          `json:"run_id"`
	RunnerType                string                          `json:"runner_type"`
	FrameworkModule           string                          `json:"framework_module"`
	FrameworkVersion          string                          `json:"framework_version"`
	AgentProtocol             string                          `json:"agent_protocol"`
	UpstreamCommit            string                          `json:"upstream_commit"`
	ObservationCodec          string                          `json:"observation_codec"`
	SourceRevision            string                          `json:"source_revision,omitempty"`
	SourceModified            bool                            `json:"source_modified"`
	BinarySHA256              string                          `json:"binary_sha256,omitempty"`
	CasesSHA256               string                          `json:"cases_sha256"`
	ModelConfigSHA256         string                          `json:"model_config_sha256"`
	EnvironmentConfigSHA256   string                          `json:"environment_config_sha256"`
	SelectedInstancesSHA256   string                          `json:"selected_instances_sha256"`
	EmbeddingConfigSHA256     string                          `json:"embedding_config_sha256,omitempty"`
	CleanRoom                 bool                            `json:"clean_room"`
	ToolLoopWarning           bool                            `json:"tool_loop_warning"`
	CodeSearch                bool                            `json:"code_search,omitempty"`
	CodeSearchToolOrder       string                          `json:"code_search_tool_order,omitempty"`
	CodeSearchInvocationDedup string                          `json:"code_search_invocation_dedup,omitempty"`
	WorkspacePreload          *bool                           `json:"workspace_preload,omitempty"`
	WorkspaceRepresentation   string                          `json:"workspace_representation,omitempty"`
	RepresentationSchema      string                          `json:"workspace_representation_schema,omitempty"`
	RepresentationSHA256      string                          `json:"workspace_representation_sha256,omitempty"`
	CleanRoomPolicySHA256     string                          `json:"clean_room_policy_sha256,omitempty"`
	OfflineAssets             *sweenv.OfflineAssetIdentity    `json:"offline_assets,omitempty"`
	ImageSetSHA256            string                          `json:"image_set_sha256,omitempty"`
	DockerImages              map[string]sweenv.ImageIdentity `json:"docker_images,omitempty"`
	StartedAt                 time.Time                       `json:"started_at"`
	FinishedAt                time.Time                       `json:"finished_at"`
	DurationMS                int64                           `json:"duration_ms"`
	Cases                     string                          `json:"cases"`
	OutputDir                 string                          `json:"output_dir"`
	Filter                    string                          `json:"filter,omitempty"`
	CaseCount                 int                             `json:"case_count"`
	AttemptedCount            int                             `json:"attempted_count"`
	SkippedExisting           int                             `json:"skipped_existing"`
	CompletedCount            int                             `json:"completed_count"`
	PredictionCount           int                             `json:"prediction_count"`
	Workers                   int                             `json:"workers"`
	RedoExisting              bool                            `json:"redo_existing"`
	RedoBackup                string                          `json:"redo_backup,omitempty"`
	Predictions               string                          `json:"predictions"`
	Progress                  string                          `json:"progress"`
	ModelConfig               map[string]string               `json:"model_config,omitempty"`
	EmbeddingConfig           map[string]any                  `json:"embedding_config,omitempty"`
	Environment               string                          `json:"environment_config"`
	CommandTimeout            string                          `json:"command_timeout"`
	CaseTimeout               string                          `json:"case_timeout"`
	ExitStatusCounts          map[string]int                  `json:"exit_status_counts"`
	LLMCalls                  int                             `json:"llm_calls"`
	ToolCalls                 int                             `json:"tool_calls"`
	ToolLoopWarningCount      int                             `json:"tool_loop_warning_count"`
	ToolLoopWarningCaseCount  int                             `json:"tool_loop_warning_case_count"`
	Embedding                 *embeddingconfig.Metrics        `json:"embedding,omitempty"`
	EmbeddingCache            *embeddingcache.Metrics         `json:"embedding_cache,omitempty"`
	Usage                     usageSummary                    `json:"usage"`
	Status                    string                          `json:"status"`
	Notes                     []string                        `json:"notes,omitempty"`
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
	Status               string `json:"status"`
	ErrorCategory        string `json:"error_category,omitempty"`
	PatchBytes           int    `json:"patch_bytes"`
	LLMCalls             int    `json:"llm_calls"`
	ToolCalls            int    `json:"tool_calls"`
	ToolLoopWarningCount int    `json:"tool_loop_warning_count"`
	DurationMS           int64  `json:"duration_ms"`
}

type resultAggregate struct {
	ExitStatusCounts         map[string]int
	LLMCalls                 int
	ToolCalls                int
	ToolLoopWarningCount     int
	ToolLoopWarningCaseCount int
	Embedding                embeddingconfig.Metrics
	EmbeddingCache           embeddingcache.Metrics
	HasEmbedding             bool
	HasEmbeddingCache        bool
	Usage                    usageSummary
	HasErrors                bool
}

// Run executes the tRPC-Agent-Go SWE-Bench runner CLI.
func Run(args []string) (runErr error) {
	fs := flag.NewFlagSet("trpc-agent-go-impl", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run id")
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases.jsonl")
	modelConfigPath := fs.String("model-config", "", "model config YAML/env path")
	embeddingConfigPath := fs.String("embedding-config", "", "optional workspace embedding YAML path")
	environmentConfigPath := fs.String("environment-config", "config/environments/swebench-testbed.yaml", "environment YAML path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>/raw/native")
	filter := fs.String("filter", "", "optional instance id regexp")
	workers := fs.Int("agent-workers", 1, "parallel agent cases")
	commandTimeout := fs.Duration("command-timeout", time.Minute, "timeout for each bash tool call")
	caseTimeout := fs.Duration("case-timeout", 2*time.Hour, "timeout for each case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	cleanRoom := fs.Bool("clean-room", false, "enable network-none generation and recursive Git sanitation")
	toolLoopWarning := fs.Bool("tool-loop-warning", false, "warn on the next model call after an exact repeated tool-use/result batch")
	codeSearch := fs.Bool("code-search", false, "enable benchmark-local static workspace code retrieval")
	workspacePreload := fs.Bool("workspace-preload", true, "inject retrieved workspace context into the initial prompt")
	workspaceRepresentationValue := fs.String(
		"workspace-representation",
		string(tagagent.WorkspaceRepresentationCurrentFixed),
		"workspace representation: current-fixed, fixed-raw, ast-code, or ast-structured",
	)
	offlineAssetsDir := fs.String(
		"offline-assets-dir",
		"",
		"portable host asset bundle prepared by scripts/prepare-offline-assets.sh",
	)
	redoExisting := fs.Bool("redo-existing", false, "rerun selected cases already present in preds.json")
	codecValue := fs.String("observation-codec", string(observation.ObservationCodecXML), "observation codec: xml, json, or text")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := validateArtifactName("run id", *runID); err != nil {
		return err
	}
	if strings.TrimSpace(*modelConfigPath) == "" {
		return fmt.Errorf("-model-config is required")
	}
	if *workers <= 0 {
		return fmt.Errorf("-agent-workers must be positive")
	}
	if *commandTimeout <= 0 || *caseTimeout <= 0 {
		return fmt.Errorf("command and case timeouts must be positive")
	}
	if !*cleanRoom && strings.TrimSpace(*offlineAssetsDir) != "" {
		return fmt.Errorf("-offline-assets-dir requires -clean-room=true")
	}
	if strings.TrimSpace(*embeddingConfigPath) != "" && !*codeSearch {
		return fmt.Errorf("-embedding-config requires -code-search=true")
	}
	workspaceRepresentation, err := tagagent.ParseWorkspaceRepresentation(*workspaceRepresentationValue)
	if err != nil {
		return err
	}
	if !*codeSearch && workspaceRepresentation != tagagent.WorkspaceRepresentationCurrentFixed {
		return fmt.Errorf("-workspace-representation=%s requires -code-search=true", workspaceRepresentation)
	}
	codec, err := observation.ParseObservationCodec(*codecValue)
	if err != nil {
		return err
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID, "raw", "native")
	}
	if err := artifact.EnsureDir(*output); err != nil {
		return err
	}

	started := time.Now()
	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
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
	environmentHash, err := fileSHA256(*environmentConfigPath)
	if err != nil {
		return fmt.Errorf("hash environment config: %w", err)
	}
	cases, err := artifact.ReadCasesJSONL(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	selected, err := selectCases(cases, *filter)
	if err != nil {
		return err
	}
	for _, c := range selected {
		if err := validateArtifactName("instance id", c.InstanceID); err != nil {
			return err
		}
	}
	selectedIDs := make([]string, 0, len(selected))
	selectedSpecs := make([]sweenv.CaseSpec, 0, len(selected))
	for _, c := range selected {
		selectedIDs = append(selectedIDs, c.InstanceID)
		selectedSpecs = append(selectedSpecs, sweenv.CaseSpec{
			InstanceID: c.InstanceID,
			Repo:       c.Repo,
			BaseCommit: c.BaseCommit,
		})
	}
	selectedHash, err := selectedInstancesSHA256(selectedIDs)
	if err != nil {
		return fmt.Errorf("hash selected instances: %w", err)
	}
	modelCfg, err := modelconfig.Load(*modelConfigPath)
	if err != nil {
		return fmt.Errorf("load model config: %w", err)
	}
	if strings.TrimSpace(modelCfg["MODEL_NAME"]) == "" {
		return fmt.Errorf("model config has no model.model_name or MODEL_NAME")
	}
	envCfg, err := sweenv.LoadConfig(*environmentConfigPath)
	if err != nil {
		return err
	}
	var embeddingCache *embeddingcache.Store
	if embeddingCfg != nil && embeddingCfg.Cache.Enabled {
		embeddingCache, err = embeddingcache.Open(
			runCtx,
			embeddingCfg.Cache.Directory,
			embeddingCfg.CacheIdentity(),
		)
		if err != nil {
			return fmt.Errorf("open embedding cache: %w", err)
		}
		defer func() {
			if closeErr := embeddingCache.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close embedding cache: %w", closeErr))
			}
		}()
	}
	var offlineAssetsIdentity sweenv.OfflineAssetIdentity
	var cleanRoomPolicySHA256 string
	if *cleanRoom {
		offlineAssetsIdentity, err = sweenv.InspectOfflineAssets(*offlineAssetsDir, selectedIDs)
		if err != nil {
			return err
		}
		cleanRoomPolicySHA256, err = sweenv.CleanRoomPolicySHA256(nil)
		if err != nil {
			return err
		}
	}
	factory := sweenv.DockerFactory{
		Config: envCfg, DockerHost: *dockerHost, CommandTimeout: *commandTimeout,
		CaseTimeout: *caseTimeout, ContainerNamePrefix: "trpc-agent-go-swebench-",
		Labels:    map[string]string{"swebench.run_id": *runID, "swebench.runner": "trpc-agent-go"},
		CleanRoom: *cleanRoom, EnableOfflineServices: *cleanRoom,
		OfflineAssetsDir: *offlineAssetsDir, OfflineAssets: offlineAssetsIdentity,
	}
	resolvedImages, err := factory.ResolveImages(runCtx, selectedSpecs)
	if err != nil {
		return err
	}
	imageSetSHA256, err := sweenv.ImageSetSHA256(resolvedImages)
	if err != nil {
		return err
	}
	factory.ResolvedImages = resolvedImages
	effectiveWorkspacePreload := *codeSearch && *workspacePreload
	var workspaceRepresentationName, representationSchema, representationSHA256 string
	var codeSearchToolOrder, codeSearchInvocationDedup string
	if *codeSearch {
		codeSearchToolOrder = tagagent.CodeSearchProviderToolOrder
		codeSearchInvocationDedup = tagagent.CodeSearchInvocationDedup
		workspaceRepresentationName = string(workspaceRepresentation)
		representationSchema = tagagent.WorkspaceRepresentationSchema(workspaceRepresentation)
		representationSHA256 = tagagent.WorkspaceRepresentationSHA256(workspaceRepresentation)
	}
	exec := executor.Executor{
		Factory: factory, ModelConfig: modelCfg, ObservationCodec: codec,
		RunID: *runID, SourceRevision: build.SourceRevision, SourceModified: build.SourceModified,
		BinarySHA256: build.BinarySHA256, ModelConfigSHA256: modelHash,
		EnvironmentConfigSHA256: environmentHash, CasesSHA256: casesHash,
		CommandTimeout: *commandTimeout, CaseTimeout: *caseTimeout,
		SelectedInstancesSHA256: selectedHash,
		CleanRoom:               *cleanRoom, ToolLoopWarning: *toolLoopWarning,
		EnableCodeSearch:        *codeSearch,
		EnableWorkspacePreload:  effectiveWorkspacePreload,
		WorkspaceRepresentation: workspaceRepresentation,
		EmbeddingConfig:         embeddingCfg,
		EmbeddingConfigSHA256:   embeddingHash,
		EmbeddingCache:          embeddingCache,
		CleanRoomPolicySHA256:   cleanRoomPolicySHA256,
		OfflineAssetsSHA256:     offlineAssetsIdentity.SHA256, ImageSetSHA256: imageSetSHA256,
		DockerImages: resolvedImages,
		Workers:      *workers,
	}
	if err := exec.Validate(); err != nil {
		return err
	}

	predictionsPath := filepath.Join(*output, "preds.json")
	redoBackup, err := preservePredictionsForRedo(predictionsPath, *redoExisting)
	if err != nil {
		return err
	}
	redoFallback := map[string]contract.Prediction{}
	if redoBackup != "" {
		redoFallback, err = artifact.ReadPredictions(redoBackup)
		if err != nil {
			return fmt.Errorf("read redo prediction backup: %w", err)
		}
	}
	identity := runIdentity{
		RunID: *runID, ObservationCodec: string(codec), SourceRevision: build.SourceRevision,
		SourceModified: build.SourceModified, BinarySHA256: build.BinarySHA256,
		ModelConfigSHA256: modelHash, EnvironmentConfigSHA256: environmentHash,
		CasesSHA256: casesHash, CommandTimeout: commandTimeout.String(),
		CaseTimeout: caseTimeout.String(), SelectedInstancesSHA256: selectedHash,
		CleanRoom: *cleanRoom, ToolLoopWarning: *toolLoopWarning,
		CodeSearch:                *codeSearch,
		CodeSearchToolOrder:       codeSearchToolOrder,
		CodeSearchInvocationDedup: codeSearchInvocationDedup,
		WorkspacePreload:          effectiveWorkspacePreload,
		WorkspaceRepresentation:   workspaceRepresentationName,
		RepresentationSHA256:      representationSHA256,
		EmbeddingConfigSHA256:     embeddingHash,
		CleanRoomPolicySHA256:     cleanRoomPolicySHA256,
		OfflineAssetsSHA256:       offlineAssetsIdentity.SHA256, ImageSetSHA256: imageSetSHA256,
		DockerImages: resolvedImages,
		Workers:      *workers,
	}
	preds, pending, skipped, err := prepareResume(*output, predictionsPath, selected, *redoExisting, identity)
	if err != nil {
		return err
	}
	if err := persistPredictions(predictionsPath, preds); err != nil {
		return err
	}
	progressPath := filepath.Join(*output, "native-runner-progress.json")
	progress := progressDocument{RunID: *runID, Cases: map[string]progressCase{}}
	for _, id := range skipped {
		progress.Cases[id] = progressCase{Status: "ExistingPrediction"}
	}
	progress.UpdatedAt = time.Now().UTC()
	if err := artifact.WriteJSON(progressPath, progress); err != nil {
		return err
	}

	var mu sync.Mutex
	results := map[string]executor.CaseResult{}
	var artifactWriteErrors []error
	loadedSkipped := 0
	for _, id := range skipped {
		result, err := loadExistingCaseBundle(*output, id, preds[id])
		if err != nil {
			return err
		}
		results[id] = result
		loadedSkipped++
		progress.Cases[id] = progressCaseFromResult(result)
	}
	progress.UpdatedAt = time.Now().UTC()
	if err := artifact.WriteJSON(progressPath, progress); err != nil {
		return err
	}

	jobs := make(chan contract.Case)
	var wg sync.WaitGroup
	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				caseResult := exec.Execute(runCtx, c)
				if runCtx.Err() != nil {
					mu.Lock()
					progress.Cases[c.InstanceID] = progressCase{
						Status: "Interrupted", DurationMS: caseResult.DurationMS,
					}
					progress.UpdatedAt = time.Now().UTC()
					if progressErr := artifact.WriteJSON(progressPath, progress); progressErr != nil {
						artifactWriteErrors = append(artifactWriteErrors,
							fmt.Errorf("persist interrupted progress %s: %w", c.InstanceID, progressErr))
					}
					mu.Unlock()
					fmt.Printf("instance=%s status=Interrupted\n", c.InstanceID)
					continue
				}
				bundleErr := validateToolLoopWarningTelemetry(c.InstanceID, caseResult)
				if bundleErr == nil {
					bundleErr = writeCaseBundle(*output, &caseResult, artifact.WriteJSON)
				}

				mu.Lock()
				if bundleErr == nil {
					preds[c.InstanceID] = contract.Prediction{
						ModelNameOrPath: "trpc-agent-go/" + modelCfg["MODEL_NAME"],
						InstanceID:      c.InstanceID, ModelPatch: caseResult.ModelPatch,
					}
				}
				results[c.InstanceID] = caseResult
				progress.Cases[c.InstanceID] = progressCaseFromResult(caseResult)
				progress.UpdatedAt = time.Now().UTC()
				predErr := persistPredictions(predictionsPath, preds)
				progressErr := artifact.WriteJSON(progressPath, progress)
				if writeErr := errors.Join(bundleErr, predErr, progressErr); writeErr != nil {
					artifactWriteErrors = append(artifactWriteErrors, fmt.Errorf("persist %s: %w", c.InstanceID, writeErr))
				}
				mu.Unlock()
				if bundleErr != nil || predErr != nil || progressErr != nil {
					fmt.Printf("instance=%s status=ArtifactError error=%q\n", c.InstanceID, errors.Join(bundleErr, predErr, progressErr))
					continue
				}
				fmt.Printf("instance=%s status=%s patch_bytes=%d llm_calls=%d tool_calls=%d\n",
					c.InstanceID, caseResult.Info.ExitStatus, len(caseResult.ModelPatch), caseResult.LLMCalls, caseResult.ToolCalls)
			}
		}()
	}
	attemptedCount := dispatchCases(runCtx, jobs, pending)
	close(jobs)
	wg.Wait()
	if runCtx.Err() != nil && len(redoFallback) > 0 {
		for id, prediction := range redoFallback {
			if _, ok := preds[id]; !ok {
				preds[id] = prediction
			}
		}
		if err := persistPredictions(predictionsPath, preds); err != nil {
			artifactWriteErrors = append(artifactWriteErrors,
				fmt.Errorf("restore interrupted redo predictions: %w", err))
		}
	}
	if err := validatePersistedPredictions(predictionsPath, preds); err != nil {
		artifactWriteErrors = append(artifactWriteErrors, err)
	}
	artifactWriteErr := errors.Join(artifactWriteErrors...)

	finished := time.Now()
	aggregate := aggregateResults(results)
	interruptedCount := 0
	for _, caseProgress := range progress.Cases {
		if caseProgress.Status == "Interrupted" {
			interruptedCount++
		}
	}
	if interruptedCount > 0 {
		aggregate.ExitStatusCounts["Interrupted"] = interruptedCount
	}
	if missingSkipped := len(skipped) - loadedSkipped; missingSkipped > 0 {
		aggregate.ExitStatusCounts["ExistingPrediction"] = missingSkipped
	}
	status := "completed"
	if runCtx.Err() != nil {
		status = "interrupted"
	}
	if len(skipped) != loadedSkipped || artifactWriteErr != nil {
		if status != "interrupted" {
			status = "completed_with_errors"
		}
	}
	if aggregate.HasErrors {
		if status != "interrupted" {
			status = "completed_with_errors"
		}
	}
	var workspacePreloadManifest *bool
	if *codeSearch {
		workspacePreloadManifest = new(bool)
		*workspacePreloadManifest = effectiveWorkspacePreload
	}
	doc := manifest{
		RunID: *runID, RunnerType: "trpc-agent-go-native", FrameworkModule: build.FrameworkModule,
		FrameworkVersion: build.FrameworkVersion,
		AgentProtocol:    agentProtocol(codec, *cleanRoom, *toolLoopWarning), UpstreamCommit: protocol.UpstreamCommit,
		ObservationCodec: string(codec), SourceRevision: build.SourceRevision,
		SourceModified: build.SourceModified, BinarySHA256: build.BinarySHA256,
		CasesSHA256: casesHash, ModelConfigSHA256: modelHash,
		EnvironmentConfigSHA256: environmentHash, SelectedInstancesSHA256: selectedHash,
		EmbeddingConfigSHA256: embeddingHash,
		CleanRoom:             *cleanRoom, ToolLoopWarning: *toolLoopWarning,
		CodeSearch:                *codeSearch,
		CodeSearchToolOrder:       codeSearchToolOrder,
		CodeSearchInvocationDedup: codeSearchInvocationDedup,
		WorkspacePreload:          workspacePreloadManifest,
		WorkspaceRepresentation:   workspaceRepresentationName,
		RepresentationSchema:      representationSchema,
		RepresentationSHA256:      representationSHA256,
		CleanRoomPolicySHA256:     cleanRoomPolicySHA256,
		ImageSetSHA256:            imageSetSHA256, DockerImages: resolvedImages,
		StartedAt:  started.UTC(),
		FinishedAt: finished.UTC(), DurationMS: finished.Sub(started).Milliseconds(),
		Cases: artifact.AbsPath(*casesPath), OutputDir: artifact.AbsPath(*output), Filter: *filter,
		CaseCount: len(selected), AttemptedCount: attemptedCount, SkippedExisting: len(skipped),
		CompletedCount:  len(preds),
		PredictionCount: len(preds), Workers: *workers, RedoExisting: *redoExisting,
		RedoBackup:  artifact.AbsPath(redoBackup),
		Predictions: artifact.AbsPath(predictionsPath), Progress: artifact.AbsPath(progressPath),
		ModelConfig: modelManifestConfig(modelCfg), Environment: artifact.AbsPath(*environmentConfigPath),
		CommandTimeout: commandTimeout.String(), CaseTimeout: caseTimeout.String(),
		ExitStatusCounts: aggregate.ExitStatusCounts, LLMCalls: aggregate.LLMCalls,
		ToolCalls: aggregate.ToolCalls, ToolLoopWarningCount: aggregate.ToolLoopWarningCount,
		ToolLoopWarningCaseCount: aggregate.ToolLoopWarningCaseCount, Usage: aggregate.Usage,
		Status: status,
		Notes: []string{
			"Each case runs through tRPC-Agent-Go llmagent and runner lifecycles in an independent official SWE-Bench container.",
			"OpenAI SDK retry is configured with nine retries after the initial request; preds.json is the resume boundary.",
		},
	}
	if embeddingCfg != nil {
		doc.EmbeddingConfig = embeddingCfg.Redacted()
		embedding := aggregate.Embedding
		doc.Embedding = &embedding
	}
	if embeddingCache != nil {
		cache := aggregate.EmbeddingCache
		doc.EmbeddingCache = &cache
	}
	if offlineAssetsIdentity.SHA256 != "" {
		doc.OfflineAssets = &offlineAssetsIdentity
	}
	if *cleanRoom {
		doc.Notes = append(doc.Notes,
			"Clean-room cases use local immutable image IDs, Docker network=none, recursive Git sanitation, and exact base-commit verification before the first model call.",
		)
	}
	if *toolLoopWarning {
		doc.Notes = append(doc.Notes,
			"Exact repeated complete tool-use/result batches inject one warning immediately before the next real model call; telemetry records those injection call numbers.",
		)
	}
	if *codeSearch {
		doc.Notes = append(doc.Notes,
			"Workspace retrieval uses a task-start benchmark-local snapshot; the selected representation and embedding configuration hash are frozen in every case artifact.",
		)
	}
	if redoBackup != "" {
		doc.Notes = append(doc.Notes,
			"The exact pre-redo preds.json boundary is preserved in redo_backup before selected predictions are replaced.",
		)
	}
	manifestPath := filepath.Join(*output, "native-runner-manifest.json")
	if err := artifact.WriteJSON(manifestPath, doc); err != nil {
		return err
	}
	fmt.Printf("selected=%d attempted=%d skipped_existing=%d predictions=%s\nprogress=%s\nmanifest=%s\n",
		len(selected), attemptedCount, len(skipped), predictionsPath, progressPath, manifestPath)
	if artifactWriteErr != nil {
		return fmt.Errorf("persist run artifacts: %w", artifactWriteErr)
	}
	if runCtx.Err() != nil {
		return fmt.Errorf("run interrupted: %w", runCtx.Err())
	}
	return nil
}

func preservePredictionsForRedo(path string, redo bool) (string, error) {
	if !redo {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read predictions before redo: %w", err)
	}
	digest := sha256.Sum256(data)
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), extension)
	backupPath := filepath.Join(
		filepath.Dir(path),
		fmt.Sprintf("%s.pre-redo.%s%s", base, hex.EncodeToString(digest[:]), extension),
	)
	existing, err := os.ReadFile(backupPath)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return "", fmt.Errorf("redo prediction backup %s has unexpected content", backupPath)
		}
		return backupPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read redo prediction backup: %w", err)
	}
	if err := artifact.WriteFileAtomic(backupPath, data, 0o644); err != nil {
		return "", fmt.Errorf("preserve predictions before redo: %w", err)
	}
	return backupPath, nil
}

func dispatchCases(ctx context.Context, jobs chan<- contract.Case, pending []contract.Case) int {
	attempted := 0
	for _, c := range pending {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- c:
			attempted++
		case <-ctx.Done():
			return attempted
		}
	}
	return attempted
}

func progressCaseFromResult(result executor.CaseResult) progressCase {
	return progressCase{
		Status: result.Info.ExitStatus, ErrorCategory: result.Info.ErrorCategory,
		PatchBytes: len(result.ModelPatch), LLMCalls: result.LLMCalls,
		ToolCalls: result.ToolCalls, ToolLoopWarningCount: result.ToolLoopWarningCount,
		DurationMS: result.DurationMS,
	}
}

func aggregateResults(results map[string]executor.CaseResult) resultAggregate {
	aggregate := resultAggregate{ExitStatusCounts: map[string]int{}}
	for _, result := range results {
		aggregate.ExitStatusCounts[result.Info.ExitStatus]++
		aggregate.LLMCalls += result.LLMCalls
		aggregate.ToolCalls += result.ToolCalls
		aggregate.ToolLoopWarningCount += result.ToolLoopWarningCount
		if result.ToolLoopWarningCount > 0 {
			aggregate.ToolLoopWarningCaseCount++
		}
		if result.Embedding != nil {
			aggregate.HasEmbedding = true
			aggregate.Embedding.Requests += result.Embedding.Requests
			aggregate.Embedding.BatchRequests += result.Embedding.BatchRequests
			aggregate.Embedding.Inputs += result.Embedding.Inputs
			aggregate.Embedding.Errors += result.Embedding.Errors
			aggregate.Embedding.PromptTokens += result.Embedding.PromptTokens
			aggregate.Embedding.TotalTokens += result.Embedding.TotalTokens
			aggregate.Embedding.DurationMS += result.Embedding.DurationMS
		}
		if result.EmbeddingCache != nil {
			aggregate.HasEmbeddingCache = true
			aggregate.EmbeddingCache.Add(*result.EmbeddingCache)
		}
		aggregate.Usage.PromptTokens += result.Usage.PromptTokens
		aggregate.Usage.CachedTokens += result.Usage.PromptTokensDetails.CachedTokens
		aggregate.Usage.CompletionTokens += result.Usage.CompletionTokens
		aggregate.Usage.ReasoningTokens += result.Usage.CompletionTokensDetails.ReasoningTokens
		aggregate.Usage.TotalTokens += result.Usage.TotalTokens
		if result.Info.ExitStatus == "Error" || result.Info.ExitStatus == "ArtifactError" {
			aggregate.HasErrors = true
		}
	}
	return aggregate
}

func agentProtocol(codec observation.ObservationCodec, cleanRoom, toolLoopWarning bool) string {
	base := "mini-swe-agent-v2.1-on-trpc-agent-go"
	if codec != observation.ObservationCodecXML {
		base += "+codec-" + string(codec)
	}
	if cleanRoom {
		base += "+clean-room-v1"
	}
	if toolLoopWarning {
		base += "+tool-loop-warning-v1"
	}
	return base
}

func modelManifestConfig(cfg modelconfig.EnvConfig) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"MODEL_NAME", "MODEL_TIMEOUT_SECONDS", "MODEL_TEMPERATURE", "MODEL_REASONING_EFFORT"} {
		if value := strings.TrimSpace(cfg[key]); value != "" {
			out[key] = value
		}
	}
	out["HTTP_HEADER_COUNT"] = strconv.Itoa(len(modelconfig.HTTPHeaders(cfg)))
	return out
}

func prepareResume(
	output, path string,
	selected []contract.Case,
	redo bool,
	identity runIdentity,
) (map[string]contract.Prediction, []contract.Case, []string, error) {
	preds := map[string]contract.Prediction{}
	if err := validateRunIdentity(identity); err != nil {
		return nil, nil, nil, err
	}
	selectedIDs := make(map[string]struct{}, len(selected))
	selectedIDList := make([]string, 0, len(selected))
	for _, c := range selected {
		if err := validateArtifactName("selected instance id", c.InstanceID); err != nil {
			return nil, nil, nil, err
		}
		if _, ok := selectedIDs[c.InstanceID]; ok {
			return nil, nil, nil, fmt.Errorf("duplicate selected instance id %q", c.InstanceID)
		}
		selectedIDs[c.InstanceID] = struct{}{}
		selectedIDList = append(selectedIDList, c.InstanceID)
	}
	selectedHash, err := selectedInstancesSHA256(selectedIDList)
	if err != nil {
		return nil, nil, nil, err
	}
	if selectedHash != identity.SelectedInstancesSHA256 {
		return nil, nil, nil, fmt.Errorf(
			"selected instance hash %q does not match run identity %q",
			selectedHash,
			identity.SelectedInstancesSHA256,
		)
	}
	existing, err := artifact.ReadPredictions(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, fmt.Errorf("read existing predictions: %w", err)
	}
	for id, prediction := range existing {
		if _, ok := selectedIDs[id]; !ok {
			return nil, nil, nil, fmt.Errorf("existing prediction %q is not part of the current selected instance set", id)
		}
		preds[id] = prediction
	}
	var pending []contract.Case
	var skipped []string
	for _, c := range selected {
		prediction, found := preds[c.InstanceID]
		if found {
			result, err := loadExistingCaseBundle(output, c.InstanceID, prediction)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("cannot validate provenance for existing prediction %s: %w", c.InstanceID, err)
			}
			if err := validateResumeResultForMode(c, result, identity, redo); err != nil {
				return nil, nil, nil, err
			}
			if !redo {
				skipped = append(skipped, c.InstanceID)
				continue
			}
		}
		delete(preds, c.InstanceID)
		pending = append(pending, c)
	}
	return preds, pending, skipped, nil
}

func validateRunIdentity(identity runIdentity) error {
	if err := validateArtifactName("run id", identity.RunID); err != nil {
		return err
	}
	required := []struct {
		name  string
		value string
	}{
		{"observation codec", identity.ObservationCodec},
		{"command timeout", identity.CommandTimeout},
		{"case timeout", identity.CaseTimeout},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("run identity has empty %s", field.name)
		}
	}
	if !isHexIdentifier(identity.SourceRevision, 40, 64) {
		return fmt.Errorf("run identity source revision %q is not a full Git revision", identity.SourceRevision)
	}
	for _, hash := range []struct {
		name  string
		value string
	}{
		{"binary hash", identity.BinarySHA256},
		{"model config hash", identity.ModelConfigSHA256},
		{"environment config hash", identity.EnvironmentConfigSHA256},
		{"cases hash", identity.CasesSHA256},
		{"selected instances hash", identity.SelectedInstancesSHA256},
	} {
		if !isHexIdentifier(hash.value, 64) {
			return fmt.Errorf("run identity %s %q is not a SHA-256 digest", hash.name, hash.value)
		}
	}
	if identity.Workers <= 0 {
		return fmt.Errorf("run identity has non-positive workers %d", identity.Workers)
	}
	if identity.CodeSearch {
		if identity.CodeSearchToolOrder != tagagent.CodeSearchProviderToolOrder {
			return fmt.Errorf(
				"run identity code_search tool order %q, want %q",
				identity.CodeSearchToolOrder,
				tagagent.CodeSearchProviderToolOrder,
			)
		}
		if identity.CodeSearchInvocationDedup != tagagent.CodeSearchInvocationDedup {
			return fmt.Errorf(
				"run identity code_search invocation dedup %q, want %q",
				identity.CodeSearchInvocationDedup,
				tagagent.CodeSearchInvocationDedup,
			)
		}
		representation, err := tagagent.ParseWorkspaceRepresentation(identity.WorkspaceRepresentation)
		if err != nil {
			return fmt.Errorf("run identity workspace representation: %w", err)
		}
		if string(representation) != identity.WorkspaceRepresentation {
			return fmt.Errorf("run identity workspace representation is not canonical")
		}
		wantRepresentationSHA256 := tagagent.WorkspaceRepresentationSHA256(representation)
		if identity.RepresentationSHA256 != wantRepresentationSHA256 {
			return fmt.Errorf(
				"run identity workspace representation hash %q, want %q",
				identity.RepresentationSHA256,
				wantRepresentationSHA256,
			)
		}
		if identity.EmbeddingConfigSHA256 != "" && !isHexIdentifier(identity.EmbeddingConfigSHA256, 64) {
			return fmt.Errorf(
				"run identity embedding config hash %q is not a SHA-256 digest",
				identity.EmbeddingConfigSHA256,
			)
		}
	} else if identity.CodeSearchToolOrder != "" || identity.CodeSearchInvocationDedup != "" ||
		identity.WorkspacePreload || identity.WorkspaceRepresentation != "" ||
		identity.RepresentationSHA256 != "" || identity.EmbeddingConfigSHA256 != "" {
		return fmt.Errorf("run identity contains workspace retrieval provenance with code_search=false")
	}
	if identity.CleanRoom {
		for _, hash := range []struct {
			name  string
			value string
		}{
			{"clean-room policy hash", identity.CleanRoomPolicySHA256},
			{"image-set hash", identity.ImageSetSHA256},
		} {
			if !isHexIdentifier(hash.value, 64) {
				return fmt.Errorf("run identity %s %q is not a SHA-256 digest", hash.name, hash.value)
			}
		}
		if identity.OfflineAssetsSHA256 != "" && !isHexIdentifier(identity.OfflineAssetsSHA256, 64) {
			return fmt.Errorf("run identity offline assets hash %q is not a SHA-256 digest", identity.OfflineAssetsSHA256)
		}
		resolvedHash, err := sweenv.ImageSetSHA256(identity.DockerImages)
		if err != nil {
			return err
		}
		if resolvedHash != identity.ImageSetSHA256 {
			return fmt.Errorf("run identity Docker images hash %q, want %q", resolvedHash, identity.ImageSetSHA256)
		}
	} else if identity.CleanRoomPolicySHA256 != "" || identity.OfflineAssetsSHA256 != "" ||
		identity.ImageSetSHA256 != "" || len(identity.DockerImages) != 0 {
		return fmt.Errorf("non-clean-room run identity contains clean-room provenance")
	}
	return nil
}

func isHexIdentifier(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		if len(value) == length {
			validLength = true
			break
		}
	}
	if !validLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func validateResumeResult(c contract.Case, result executor.CaseResult, expected runIdentity) error {
	return validateResumeResultForMode(c, result, expected, false)
}

func validateResumeResultForMode(
	c contract.Case,
	result executor.CaseResult,
	expected runIdentity,
	redo bool,
) error {
	instanceID := c.InstanceID
	allowPreStartRetry := redo && result.IsRetryableCleanRoomPreStartFailure()
	if result.InstanceID != instanceID {
		return fmt.Errorf("existing prediction %s has result instance_id %q", instanceID, result.InstanceID)
	}
	info := result.Info
	if info.CleanRoom != expected.CleanRoom {
		return fmt.Errorf("existing prediction %s has clean_room=%t, want %t", instanceID, info.CleanRoom, expected.CleanRoom)
	}
	if info.ToolLoopWarning != expected.ToolLoopWarning {
		return fmt.Errorf(
			"existing prediction %s has tool_loop_warning=%t, want %t",
			instanceID,
			info.ToolLoopWarning,
			expected.ToolLoopWarning,
		)
	}
	if info.CodeSearch != expected.CodeSearch {
		return fmt.Errorf(
			"existing prediction %s has code_search=%t, want %t",
			instanceID,
			info.CodeSearch,
			expected.CodeSearch,
		)
	}
	if info.CodeSearchToolOrder != expected.CodeSearchToolOrder {
		return fmt.Errorf(
			"existing prediction %s has code_search_tool_order=%q, want %q",
			instanceID,
			info.CodeSearchToolOrder,
			expected.CodeSearchToolOrder,
		)
	}
	if info.CodeSearchInvocationDedup != expected.CodeSearchInvocationDedup {
		return fmt.Errorf(
			"existing prediction %s has code_search_invocation_dedup=%q, want %q",
			instanceID,
			info.CodeSearchInvocationDedup,
			expected.CodeSearchInvocationDedup,
		)
	}
	if expected.CodeSearch && (info.WorkspacePreload == nil || *info.WorkspacePreload != expected.WorkspacePreload) {
		actual := "missing"
		if info.WorkspacePreload != nil {
			actual = strconv.FormatBool(*info.WorkspacePreload)
		}
		return fmt.Errorf(
			"existing prediction %s has workspace_preload=%s, want %t",
			instanceID,
			actual,
			expected.WorkspacePreload,
		)
	}
	if !expected.CodeSearch && info.WorkspacePreload != nil {
		return fmt.Errorf(
			"existing prediction %s has workspace_preload set for code_search=false",
			instanceID,
		)
	}
	if err := validateToolLoopWarningTelemetry(instanceID, result); err != nil {
		return err
	}
	if err := validateWorkspaceRetrievalTelemetry(instanceID, result, expected, allowPreStartRetry); err != nil {
		return err
	}
	checks := []struct {
		name     string
		actual   string
		expected string
	}{
		{"run id", info.RunID, expected.RunID},
		{"observation codec", info.ObservationCodec, expected.ObservationCodec},
		{"source revision", info.SourceRevision, expected.SourceRevision},
		{"binary hash", info.BinarySHA256, expected.BinarySHA256},
		{"model config hash", info.ModelConfigSHA256, expected.ModelConfigSHA256},
		{"environment config hash", info.EnvironmentConfigSHA256, expected.EnvironmentConfigSHA256},
		{"cases hash", info.CasesSHA256, expected.CasesSHA256},
		{"command timeout", info.CommandTimeout, expected.CommandTimeout},
		{"case timeout", info.CaseTimeout, expected.CaseTimeout},
		{"selected instances hash", info.SelectedInstancesSHA256, expected.SelectedInstancesSHA256},
	}
	for _, check := range checks {
		if check.expected != "" && check.actual != check.expected {
			return fmt.Errorf("existing prediction %s has %s %q, want %q", instanceID, check.name, check.actual, check.expected)
		}
	}
	for _, check := range []struct {
		name     string
		actual   string
		expected string
	}{
		{"workspace representation", info.WorkspaceRepresentation, expected.WorkspaceRepresentation},
		{"workspace representation hash", info.RepresentationSHA256, expected.RepresentationSHA256},
		{"embedding config hash", info.EmbeddingConfigSHA256, expected.EmbeddingConfigSHA256},
	} {
		if check.actual != check.expected {
			return fmt.Errorf("existing prediction %s has %s %q, want %q", instanceID, check.name, check.actual, check.expected)
		}
	}
	for _, check := range []struct {
		name     string
		actual   string
		expected string
	}{
		{"clean-room policy hash", info.CleanRoomPolicySHA256, expected.CleanRoomPolicySHA256},
		{"offline assets hash", info.OfflineAssetsSHA256, expected.OfflineAssetsSHA256},
		{"image-set hash", info.ImageSetSHA256, expected.ImageSetSHA256},
	} {
		if check.actual != check.expected {
			return fmt.Errorf("existing prediction %s has %s %q, want %q", instanceID, check.name, check.actual, check.expected)
		}
	}
	if info.SourceModified != expected.SourceModified {
		return fmt.Errorf(
			"existing prediction %s has source_modified=%t, want %t",
			instanceID,
			info.SourceModified,
			expected.SourceModified,
		)
	}
	if expected.CleanRoom {
		for _, check := range []struct {
			name     string
			actual   string
			expected string
		}{
			{"repo", info.Repo, c.Repo},
			{"base commit", info.BaseCommit, c.BaseCommit},
		} {
			if check.actual != check.expected {
				return fmt.Errorf("existing prediction %s has %s %q, want %q", instanceID, check.name, check.actual, check.expected)
			}
		}
		if !allowPreStartRetry {
			if info.VerifiedBaseCommit != c.BaseCommit {
				return fmt.Errorf(
					"existing prediction %s has verified base commit %q, want %q",
					instanceID,
					info.VerifiedBaseCommit,
					c.BaseCommit,
				)
			}
			if info.EnvironmentProvenance == nil {
				return fmt.Errorf("existing prediction %s has no environment provenance", instanceID)
			}
			wantImage, ok := expected.DockerImages[sweenv.ImageForInstance(instanceID)]
			if !ok || info.EnvironmentProvenance.Testbed != wantImage {
				return fmt.Errorf("existing prediction %s has unexpected testbed image provenance", instanceID)
			}
			wantAuxiliary, err := expectedAuxiliaryImages(instanceID, expected.DockerImages, wantImage)
			if err != nil {
				return fmt.Errorf("existing prediction %s: %w", instanceID, err)
			}
			if len(info.EnvironmentProvenance.AuxiliaryImages) != len(wantAuxiliary) {
				return fmt.Errorf("existing prediction %s has unexpected auxiliary image roles", instanceID)
			}
			for role, want := range wantAuxiliary {
				if actual, ok := info.EnvironmentProvenance.AuxiliaryImages[role]; !ok || actual != want {
					return fmt.Errorf("existing prediction %s has unexpected %s image provenance", instanceID, role)
				}
			}
		}
	} else if info.VerifiedBaseCommit != "" || info.EnvironmentProvenance != nil {
		return fmt.Errorf("existing prediction %s carries clean-room case provenance with clean_room=false", instanceID)
	}
	if info.Workers != expected.Workers {
		return fmt.Errorf("existing prediction %s has workers %d, want %d", instanceID, info.Workers, expected.Workers)
	}
	return nil
}

func validateWorkspaceRetrievalTelemetry(
	instanceID string,
	result executor.CaseResult,
	expected runIdentity,
	allowPreStartRetry bool,
) error {
	hasTelemetry := result.CodeSearchCalls != 0 || result.CodeSearchErrors != 0 ||
		result.CodeSearchResultBytes != 0 || result.CodeSearchObservationBytes != 0 ||
		len(result.CodeSearchRawResults) != 0 || len(result.RetrievalTrace) != 0 ||
		result.WorkspaceIndex != nil || result.Embedding != nil || result.EmbeddingCache != nil
	if !expected.CodeSearch {
		if hasTelemetry {
			return fmt.Errorf(
				"existing prediction %s has workspace retrieval telemetry with code_search=false",
				instanceID,
			)
		}
		return nil
	}
	for _, value := range []struct {
		name  string
		value int
	}{
		{"code_search_calls", result.CodeSearchCalls},
		{"code_search_errors", result.CodeSearchErrors},
		{"code_search_result_bytes", result.CodeSearchResultBytes},
		{"code_search_observation_bytes", result.CodeSearchObservationBytes},
	} {
		if value.value < 0 {
			return fmt.Errorf("existing prediction %s has negative %s %d", instanceID, value.name, value.value)
		}
	}
	if len(result.CodeSearchRawResults) != len(result.RetrievalTrace) ||
		len(result.RetrievalTrace) != result.CodeSearchCalls {
		return fmt.Errorf(
			"existing prediction %s has code_search evidence counts raw=%d trace=%d calls=%d",
			instanceID,
			len(result.CodeSearchRawResults),
			len(result.RetrievalTrace),
			result.CodeSearchCalls,
		)
	}
	resultBytes := 0
	observationBytes := 0
	errorCount := 0
	for index, entry := range result.RetrievalTrace {
		if entry.Call != index+1 {
			return fmt.Errorf("existing prediction %s retrieval call %d is not sequential", instanceID, entry.Call)
		}
		if strings.TrimSpace(entry.ToolCallID) == "" {
			return fmt.Errorf("existing prediction %s retrieval call %d has empty tool_call_id", instanceID, entry.Call)
		}
		switch entry.Status {
		case "success":
			if entry.Error != "" || entry.ErrorSHA256 != "" {
				return fmt.Errorf("existing prediction %s retrieval call %d has error fields with status=success", instanceID, entry.Call)
			}
			if entry.ObservationBytes <= 0 || !isHexIdentifier(entry.ObservationSHA256, 64) {
				return fmt.Errorf("existing prediction %s retrieval call %d has incomplete success observation", instanceID, entry.Call)
			}
		case "error":
			errorCount++
			if strings.TrimSpace(entry.Error) == "" ||
				entry.ErrorSHA256 != tagagent.DigestBytes([]byte(entry.Error)) {
				return fmt.Errorf("existing prediction %s retrieval call %d has invalid error identity", instanceID, entry.Call)
			}
			if len(entry.Documents) != 0 {
				return fmt.Errorf("existing prediction %s retrieval call %d has documents with status=error", instanceID, entry.Call)
			}
		default:
			return fmt.Errorf("existing prediction %s retrieval call %d has invalid status %q", instanceID, entry.Call, entry.Status)
		}
		if !isHexIdentifier(entry.ArgumentsSHA256, 64) || !isHexIdentifier(entry.ResultSHA256, 64) {
			return fmt.Errorf("existing prediction %s retrieval call %d has invalid digest", instanceID, entry.Call)
		}
		if entry.ResultBytes < 0 || entry.ObservationBytes < 0 ||
			(entry.ObservationBytes == 0 && entry.ObservationSHA256 != "") ||
			(entry.ObservationBytes > 0 && !isHexIdentifier(entry.ObservationSHA256, 64)) {
			return fmt.Errorf("existing prediction %s retrieval call %d has invalid byte or observation identity", instanceID, entry.Call)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, result.CodeSearchRawResults[index]); err != nil {
			return fmt.Errorf("existing prediction %s retrieval call %d has invalid raw result: %w", instanceID, entry.Call, err)
		}
		raw := compact.Bytes()
		if len(raw) != entry.ResultBytes || tagagent.DigestBytes(raw) != entry.ResultSHA256 {
			return fmt.Errorf("existing prediction %s retrieval call %d does not match raw result", instanceID, entry.Call)
		}
		if entry.Status == "error" {
			var payload struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil || payload.Error != entry.Error {
				return fmt.Errorf("existing prediction %s retrieval call %d error does not match raw result", instanceID, entry.Call)
			}
		}
		for _, document := range entry.Documents {
			if math.IsNaN(document.Score) || math.IsInf(document.Score, 0) ||
				!isHexIdentifier(document.ContentSHA256, 64) {
				return fmt.Errorf("existing prediction %s retrieval call %d has invalid document identity", instanceID, entry.Call)
			}
		}
		resultBytes += entry.ResultBytes
		observationBytes += entry.ObservationBytes
	}
	if errorCount != result.CodeSearchErrors {
		return fmt.Errorf(
			"existing prediction %s has %d retrieval errors, want code_search_errors=%d",
			instanceID,
			errorCount,
			result.CodeSearchErrors,
		)
	}
	if resultBytes != result.CodeSearchResultBytes || observationBytes != result.CodeSearchObservationBytes {
		return fmt.Errorf(
			"existing prediction %s retrieval byte totals %d/%d do not match declared %d/%d",
			instanceID,
			resultBytes,
			observationBytes,
			result.CodeSearchResultBytes,
			result.CodeSearchObservationBytes,
		)
	}
	if result.WorkspaceIndex == nil {
		if allowPreStartRetry && !hasTelemetry {
			return nil
		}
		return fmt.Errorf("existing prediction %s is missing workspace_index for code_search=true", instanceID)
	}
	stats := result.WorkspaceIndex
	wantRepresentation := tagagent.WorkspaceRepresentation(expected.WorkspaceRepresentation)
	wantSchema := tagagent.WorkspaceRepresentationSchema(wantRepresentation)
	if stats.Representation != expected.WorkspaceRepresentation ||
		stats.RepresentationSchema != wantSchema ||
		stats.RepresentationSHA256 != expected.RepresentationSHA256 {
		return fmt.Errorf("existing prediction %s workspace_index representation identity does not match run", instanceID)
	}
	if stats.InvocationDedup != expected.CodeSearchInvocationDedup {
		return fmt.Errorf("existing prediction %s workspace_index invocation_dedup does not match run", instanceID)
	}
	if stats.PreloadInjected != expected.WorkspacePreload {
		return fmt.Errorf("existing prediction %s workspace_index preload identity does not match run", instanceID)
	}
	if !stats.PreloadInjected && (stats.PreloadedDocuments != 0 || stats.PreloadedChars != 0) {
		return fmt.Errorf("existing prediction %s workspace_index has preload totals without preload", instanceID)
	}
	for _, digest := range []string{
		stats.EligibleFileSetSHA256,
		stats.EligibleContentSHA256,
		stats.IndexedFileSetSHA256,
		stats.DocumentSetSHA256,
	} {
		if !isHexIdentifier(digest, 64) {
			return fmt.Errorf("existing prediction %s workspace_index has invalid corpus digest", instanceID)
		}
	}
	if stats.DurationMS < 0 || math.IsNaN(stats.FileCoverage) || math.IsInf(stats.FileCoverage, 0) ||
		stats.FileCoverage < 0 || stats.FileCoverage > 1 ||
		math.IsNaN(stats.DuplicateDocumentRate) || math.IsInf(stats.DuplicateDocumentRate, 0) ||
		stats.DuplicateDocumentRate < 0 || stats.DuplicateDocumentRate > 1 ||
		strings.TrimSpace(stats.RetrievalMode) == "" {
		return fmt.Errorf("existing prediction %s workspace_index has invalid duration, rate, or retrieval mode", instanceID)
	}
	for _, value := range []int{
		stats.Documents,
		stats.EligibleFiles,
		stats.IndexedFiles,
		stats.FallbackDocuments,
		stats.ContentChars,
		stats.EmbeddingTextChars,
		stats.DuplicateDocuments,
		stats.PreloadedDocuments,
		stats.PreloadedChars,
	} {
		if value < 0 {
			return fmt.Errorf("existing prediction %s workspace_index has negative metric", instanceID)
		}
	}
	if expected.EmbeddingConfigSHA256 == "" {
		if result.Embedding != nil || result.EmbeddingCache != nil {
			return fmt.Errorf("existing prediction %s has embedding telemetry without embedding config", instanceID)
		}
		return nil
	}
	if result.Embedding == nil {
		return fmt.Errorf("existing prediction %s is missing configured embedding telemetry", instanceID)
	}
	if err := validateEmbeddingMetrics(instanceID, *result.Embedding); err != nil {
		return err
	}
	if result.EmbeddingCache != nil {
		return validateEmbeddingCacheMetrics(instanceID, *result.EmbeddingCache)
	}
	return nil
}

func validateEmbeddingMetrics(instanceID string, metrics embeddingconfig.Metrics) error {
	values := []int64{
		metrics.Requests,
		metrics.BatchRequests,
		metrics.Inputs,
		metrics.Errors,
		metrics.PromptTokens,
		metrics.TotalTokens,
		metrics.DurationMS,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("existing prediction %s has negative embedding metric", instanceID)
		}
	}
	if metrics.BatchRequests > metrics.Requests || metrics.Errors > metrics.Requests {
		return fmt.Errorf("existing prediction %s has inconsistent embedding request totals", instanceID)
	}
	return nil
}

func validateEmbeddingCacheMetrics(instanceID string, metrics embeddingcache.Metrics) error {
	values := []int64{
		metrics.Requests,
		metrics.BatchRequests,
		metrics.Inputs,
		metrics.Hits,
		metrics.Misses,
		metrics.Writes,
		metrics.Corruptions,
		metrics.Errors,
		metrics.BytesRead,
		metrics.BytesWritten,
		metrics.ReadDurationMS,
		metrics.WriteDurationMS,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("existing prediction %s has negative embedding cache metric", instanceID)
		}
	}
	if metrics.BatchRequests > metrics.Requests {
		return fmt.Errorf("existing prediction %s has inconsistent embedding cache request totals", instanceID)
	}
	return nil
}

func validateToolLoopWarningTelemetry(instanceID string, result executor.CaseResult) error {
	count := result.ToolLoopWarningCount
	calls := result.ToolLoopWarningLLMCalls
	if count < 0 {
		return fmt.Errorf("existing prediction %s has negative tool_loop_warning_count %d", instanceID, count)
	}
	if len(calls) != count {
		return fmt.Errorf(
			"existing prediction %s has %d tool_loop_warning_llm_calls, want count %d",
			instanceID,
			len(calls),
			count,
		)
	}
	if !result.Info.ToolLoopWarning && count != 0 {
		return fmt.Errorf(
			"existing prediction %s has tool-loop warning telemetry with tool_loop_warning=false",
			instanceID,
		)
	}
	if count == 0 {
		if result.FirstToolLoopWarningLLMCall != nil {
			return fmt.Errorf("existing prediction %s has first_tool_loop_warning_llm_call without a warning", instanceID)
		}
		return nil
	}
	if result.FirstToolLoopWarningLLMCall == nil || *result.FirstToolLoopWarningLLMCall != calls[0] {
		return fmt.Errorf("existing prediction %s has inconsistent first_tool_loop_warning_llm_call", instanceID)
	}
	previous := 0
	for _, call := range calls {
		if call <= previous {
			return fmt.Errorf("existing prediction %s tool_loop_warning_llm_calls are not strictly increasing", instanceID)
		}
		if call > result.LLMCalls {
			return fmt.Errorf(
				"existing prediction %s has tool-loop warning at LLM call %d beyond llm_calls=%d",
				instanceID,
				call,
				result.LLMCalls,
			)
		}
		previous = call
	}
	return nil
}

func expectedAuxiliaryImages(
	instanceID string,
	images map[string]sweenv.ImageIdentity,
	testbed sweenv.ImageIdentity,
) (map[string]sweenv.ImageIdentity, error) {
	expected := map[string]sweenv.ImageIdentity{}
	if strings.HasPrefix(instanceID, "psf__requests-") {
		httpbin, ok := images[offlineHTTPBinImageReference]
		if !ok {
			return nil, fmt.Errorf("resolved Docker images do not contain the offline httpbin image")
		}
		expected["httpbin"] = httpbin
	}
	switch instanceID {
	case "psf__requests-2317", "psf__requests-2931", "psf__requests-5414", "psf__requests-6028":
		expected["network-helper"] = testbed
	}
	return expected, nil
}

func persistPredictions(path string, predictions map[string]contract.Prediction) error {
	if len(predictions) > 0 {
		return artifact.WriteJSON(path, predictions)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove empty predictions boundary: %w", err)
	}
	return nil
}

func writeCaseBundle(output string, result *executor.CaseResult, writeJSON func(string, any) error) error {
	caseDir := filepath.Join(output, result.InstanceID)
	responsesPath := filepath.Join(caseDir, result.InstanceID+".responses.json")
	result.ResponseCount = len(result.Responses)
	result.ResponsesSHA256 = ""
	responsesErr := writeJSON(responsesPath, result.Responses)
	if responsesErr != nil {
		markArtifactError(result, responsesErr)
	} else {
		result.ResponsesSHA256, responsesErr = fileSHA256(responsesPath)
		if responsesErr != nil {
			markArtifactError(result, responsesErr)
		}
	}
	resultPath := filepath.Join(caseDir, result.InstanceID+".native.json")
	resultErr := writeJSON(resultPath, result)
	if resultErr != nil {
		markArtifactError(result, resultErr)
	}
	return errors.Join(responsesErr, resultErr)
}

func loadExistingCaseBundle(output, instanceID string, prediction contract.Prediction) (executor.CaseResult, error) {
	caseDir := filepath.Join(output, instanceID)
	resultPath := filepath.Join(caseDir, instanceID+".native.json")
	result, err := readExistingCaseResult(resultPath, instanceID)
	if err != nil {
		return result, fmt.Errorf("validate existing prediction %s result artifact: %w", instanceID, err)
	}
	var responses []*model.Response
	responsesPath := filepath.Join(caseDir, instanceID+".responses.json")
	if err := artifact.ReadJSONFile(responsesPath, &responses); err != nil {
		return result, fmt.Errorf("validate existing prediction %s response artifact: %w", instanceID, err)
	}
	if len(responses) != result.ResponseCount {
		return result, fmt.Errorf(
			"existing prediction %s response count %d does not match result artifact %d",
			instanceID,
			len(responses),
			result.ResponseCount,
		)
	}
	responsesSHA256, err := fileSHA256(responsesPath)
	if err != nil {
		return result, fmt.Errorf("hash existing prediction %s response artifact: %w", instanceID, err)
	}
	if responsesSHA256 != result.ResponsesSHA256 {
		return result, fmt.Errorf(
			"existing prediction %s response SHA-256 %q does not match result artifact %q",
			instanceID,
			responsesSHA256,
			result.ResponsesSHA256,
		)
	}
	if result.InstanceID != instanceID {
		return result, fmt.Errorf("existing prediction %s has result instance_id %q", instanceID, result.InstanceID)
	}
	if result.Info.ExitStatus == "" {
		return result, fmt.Errorf("existing prediction %s has empty exit status", instanceID)
	}
	if result.ModelPatch != prediction.ModelPatch {
		return result, fmt.Errorf("existing prediction %s patch does not match result artifact", instanceID)
	}
	result.Responses = responses
	return result, nil
}

func readExistingCaseResult(path, instanceID string) (executor.CaseResult, error) {
	var result executor.CaseResult
	payload, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, err
	}
	if result.Info.CodeSearch {
		var envelope struct {
			Info map[string]json.RawMessage `json:"info"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return result, err
		}
		for _, field := range []string{
			"code_search",
			"workspace_preload",
			"workspace_representation",
			"workspace_representation_sha256",
		} {
			value, ok := envelope.Info[field]
			if !ok || strings.TrimSpace(string(value)) == "null" {
				return result, fmt.Errorf(
					"workspace retrieval result %s is missing required info field %q",
					instanceID,
					field,
				)
			}
		}
	}
	if !result.Info.ToolLoopWarning {
		return result, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return result, err
	}
	for _, field := range []string{
		"tool_loop_warning_count",
		"first_tool_loop_warning_llm_call",
		"tool_loop_warning_llm_calls",
	} {
		value, ok := fields[field]
		if !ok {
			return result, fmt.Errorf(
				"tool-loop warning enabled result %s is missing required field %q",
				instanceID,
				field,
			)
		}
		if field != "first_tool_loop_warning_llm_call" && strings.TrimSpace(string(value)) == "null" {
			return result, fmt.Errorf(
				"tool-loop warning enabled result %s has null required field %q",
				instanceID,
				field,
			)
		}
	}
	return result, nil
}

func validatePersistedPredictions(path string, want map[string]contract.Prediction) error {
	if len(want) == 0 {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("validate empty persisted predictions: %w", err)
		}
		return fmt.Errorf("persisted predictions exists although in-memory predictions are empty")
	}
	got, err := artifact.ReadPredictions(path)
	if err != nil {
		return fmt.Errorf("validate persisted predictions: %w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("persisted predictions count %d does not match in-memory count %d", len(got), len(want))
	}
	for id, expected := range want {
		actual, ok := got[id]
		if !ok {
			return fmt.Errorf("persisted predictions missing %q", id)
		}
		if actual.InstanceID != expected.InstanceID || actual.ModelNameOrPath != expected.ModelNameOrPath || actual.ModelPatch != expected.ModelPatch {
			return fmt.Errorf("persisted prediction %q does not match in-memory value", id)
		}
	}
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

func validateArtifactName(kind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("-%s is required", strings.ReplaceAll(kind, " ", "-"))
	}
	if len(value) > 200 || !artifactNamePattern.MatchString(value) {
		return fmt.Errorf("%s %q must contain only letters, digits, dot, underscore, or hyphen", kind, value)
	}
	return nil
}

func markArtifactError(result *executor.CaseResult, err error) {
	result.Info.ExitStatus = "ArtifactError"
	result.Info.Error = err.Error()
	result.Info.ErrorCategory = protocol.ErrorCategoryArtifact
	result.Info.Retryable = true
}
