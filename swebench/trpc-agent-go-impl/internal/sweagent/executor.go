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
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
	agentRunner "trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const maxSteps = 250

// CaseResult is the complete native result for one instance.
type CaseResult struct {
	InstanceID  string         `json:"instance_id"`
	Info        CaseInfo       `json:"info"`
	Messages    []TraceMessage `json:"messages"`
	Events      []*event.Event `json:"events,omitempty"`
	ModelPatch  string         `json:"-"`
	DurationMS  int64          `json:"duration_ms"`
	Environment string         `json:"environment"`
	EventCount  int            `json:"event_count"`
	LLMCalls    int            `json:"llm_calls"`
	ToolCalls   int            `json:"tool_calls"`
}

// CaseInfo is compatible with the shared trajectory importer.
type CaseInfo struct {
	ExitStatus    string `json:"exit_status"`
	Submission    string `json:"submission,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
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
	Role    string         `json:"role"`
	Content string         `json:"content"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// Executor runs one case with a fresh tRPC agent and environment.
type Executor struct {
	Factory      environment.Factory
	ModelConfig  modelconfig.EnvConfig
	CaseTimeout  time.Duration
	ModelFactory func(modelconfig.EnvConfig) model.Model
	Progress     func(ProgressUpdate)
}

// Execute runs one safe SWE-Bench case and always attempts to collect git diff.
func (e Executor) Execute(ctx context.Context, c contract.Case) (result CaseResult) {
	started := time.Now()
	lastEventAt := started
	var lastLLMAt *time.Time
	lastEventObject := ""
	result.InstanceID = c.InstanceID
	result.Environment = "docker"
	result.Info.ExitStatus = "Error"
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
		result.finishMessage()
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
	submission := &Submission{}
	var modelImpl model.Model = newModel(e.ModelConfig)
	if e.ModelFactory != nil {
		modelImpl = e.ModelFactory(e.ModelConfig)
	}
	ag := llmagent.New("mini-swe-agent",
		llmagent.WithModel(modelImpl),
		llmagent.WithGlobalInstruction(SystemPrompt),
		llmagent.WithTools([]tool.Tool{NewBashTool(env, submission)}),
		llmagent.WithToolCallbacks(ToolCallbacks()),
		llmagent.WithGenerationConfig(generationConfig(e.ModelConfig)),
		llmagent.WithMaxLLMCalls(maxSteps),
		llmagent.WithMaxToolIterations(maxSteps),
		llmagent.WithEnableParallelTools(false),
		llmagent.WithEnableCodeExecutionResponseProcessor(false),
	)
	r := agentRunner.NewRunner("swebench", ag)
	defer r.Close()
	events, err := r.Run(caseCtx, "swebench", c.InstanceID, model.NewUserMessage(PromptForCase(c)), agent.WithMaxRunDuration(caseTimeout))
	if err != nil {
		result.Info.Error = err.Error()
	} else {
		for ev := range events {
			result.Events = append(result.Events, ev)
			result.EventCount++
			lastEventAt = time.Now()
			if ev != nil && ev.Response != nil {
				lastEventObject = ev.Object
				switch ev.Object {
				case model.ObjectTypeChatCompletion:
					result.LLMCalls++
					completedAt := lastEventAt.UTC()
					lastLLMAt = &completedAt
				case model.ObjectTypeToolResponse:
					result.ToolCalls++
				}
			}
			if ev != nil && ev.Response != nil && ev.Error != nil {
				result.Info.Error = ev.Error.Message
			}
			report("running")
		}
	}
	if text, ok := submission.Value(); ok {
		result.Info.ExitStatus = "Submitted"
		result.Info.Submission = text
		result.Info.ErrorCategory = ""
		result.Info.Retryable = false
	} else if caseCtx.Err() == context.DeadlineExceeded {
		result.Info.ExitStatus = "Timeout"
		result.Info.Error = caseCtx.Err().Error()
		result.Info.ErrorCategory = ErrorCategoryCaseTimeout
	} else if strings.Contains(strings.ToLower(result.Info.Error), "limit") {
		result.Info.ExitStatus = "LimitsExceeded"
		result.Info.ErrorCategory = ErrorCategoryAgentLimit
	} else if result.Info.Error == "" {
		result.Info.ExitStatus = "NoSubmission"
	}
	if result.Info.Error != "" && result.Info.ErrorCategory == "" {
		result.Info.ErrorCategory, result.Info.Retryable = ClassifyError(result.Info.Error)
	}
	patch := env.Execute(context.Background(), "git add -N . >/dev/null 2>&1; git diff --binary --no-ext-diff")
	if patch.ReturnCode == 0 {
		result.ModelPatch = patch.Output
	} else if result.Info.Error == "" {
		result.Info.Error = fmt.Sprintf("collect patch: %s", patch.ExceptionInfo)
		result.Info.ErrorCategory = ErrorCategoryEnvironment
		result.Info.Retryable = true
	}
	result.finishMessage()
	return result
}

func (r *CaseResult) finishMessage() {
	r.Messages = append(r.Messages, TraceMessage{
		Role:    "exit",
		Content: r.Info.ExitStatus,
		Extra:   map[string]any{"exit_status": r.Info.ExitStatus},
	})
}

func newModel(cfg modelconfig.EnvConfig) model.Model {
	opts := []openai.Option{
		openai.WithAPIKey(cfg["OPENAI_API_KEY"]),
		openai.WithBaseURL(cfg["OPENAI_BASE_URL"]),
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
	if seconds, err := strconv.ParseFloat(cfg["MODEL_TIMEOUT_SECONDS"], 64); err == nil && seconds > 0 {
		opts = append(opts, openai.WithHTTPClientOptions(openai.WithHTTPClientTransport(timeoutTransport{
			base:    http.DefaultTransport,
			timeout: time.Duration(seconds * float64(time.Second)),
		})))
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
