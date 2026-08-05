//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestValidateKnowledgeSearchArguments(t *testing.T) {
	tests := []struct {
		name       string
		arguments  string
		missing    []string
		unexpected []string
		invalid    []string
	}{
		{name: "valid", arguments: `{"query":"focused query"}`},
		{
			name:       "wrong field",
			arguments:  `{"query: ":"focused query"}`,
			missing:    []string{"query"},
			unexpected: []string{"query: "},
		},
		{name: "missing", arguments: `{}`, missing: []string{"query"}},
		{name: "empty", arguments: `{"query":"  "}`, invalid: []string{"query"}},
		{name: "wrong type", arguments: `{"query":42}`, invalid: []string{"query"}},
		{name: "invalid JSON", arguments: `{"query":`, invalid: []string{"arguments"}},
		{name: "not object", arguments: `[]`, invalid: []string{"arguments"}},
		{name: "extra field", arguments: `{"query":"q","extra":true}`, unexpected: []string{"extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := validateKnowledgeSearchArguments([]byte(test.arguments))
			if !reflect.DeepEqual(got.missing, test.missing) ||
				!reflect.DeepEqual(got.unexpected, test.unexpected) ||
				!reflect.DeepEqual(got.invalid, test.invalid) {
				t.Fatalf(
					"validation = %#v, want missing=%#v unexpected=%#v invalid=%#v",
					got,
					test.missing,
					test.unexpected,
					test.invalid,
				)
			}
		})
	}
}

func TestQueryArgumentGuardAllowsOneModelCorrection(t *testing.T) {
	guard := newQueryArgumentGuard()
	invalid := &tool.BeforeToolArgs{
		ToolName:  knowledgeSearchToolName,
		Arguments: []byte(`{"query: ":"focused query"}`),
	}
	result, err := guard.beforeTool(context.Background(), invalid)
	if err != nil {
		t.Fatalf("first invalid call error = %v", err)
	}
	feedback, ok := result.CustomResult.(*toolArgumentValidationResult)
	if !ok {
		t.Fatalf("custom result = %#v, want validation feedback", result.CustomResult)
	}
	if feedback.Type != toolArgumentValidationErrorType ||
		feedback.Policy != toolArgumentPolicy || !feedback.Retryable ||
		feedback.RemainingRepairs != 1 ||
		!reflect.DeepEqual(feedback.Allowed, []string{"query"}) ||
		!reflect.DeepEqual(feedback.Missing, []string{"query"}) ||
		!reflect.DeepEqual(feedback.Unexpected, []string{"query: "}) {
		t.Fatalf("feedback = %#v", feedback)
	}

	corrected := &tool.BeforeToolArgs{
		ToolName:  knowledgeSearchToolName,
		Arguments: []byte(`{"query":"focused query"}`),
	}
	result, err = guard.beforeTool(context.Background(), corrected)
	if err != nil || result != nil {
		t.Fatalf("corrected call result/error = %#v/%v, want nil/nil", result, err)
	}
}

func TestQueryArgumentGuardFailsClosedAfterRepairBudget(t *testing.T) {
	guard := newQueryArgumentGuard()
	args := &tool.BeforeToolArgs{
		ToolName:  knowledgeSearchToolName,
		Arguments: []byte(`{"query: ":"focused query"}`),
	}
	if _, err := guard.beforeTool(context.Background(), args); err != nil {
		t.Fatalf("first invalid call error = %v", err)
	}
	_, err := guard.beforeTool(context.Background(), args)
	stop, ok := agent.AsStopError(err)
	if !ok || stop.Message != toolArgumentRepairExhaustedType {
		t.Fatalf("second invalid call error = %v, want StopError(%s)", err, toolArgumentRepairExhaustedType)
	}
}

func TestQueryArgumentGuardIgnoresOtherTools(t *testing.T) {
	guard := newQueryArgumentGuard()
	result, err := guard.beforeTool(context.Background(), &tool.BeforeToolArgs{
		ToolName:  "other_tool",
		Arguments: []byte(`{"query: ":"focused query"}`),
	})
	if err != nil || result != nil {
		t.Fatalf("other tool result/error = %#v/%v, want nil/nil", result, err)
	}
}

