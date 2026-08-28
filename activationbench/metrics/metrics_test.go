//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package metrics

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type cumulativeUsageModel struct {
	responses []*model.Response
}

func (m cumulativeUsageModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response, len(m.responses))
	for _, response := range m.responses {
		responses <- response
	}
	close(responses)
	return responses, nil
}

func (cumulativeUsageModel) Info() model.Info { return model.Info{Name: "cumulative-usage-test"} }

func TestTokenUsageAddNormalizesMissingTotals(t *testing.T) {
	var usage TokenUsage
	usage.Add(TokenUsage{PromptTokens: 7, CompletionTokens: 3})
	usage.Add(TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7})
	if usage.TotalTokens != 17 {
		t.Fatalf("total tokens = %d, want 17", usage.TotalTokens)
	}
	if usage.UncachedPromptTokens() != 12 {
		t.Fatalf("uncached prompt tokens = %d, want 12", usage.UncachedPromptTokens())
	}
}

func TestNewAggregateUsesSuccessNormalizedTokens(t *testing.T) {
	results := []RunResult{
		{Passed: true, Score: 1, Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, PeakVisibleTools: 4, InitialToolCount: 2, ToolCallCount: 1, LLMCalls: 2, Requests: []RequestRecord{{Usage: TokenUsage{PromptTokens: 10}, VisibleToolCount: 4}}},
		{Passed: false, Score: 0.5, Usage: TokenUsage{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23}, PeakVisibleTools: 8, InitialToolCount: 3, ToolCallCount: 2, LLMCalls: 3, Requests: []RequestRecord{{Usage: TokenUsage{PromptTokens: 20}, VisibleToolCount: 8}}},
	}
	agg := NewAggregate("test", results)
	if agg.Passed != 1 || agg.Runs != 2 {
		t.Fatalf("aggregate counts = (%d, %d), want (1, 2)", agg.Passed, agg.Runs)
	}
	if agg.TokensPerSuccess != 35 {
		t.Fatalf("tokens per success = %v, want 35", agg.TokensPerSuccess)
	}
	if agg.AverageVisibleTools != 6 {
		t.Fatalf("average visible tools = %v, want 6", agg.AverageVisibleTools)
	}
	if agg.P50TotalTokens != 12 || agg.P95TotalTokens != 23 {
		t.Fatalf("percentiles = (%d, %d), want (12, 23)", agg.P50TotalTokens, agg.P95TotalTokens)
	}
	if agg.MaxRequestPromptTokens != 20 || agg.MaxRequestVisibleTools != 8 {
		t.Fatalf("max request metrics = (%d, %d), want (20, 8)", agg.MaxRequestPromptTokens, agg.MaxRequestVisibleTools)
	}
}

func TestAggregateSeparatesFlowErrorsFromQualitySamples(t *testing.T) {
	results := []RunResult{
		{Passed: true, Score: 1},
		{Passed: false, Score: 0, Error: "context deadline exceeded"},
		{Passed: false, Score: 0.25},
		// A malformed caller result must still be counted in the all-sample
		// diagnostic, while the error keeps it out of the quality denominator.
		{Passed: true, Score: 1, Error: "provider stopped"},
	}
	agg := NewAggregate("quality-errors", results)
	if agg.Runs != 4 || agg.EvaluatedRuns != 2 || agg.ErrorRuns != 2 || agg.Passed != 1 {
		t.Fatalf("quality/error counts = runs=%d evaluated=%d errors=%d passed=%d, want (4, 2, 2, 1)",
			agg.Runs, agg.EvaluatedRuns, agg.ErrorRuns, agg.Passed)
	}
	if agg.PassRate != 0.5 || agg.ObservedPassRate != 0.5 {
		t.Fatalf("pass rates = quality=%v observed=%v, want (0.5, 0.5)", agg.PassRate, agg.ObservedPassRate)
	}
	if agg.AverageScore != 0.625 {
		t.Fatalf("average quality score = %v, want 0.625", agg.AverageScore)
	}
	if PassRate(results) != 0.5 || ObservedPassRate(results) != 0.5 {
		t.Fatalf("helper pass rates = quality=%v observed=%v, want (0.5, 0.5)",
			PassRate(results), ObservedPassRate(results))
	}
}

