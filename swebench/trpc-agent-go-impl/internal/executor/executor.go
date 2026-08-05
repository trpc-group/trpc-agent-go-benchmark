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
	"context"
	"errors"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	tagrunner "trpc.group/trpc-go/trpc-agent-go/runner"
)

// CaseInfo records the minimal status needed for resume and result analysis.
type CaseInfo struct {
	RunID                   string `json:"run_id,omitempty"`
	ObservationCodec        string `json:"observation_codec,omitempty"`
	SourceRevision          string `json:"source_revision,omitempty"`
	SourceModified          bool   `json:"source_modified,omitempty"`
	BinarySHA256            string `json:"binary_sha256,omitempty"`
	ModelConfigSHA256       string `json:"model_config_sha256,omitempty"`
	EnvironmentConfigSHA256 string `json:"environment_config_sha256,omitempty"`
	CasesSHA256             string `json:"cases_sha256,omitempty"`
	CommandTimeout          string `json:"command_timeout,omitempty"`
	CaseTimeout             string `json:"case_timeout,omitempty"`
	SelectedInstancesSHA256 string `json:"selected_instances_sha256,omitempty"`
	Workers                 int    `json:"workers"`
	ExitStatus              string `json:"exit_status"`
	Error                   string `json:"error,omitempty"`
	ErrorCategory           string `json:"error_category,omitempty"`
	Retryable               bool   `json:"retryable,omitempty"`
}

// CaseResult is the framework-native result artifact for one instance.
type CaseResult struct {
	InstanceID      string            `json:"instance_id"`
	Info            CaseInfo          `json:"info"`
	ModelPatch      string            `json:"model_patch"`
	DurationMS      int64             `json:"duration_ms"`
	LLMCalls        int               `json:"llm_calls"`
	ToolCalls       int               `json:"tool_calls"`
	Usage           model.Usage       `json:"usage"`
	Events          []*event.Event    `json:"events,omitempty"`
	ResponseCount   int               `json:"response_count"`
	ResponsesSHA256 string            `json:"responses_sha256"`
	Responses       []*model.Response `json:"-"`
}

// Executor owns the per-case model and environment lifecycle.
type Executor struct {
	Factory                 sweenv.Factory
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
	Workers                 int
	ModelFactory            func(modelconfig.EnvConfig) model.Model
}

// Execute runs one case through llmagent.New, runner.NewRunner, and runner.Run,
// then drains the complete event stream.
func (e Executor) Execute(ctx context.Context, c contract.Case) (result CaseResult) {
	started := time.Now()
	result.InstanceID = c.InstanceID
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
		Workers:                 e.Workers,
		ExitStatus:              "Error",
	}
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	environment, err := e.Factory.Start(ctx, c.InstanceID)
	if err != nil {
		result.Info.Error = err.Error()
		result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
		result.Info.Retryable = true
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
	modelImpl = validatedNonStreamingModel{Model: modelImpl}
	state := &tagagent.State{}
	agentImpl := tagagent.New(modelImpl, environment, e.ObservationCodec, generationConfig(e.ModelConfig), state)
	run := tagrunner.NewRunner("swebench", agentImpl)
	defer run.Close()

	events, err := run.Run(
		caseCtx,
		c.InstanceID,
		c.InstanceID,
		model.NewUserMessage(protocol.PromptForTask(c.ProblemStatement)),
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

func applySnapshot(result *CaseResult, snapshot tagagent.Snapshot) {
	result.ModelPatch = snapshot.Submission
	result.LLMCalls = snapshot.LLMCalls
	result.ToolCalls = snapshot.ToolCalls
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
	return nil
}
