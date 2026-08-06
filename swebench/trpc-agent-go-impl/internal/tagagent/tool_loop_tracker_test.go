//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tagagent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/toolloop"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestToolLoopWarningUsesCompleteBatchAndNextRealModelCall(t *testing.T) {
	state := &State{}
	tracker := newToolLoopTracker(true)
	modelCB := modelCallbacks(state, tracker)
	toolCB := toolCallbacks(state, observation.ObservationCodecXML, tracker)

	first := []model.ToolCall{
		bashToolCallForLoopTest("first-a", `{"command":"pwd"}`),
		bashToolCallForLoopTest("first-b", `{"command":"git status"}`),
	}
	second := []model.ToolCall{
		bashToolCallForLoopTest("second-a", `{ "command" : "pwd" }`),
		bashToolCallForLoopTest("second-b", "{\n\"command\":\"git status\"\n}"),
	}
	results := []sweenv.CommandResult{{Output: "/repo\n"}, {Output: "clean\n"}}
	runToolLoopBatch(t, modelCB, toolCB, first, results)
	runToolLoopBatch(t, modelCB, toolCB, second, results)

	// Detection alone is not telemetry: only a real next request is.
	before := state.Snapshot()
	if before.ToolLoopWarningCount != 0 || before.FirstToolLoopWarningLLMCall != 0 ||
		len(before.ToolLoopWarningLLMCalls) != 0 {
		t.Fatalf("warning telemetry before injection = %#v", before)
	}
	request := &model.Request{Messages: []model.Message{model.NewUserMessage("task")}}
	if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: request}); err != nil {
		t.Fatalf("RunBeforeModel() error = %v", err)
	}
	if len(request.Messages) != 2 || request.Messages[1].Role != model.RoleUser ||
		request.Messages[1].Content != toolloop.Warning {
		t.Fatalf("messages after warning = %#v", request.Messages)
	}
	snapshot := state.Snapshot()
	if snapshot.LLMCalls != 1 || snapshot.ToolLoopWarningCount != 1 ||
		snapshot.FirstToolLoopWarningLLMCall != 1 ||
		!reflect.DeepEqual(snapshot.ToolLoopWarningLLMCalls, []int{1}) {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	// Detector reset after T,T means a third T only establishes a new baseline.
	runToolLoopBatch(t, modelCB, toolCB, first, results)
	next := &model.Request{Messages: []model.Message{model.NewUserMessage("task")}}
	if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: next}); err != nil {
		t.Fatal(err)
	}
	if len(next.Messages) != 1 {
		t.Fatalf("third identical batch injected a warning: %#v", next.Messages)
	}
	runToolLoopBatch(t, modelCB, toolCB, second, results)
	fourth := &model.Request{}
	if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: fourth}); err != nil {
		t.Fatal(err)
	}
	if len(fourth.Messages) != 1 || fourth.Messages[0].Content != toolloop.Warning {
		t.Fatalf("fourth identical batch warning = %#v", fourth.Messages)
	}
	allWarnings := state.Snapshot()
	if allWarnings.ToolLoopWarningCount != 2 ||
		!reflect.DeepEqual(allWarnings.ToolLoopWarningLLMCalls, []int{1, 3}) {
		t.Fatalf("all warning calls = %#v", allWarnings)
	}
}

func TestToolLoopTrackerRequiresOrderedCompleteBatch(t *testing.T) {
	tracker := newToolLoopTracker(true)
	calls := []model.ToolCall{
		bashToolCallForLoopTest("a", `{"command":"pwd"}`),
		bashToolCallForLoopTest("b", `{"command":"git status"}`),
	}
	tracker.start(calls)
	tracker.add("a", "bash", calls[0].Function.Arguments, "one")
	if tracker.takeWarning() {
		t.Fatal("incomplete batch produced a warning")
	}
	// Starting a new response while a result is missing resets history.
	tracker.start(calls)
	tracker.add("b", "bash", calls[1].Function.Arguments, "two")
	if tracker.takeWarning() {
		t.Fatal("out-of-order result produced a warning")
	}
	for i := 0; i < 2; i++ {
		tracker.start(calls)
		tracker.add("a", "bash", calls[0].Function.Arguments, "one")
		tracker.add("b", "bash", calls[1].Function.Arguments, "two")
	}
	if !tracker.takeWarning() {
		t.Fatal("two complete ordered batches did not produce a warning")
	}
}

