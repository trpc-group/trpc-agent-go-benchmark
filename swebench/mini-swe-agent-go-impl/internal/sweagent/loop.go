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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	openaisdk "github.com/openai/openai-go"
	environment "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

const maxSteps = 250

// MiniAgent is the source-aligned v2.1.0 control loop. tRPC-Agent-Go supplies
// the model transport and message/tool types; this type owns mini-SWE-agent's
// query, action parsing, observation, submission, and step-limit semantics.
// Cost-limit enforcement is available only when ResponseCost is configured;
// the provider-neutral runner does not invent a model price table.
type MiniAgent struct {
	Model            model.Model
	Environment      environment.Environment
	ObservationCodec ObservationCodec
	GenerationConfig model.GenerationConfig
	StepLimit        int
	CostLimit        float64
	ResponseCost     func(*model.Response) (float64, error)
	ModelAttempts    int
	RetryWait        func(context.Context, time.Duration) error
	OnQueryStart     func(int)
	OnLLMResponse    func(*model.Response)
	OnToolResult     func(Action, environment.CommandResult)
}

// LoopResult is the complete outcome of one MiniAgent run.
type LoopResult struct {
	Info        CaseInfo
	Messages    []TraceMessage
	Responses   []*model.Response
	LLMCalls    int
	ToolCalls   int
	LastEventAt time.Time
	LastLLMAt   *time.Time
	Submission  string
	Cost        float64
}

// Run executes the task until the environment submits, the step limit is
// reached, or an uncaught model/environment error occurs.
func (a *MiniAgent) Run(ctx context.Context, task string) LoopResult {
	limit := a.StepLimit
	if limit <= 0 {
		limit = maxSteps
	}
	costLimit := effectiveCostLimit(a.CostLimit, a.ResponseCost)
	now := time.Now()
	result := LoopResult{
		LastEventAt: now,
		Messages: []TraceMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: PromptForTask(task)},
		},
	}
	apiMessages := []model.Message{
		model.NewSystemMessage(SystemPrompt),
		model.NewUserMessage(PromptForTask(task)),
	}

	for {
		// DefaultAgent.query checks the limit before incrementing n_calls.
		if result.LLMCalls >= limit || (costLimit != nil && *costLimit > 0 && result.Cost >= *costLimit) {
			result.Info.ExitStatus = "LimitsExceeded"
			result.Messages = append(result.Messages, exitMessage("LimitsExceeded", ""))
			return result
		}

		result.LLMCalls++
		if a.OnQueryStart != nil {
			a.OnQueryStart(result.LLMCalls)
		}
		response, err := a.query(ctx, apiMessages, &result)
		if err != nil {
			result.Info.ExitStatus = "Error"
			result.Info.Error = err.Error()
			result.Messages = append(result.Messages, errorExitMessage(err))
			return result
		}
		if a.ResponseCost != nil {
			cost, costErr := a.ResponseCost(response)
			if costErr != nil {
				result.Info.ExitStatus = "Error"
				result.Info.Error = costErr.Error()
				result.Messages = append(result.Messages, errorExitMessage(costErr))
				return result
			}
			result.Cost += cost
		}
		if len(response.Choices) == 0 {
			err = errors.New("model response contains no choices")
			result.Info.ExitStatus = "Error"
			result.Info.Error = err.Error()
			result.Messages = append(result.Messages, errorExitMessage(err))
			return result
		}

		assistant := response.Choices[0].Message
		actions, parseErr := ParseActions(assistant.ToolCalls)
		if parseErr != nil {
			var formatErr formatErrorValue
			if !errors.As(parseErr, &formatErr) {
				result.Info.ExitStatus = "Error"
				result.Info.Error = parseErr.Error()
				result.Messages = append(result.Messages, errorExitMessage(parseErr))
				return result
			}
			// v2.1.0 raises FormatError while parsing, before the invalid raw
			// assistant message is added to the trajectory/API history.
			trace := TraceMessage{
				Role:    "user",
				Content: formatErr.Error(),
				Extra:   map[string]any{"interrupt_type": "FormatError"},
			}
			result.Messages = append(result.Messages, trace)
			apiMessages = append(apiMessages, model.NewUserMessage(trace.Content))
			result.LastEventAt = time.Now()
			continue
		}

		assistantTrace := traceFromAssistant(assistant, actions, response)
		result.Messages = append(result.Messages, assistantTrace)
		apiMessages = append(apiMessages, assistant)
		result.LastEventAt = time.Now()

		// The Python list comprehension executes actions sequentially. A
		// Submitted exception interrupts it immediately and no observations
		// from this assistant response are appended.
		outputs := make([]environment.CommandResult, 0, len(actions))
		for _, action := range actions {
			output := a.Environment.Execute(ctx, action.Command)
			result.ToolCalls++
			result.LastEventAt = time.Now()
			if a.OnToolResult != nil {
				a.OnToolResult(action, output)
			}
			if submission, submitted := SubmissionFromResult(output); submitted {
				result.Submission = submission
				result.Info.ExitStatus = "Submitted"
				result.Info.Submission = submission
				result.Messages = append(result.Messages, exitMessage("Submitted", submission))
				return result
			}
			outputs = append(outputs, output)
		}

		for i, output := range outputs {
			observation, formatErr := FormatObservationWithCodec(output, a.ObservationCodec)
			if formatErr != nil {
				result.Info.ExitStatus = "Error"
				result.Info.Error = formatErr.Error()
				result.Messages = append(result.Messages, errorExitMessage(formatErr))
				return result
			}
			trace := TraceMessage{
				Role:       "tool",
				Content:    observation,
				ToolCallID: actions[i].ToolCallID,
				Extra: map[string]any{
					"raw_output":     output.Output,
					"returncode":     output.ReturnCode,
					"timestamp":      float64(time.Now().UnixNano()) / 1e9,
					"exception_info": output.ExceptionInfo,
				},
			}
			result.Messages = append(result.Messages, trace)
			apiMessages = append(apiMessages, model.NewToolMessage(actions[i].ToolCallID, "bash", observation))
		}
		result.LastEventAt = time.Now()
	}
}

