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
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

var artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const offlineHTTPBinImageReference = "docker.io/kennethreitz/httpbin:latest"

type manifest struct {
	RunID                   string                          `json:"run_id"`
	RunnerType              string                          `json:"runner_type"`
	FrameworkModule         string                          `json:"framework_module"`
	FrameworkVersion        string                          `json:"framework_version"`
	AgentProtocol           string                          `json:"agent_protocol"`
	UpstreamCommit          string                          `json:"upstream_commit"`
	ObservationCodec        string                          `json:"observation_codec"`
	SourceRevision          string                          `json:"source_revision,omitempty"`
	SourceModified          bool                            `json:"source_modified"`
	BinarySHA256            string                          `json:"binary_sha256,omitempty"`
	CasesSHA256             string                          `json:"cases_sha256"`
	ModelConfigSHA256       string                          `json:"model_config_sha256"`
	EnvironmentConfigSHA256 string                          `json:"environment_config_sha256"`
	SelectedInstancesSHA256 string                          `json:"selected_instances_sha256"`
	CleanRoom               bool                            `json:"clean_room"`
	CleanRoomPolicySHA256   string                          `json:"clean_room_policy_sha256,omitempty"`
	OfflineAssets           *sweenv.OfflineAssetIdentity    `json:"offline_assets,omitempty"`
	ImageSetSHA256          string                          `json:"image_set_sha256,omitempty"`
	DockerImages            map[string]sweenv.ImageIdentity `json:"docker_images,omitempty"`
	StartedAt               time.Time                       `json:"started_at"`
	FinishedAt              time.Time                       `json:"finished_at"`
	DurationMS              int64                           `json:"duration_ms"`
	Cases                   string                          `json:"cases"`
	OutputDir               string                          `json:"output_dir"`
	Filter                  string                          `json:"filter,omitempty"`
	CaseCount               int                             `json:"case_count"`
	AttemptedCount          int                             `json:"attempted_count"`
	SkippedExisting         int                             `json:"skipped_existing"`
	CompletedCount          int                             `json:"completed_count"`
	PredictionCount         int                             `json:"prediction_count"`
	Workers                 int                             `json:"workers"`
	RedoExisting            bool                            `json:"redo_existing"`
	Predictions             string                          `json:"predictions"`
	Progress                string                          `json:"progress"`
	ModelConfig             map[string]string               `json:"model_config,omitempty"`
	Environment             string                          `json:"environment_config"`
	CommandTimeout          string                          `json:"command_timeout"`
	CaseTimeout             string                          `json:"case_timeout"`
	ExitStatusCounts        map[string]int                  `json:"exit_status_counts"`
	LLMCalls                int                             `json:"llm_calls"`
	ToolCalls               int                             `json:"tool_calls"`
	Usage                   usageSummary                    `json:"usage"`
	Status                  string                          `json:"status"`
	Notes                   []string                        `json:"notes,omitempty"`
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

// Run executes the tRPC-Agent-Go SWE-Bench runner CLI.
func Run(args []string) error {
	fs := flag.NewFlagSet("trpc-agent-go-impl", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run id")
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases.jsonl")
	modelConfigPath := fs.String("model-config", "", "model config YAML/env path")
	environmentConfigPath := fs.String("environment-config", "config/environments/swebench-testbed.yaml", "environment YAML path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>/raw/native")
	filter := fs.String("filter", "", "optional instance id regexp")
	workers := fs.Int("agent-workers", 1, "parallel agent cases")
	commandTimeout := fs.Duration("command-timeout", time.Minute, "timeout for each bash tool call")
	caseTimeout := fs.Duration("case-timeout", 2*time.Hour, "timeout for each case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	cleanRoom := fs.Bool("clean-room", false, "enable network-none generation and recursive Git sanitation")
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
	resolvedImages, err := factory.ResolveImages(context.Background(), selectedSpecs)
	if err != nil {
		return err
	}
	imageSetSHA256, err := sweenv.ImageSetSHA256(resolvedImages)
	if err != nil {
		return err
	}
	factory.ResolvedImages = resolvedImages
	exec := executor.Executor{
		Factory: factory, ModelConfig: modelCfg, ObservationCodec: codec,
		RunID: *runID, SourceRevision: build.SourceRevision, SourceModified: build.SourceModified,
		BinarySHA256: build.BinarySHA256, ModelConfigSHA256: modelHash,
		EnvironmentConfigSHA256: environmentHash, CasesSHA256: casesHash,
		CommandTimeout: *commandTimeout, CaseTimeout: *caseTimeout,
		SelectedInstancesSHA256: selectedHash,
		CleanRoom:               *cleanRoom, CleanRoomPolicySHA256: cleanRoomPolicySHA256,
		OfflineAssetsSHA256: offlineAssetsIdentity.SHA256, ImageSetSHA256: imageSetSHA256,
		DockerImages: resolvedImages,
		Workers:      *workers,
	}
	if err := exec.Validate(); err != nil {
		return err
	}

	predictionsPath := filepath.Join(*output, "preds.json")
	identity := runIdentity{
		RunID: *runID, ObservationCodec: string(codec), SourceRevision: build.SourceRevision,
		SourceModified: build.SourceModified, BinarySHA256: build.BinarySHA256,
		ModelConfigSHA256: modelHash, EnvironmentConfigSHA256: environmentHash,
		CasesSHA256: casesHash, CommandTimeout: commandTimeout.String(),
		CaseTimeout: caseTimeout.String(), SelectedInstancesSHA256: selectedHash,
		CleanRoom: *cleanRoom, CleanRoomPolicySHA256: cleanRoomPolicySHA256,
		OfflineAssetsSHA256: offlineAssetsIdentity.SHA256, ImageSetSHA256: imageSetSHA256,
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
		progress.Cases[id] = progressCase{
			Status: result.Info.ExitStatus, ErrorCategory: result.Info.ErrorCategory,
			PatchBytes: len(result.ModelPatch), LLMCalls: result.LLMCalls,
			ToolCalls: result.ToolCalls, DurationMS: result.DurationMS,
		}
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
				caseResult := exec.Execute(context.Background(), c)
				bundleErr := writeCaseBundle(*output, &caseResult, artifact.WriteJSON)

				mu.Lock()
				if bundleErr == nil {
					preds[c.InstanceID] = contract.Prediction{
						ModelNameOrPath: "trpc-agent-go/" + modelCfg["MODEL_NAME"],
						InstanceID:      c.InstanceID, ModelPatch: caseResult.ModelPatch,
					}
				}
				results[c.InstanceID] = caseResult
				progress.Cases[c.InstanceID] = progressCase{
					Status: caseResult.Info.ExitStatus, ErrorCategory: caseResult.Info.ErrorCategory,
					PatchBytes: len(caseResult.ModelPatch), LLMCalls: caseResult.LLMCalls,
					ToolCalls: caseResult.ToolCalls, DurationMS: caseResult.DurationMS,
				}
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
	for _, c := range pending {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	if err := validatePersistedPredictions(predictionsPath, preds); err != nil {
		artifactWriteErrors = append(artifactWriteErrors, err)
	}
	artifactWriteErr := errors.Join(artifactWriteErrors...)

	finished := time.Now()
	exitCounts := map[string]int{}
	if missingSkipped := len(skipped) - loadedSkipped; missingSkipped > 0 {
		exitCounts["ExistingPrediction"] = missingSkipped
	}
	var llmCalls, toolCalls int
	var usage usageSummary
	status := "completed"
	if len(skipped) != loadedSkipped || artifactWriteErr != nil {
		status = "completed_with_errors"
	}
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
		RunID: *runID, RunnerType: "trpc-agent-go-native", FrameworkModule: build.FrameworkModule,
		FrameworkVersion: build.FrameworkVersion,
		AgentProtocol:    agentProtocol(codec, *cleanRoom), UpstreamCommit: protocol.UpstreamCommit,
		ObservationCodec: string(codec), SourceRevision: build.SourceRevision,
		SourceModified: build.SourceModified, BinarySHA256: build.BinarySHA256,
		CasesSHA256: casesHash, ModelConfigSHA256: modelHash,
		EnvironmentConfigSHA256: environmentHash, SelectedInstancesSHA256: selectedHash,
		CleanRoom: *cleanRoom, CleanRoomPolicySHA256: cleanRoomPolicySHA256,
		ImageSetSHA256: imageSetSHA256, DockerImages: resolvedImages,
		StartedAt:  started.UTC(),
		FinishedAt: finished.UTC(), DurationMS: finished.Sub(started).Milliseconds(),
		Cases: artifact.AbsPath(*casesPath), OutputDir: artifact.AbsPath(*output), Filter: *filter,
		CaseCount: len(selected), AttemptedCount: len(pending), SkippedExisting: len(skipped),
		CompletedCount:  len(preds),
		PredictionCount: len(preds), Workers: *workers, RedoExisting: *redoExisting,
		Predictions: artifact.AbsPath(predictionsPath), Progress: artifact.AbsPath(progressPath),
		ModelConfig: modelManifestConfig(modelCfg), Environment: artifact.AbsPath(*environmentConfigPath),
		CommandTimeout: commandTimeout.String(), CaseTimeout: caseTimeout.String(),
		ExitStatusCounts: exitCounts, LLMCalls: llmCalls, ToolCalls: toolCalls, Usage: usage,
		Status: status,
		Notes: []string{
			"Each case runs through tRPC-Agent-Go llmagent and runner lifecycles in an independent official SWE-Bench container.",
			"OpenAI SDK retry is configured with nine retries after the initial request; preds.json is the resume boundary.",
		},
	}
	if offlineAssetsIdentity.SHA256 != "" {
		doc.OfflineAssets = &offlineAssetsIdentity
	}
	if *cleanRoom {
		doc.Notes = append(doc.Notes,
			"Clean-room cases use local immutable image IDs, Docker network=none, recursive Git sanitation, and exact base-commit verification before the first model call.",
		)
	}
	manifestPath := filepath.Join(*output, "native-runner-manifest.json")
	if err := artifact.WriteJSON(manifestPath, doc); err != nil {
		return err
	}
	fmt.Printf("selected=%d attempted=%d skipped_existing=%d predictions=%s\nprogress=%s\nmanifest=%s\n",
		len(selected), len(pending), len(skipped), predictionsPath, progressPath, manifestPath)
	if artifactWriteErr != nil {
		return fmt.Errorf("persist run artifacts: %w", artifactWriteErr)
	}
	return nil
}

func agentProtocol(codec observation.ObservationCodec, cleanRoom bool) string {
	base := "mini-swe-agent-v2.1-on-trpc-agent-go"
	if codec != observation.ObservationCodecXML {
		base += "+codec-" + string(codec)
	}
	if cleanRoom {
		base += "+clean-room-v1"
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
			if err := validateResumeResult(c, result, identity); err != nil {
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
	instanceID := c.InstanceID
	if result.InstanceID != instanceID {
		return fmt.Errorf("existing prediction %s has result instance_id %q", instanceID, result.InstanceID)
	}
	info := result.Info
	if info.CleanRoom != expected.CleanRoom {
		return fmt.Errorf("existing prediction %s has clean_room=%t, want %t", instanceID, info.CleanRoom, expected.CleanRoom)
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
			{"verified base commit", info.VerifiedBaseCommit, c.BaseCommit},
		} {
			if check.actual != check.expected {
				return fmt.Errorf("existing prediction %s has %s %q, want %q", instanceID, check.name, check.actual, check.expected)
			}
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
	} else if info.VerifiedBaseCommit != "" || info.EnvironmentProvenance != nil {
		return fmt.Errorf("existing prediction %s carries clean-room case provenance with clean_room=false", instanceID)
	}
	if info.Workers != expected.Workers {
		return fmt.Errorf("existing prediction %s has workers %d, want %d", instanceID, info.Workers, expected.Workers)
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
	var result executor.CaseResult
	resultPath := filepath.Join(caseDir, instanceID+".native.json")
	if err := artifact.ReadJSONFile(resultPath, &result); err != nil {
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
