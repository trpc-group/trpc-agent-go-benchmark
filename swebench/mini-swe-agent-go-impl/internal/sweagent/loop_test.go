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
	"testing"
	"time"

	openaisdk "github.com/openai/openai-go"
	environment "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type scriptedModel struct {
	responses []*model.Response
	requests  []*model.Request
}

type flakyModel struct {
	failures int
	calls    int
	err      error
}

func (*flakyModel) Info() model.Info { return model.Info{Name: "flaky"} }

func (m *flakyModel) GenerateContent(_ context.Context, _ *model.Request) (<-chan *model.Response, error) {
	m.calls++
	if m.calls <= m.failures {
		if m.err != nil {
			return nil, m.err
		}
		return nil, errors.New("rate limit")
	}
	responses := make(chan *model.Response, 1)
	responses <- assistantResponse("done", bashCall("submit", "submit"))
	close(responses)
	return responses, nil
}

func (*scriptedModel) Info() model.Info { return model.Info{Name: "scripted"} }

func (m *scriptedModel) GenerateContent(_ context.Context, request *model.Request) (<-chan *model.Response, error) {
	copyRequest := *request
	copyRequest.Messages = append([]model.Message(nil), request.Messages...)
	m.requests = append(m.requests, &copyRequest)
	responses := make(chan *model.Response, 1)
	if len(m.responses) > 0 {
		responses <- m.responses[0]
		m.responses = m.responses[1:]
	}
	close(responses)
	return responses, nil
}

func assistantResponse(content string, calls ...model.ToolCall) *model.Response {
	return &model.Response{
		Done:   true,
		Object: model.ObjectTypeChatCompletion,
		Choices: []model.Choice{{Message: model.Message{
			Role:      model.RoleAssistant,
			Content:   content,
			ToolCalls: calls,
		}}},
	}
}

func bashCall(id, command string) model.ToolCall {
	return model.ToolCall{
		ID:   id,
		Type: "function",
		Function: model.FunctionDefinitionParam{
			Name:      "bash",
			Arguments: []byte(`{"command":` + quotedJSON(command) + `}`),
		},
	}
}

func quotedJSON(value string) string {
	quoted := "\""
	for _, character := range value {
		switch character {
		case '\\':
			quoted += "\\\\"
		case '"':
			quoted += "\\\""
		case '\n':
			quoted += "\\n"
		default:
			quoted += string(character)
		}
	}
	return quoted + "\""
}

func TestMiniAgentFormatErrorContinuesThenSubmits(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse(SubmissionMarker + "\nnot a tool call"),
		assistantResponse("done", bashCall("submit", "echo "+SubmissionMarker+" && cat patch.txt")),
	}}
	env := &fakeEnvironment{results: []environment.CommandResult{{
		Output: SubmissionMarker + "\ndiff --git a/a b/a\n",
	}}}
	result := (&MiniAgent{Model: modelImpl, Environment: env, StepLimit: 3}).Run(context.Background(), "fix it")
	if result.Info.ExitStatus != "Submitted" || result.Submission != "diff --git a/a b/a\n" {
		t.Fatalf("result = %#v", result.Info)
	}
	if result.LLMCalls != 2 || result.ToolCalls != 1 {
		t.Fatalf("calls = llm %d tool %d", result.LLMCalls, result.ToolCalls)
	}
	if len(modelImpl.requests) != 2 || len(modelImpl.requests[1].Messages) != 3 {
		t.Fatalf("second request messages = %#v", modelImpl.requests)
	}
	if got := modelImpl.requests[1].Messages[2]; got.Role != model.RoleUser || got.Content == "" {
		t.Fatalf("format-error message = %#v", got)
	}
	for _, message := range result.Messages {
		if message.Role == "assistant" && message.Content == SubmissionMarker+"\nnot a tool call" {
			t.Fatal("invalid raw assistant message must not enter the v2.1 trajectory")
		}
	}
}

func TestMiniAgentExecutesMultipleActionsSequentiallyAndSubmissionDropsObservations(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("work", bashCall("one", "pwd"), bashCall("two", "submit")),
	}}
	env := &fakeEnvironment{results: []environment.CommandResult{
		{Output: "/testbed\n"},
		{Output: SubmissionMarker + "\npatch\n"},
	}}
	result := (&MiniAgent{Model: modelImpl, Environment: env}).Run(context.Background(), "fix it")
	if len(env.commands) != 2 || env.commands[0] != "pwd" || env.commands[1] != "submit" {
		t.Fatalf("commands = %#v", env.commands)
	}
	if result.Info.ExitStatus != "Submitted" || result.ToolCalls != 2 {
		t.Fatalf("result = %#v, tool calls = %d", result.Info, result.ToolCalls)
	}
	for _, message := range result.Messages {
		if message.Role == "tool" {
			t.Fatal("Submitted interrupts action execution before any observation is appended")
		}
	}
}

