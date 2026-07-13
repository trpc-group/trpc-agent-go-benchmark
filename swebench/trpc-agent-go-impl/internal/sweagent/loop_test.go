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
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
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