func TestPartialPreservesButIncompleteAndInvalidResponsesReset(t *testing.T) {
	tests := []struct {
		name       string
		interrupt  *model.AfterModelArgs
		wantErr    bool
		wantResult bool
		preserves  bool
	}{
		{
			name: "partial",
			interrupt: &model.AfterModelArgs{Response: &model.Response{
				IsPartial: true,
			}},
			preserves: true,
		},
		{
			name: "nonpartial bridge chunk",
			interrupt: &model.AfterModelArgs{Response: &model.Response{
				Done: false,
			}},
			preserves: true,
		},
		{
			name:      "nil response",
			interrupt: &model.AfterModelArgs{},
		},
		{
			name: "model error",
			interrupt: &model.AfterModelArgs{
				Response: &model.Response{Done: true}, Error: errors.New("model failed"),
			},
		},
		{
			name: "no choice",
			interrupt: &model.AfterModelArgs{Response: &model.Response{
				Done: true,
			}},
			wantErr: true,
		},
		{
			name: "format error",
			interrupt: &model.AfterModelArgs{Response: &model.Response{
				Done:    true,
				Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant}}},
			}},
			wantResult: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &State{}
			tracker := newToolLoopTracker(true)
			modelCB := modelCallbacks(state, tracker)
			toolCB := toolCallbacks(state, observation.ObservationCodecXML, tracker)
			call := []model.ToolCall{bashToolCallForLoopTest("baseline", `{"command":"pwd"}`)}
			result := []sweenv.CommandResult{{Output: "/repo\n"}}
			runToolLoopBatch(t, modelCB, toolCB, call, result)

			got, err := modelCB.RunAfterModel(context.Background(), test.interrupt)
			if (err != nil) != test.wantErr {
				t.Fatalf("RunAfterModel() error = %v, wantErr %v", err, test.wantErr)
			}
			if (got != nil) != test.wantResult {
				t.Fatalf("RunAfterModel() result = %#v, wantResult %v", got, test.wantResult)
			}
			repeat := []model.ToolCall{bashToolCallForLoopTest("repeat", `{"command":"pwd"}`)}
			runToolLoopBatch(t, modelCB, toolCB, repeat, result)
			request := &model.Request{}
			if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: request}); err != nil {
				t.Fatal(err)
			}
			warned := len(request.Messages) == 1 && request.Messages[0].Content == toolloop.Warning
			if warned != test.preserves {
				t.Fatalf("warned = %v, want %v", warned, test.preserves)
			}
		})
	}
}

func TestToolAndFormattingFailuresResetLoopHistory(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(t *testing.T, callbacks *tool.Callbacks, call model.ToolCall)
	}{
		{
			name: "tool error",
			run: func(t *testing.T, callbacks *tool.Callbacks, call model.ToolCall) {
				_, err := callbacks.RunAfterTool(context.Background(), &tool.AfterToolArgs{
					ToolCallID: call.ID, ToolName: "bash", Arguments: call.Function.Arguments,
					Error: errors.New("tool failed"),
				})
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "format error",
			run: func(t *testing.T, callbacks *tool.Callbacks, call model.ToolCall) {
				_, err := callbacks.RunToolResultMessages(context.Background(), &tool.ToolResultMessagesInput{
					ToolCallID: call.ID, ToolName: "bash", Arguments: call.Function.Arguments,
					Result: sweenv.CommandResult{Output: "/repo\n"},
				})
				if err == nil {
					t.Fatal("invalid codec did not fail")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &State{}
			tracker := newToolLoopTracker(true)
			modelCB := modelCallbacks(state, tracker)
			validToolCB := toolCallbacks(state, observation.ObservationCodecXML, tracker)
			call := []model.ToolCall{bashToolCallForLoopTest("baseline", `{"command":"pwd"}`)}
			result := []sweenv.CommandResult{{Output: "/repo\n"}}
			runToolLoopBatch(t, modelCB, validToolCB, call, result)

			interruptCall := bashToolCallForLoopTest("interrupt", `{"command":"pwd"}`)
			startToolLoopBatch(t, modelCB, []model.ToolCall{interruptCall})
			callbacks := validToolCB
			if test.name == "format error" {
				callbacks = toolCallbacks(state, observation.ObservationCodec("invalid"), tracker)
			}
			test.run(t, callbacks, interruptCall)

			repeat := []model.ToolCall{bashToolCallForLoopTest("repeat", `{"command":"pwd"}`)}
			runToolLoopBatch(t, modelCB, validToolCB, repeat, result)
			request := &model.Request{}
			if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: request}); err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) != 0 {
				t.Fatalf("failure retained loop history: %#v", request.Messages)
			}
		})
	}
}

func TestSubmissionClearsPendingWarning(t *testing.T) {
	state := &State{}
	tracker := newToolLoopTracker(true)
	modelCB := modelCallbacks(state, tracker)
	toolCB := toolCallbacks(state, observation.ObservationCodecXML, tracker)
	result := []sweenv.CommandResult{{Output: "same"}}
	runToolLoopBatch(t, modelCB, toolCB,
		[]model.ToolCall{bashToolCallForLoopTest("one", `{"command":"pwd"}`)}, result)
	runToolLoopBatch(t, modelCB, toolCB,
		[]model.ToolCall{bashToolCallForLoopTest("two", `{"command":"pwd"}`)}, result)
	_, err := toolCB.RunAfterTool(context.Background(), &tool.AfterToolArgs{
		ToolCallID: "submit", ToolName: "bash", Arguments: []byte(`{"command":"submit"}`),
		Result: sweenv.CommandResult{Output: protocol.SubmissionMarker + "\npatch"},
	})
	if err == nil {
		t.Fatal("submission did not stop the agent")
	}
	request := &model.Request{}
	if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: request}); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 0 || state.Snapshot().ToolLoopWarningCount != 0 {
		t.Fatalf("submission retained pending warning: %#v", state.Snapshot())
	}
}