func TestIsToolArgumentValidationResponse(t *testing.T) {
	if !isToolArgumentValidationResponse(`{"type":"tool_argument_validation_error"}`) {
		t.Fatal("expected validation response to be recognized")
	}
	if isToolArgumentValidationResponse(`{"documents":[]}`) {
		t.Fatal("knowledge response must not be recognized as validation feedback")
	}
}

func TestToolArgumentValidationResponseDoesNotPolluteContexts(t *testing.T) {
	content := `{"type":"tool_argument_validation_error","policy":"query-guard/v1"}`
	result := &AnswerResult{Trace: &AgentTrace{}}
	(&KnowledgeService{config: &ServiceConfig{
		IndexVariant: indexVariantBaseline,
	}}).captureToolResponses(
		&event.Event{Response: &model.Response{Choices: []model.Choice{{
			Message: model.Message{
				Role:    model.RoleTool,
				ToolID:  "tool-invalid",
				Content: content,
			},
		}}}},
		result.Trace,
		map[string]bool{},
		result,
	)
	if len(result.Contexts) != 0 {
		t.Fatalf("contexts = %#v, want no validation feedback", result.Contexts)
	}
	if len(result.Trace.ToolResponses) != 1 || result.Trace.ToolResponses[0].Content != content {
		t.Fatalf("tool responses = %#v, want validation feedback in trace", result.Trace.ToolResponses)
	}
}

func TestToolDispatcherErrorDoesNotPolluteContexts(t *testing.T) {
	content := "executeToolCall: Error: tool not found"
	result := &AnswerResult{Trace: &AgentTrace{}}
	(&KnowledgeService{config: &ServiceConfig{
		IndexVariant: indexVariantBaseline,
	}}).captureToolResponses(
		&event.Event{Response: &model.Response{Choices: []model.Choice{{
			Message: model.Message{
				Role:    model.RoleTool,
				ToolID:  "tool-name-error",
				Content: content,
			},
		}}}},
		result.Trace,
		map[string]bool{},
		result,
	)
	if len(result.Contexts) != 0 {
		t.Fatalf("contexts = %#v, want no dispatcher error", result.Contexts)
	}
	if len(result.Trace.ToolResponses) != 1 || result.Trace.ToolResponses[0].Content != content {
		t.Fatalf("tool responses = %#v, want dispatcher error in trace", result.Trace.ToolResponses)
	}
}

func TestLegacyToolResponsePreservesRawContext(t *testing.T) {
	content := "executeToolCall: Error: tool not found"
	result := &AnswerResult{Trace: &AgentTrace{}}
	(&KnowledgeService{config: &ServiceConfig{
		IndexVariant: indexVariantLegacy,
	}}).captureToolResponses(
		&event.Event{Response: &model.Response{Choices: []model.Choice{{
			Message: model.Message{
				Role:    model.RoleTool,
				ToolID:  "legacy-tool-error",
				Content: content,
			},
		}}}},
		result.Trace,
		map[string]bool{},
		result,
	)
	if len(result.Contexts) != 1 || result.Contexts[0] != content {
		t.Fatalf("contexts = %#v, want legacy raw response", result.Contexts)
	}
	if len(result.Trace.ToolResponses) != 1 ||
		result.Trace.ToolResponses[0].Content != content {
		t.Fatalf("tool responses = %#v, want legacy raw response in trace", result.Trace.ToolResponses)
	}
}

func TestAnswerErrorType(t *testing.T) {
	if got := answerErrorType(
		errors.New("event error: " + toolArgumentRepairExhaustedType),
	); got != toolArgumentRepairExhaustedType {
		t.Fatalf("answerErrorType() = %q, want %q", got, toolArgumentRepairExhaustedType)
	}
	if got := answerErrorType(errors.New("upstream unavailable")); got != "agent_execution_error" {
		t.Fatalf("answerErrorType() = %q, want agent_execution_error", got)
	}
}
