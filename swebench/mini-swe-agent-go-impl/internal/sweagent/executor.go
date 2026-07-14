//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweagent

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	openaiopt "github.com/openai/openai-go/option"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/mini-swe-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// CaseResult is the complete mini-go result for one instance.
type CaseResult struct {
	InstanceID       string            `json:"instance_id"`
	Info             CaseInfo          `json:"info"`
	Messages         []TraceMessage    `json:"messages"`
	TrajectoryFormat string            `json:"trajectory_format"`
	TRPCResponses    []*model.Response `json:"-"`
	ModelPatch       string            `json:"-"`
	DurationMS       int64             `json:"duration_ms"`
	Environment      string            `json:"environment"`
	EventCount       int               `json:"event_count"`
	LLMCalls         int               `json:"llm_calls"`
	ToolCalls        int               `json:"tool_calls"`
}

// CaseInfo is compatible with the shared trajectory importer.
type CaseInfo struct {
	ModelStats       ModelStats     `json:"model_stats"`
	Config           map[string]any `json:"config,omitempty"`
	MiniVersion      string         `json:"mini_version"`
	ObservationCodec string         `json:"observation_codec,omitempty"`
	BillingAgentName string         `json:"billing_agent_name,omitempty"`
	ExperimentID     string         `json:"experiment_id,omitempty"`
	SourceRevision   string         `json:"source_revision,omitempty"`
	SourceModified   bool           `json:"source_modified,omitempty"`
	BinarySHA256     string         `json:"binary_sha256,omitempty"`
	ModelConfigHash  string         `json:"model_config_sha256,omitempty"`
	CasesHash        string         `json:"cases_sha256,omitempty"`
	ExitStatus       string         `json:"exit_status"`
	Submission       string         `json:"submission,omitempty"`
	Error            string         `json:"error,omitempty"`
	ErrorCategory    string         `json:"error_category,omitempty"`
	Retryable        bool           `json:"retryable,omitempty"`
}

// ModelStats matches DefaultAgent.serialize's model_stats object.
type ModelStats struct {
	InstanceCost float64 `json:"instance_cost"`
	APICalls     int     `json:"api_calls"`
}

// ProgressUpdate is a compact, frequently updated view of an active case.
type ProgressUpdate struct {
	InstanceID      string     `json:"instance_id"`
	Phase           string     `json:"phase"`
	StartedAt       time.Time  `json:"started_at"`
	LastEventAt     time.Time  `json:"last_event_at"`
	LastLLMAt       *time.Time `json:"last_llm_at,omitempty"`
	LastEventObject string     `json:"last_event_object,omitempty"`
	EventCount      int        `json:"event_count"`
	LLMCalls        int        `json:"llm_calls"`
	ToolCalls       int        `json:"tool_calls"`
	ExitStatus      string     `json:"exit_status,omitempty"`
	ErrorCategory   string     `json:"error_category,omitempty"`
}

// TraceMessage provides a mini-compatible terminal message.
type TraceMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ToolCalls        []model.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	Extra            map[string]any   `json:"extra,omitempty"`
}

// Executor runs one case with a fresh source-aligned mini agent and environment.
type Executor struct {
	Factory          environment.Factory
	ModelConfig      modelconfig.EnvConfig
	ObservationCodec ObservationCodec
	BillingAgentName string
	ExperimentID     string
	SourceRevision   string
	SourceModified   bool
	BinarySHA256     string
	ModelConfigHash  string
	CasesHash        string
	CaseTimeout      time.Duration
	ModelFactory     func(modelconfig.EnvConfig) model.Model
	Progress         func(ProgressUpdate)
}

