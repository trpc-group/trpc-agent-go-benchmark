//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package loopreplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/toolloop"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestReplayMatchesSharedDetectorForCanonicalMultiToolBatch(t *testing.T) {
	firstCalls := []model.ToolCall{
		bashCall("a-1", `{"command":"pwd","timeout":1}`),
		bashCall("a-2", `{"command":"git status"}`),
	}
	secondCalls := []model.ToolCall{
		bashCall("b-1", "{\n\"timeout\":1,\"command\":\"pwd\"}"),
		bashCall("b-2", `{ "command" : "git status" }`),
	}
	entries := []toolloop.Entry{
		{ToolName: "bash", Arguments: []byte(firstCalls[0].Function.Arguments), Observation: "one"},
		{ToolName: "bash", Arguments: []byte(firstCalls[1].Function.Arguments), Observation: "two"},
	}
	detector := &toolloop.Detector{}
	if warning, err := detector.Observe(entries); err != nil || warning {
		t.Fatalf("first Observe() = %v, %v", warning, err)
	}
	canonicalEntries := []toolloop.Entry{
		{ToolName: "bash", Arguments: []byte(secondCalls[0].Function.Arguments), Observation: "one"},
		{ToolName: "bash", Arguments: []byte(secondCalls[1].Function.Arguments), Observation: "two"},
	}
	if warning, err := detector.Observe(canonicalEntries); err != nil || !warning {
		t.Fatalf("second Observe() = %v, %v", warning, err)
	}
	canonical, err := toolloop.CanonicalBatch(canonicalEntries)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(canonical)

	result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 4}
	result.Events = []*event.Event{
		assistantEvent(firstCalls...), toolEvent(firstCalls, "one", "two"),
		assistantEvent(secondCalls...), toolEvent(secondCalls, "one", "two"),
		assistantEvent(bashCall("c-1", `{"command":"next"}`)),
	}
	report := mustReplay(t, result)
	if report.WarningCount != 1 || report.FirstWarningLLMCall != 3 {
		t.Fatalf("warning summary = %+v", report)
	}
	if got, want := report.WarningLLMCalls, []int{3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning calls = %v, want %v", got, want)
	}
	warning := report.Warnings[0]
	if warning.RepeatCompletedEventIndex != 4 || warningEventIndex(warning) != 5 ||
		warning.WouldInjectBeforeLLMCall != 3 || !warning.InjectionEventObserved || warning.ToolCount != 2 ||
		warning.BatchSHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("warning event = %+v", warning)
	}
	if report.PostFirstWarningTrace.LLMCallsAfterFirstWarning == nil ||
		*report.PostFirstWarningTrace.LLMCallsAfterFirstWarning != 1 ||
		report.PostFirstWarningTrace.ToolCallsAfterFirstWarning == nil ||
		*report.PostFirstWarningTrace.ToolCallsAfterFirstWarning != 0 {
		t.Fatalf("post-warning trace = %+v", report.PostFirstWarningTrace)
	}
}

func TestReplayPartialAndBridgeEventsRetainPendingWarning(t *testing.T) {
	callsA := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	callsB := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	partialTool := toolEvent(callsA, "ignored")
	partialTool.Response.IsPartial = true
	partialTool.Response.Choices[0].Delta = partialTool.Response.Choices[0].Message
	partialTool.Response.Choices[0].Message = model.Message{}
	bridge := &event.Event{Response: &model.Response{
		Object:  model.ObjectTypeChatCompletionChunk,
		Choices: []model.Choice{{Delta: model.Message{Role: model.RoleAssistant, Content: "bridge"}}},
	}}
	result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
	result.Events = []*event.Event{
		assistantEvent(callsA...), partialTool, toolEvent(callsA, "same"),
		assistantEvent(callsB...), toolEvent(callsB, "same"),
		bridge,
		assistantEvent(bashCall("c", `{"command":"next"}`)),
	}
	result.Responses = modelResponses(result.Events...)
	result.responsesLoaded = true
	report := mustReplay(t, result)
	if report.WarningCount != 1 {
		t.Fatalf("warning count = %d, want 1", report.WarningCount)
	}
	warning := report.Warnings[0]
	if warningEventIndex(warning) != 6 || warning.WouldInjectBeforeLLMCall != 3 {
		t.Fatalf("warning = %+v, bridge must not consume it or increment the call", warning)
	}
}

func TestReplayFormatRecoveryUsesOriginalResponseAndResets(t *testing.T) {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	invalid := assistantTextResponse("forgot the tool")
	recovery := formatRecoveryEvent(t, invalid)
	next := assistantEvent(bashCall("next", `{"command":"next"}`))
	result := nativeResult{InstanceID: "case-a", LLMCalls: 4, ToolCalls: 2}
	result.Events = []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"), recovery,
		assistantEvent(second...), toolEvent(second, "same"), next,
	}
	result.Responses = []*model.Response{
		result.Events[0].Response.Clone(), invalid.Clone(),
		result.Events[3].Response.Clone(), next.Response.Clone(),
	}
	result.responsesLoaded = true
	report := mustReplay(t, result)
	if report.WarningCount != 0 || report.FormatRecoveryResetCount != 1 {
		t.Fatalf("report = %+v, want one format reset and no cross-reset warning", report)
	}
}

func TestReplayPendingWarningIsConsumedByFormatRecoveryCall(t *testing.T) {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	invalid := assistantTextResponse("forgot the tool")
	recovery := formatRecoveryEvent(t, invalid)
	result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
	result.Events = []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"),
		assistantEvent(second...), toolEvent(second, "same"), recovery,
	}
	result.Responses = []*model.Response{
		result.Events[0].Response.Clone(), result.Events[2].Response.Clone(), invalid.Clone(),
	}
	result.responsesLoaded = true
	report := mustReplay(t, result)
	if report.WarningCount != 1 || report.FormatRecoveryResetCount != 1 ||
		warningEventIndex(report.Warnings[0]) != 5 ||
		report.Warnings[0].WouldInjectBeforeLLMCall != 3 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReplayPendingWarningCountsNextModelErrorThenResets(t *testing.T) {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	modelError := &event.Event{Response: &model.Response{
		Object: model.ObjectTypeError,
		Error:  &model.ResponseError{Message: "endpoint failed"},
	}}
	result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
	result.Events = []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"),
		assistantEvent(second...), toolEvent(second, "same"), modelError,
	}
	result.Responses = []*model.Response{
		result.Events[0].Response.Clone(), result.Events[2].Response.Clone(), modelError.Response.Clone(),
	}
	if !responsesEqual(modelError.Response, result.Responses[2]) {
		t.Fatal("cloned model error does not align")
	}
	result.responsesLoaded = true
	report := mustReplay(t, result)
	if report.WarningCount != 1 || warningEventIndex(report.Warnings[0]) != 5 ||
		report.Warnings[0].WouldInjectBeforeLLMCall != 3 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReplayPendingWarningErrorBoundaries(t *testing.T) {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	prefix := []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"),
		assistantEvent(second...), toolEvent(second, "same"),
	}
	t.Run("max calls is pre-BeforeModel", func(t *testing.T) {
		result := nativeResult{InstanceID: "case-a", LLMCalls: 2, ToolCalls: 2}
		result.Events = append(append([]*event.Event{}, prefix...), &event.Event{Response: &model.Response{
			Object: model.ObjectTypeError,
			Error: &model.ResponseError{
				Type: agent.ErrorTypeStopAgentError, Message: "max LLM calls (250) exceeded",
			},
		}})
		if report := mustReplay(t, result); report.WarningCount != 0 {
			t.Fatalf("report = %+v", report)
		}
	})
	t.Run("tool or flow error is not a model call", func(t *testing.T) {
		result := nativeResult{InstanceID: "case-a", LLMCalls: 2, ToolCalls: 2}
		result.Events = append(append([]*event.Event{}, prefix...), &event.Event{Response: &model.Response{
			Object: model.ObjectTypeError, Error: &model.ResponseError{Message: "flow failed"},
		}})
		result.Responses = []*model.Response{prefix[0].Response.Clone(), prefix[2].Response.Clone()}
		result.responsesLoaded = true
		if report := mustReplay(t, result); report.WarningCount != 0 {
			t.Fatalf("report = %+v", report)
		}
	})
	t.Run("response-less model error consumes warning", func(t *testing.T) {
		result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
		result.Events = append(append([]*event.Event{}, prefix...), &event.Event{Response: &model.Response{
			Object: model.ObjectTypeError, Error: &model.ResponseError{Message: "endpoint failed"},
		}})
		result.Responses = []*model.Response{prefix[0].Response.Clone(), prefix[2].Response.Clone()}
		result.responsesLoaded = true
		report := mustReplay(t, result)
		if report.WarningCount != 1 || report.Warnings[0].WouldInjectBeforeLLMCall != 3 ||
			warningEventIndex(report.Warnings[0]) != 5 {
			t.Fatalf("report = %+v", report)
		}
	})
}

