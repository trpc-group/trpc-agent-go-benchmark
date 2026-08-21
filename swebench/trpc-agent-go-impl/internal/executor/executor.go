//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package executor runs one SWE-Bench case through the tRPC-Agent-Go runner lifecycle.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/model"
	tagrunner "trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const offlineHTTPBinImageReference = "docker.io/kennethreitz/httpbin:latest"

// CaseInfo records the minimal status needed for resume and result analysis.
type CaseInfo struct {
	RunID                     string             `json:"run_id,omitempty"`
	ObservationCodec          string             `json:"observation_codec,omitempty"`
	SourceRevision            string             `json:"source_revision,omitempty"`
	SourceModified            bool               `json:"source_modified,omitempty"`
	BinarySHA256              string             `json:"binary_sha256,omitempty"`
	ModelConfigSHA256         string             `json:"model_config_sha256,omitempty"`
	EnvironmentConfigSHA256   string             `json:"environment_config_sha256,omitempty"`
	CasesSHA256               string             `json:"cases_sha256,omitempty"`
	CommandTimeout            string             `json:"command_timeout,omitempty"`
	CaseTimeout               string             `json:"case_timeout,omitempty"`
	SelectedInstancesSHA256   string             `json:"selected_instances_sha256,omitempty"`
	CleanRoom                 bool               `json:"clean_room"`
	ToolLoopWarning           bool               `json:"tool_loop_warning"`
	CodeSearch                bool               `json:"code_search,omitempty"`
	CodeSearchToolOrder       string             `json:"code_search_tool_order,omitempty"`
	CodeSearchInvocationDedup string             `json:"code_search_invocation_dedup,omitempty"`
	WorkspacePreload          *bool              `json:"workspace_preload,omitempty"`
	WorkspaceRepresentation   string             `json:"workspace_representation,omitempty"`
	RepresentationSHA256      string             `json:"workspace_representation_sha256,omitempty"`
	EmbeddingConfigSHA256     string             `json:"embedding_config_sha256,omitempty"`
	CleanRoomPolicySHA256     string             `json:"clean_room_policy_sha256,omitempty"`
	OfflineAssetsSHA256       string             `json:"offline_assets_sha256,omitempty"`
	ImageSetSHA256            string             `json:"image_set_sha256,omitempty"`
	Repo                      string             `json:"repo,omitempty"`
	BaseCommit                string             `json:"base_commit,omitempty"`
	VerifiedBaseCommit        string             `json:"verified_base_commit,omitempty"`
	EnvironmentProvenance     *sweenv.Provenance `json:"environment_provenance,omitempty"`
	Workers                   int                `json:"workers"`
	ExitStatus                string             `json:"exit_status"`
	Error                     string             `json:"error,omitempty"`
	ErrorCategory             string             `json:"error_category,omitempty"`
	Retryable                 bool               `json:"retryable,omitempty"`
}

// CaseResult is the framework-native result artifact for one instance.
type CaseResult struct {
	InstanceID                  string                         `json:"instance_id"`
	Info                        CaseInfo                       `json:"info"`
	ModelPatch                  string                         `json:"model_patch"`
	DurationMS                  int64                          `json:"duration_ms"`
	LLMCalls                    int                            `json:"llm_calls"`
	ToolCalls                   int                            `json:"tool_calls"`
	ToolLoopWarningCount        int                            `json:"tool_loop_warning_count"`
	FirstToolLoopWarningLLMCall *int                           `json:"first_tool_loop_warning_llm_call"`
	ToolLoopWarningLLMCalls     []int                          `json:"tool_loop_warning_llm_calls"`
	CodeSearchCalls             int                            `json:"code_search_calls,omitempty"`
	CodeSearchErrors            int                            `json:"code_search_errors,omitempty"`
	CodeSearchResultBytes       int                            `json:"code_search_result_bytes,omitempty"`
	CodeSearchObservationBytes  int                            `json:"code_search_observation_bytes,omitempty"`
	CodeSearchRawResults        []json.RawMessage              `json:"code_search_raw_results,omitempty"`
	RetrievalTrace              []tagagent.RetrievalTraceEntry `json:"retrieval_trace,omitempty"`
	WorkspaceIndex              *tagagent.WorkspaceIndexStats  `json:"workspace_index,omitempty"`
	Embedding                   *embeddingconfig.Metrics       `json:"embedding,omitempty"`
	EmbeddingCache              *embeddingcache.Metrics        `json:"embedding_cache,omitempty"`
	Usage                       model.Usage                    `json:"usage"`
	Events                      []*event.Event                 `json:"events,omitempty"`
	ResponseCount               int                            `json:"response_count"`
	ResponsesSHA256             string                         `json:"responses_sha256"`
	Responses                   []*model.Response              `json:"-"`
}