func TestNewAggregateIncludesTTFTAndTaskDurationMetrics(t *testing.T) {
	results := []RunResult{
		{
			DurationNanos: 10,
			Requests: []RequestRecord{
				{TTFTNanos: 2},
				// A request with no meaningful response is not a TTFT sample.
				{},
			},
		},
		{
			DurationNanos: 20,
			Requests: []RequestRecord{
				{TTFTNanos: 4},
				{TTFTNanos: 6},
			},
		},
		{DurationNanos: 30},
	}
	agg := NewAggregate("latency", results)
	if agg.TTFTSamples != 3 {
		t.Fatalf("ttft samples = %d, want 3", agg.TTFTSamples)
	}
	if agg.TTFTRequestCount != 4 || agg.TTFTMissingCount != 1 {
		t.Fatalf("ttft coverage = (%d, %d), want (4, 1)", agg.TTFTRequestCount, agg.TTFTMissingCount)
	}
	if agg.UsageReportedRequests != 0 || agg.UsageEstimatedRequests != 0 || agg.UsageMissingRequests != 4 {
		t.Fatalf("usage coverage = reported=%d estimated=%d missing=%d, want (0, 0, 4)",
			agg.UsageReportedRequests, agg.UsageEstimatedRequests, agg.UsageMissingRequests)
	}
	if agg.UsageSamples != 4 || agg.UsageKnownSamples != 0 || agg.UsageCoverage != 0 || agg.UsageComplete {
		t.Fatalf("usage completeness = samples=%d known=%d coverage=%v complete=%t, want (4, 0, 0, false)",
			agg.UsageSamples, agg.UsageKnownSamples, agg.UsageCoverage, agg.UsageComplete)
	}
	if agg.AverageTTFTNanos != 4 {
		t.Fatalf("average ttft = %v, want 4", agg.AverageTTFTNanos)
	}
	if agg.P50TTFTNanos != 4 || agg.P95TTFTNanos != 6 || agg.MaxTTFTNanos != 6 {
		t.Fatalf("ttft percentiles/max = (%d, %d, %d), want (4, 6, 6)",
			agg.P50TTFTNanos, agg.P95TTFTNanos, agg.MaxTTFTNanos)
	}
	if agg.TotalDurationNanos != 60 || agg.AverageDurationNanos != 20 {
		t.Fatalf("duration total/average = (%d, %v), want (60, 20)",
			agg.TotalDurationNanos, agg.AverageDurationNanos)
	}
	if agg.P50DurationNanos != 20 || agg.P95DurationNanos != 30 || agg.MaxDurationNanos != 30 {
		t.Fatalf("duration percentiles/max = (%d, %d, %d), want (20, 30, 30)",
			agg.P50DurationNanos, agg.P95DurationNanos, agg.MaxDurationNanos)
	}
	if agg.TaskFirstTTFTSamples != 2 || agg.AverageTaskFirstTTFTNanos != 3 ||
		agg.P50TaskFirstTTFTNanos != 2 || agg.P95TaskFirstTTFTNanos != 4 || agg.MaxTaskFirstTTFTNanos != 4 {
		t.Fatalf("task-first ttft = samples=%d avg=%v p50=%d p95=%d max=%d, want (2, 3, 2, 4, 4)",
			agg.TaskFirstTTFTSamples, agg.AverageTaskFirstTTFTNanos,
			agg.P50TaskFirstTTFTNanos, agg.P95TaskFirstTTFTNanos, agg.MaxTaskFirstTTFTNanos)
	}
	if agg.DurationSamples != 3 {
		t.Fatalf("duration samples = %d, want 3", agg.DurationSamples)
	}
}

func TestNewAggregateLatencyMetricsAreZeroWithoutSamples(t *testing.T) {
	agg := NewAggregate("empty-latency", []RunResult{{Requests: []RequestRecord{{}}, DurationNanos: 0}})
	if agg.TTFTSamples != 0 || agg.AverageTTFTNanos != 0 || agg.P50TTFTNanos != 0 ||
		agg.P95TTFTNanos != 0 || agg.MaxTTFTNanos != 0 {
		t.Fatalf("unexpected ttft metrics without samples: %+v", agg)
	}
	if agg.TTFTRequestCount != 1 || agg.TTFTMissingCount != 1 || agg.DurationSamples != 0 ||
		agg.TotalDurationNanos != 0 || agg.AverageDurationNanos != 0 {
		t.Fatalf("unexpected latency coverage without samples: %+v", agg)
	}
}

func TestAggregatePromptPercentileFallsBackWhenProviderOnlyReturnsTotal(t *testing.T) {
	agg := NewAggregate("total-only", []RunResult{
		{Usage: TokenUsage{TotalTokens: 120, CompletionTokens: 20}},
		{Usage: TokenUsage{TotalTokens: 80}},
	})
	if agg.P50PromptTokens != 80 || agg.P95PromptTokens != 100 {
		t.Fatalf("prompt percentiles = (%d, %d), want (80, 100)", agg.P50PromptTokens, agg.P95PromptTokens)
	}
}