func TestReplayCaseTimeoutAccountsForUnobservedFinalBeforeModel(t *testing.T) {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
	result.Info.ErrorCategory = protocol.ErrorCategoryCaseTimeout
	result.Events = []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"),
		assistantEvent(second...), toolEvent(second, "same"),
	}
	result.Responses = []*model.Response{
		result.Events[0].Response.Clone(), result.Events[2].Response.Clone(),
	}
	result.responsesLoaded = true
	report := mustReplay(t, result)
	if report.WarningCount != 1 || report.Warnings[0].WouldInjectBeforeLLMCall != 3 ||
		report.Warnings[0].InjectionEventObserved ||
		report.Warnings[0].WouldInjectBeforeEventIndex != nil {
		t.Fatalf("report = %+v", report)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reportJSON), `"would_inject_before_event_index":null`) {
		t.Fatalf("timeout warning does not carry a nullable event index: %s", reportJSON)
	}

	result.Info.ErrorCategory = protocol.ErrorCategoryAgent
	if _, err := replayCase(result); err == nil || !strings.Contains(err.Error(), "llm_calls") {
		t.Fatalf("replayCase() error = %v, want non-timeout missing-call rejection", err)
	}
	result.Info.ErrorCategory = protocol.ErrorCategoryCaseTimeout
	result.LLMCalls = 4
	if _, err := replayCase(result); err == nil || !strings.Contains(err.Error(), "llm_calls") {
		t.Fatalf("replayCase() error = %v, want multi-call timeout gap rejection", err)
	}
}

func TestResponseAlignmentIgnoresOnlyEventEnvelopeIdentity(t *testing.T) {
	evt := assistantEvent(bashCall("a", `{"command":"pwd"}`))
	evt.Response.ID = "event-id"
	evt.Response.Timestamp = time.Unix(100, 100)
	original := evt.Response.Clone()
	original.ID = "provider-id"
	original.Timestamp = time.Unix(200, 200)
	result := nativeResult{
		InstanceID: "case-a", LLMCalls: 1, Events: []*event.Event{evt},
		Responses: []*model.Response{original}, responsesLoaded: true,
	}
	if _, err := replayCase(result); err != nil {
		t.Fatalf("replayCase() error = %v, envelope-only difference must align", err)
	}
	original.Choices[0].Message.Content = "semantic mismatch"
	if _, err := replayCase(result); err == nil || !strings.Contains(err.Error(), "does not align") {
		t.Fatalf("replayCase() error = %v, want semantic mismatch", err)
	}
}

func TestResponseAlignmentDoesNotSkipMissingRawResponses(t *testing.T) {
	terminal := assistantEvent(bashCall("a", `{"command":"pwd"}`))
	nonPartialBridge := &model.Response{
		Object: model.ObjectTypeChatCompletionChunk, Done: false,
		Choices: []model.Choice{{Delta: model.Message{Role: model.RoleAssistant, Content: "bridge"}}},
	}
	result := nativeResult{
		InstanceID: "case-a", LLMCalls: 1, Events: []*event.Event{terminal},
		Responses:       []*model.Response{nonPartialBridge, terminal.Response.Clone()},
		responsesLoaded: true,
	}
	if _, err := replayCase(result); err == nil || !strings.Contains(err.Error(), "does not align") {
		t.Fatalf("replayCase() error = %v, want missing non-partial bridge failure", err)
	}

	bufferedPartial := nonPartialBridge.Clone()
	bufferedPartial.IsPartial = true
	result.Responses = []*model.Response{bufferedPartial, terminal.Response.Clone()}
	if _, err := replayCase(result); err == nil || !strings.Contains(err.Error(), "does not align") {
		t.Fatalf("replayCase() error = %v, want missing partial failure", err)
	}
}

func TestRunFailsClosedOnResponseAlignmentMismatch(t *testing.T) {
	runDir := t.TempDir()
	evt := assistantEvent(bashCall("a", `{"command":"pwd"}`))
	different := assistantEvent(bashCall("b", `{"command":"ls"}`)).Response
	writeBundle(t, runDir, nativeResult{
		InstanceID: "case-a", LLMCalls: 1, Events: []*event.Event{evt},
		Responses: []*model.Response{different}, responsesLoaded: true,
	})
	output := filepath.Join(t.TempDir(), "shadow.json")
	err := Run([]string{"tool-loop-shadow-replay", "--run-dir", runDir, "--output", output})
	if err == nil || !strings.Contains(err.Error(), "does not align") {
		t.Fatalf("Run() error = %v, want fail-closed alignment error", err)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output exists after failed replay: %v", err)
	}
}

func TestReplayDoesNotCountPendingWarningWithoutNextModelCall(t *testing.T) {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	result := nativeResult{InstanceID: "case-a", LLMCalls: 2, ToolCalls: 2}
	result.Events = []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"),
		assistantEvent(second...), toolEvent(second, "same"),
	}
	if report := mustReplay(t, result); report.WarningCount != 0 {
		t.Fatalf("warning count = %d, want 0 for terminal pending repeat", report.WarningCount)
	}
}