// IsRetryableCleanRoomPreStartFailure reports whether the case failed while
// starting its clean-room environment, before the success-only base and image
// provenance attestations or any model/tool activity could exist.
func (r CaseResult) IsRetryableCleanRoomPreStartFailure() bool {
	return r.Info.CleanRoom &&
		r.Info.ExitStatus == "Error" &&
		r.Info.ErrorCategory == protocol.ErrorCategoryEnvironment &&
		r.Info.Retryable &&
		strings.TrimSpace(r.Info.Error) != "" &&
		r.Info.VerifiedBaseCommit == "" &&
		r.Info.EnvironmentProvenance == nil &&
		r.ModelPatch == "" &&
		r.LLMCalls == 0 &&
		r.ToolCalls == 0 &&
		r.ToolLoopWarningCount == 0 &&
		r.FirstToolLoopWarningLLMCall == nil &&
		len(r.ToolLoopWarningLLMCalls) == 0 &&
		r.ResponseCount == 0 &&
		len(r.Responses) == 0 &&
		len(r.Events) == 0 &&
		isZeroUsage(r.Usage)
}

func isZeroUsage(usage model.Usage) bool {
	return usage == (model.Usage{})
}

// Executor owns the per-case model and environment lifecycle.
type Executor struct {
	Factory                 sweenv.CaseFactory
	ModelConfig             modelconfig.EnvConfig
	ObservationCodec        observation.ObservationCodec
	RunID                   string
	SourceRevision          string
	SourceModified          bool
	BinarySHA256            string
	ModelConfigSHA256       string
	EnvironmentConfigSHA256 string
	CasesSHA256             string
	CommandTimeout          time.Duration
	CaseTimeout             time.Duration
	SelectedInstancesSHA256 string
	CleanRoom               bool
	ToolLoopWarning         bool
	EnableCodeSearch        bool
	EnableWorkspacePreload  bool
	WorkspaceRepresentation tagagent.WorkspaceRepresentation
	EmbeddingConfig         *embeddingconfig.Config
	EmbeddingConfigSHA256   string
	EmbeddingCache          *embeddingcache.Store
	CleanRoomPolicySHA256   string
	OfflineAssetsSHA256     string
	ImageSetSHA256          string
	DockerImages            map[string]sweenv.ImageIdentity
	Workers                 int
	ModelFactory            func(modelconfig.EnvConfig) model.Model
}