func TestAggregateMaxPromptUsesCompletionWhenOnlyTotalIsReported(t *testing.T) {
	agg := NewAggregate("total-only-max", []RunResult{{
		Requests: []RequestRecord{{Usage: TokenUsage{TotalTokens: 120, CompletionTokens: 20}}},
	}})
	if agg.MaxRequestPromptTokens != 100 {
		t.Fatalf("max request prompt = %d, want 100", agg.MaxRequestPromptTokens)
	}
}

func TestAggregateSeparatesSyntheticAndProviderUsageCoverage(t *testing.T) {
	agg := NewAggregate("usage-sources", []RunResult{{
		Requests: []RequestRecord{
			{Usage: TokenUsage{TotalTokens: 3}, UsageSource: UsageSourceSynthetic},
			{Usage: TokenUsage{TotalTokens: 4}, UsageSource: UsageSourceReported},
			{UsageSource: UsageSourceMissing},
		},
	}})
	if agg.UsageSyntheticRequests != 1 || agg.UsageReportedRequests != 1 || agg.UsageMissingRequests != 1 {
		t.Fatalf("usage source coverage = synthetic=%d reported=%d missing=%d", agg.UsageSyntheticRequests, agg.UsageReportedRequests, agg.UsageMissingRequests)
	}
	if agg.UsageSamples != 3 || agg.UsageKnownSamples != 2 || math.Abs(agg.UsageCoverage-2.0/3.0) > 1e-12 || agg.UsageComplete {
		t.Fatalf("usage completeness = samples=%d known=%d coverage=%v complete=%t, want (3, 2, 2/3, false)",
			agg.UsageSamples, agg.UsageKnownSamples, agg.UsageCoverage, agg.UsageComplete)
	}
}

func TestAggregateMarksCompleteUsageSourcesAsComparable(t *testing.T) {
	agg := NewAggregate("complete-usage", []RunResult{{
		Requests: []RequestRecord{
			{Usage: TokenUsage{TotalTokens: 3}, UsageSource: UsageSourceReported},
			{Usage: TokenUsage{TotalTokens: 4}, UsageSource: UsageSourceSynthetic},
			{Usage: TokenUsage{TotalTokens: 5}, UsageSource: UsageSourceEstimated},
		},
	}})
	if agg.UsageSamples != 3 || agg.UsageKnownSamples != 3 || agg.UsageCoverage != 1 || !agg.UsageComplete {
		t.Fatalf("usage completeness = samples=%d known=%d coverage=%v complete=%t, want (3, 3, 1, true)",
			agg.UsageSamples, agg.UsageKnownSamples, agg.UsageCoverage, agg.UsageComplete)
	}
}

func TestNewComparisonReportsDynamicDeltas(t *testing.T) {
	static := []RunResult{{Passed: true, Score: 1, Usage: TokenUsage{TotalTokens: 100, PromptTokens: 80, CompletionTokens: 20}}}
	dynamic := []RunResult{{Passed: true, Score: 1, Usage: TokenUsage{TotalTokens: 60, PromptTokens: 45, CompletionTokens: 15}}}
	comparison := NewComparison("suite", static, dynamic)
	if comparison.TokenDelta.Absolute != -40 || comparison.AverageTokenDelta.Absolute != -40 {
		t.Fatalf("token delta = %v, want -40", comparison.TokenDelta.Absolute)
	}
	if comparison.TokenDelta.Relative != -0.4 {
		t.Fatalf("token relative delta = %v, want -0.4", comparison.TokenDelta.Relative)
	}
	if comparison.QualityDelta.Absolute != 0 {
		t.Fatalf("quality delta = %v, want 0", comparison.QualityDelta.Absolute)
	}
	if comparison.TokenDeltaComparable {
		t.Fatal("result-only comparison must not claim token comparability without raw usage samples")
	}
	if comparison.ScoreDelta.Absolute != 0 || comparison.PassRateDelta.Absolute != 0 {
		t.Fatalf("unexpected zero-quality deltas: score=%v pass=%v", comparison.ScoreDelta.Absolute, comparison.PassRateDelta.Absolute)
	}
}