func TestReplayAcceptsCurrentNativeSubmittedTerminalToolGaps(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls int
	}{
		{name: "submit first", toolCalls: 1},
		{name: "ordinary then submit", toolCalls: 2},
		{name: "submit after full prefix", toolCalls: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := currentNativeSubmittedResult(
				[]model.ToolCall{
					bashCall("one", `{"command":"pwd"}`),
					bashCall("two", `{"command":"git status"}`),
					bashCall("three", `{"command":"submit"}`),
				},
				test.toolCalls,
			)
			report, err := replayCase(result)
			if err != nil {
				t.Fatal(err)
			}
			if report.WarningCount != 0 || !report.PostFirstWarningTrace.Submitted ||
				report.PostFirstWarningTrace.TotalToolCalls != test.toolCalls {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestReplayAcceptsCurrentExecutorSubmissionStopShape(t *testing.T) {
	calls := []model.ToolCall{
		bashCall("ordinary", `{"command":"pwd"}`),
		bashCall("submit", `{"command":"submit"}`),
		bashCall("unexecuted", `{"command":"touch never"}`),
	}
	modelImpl := &replayExecutorModel{response: assistantEvent(calls...).Response}
	environment := &replayExecutorEnvironment{results: []sweenv.CommandResult{
		{Output: "/testbed\n"},
		{Output: protocol.SubmissionMarker + "\npatch\n"},
	}}
	exec := executor.Executor{
		Factory: replayExecutorFactory{environment: environment},
		ModelFactory: func(modelconfig.EnvConfig) model.Model {
			return modelImpl
		},
		ObservationCodec: observation.ObservationCodecXML,
		Workers:          1,
	}
	actual := exec.Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)
	if actual.Info.ExitStatus != "Submitted" || actual.LLMCalls != 1 || actual.ToolCalls != 2 {
		t.Fatalf("executor result = %+v", actual)
	}
	if got, want := environment.commands, []string{"pwd", "submit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	for _, evt := range actual.Events {
		if evt != nil && evt.Response != nil && evt.Response.Object == model.ObjectTypeToolResponse {
			t.Fatal("current framework unexpectedly persisted the discarded final tool-response batch")
		}
	}

	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var persisted nativeResult
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	persisted.Responses = actual.Responses
	persisted.responsesLoaded = true
	persisted.modernNative = true
	report, err := replayCase(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PostFirstWarningTrace.Submitted ||
		report.PostFirstWarningTrace.TotalToolCalls != 2 || report.WarningCount != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReplayRejectsInvalidCurrentNativeSubmissionBoundaries(t *testing.T) {
	base := func() nativeResult {
		return currentNativeSubmittedResult(
			[]model.ToolCall{bashCall("submit", `{"command":"submit"}`)}, 1,
		)
	}
	tests := []struct {
		name   string
		mutate func(*nativeResult)
		want   string
	}{
		{name: "missing boundary", mutate: func(result *nativeResult) {
			result.Events = result.Events[:1]
		}, want: "no exact submission StopError boundary"},
		{name: "wrong status", mutate: func(result *nativeResult) {
			result.Info.ExitStatus = "Error"
		}, want: "terminal metadata"},
		{name: "terminal error text", mutate: func(result *nativeResult) {
			result.Info.Error = "unexpected"
		}, want: "terminal error metadata"},
		{name: "terminal error category", mutate: func(result *nativeResult) {
			result.Info.ErrorCategory = protocol.ErrorCategoryAgent
		}, want: "terminal error metadata"},
		{name: "terminal retryable", mutate: func(result *nativeResult) {
			result.Info.Retryable = true
		}, want: "terminal error metadata"},
		{name: "wrong boundary type", mutate: func(result *nativeResult) {
			result.Events[1].Response.Error.Type = agent.ErrorTypeStopAgentError
		}, want: "no exact submission StopError boundary"},
		{name: "wrong boundary message", mutate: func(result *nativeResult) {
			result.Events[1].Response.Error.Message = "different"
		}, want: "no exact submission StopError boundary"},
		{name: "nonterminal boundary", mutate: func(result *nativeResult) {
			result.Events[1].Response.Done = false
		}, want: "no exact submission StopError boundary"},
		{name: "no pending batch", mutate: func(result *nativeResult) {
			result.Events = result.Events[1:]
			result.Responses = nil
			result.LLMCalls = 0
		}, want: "no incomplete final tool batch"},
		{name: "completed batch", mutate: func(result *nativeResult) {
			calls := result.Events[0].Response.Choices[0].Message.ToolCalls
			result.Events = []*event.Event{
				result.Events[0], toolEvent(calls, "done"), result.Events[1],
			}
			result.ToolCalls = 2
		}, want: "no incomplete final tool batch"},
		{name: "zero tool gap", mutate: func(result *nativeResult) {
			result.ToolCalls = 0
		}, want: "submission tool gap=0"},
		{name: "tool gap exceeds batch", mutate: func(result *nativeResult) {
			result.ToolCalls = 2
		}, want: "outside final pending batch"},
		{name: "llm total mismatch", mutate: func(result *nativeResult) {
			result.LLMCalls = 2
		}, want: "reconstructed llm_calls=1"},
		{name: "duplicate boundary", mutate: func(result *nativeResult) {
			result.Events = append(result.Events, submissionStopEvent())
		}, want: "follows the terminal submission"},
		{name: "assistant after boundary", mutate: func(result *nativeResult) {
			result.Events = append(result.Events, assistantEvent(
				bashCall("later", `{"command":"pwd"}`),
			))
		}, want: "follows the terminal submission"},
		{name: "tool result after boundary", mutate: func(result *nativeResult) {
			result.Events = append(result.Events, toolEvent(
				[]model.ToolCall{bashCall("later", `{"command":"pwd"}`)}, "late",
			))
		}, want: "follows the terminal submission"},
		{name: "choice semantic event after boundary", mutate: func(result *nativeResult) {
			result.Events = append(result.Events, &event.Event{Response: &model.Response{
				Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant}}},
			}})
		}, want: "follows the terminal submission"},
		{name: "detached response missing", mutate: func(result *nativeResult) {
			result.Responses = nil
		}, want: "does not align"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := base()
			test.mutate(&result)
			_, err := replayCase(result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("replayCase() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReplayReportsMultipleWarningsAfterDetectorReset(t *testing.T) {
	result := nativeResult{InstanceID: "case-a", LLMCalls: 5, ToolCalls: 4}
	for i := 0; i < 4; i++ {
		calls := []model.ToolCall{bashCall(string(rune('a'+i)), `{"command":"pwd"}`)}
		result.Events = append(result.Events, assistantEvent(calls...), toolEvent(calls, "same"))
	}
	result.Events = append(result.Events, assistantEvent(bashCall("e", `{"command":"next"}`)))
	report := mustReplay(t, result)
	if got, want := report.WarningLLMCalls, []int{3, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning calls = %v, want %v", got, want)
	}
	if report.Warnings[0].RepeatCompletedEventIndex != 4 ||
		report.Warnings[1].RepeatCompletedEventIndex != 8 {
		t.Fatalf("warnings = %+v", report.Warnings)
	}
}

func TestReplayResetsOnFormatIncompleteErrorAndMismatch(t *testing.T) {
	tests := []struct {
		name       string
		interrupt  *event.Event
		secondTool *event.Event
	}{
		{
			name: "format",
			interrupt: &event.Event{Response: &model.Response{
				Object: model.ObjectTypeChatCompletion, Done: true,
				Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant, Content: "text only"}}},
			}},
		},
		{
			name: "model response error",
			interrupt: &event.Event{Response: &model.Response{
				Object: model.ObjectTypeChatCompletion, Done: true,
				Error: &model.ResponseError{Message: "failed"},
			}},
		},
		{
			name: "tool error",
			secondTool: &event.Event{Response: &model.Response{
				Object: model.ObjectTypeToolResponse,
				Error:  &model.ResponseError{Message: "tool failed"},
			}},
		},
		{
			name: "mismatched id",
			secondTool: toolEvent(
				[]model.ToolCall{bashCall("wrong", `{"command":"pwd"}`)}, "same",
			),
		},
		{
			name: "mismatched name",
			secondTool: func() *event.Event {
				evt := toolEvent(
					[]model.ToolCall{bashCall("b", `{"command":"pwd"}`)}, "same",
				)
				evt.Response.Choices[0].Message.ToolName = "shell"
				return evt
			}(),
		},
		{
			name: "incomplete tool batch",
			secondTool: toolEvent(
				[]model.ToolCall{bashCall("b", `{"command":"pwd"}`)}, "same",
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
			second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
			if test.name == "incomplete tool batch" {
				second = append(second, bashCall("b-2", `{"command":"git status"}`))
			}
			events := []*event.Event{assistantEvent(first...), toolEvent(first, "same")}
			if test.interrupt != nil {
				events = append(events, test.interrupt)
			}
			events = append(events, assistantEvent(second...))
			if test.secondTool != nil {
				events = append(events, test.secondTool)
			} else {
				events = append(events, toolEvent(second, "same"))
			}
			events = append(events, assistantEvent(bashCall("next", `{"command":"next"}`)))
			llmCalls := 3
			if test.interrupt != nil {
				llmCalls++
			}
			result := nativeResult{InstanceID: "case-a", LLMCalls: llmCalls, ToolCalls: 2, Events: events}
			if report := mustReplay(t, result); report.WarningCount != 0 {
				t.Fatalf("warning count = %d, want reset", report.WarningCount)
			}
		})
	}
}

func TestReplayErrorsResetBeforePartialStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *model.Response
	}{
		{
			name: "partial model error",
			response: &model.Response{
				Object: model.ObjectTypeChatCompletionChunk, IsPartial: true,
				Error: &model.ResponseError{Message: "stream failed"},
			},
		},
		{
			name: "Done=false model error",
			response: &model.Response{
				Object: model.ObjectTypeChatCompletion, Done: false,
				Error: &model.ResponseError{Message: "bridge failed"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
			second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
			result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
			result.Events = []*event.Event{
				assistantEvent(first...), toolEvent(first, "same"),
				{Response: test.response},
				assistantEvent(second...), toolEvent(second, "same"),
				assistantEvent(bashCall("next", `{"command":"next"}`)),
			}
			if report := mustReplay(t, result); report.WarningCount != 0 {
				t.Fatalf("report = %+v", report)
			}
		})
	}

	t.Run("non-terminal model error without terminal response fails closed", func(t *testing.T) {
		result := nativeResult{InstanceID: "case-a", LLMCalls: 1}
		result.Events = []*event.Event{{Response: &model.Response{
			Object: model.ObjectTypeChatCompletionChunk, IsPartial: true,
			Error: &model.ResponseError{Message: "stream failed"},
		}}}
		if _, err := replayCase(result); err == nil || !strings.Contains(err.Error(), "no terminal response") {
			t.Fatalf("incomplete error response sequence error = %v", err)
		}
	})

	t.Run("partial tool error", func(t *testing.T) {
		first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
		second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
		result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
		result.Events = []*event.Event{
			assistantEvent(first...),
			{Response: &model.Response{
				Object: model.ObjectTypeToolResponse, IsPartial: true,
				Error: &model.ResponseError{Message: "tool failed"},
			}},
			assistantEvent(second...), toolEvent(second, "same"),
			assistantEvent(bashCall("next", `{"command":"next"}`)),
		}
		if report := mustReplay(t, result); report.WarningCount != 0 {
			t.Fatalf("report = %+v", report)
		}
	})
}

func TestReplayResetsOnOutOfOrderToolResults(t *testing.T) {
	first := []model.ToolCall{
		bashCall("a-1", `{"command":"pwd"}`),
		bashCall("a-2", `{"command":"git status"}`),
	}
	second := []model.ToolCall{
		bashCall("b-1", `{"command":"pwd"}`),
		bashCall("b-2", `{"command":"git status"}`),
	}
	reversed := []model.ToolCall{second[1], second[0]}
	result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 4}
	result.Events = []*event.Event{
		assistantEvent(first...), toolEvent(first, "one", "two"),
		assistantEvent(second...), toolEvent(reversed, "two", "one"),
		assistantEvent(bashCall("next", `{"command":"next"}`)),
	}
	if report := mustReplay(t, result); report.WarningCount != 0 {
		t.Fatalf("warning count = %d, want order mismatch reset", report.WarningCount)
	}
}