// Execute runs one case through llmagent.New, runner.NewRunner, and runner.Run,
// then drains the complete event stream.
func (e Executor) Execute(ctx context.Context, c contract.Case) (result CaseResult) {
	started := time.Now()
	defer func() { scrubCaseResultSensitive(&result, e.EmbeddingConfig) }()
	workspaceRepresentation := e.WorkspaceRepresentation
	if parsed, err := tagagent.ParseWorkspaceRepresentation(string(workspaceRepresentation)); err == nil {
		workspaceRepresentation = parsed
	}
	result.InstanceID = c.InstanceID
	// New artifacts always encode an explicit array, including failures that
	// happen before agent state exists. This keeps warning-on artifacts valid at
	// both the resume and evaluator boundaries while legacy warning-off artifacts
	// remain readable.
	result.ToolLoopWarningLLMCalls = []int{}
	result.Info = CaseInfo{
		RunID:                   e.RunID,
		ObservationCodec:        string(e.ObservationCodec),
		SourceRevision:          e.SourceRevision,
		SourceModified:          e.SourceModified,
		BinarySHA256:            e.BinarySHA256,
		ModelConfigSHA256:       e.ModelConfigSHA256,
		EnvironmentConfigSHA256: e.EnvironmentConfigSHA256,
		CasesSHA256:             e.CasesSHA256,
		CommandTimeout:          e.CommandTimeout.String(),
		CaseTimeout:             e.CaseTimeout.String(),
		SelectedInstancesSHA256: e.SelectedInstancesSHA256,
		CleanRoom:               e.CleanRoom,
		ToolLoopWarning:         e.ToolLoopWarning,
		CodeSearch:              e.EnableCodeSearch,
		EmbeddingConfigSHA256:   e.EmbeddingConfigSHA256,
		CleanRoomPolicySHA256:   e.CleanRoomPolicySHA256,
		OfflineAssetsSHA256:     e.OfflineAssetsSHA256,
		ImageSetSHA256:          e.ImageSetSHA256,
		Repo:                    c.Repo,
		BaseCommit:              c.BaseCommit,
		Workers:                 e.Workers,
		ExitStatus:              "Error",
	}
	if e.EnableCodeSearch {
		workspacePreload := e.EnableWorkspacePreload
		result.Info.CodeSearchToolOrder = tagagent.CodeSearchProviderToolOrder
		result.Info.CodeSearchInvocationDedup = tagagent.CodeSearchInvocationDedup
		result.Info.WorkspacePreload = &workspacePreload
		result.Info.WorkspaceRepresentation = string(workspaceRepresentation)
		result.Info.RepresentationSHA256 = tagagent.WorkspaceRepresentationSHA256(workspaceRepresentation)
	}
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	caseTimeout := e.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	startCtx := ctx
	var caseCtx context.Context
	var cancel context.CancelFunc
	if e.CleanRoom {
		// Clean-room setup is part of the bounded case because Git sanitation,
		// asset installation, and local fixtures all run before the model.
		caseCtx, cancel = context.WithTimeout(ctx, caseTimeout)
		startCtx = caseCtx
		defer cancel()
	}

	environment, err := e.Factory.StartCase(startCtx, sweenv.CaseSpec{
		InstanceID: c.InstanceID,
		Repo:       c.Repo,
		BaseCommit: c.BaseCommit,
	})
	if err != nil {
		if e.CleanRoom && errors.Is(caseCtx.Err(), context.DeadlineExceeded) {
			result.Info.ExitStatus = "Timeout"
			result.Info.Error = caseCtx.Err().Error()
			result.Info.ErrorCategory = protocol.ErrorCategoryCaseTimeout
			return result
		}
		result.Info.Error = err.Error()
		result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
		// Preserve the default runner's historical retry behavior. Clean-room
		// setup is stricter: only Docker startup failures explicitly marked by
		// the environment boundary may be retried.
		result.Info.Retryable = !e.CleanRoom || sweenv.IsStartErrorRetryable(err)
		return result
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if closeErr := environment.Close(closeCtx); closeErr != nil && result.Info.Error == "" {
			result.Info.ExitStatus = "Error"
			result.Info.Error = closeErr.Error()
			result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
			result.Info.Retryable = true
		}
	}()
	if e.CleanRoom {
		provider, ok := environment.(sweenv.ProvenanceProvider)
		if !ok {
			result.Info.Error = "clean-room environment did not attest image provenance"
			result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
			// A missing provenance provider is a deterministic contract failure,
			// not a transient Docker startup failure.
			result.Info.Retryable = false
			return result
		}
		provenance := provider.Provenance()
		result.Info.EnvironmentProvenance = &provenance
		if err := validateCleanRoomProvenance(c.InstanceID, *result.Info.EnvironmentProvenance, e.DockerImages); err != nil {
			result.Info.Error = err.Error()
			result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
			// Retrying the same frozen run cannot repair an attestation mismatch.
			result.Info.Retryable = false
			return result
		}
		result.Info.VerifiedBaseCommit = c.BaseCommit
	} else {
		// Preserve the default runner's historical timeout boundary: ordinary
		// Docker startup is not charged to the model/tool case timeout.
		caseCtx, cancel = context.WithTimeout(ctx, caseTimeout)
		defer cancel()
	}

	modelImpl := newModel(e.ModelConfig)
	if e.ModelFactory != nil {
		modelImpl = e.ModelFactory(e.ModelConfig)
	}
	modelImpl = validatedNonStreamingModel{Model: modelImpl}
	state := &tagagent.State{}
	var extraTools []tool.Tool
	var workspaceContext string
	if e.EnableCodeSearch {
		workspaceConfig := &tagagent.WorkspaceSearchConfig{
			SearchMode:     vectorstore.SearchModeKeyword,
			Representation: workspaceRepresentation,
			RepositoryName: c.Repo,
		}
		if e.EmbeddingConfig != nil {
			searchMode, searchModeErr := e.EmbeddingConfig.SearchMode()
			if searchModeErr != nil {
				result.Info.Error = searchModeErr.Error()
				result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
				return result
			}
			metered := embeddingconfig.NewMetered(e.EmbeddingConfig.NewEmbedder())
			defer func() {
				metrics := metered.Snapshot()
				result.Embedding = &metrics
			}()
			var workspaceEmbedder embedder.Embedder = metered
			if e.EmbeddingConfig.Cache.Enabled {
				if e.EmbeddingCache == nil {
					result.Info.Error = "embedding cache is enabled but no cache store is configured"
					result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
					return result
				}
				cached, cacheErr := embeddingcache.New(e.EmbeddingCache, metered)
				if cacheErr != nil {
					result.Info.Error = cacheErr.Error()
					result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
					return result
				}
				defer func() {
					metrics := cached.Snapshot()
					result.EmbeddingCache = &metrics
				}()
				workspaceEmbedder = cached
			}
			workspaceEmbedder = embeddingconfig.NewSanitized(
				workspaceEmbedder,
				e.EmbeddingConfig.ScrubSensitiveText,
			)
			workspaceConfig = &tagagent.WorkspaceSearchConfig{
				Embedder:       workspaceEmbedder,
				SearchMode:     searchMode,
				BatchSize:      e.EmbeddingConfig.Embedding.BatchSize,
				DocConcurrency: e.EmbeddingConfig.Embedding.Concurrency,
				MaxResults:     e.EmbeddingConfig.Retrieval.MaxResults,
				MaxChars:       e.EmbeddingConfig.Retrieval.MaxChars,
				Representation: workspaceRepresentation,
				RepositoryName: c.Repo,
			}
		}
		workspaceIndex, indexStats, searchErr := tagagent.NewWorkspaceIndexFromEnvironment(
			caseCtx,
			environment,
			c.InstanceID,
			workspaceConfig,
		)
		if searchErr != nil {
			result.Info.Error = searchErr.Error()
			result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
			return result
		}
		defer func() { _ = workspaceIndex.Close() }()
		result.WorkspaceIndex = &indexStats
		extraTools = append(extraTools, workspaceIndex.Tool())
		if e.EnableWorkspacePreload {
			preloaded, preloadErr := workspaceIndex.BuildContext(caseCtx, c.ProblemStatement)
			if preloadErr != nil {
				result.Info.Error = fmt.Sprintf("preload workspace context: %v", preloadErr)
				result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
				return result
			}
			indexStats.PreloadedDocuments = preloaded.Documents
			indexStats.PreloadedChars = preloaded.Chars
			indexStats.PreloadInjected = true
			workspaceContext = preloaded.Text
		}
	}
	agentImpl := tagagent.New(
		modelImpl,
		environment,
		e.ObservationCodec,
		generationConfig(e.ModelConfig),
		state,
		tagagent.Config{CleanRoom: e.CleanRoom, ToolLoopWarning: e.ToolLoopWarning},
		extraTools...,
	)
	run := tagrunner.NewRunner("swebench", agentImpl)
	defer run.Close()

	taskPrompt := protocol.PromptForTaskForMode(c.ProblemStatement, e.CleanRoom)
	if e.EnableCodeSearch {
		taskPrompt = protocol.PromptForTaskWithCodeSearchForMode(c.ProblemStatement, e.CleanRoom)
	}
	if workspaceContext != "" {
		taskPrompt += "\n\n<workspace_context>\n" + workspaceContext + "\n</workspace_context>"
	}
	events, err := run.Run(
		caseCtx,
		c.InstanceID,
		c.InstanceID,
		model.NewUserMessage(taskPrompt),
		agent.WithStream(false),
		agent.WithModelRequestExtraFields(map[string]any{"parallel_tool_calls": true}),
	)
	if err != nil {
		applySnapshot(&result, state.Snapshot())
		if errors.Is(caseCtx.Err(), context.DeadlineExceeded) {
			result.Info.ExitStatus = "Timeout"
			result.Info.Error = caseCtx.Err().Error()
			result.Info.ErrorCategory = protocol.ErrorCategoryCaseTimeout
			return result
		}
		result.Info.Error = err.Error()
		result.Info.ErrorCategory, result.Info.Retryable = protocol.ClassifyError(err.Error())
		return result
	}
	var terminalErrorType string
	for evt := range events {
		if evt == nil {
			continue
		}
		result.Events = append(result.Events, evt)
		if evt.Response != nil && evt.Response.Error != nil {
			result.Info.Error = evt.Response.Error.Message
			terminalErrorType = evt.Response.Error.Type
		}
	}

	snapshot := state.Snapshot()
	applySnapshot(&result, snapshot)
	switch {
	case caseCtx.Err() == context.DeadlineExceeded:
		result.Info.ExitStatus = "Timeout"
		result.Info.Error = caseCtx.Err().Error()
		result.Info.ErrorCategory = protocol.ErrorCategoryCaseTimeout
	case snapshot.Submitted:
		result.Info.ExitStatus = "Submitted"
		result.Info.Error = ""
	case terminalErrorType == agent.ErrorTypeStopAgentError &&
		result.Info.Error == fmt.Sprintf("max LLM calls (%d) exceeded", tagagent.MaxLLMCalls):
		result.Info.ExitStatus = "LimitsExceeded"
		result.Info.ErrorCategory = protocol.ErrorCategoryAgentLimit
		result.Info.Retryable = false
	case result.Info.Error != "":
		result.Info.ExitStatus = "Error"
		result.Info.ErrorCategory, result.Info.Retryable = protocol.ClassifyError(result.Info.Error)
	default:
		result.Info.ExitStatus = "Error"
		result.Info.Error = "agent stopped without submission or terminal error"
		result.Info.ErrorCategory = protocol.ErrorCategoryAgent
	}
	return result
}