func TestNewComparisonMarksCompleteRequestUsageComparable(t *testing.T) {
	static := []RunResult{{Usage: TokenUsage{TotalTokens: 100}, Requests: []RequestRecord{{Usage: TokenUsage{TotalTokens: 100}, UsageSource: UsageSourceReported}}}}
	dynamic := []RunResult{{Usage: TokenUsage{TotalTokens: 60}, Requests: []RequestRecord{{Usage: TokenUsage{TotalTokens: 60}, UsageSource: UsageSourceReported}}}}
	comparison := NewComparison("suite", static, dynamic)
	if !comparison.TokenDeltaComparable {
		t.Fatalf("complete request usage was marked incomparable: static=%+v dynamic=%+v", comparison.Static, comparison.Dynamic)
	}
	if !comparison.QualityDeltaComparable {
		t.Fatalf("error-free evaluated arms were marked quality-incomparable: static=%+v dynamic=%+v", comparison.Static, comparison.Dynamic)
	}
}

func TestNewComparisonMarksMissingArmUsageIncomparable(t *testing.T) {
	static := []RunResult{{Requests: []RequestRecord{{Usage: TokenUsage{TotalTokens: 100}, UsageSource: UsageSourceReported}}}}
	dynamic := []RunResult{{Requests: []RequestRecord{{UsageSource: UsageSourceMissing}}}}
	comparison := NewComparison("suite", static, dynamic)
	if comparison.TokenDeltaComparable {
		t.Fatal("missing request usage must make token comparison incomparable")
	}
}

func TestNewComparisonMarksFlowErrorsIncomparable(t *testing.T) {
	static := []RunResult{{
		Passed: true, Usage: TokenUsage{TotalTokens: 100},
		Requests: []RequestRecord{{Usage: TokenUsage{TotalTokens: 100}, UsageSource: UsageSourceReported}},
	}}
	dynamic := []RunResult{{
		Passed: false, Error: "provider unavailable", Usage: TokenUsage{TotalTokens: 60},
		Requests: []RequestRecord{{Usage: TokenUsage{TotalTokens: 60}, UsageSource: UsageSourceReported}},
	}}
	comparison := NewComparison("suite", static, dynamic)
	if comparison.TokenDeltaComparable || comparison.QualityDeltaComparable {
		t.Fatalf("flow error should block claims: token=%t quality=%t", comparison.TokenDeltaComparable, comparison.QualityDeltaComparable)
	}
	if comparison.Static.EvaluatedRuns != 1 || comparison.Dynamic.EvaluatedRuns != 0 || comparison.Dynamic.ErrorRuns != 1 {
		t.Fatalf("comparison error accounting = static=%+v dynamic=%+v", comparison.Static, comparison.Dynamic)
	}
}

func TestAnnotateRepetitionAddsStablePairIDs(t *testing.T) {
	input := []RunResult{{TaskID: "task-a", Mode: "static-all"}, {TaskID: "task-b", Mode: "dynamic-activation"}}
	annotated := AnnotateRepetition(input, 3)
	if annotated[0].PairID != "task-a#3" || annotated[1].PairID != "task-b#3" {
		t.Fatalf("pair ids = %q, %q", annotated[0].PairID, annotated[1].PairID)
	}
	if annotated[0].Repetition != 3 || annotated[1].Repetition != 3 {
		t.Fatalf("repetitions = %d, %d", annotated[0].Repetition, annotated[1].Repetition)
	}
	if input[0].PairID != "" || input[1].PairID != "" {
		t.Fatal("AnnotateRepetition mutated the input slice")
	}
}

func TestNewComparisonReportsSpecificQualityDeltas(t *testing.T) {
	static := []RunResult{{
		Passed: true, Score: 0.75, ToolRecall: 0.8, ToolPrecision: 0.9,
		WrongToolCalls: 2, InvalidToolCalls: 1, CollateralCount: 3,
		Usage: TokenUsage{TotalTokens: 100},
	}}
	dynamic := []RunResult{{
		Passed: false, Score: 0.5, ToolRecall: 0.6, ToolPrecision: 0.7,
		WrongToolCalls: 1, InvalidToolCalls: 0, CollateralCount: 1,
		Usage: TokenUsage{TotalTokens: 80},
	}}
	comparison := NewComparison("suite", static, dynamic)
	if comparison.PassRateDelta.Absolute != -1 {
		t.Fatalf("pass-rate delta = %v, want -1", comparison.PassRateDelta.Absolute)
	}
	if comparison.ScoreDelta.Absolute != -0.25 {
		t.Fatalf("score delta = %v, want -0.25", comparison.ScoreDelta.Absolute)
	}
	if math.Abs(comparison.ToolRecallDelta.Absolute+0.2) > 1e-9 || math.Abs(comparison.ToolPrecisionDelta.Absolute+0.2) > 1e-9 {
		t.Fatalf("selection deltas = (%v, %v), want (-0.2, -0.2)", comparison.ToolRecallDelta.Absolute, comparison.ToolPrecisionDelta.Absolute)
	}
	if comparison.WrongToolCallDelta.Absolute != -1 || comparison.InvalidToolCallDelta.Absolute != -1 || comparison.CollateralDelta.Absolute != -2 {
		t.Fatalf("error deltas = (%v, %v, %v), want (-1, -1, -2)", comparison.WrongToolCallDelta.Absolute, comparison.InvalidToolCallDelta.Absolute, comparison.CollateralDelta.Absolute)
	}
}