// Execute runs one SWE-Bench case with the source-aligned mini-SWE-agent loop.
func (e Executor) Execute(ctx context.Context, c contract.Case) (result CaseResult) {
	started := time.Now()
	lastEventAt := started
	var lastLLMAt *time.Time
	lastEventObject := ""
	result.InstanceID = c.InstanceID
	result.Environment = "docker"
	result.TrajectoryFormat = "mini-swe-agent-1.1"
	result.Info.ExitStatus = "Error"
	result.Info.MiniVersion = "2.1.0"
	codec := normalizeObservationCodec(e.ObservationCodec)
	result.Info.ObservationCodec = string(codec)
	result.Info.BillingAgentName = e.BillingAgentName
	result.Info.ExperimentID = e.ExperimentID
	result.Info.SourceRevision = e.SourceRevision
	result.Info.SourceModified = e.SourceModified
	result.Info.BinarySHA256 = e.BinarySHA256
	result.Info.ModelConfigHash = e.ModelConfigHash
	result.Info.CasesHash = e.CasesHash
	result.Info.Config = agentTrajectoryConfig(codec)
	report := func(phase string) {
		if e.Progress == nil {
			return
		}
		exitStatus := ""
		if phase == "finished" {
			exitStatus = result.Info.ExitStatus
		}
		e.Progress(ProgressUpdate{
			InstanceID:      c.InstanceID,
			Phase:           phase,
			StartedAt:       started.UTC(),
			LastEventAt:     lastEventAt.UTC(),
			LastLLMAt:       lastLLMAt,
			LastEventObject: lastEventObject,
			EventCount:      result.EventCount,
			LLMCalls:        result.LLMCalls,
			ToolCalls:       result.ToolCalls,
			ExitStatus:      exitStatus,
			ErrorCategory:   result.Info.ErrorCategory,
		})
	}
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		report("finished")
	}()
	report("starting")

	env, err := e.Factory.Start(ctx, c.InstanceID)
	if err != nil {
		result.Info.Error = err.Error()
		result.Info.ErrorCategory = ErrorCategoryEnvironment
		result.Info.Retryable = true
		result.Messages = append(result.Messages, errorExitMessage(err))
		return result
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if closeErr := env.Close(closeCtx); closeErr != nil && result.Info.Error == "" {
			result.Info.Error = closeErr.Error()
			result.Info.ErrorCategory = ErrorCategoryEnvironment
			result.Info.Retryable = true
		}
	}()
	report("running")

	caseTimeout := e.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	caseCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	var modelImpl model.Model = newModel(e.ModelConfig)
	if e.ModelFactory != nil {
		modelImpl = e.ModelFactory(e.ModelConfig)
	}
	loop := MiniAgent{
		Model:            modelImpl,
		Environment:      env,
		ObservationCodec: codec,
		GenerationConfig: generationConfig(e.ModelConfig),
		StepLimit:        maxSteps,
		OnQueryStart: func(calls int) {
			result.LLMCalls = calls
			lastEventAt = time.Now()
			lastEventObject = "model.request"
			report("running")
		},
		OnLLMResponse: func(response *model.Response) {
			result.EventCount++
			lastEventAt = time.Now()
			lastEventObject = response.Object
			if response.Done {
				completedAt := lastEventAt.UTC()
				lastLLMAt = &completedAt
			}
			report("running")
		},
		OnToolResult: func(_ Action, _ environment.CommandResult) {
			result.ToolCalls++
			result.EventCount++
			lastEventAt = time.Now()
			lastEventObject = "tool.response"
			report("running")
		},
	}
	loopResult := loop.Run(caseCtx, c.ProblemStatement)
	result.Info = loopResult.Info
	result.Info.ModelStats = ModelStats{InstanceCost: loopResult.Cost, APICalls: loopResult.LLMCalls}
	result.Info.MiniVersion = "2.1.0"
	result.Info.ObservationCodec = string(codec)
	result.Info.BillingAgentName = e.BillingAgentName
	result.Info.ExperimentID = e.ExperimentID
	result.Info.SourceRevision = e.SourceRevision
	result.Info.SourceModified = e.SourceModified
	result.Info.BinarySHA256 = e.BinarySHA256
	result.Info.ModelConfigHash = e.ModelConfigHash
	result.Info.CasesHash = e.CasesHash
	result.Info.Config = agentTrajectoryConfig(codec)
	result.Messages = loopResult.Messages
	result.TRPCResponses = loopResult.Responses
	result.LLMCalls = loopResult.LLMCalls
	result.ToolCalls = loopResult.ToolCalls
	result.ModelPatch = loopResult.Submission
	lastEventAt = loopResult.LastEventAt
	lastLLMAt = loopResult.LastLLMAt
	if caseCtx.Err() == context.DeadlineExceeded {
		result.Info.ExitStatus = "Timeout"
		result.Info.Error = caseCtx.Err().Error()
		result.Info.ErrorCategory = ErrorCategoryCaseTimeout
	}
	if result.Info.Error != "" && result.Info.ErrorCategory == "" {
		result.Info.ErrorCategory, result.Info.Retryable = ClassifyError(result.Info.Error)
	}
	return result
}

func newModel(cfg modelconfig.EnvConfig) model.Model {
	return newModelWithTransport(cfg, nil)
}

func newModelWithTransport(cfg modelconfig.EnvConfig, transport http.RoundTripper) model.Model {
	opts := []openai.Option{
		openai.WithAPIKey(cfg["OPENAI_API_KEY"]),
		openai.WithBaseURL(cfg["OPENAI_BASE_URL"]),
		// mini-SWE-agent owns the ten-attempt exponential retry loop. Disable
		// the SDK's nested retries so an attempt has the same meaning.
		openai.WithOpenAIOptions(openaiopt.WithMaxRetries(0)),
	}
	headers := map[string]string{}
	for envKey, header := range map[string]string{
		"X_SMG_ROUTING_KEY": "X-SMG-Routing-Key",
		"X_SMG_AGENT_NAME":  "X-SMG-Agent-Name",
		"X_SMG_PROVIDER":    "X-SMG-Provider",
	} {
		if value := strings.TrimSpace(cfg[envKey]); value != "" {
			headers[header] = value
		}
	}
	if len(headers) > 0 {
		opts = append(opts, openai.WithHeaders(headers))
	}
	baseTransport := transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	if seconds, err := strconv.ParseFloat(cfg["MODEL_TIMEOUT_SECONDS"], 64); err == nil && seconds > 0 {
		opts = append(opts, openai.WithHTTPClientOptions(openai.WithHTTPClientTransport(timeoutTransport{
			base:    baseTransport,
			timeout: time.Duration(seconds * float64(time.Second)),
		})))
	} else if transport != nil {
		opts = append(opts, openai.WithHTTPClientOptions(openai.WithHTTPClientTransport(transport)))
	}
	return openai.New(cfg["MODEL_NAME"], opts...)
}

type timeoutTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t timeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(request.Context(), t.timeout)
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func generationConfig(cfg modelconfig.EnvConfig) model.GenerationConfig {
	temperature := 0.0
	if value, err := strconv.ParseFloat(cfg["MODEL_TEMPERATURE"], 64); err == nil {
		temperature = value
	}
	gen := model.GenerationConfig{Temperature: &temperature}
	if effort := strings.TrimSpace(cfg["MODEL_REASONING_EFFORT"]); effort != "" {
		gen.ReasoningEffort = &effort
	}
	return gen
}