func TestReplayDistinguishesChangedBatchFields(t *testing.T) {
	tests := []struct {
		name        string
		secondCalls []model.ToolCall
		observation string
	}{
		{name: "arguments", secondCalls: []model.ToolCall{bashCall("b", `{"command":"ls"}`)}, observation: "same"},
		{name: "observation", secondCalls: []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}, observation: "changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
			result := nativeResult{InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2}
			result.Events = []*event.Event{
				assistantEvent(first...), toolEvent(first, "same"),
				assistantEvent(test.secondCalls...), toolEvent(test.secondCalls, test.observation),
				assistantEvent(bashCall("next", `{"command":"next"}`)),
			}
			if report := mustReplay(t, result); report.WarningCount != 0 {
				t.Fatalf("warning count = %d, want changed batch", report.WarningCount)
			}
		})
	}
}

func TestScanAndRunAreDeterministicAndValidateResponseBoundary(t *testing.T) {
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, "support"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBundle(t, runDir, nativeResult{InstanceID: "case-b", LLMCalls: 0, ToolCalls: 0})
	writeBundle(t, runDir, nativeResult{
		InstanceID: "case-a", LLMCalls: 3, ToolCalls: 2,
		Events: exactRepeatEvents(),
	})
	setNativeSelectedInstances(t, runDir, []string{"case-a", "case-b"})
	first, err := Scan(runDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Scan(runDir)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("Scan() output is not deterministic")
	}
	if first.CasesScanned != 2 || first.WouldWarnCaseCount != 1 ||
		first.WouldWarnEventCount != 1 || first.Cases[0].InstanceID != "case-a" ||
		first.Cases[1].InstanceID != "case-b" {
		t.Fatalf("report = %+v", first)
	}
	if first.Detector.Algorithm != toolloop.Algorithm || first.Detector.Version != toolloop.Version {
		t.Fatalf("detector = %+v", first.Detector)
	}
	if first.Input.RunIdentity.CleanRoom == nil || *first.Input.RunIdentity.CleanRoom {
		t.Fatalf("modern native clean_room identity = %v, want explicit false", first.Input.RunIdentity.CleanRoom)
	}
	if got := first.Input.Artifacts[0]; got.TrajectoryKind != "native-case" ||
		got.TrajectoryPath != "case-a/case-a.native.json" {
		t.Fatalf("modern native fingerprint = %+v", got)
	}
	if !first.Cases[0].PostFirstWarningTrace.Submitted ||
		first.Cases[0].PostFirstWarningTrace.PatchBytes != len("patch") {
		t.Fatalf("trajectory = %+v", first.Cases[0].PostFirstWarningTrace)
	}
	output := filepath.Join(t.TempDir(), "nested", "shadow.json")
	if err := Run([]string{"tool-loop-shadow-replay", "--run-dir", runDir, "--output", output}); err != nil {
		t.Fatal(err)
	}
	var written Report
	if err := artifact.ReadJSONFile(output, &written); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, written) {
		t.Fatalf("written report differs from Scan():\n%+v\n%+v", first, written)
	}
	outputEntries, err := os.ReadDir(filepath.Dir(output))
	if err != nil {
		t.Fatal(err)
	}
	if len(outputEntries) != 1 || outputEntries[0].Name() != filepath.Base(output) {
		t.Fatalf("output directory contains non-atomic leftovers: %v", outputEntries)
	}

	responsesPath := filepath.Join(runDir, "case-a", "case-a.responses.json")
	if err := os.WriteFile(responsesPath, []byte("[]\nchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "responses artifact") {
		t.Fatalf("Scan() error = %v, want responses boundary failure", err)
	}
}