func TestComparisonSeparatesTotalAndAverageTokenDeltas(t *testing.T) {
	static := []RunResult{
		{Usage: TokenUsage{TotalTokens: 100}},
		{Usage: TokenUsage{TotalTokens: 300}},
	}
	dynamic := []RunResult{
		{Usage: TokenUsage{TotalTokens: 50}},
		{Usage: TokenUsage{TotalTokens: 100}},
	}
	comparison := NewComparison("suite", static, dynamic)
	if comparison.TokenDelta.Absolute != -250 {
		t.Fatalf("total token delta = %v, want -250", comparison.TokenDelta.Absolute)
	}
	if comparison.AverageTokenDelta.Absolute != -125 {
		t.Fatalf("average token delta = %v, want -125", comparison.AverageTokenDelta.Absolute)
	}
}

func TestComparisonKeepsFractionalAverageTokenDelta(t *testing.T) {
	static := []RunResult{
		{Usage: TokenUsage{TotalTokens: 100}},
		{Usage: TokenUsage{TotalTokens: 100}},
		{Usage: TokenUsage{TotalTokens: 100}},
	}
	dynamic := []RunResult{
		{Usage: TokenUsage{TotalTokens: 99}},
		{Usage: TokenUsage{TotalTokens: 100}},
		{Usage: TokenUsage{TotalTokens: 100}},
	}
	comparison := NewComparison("suite", static, dynamic)
	want := -1.0 / 3.0
	if math.Abs(comparison.AverageTokenDelta.Absolute-want) > 1e-12 {
		t.Fatalf("fractional average token delta = %v, want %v", comparison.AverageTokenDelta.Absolute, want)
	}
	if math.Abs(comparison.Dynamic.AverageTotalTokens-299.0/3.0) > 1e-12 {
		t.Fatalf("exact dynamic average = %v, want %v", comparison.Dynamic.AverageTotalTokens, 299.0/3.0)
	}
}

func TestRecorderUsesTerminalStreamingUsageOncePerRequest(t *testing.T) {
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{IsPartial: true, Choices: []model.Choice{{Delta: model.Message{Content: "x"}}}, Usage: &model.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}},
		{IsPartial: true, Choices: []model.Choice{{Delta: model.Message{Content: "y"}}}, Usage: &model.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}},
		{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "xy"}}}, Usage: &model.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	usage := recorded[0].Usage
	if usage.PromptTokens != 100 || usage.CompletionTokens != 20 || usage.TotalTokens != 120 {
		t.Fatalf("usage = %+v, want prompt=100 completion=20 total=120", usage)
	}
	if recorded[0].UsageSource != "reported" {
		t.Fatalf("usage source = %q, want reported", recorded[0].UsageSource)
	}
	if recorded[0].TTFTNanos <= 0 {
		t.Fatalf("ttft = %d, want a positive first-response latency", recorded[0].TTFTNanos)
	}
}

func TestRecorderRejectsNilContext(t *testing.T) {
	recorder := NewRecorder(cumulativeUsageModel{})
	if _, err := recorder.GenerateContent(nil, &model.Request{}); err == nil {
		t.Fatal("channel path should reject a nil context")
	}
	if _, err := recorder.GenerateContentIter(nil, &model.Request{}); err == nil {
		t.Fatal("iterator path should reject a nil context")
	}
}