func TestMiniAgentChecksStepLimitBeforeNextQuery(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("one", bashCall("one", "pwd")),
		assistantResponse("two", bashCall("two", "pwd")),
	}}
	env := &fakeEnvironment{results: []environment.CommandResult{{Output: "one"}, {Output: "two"}}}
	result := (&MiniAgent{Model: modelImpl, Environment: env, StepLimit: 2}).Run(context.Background(), "fix it")
	if result.Info.ExitStatus != "LimitsExceeded" || result.LLMCalls != 2 || result.ToolCalls != 2 {
		t.Fatalf("result = %#v, llm=%d tool=%d", result.Info, result.LLMCalls, result.ToolCalls)
	}
	last := result.Messages[len(result.Messages)-1]
	if last.Role != "exit" || last.Content != "LimitsExceeded" {
		t.Fatalf("last message = %#v", last)
	}
}

func TestMiniAgentCostLimitRequiresExplicitCostAccounting(t *testing.T) {
	withoutCost := &scriptedModel{responses: []*model.Response{
		assistantResponse("one", bashCall("one", "pwd")),
		assistantResponse("two", bashCall("two", "pwd")),
	}}
	withoutCostResult := (&MiniAgent{
		Model: withoutCost, Environment: &fakeEnvironment{results: []environment.CommandResult{{}, {}}},
		StepLimit: 2, CostLimit: 0.1,
	}).Run(context.Background(), "fix it")
	if withoutCostResult.Info.ExitStatus != "LimitsExceeded" || withoutCostResult.LLMCalls != 2 {
		t.Fatalf("without cost callback: status=%q llm_calls=%d", withoutCostResult.Info.ExitStatus, withoutCostResult.LLMCalls)
	}

	withCost := &scriptedModel{responses: []*model.Response{
		assistantResponse("one", bashCall("one", "pwd")),
	}}
	withCostResult := (&MiniAgent{
		Model: withCost, Environment: &fakeEnvironment{results: []environment.CommandResult{{}}},
		StepLimit: 2, CostLimit: 0.1,
		ResponseCost: func(*model.Response) (float64, error) {
			return 0.2, nil
		},
	}).Run(context.Background(), "fix it")
	if withCostResult.Info.ExitStatus != "LimitsExceeded" || withCostResult.LLMCalls != 1 || withCostResult.Cost != 0.2 {
		t.Fatalf("with cost callback: status=%q llm_calls=%d cost=%v", withCostResult.Info.ExitStatus, withCostResult.LLMCalls, withCostResult.Cost)
	}
}

func TestAgentTrajectoryConfigReportsActualCostLimit(t *testing.T) {
	withoutCost := agentTrajectoryConfig(ObservationCodecXML, nil)
	withoutAgent := withoutCost["agent"].(map[string]any)
	if withoutAgent["cost_limit"] != nil {
		t.Fatalf("cost_limit without accounting = %#v, want nil", withoutAgent["cost_limit"])
	}
	limit := 3.0
	withCost := agentTrajectoryConfig(ObservationCodecXML, &limit)
	withAgent := withCost["agent"].(map[string]any)
	if withAgent["cost_limit"] != limit {
		t.Fatalf("cost_limit with accounting = %#v, want %v", withAgent["cost_limit"], limit)
	}
}

func TestMiniAgentUsesSelectedObservationCodec(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("inspect", bashCall("inspect", "pwd")),
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	env := &fakeEnvironment{results: []environment.CommandResult{
		{Output: "/testbed\n"},
		{Output: SubmissionMarker + "\npatch\n"},
	}}
	result := (&MiniAgent{
		Model: modelImpl, Environment: env, ObservationCodec: ObservationCodecJSON,
	}).Run(context.Background(), "fix it")
	if result.Info.ExitStatus != "Submitted" {
		t.Fatalf("result = %#v", result.Info)
	}
	toolMessage := modelImpl.requests[1].Messages[3]
	if toolMessage.Role != model.RoleTool || toolMessage.Content != `{"returncode":0,"output":"/testbed\n"}` {
		t.Fatalf("tool message = %#v", toolMessage)
	}
	for _, message := range result.Messages {
		if message.Role == "tool" && message.Content != toolMessage.Content {
			t.Fatalf("trajectory observation %q differs from request %q", message.Content, toolMessage.Content)
		}
	}
}

func TestMiniAgentRequestUsesParallelBashTools(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	env := &fakeEnvironment{results: []environment.CommandResult{{Output: SubmissionMarker}}}
	(&MiniAgent{Model: modelImpl, Environment: env}).Run(context.Background(), "task")
	request := modelImpl.requests[0]
	if request.ExtraFields["parallel_tool_calls"] != true || request.Tools["bash"] == nil {
		t.Fatalf("request tools/extra = %#v / %#v", request.Tools, request.ExtraFields)
	}
}