func TestScanFrozenV12TagRunAndFailClosedBoundaries(t *testing.T) {
	runDir := t.TempDir()
	results := make([]nativeResult, legacyTagCaseCount)
	for i := range results {
		results[i] = nativeResult{
			InstanceID: fmt.Sprintf("case-%03d", i),
			Info:       nativeInfo{ExitStatus: "Submitted"},
		}
	}
	results[0].LLMCalls = 3
	results[0].ToolCalls = 2
	results[0].ModelPatch = "patch"
	results[0].Events = exactRepeatEvents()
	manifestPath := writeLegacyTagRun(t, runDir, results)

	first, err := Scan(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.CasesScanned != legacyTagCaseCount || first.WouldWarnCaseCount != 1 ||
		first.WouldWarnEventCount != 1 {
		t.Fatalf("legacy report counts = %+v", first)
	}
	if first.Input.Kind != "legacy-tag-manifest-warning-off-run-directory" ||
		first.Input.ManifestPath != legacyTagManifestFilename ||
		!isHexIdentifier(first.Input.ManifestSHA256, 64) ||
		first.Input.RunIdentity.ArtifactFormat != legacyTagManifestFormat ||
		first.Input.RunIdentity.CleanRoom != nil ||
		first.Input.RunIdentity.SourceRevision != "" {
		t.Fatalf("legacy input metadata = %+v", first.Input)
	}
	if got := first.Input.Artifacts[0]; got.TrajectoryKind != "legacy-tag-case" ||
		got.TrajectoryPath != "case-000/case-000.tag.json" ||
		got.ResponsesPath != "case-000/case-000.responses.json" ||
		!isHexIdentifier(got.TrajectorySHA256, 64) || !isHexIdentifier(got.ResponsesSHA256, 64) {
		t.Fatalf("legacy fingerprint = %+v", got)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestData)
	if first.Input.ManifestSHA256 != hex.EncodeToString(manifestHash[:]) {
		t.Fatalf("manifest SHA = %q, want %x", first.Input.ManifestSHA256, manifestHash)
	}

	mutateNativeJSON(t, manifestPath, func(document map[string]any) {
		document["notes"] = []any{"fingerprinted optional field"}
	})
	withNote, err := Scan(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if withNote.Input.ManifestSHA256 == first.Input.ManifestSHA256 ||
		withNote.Input.SHA256 == first.Input.SHA256 ||
		!runIdentitiesEqual(withNote.Input.RunIdentity, first.Input.RunIdentity) {
		t.Fatalf("optional manifest mutation was not isolated to input fingerprints: before=%+v after=%+v", first.Input, withNote.Input)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		t.Fatal(err)
	}

	mixedPath := filepath.Join(runDir, "case-000", "case-000.native.json")
	if err := os.WriteFile(mixedPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "mixes a native artifact") {
		t.Fatalf("mixed-layout Scan() error = %v", err)
	}
	if err := os.Remove(mixedPath); err != nil {
		t.Fatal(err)
	}

	tagPath := filepath.Join(runDir, "case-000", "case-000.tag.json")
	tagData, err := os.ReadFile(tagPath)
	if err != nil {
		t.Fatal(err)
	}
	mutateNativeJSON(t, tagPath, func(document map[string]any) {
		document["marker_probe"] = toolLoopDetectedMarker
	})
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "contains <tool_loop_detected> marker") {
		t.Fatalf("escaped marker Scan() error = %v", err)
	}
	if err := os.WriteFile(tagPath, tagData, 0o644); err != nil {
		t.Fatal(err)
	}
	responsesPath := filepath.Join(runDir, "case-000", "case-000.responses.json")
	responsesData, err := os.ReadFile(responsesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.WriteJSON(responsesPath, []any{toolLoopDetectedMarker}); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "responses artifact case-000 contains <tool_loop_detected> marker") {
		t.Fatalf("detached escaped marker Scan() error = %v", err)
	}
	if err := os.WriteFile(responsesPath, responsesData, 0o644); err != nil {
		t.Fatal(err)
	}

	mutateNativeJSON(t, manifestPath, func(document map[string]any) {
		document["selected_case_set_sha256"] = strings.Repeat("0", 64)
	})
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "case-set SHA-256") {
		t.Fatalf("selected-set Scan() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		t.Fatal(err)
	}

	removed := filepath.Join(filepath.Dir(runDir), filepath.Base(runDir)+"-removed-case")
	casePath := filepath.Join(runDir, "case-499")
	if err := os.Rename(casePath, removed); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "499 exact case artifacts") {
		t.Fatalf("actual-coverage Scan() error = %v", err)
	}
	if err := os.Rename(removed, casePath); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenV12TagManifestAndCaseWarningGates(t *testing.T) {
	manifest := testTagManifest([]string{"case-a"})
	manifest.CaseCount = legacyTagCaseCount
	manifest.AttemptedCount = legacyTagCaseCount
	manifest.PredictionCount = legacyTagCaseCount
	manifest.Status = "completed_with_errors"
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTagManifest(data, manifest); err != nil {
		t.Fatalf("completed_with_errors legacy manifest rejected: %v", err)
	}
	manifest.ToolLoopWarningCount = 1
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTagManifest(data, manifest); err == nil || !strings.Contains(err.Error(), "warning telemetry") {
		t.Fatalf("manifest warning gate error = %v", err)
	}

	result := nativeResult{InstanceID: "case-a", ToolLoopWarningCount: 1}
	caseData := []byte(`{
  "instance_id":"case-a","info":{"exit_status":"Submitted"},"duration_ms":0,
  "llm_calls":0,"tool_calls":0,"usage":{},"events":[],
  "tool_loop_warning_count":1,"first_tool_loop_warning_llm_call":null
}`)
	if err := validateTagWarningOffArtifact(caseData, result); err == nil ||
		!strings.Contains(err.Error(), "warning telemetry") {
		t.Fatalf("case warning gate error = %v", err)
	}
	firstWarning := 7
	result.ToolLoopWarningCount = 0
	result.FirstToolLoopWarningLLMCall = &firstWarning
	if err := validateTagWarningOffArtifact(caseData, result); err == nil ||
		!strings.Contains(err.Error(), "warning telemetry") {
		t.Fatalf("case first-warning gate error = %v", err)
	}
}

func TestFrozenV12TagTerminalRules(t *testing.T) {
	timeout := nativeResult{
		InstanceID: "timeout", LLMCalls: 213,
		Info: nativeInfo{ExitStatus: "Timeout", Error: "context deadline exceeded",
			ErrorCategory: protocol.ErrorCategoryCaseTimeout},
	}
	if err := validateTagTerminal(timeout, 212); err != nil {
		t.Fatalf("canonical V12 timeout rejected: %v", err)
	}
	limit := nativeResult{
		InstanceID: "limit", LLMCalls: 250,
		Info: nativeInfo{ExitStatus: "Error", Error: "max LLM calls (250) exceeded",
			ErrorCategory: protocol.ErrorCategoryAgent},
	}
	if err := validateTagTerminal(limit, 250); err != nil {
		t.Fatalf("canonical V12 call-limit error rejected: %v", err)
	}
	if err := validateTagTerminal(timeout, 211); err == nil {
		t.Fatal("non-canonical V12 timeout response count was accepted")
	}
}

func TestRunFailurePreservesExistingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "shadow.json")
	if err := os.WriteFile(output, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run([]string{"tool-loop-shadow-replay", "--run-dir", filepath.Join(t.TempDir(), "missing"), "--output", output})
	if err == nil {
		t.Fatal("Run() unexpectedly succeeded")
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "original\n" {
		t.Fatalf("output = %q, want preserved original", data)
	}
}

func TestRunRejectsOverwritingInputArtifact(t *testing.T) {
	runDir := t.TempDir()
	writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
	nativePath := filepath.Join(runDir, "case-a", "case-a.native.json")
	before, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	err = Run([]string{
		"tool-loop-shadow-replay", "--run-dir", runDir, "--output", nativePath,
	})
	if err == nil || !strings.Contains(err.Error(), "outside --run-dir") {
		t.Fatalf("Run() error = %v, want input-overwrite rejection", err)
	}
	after, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("Run() changed its native input artifact")
	}
}