func TestRecorderPrefersTerminalUsageOverLargerPartialSnapshot(t *testing.T) {
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{IsPartial: true, Usage: &model.Usage{PromptTokens: 100, CompletionTokens: 90, TotalTokens: 190}},
		{Done: true, Usage: &model.Usage{PromptTokens: 100, CompletionTokens: 8, TotalTokens: 108}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	if got := recorded[0].Usage; got.TotalTokens != 108 || got.CompletionTokens != 8 {
		t.Fatalf("usage = %+v, want terminal snapshot", got)
	}
}

func TestRecorderCapturesAllChoicesAndDeltaToolCalls(t *testing.T) {
	firstIndex := 0
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{IsPartial: true, Choices: []model.Choice{{Index: 0, Delta: model.Message{ToolCalls: []model.ToolCall{{ID: "call-1", Index: &firstIndex, Function: model.FunctionDefinitionParam{Name: "search"}}}}}}},
		{Done: true, Choices: []model.Choice{
			{Index: 0, Message: model.Message{ToolCalls: []model.ToolCall{{ID: "call-1", Function: model.FunctionDefinitionParam{Name: "search", Arguments: []byte(`{"q":"x"}`)}}}}},
			{Index: 1, Delta: model.Message{ToolCalls: []model.ToolCall{{ID: "call-2", Function: model.FunctionDefinitionParam{Name: "save", Arguments: []byte(`{}`)}}}}},
		}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 || len(recorded[0].ToolCallsReturned) != 2 {
		t.Fatalf("tool calls = %+v, want search/save", recorded)
	}
	if recorded[0].ToolCallsReturned[0] != "search" || recorded[0].ToolCallsReturned[1] != "save" {
		t.Fatalf("tool call names = %v", recorded[0].ToolCallsReturned)
	}
	if len(recorded[0].ToolCallIDs) != 2 || recorded[0].ToolCallIDs[0] != "call-1" || recorded[0].ToolCallIDs[1] != "call-2" {
		t.Fatalf("tool call ids = %v", recorded[0].ToolCallIDs)
	}
	if recorded[0].ToolCallArguments[0] != `{"q":"x"}` {
		t.Fatalf("merged arguments = %q", recorded[0].ToolCallArguments[0])
	}
}

func TestRecorderRetainsToolCallFromPartialBeforeTextOnlyTerminal(t *testing.T) {
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{IsPartial: true, Choices: []model.Choice{{Delta: model.Message{ToolCalls: []model.ToolCall{{
			ID: "call-1", Function: model.FunctionDefinitionParam{Name: "search"},
		}}}}}},
		{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "done"}}}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	requests := recorder.Snapshot()
	if len(requests) != 1 || len(requests[0].ToolCallsReturned) != 1 {
		t.Fatalf("partial tool call was lost: %#v", requests)
	}
	if requests[0].ToolCallsReturned[0] != "search" || requests[0].ToolCallIDs[0] != "call-1" {
		t.Fatalf("unexpected retained tool call: %#v", requests[0])
	}
}

func TestRecorderMergesIndexIdentifiedStreamingToolCallWithoutID(t *testing.T) {
	index := 0
	first := &model.Response{IsPartial: true, Choices: []model.Choice{{
		Delta: model.Message{ToolCalls: []model.ToolCall{{
			Index: &index,
			Function: model.FunctionDefinitionParam{
				Name:      "search",
				Arguments: []byte(`{"query":"`),
			},
		}}},
	}}}
	second := &model.Response{IsPartial: true, Choices: []model.Choice{{
		Delta: model.Message{ToolCalls: []model.ToolCall{{
			Index:    &index,
			Function: model.FunctionDefinitionParam{Arguments: []byte(`Atlas"}`)},
		}}},
	}}}
	terminal := &model.Response{Done: true, Choices: []model.Choice{{
		Delta: model.Message{ToolCalls: []model.ToolCall{{
			Index:    &index,
			Function: model.FunctionDefinitionParam{Arguments: []byte(`{"query":"Atlas"}`)},
		}}},
	}}}
	recorder := NewRecorder(cumulativeUsageModel{responses: []*model.Response{
		first, second, terminal,
	}})
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	requests := recorder.Snapshot()
	if len(requests) != 1 || len(requests[0].ToolCallsReturned) != 1 {
		t.Fatalf("tool calls = %#v, want one merged call", requests)
	}
	if requests[0].ToolCallsReturned[0] != "search" {
		t.Fatalf("tool call name = %q, want search", requests[0].ToolCallsReturned[0])
	}
	if got := requests[0].ToolCallArguments[0]; got != `{"query":"Atlas"}` {
		t.Fatalf("merged arguments = %q, want complete JSON", got)
	}
}