func effectiveCostLimit(
	configured float64,
	responseCost func(*model.Response) (float64, error),
) *float64 {
	if responseCost == nil {
		return nil
	}
	if configured == 0 {
		configured = 3
	}
	return &configured
}

func (a *MiniAgent) query(ctx context.Context, messages []model.Message, result *LoopResult) (*model.Response, error) {
	attempts := a.ModelAttempts
	if attempts <= 0 {
		attempts = 10
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := a.queryOnce(ctx, messages, result)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if attempt == attempts || !shouldRetryModelError(err) {
			return nil, err
		}
		wait := retryDelay(attempt)
		if a.RetryWait != nil {
			if err := a.RetryWait(ctx, wait); err != nil {
				return nil, err
			}
		} else {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}

func (a *MiniAgent) queryOnce(ctx context.Context, messages []model.Message, result *LoopResult) (*model.Response, error) {
	request := &model.Request{
		Messages:         append([]model.Message(nil), messages...),
		GenerationConfig: a.GenerationConfig,
		ExtraFields:      map[string]any{"parallel_tool_calls": true},
		Tools:            bashTools(),
	}
	responses, err := a.Model.GenerateContent(ctx, request)
	if err != nil {
		return nil, err
	}
	if responses == nil {
		return nil, errors.New("model returned nil response channel")
	}
	var final *model.Response
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case response, ok := <-responses:
			if !ok {
				if final == nil {
					return nil, errors.New("model returned no response")
				}
				return final, nil
			}
			if response == nil {
				continue
			}
			result.Responses = append(result.Responses, response)
			result.LastEventAt = time.Now()
			if a.OnLLMResponse != nil {
				a.OnLLMResponse(response)
			}
			if response.Error != nil {
				return nil, response.Error
			}
			final = response
			if response.Done {
				completedAt := time.Now().UTC()
				result.LastLLMAt = &completedAt
				return response, nil
			}
		}
	}
}

func retryDelay(failedAttempt int) time.Duration {
	seconds := 1 << max(failedAttempt-1, 0)
	if seconds < 4 {
		seconds = 4
	}
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func shouldRetryModelError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *openaisdk.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		switch apiErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
			return true
		default:
			return apiErr.StatusCode >= http.StatusInternalServerError
		}
	}
	message := strings.ToLower(err.Error())
	return !containsAny(message,
		"authentication", "unauthorized", "invalid api key", "invalid_api_key", "status 401", "status code 401",
		"permission denied", "forbidden", "status 403", "status code 403",
		"not found", "status 404", "status code 404",
		"unsupported param", "unsupported parameter",
		"context window", "context length", "maximum context", "max context",
	)
}

func traceFromAssistant(message model.Message, actions []Action, response *model.Response) TraceMessage {
	actionData := make([]map[string]string, 0, len(actions))
	for _, action := range actions {
		actionData = append(actionData, map[string]string{
			"command":      action.Command,
			"tool_call_id": action.ToolCallID,
		})
	}
	return TraceMessage{
		Role:             "assistant",
		Content:          message.Content,
		ToolCalls:        message.ToolCalls,
		ReasoningContent: message.ReasoningContent,
		Extra: map[string]any{
			"actions":   actionData,
			"response":  response,
			"timestamp": float64(time.Now().UnixNano()) / 1e9,
		},
	}
}

func exitMessage(status, submission string) TraceMessage {
	content := status
	if status == "Submitted" {
		content = submission
	}
	return TraceMessage{
		Role:    "exit",
		Content: content,
		Extra: map[string]any{
			"exit_status": status,
			"submission":  submission,
		},
	}
}

func errorExitMessage(err error) TraceMessage {
	return TraceMessage{
		Role:    "exit",
		Content: err.Error(),
		Extra: map[string]any{
			"exit_status":   fmt.Sprintf("%T", err),
			"submission":    "",
			"exception_str": err.Error(),
		},
	}
}
