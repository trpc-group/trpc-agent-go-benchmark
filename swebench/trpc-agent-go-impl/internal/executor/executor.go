//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package executor runs one SWE-Bench case through the TAG runner lifecycle.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/minicompat"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/model"
	tagrunner "trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// CaseInfo records the minimal status needed for resume and result analysis.
type CaseInfo struct {
	ExitStatus    string `json:"exit_status"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
}

// CaseResult is the TAG-native result artifact for one instance.
type CaseResult struct {
	InstanceID                 string                       `json:"instance_id"`
	Info                       CaseInfo                     `json:"info"`
	ModelPatch                 string                       `json:"model_patch,omitempty"`
	DurationMS                 int64                        `json:"duration_ms"`
	LLMCalls                   int                          `json:"llm_calls"`
	ToolCalls                  int                          `json:"tool_calls"`
	CodeSearchCalls            int                          `json:"code_search_calls,omitempty"`
	CodeSearchResultBytes      int                          `json:"code_search_result_bytes,omitempty"`
	CodeSearchObservationBytes int                          `json:"code_search_observation_bytes,omitempty"`
	CodeSearchRawResults       []json.RawMessage            `json:"code_search_raw_results,omitempty"`
	WorkspaceIndex             tagagent.WorkspaceIndexStats `json:"workspace_index,omitempty"`
	Embedding                  *embeddingconfig.Metrics     `json:"embedding,omitempty"`
	EmbeddingCache             *embeddingcache.Metrics      `json:"embedding_cache,omitempty"`
	Usage                      model.Usage                  `json:"usage"`
	Events                     []*event.Event               `json:"events,omitempty"`
	Responses                  []*model.Response            `json:"-"`
}

// Executor owns the per-case TAG model and environment lifecycle.
type Executor struct {
	Factory                 sweenv.Factory
	ModelConfig             modelconfig.EnvConfig
	ObservationCodec        minicompat.ObservationCodec
	CaseTimeout             time.Duration
	ModelFactory            func(modelconfig.EnvConfig) model.Model
	EnableCodeSearch        bool
	EnableWorkspacePreload  bool
	WorkspaceRepresentation tagagent.WorkspaceRepresentation
	EmbeddingConfig         *embeddingconfig.Config
	EmbeddingCache          *embeddingcache.Store
}

// Execute runs one case and drains the complete TAG event stream.
func (e Executor) Execute(ctx context.Context, c contract.Case) (result CaseResult) {
	started := time.Now()
	result.InstanceID = c.InstanceID
	result.Info.ExitStatus = "Error"
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	environment, err := e.Factory.Start(ctx, c.InstanceID)
	if err != nil {
		result.Info.Error = err.Error()
		result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
		result.Info.Retryable = true
		return result
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if closeErr := environment.Close(closeCtx); closeErr != nil && result.Info.Error == "" {
			result.Info.Error = closeErr.Error()
			result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
			result.Info.Retryable = true
		}
	}()

	caseTimeout := e.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	caseCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()

	modelImpl := newModel(e.ModelConfig)
	if e.ModelFactory != nil {
		modelImpl = e.ModelFactory(e.ModelConfig)
	}
	state := &tagagent.State{}
	var extraTools []tool.Tool
	var workspaceContext string
	if e.EnableCodeSearch {
		workspaceSearchConfig := &tagagent.WorkspaceSearchConfig{
			SearchMode:     vectorstore.SearchModeKeyword,
			Representation: e.WorkspaceRepresentation,
			RepositoryName: c.Repo,
		}
		if e.EmbeddingConfig != nil {
			searchMode, searchModeErr := e.EmbeddingConfig.SearchMode()
			if searchModeErr != nil {
				result.Info.Error = searchModeErr.Error()
				result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
				return result
			}
			metered := embeddingconfig.NewMetered(e.EmbeddingConfig.NewEmbedder())
			defer func() {
				snapshot := metered.Snapshot()
				result.Embedding = &snapshot
			}()
			var workspaceEmbedder embedder.Embedder = metered
			if e.EmbeddingConfig.Cache.Enabled {
				if e.EmbeddingCache == nil {
					result.Info.Error = "embedding cache is enabled but no cache store is configured"
					result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
					return result
				}
				cached, cacheErr := embeddingcache.New(e.EmbeddingCache, metered)
				if cacheErr != nil {
					result.Info.Error = cacheErr.Error()
					result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
					return result
				}
				defer func() {
					snapshot := cached.Snapshot()
					result.EmbeddingCache = &snapshot
				}()
				workspaceEmbedder = cached
			}
			workspaceSearchConfig = &tagagent.WorkspaceSearchConfig{
				Embedder:       workspaceEmbedder,
				SearchMode:     searchMode,
				BatchSize:      e.EmbeddingConfig.Embedding.BatchSize,
				DocConcurrency: e.EmbeddingConfig.Embedding.Concurrency,
				MaxResults:     e.EmbeddingConfig.Retrieval.MaxResults,
				MaxChars:       e.EmbeddingConfig.Retrieval.MaxChars,
				Representation: e.WorkspaceRepresentation,
				RepositoryName: c.Repo,
			}
		}
		workspaceIndex, indexStats, searchErr := tagagent.NewWorkspaceIndexFromEnvironment(
			caseCtx, environment, c.InstanceID, workspaceSearchConfig,
		)
		if searchErr != nil {
			result.Info.Error = searchErr.Error()
			result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
			return result
		}
		defer func() { _ = workspaceIndex.Close() }()
		extraTools = append(extraTools, workspaceIndex.Tool())
		if e.EnableWorkspacePreload {
			preloaded, preloadErr := workspaceIndex.BuildContext(caseCtx, c.ProblemStatement)
			if preloadErr != nil {
				result.Info.Error = fmt.Sprintf("preload workspace context: %v", preloadErr)
				result.Info.ErrorCategory = minicompat.ErrorCategoryEnvironment
				return result
			}
			indexStats.PreloadedDocuments = preloaded.Documents
			indexStats.PreloadedChars = preloaded.Chars
			indexStats.PreloadInjected = true
			workspaceContext = preloaded.Text
		}
		result.WorkspaceIndex = indexStats
	}
	agentImpl := tagagent.New(modelImpl, environment, e.ObservationCodec, generationConfig(e.ModelConfig), state, extraTools...)
	run := tagrunner.NewRunner("tag-swebench", agentImpl)
	defer run.Close()

	taskPrompt := minicompat.PromptForTaskOffline(c.ProblemStatement)
	if e.EnableCodeSearch {
		taskPrompt = minicompat.PromptForTaskWithCodeSearchOffline(c.ProblemStatement)
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
		result.Info.Error = err.Error()
		result.Info.ErrorCategory, result.Info.Retryable = minicompat.ClassifyError(err.Error())
		return result
	}
	for evt := range events {
		if evt == nil {
			continue
		}
		result.Events = append(result.Events, evt)
		if evt.Response != nil && evt.Response.Error != nil {
			result.Info.Error = evt.Response.Error.Message
		}
	}

	snapshot := state.Snapshot()
	result.ModelPatch = snapshot.Submission
	result.LLMCalls = snapshot.LLMCalls
	result.ToolCalls = snapshot.ToolCalls
	result.CodeSearchCalls = snapshot.CodeSearchCalls
	result.CodeSearchResultBytes = snapshot.CodeSearchResultBytes
	result.CodeSearchObservationBytes = snapshot.CodeSearchObservationBytes
	result.CodeSearchRawResults = snapshot.CodeSearchRawResults
	result.Usage = snapshot.Usage
	result.Responses = snapshot.Responses
	switch {
	case snapshot.Submitted:
		result.Info.ExitStatus = "Submitted"
		result.Info.Error = ""
	case caseCtx.Err() == context.DeadlineExceeded:
		result.Info.ExitStatus = "Timeout"
		result.Info.Error = caseCtx.Err().Error()
		result.Info.ErrorCategory = minicompat.ErrorCategoryCaseTimeout
	case result.Info.Error != "":
		result.Info.ExitStatus = "Error"
		result.Info.ErrorCategory, result.Info.Retryable = minicompat.ClassifyError(result.Info.Error)
	default:
		result.Info.ExitStatus = "LimitsExceeded"
		result.Info.ErrorCategory = minicompat.ErrorCategoryAgentLimit
	}
	return result
}

// Validate checks the executor's required dependencies before workers start.
func (e Executor) Validate() error {
	if e.Factory == nil {
		return fmt.Errorf("executor factory is required")
	}
	if e.ModelFactory == nil && e.ModelConfig["MODEL_NAME"] == "" {
		return fmt.Errorf("MODEL_NAME is required")
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