func TestRecorderPrefersTerminalToolCallsOverMalformedPartialSnapshots(t *testing.T) {
	// A provider can reuse an ID while sending partial deltas and finish with a
	// complete tool-call snapshot. The terminal call is what the framework
	// executes; partial fragments must not be appended to it in the report.
	partialStart := model.ToolCall{
		ID: "call-1",
		Function: model.FunctionDefinitionParam{
			Name: "skill_load", Arguments: []byte(`{"skill":"files"}`),
		},
	}
	partialContinuation := model.ToolCall{
		ID:       "call-1",
		Function: model.FunctionDefinitionParam{Arguments: []byte(`ill":"documents"}`)},
	}
	terminalCall := model.ToolCall{
		ID: "call-1",
		Function: model.FunctionDefinitionParam{
			Name: "skill_load", Arguments: []byte(`{"skill":"files"}`),
		},
	}
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{IsPartial: true, Choices: []model.Choice{{Delta: model.Message{ToolCalls: []model.ToolCall{partialStart}}}}},
		{IsPartial: true, Choices: []model.Choice{{Delta: model.Message{ToolCalls: []model.ToolCall{partialContinuation}}}}},
		{Done: true, Choices: []model.Choice{{Message: model.Message{ToolCalls: []model.ToolCall{terminalCall}}}}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	requests := recorder.Snapshot()
	if len(requests) != 1 || len(requests[0].ToolCallsReturned) != 1 {
		t.Fatalf("tool calls = %#v, want one terminal call", requests)
	}
	if requests[0].ToolCallsReturned[0] != "skill_load" {
		t.Fatalf("tool call name = %q, want skill_load", requests[0].ToolCallsReturned[0])
	}
	if got := requests[0].ToolCallArguments[0]; got != `{"skill":"files"}` {
		t.Fatalf("tool call arguments = %q, want terminal JSON", got)
	}
}

func TestRecorderLabelsSyntheticUsageExplicitly(t *testing.T) {
	recorder := NewRecorder(cumulativeUsageModel{responses: []*model.Response{
		{Done: true, Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}},
	}})
	recorder.UsageSourceOverride = UsageSourceSynthetic
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 || recorded[0].UsageSource != UsageSourceSynthetic {
		t.Fatalf("usage source = %+v, want synthetic", recorded)
	}
}

func TestRecorderRecordsResponseLevelError(t *testing.T) {
	recorder := NewRecorder(cumulativeUsageModel{responses: []*model.Response{
		{Done: true, Error: &model.ResponseError{Type: model.ErrorTypeAPIError, Message: "rate limited"}},
	}})
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 || recorded[0].Error != "api_error: rate limited" {
		t.Fatalf("response error record = %+v", recorded)
	}
}

type iterOnlyModel struct {
	responses []*model.Response
	iterCalls int
}

func (m *iterOnlyModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	return nil, fmt.Errorf("channel path should not be used")
}

func (m *iterOnlyModel) GenerateContentIter(context.Context, *model.Request) (model.Seq[*model.Response], error) {
	m.iterCalls++
	return func(yield func(*model.Response) bool) {
		for _, response := range m.responses {
			if !yield(response) {
				return
			}
		}
	}, nil
}

func (m *iterOnlyModel) Info() model.Info { return model.Info{Name: "iter-only-test"} }

func TestRecorderPreservesIterModelFastPath(t *testing.T) {
	base := &iterOnlyModel{responses: []*model.Response{{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "ok"}}}, Usage: &model.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}}}}
	recorder := NewRecorder(base)
	seq, err := recorder.GenerateContentIter(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContentIter: %v", err)
	}
	seq(func(*model.Response) bool { return true })
	if base.iterCalls != 1 {
		t.Fatalf("iter calls = %d, want 1", base.iterCalls)
	}
	if got := recorder.Snapshot(); len(got) != 1 || got[0].Usage.TotalTokens != 6 {
		t.Fatalf("recorded iter usage = %+v", got)
	}
}

// cancelAwareChannelModel exercises the compatibility adapter used when a
// provider has the legacy channel API but the framework consumes it through
// model.IterModel. It emits a terminal snapshot only after cancellation so
// the recorder must cancel and drain before committing the request record.
type cancelAwareChannelModel struct {
	terminalObserved chan struct{}
}

func (m *cancelAwareChannelModel) GenerateContent(ctx context.Context, _ *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response)
	go func() {
		defer close(responses)
		responses <- &model.Response{IsPartial: true, Choices: []model.Choice{{Delta: model.Message{Content: "partial"}}}, Usage: &model.Usage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}}
		<-ctx.Done()
		responses <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "final"}}}, Usage: &model.Usage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14}}
		if m.terminalObserved != nil {
			close(m.terminalObserved)
		}
	}()
	return responses, nil
}

func (cancelAwareChannelModel) Info() model.Info {
	return model.Info{Name: "cancel-aware-channel-test"}
}