func scrubCaseResultSensitive(result *CaseResult, config *embeddingconfig.Config) {
	if result == nil || config == nil {
		return
	}
	result.Info.Error = config.ScrubSensitiveText(result.Info.Error)
	result.CodeSearchResultBytes = 0
	for index, raw := range result.CodeSearchRawResults {
		scrubbed, err := scrubJSONStrings(raw, config.ScrubSensitiveText)
		if err != nil {
			scrubbed = []byte(config.ScrubSensitiveText(string(raw)))
		}
		result.CodeSearchRawResults[index] = append(json.RawMessage(nil), scrubbed...)
		result.CodeSearchResultBytes += len(scrubbed)
		if index < len(result.RetrievalTrace) {
			result.RetrievalTrace[index].ResultBytes = len(scrubbed)
			result.RetrievalTrace[index].ResultSHA256 = tagagent.DigestBytes(scrubbed)
		}
	}
	for index := range result.RetrievalTrace {
		entry := &result.RetrievalTrace[index]
		entry.Error = config.ScrubSensitiveText(entry.Error)
		if entry.Error != "" {
			entry.ErrorSHA256 = tagagent.DigestBytes([]byte(entry.Error))
		}
	}
	for _, value := range []any{&result.Responses, &result.Events} {
		data, err := json.Marshal(value)
		if err != nil {
			continue
		}
		scrubbed, err := scrubJSONStrings(data, config.ScrubSensitiveText)
		if err != nil {
			continue
		}
		_ = json.Unmarshal(scrubbed, value)
	}
}