func TestMiniAgentRetriesModelLikeUpstreamWithoutConsumingSteps(t *testing.T) {
	modelImpl := &flakyModel{failures: 2}
	env := &fakeEnvironment{results: []environment.CommandResult{{Output: SubmissionMarker}}}
	var waits []time.Duration
	result := (&MiniAgent{
		Model:         modelImpl,
		Environment:   env,
		ModelAttempts: 10,
		RetryWait: func(_ context.Context, wait time.Duration) error {
			waits = append(waits, wait)
			return nil
		},
	}).Run(context.Background(), "task")
	if result.Info.ExitStatus != "Submitted" || result.LLMCalls != 1 || modelImpl.calls != 3 {
		t.Fatalf("result=%#v llm_calls=%d attempts=%d", result.Info, result.LLMCalls, modelImpl.calls)
	}
	if len(waits) != 2 || waits[0] != 4*time.Second || waits[1] != 4*time.Second {
		t.Fatalf("waits = %#v", waits)
	}
}

func TestMiniAgentRetriesRequestDeadlineWhileParentContextAlive(t *testing.T) {
	modelImpl := &flakyModel{failures: 1, err: context.DeadlineExceeded}
	env := &fakeEnvironment{results: []environment.CommandResult{{Output: SubmissionMarker}}}
	var waits []time.Duration
	result := (&MiniAgent{
		Model:         modelImpl,
		Environment:   env,
		ModelAttempts: 2,
		RetryWait: func(ctx context.Context, wait time.Duration) error {
			if err := ctx.Err(); err != nil {
				t.Fatalf("parent context unexpectedly done: %v", err)
			}
			waits = append(waits, wait)
			return nil
		},
	}).Run(context.Background(), "task")
	if result.Info.ExitStatus != "Submitted" || result.LLMCalls != 1 || modelImpl.calls != 2 {
		t.Fatalf("result=%#v llm_calls=%d attempts=%d", result.Info, result.LLMCalls, modelImpl.calls)
	}
	if len(waits) != 1 || waits[0] != 4*time.Second {
		t.Fatalf("waits = %#v", waits)
	}
}

func TestMiniAgentDoesNotQueryOrRetryWhenParentContextDone(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.context()
			defer cancel()
			modelImpl := &flakyModel{failures: 10}
			result := (&MiniAgent{
				Model:         modelImpl,
				Environment:   &fakeEnvironment{},
				ModelAttempts: 10,
				RetryWait: func(context.Context, time.Duration) error {
					t.Fatal("done parent context must not wait for a retry")
					return nil
				},
			}).Run(ctx, "task")
			if result.Info.ExitStatus != "Error" || result.Info.Error != test.wantErr.Error() {
				t.Fatalf("result = %#v, want %v", result.Info, test.wantErr)
			}
			if modelImpl.calls != 0 {
				t.Fatalf("model attempts = %d, want 0", modelImpl.calls)
			}
		})
	}
}

func TestMiniAgentDoesNotRetryAbortErrors(t *testing.T) {
	modelImpl := &flakyModel{failures: 10, err: errors.New("status 401 authentication failed")}
	env := &fakeEnvironment{}
	result := (&MiniAgent{
		Model:       modelImpl,
		Environment: env,
		RetryWait: func(context.Context, time.Duration) error {
			t.Fatal("abort errors must not wait for a retry")
			return nil
		},
	}).Run(context.Background(), "task")
	// Swap the generic flaky error for a direct predicate check so this test
	// stays focused on the LiteLLM abort categories.
	if !shouldRetryModelError(errors.New("rate limit")) || shouldRetryModelError(errors.New("status 401 authentication failed")) {
		t.Fatal("model retry classification mismatch")
	}
	if result.Info.ExitStatus != "Error" || modelImpl.calls != 1 {
		t.Fatalf("result = %#v, attempts = %d", result.Info, modelImpl.calls)
	}
}

func TestShouldRetryModelErrorUsesTypedHTTPStatus(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: http.StatusBadRequest, want: false},
		{status: http.StatusUnauthorized, want: false},
		{status: http.StatusForbidden, want: false},
		{status: http.StatusNotFound, want: false},
		{status: http.StatusRequestTimeout, want: true},
		{status: http.StatusConflict, want: true},
		{status: http.StatusTooManyRequests, want: true},
		{status: http.StatusInternalServerError, want: true},
		{status: http.StatusBadGateway, want: true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("status-%d", test.status), func(t *testing.T) {
			err := fmt.Errorf("model request failed: %w", &openaisdk.Error{StatusCode: test.status})
			if got := shouldRetryModelError(err); got != test.want {
				t.Fatalf("shouldRetryModelError(status=%d) = %t, want %t", test.status, got, test.want)
			}
		})
	}
}