func TestRecorderCancelsAndDrainsChannelIterator(t *testing.T) {
	modelUnderTest := &cancelAwareChannelModel{terminalObserved: make(chan struct{})}
	recorder := NewRecorder(modelUnderTest)
	seq, err := recorder.GenerateContentIter(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContentIter: %v", err)
	}
	seq(func(*model.Response) bool { return false })
	select {
	case <-modelUnderTest.terminalObserved:
	case <-time.After(time.Second):
		t.Fatal("channel provider was not drained after early stop")
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	if got := recorded[0].Usage; got.TotalTokens != 14 || got.CompletionTokens != 4 {
		t.Fatalf("drained terminal usage = %+v, want total=14 completion=4", got)
	}
}

func TestRecorderTTFTStartsAtFirstMeaningfulResponse(t *testing.T) {
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		// Nil and metadata-only responses can occur in a streaming channel and
		// must not establish the first-token timestamp.
		nil,
		{IsPartial: true},
		{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "done"}}}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	if recorded[0].ResponseCount != 3 {
		t.Fatalf("response count = %d, want 3", recorded[0].ResponseCount)
	}
	if recorded[0].TTFTNanos <= 0 {
		t.Fatalf("ttft = %d, want a positive first meaningful response latency", recorded[0].TTFTNanos)
	}
}

func TestRecorderTTFTIncludesGenerateContentSetupLatency(t *testing.T) {
	modelUnderTest := delayedModel{
		delay:    2 * time.Millisecond,
		response: &model.Response{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "done"}}}},
	}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	if recorded[0].TTFTNanos < modelUnderTest.delay.Nanoseconds() {
		t.Fatalf("ttft = %d, want at least setup delay %d", recorded[0].TTFTNanos, modelUnderTest.delay.Nanoseconds())
	}
}

// markerDeclarationTool makes the recorder's request metadata pass observable.
// TTFT must begin after this pass, otherwise schema inspection is incorrectly
// reported as model first-response latency.
type markerDeclarationTool struct {
	finished chan time.Time
}

func (t markerDeclarationTool) Declaration() *tool.Declaration {
	// The channel is buffered so requestRecord can finish without depending on
	// the test receiving the marker concurrently.
	t.finished <- time.Now()
	return &tool.Declaration{Name: "slow"}
}

func TestRecorderTTFTExcludesRequestMetadataCollection(t *testing.T) {
	recorder := NewRecorder(cumulativeUsageModel{responses: []*model.Response{
		{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "done"}}}},
	}})
	finished := make(chan time.Time, 1)
	request := &model.Request{Tools: map[string]tool.Tool{
		"slow": markerDeclarationTool{finished: finished},
	}}
	capture := recorder.newCapture(request)
	declarationFinished := <-finished
	if capture.start.Before(declarationFinished) {
		t.Fatalf("capture start %v precedes request metadata completion %v", capture.start, declarationFinished)
	}
}

func TestRecorderNormalizesLargestUsageWhenTotalIsMissing(t *testing.T) {
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{IsPartial: true, Usage: &model.Usage{PromptTokens: 10, CompletionTokens: 2}},
		{Done: true, Usage: &model.Usage{PromptTokens: 10, CompletionTokens: 5}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	usage := recorded[0].Usage
	if usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v, want prompt=10 completion=5 total=15", usage)
	}
	if recorded[0].UsageSource != "reported" {
		t.Fatalf("usage source = %q, want reported", recorded[0].UsageSource)
	}
}

func TestRecorderLeavesMissingProviderUsageUnestimated(t *testing.T) {
	modelUnderTest := cumulativeUsageModel{responses: []*model.Response{
		{Done: true, Choices: []model.Choice{{Message: model.Message{Content: "done"}}}},
	}}
	recorder := NewRecorder(modelUnderTest)
	responses, err := recorder.GenerateContent(context.Background(), &model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "a prompt"}},
	})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	for range responses {
	}
	recorded := recorder.Snapshot()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	if recorded[0].Usage != (TokenUsage{}) {
		t.Fatalf("missing provider usage was estimated: %+v", recorded[0].Usage)
	}
	if recorded[0].UsageSource != "missing" {
		t.Fatalf("usage source = %q, want missing", recorded[0].UsageSource)
	}
}

type delayedModel struct {
	delay    time.Duration
	response *model.Response
}

func (m delayedModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	time.Sleep(m.delay)
	responses := make(chan *model.Response, 1)
	responses <- m.response
	close(responses)
	return responses, nil
}

func (delayedModel) Info() model.Info { return model.Info{Name: "delayed-test"} }