func scrubJSONStrings(data []byte, scrub func(string) string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value = scrubJSONValue(value, scrub)
	return json.Marshal(value)
}

func scrubJSONValue(value any, scrub func(string) string) any {
	switch typed := value.(type) {
	case string:
		return scrub(typed)
	case []any:
		for index := range typed {
			typed[index] = scrubJSONValue(typed[index], scrub)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = scrubJSONValue(item, scrub)
		}
		return typed
	default:
		return value
	}
}

func validateCleanRoomProvenance(
	instanceID string,
	provenance sweenv.Provenance,
	images map[string]sweenv.ImageIdentity,
) error {
	testbed, ok := images[sweenv.ImageForInstance(instanceID)]
	if !ok || provenance.Testbed != testbed {
		return fmt.Errorf("clean-room environment did not attest the resolved testbed image")
	}
	expected := map[string]sweenv.ImageIdentity{}
	if strings.HasPrefix(instanceID, "psf__requests-") {
		httpbin, ok := images[offlineHTTPBinImageReference]
		if !ok {
			return fmt.Errorf("resolved Docker images do not contain the offline httpbin image")
		}
		expected["httpbin"] = httpbin
	}
	switch instanceID {
	case "psf__requests-2317", "psf__requests-2931", "psf__requests-5414", "psf__requests-6028":
		expected["network-helper"] = testbed
	}
	if len(provenance.AuxiliaryImages) != len(expected) {
		return fmt.Errorf("clean-room environment attested unexpected auxiliary image roles")
	}
	for role, want := range expected {
		if actual, ok := provenance.AuxiliaryImages[role]; !ok || actual != want {
			return fmt.Errorf("clean-room environment attested unexpected %s image provenance", role)
		}
	}
	return nil
}