func TestDisabledLoopWarningLeavesRequestUnchanged(t *testing.T) {
	state := &State{}
	tracker := newToolLoopTracker(false)
	modelCB := modelCallbacks(state, tracker)
	toolCB := toolCallbacks(state, observation.ObservationCodecXML, tracker)
	result := []sweenv.CommandResult{{Output: "same"}}
	for _, id := range []string{"one", "two"} {
		runToolLoopBatch(t, modelCB, toolCB,
			[]model.ToolCall{bashToolCallForLoopTest(id, `{"command":"pwd"}`)}, result)
	}
	request := &model.Request{Messages: []model.Message{model.NewUserMessage("task")}}
	want := append([]model.Message(nil), request.Messages...)
	if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: request}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Messages, want) {
		t.Fatalf("disabled request changed from %#v to %#v", want, request.Messages)
	}
	if got := state.Snapshot(); got.LLMCalls != 1 || got.ToolLoopWarningCount != 0 ||
		got.FirstToolLoopWarningLLMCall != 0 || len(got.ToolLoopWarningLLMCalls) != 0 {
		t.Fatalf("disabled snapshot = %#v", got)
	}
}

func TestWarningTelemetryUsesActualCallAtMaxBoundary(t *testing.T) {
	state := &State{}
	tracker := newToolLoopTracker(true)
	modelCB := modelCallbacks(state, tracker)
	toolCB := toolCallbacks(state, observation.ObservationCodecXML, tracker)
	for i := 0; i < MaxLLMCalls-1; i++ {
		state.recordModelCall()
	}
	result := []sweenv.CommandResult{{Output: "same"}}
	for _, id := range []string{"one", "two"} {
		runToolLoopBatch(t, modelCB, toolCB,
			[]model.ToolCall{bashToolCallForLoopTest(id, `{"command":"pwd"}`)}, result)
	}
	if got := state.Snapshot(); got.ToolLoopWarningCount != 0 {
		t.Fatalf("warning counted without next call: %#v", got)
	}
	request := &model.Request{}
	if _, err := modelCB.RunBeforeModel(context.Background(), &model.BeforeModelArgs{Request: request}); err != nil {
		t.Fatal(err)
	}
	got := state.Snapshot()
	if got.LLMCalls != MaxLLMCalls || got.FirstToolLoopWarningLLMCall != MaxLLMCalls ||
		!reflect.DeepEqual(got.ToolLoopWarningLLMCalls, []int{MaxLLMCalls}) {
		t.Fatalf("warning calls = %#v", got)
	}
	got.ToolLoopWarningLLMCalls[0] = 99
	if state.Snapshot().ToolLoopWarningLLMCalls[0] != MaxLLMCalls {
		t.Fatal("warning call snapshot aliases mutable state")
	}
}

func runToolLoopBatch(
	t *testing.T,
	modelCB *model.Callbacks,
	toolCB *tool.Callbacks,
	calls []model.ToolCall,
	results []sweenv.CommandResult,
) {
	t.Helper()
	if len(calls) != len(results) {
		t.Fatalf("calls = %d, results = %d", len(calls), len(results))
	}
	startToolLoopBatch(t, modelCB, calls)
	for i, call := range calls {
		message, err := toolCB.RunToolResultMessages(context.Background(), &tool.ToolResultMessagesInput{
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Arguments:  call.Function.Arguments,
			Result:     results[i],
		})
		if err != nil {
			t.Fatalf("RunToolResultMessages(%d) error = %v", i, err)
		}
		if _, ok := message.(model.Message); !ok {
			t.Fatalf("tool message %d type = %T", i, message)
		}
	}
}

func startToolLoopBatch(t *testing.T, callbacks *model.Callbacks, calls []model.ToolCall) {
	t.Helper()
	response := &model.Response{
		Done: true,
		Choices: []model.Choice{{Message: model.Message{
			Role: model.RoleAssistant, ToolCalls: calls,
		}}},
	}
	if _, err := callbacks.RunAfterModel(context.Background(), &model.AfterModelArgs{Response: response}); err != nil {
		t.Fatalf("RunAfterModel() error = %v", err)
	}
}

func bashToolCallForLoopTest(id string, arguments string) model.ToolCall {
	return model.ToolCall{
		ID: id,
		Function: model.FunctionDefinitionParam{
			Name: "bash", Arguments: []byte(arguments),
		},
	}
}