func TestScanRejectsUnsafeOrAmbiguousPaths(t *testing.T) {
	t.Run("run directory symlink", func(t *testing.T) {
		realDir := t.TempDir()
		link := filepath.Join(t.TempDir(), "run-link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(link); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Scan() error = %v, want symlink", err)
		}
	})

	t.Run("top-level symlink", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(runDir, "case-a")); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Scan() error = %v, want symlink", err)
		}
	})

	t.Run("unsafe directory", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(runDir, "bad name"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("Scan() error = %v, want unsafe path", err)
		}
	})

	for _, suffix := range []string{".native.json", ".responses.json", ".tag.json"} {
		t.Run("root case artifact "+suffix, func(t *testing.T) {
			runDir := t.TempDir()
			writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
			if err := os.WriteFile(filepath.Join(runDir, "orphan"+suffix), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "outside an instance directory") {
				t.Fatalf("Scan() error = %v, want root artifact failure", err)
			}
		})
	}

	t.Run("native file symlink", func(t *testing.T) {
		runDir := t.TempDir()
		caseDir := filepath.Join(runDir, "case-a")
		if err := os.Mkdir(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.native.json")
		if err := os.WriteFile(target, []byte(`{"instance_id":"case-a"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(caseDir, "case-a.native.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Scan() error = %v, want symlink", err)
		}
	})

	t.Run("wrong native filename", func(t *testing.T) {
		runDir := t.TempDir()
		caseDir := filepath.Join(runDir, "case-a")
		if err := os.Mkdir(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(caseDir, "other.native.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "exact instance path") {
			t.Fatalf("Scan() error = %v, want exact path failure", err)
		}
	})

	t.Run("extra wrong native filename beside exact bundle", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		if err := os.WriteFile(
			filepath.Join(runDir, "case-a", "other.native.json"), []byte("{}"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "exact instance path") {
			t.Fatalf("Scan() error = %v, want extra native path failure", err)
		}
	})

	t.Run("extra wrong responses filename beside exact bundle", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		if err := os.WriteFile(
			filepath.Join(runDir, "case-a", "other.responses.json"), []byte("[]"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "exact instance path") {
			t.Fatalf("Scan() error = %v, want extra responses path failure", err)
		}
	})

	t.Run("orphan responses artifact", func(t *testing.T) {
		runDir := t.TempDir()
		caseDir := filepath.Join(runDir, "case-a")
		if err := os.Mkdir(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := artifact.WriteJSON(
			filepath.Join(caseDir, "case-a.responses.json"), []*model.Response{},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "orphan responses") {
			t.Fatalf("Scan() error = %v, want orphan response failure", err)
		}
	})

	t.Run("legacy tag artifact without root manifest", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		if err := os.WriteFile(
			filepath.Join(runDir, "case-a", "case-a.tag.json"), []byte("{}\n"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "without an exact root") {
			t.Fatalf("Scan() error = %v, want mixed legacy rejection", err)
		}
	})
}

func TestSameFileSnapshotDetectsInPlaceMetadataChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"changed\":true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("test rewrite unexpectedly replaced the inode")
	}
	if sameFileSnapshot(before, after) {
		t.Fatal("sameFileSnapshot accepted an in-place size/metadata change")
	}
}

func TestScanRequiresPristineWarningOffArtifacts(t *testing.T) {
	t.Run("warning enabled", func(t *testing.T) {
		runDir := t.TempDir()
		info := testNativeInfo()
		info.ToolLoopWarning = true
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a", Info: info})
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "tool_loop_warning=true") {
			t.Fatalf("Scan() error = %v", err)
		}
	})

	tests := []struct {
		name   string
		change func(*nativeResult)
	}{
		{name: "warning count", change: func(result *nativeResult) { result.ToolLoopWarningCount = 1 }},
		{name: "first warning pointer", change: func(result *nativeResult) {
			value := 0
			result.FirstToolLoopWarningLLMCall = &value
		}},
		{name: "warning calls", change: func(result *nativeResult) {
			result.ToolLoopWarningLLMCalls = []int{1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runDir := t.TempDir()
			result := nativeResult{InstanceID: "case-a"}
			test.change(&result)
			writeBundle(t, runDir, result)
			if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "warning telemetry") {
				t.Fatalf("Scan() error = %v", err)
			}
		})
	}

	t.Run("missing explicit warning fields", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		path := filepath.Join(runDir, "case-a", "case-a.native.json")
		mutateNativeJSON(t, path, func(document map[string]any) {
			delete(document, "tool_loop_warning_count")
			info := document["info"].(map[string]any)
			delete(info, "tool_loop_warning")
		})
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "missing required field") {
			t.Fatalf("Scan() error = %v", err)
		}
	})

	t.Run("missing report input field", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		path := filepath.Join(runDir, "case-a", "case-a.native.json")
		mutateNativeJSON(t, path, func(document map[string]any) {
			delete(document, "tool_calls")
		})
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), `missing required field "tool_calls"`) {
			t.Fatalf("Scan() error = %v", err)
		}
	})
}

func TestScanPinsOneCompleteRunIdentity(t *testing.T) {
	t.Run("mixed run identity", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		info := testNativeInfo()
		info.RunID = "other-run"
		writeBundle(t, runDir, nativeResult{InstanceID: "case-b", Info: info})
		setNativeSelectedInstances(t, runDir, []string{"case-a", "case-b"})
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "different run identity") {
			t.Fatalf("Scan() error = %v", err)
		}
	})

	t.Run("missing selected instance artifact", func(t *testing.T) {
		runDir := t.TempDir()
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
		writeBundle(t, runDir, nativeResult{InstanceID: "case-b"})
		setNativeSelectedInstances(t, runDir, []string{"case-a", "case-b"})
		if err := os.RemoveAll(filepath.Join(runDir, "case-b")); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "case-set SHA-256") {
			t.Fatalf("Scan() error = %v, want incomplete selection failure", err)
		}
	})

	t.Run("legacy incomplete identity", func(t *testing.T) {
		runDir := t.TempDir()
		info := testNativeInfo()
		info.SourceRevision = ""
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a", Info: info})
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "full Git revision") {
			t.Fatalf("Scan() error = %v", err)
		}
	})

	t.Run("unknown observation codec", func(t *testing.T) {
		runDir := t.TempDir()
		info := testNativeInfo()
		info.ObservationCodec = "yaml"
		writeBundle(t, runDir, nativeResult{InstanceID: "case-a", Info: info})
		if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "not canonical") {
			t.Fatalf("Scan() error = %v", err)
		}
	})
}

func TestScanRejectsAmbiguousToolErrorChoice(t *testing.T) {
	runDir := t.TempDir()
	result := nativeResult{InstanceID: "case-a", LLMCalls: 1, ToolCalls: 1}
	calls := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	result.Events = []*event.Event{assistantEvent(calls...), toolEvent(calls, "same")}
	writeBundle(t, runDir, result)
	nativePath := filepath.Join(runDir, "case-a", "case-a.native.json")
	var persisted nativeResult
	if err := artifact.ReadJSONFile(nativePath, &persisted); err != nil {
		t.Fatal(err)
	}
	persisted.Events[1].Response.Choices[0].Message.Content = "bash execution failed"
	if err := artifact.WriteJSON(nativePath, persisted); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(runDir); err == nil || !strings.Contains(err.Error(), "observation envelope") {
		t.Fatalf("Scan() error = %v, want ambiguous tool error rejection", err)
	}
}

func TestObservationEnvelopeValidationMatchesFormatters(t *testing.T) {
	result := sweenv.CommandResult{Output: "ok\n", ReturnCode: 7, ExceptionInfo: "boom"}
	for _, codec := range []observation.ObservationCodec{
		observation.ObservationCodecXML,
		observation.ObservationCodecJSON,
		observation.ObservationCodecText,
	} {
		formatted, err := observation.FormatWithCodec(result, codec)
		if err != nil {
			t.Fatal(err)
		}
		if !validObservationEnvelope(formatted, codec) {
			t.Fatalf("valid %s formatter output was rejected: %q", codec, formatted)
		}
		if validObservationEnvelope("bash execution failed", codec) {
			t.Fatalf("ambiguous %s tool error was accepted", codec)
		}
	}
}

func TestRunRejectsPhysicalOutputContainment(t *testing.T) {
	runDir := t.TempDir()
	writeBundle(t, runDir, nativeResult{InstanceID: "case-a"})
	for _, output := range []string{
		filepath.Join(runDir, "shadow.json"),
		filepath.Join(runDir, "case-a", "case-a.native.json"),
	} {
		if err := rejectOutputInsideRunDir(runDir, output); err == nil ||
			!strings.Contains(err.Error(), "outside --run-dir") {
			t.Fatalf("rejectOutputInsideRunDir(%q) = %v", output, err)
		}
	}
	alias := filepath.Join(t.TempDir(), "run-alias")
	if err := os.Symlink(runDir, alias); err != nil {
		t.Fatal(err)
	}
	if err := rejectOutputInsideRunDir(runDir, filepath.Join(alias, "missing", "shadow.json")); err == nil ||
		!strings.Contains(err.Error(), "outside --run-dir") {
		t.Fatalf("parent-symlink output was not rejected: %v", err)
	}
	if err := rejectOutputInsideRunDir(runDir, filepath.Join(t.TempDir(), "shadow.json")); err != nil {
		t.Fatalf("normal sibling output rejected: %v", err)
	}
}

func exactRepeatEvents() []*event.Event {
	first := []model.ToolCall{bashCall("a", `{"command":"pwd"}`)}
	second := []model.ToolCall{bashCall("b", `{"command":"pwd"}`)}
	return []*event.Event{
		assistantEvent(first...), toolEvent(first, "same"),
		assistantEvent(second...), toolEvent(second, "same"),
		assistantEvent(bashCall("next", `{"command":"next"}`)),
	}
}

func modelResponses(events ...*event.Event) []*model.Response {
	responses := make([]*model.Response, 0)
	for _, evt := range events {
		if evt == nil || evt.Response == nil || !isModelResponse(evt.Response) {
			continue
		}
		responses = append(responses, evt.Response.Clone())
	}
	return responses
}

func assistantTextResponse(content string) *model.Response {
	return &model.Response{
		Object: model.ObjectTypeChatCompletion, Done: true,
		Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant, Content: content}}},
	}
}

func formatRecoveryEvent(t *testing.T, original *model.Response) *event.Event {
	t.Helper()
	if original == nil || len(original.Choices) == 0 {
		t.Fatal("formatRecoveryEvent requires an original response choice")
	}
	_, err := protocol.ParseActions(original.Choices[0].Message.ToolCalls)
	var formatErr protocol.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("ParseActions() error = %v, want FormatError", err)
	}
	return &event.Event{Response: &model.Response{
		Object: model.ObjectTypeChatCompletion, Done: false,
		Choices: []model.Choice{{Message: model.NewUserMessage(formatErr.Error())}},
	}}
}

func mustReplay(t *testing.T, result nativeResult) CaseReport {
	t.Helper()
	report, err := replayCase(result)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func warningEventIndex(warning WarningEvent) int {
	if warning.WouldInjectBeforeEventIndex == nil {
		return 0
	}
	return *warning.WouldInjectBeforeEventIndex
}

func assistantEvent(calls ...model.ToolCall) *event.Event {
	return &event.Event{Response: &model.Response{
		Object: model.ObjectTypeChatCompletion,
		Done:   true,
		Choices: []model.Choice{{Message: model.Message{
			Role: model.RoleAssistant, ToolCalls: calls,
		}}},
	}}
}

func toolEvent(calls []model.ToolCall, observations ...string) *event.Event {
	if len(calls) != len(observations) {
		panic("toolEvent calls and observations differ")
	}
	choices := make([]model.Choice, len(calls))
	arguments := make(map[string]string, len(calls))
	for i, call := range calls {
		choices[i] = model.Choice{Index: i, Message: model.NewToolMessage(
			call.ID, call.Function.Name, observations[i],
		)}
		arguments[call.ID] = string(call.Function.Arguments)
	}
	evt := &event.Event{Response: &model.Response{
		Object: model.ObjectTypeToolResponse, Choices: choices,
	}}
	if err := event.SetExtension(evt, event.ToolCallArgsExtensionKey, arguments); err != nil {
		panic(err)
	}
	return evt
}

func submissionStopEvent() *event.Event {
	return &event.Event{Response: &model.Response{
		Object: model.ObjectTypeError,
		Done:   true,
		Error: &model.ResponseError{
			Type: model.ErrorTypeFlowError, Message: protocol.SubmissionStopEventMessage,
		},
	}}
}

func currentNativeSubmittedResult(calls []model.ToolCall, toolCalls int) nativeResult {
	assistant := assistantEvent(calls...)
	return nativeResult{
		InstanceID: "case-a", Info: nativeInfo{ExitStatus: "Submitted"},
		ModelPatch: "patch", LLMCalls: 1, ToolCalls: toolCalls,
		Events:          []*event.Event{assistant, submissionStopEvent()},
		Responses:       []*model.Response{assistant.Response.Clone()},
		responsesLoaded: true, modernNative: true,
	}
}

type replayExecutorModel struct {
	response *model.Response
}

func (*replayExecutorModel) Info() model.Info { return model.Info{Name: "replay-test"} }

func (m *replayExecutorModel) GenerateContent(
	context.Context, *model.Request,
) (<-chan *model.Response, error) {
	responses := make(chan *model.Response, 1)
	if m.response != nil {
		responses <- m.response.Clone()
		m.response = nil
	}
	close(responses)
	return responses, nil
}

type replayExecutorFactory struct {
	environment sweenv.Environment
}

func (f replayExecutorFactory) StartCase(
	context.Context, sweenv.CaseSpec,
) (sweenv.Environment, error) {
	return f.environment, nil
}

type replayExecutorEnvironment struct {
	results  []sweenv.CommandResult
	commands []string
}

func (e *replayExecutorEnvironment) Execute(
	_ context.Context, command string,
) sweenv.CommandResult {
	e.commands = append(e.commands, command)
	if len(e.results) == 0 {
		return sweenv.CommandResult{}
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result
}

func (*replayExecutorEnvironment) Close(context.Context) error { return nil }

func bashCall(id, arguments string) model.ToolCall {
	return model.ToolCall{
		Type: "function", ID: id,
		Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(arguments)},
	}
}

func mutateNativeJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	if err := artifact.WriteJSON(path, document); err != nil {
		t.Fatal(err)
	}
}

func setNativeSelectedInstances(t *testing.T, runDir string, instanceIDs []string) {
	t.Helper()
	ids := append([]string(nil), instanceIDs...)
	sortStrings(ids)
	selectedHash := stringSHA256(strings.Join(ids, "\n") + "\n")
	for _, instanceID := range ids {
		path := filepath.Join(runDir, instanceID, instanceID+".native.json")
		mutateNativeJSON(t, path, func(document map[string]any) {
			info := document["info"].(map[string]any)
			info["selected_instances_sha256"] = selectedHash
		})
	}
}

func writeBundle(t *testing.T, runDir string, result nativeResult) {
	t.Helper()
	caseDir := filepath.Join(runDir, result.InstanceID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if result.Info.RunID == "" {
		exitStatus := result.Info.ExitStatus
		errorCategory := result.Info.ErrorCategory
		result.Info = testNativeInfo()
		result.Info.ExitStatus = exitStatus
		result.Info.ErrorCategory = errorCategory
	}
	for _, evt := range result.Events {
		if evt == nil || evt.Response == nil || evt.Response.Object != model.ObjectTypeToolResponse {
			continue
		}
		for index := range evt.Response.Choices {
			message := &evt.Response.Choices[index].Message
			if message.Role == model.RoleTool &&
				!validObservationEnvelope(message.Content, observationCodec(result.Info.ObservationCodec)) {
				message.Content = xmlObservation(message.Content)
			}
		}
	}
	responses := result.Responses
	if !result.responsesLoaded {
		responses = make([]*model.Response, 0)
		for _, evt := range result.Events {
			if evt == nil || evt.Response == nil {
				continue
			}
			if evt.Response.Object == model.ObjectTypeChatCompletion ||
				evt.Response.Object == model.ObjectTypeChatCompletionChunk {
				responses = append(responses, evt.Response.Clone())
			}
		}
	}
	if pendingParsedToolCalls(result.Events) == 0 {
		assistant := assistantEvent(bashCall("test-submission", `{"command":"submit"}`))
		result.Events = append(result.Events, assistant)
		responses = append(responses, assistant.Response.Clone())
		result.LLMCalls++
	}
	// Current runner persistence drops the entire final merged tool-response
	// event when the submission callback returns StopError. Test bundles model
	// one executed submission call inside that still-pending final batch.
	result.ToolCalls++
	result.Events = append(result.Events, submissionStopEvent())
	responsesPath := filepath.Join(caseDir, result.InstanceID+".responses.json")
	if err := artifact.WriteJSON(responsesPath, responses); err != nil {
		t.Fatal(err)
	}
	responsesData, err := os.ReadFile(responsesPath)
	if err != nil {
		t.Fatal(err)
	}
	responsesHash := sha256.Sum256(responsesData)
	result.ResponseCount = len(responses)
	result.ResponsesSHA256 = hex.EncodeToString(responsesHash[:])
	result.Info.ExitStatus = "Submitted"
	result.Info.Error = ""
	result.Info.ErrorCategory = ""
	result.Info.Retryable = false
	result.ModelPatch = "patch"
	if err := artifact.WriteJSON(
		filepath.Join(caseDir, result.InstanceID+".native.json"), result,
	); err != nil {
		t.Fatal(err)
	}
}

func pendingParsedToolCalls(events []*event.Event) int {
	remaining := 0
	for _, evt := range events {
		if evt == nil || evt.Response == nil {
			continue
		}
		response := evt.Response
		if response.Object == model.ObjectTypeToolResponse && !response.IsPartial {
			for _, choice := range response.Choices {
				if choice.Message.Role == model.RoleTool && remaining > 0 {
					remaining--
				}
			}
			continue
		}
		if response.Object == model.ObjectTypeError {
			remaining = 0
			continue
		}
		if response.Object != model.ObjectTypeChatCompletion &&
			response.Object != model.ObjectTypeChatCompletionChunk {
			continue
		}
		if response.Error != nil {
			remaining = 0
			continue
		}
		if response.IsPartial || !response.Done || len(response.Choices) == 0 {
			continue
		}
		calls := response.Choices[0].Message.ToolCalls
		if _, err := protocol.ParseActions(calls); err != nil {
			remaining = 0
			continue
		}
		remaining = len(calls)
	}
	return remaining
}

func observationCodec(value string) observation.ObservationCodec {
	return observation.ObservationCodec(value)
}

func xmlObservation(output string) string {
	return "<returncode>0</returncode>\n<output>\n" + output + "</output>"
}

type testTagCaseInfo struct {
	ExitStatus    string `json:"exit_status"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
}

type testTagCaseDocument struct {
	InstanceID                  string          `json:"instance_id"`
	Info                        testTagCaseInfo `json:"info"`
	ModelPatch                  string          `json:"model_patch,omitempty"`
	DurationMS                  int64           `json:"duration_ms"`
	LLMCalls                    int             `json:"llm_calls"`
	ToolCalls                   int             `json:"tool_calls"`
	ToolLoopWarningCount        int             `json:"tool_loop_warning_count"`
	FirstToolLoopWarningLLMCall *int            `json:"first_tool_loop_warning_llm_call"`
	Usage                       model.Usage     `json:"usage"`
	Events                      []*event.Event  `json:"events"`
}

func writeLegacyTagRun(t *testing.T, runDir string, results []nativeResult) string {
	t.Helper()
	ids := make([]string, 0, len(results))
	exitCounts := make(map[string]int)
	var totalLLMCalls, totalToolCalls int
	for _, result := range results {
		if result.Info.ExitStatus == "" {
			result.Info.ExitStatus = "Submitted"
		}
		for _, evt := range result.Events {
			if evt == nil || evt.Response == nil || evt.Response.Object != model.ObjectTypeToolResponse {
				continue
			}
			for index := range evt.Response.Choices {
				message := &evt.Response.Choices[index].Message
				if message.Role == model.RoleTool &&
					!validObservationEnvelope(message.Content, observation.ObservationCodecXML) {
					message.Content = xmlObservation(message.Content)
				}
			}
		}
		responses := modelResponses(result.Events...)
		caseDir := filepath.Join(runDir, result.InstanceID)
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := artifact.WriteJSON(
			filepath.Join(caseDir, result.InstanceID+".responses.json"), responses,
		); err != nil {
			t.Fatal(err)
		}
		events := result.Events
		if events == nil {
			events = []*event.Event{}
		}
		document := testTagCaseDocument{
			InstanceID: result.InstanceID,
			Info: testTagCaseInfo{
				ExitStatus: result.Info.ExitStatus, Error: result.Info.Error,
				ErrorCategory: result.Info.ErrorCategory, Retryable: result.Info.Retryable,
			},
			ModelPatch: result.ModelPatch, DurationMS: 1,
			LLMCalls: result.LLMCalls, ToolCalls: result.ToolCalls,
			ToolLoopWarningCount:        result.ToolLoopWarningCount,
			FirstToolLoopWarningLLMCall: result.FirstToolLoopWarningLLMCall,
			Usage:                       model.Usage{}, Events: events,
		}
		if err := artifact.WriteJSON(
			filepath.Join(caseDir, result.InstanceID+".tag.json"), document,
		); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.InstanceID)
		exitCounts[result.Info.ExitStatus]++
		totalLLMCalls += result.LLMCalls
		totalToolCalls += result.ToolCalls
	}
	sortStrings(ids)
	manifest := testTagManifest(ids)
	manifest.CaseCount = len(results)
	manifest.AttemptedCount = len(results)
	manifest.PredictionCount = len(results)
	manifest.ExitStatusCounts = exitCounts
	manifest.LLMCalls = totalLLMCalls
	manifest.ToolCalls = totalToolCalls
	manifestPath := filepath.Join(runDir, legacyTagManifestFilename)
	if err := artifact.WriteJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

func testTagManifest(ids []string) tagManifest {
	return tagManifest{
		RunID: "test-v12-tag-run", RunnerType: "tag",
		FrameworkVersion:  "v1.10.1-0.20260616104537-c6c3bb29ab60",
		FrameworkRevision: strings.Repeat("a", 40), UpstreamCommit: strings.Repeat("b", 40),
		AgentProtocol:    "mini-swe-agent-v2.1-on-tag",
		ObservationCodec: string(observation.ObservationCodecXML), SourceModified: false,
		BinarySHA256: strings.Repeat("c", 64), CasesSHA256: strings.Repeat("d", 64),
		SelectedCaseSetSHA256:   stringSHA256(strings.Join(ids, "\n") + "\n"),
		ModelConfigSHA256:       strings.Repeat("e", 64),
		EnvironmentConfigSHA256: strings.Repeat("f", 64),
		DurationMS:              1,
		Cases:                   "/frozen/cases.jsonl",
		Predictions:             "/frozen/preds.json",
		Progress:                "/frozen/progress.json",
		CaseCount:               len(ids), AttemptedCount: len(ids), PredictionCount: len(ids),
		Workers: 15, CommandTimeout: "1m0s", CaseTimeout: "4h0m0s",
		WorkspaceRepresentation:       "current-fixed",
		WorkspaceRepresentationSchema: "workspace-representation/v1;reader=text;chunk=fixed;size=1024;overlap=128;whitespace=trim-lines",
		WorkspaceRepresentationSHA256: strings.Repeat("1", 64),
		ExitStatusCounts:              map[string]int{"Submitted": len(ids)},
		Usage:                         map[string]int{}, Status: "completed",
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func testNativeInfo() nativeInfo {
	return nativeInfo{
		RunID: "test-run", ObservationCodec: "xml",
		SourceRevision:          strings.Repeat("a", 40),
		BinarySHA256:            strings.Repeat("b", 64),
		ModelConfigSHA256:       strings.Repeat("c", 64),
		EnvironmentConfigSHA256: strings.Repeat("d", 64),
		CasesSHA256:             strings.Repeat("e", 64),
		CommandTimeout:          "1m0s", CaseTimeout: "4h0m0s",
		SelectedInstancesSHA256: stringSHA256("case-a\n"),
		Workers:                 1,
	}
}
