//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/catalog"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/tasks"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestRunnerSkillRepositoryUsesFrameworkFilesystem(t *testing.T) {
	repository, err := newTempSkillRepository(testSuite().Skills)
	if err != nil {
		t.Fatal(err)
	}
	root := repository.Root()
	defer func() { _ = repository.Close() }()
	path, err := repository.Path("research")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || filepath.Base(path) != "research" {
		t.Fatalf("skill path = %q, root = %q", path, root)
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
		t.Fatalf("runner did not materialize SKILL.md: %v", err)
	}
}

func TestRunnerReusesConfiguredLocalSkillRoot(t *testing.T) {
	suite := testSuite()
	root := t.TempDir()
	if _, err := catalog.NewSkillRepositoryFromSkills(frameworkSkills(suite.Skills), root); err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{Suite: suite, SkillRoot: root, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := r.openSkillRepository()
	if err != nil {
		t.Fatal(err)
	}
	if repository.Root() != root {
		t.Fatalf("configured Skill root = %q, want %q", repository.Root(), root)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "research", "SKILL.md")); err != nil {
		t.Fatalf("configured local Skill root was removed: %v", err)
	}
}

func TestCheckedInSkillFilesMatchDefaultSuite(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(sourceFile), "..", "skills")
	repository, err := catalog.OpenSkillRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := tasks.DefaultSuite()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range suite.Skills {
		loaded, err := repository.Get(spec.Name)
		if err != nil {
			t.Fatalf("load checked-in Skill %q: %v", spec.Name, err)
		}
		if loaded.Summary.Description != spec.Description || loaded.Body != spec.Body {
			t.Fatalf("checked-in Skill %q diverges from suite metadata: loaded=%#v want description=%q body=%q", spec.Name, loaded, spec.Description, spec.Body)
		}
	}
}

func TestRunTaskStaticAndDynamic(t *testing.T) {
	suite := testSuite()
	r, err := New(Config{Suite: suite, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	static, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeStaticAll)
	if err != nil {
		t.Fatalf("static RunTask: %v", err)
	}
	dynamic, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeDynamicActivation)
	if err != nil {
		t.Fatalf("dynamic RunTask: %v", err)
	}
	if !static.Passed || !dynamic.Passed {
		t.Fatalf("runs should pass: static=%+v dynamic=%+v", static, dynamic)
	}
	if static.InitialToolCount <= dynamic.InitialToolCount {
		t.Fatalf("static initial tools = %d, dynamic = %d; dynamic should start smaller", static.InitialToolCount, dynamic.InitialToolCount)
	}
	if dynamic.ActivationCount != 1 {
		t.Fatalf("dynamic activation count = %d, want 1", dynamic.ActivationCount)
	}
	if dynamic.Usage.TotalTokens <= 0 || static.Usage.TotalTokens <= 0 {
		t.Fatalf("missing token usage: static=%+v dynamic=%+v", static.Usage, dynamic.Usage)
	}
	for _, name := range dynamic.Requests[0].VisibleTools {
		if name == "skill_list_docs" || name == "skill_select_docs" {
			t.Fatalf("unneeded Skill tool %q leaked into the benchmark surface: %v", name, dynamic.Requests[0].VisibleTools)
		}
	}
	if dynamic.Requests[0].UsageSource != metrics.UsageSourceReported {
		t.Fatalf("test model usage source = %q, want reported", dynamic.Requests[0].UsageSource)
	}
	if len(dynamic.Activations[0].ActivatedTools) != 3 {
		t.Fatalf("activated tools = %v, want all tools in the set", dynamic.Activations[0].ActivatedTools)
	}
}