func applySnapshot(result *CaseResult, snapshot tagagent.Snapshot) {
	result.ModelPatch = snapshot.Submission
	result.LLMCalls = snapshot.LLMCalls
	result.ToolCalls = snapshot.ToolCalls
	result.ToolLoopWarningCount = snapshot.ToolLoopWarningCount
	result.FirstToolLoopWarningLLMCall = nil
	if snapshot.ToolLoopWarningCount > 0 {
		firstCall := snapshot.FirstToolLoopWarningLLMCall
		result.FirstToolLoopWarningLLMCall = &firstCall
	}
	result.ToolLoopWarningLLMCalls = append([]int{}, snapshot.ToolLoopWarningLLMCalls...)
	result.CodeSearchCalls = snapshot.CodeSearchCalls
	result.CodeSearchErrors = snapshot.CodeSearchErrors
	result.CodeSearchResultBytes = snapshot.CodeSearchResultBytes
	result.CodeSearchObservationBytes = snapshot.CodeSearchObservationBytes
	result.CodeSearchRawResults = append([]json.RawMessage(nil), snapshot.CodeSearchRawResults...)
	result.RetrievalTrace = append([]tagagent.RetrievalTraceEntry(nil), snapshot.RetrievalTrace...)
	result.Usage = snapshot.Usage
	result.Responses = snapshot.Responses
}

// Validate checks the executor's required dependencies before workers start.
func (e Executor) Validate() error {
	if e.Factory == nil {
		return fmt.Errorf("executor factory is required")
	}
	if e.ModelFactory == nil && e.ModelConfig["MODEL_NAME"] == "" {
		return fmt.Errorf("MODEL_NAME is required")
	}
	if e.Workers <= 0 {
		return fmt.Errorf("executor workers must be positive")
	}
	representation, err := tagagent.ParseWorkspaceRepresentation(string(e.WorkspaceRepresentation))
	if err != nil {
		return err
	}
	if !e.EnableCodeSearch && representation != tagagent.WorkspaceRepresentationCurrentFixed {
		return fmt.Errorf("workspace representation %q requires code search", representation)
	}
	if e.EnableWorkspacePreload && !e.EnableCodeSearch {
		return fmt.Errorf("workspace preload requires code search")
	}
	if e.EmbeddingConfig != nil {
		if !e.EnableCodeSearch {
			return fmt.Errorf("embedding config requires code search")
		}
		if err := e.EmbeddingConfig.Validate(); err != nil {
			return err
		}
		if e.EmbeddingConfig.Cache.Enabled && e.EmbeddingCache == nil {
			return fmt.Errorf("embedding cache is enabled but no cache store is configured")
		}
	} else if e.EmbeddingCache != nil {
		return fmt.Errorf("embedding cache store requires embedding config")
	}
	return nil
}