func TestDynamicActivationKeepsExplicitBaseTools(t *testing.T) {
	suite := testSuite()
	r, err := New(Config{Suite: suite, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeDynamicActivation)
	if err != nil {
		t.Fatalf("dynamic RunTask: %v", err)
	}
	if len(result.Requests) < 2 {
		t.Fatalf("requests = %d, want a load and a post-load request", len(result.Requests))
	}
	for _, request := range result.Requests {
		if request.Index == 0 {
			continue
		}
		found := false
		for _, name := range request.VisibleTools {
			if name == "clock" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("base tool disappeared after activation: visible=%v", request.VisibleTools)
		}
		return
	}
	t.Fatal("did not inspect a post-load request")
}

func TestCallableToolFailsClosedWithoutHandler(t *testing.T) {
	state := bench.NewTaskState(nil)
	toolInstance := &callableTool{
		spec:     bench.ToolSpec{Name: "unimplemented"},
		fullName: "unimplemented",
		state:    state,
	}
	if _, err := toolInstance.Call(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("unimplemented tool should return an error")
	}
	calls := state.SnapshotCalls()
	if len(calls) != 1 || calls[0].Succeeded || calls[0].Error == "" {
		t.Fatalf("missing-handler call record = %#v, want one failed call", calls)
	}
}

func TestDrainRunEventsPropagatesFrameworkErrorEvents(t *testing.T) {
	events := make(chan *event.Event, 1)
	events <- &event.Event{Response: &model.Response{
		Done:   true,
		Object: model.ObjectTypeError,
		Error:  &model.ResponseError{Type: model.ErrorTypeFlowError, Message: "tool loop failed"},
	}}
	close(events)
	if err := drainRunEvents(events); err == nil || err.Error() != "flow_error: tool loop failed" {
		t.Fatalf("drainRunEvents error = %v, want flow error", err)
	}
}

func TestDrainRunEventsRejectsNilChannel(t *testing.T) {
	if err := drainRunEvents(nil); err == nil {
		t.Fatal("drainRunEvents should reject a nil event channel")
	}
}

func TestRunComparisonReturnsPairedResults(t *testing.T) {
	suite := testSuite()
	r, err := New(Config{Suite: suite, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	comparison, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(comparison.StaticRuns) != len(suite.Tasks) || len(comparison.DynamicRuns) != len(suite.Tasks) {
		t.Fatalf("paired result lengths = (%d, %d), want %d", len(comparison.StaticRuns), len(comparison.DynamicRuns), len(suite.Tasks))
	}
	if comparison.Static.PassRate != 1 || comparison.Dynamic.PassRate != 1 {
		t.Fatalf("pass rates = (%v, %v), want 1", comparison.Static.PassRate, comparison.Dynamic.PassRate)
	}
	if comparison.Static.Usage.TotalTokens <= 0 || comparison.Dynamic.Usage.TotalTokens <= 0 {
		t.Fatalf("comparison should include token usage: static=%+v dynamic=%+v", comparison.Static.Usage, comparison.Dynamic.Usage)
	}
	if !comparison.TokenDeltaComparable {
		t.Fatalf("deterministic request usage should be comparable: static=%+v dynamic=%+v", comparison.Static, comparison.Dynamic)
	}
}

func TestRunnerRejectsInvalidConfig(t *testing.T) {
	_, err := New(Config{Suite: bench.Suite{Name: ""}})
	if err == nil {
		t.Fatal("expected empty suite name error")
	}
	suite := testSuite()
	if _, err := New(Config{Suite: suite}); err == nil || !strings.Contains(err.Error(), "model factory is required") {
		t.Fatalf("missing model factory error = %v", err)
	}
	_, err = New(Config{Suite: suite, ModelFactory: testModelFactory(suite), Lifetime: llmagent.ToolActivationLifetime("bad")})
	if err == nil {
		t.Fatal("expected invalid lifetime error")
	}
}

func TestNewRendersModelFacingToolReferences(t *testing.T) {
	suite := testSuite()
	suite.Skills[0].Body = "Use {{tool:search}} and {{tool:save}}."
	suite.Tools[0].Description = "Search via {{tool:search}}."
	suite.Tools[0].InputSchema.Properties["id"] = &tool.Schema{Type: "string", Description: "Id from {{tool:search}}."}
	suite.Tasks[0].Prompt = "Complete the task with {{tool:search}}."
	r, err := New(Config{Suite: suite, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if suite.Skills[0].Body != "Use {{tool:search}} and {{tool:save}}." || suite.Tools[0].Description != "Search via {{tool:search}}." || suite.Tools[0].InputSchema.Properties["id"].Description != "Id from {{tool:search}}." || suite.Tasks[0].Prompt != "Complete the task with {{tool:search}}." {
		t.Fatal("New mutated the caller's suite")
	}
	modelSuite := r.Config().Suite
	if modelSuite.Skills[0].Body != "Use research-tools_search and research-tools_save." {
		t.Fatalf("model-facing Skill body = %q", modelSuite.Skills[0].Body)
	}
	if modelSuite.Tools[0].Description != "Search via research-tools_search." {
		t.Fatalf("model-facing Tool description = %q", modelSuite.Tools[0].Description)
	}
	if got := modelSuite.Tools[0].InputSchema.Properties["id"].Description; got != "Id from research-tools_search." {
		t.Fatalf("model-facing schema description = %q", got)
	}
	if modelSuite.Tasks[0].Prompt != "Complete the task with research-tools_search." {
		t.Fatalf("model-facing task prompt = %q", modelSuite.Tasks[0].Prompt)
	}
}

func TestRunTaskRendersPromptPassedDirectlyByCaller(t *testing.T) {
	suite := testSuite()
	var received ModelInput
	r, err := New(Config{
		Suite: suite,
		ModelFactory: func(input ModelInput) model.Model {
			received = input
			return &requestCaptureModel{}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	task := suite.Tasks[0]
	task.Prompt = "Use {{tool:search}} to complete the task."
	if _, err := r.RunTask(context.Background(), task, bench.ModeStaticAll); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if received.Prompt != "Use research-tools_search to complete the task." {
		t.Fatalf("model prompt = %q", received.Prompt)
	}
}

func TestRenderedDescriptionsReachFrameworkModelRequest(t *testing.T) {
	suite := testSuite()
	suite.Tools[0].Description = "Search via {{tool:search}}."
	suite.Tools[0].InputSchema.Properties["id"] = &tool.Schema{Type: "string", Description: "Id from {{tool:search}}."}
	modelUnderTest := &requestCaptureModel{}
	r, err := New(Config{
		Suite: suite,
		ModelFactory: func(ModelInput) model.Model {
			return modelUnderTest
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeStaticAll); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(modelUnderTest.requests) == 0 {
		t.Fatal("model received no requests")
	}
	declared, ok := modelUnderTest.requests[0].Tools["research-tools_search"]
	if !ok {
		t.Fatalf("model request has no qualified search tool: %v", modelUnderTest.requests[0].Tools)
	}
	declaration := declared.Declaration()
	if declaration.Description != "Search via research-tools_search." {
		t.Fatalf("request tool description = %q", declaration.Description)
	}
	if got := declaration.InputSchema.Properties["id"].Description; got != "Id from research-tools_search." {
		t.Fatalf("request schema description = %q", got)
	}
}

func TestModelRequestObserverSeesPriorToolCallAndResult(t *testing.T) {
	suite := testSuite()
	traces := make([]ModelRequestTrace, 0)
	r, err := New(Config{
		Suite:        suite,
		ModelFactory: testModelFactory(suite),
		ModelRequestObserver: func(trace ModelRequestTrace) {
			traces = append(traces, trace)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeDynamicActivation); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(traces) < 2 {
		t.Fatalf("before-model traces = %d, want at least 2 turns", len(traces))
	}
	if traces[0].RequestIndex != 0 || traces[1].RequestIndex != 1 {
		t.Fatalf("request indexes = (%d, %d), want (0, 1)", traces[0].RequestIndex, traces[1].RequestIndex)
	}
	foundAssistantCall := false
	foundToolResult := false
	interactionTrace := -1
	for i, trace := range traces {
		traceHasAssistantCall := false
		traceHasToolResult := false
		for _, message := range trace.Messages {
			if message.Role == model.RoleAssistant && len(message.ToolCalls) > 0 {
				traceHasAssistantCall = true
			}
			if message.Role == model.RoleTool && message.ToolName != "" && message.Content != "" {
				traceHasToolResult = true
			}
		}
		if traceHasAssistantCall && traceHasToolResult {
			foundAssistantCall = true
			foundToolResult = true
			interactionTrace = i
			break
		}
	}
	if !foundAssistantCall || !foundToolResult {
		t.Fatalf("no later request retained a prior tool interaction: assistant_call=%t tool_result=%t", foundAssistantCall, foundToolResult)
	}
	if len(traces[interactionTrace].Tools) == 0 {
		t.Fatalf("request %d has no visible tool declarations", interactionTrace)
	}
}

func TestModelRequestObserverCapturesToolErrorResult(t *testing.T) {
	suite := testSuite()
	suite.Tools[0].Handler = func(context.Context, []byte, *bench.TaskState) (any, error) {
		return nil, errors.New("simulated not found")
	}
	traces := make([]ModelRequestTrace, 0)
	r, err := New(Config{
		Suite:       suite,
		MaxLLMCalls: 4,
		ModelFactory: func(ModelInput) model.Model {
			return &repeatingToolErrorModel{}
		},
		ModelRequestObserver: func(trace ModelRequestTrace) {
			traces = append(traces, trace)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _ = r.RunTask(context.Background(), suite.Tasks[0], bench.ModeDynamicActivation)

	foundError := false
	for _, trace := range traces {
		for _, message := range trace.Messages {
			if message.Role == model.RoleTool && strings.Contains(message.Content, "simulated not found") {
				foundError = true
			}
		}
	}
	if !foundError {
		t.Fatalf("before-model trace did not retain the tool error result: %+v", traces)
	}
}

func TestRunnerRejectsNilContext(t *testing.T) {
	suite := testSuite()
	r, err := New(Config{Suite: suite, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Run(nil); err == nil {
		t.Fatal("Run should reject a nil context")
	}
	if _, err := r.RunMode(nil, bench.ModeStaticAll); err == nil {
		t.Fatal("RunMode should reject a nil context")
	}
	if _, err := r.RunTask(nil, suite.Tasks[0], bench.ModeStaticAll); err == nil {
		t.Fatal("RunTask should reject a nil context")
	}
}

func TestRunModePropagatesCanceledContext(t *testing.T) {
	suite := testSuite()
	r, err := New(Config{Suite: suite, ModelFactory: testModelFactory(suite)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := r.RunMode(ctx, bench.ModeStaticAll)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunMode error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Fatalf("canceled RunMode returned %d results, want none", len(results))
	}
}

func TestRunModeRetainsTaskResultWhenContextExpiresDuringRun(t *testing.T) {
	suite := testSuite()
	r, err := New(Config{
		Suite: suite,
		ModelFactory: func(ModelInput) model.Model {
			return &blockingModel{}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	results, err := r.RunMode(ctx, bench.ModeStaticAll)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunMode error = %v, want context.DeadlineExceeded", err)
	}
	if len(results) != 1 {
		t.Fatalf("expired RunMode returned %d results, want the partial task result", len(results))
	}
	if results[0].TaskID != suite.Tasks[0].ID || results[0].Error == "" || results[0].DurationNanos <= 0 {
		t.Fatalf("partial task result = %+v, want id/error/duration", results[0])
	}
}

func TestRunTaskSkipsModelConstructionForCanceledContext(t *testing.T) {
	suite := testSuite()
	factoryCalled := false
	r, err := New(Config{
		Suite: suite,
		ModelFactory: func(ModelInput) model.Model {
			factoryCalled = true
			return &requestCaptureModel{}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := r.RunTask(ctx, suite.Tasks[0], bench.ModeStaticAll)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTask error = %v, want context.Canceled", err)
	}
	if factoryCalled {
		t.Fatal("canceled RunTask should not construct a model")
	}
	if result.Error != context.Canceled.Error() {
		t.Fatalf("canceled result error = %q, want %q", result.Error, context.Canceled.Error())
	}
}

func TestRunnerRequestsStreamingByDefault(t *testing.T) {
	suite := testSuite()
	modelUnderTest := &requestCaptureModel{}
	r, err := New(Config{
		Suite: suite,
		ModelFactory: func(ModelInput) model.Model {
			return modelUnderTest
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeStaticAll); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(modelUnderTest.requests) == 0 || !modelUnderTest.requests[0].GenerationConfig.Stream {
		t.Fatalf("default request streaming = %#v, want true", modelUnderTest.requests)
	}
	if len(modelUnderTest.requests) != 1 {
		t.Fatalf("requests = %d, want one", len(modelUnderTest.requests))
	}
}

func TestRunnerCanDisableStreamingExplicitly(t *testing.T) {
	suite := testSuite()
	modelUnderTest := &requestCaptureModel{}
	r, err := New(Config{
		Suite:            suite,
		DisableStreaming: true,
		ModelFactory: func(ModelInput) model.Model {
			return modelUnderTest
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeStaticAll); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(modelUnderTest.requests) == 0 || modelUnderTest.requests[0].GenerationConfig.Stream {
		t.Fatalf("disabled request streaming = %#v, want false", modelUnderTest.requests)
	}
}

func TestModelFactoryInputIsSanitizedAndNilIsAnError(t *testing.T) {
	suite := testSuite()
	var received ModelInput
	r, err := New(Config{
		Suite: suite,
		ModelFactory: func(input ModelInput) model.Model {
			received = input
			return &requestCaptureModel{}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.RunTask(context.Background(), suite.Tasks[0], bench.ModeStaticAll); err != nil {
		t.Fatalf("RunTask with custom model: %v", err)
	}
	if received.Prompt != suite.Tasks[0].Prompt {
		t.Fatalf("unexpected sanitized model input: %+v", received)
	}

	nilRunner, err := New(Config{
		Suite: suite,
		ModelFactory: func(ModelInput) model.Model {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New nil-factory runner: %v", err)
	}
	if _, err := nilRunner.RunTask(context.Background(), suite.Tasks[0], bench.ModeStaticAll); err == nil {
		t.Fatal("expected nil model factory to return an error")
	}
}

func TestSessionLifetimeReusesActivationAcrossTaskBundle(t *testing.T) {
	suite := testSuite()
	first := suite.Tasks[0]
	first.SessionID = "research-bundle"
	second := first
	second.ID = "research-2"
	second.Prompt = "Search the notes and save the finding again."
	second.SessionID = first.SessionID
	suite.Tasks = []bench.Task{first, second}
	traces := make([]ModelRequestTrace, 0)
	r, err := New(Config{
		Suite:        suite,
		Lifetime:     llmagent.ToolActivationLifetimeSession,
		ModelFactory: testModelFactory(suite),
		ModelRequestObserver: func(trace ModelRequestTrace) {
			traces = append(traces, trace)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results, err := r.RunMode(context.Background(), bench.ModeDynamicActivation)
	if err != nil {
		t.Fatalf("RunMode: %v", err)
	}
	if len(results) != 2 || !results[0].Passed || !results[1].Passed {
		t.Fatalf("session bundle should pass: %+v", results)
	}
	if results[0].SkillLoadCount != 1 || results[1].SkillLoadCount != 0 {
		t.Fatalf("skill loads = (%d, %d), want (1, 0)", results[0].SkillLoadCount, results[1].SkillLoadCount)
	}
	if results[1].InitialToolCount < results[0].InitialToolCount {
		t.Fatalf("session activation should not shrink menu: (%d, %d)", results[0].InitialToolCount, results[1].InitialToolCount)
	}
	for _, trace := range traces {
		if trace.TaskID != second.ID || trace.RequestIndex != 0 {
			continue
		}
		for _, message := range trace.Messages {
			if message.Role == model.RoleAssistant || message.Role == model.RoleTool {
				t.Fatalf("session task inherited prior conversation message: %+v", message)
			}
		}
		return
	}
	t.Fatal("missing first request trace for second session-lifetime task")
}

func TestToolNameAliasesRejectLookalikesAndKeepQualifiedNames(t *testing.T) {
	specs := []bench.ToolSpec{{
		Name: "mail_search", Skill: "mail", ToolSet: "mail-tools",
	}}
	executed := []bench.CallRecord{
		{Name: "mail-tools_mail_search", Arguments: `{}`, Succeeded: true},
		{Name: "mail-tools_mail_search", Arguments: `{}`, Succeeded: true},
	}
	requests := []metrics.RequestRecord{{
		ToolCallsReturned: []string{"evil_mail_search", "evil_skill_load", "mail_search", "mail-tools_mail_search"},
		ToolCallArguments: []string{"{}", `{ "skill": "mail" }`, "{}", "{}"},
	}}
	calls := observedCalls(requests, executed, specs)
	if len(calls) != 4 {
		t.Fatalf("observed calls = %d, want 4: %#v", len(calls), calls)
	}
	if calls[0].Succeeded || calls[0].Name != "evil_mail_search" {
		t.Fatalf("lookalike call was accepted: %#v", calls[0])
	}
	if calls[1].Framework || calls[1].Succeeded {
		t.Fatalf("lookalike framework call was accepted: %#v", calls[1])
	}
	if !calls[2].Succeeded || !calls[3].Succeeded {
		t.Fatalf("valid aliases were not matched: %#v", calls)
	}
	required := []string{"mail-tools_mail_search"}
	if got := wrongToolCalls(calls, required, specs); got != 2 {
		t.Fatalf("wrong tool calls = %d, want 2", got)
	}
	if got := successfulRequiredPrecision(calls, required, specs); got != 2.0/4.0 {
		t.Fatalf("tool precision = %v, want %v", got, 2.0/4.0)
	}
	if sameToolName("evil_mail_search", "mail_search") {
		t.Fatal("sameToolName accepted a suffix lookalike")
	}
	if !sameToolName("mail-tools_mail_search", "mail_search") {
		t.Fatal("sameToolName rejected the conventional ToolSet prefix")
	}
}

func TestCustomToolSetMetadataIsUsedForActivationAccounting(t *testing.T) {
	specs := []bench.ToolSpec{{
		Name: "search", Skill: "research", ToolSet: "research_ops",
		InputSchema: &tool.Schema{Type: "object"},
	}}
	sets := []tool.ToolSet{&taskToolSet{name: "research_ops"}}
	got := skillToolSetNames(bench.SkillSpec{Name: "research"}, sets, specs)
	if len(got) != 1 || got[0] != "research_ops" {
		t.Fatalf("inferred tool sets = %v, want [research_ops]", got)
	}
	if got := toolSetForActivatedTools([]string{"research_ops_search"}, specs); got != "research_ops" {
		t.Fatalf("tool set from qualified name = %q, want research_ops", got)
	}
}

func TestInferActivationsRequiresAdjacentOwnedMenuDiff(t *testing.T) {
	specs := []bench.ToolSpec{
		{Name: "search", Skill: "research", ToolSet: "research_ops"},
		{Name: "save", Skill: "research", ToolSet: "research_ops"},
		{Name: "send", Skill: "mail", ToolSet: "mail_ops"},
	}
	requests := []metrics.RequestRecord{
		{
			Index:             0,
			VisibleTools:      []string{"skill_load"},
			ToolCallsReturned: []string{"skill_load"},
			ToolCallArguments: []string{`{"skill":"research"}`},
		},
		{
			Index:        1,
			VisibleTools: []string{"research_ops_save", "research_ops_search", "skill_load"},
		},
	}
	got := inferActivations(requests, []string{"research_ops_search"}, specs, llmagent.ToolActivationLifetimeInvocation)
	if len(got) != 1 {
		t.Fatalf("inferred activations = %#v, want one record", got)
	}
	if got[0].Skill != "research" || got[0].ToolSet != "research_ops" {
		t.Fatalf("activation ownership = %#v, want research/research_ops", got[0])
	}
	if got[0].RequestIndex != 1 || got[0].NewToolCount != 2 || !got[0].Necessary {
		t.Fatalf("activation metadata = %#v", got[0])
	}
	if want := []string{"research_ops_save", "research_ops_search"}; !reflect.DeepEqual(got[0].ActivatedTools, want) {
		t.Fatalf("activated tools = %v, want %v", got[0].ActivatedTools, want)
	}
}

func TestInferActivationsDoesNotLookPastNoopOrFailedLoad(t *testing.T) {
	specs := []bench.ToolSpec{{Name: "search", Skill: "research", ToolSet: "research_ops"}}
	base := []string{"skill_load"}
	requests := []metrics.RequestRecord{
		{
			VisibleTools:      base,
			ToolCallsReturned: []string{"skill_load"},
			ToolCallArguments: []string{`{"skill":"research"}`},
		},
		// The load produced no menu change. A later snapshot must not be
		// attributed to this stale request.
		{VisibleTools: base},
		{VisibleTools: []string{"research_ops_search", "skill_load"}},
	}
	if got := inferActivations(requests, nil, specs, llmagent.ToolActivationLifetimeInvocation); len(got) != 0 {
		t.Fatalf("stale load inferred activation = %#v, want none", got)
	}

	requests = []metrics.RequestRecord{
		{
			VisibleTools:      base,
			ToolCallsReturned: []string{"skill_load"},
			ToolCallArguments: []string{`{"skill":"missing"}`},
		},
		{VisibleTools: []string{"research_ops_search", "skill_load"}},
	}
	if got := inferActivations(requests, nil, specs, llmagent.ToolActivationLifetimeInvocation); len(got) != 0 {
		t.Fatalf("unknown skill inferred activation = %#v, want none", got)
	}
}

func TestInferActivationsRefreshesBaselineEveryRequest(t *testing.T) {
	specs := []bench.ToolSpec{
		{Name: "search", Skill: "research", ToolSet: "research_ops"},
		{Name: "send", Skill: "mail", ToolSet: "mail_ops"},
	}
	requests := []metrics.RequestRecord{
		{VisibleTools: []string{"skill_load"}},
		// An unrelated menu update without a load must become the new baseline.
		{VisibleTools: []string{"mail_ops_send", "skill_load"}},
		{
			VisibleTools:      []string{"mail_ops_send", "skill_load"},
			ToolCallsReturned: []string{"skill_load"},
			ToolCallArguments: []string{`{"skill":"research"}`},
		},
		{VisibleTools: []string{"mail_ops_send", "research_ops_search", "skill_load"}},
	}
	got := inferActivations(requests, []string{"research_ops_search"}, specs, llmagent.ToolActivationLifetimeInvocation)
	if len(got) != 1 {
		t.Fatalf("baseline-refresh activations = %#v, want one record", got)
	}
	if got[0].RequestIndex != 3 || !reflect.DeepEqual(got[0].ActivatedTools, []string{"research_ops_search"}) {
		t.Fatalf("baseline-refresh record = %#v", got[0])
	}
}

func TestInferActivationsSkipsAmbiguousLoadsAndMixedMenuDiff(t *testing.T) {
	specs := []bench.ToolSpec{
		{Name: "search", Skill: "research", ToolSet: "research_ops"},
		{Name: "send", Skill: "mail", ToolSet: "mail_ops"},
	}
	base := []string{"skill_load"}
	multiple := []metrics.RequestRecord{
		{
			VisibleTools:      base,
			ToolCallsReturned: []string{"skill_load", "skill_load"},
			ToolCallArguments: []string{`{"skill":"research"}`, `{"skill":"mail"}`},
		},
		{VisibleTools: []string{"mail_ops_send", "research_ops_search", "skill_load"}},
	}
	if got := inferActivations(multiple, nil, specs, llmagent.ToolActivationLifetimeInvocation); len(got) != 0 {
		t.Fatalf("multiple loads inferred activation = %#v, want none", got)
	}

	mixed := []metrics.RequestRecord{
		{
			VisibleTools:      base,
			ToolCallsReturned: []string{"skill_load"},
			ToolCallArguments: []string{`{"skill":"research"}`},
		},
		{VisibleTools: []string{"mail_ops_send", "research_ops_search", "skill_load"}},
	}
	if got := inferActivations(mixed, nil, specs, llmagent.ToolActivationLifetimeInvocation); len(got) != 0 {
		t.Fatalf("mixed ownership diff inferred activation = %#v, want none", got)
	}
}

func TestKnownSkillLoadsRequiresExactSuccessfulSkillName(t *testing.T) {
	toolsBySkill := map[string]map[string]bool{
		"research": {"research_ops_search": true},
	}
	tests := []struct {
		name    string
		request metrics.RequestRecord
	}{
		{
			name: "whitespace is not normalized",
			request: metrics.RequestRecord{
				ToolCallsReturned: []string{"skill_load"},
				ToolCallArguments: []string{`{"skill":" research"}`},
			},
		},
		{
			name: "response error rejects load",
			request: metrics.RequestRecord{
				Error:             "provider error",
				ToolCallsReturned: []string{"skill_load"},
				ToolCallArguments: []string{`{"skill":"research"}`},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := knownSkillLoads(test.request, toolsBySkill); got != nil {
				t.Fatalf("knownSkillLoads = %v, want nil", got)
			}
		})
	}
}

func testSuite() bench.Suite {
	object := &tool.Schema{Type: "object", Properties: map[string]*tool.Schema{}}
	return bench.Suite{
		Name: "runner-test",
		Skills: []bench.SkillSpec{
			{Name: "research", Description: "research local notes", Body: "Use research tools.", ToolSets: []string{"research-tools"}},
		},
		Tools: []bench.ToolSpec{
			{Name: "search", Description: "search notes", Skill: "research", ToolSet: "research-tools", InputSchema: object, Handler: func(context.Context, []byte, *bench.TaskState) (any, error) {
				return map[string]string{"result": "found"}, nil
			}},
			{Name: "save", Description: "save finding", Skill: "research", ToolSet: "research-tools", InputSchema: object, Handler: func(_ context.Context, _ []byte, state *bench.TaskState) (any, error) {
				state.Set("saved", true)
				return map[string]string{"result": "saved"}, nil
			}},
			{Name: "distractor", Description: "unrelated operation", Skill: "research", ToolSet: "research-tools", InputSchema: object},
			{Name: "clock", Description: "read local clock", InputSchema: object},
		},
		Tasks: []bench.Task{{
			ID:             "research-1",
			Prompt:         "Search the notes and save the finding.",
			RequiredSkills: []string{"research"},
			RequiredTools:  []string{"search", "save"},
			Evaluate: func(state *bench.TaskState) bench.Evaluation {
				base := bench.DefaultEvaluation(bench.Task{RequiredTools: []string{"search", "save"}}, state)
				if saved, ok := state.Get("saved"); !ok || saved != true {
					base.Passed = false
					base.Message = "finding was not saved"
				}
				return base
			},
		}},
	}
}

// testModelFactory is a tiny scripted model used only by runner unit tests.
// It verifies the framework wiring without making the benchmark executable
// depend on a second production model implementation.
func testModelFactory(suite bench.Suite) ModelFactory {
	return func(input ModelInput) model.Model {
		task := suite.Tasks[0]
		for _, candidate := range suite.Tasks {
			if candidate.Prompt == input.Prompt {
				task = candidate
				break
			}
		}
		qualified := qualifiedRequiredTools(task, suite.Tools)
		toolSkills := make(map[string]string, len(qualified))
		for _, target := range qualified {
			for _, spec := range suite.Tools {
				if bench.QualifiedToolName(spec) == target {
					toolSkills[target] = spec.Skill
					break
				}
			}
		}
		return &testPlanModel{required: qualified, toolSkills: toolSkills, called: make(map[string]bool), loaded: make(map[string]bool)}
	}
}

type testPlanModel struct {
	required   []string
	toolSkills map[string]string
	called     map[string]bool
	loaded     map[string]bool
	step       int
}

func (m *testPlanModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.step++
	visible := request.Tools
	for _, target := range m.required {
		if m.called[target] {
			continue
		}
		if _, ok := visible[target]; ok {
			m.called[target] = true
			return testModelResponse(m.step, target, []byte(`{}`)), nil
		}
		if skill := m.toolSkills[target]; skill != "" && !m.loaded[skill] {
			if _, ok := visible["skill_load"]; ok {
				m.loaded[skill] = true
				args, _ := json.Marshal(map[string]string{"skill": skill})
				return testModelResponse(m.step, "skill_load", args), nil
			}
		}
		break
	}
	response := &model.Response{
		ID: fmt.Sprintf("test-model-%d", m.step), Model: "activationbench-test-model", Done: true,
		Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant, Content: "done"}}},
		Usage:   &model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
	ch := make(chan *model.Response, 1)
	ch <- response
	close(ch)
	return ch, nil
}

func testModelResponse(step int, name string, args []byte) <-chan *model.Response {
	response := &model.Response{
		ID: fmt.Sprintf("test-model-%d", step), Model: "activationbench-test-model", Done: true,
		Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{
			Type: "function", ID: fmt.Sprintf("test-call-%d", step),
			Function: model.FunctionDefinitionParam{Name: name, Arguments: args},
		}}}}},
		Usage: &model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
	ch := make(chan *model.Response, 1)
	ch <- response
	close(ch)
	return ch
}

func (m *testPlanModel) Info() model.Info {
	return model.Info{Name: "activationbench-test-model", ContextWindow: 128000}
}

// requestCaptureModel is intentionally minimal: these tests only verify that
// the runner uses the framework's public per-invocation stream override.
type requestCaptureModel struct {
	requests []*model.Request
}

type repeatingToolErrorModel struct {
	mu    sync.Mutex
	steps int
}

func (m *repeatingToolErrorModel) GenerateContent(_ context.Context, _ *model.Request) (<-chan *model.Response, error) {
	m.mu.Lock()
	m.steps++
	step := m.steps
	m.mu.Unlock()
	name := "research-tools_search"
	args := []byte(`{}`)
	if step == 1 {
		name = "skill_load"
		args = []byte(`{"skill":"research"}`)
	}
	response := &model.Response{
		Done: true,
		Choices: []model.Choice{{Message: model.Message{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				Type: "function",
				ID:   "call-" + strconv.Itoa(step),
				Function: model.FunctionDefinitionParam{
					Name: name, Arguments: args,
				},
			}},
		}}},
	}
	responses := make(chan *model.Response, 1)
	responses <- response
	close(responses)
	return responses, nil
}

func (*repeatingToolErrorModel) Info() model.Info {
	return model.Info{Name: "repeating-tool-error-test"}
}

// blockingModel lets cancellation tests exercise the runner's real framework
// invocation path without introducing a network server or a second protocol.
type blockingModel struct{}

func (*blockingModel) GenerateContent(ctx context.Context, _ *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response)
	go func() {
		<-ctx.Done()
		close(responses)
	}()
	return responses, nil
}

func (*blockingModel) Info() model.Info { return model.Info{Name: "blocking-test"} }

func (m *requestCaptureModel) GenerateContent(_ context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.requests = append(m.requests, request)
	responses := make(chan *model.Response, 1)
	responses <- &model.Response{
		Done: true,
		Choices: []model.Choice{{Message: model.Message{
			Role:    model.RoleAssistant,
			Content: "done",
		}}},
	}
	close(responses)
	return responses, nil
}

func (*requestCaptureModel) Info() model.Info {
	return model.Info{Name: "request-capture"}
}
