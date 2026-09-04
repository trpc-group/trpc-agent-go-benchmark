//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package metrics defines the provider-independent measurements emitted by
// ActivationBench-Lite.  It intentionally keeps raw request observations in
// the report so a token saving claim can be audited after a run.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// TokenUsage is an additive token counter.  Prompt and completion counts are
// kept separate because dynamic tool activation primarily affects prompt
// schema tokens.  Cached and reasoning counters are retained when providers
// expose them.
type TokenUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	CachedPromptTokens  int `json:"cached_prompt_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
}

// Usage sources are deliberately explicit because a report may contain
// provider usage, externally estimated usage, or synthetic test fixtures.
// Synthetic values must never be presented as provider billing records.
const (
	UsageSourceReported  = "reported"
	UsageSourceEstimated = "estimated"
	UsageSourceMissing   = "missing"
	UsageSourceSynthetic = "synthetic"
)

// Add adds another usage value to u.
func (u *TokenUsage) Add(other TokenUsage) {
	if u == nil {
		return
	}
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	// Providers occasionally omit TotalTokens while returning the two
	// component counters.  Normalize each addend before accumulating so a
	// multi-request run cannot silently undercount.
	otherTotal := other.TotalTokens
	if otherTotal == 0 {
		otherTotal = other.PromptTokens + other.CompletionTokens
	}
	u.TotalTokens += otherTotal
	u.CachedPromptTokens += other.CachedPromptTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.ReasoningTokens += other.ReasoningTokens
	if u.TotalTokens == 0 && (u.PromptTokens != 0 || u.CompletionTokens != 0) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
}

// UncachedPromptTokens returns the prompt tokens not served from a cache.
func (u TokenUsage) UncachedPromptTokens() int {
	n := u.PromptTokens - u.CachedPromptTokens
	if n < 0 {
		return 0
	}
	return n
}

// RequestRecord captures one model request and its returned usage.
type RequestRecord struct {
	Index int `json:"index"`
	// Streaming records the effective framework request mode. It makes a
	// zero-TTFT sample auditable: non-streaming providers deliver the first
	// meaningful response only after the complete response is available.
	Streaming        bool       `json:"streaming"`
	VisibleTools     []string   `json:"visible_tools,omitempty"`
	VisibleToolCount int        `json:"visible_tool_count"`
	MessageBytes     int        `json:"message_bytes,omitempty"`
	ToolSchemaBytes  int        `json:"tool_schema_bytes,omitempty"`
	Usage            TokenUsage `json:"usage"`
	// UsageSource describes where Usage came from. See the UsageSource*
	// constants; synthetic is reserved for explicitly marked test/replay data.
	UsageSource   string `json:"usage_source,omitempty"`
	ResponseCount int    `json:"response_count"`
	// TTFTNanos is the wall-clock time from GenerateContent invocation until
	// the first meaningful response is received. Meaningful follows
	// model.Response.IsValidContent, so nil/metadata-only chunks do not count.
	// A zero value means that no meaningful response was observed (for example,
	// an empty stream or a model error). It deliberately measures adapter-visible
	// arrival time rather than relying on a provider-specific timing field so
	// static/dynamic runs use one definition.
	TTFTNanos         int64    `json:"ttft_nanos,omitempty"`
	ToolCallsReturned []string `json:"tool_calls_returned,omitempty"`
	ToolCallIDs       []string `json:"tool_call_ids,omitempty"`
	ToolCallArguments []string `json:"tool_call_arguments,omitempty"`
	Error             string   `json:"error,omitempty"`
	DurationNanos     int64    `json:"duration_nanos,omitempty"`
}

// CallRecord captures one observed tool call. For Framework calls the public
// event API exposes the attempt but not the internal tool result, so
// Succeeded is intentionally not inferred from the model response.
type CallRecord struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Succeeded bool   `json:"succeeded"`
	// OutcomeKnown is false for framework attempts because the public event
	// stream does not expose their internal tool result.
	OutcomeKnown bool `json:"outcome_known,omitempty"`
	// Framework marks an internal Skill/tool-surface call (for example
	// skill_load). Framework calls are useful for activation accounting but
	// should not lower task-tool precision or count as invalid domain calls.
	Framework bool   `json:"framework,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ActivationRecord describes an inferred activation event.  The framework's
// activation records are intentionally internal; the runner derives these
// records from skill_load calls and the visible tool snapshots, which keeps
// the benchmark compatible with public APIs.
type ActivationRecord struct {
	Skill          string   `json:"skill,omitempty"`
	ToolSet        string   `json:"tool_set,omitempty"`
	ActivatedTools []string `json:"activated_tools,omitempty"`
	NewToolCount   int      `json:"new_tool_count"`
	RequestIndex   int      `json:"request_index"`
	Lifetime       string   `json:"lifetime,omitempty"`
	Necessary      bool     `json:"necessary"`
}

// RunResult is the complete result for one task/mode pair.
type RunResult struct {
	TaskID string `json:"task_id"`
	// PairID identifies the same task/repetition in the Static and Dynamic
	// arms. It is populated by AnnotateRepetition; callers that run a single
	// comparison may leave it empty and pair by TaskID.
	PairID             string  `json:"pair_id,omitempty"`
	Repetition         int     `json:"repetition,omitempty"`
	Mode               string  `json:"mode"`
	SessionID          string  `json:"session_id,omitempty"`
	Passed             bool    `json:"passed"`
	Score              float64 `json:"score"`
	EvaluationMessage  string  `json:"evaluation_message,omitempty"`
	RequiredToolCount  int     `json:"required_tool_count"`
	SatisfiedToolCount int     `json:"satisfied_tool_count"`
	CollateralCount    int     `json:"collateral_count"`
	ToolRecall         float64 `json:"tool_recall"`
	ToolPrecision      float64 `json:"tool_precision"`
	Error              string  `json:"error,omitempty"`
	// DurationNanos covers the framework agent run and event drain. It includes
	// recorder/runner overhead and excludes the evaluator call that follows the
	// drain; use the CLI arm wall-clock fields for end-to-end harness time.
	DurationNanos      int64              `json:"duration_nanos,omitempty"`
	Usage              TokenUsage         `json:"usage"`
	Requests           []RequestRecord    `json:"requests,omitempty"`
	ToolCalls          []CallRecord       `json:"tool_calls,omitempty"`
	Activations        []ActivationRecord `json:"activations,omitempty"`
	RequiredTools      []string           `json:"required_tools,omitempty"`
	RequiredSkills     []string           `json:"required_skills,omitempty"`
	InitialToolCount   int                `json:"initial_tool_count"`
	PeakVisibleTools   int                `json:"peak_visible_tools"`
	FinalVisibleTools  int                `json:"final_visible_tools"`
	WrongToolCalls     int                `json:"wrong_tool_calls"`
	InvalidToolCalls   int                `json:"invalid_tool_calls"`
	LLMCalls           int                `json:"llm_calls"`
	ToolCallCount      int                `json:"tool_call_count"`
	FrameworkCallCount int                `json:"framework_call_count"`
	SkillLoadCount     int                `json:"skill_load_count"`
	ActivationCount    int                `json:"activation_count"`
}

// PassRate returns the evaluator success rate for usable task results.
// Results with a non-empty RunResult.Error are infrastructure/flow failures,
// not quality observations, and are excluded from this denominator. Use
// ObservedPassRate when a caller explicitly wants the all-sample diagnostic.
func PassRate(results []RunResult) float64 {
	passed, evaluated := qualityCounts(results)
	if evaluated == 0 {
		return 0
	}
	return float64(passed) / float64(evaluated)
}

// ObservedPassRate returns the fraction of all result samples marked passed,
// including samples that carry an infrastructure/flow error. It is a
// diagnostic only; quality comparisons should use PassRate and inspect the
// error counts exposed by Aggregate.
func ObservedPassRate(results []RunResult) float64 {
	if len(results) == 0 {
		return 0
	}
	passed := 0
	for _, result := range results {
		if result.Passed {
			passed++
		}
	}
	return float64(passed) / float64(len(results))
}

func qualityCounts(results []RunResult) (passed, evaluated int) {
	for _, result := range results {
		if strings.TrimSpace(result.Error) != "" {
			continue
		}
		evaluated++
		if result.Passed {
			passed++
		}
	}
	return passed, evaluated
}

// AnnotateRepetition returns results with an explicit pairing key. Repeated
// runs otherwise share the same TaskID, which makes array position the only
// way to identify a pair after serialization or filtering. The returned slice
// is independent at the top level; nested diagnostic slices are intentionally
// left untouched because this helper only changes pairing metadata.
func AnnotateRepetition(results []RunResult, repetition int) []RunResult {
	annotated := append([]RunResult(nil), results...)
	for index := range annotated {
		annotated[index].Repetition = repetition
		annotated[index].PairID = fmt.Sprintf("%s#%d", annotated[index].TaskID, repetition)
	}
	return annotated
}

// Aggregate is a summary for one benchmark mode.
type Aggregate struct {
	Mode string `json:"mode"`
	// Runs counts every task-result sample, including samples that stopped with
	// an infrastructure/flow error. EvaluatedRuns is the quality denominator;
	// ErrorRuns makes the excluded samples explicit.
	Runs          int `json:"runs"`
	EvaluatedRuns int `json:"evaluated_runs"`
	ErrorRuns     int `json:"error_runs"`
	Passed        int `json:"passed"`
	// PassRate is Passed / EvaluatedRuns. It excludes RunResult.Error samples;
	// ObservedPassRate below is the all-sample diagnostic.
	PassRate float64 `json:"pass_rate"`
	// ObservedPassRate is the all-sample diagnostic (Passed / Runs). It can be
	// lower than PassRate when provider/flow errors are present.
	ObservedPassRate       float64    `json:"observed_pass_rate"`
	AverageScore           float64    `json:"average_score"`
	AverageToolRecall      float64    `json:"average_tool_recall"`
	AverageToolPrecision   float64    `json:"average_tool_precision"`
	AverageCollateralCount float64    `json:"average_collateral_count"`
	Usage                  TokenUsage `json:"usage"`
	// AverageUsage keeps integer counters for compatibility with the first Lite
	// report format; integer division truncates fractional per-run values.
	AverageUsage TokenUsage `json:"average_usage"`
	// AverageTotalTokens is the exact per-result average used for comparisons.
	// Keep it separate from AverageUsage so a one-token difference is not lost
	// when a suite has more than one run.
	AverageTotalTokens            float64 `json:"average_total_tokens"`
	TokensPerSuccess              float64 `json:"tokens_per_success"`
	UncachedTokensPerSuccess      float64 `json:"uncached_tokens_per_success"`
	AverageVisibleTools           float64 `json:"average_visible_tools"`
	AverageInitialTools           float64 `json:"average_initial_tools"`
	AverageToolCalls              float64 `json:"average_tool_calls"`
	AverageLLMCalls               float64 `json:"average_llm_calls"`
	AverageActivations            float64 `json:"average_activations"`
	AverageActivatedTools         float64 `json:"average_activated_tools"`
	AverageNecessaryActivations   float64 `json:"average_necessary_activations"`
	AverageUnnecessaryActivations float64 `json:"average_unnecessary_activations"`
	AverageWrongToolCalls         float64 `json:"average_wrong_tool_calls"`
	AverageInvalidToolCalls       float64 `json:"average_invalid_tool_calls"`
	AverageFrameworkCalls         float64 `json:"average_framework_calls"`
	AverageSkillLoads             float64 `json:"average_skill_loads"`
	UsageReportedRequests         int     `json:"usage_reported_requests"`
	UsageSyntheticRequests        int     `json:"usage_synthetic_requests"`
	UsageEstimatedRequests        int     `json:"usage_estimated_requests"`
	UsageMissingRequests          int     `json:"usage_missing_requests"`
	// UsageSamples and UsageKnownSamples count raw RequestRecord samples. A
	// token delta is claimable only when every request in both arms has a known
	// reported, synthetic, or explicitly estimated usage value. Results built
	// without raw Requests intentionally have zero coverage rather than
	// guessing how many model calls their aggregate Usage represents.
	UsageSamples      int     `json:"usage_samples"`
	UsageKnownSamples int     `json:"usage_known_samples"`
	UsageCoverage     float64 `json:"usage_coverage"`
	UsageComplete     bool    `json:"usage_complete"`
	// TTFT fields summarize observed first-meaningful-response latency across
	// all model requests in the run. Requests that return no meaningful response
	// are not included; TTFTSamples and TTFTRequestCount make coverage explicit.
	TTFTSamples      int     `json:"ttft_samples"`
	TTFTRequestCount int     `json:"ttft_request_count"`
	TTFTMissingCount int     `json:"ttft_missing_count"`
	AverageTTFTNanos float64 `json:"average_ttft_nanos"`
	P50TTFTNanos     int64   `json:"p50_ttft_nanos"`
	P95TTFTNanos     int64   `json:"p95_ttft_nanos"`
	MaxTTFTNanos     int64   `json:"max_ttft_nanos"`
	// TaskFirstTTFT fields use the first observed model response in each task.
	// This task-weighted view is the preferred latency comparison when Static
	// and Dynamic produce different numbers of model requests.
	TaskFirstTTFTSamples      int     `json:"task_first_ttft_samples"`
	AverageTaskFirstTTFTNanos float64 `json:"average_task_first_ttft_nanos"`
	P50TaskFirstTTFTNanos     int64   `json:"p50_task_first_ttft_nanos"`
	P95TaskFirstTTFTNanos     int64   `json:"p95_task_first_ttft_nanos"`
	MaxTaskFirstTTFTNanos     int64   `json:"max_task_first_ttft_nanos"`
	// Duration fields summarize measured task wall-clock duration
	// (RunResult.DurationNanos). Results with a zero duration are retained in
	// Runs but excluded from duration statistics; DurationSamples is the
	// denominator for the duration aggregates.
	DurationSamples      int     `json:"duration_samples"`
	TotalDurationNanos   int64   `json:"total_duration_nanos"`
	AverageDurationNanos float64 `json:"average_duration_nanos"`
	P50DurationNanos     int64   `json:"p50_duration_nanos"`
	P95DurationNanos     int64   `json:"p95_duration_nanos"`
	MaxDurationNanos     int64   `json:"max_duration_nanos"`
	// AverageVisibleTools is retained as the mean peak menu size for backwards
	// compatibility with early Lite reports. Use AveragePeakVisibleTools when
	// the distinction matters, and AverageRequestVisibleTools for the mean over
	// every model request.
	P50TotalTokens             int     `json:"p50_total_tokens"`
	P95TotalTokens             int     `json:"p95_total_tokens"`
	P50PromptTokens            int     `json:"p50_prompt_tokens"`
	P95PromptTokens            int     `json:"p95_prompt_tokens"`
	MaxRequestPromptTokens     int     `json:"max_request_prompt_tokens"`
	MaxRequestVisibleTools     int     `json:"max_request_visible_tools"`
	AveragePeakVisibleTools    float64 `json:"average_peak_visible_tools"`
	AverageRequestVisibleTools float64 `json:"average_request_visible_tools"`
}

// Comparison contains paired static/dynamic results and deltas.  Raw runs
// remain available so callers can perform their own statistical tests.
type Comparison struct {
	Benchmark   string    `json:"benchmark"`
	GeneratedAt time.Time `json:"generated_at"`
	Static      Aggregate `json:"static"`
	Dynamic     Aggregate `json:"dynamic"`
	TokenDelta  Delta     `json:"token_delta"`
	// TokenDeltaComparable is false when either arm has a missing usage sample
	// (or no raw request samples). Numeric deltas are still retained for
	// diagnostics, but callers should not present them as a token-saving claim.
	TokenDeltaComparable bool `json:"token_delta_comparable"`
	// AverageTokenDelta is the per-task (or per repetition-task) token delta.
	// TokenDelta above uses the aggregate totals; both are retained because a
	// total is useful for cost accounting while an average is easier to compare
	// across suites with different task counts.
	AverageTokenDelta Delta `json:"average_token_delta"`
	// QualityDelta is retained as the pass-rate delta for compatibility with
	// early Lite reports. The more specific deltas below make partial quality
	// changes and tool-selection errors auditable without collapsing them into
	// one score.
	QualityDelta         Delta `json:"quality_delta"`
	PassRateDelta        Delta `json:"pass_rate_delta"`
	ScoreDelta           Delta `json:"score_delta"`
	ToolRecallDelta      Delta `json:"tool_recall_delta"`
	ToolPrecisionDelta   Delta `json:"tool_precision_delta"`
	WrongToolCallDelta   Delta `json:"wrong_tool_call_delta"`
	InvalidToolCallDelta Delta `json:"invalid_tool_call_delta"`
	CollateralDelta      Delta `json:"collateral_delta"`
	// QualityDeltaComparable is false when either arm contains an
	// infrastructure/flow error or has no evaluator-observed samples. Numeric
	// quality deltas remain available for diagnostics in that case.
	QualityDeltaComparable bool        `json:"quality_delta_comparable"`
	StaticRuns             []RunResult `json:"static_runs"`
	DynamicRuns            []RunResult `json:"dynamic_runs"`
}

// Delta expresses dynamic minus static for a selected metric.  Relative is
// zero when the static denominator is zero.
type Delta struct {
	Absolute float64 `json:"absolute"`
	Relative float64 `json:"relative"`
}

// NewAggregate computes an aggregate from raw results.
func NewAggregate(mode string, results []RunResult) Aggregate {
	agg := Aggregate{Mode: mode, Runs: len(results)}
	if len(results) == 0 {
		return agg
	}
	var score float64
	var visible, initial, calls, llm, activations, activatedTools, necessaryActivations, unnecessaryActivations, wrong, invalid, frameworkCalls, skillLoads, requestVisible, recall, precision, collateral float64
	var requestCount int
	totalTokens := make([]int, 0, len(results))
	promptTokens := make([]int, 0, len(results))
	durations := make([]int64, 0, len(results))
	ttftValues := make([]int64, 0)
	taskFirstTTFTValues := make([]int64, 0, len(results))
	var ttftTotal int64
	var taskFirstTTFTTotal int64
	observedPassed := 0
	for _, result := range results {
		if result.Passed {
			observedPassed++
		}
		qualitySample := strings.TrimSpace(result.Error) == ""
		if qualitySample {
			agg.EvaluatedRuns++
			if result.Passed {
				agg.Passed++
			}
			score += result.Score
			recall += result.ToolRecall
			precision += result.ToolPrecision
			collateral += float64(result.CollateralCount)
		} else {
			agg.ErrorRuns++
		}
		if result.DurationNanos > 0 {
			agg.DurationSamples++
			agg.TotalDurationNanos += result.DurationNanos
			durations = append(durations, result.DurationNanos)
		}
		agg.Usage.Add(result.Usage)
		visible += float64(result.PeakVisibleTools)
		initial += float64(result.InitialToolCount)
		calls += float64(result.ToolCallCount)
		llm += float64(result.LLMCalls)
		activations += float64(result.ActivationCount)
		for _, activation := range result.Activations {
			activatedTools += float64(activation.NewToolCount)
			if activation.Necessary {
				necessaryActivations++
			} else {
				unnecessaryActivations++
			}
		}
		wrong += float64(result.WrongToolCalls)
		invalid += float64(result.InvalidToolCalls)
		frameworkCalls += float64(result.FrameworkCallCount)
		skillLoads += float64(result.SkillLoadCount)
		var firstTaskTTFT int64
		for _, request := range result.Requests {
			requestCount++
			switch request.UsageSource {
			case UsageSourceReported:
				agg.UsageReportedRequests++
			case UsageSourceSynthetic:
				agg.UsageSyntheticRequests++
			case UsageSourceEstimated:
				agg.UsageEstimatedRequests++
			case UsageSourceMissing:
				agg.UsageMissingRequests++
			default:
				// Preserve useful counts for callers that construct raw records
				// directly instead of using Recorder.
				if hasUsage(request.Usage) {
					agg.UsageReportedRequests++
				} else {
					agg.UsageMissingRequests++
				}
			}
			if firstTaskTTFT == 0 && request.TTFTNanos > 0 {
				firstTaskTTFT = request.TTFTNanos
			}
			if request.TTFTNanos > 0 {
				ttftValues = append(ttftValues, request.TTFTNanos)
				ttftTotal += request.TTFTNanos
			}
			requestVisible += float64(request.VisibleToolCount)
			prompt := requestPromptTokens(request)
			if prompt > agg.MaxRequestPromptTokens {
				agg.MaxRequestPromptTokens = prompt
			}
			if request.VisibleToolCount > agg.MaxRequestVisibleTools {
				agg.MaxRequestVisibleTools = request.VisibleToolCount
			}
		}
		if firstTaskTTFT > 0 {
			taskFirstTTFTValues = append(taskFirstTTFTValues, firstTaskTTFT)
			taskFirstTTFTTotal += firstTaskTTFT
		}
		usage := result.Usage
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
		totalTokens = append(totalTokens, usage.TotalTokens)
		promptTokens = append(promptTokens, resultPromptTokens(result))
	}
	if agg.Runs > 0 {
		agg.ObservedPassRate = float64(observedPassed) / float64(agg.Runs)
	}
	if agg.EvaluatedRuns > 0 {
		agg.PassRate = float64(agg.Passed) / float64(agg.EvaluatedRuns)
		agg.AverageScore = score / float64(agg.EvaluatedRuns)
		agg.AverageToolRecall = recall / float64(agg.EvaluatedRuns)
		agg.AverageToolPrecision = precision / float64(agg.EvaluatedRuns)
		agg.AverageCollateralCount = collateral / float64(agg.EvaluatedRuns)
	}
	agg.AverageVisibleTools = visible / float64(agg.Runs)
	agg.AverageInitialTools = initial / float64(agg.Runs)
	agg.AverageToolCalls = calls / float64(agg.Runs)
	agg.AverageLLMCalls = llm / float64(agg.Runs)
	agg.AverageActivations = activations / float64(agg.Runs)
	agg.AverageActivatedTools = activatedTools / float64(agg.Runs)
	agg.AverageNecessaryActivations = necessaryActivations / float64(agg.Runs)
	agg.AverageUnnecessaryActivations = unnecessaryActivations / float64(agg.Runs)
	agg.AverageWrongToolCalls = wrong / float64(agg.Runs)
	agg.AverageInvalidToolCalls = invalid / float64(agg.Runs)
	agg.AverageFrameworkCalls = frameworkCalls / float64(agg.Runs)
	agg.AverageSkillLoads = skillLoads / float64(agg.Runs)
	if agg.DurationSamples > 0 {
		agg.AverageDurationNanos = float64(agg.TotalDurationNanos) / float64(agg.DurationSamples)
	}
	agg.AveragePeakVisibleTools = visible / float64(agg.Runs)
	if requestCount > 0 {
		agg.AverageRequestVisibleTools = requestVisible / float64(requestCount)
	}
	agg.UsageSamples = requestCount
	agg.UsageKnownSamples = agg.UsageReportedRequests +
		agg.UsageSyntheticRequests + agg.UsageEstimatedRequests
	if agg.UsageSamples > 0 {
		agg.UsageCoverage = float64(agg.UsageKnownSamples) / float64(agg.UsageSamples)
		agg.UsageComplete = agg.UsageKnownSamples == agg.UsageSamples
	}
	agg.AverageUsage = agg.Usage
	agg.AverageTotalTokens = float64(agg.Usage.TotalTokens) / float64(agg.Runs)
	agg.AverageUsage.PromptTokens /= agg.Runs
	agg.AverageUsage.CompletionTokens /= agg.Runs
	agg.AverageUsage.TotalTokens /= agg.Runs
	agg.AverageUsage.CachedPromptTokens /= agg.Runs
	agg.AverageUsage.CacheCreationTokens /= agg.Runs
	agg.AverageUsage.CacheReadTokens /= agg.Runs
	agg.AverageUsage.ReasoningTokens /= agg.Runs
	if agg.Passed > 0 {
		agg.TokensPerSuccess = float64(agg.Usage.TotalTokens) / float64(agg.Passed)
		uncached := agg.Usage.UncachedPromptTokens() + agg.Usage.CompletionTokens
		agg.UncachedTokensPerSuccess = float64(uncached) / float64(agg.Passed)
	}
	agg.P50TotalTokens = percentile(totalTokens, 0.50)
	agg.P95TotalTokens = percentile(totalTokens, 0.95)
	agg.P50PromptTokens = percentile(promptTokens, 0.50)
	agg.P95PromptTokens = percentile(promptTokens, 0.95)
	agg.P50DurationNanos = percentileNanos(durations, 0.50)
	agg.P95DurationNanos = percentileNanos(durations, 0.95)
	if len(durations) > 0 {
		for _, duration := range durations {
			if duration > agg.MaxDurationNanos {
				agg.MaxDurationNanos = duration
			}
		}
	}
	agg.TTFTSamples = len(ttftValues)
	agg.TTFTRequestCount = requestCount
	if requestCount > agg.TTFTSamples {
		agg.TTFTMissingCount = requestCount - agg.TTFTSamples
	}
	if agg.TTFTSamples > 0 {
		agg.AverageTTFTNanos = float64(ttftTotal) / float64(agg.TTFTSamples)
		agg.P50TTFTNanos = percentileNanos(ttftValues, 0.50)
		agg.P95TTFTNanos = percentileNanos(ttftValues, 0.95)
		for _, ttft := range ttftValues {
			if ttft > agg.MaxTTFTNanos {
				agg.MaxTTFTNanos = ttft
			}
		}
	}
	agg.TaskFirstTTFTSamples = len(taskFirstTTFTValues)
	if agg.TaskFirstTTFTSamples > 0 {
		agg.AverageTaskFirstTTFTNanos = float64(taskFirstTTFTTotal) / float64(agg.TaskFirstTTFTSamples)
		agg.P50TaskFirstTTFTNanos = percentileNanos(taskFirstTTFTValues, 0.50)
		agg.P95TaskFirstTTFTNanos = percentileNanos(taskFirstTTFTValues, 0.95)
		for _, ttft := range taskFirstTTFTValues {
			if ttft > agg.MaxTaskFirstTTFTNanos {
				agg.MaxTaskFirstTTFTNanos = ttft
			}
		}
	}
	return agg
}

// resultPromptTokens returns the best available provider prompt-token value
// for percentile reporting. Provider adapters may expose only total usage;
// deriving a prompt component from total-completion is retained as a clearly
// documented diagnostic fallback, never as a tokenizer estimate.
func resultPromptTokens(result RunResult) int {
	if result.Usage.PromptTokens > 0 {
		return result.Usage.PromptTokens
	}
	if result.Usage.TotalTokens > 0 {
		if result.Usage.CompletionTokens > 0 && result.Usage.TotalTokens >= result.Usage.CompletionTokens {
			return result.Usage.TotalTokens - result.Usage.CompletionTokens
		}
		return result.Usage.TotalTokens
	}
	total := 0
	for _, request := range result.Requests {
		total += requestPromptTokens(request)
	}
	return total
}

// requestPromptTokens returns the best prompt-token value available for one
// request. Keeping this fallback in one helper prevents max/percentile
// summaries from using different meanings for total-only provider usage.
func requestPromptTokens(request RequestRecord) int {
	if request.Usage.PromptTokens > 0 {
		return request.Usage.PromptTokens
	}
	if request.Usage.TotalTokens > 0 {
		if request.Usage.CompletionTokens > 0 && request.Usage.TotalTokens >= request.Usage.CompletionTokens {
			return request.Usage.TotalTokens - request.Usage.CompletionTokens
		}
		return request.Usage.TotalTokens
	}
	return 0
}

// NewComparison computes mode aggregates and token/quality deltas.
func NewComparison(benchmark string, static, dynamic []RunResult) Comparison {
	staticAgg := NewAggregate("static-all", static)
	dynamicAgg := NewAggregate("dynamic-activation", dynamic)
	staticTokens := float64(staticAgg.Usage.TotalTokens)
	dynamicTokens := float64(dynamicAgg.Usage.TotalTokens)
	staticAverageTokens := staticAgg.AverageTotalTokens
	dynamicAverageTokens := dynamicAgg.AverageTotalTokens
	passRateDelta := delta(staticAgg.PassRate, dynamicAgg.PassRate)
	return Comparison{
		Benchmark:   benchmark,
		GeneratedAt: time.Now().UTC(),
		Static:      staticAgg,
		Dynamic:     dynamicAgg,
		TokenDelta: Delta{
			Absolute: dynamicTokens - staticTokens,
			Relative: relativeDelta(staticTokens, dynamicTokens),
		},
		TokenDeltaComparable: staticAgg.UsageComplete && dynamicAgg.UsageComplete &&
			staticAgg.ErrorRuns == 0 && dynamicAgg.ErrorRuns == 0,
		AverageTokenDelta:    delta(staticAverageTokens, dynamicAverageTokens),
		QualityDelta:         passRateDelta,
		PassRateDelta:        passRateDelta,
		ScoreDelta:           delta(staticAgg.AverageScore, dynamicAgg.AverageScore),
		ToolRecallDelta:      delta(staticAgg.AverageToolRecall, dynamicAgg.AverageToolRecall),
		ToolPrecisionDelta:   delta(staticAgg.AverageToolPrecision, dynamicAgg.AverageToolPrecision),
		WrongToolCallDelta:   delta(staticAgg.AverageWrongToolCalls, dynamicAgg.AverageWrongToolCalls),
		InvalidToolCallDelta: delta(staticAgg.AverageInvalidToolCalls, dynamicAgg.AverageInvalidToolCalls),
		CollateralDelta:      delta(staticAgg.AverageCollateralCount, dynamicAgg.AverageCollateralCount),
		QualityDeltaComparable: staticAgg.ErrorRuns == 0 && dynamicAgg.ErrorRuns == 0 &&
			staticAgg.EvaluatedRuns > 0 && dynamicAgg.EvaluatedRuns > 0,
		StaticRuns:  append([]RunResult(nil), static...),
		DynamicRuns: append([]RunResult(nil), dynamic...),
	}
}

func delta(base, value float64) Delta {
	return Delta{Absolute: value - base, Relative: relativeDelta(base, value)}
}

func relativeDelta(base, value float64) float64 {
	if base == 0 {
		return 0
	}
	return (value - base) / base
}

func percentile(values []int, p float64) int {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int(nil), values...)
	sort.Ints(copyValues)
	if p <= 0 {
		return copyValues[0]
	}
	if p >= 1 {
		return copyValues[len(copyValues)-1]
	}
	index := int(math.Ceil(p*float64(len(copyValues)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copyValues) {
		index = len(copyValues) - 1
	}
	return copyValues[index]
}

// percentileNanos is the int64 counterpart of percentile. Durations are
// retained as nanoseconds in raw records, so converting them to int could
// overflow on long-running tasks or platforms where int is 32-bit.
func percentileNanos(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	if p <= 0 {
		return copyValues[0]
	}
	if p >= 1 {
		return copyValues[len(copyValues)-1]
	}
	index := int(math.Ceil(p*float64(len(copyValues)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copyValues) {
		index = len(copyValues) - 1
	}
	return copyValues[index]
}

// Recorder is a small request observer around a framework model. The
// framework already owns model invocation, usage propagation, and telemetry;
// this observer only keeps the per-request raw values needed to pair static
// and dynamic runs (visible tool menu, adapter TTFT, and response calls).
// It is safe for concurrent model calls, although the default LLMAgent flow is
// sequential.
type Recorder struct {
	base model.Model
	// UsageSourceOverride labels model-supplied usage when a caller explicitly
	// knows it is synthetic or estimated. Leave empty for a real provider,
	// which is then labeled reported or missing.
	UsageSourceOverride string
	mu                  sync.Mutex
	Requests            []RequestRecord
	nextIndex           int
}

// NewRecorder returns a model observer that reads model.Response.Usage.
// Missing usage stays missing; tokenization belongs to the provider adapter,
// not to this benchmark observer.
func NewRecorder(base model.Model) *Recorder {
	return &Recorder{base: base}
}

// responseCapture owns mutable state for one model request until its stream or
// iterator is fully consumed.
type responseCapture struct {
	record            RequestRecord
	start             time.Time
	ttftRecorded      bool
	usageObservations []usageObservation
	toolCallSnapshots []toolCallObservation
}

type usageObservation struct {
	usage    TokenUsage
	terminal bool
}

type capturedToolCall struct {
	id        string
	name      string
	arguments string
	// Choice and Ordinal provide a stable fallback identity when a provider
	// omits the optional tool-call ID. Index is the framework's public
	// model.ToolCall.Index and is preferred when present for streaming chunks.
	Choice   int
	Ordinal  int
	Index    int
	HasIndex bool
}

type toolCallObservation struct {
	calls    []capturedToolCall
	terminal bool
}

func (r *Recorder) newCapture(request *model.Request) *responseCapture {
	capture := &responseCapture{}
	capture.record = requestRecord(request)
	capture.record.Index = r.reserveIndex()
	// Start the adapter-visible latency clock only after recorder metadata has
	// been collected. requestRecord marshals messages and tool declarations for
	// byte-size diagnostics; including that work would make TTFT depend on the
	// benchmark's instrumentation cost (and especially bias large menus).
	capture.start = time.Now()
	return capture
}

func (c *responseCapture) observe(response *model.Response) {
	if c == nil {
		return
	}
	c.record.ResponseCount++
	if response == nil {
		return
	}
	// IsValidContent is the framework's canonical meaningful-response test and
	// avoids counting metadata-only chunks as TTFT.
	if !c.ttftRecorded && response.IsValidContent() {
		c.record.TTFTNanos = time.Since(c.start).Nanoseconds()
		c.ttftRecorded = true
	}
	if response.Error != nil && c.record.Error == "" {
		c.record.Error = responseErrorString(response.Error)
	}
	if response.Usage != nil {
		if usage := usageFromResponse(response); hasUsage(usage) {
			c.usageObservations = append(c.usageObservations, usageObservation{
				usage: usage, terminal: !response.IsPartial,
			})
		}
	}
	if calls := responseToolCalls(response); len(calls) > 0 {
		c.toolCallSnapshots = append(c.toolCallSnapshots, toolCallObservation{
			calls: calls, terminal: !response.IsPartial,
		})
	}
}

func (c *responseCapture) finish(r *Recorder, returnedErr error) RequestRecord {
	if c == nil {
		return RequestRecord{}
	}
	if returnedErr != nil && c.record.Error == "" {
		c.record.Error = returnedErr.Error()
	}
	c.record.Usage = selectObservedUsage(c.usageObservations)
	c.record.ToolCallsReturned, c.record.ToolCallIDs, c.record.ToolCallArguments = flattenToolCalls(c.toolCallSnapshots)
	if hasUsage(c.record.Usage) {
		c.record.UsageSource = UsageSourceReported
		if r != nil && strings.TrimSpace(r.UsageSourceOverride) != "" {
			c.record.UsageSource = strings.TrimSpace(r.UsageSourceOverride)
		}
	} else {
		c.record.UsageSource = UsageSourceMissing
	}
	c.record.DurationNanos = time.Since(c.start).Nanoseconds()
	return c.record
}

// GenerateContent implements model.Model. The forwarding path remains for
// callers that use the channel API; framework flows prefer GenerateContentIter
// below when the wrapped model supports it.
func (r *Recorder) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	if r == nil || r.base == nil {
		return nil, fmt.Errorf("metrics recorder requires a model")
	}
	if ctx == nil {
		return nil, fmt.Errorf("metrics recorder requires a context")
	}
	capture := r.newCapture(request)
	// Keep a private cancellation handle so a framework consumer that stops a
	// stream early (for example after a terminal response error) can stop the
	// provider too. The public Model contract does not expose a Close method;
	// context cancellation is the portable cleanup mechanism.
	sourceCtx, cancel := context.WithCancel(ctx)
	responseCh, err := r.base.GenerateContent(sourceCtx, request)
	if err != nil {
		cancel()
		r.appendRequest(capture.finish(r, err))
		return nil, err
	}
	if responseCh == nil {
		cancel()
		err = fmt.Errorf("model returned a nil response channel")
		r.appendRequest(capture.finish(r, err))
		return nil, err
	}
	out := make(chan *model.Response)
	go func() {
		defer close(out)
		defer cancel()
		defer func() { r.appendRequest(capture.finish(r, nil)) }()
		forward := true
		for response := range responseCh {
			capture.observe(response)
			if !forward {
				continue
			}
			// If the downstream flow cancels, stop forwarding but keep draining
			// the provider channel so a well-behaved adapter can terminate cleanly.
			select {
			case out <- response:
			case <-ctx.Done():
				capture.record.Error = ctx.Err().Error()
				cancel()
				forward = false
			}
		}
	}()
	return out, nil
}

// GenerateContentIter preserves the framework's optional IterModel fast path.
// When the wrapped model only implements Model, its response channel is
// adapted to a sequence while retaining the same observation semantics.
func (r *Recorder) GenerateContentIter(ctx context.Context, request *model.Request) (model.Seq[*model.Response], error) {
	if r == nil || r.base == nil {
		return nil, fmt.Errorf("metrics recorder requires a model")
	}
	if ctx == nil {
		return nil, fmt.Errorf("metrics recorder requires a context")
	}
	capture := r.newCapture(request)
	sourceCtx, cancel := context.WithCancel(ctx)
	var seq model.Seq[*model.Response]
	var err error
	observeInSource := false
	if iterModel, ok := r.base.(model.IterModel); ok {
		seq, err = iterModel.GenerateContentIter(sourceCtx, request)
		if err != nil {
			cancel()
			r.appendRequest(capture.finish(r, err))
			return nil, err
		}
		if seq == nil {
			cancel()
			err = fmt.Errorf("model returned a nil response sequence")
			r.appendRequest(capture.finish(r, err))
			return nil, err
		}
	} else {
		responseCh, err := r.base.GenerateContent(sourceCtx, request)
		if err != nil {
			cancel()
			r.appendRequest(capture.finish(r, err))
			return nil, err
		}
		if responseCh == nil {
			cancel()
			err = fmt.Errorf("model returned a nil response channel")
			r.appendRequest(capture.finish(r, err))
			return nil, err
		}
		observeInSource = true
		seq = func(yield func(*model.Response) bool) {
			for response := range responseCh {
				capture.observe(response)
				if !yield(response) {
					// A callback may stop early after an error. Cancel the source and
					// drain it synchronously so any final usage/call snapshot is
					// observed before the request is committed. Framework model
					// adapters are required to honor context cancellation.
					cancel()
					for response := range responseCh {
						capture.observe(response)
					}
					return
				}
			}
		}
	}
	return func(yield func(*model.Response) bool) {
		defer cancel()
		defer func() { r.appendRequest(capture.finish(r, nil)) }()
		if observeInSource {
			seq(yield)
			return
		}
		seq(func(response *model.Response) bool {
			capture.observe(response)
			keepGoing := yield(response)
			if !keepGoing {
				cancel()
			}
			return keepGoing
		})
	}, nil
}

// hasUsage reports whether a response supplied any non-zero usage field. Some
// streaming APIs send an empty usage object on partial chunks; those do not
// constitute a usable provider snapshot.
func hasUsage(usage TokenUsage) bool {
	return usage.PromptTokens != 0 ||
		usage.CompletionTokens != 0 ||
		usage.TotalTokens != 0 ||
		usage.CachedPromptTokens != 0 ||
		usage.CacheCreationTokens != 0 ||
		usage.CacheReadTokens != 0 ||
		usage.ReasoningTokens != 0
}

// selectObservedUsage chooses one usage snapshot for a single model request.
// A non-partial response is the framework's terminal snapshot for that
// request, so it wins even if an adapter emitted a larger partial value. If no
// terminal snapshot carries usage, the latest observed value is retained. The
// provider adapter remains responsible for aggregating incremental usage
// fields before returning them; this observer never adds stream chunks.
func selectObservedUsage(observations []usageObservation) TokenUsage {
	if len(observations) == 0 {
		return TokenUsage{}
	}
	for index := len(observations) - 1; index >= 0; index-- {
		if observations[index].terminal {
			selected := observations[index].usage
			if selected.TotalTokens == 0 {
				selected.TotalTokens = selected.PromptTokens + selected.CompletionTokens
			}
			return selected
		}
	}
	selected := observations[len(observations)-1].usage
	if selected.TotalTokens == 0 {
		selected.TotalTokens = selected.PromptTokens + selected.CompletionTokens
	}
	return selected
}

// responseErrorString keeps the provider's type when it is available while
// preserving the human-readable message used by model.ResponseError.Error.
func responseErrorString(err *model.ResponseError) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Message)
	typ := strings.TrimSpace(err.Type)
	if typ == "" {
		return message
	}
	if message == "" {
		return typ
	}
	return typ + ": " + message
}

func responseToolCalls(response *model.Response) []capturedToolCall {
	if response == nil {
		return nil
	}
	result := make([]capturedToolCall, 0)
	for choiceIndex, choice := range response.Choices {
		appendToolCalls := func(calls []model.ToolCall) {
			for ordinal, call := range calls {
				name := strings.TrimSpace(call.Function.Name)
				id := strings.TrimSpace(call.ID)
				arguments := string(call.Function.Arguments)
				// A later streaming chunk may carry only an argument fragment;
				// retain it so flattenToolCalls can merge it with the first chunk.
				if name == "" && id == "" && arguments == "" && call.Index == nil {
					continue
				}
				captured := capturedToolCall{
					id:        id,
					name:      name,
					arguments: arguments,
					Choice:    choiceIndex,
					Ordinal:   ordinal,
				}
				if call.Index != nil {
					captured.Index = *call.Index
					captured.HasIndex = true
				}
				result = append(result, captured)
			}
		}
		// Providers place calls in Message for non-streaming responses and in
		// Delta for streaming responses. They are alternative representations;
		// preferring Message when both are populated avoids counting one call
		// twice in an adapter that exposes a cumulative terminal message.
		if len(choice.Message.ToolCalls) > 0 {
			appendToolCalls(choice.Message.ToolCalls)
		} else {
			appendToolCalls(choice.Delta.ToolCalls)
		}
	}
	return result
}

func flattenToolCalls(observations []toolCallObservation) (names, ids, arguments []string) {
	// A terminal response is the adapter's authoritative representation of the
	// tool calls that the framework can execute. Some streaming providers emit
	// partial deltas for several calls (or reuse an ID) and then finish with a
	// complete Message.ToolCalls snapshot. Merging those unrelated partials
	// into the terminal call would fabricate arguments such as `{}{}` and
	// corrupt activation/tool-call accounting. Keep partial observations only
	// when the terminal response has no tool calls (for adapters that terminate
	// with text after exposing a call in a delta).
	for _, observation := range observations {
		if observation.terminal && terminalToolCallsUsable(observation.calls) {
			return flattenToolCallObservations([]toolCallObservation{observation})
		}
	}
	return flattenToolCallObservations(observations)
}

func terminalToolCallsUsable(calls []capturedToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if strings.TrimSpace(call.name) == "" || strings.TrimSpace(call.arguments) == "" {
			return false
		}
	}
	return true
}

func flattenToolCallObservations(observations []toolCallObservation) (names, ids, arguments []string) {
	// Merge observations when there is no usable terminal snapshot. Some
	// adapters put the tool call in an early delta and finish the stream with a
	// text-only terminal response; retaining the partial call is necessary for
	// an accurate attempt audit in that case.
	type idKey struct {
		choice int
		id     string
	}
	type indexKey struct {
		choice int
		index  int
	}
	type positionKey struct {
		choice  int
		ordinal int
	}
	byID := make(map[idKey]int)
	byIndex := make(map[indexKey]int)
	byPosition := make(map[positionKey]int)
	for _, observation := range observations {
		for _, call := range observation.calls {
			position := positionKey{choice: call.Choice, ordinal: call.Ordinal}
			var existing int
			var found bool
			if call.id != "" {
				existing, found = byID[idKey{choice: call.Choice, id: call.id}]
			}
			if !found && call.HasIndex {
				existing, found = byIndex[indexKey{choice: call.Choice, index: call.Index}]
			}
			if !found {
				existing, found = byPosition[position]
			}
			if !found {
				existing = len(names)
				names = append(names, call.name)
				ids = append(ids, call.id)
				arguments = append(arguments, call.arguments)
			} else {
				// Streaming deltas often provide the name first and arguments
				// later. Keep the newest non-empty fields and join argument
				// fragments without duplicating cumulative JSON snapshots.
				if call.name != "" {
					names[existing] = call.name
				}
				if call.id != "" {
					ids[existing] = call.id
				}
				arguments[existing] = mergeToolCallArguments(arguments[existing], call.arguments)
			}
			byPosition[position] = existing
			if call.id != "" {
				byID[idKey{choice: call.Choice, id: call.id}] = existing
			}
			if call.HasIndex {
				byIndex[indexKey{choice: call.Choice, index: call.Index}] = existing
			}
		}
	}
	return names, ids, arguments
}

// mergeToolCallArguments handles both provider styles: cumulative snapshots
// and raw streaming fragments. If both values are complete JSON documents,
// the newer snapshot wins; otherwise fragments are concatenated in order.
func mergeToolCallArguments(existing, incoming string) string {
	if incoming == "" {
		return existing
	}
	if existing == "" || existing == incoming {
		return incoming
	}
	if strings.HasPrefix(incoming, existing) {
		return incoming
	}
	if strings.HasPrefix(existing, incoming) {
		return existing
	}
	if json.Valid([]byte(existing)) && json.Valid([]byte(incoming)) {
		return incoming
	}
	return existing + incoming
}

// Info implements model.Model.
func (r *Recorder) Info() model.Info {
	if r == nil || r.base == nil {
		return model.Info{Name: "activationbench-recorder"}
	}
	return r.base.Info()
}

// Snapshot returns a copy of all request records.
func (r *Recorder) Snapshot() []RequestRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]RequestRecord, len(r.Requests))
	copy(result, r.Requests)
	sort.SliceStable(result, func(i, j int) bool { return result[i].Index < result[j].Index })
	for i := range result {
		result[i].VisibleTools = append([]string(nil), result[i].VisibleTools...)
		result[i].ToolCallsReturned = append([]string(nil), result[i].ToolCallsReturned...)
		result[i].ToolCallIDs = append([]string(nil), result[i].ToolCallIDs...)
		result[i].ToolCallArguments = append([]string(nil), result[i].ToolCallArguments...)
	}
	return result
}

func (r *Recorder) appendRequest(record RequestRecord) {
	r.mu.Lock()
	r.Requests = append(r.Requests, record)
	r.mu.Unlock()
}

func (r *Recorder) reserveIndex() int {
	r.mu.Lock()
	index := r.nextIndex
	r.nextIndex++
	r.mu.Unlock()
	return index
}

func requestRecord(request *model.Request) RequestRecord {
	record := RequestRecord{}
	if request == nil {
		return record
	}
	record.Streaming = request.GenerationConfig.Stream
	record.VisibleTools = visibleToolNames(request.Tools)
	record.VisibleToolCount = len(record.VisibleTools)
	for _, message := range request.Messages {
		// Marshal the complete message rather than only Content so byte-size
		// diagnostics include tool-call arguments, tool-result metadata, and
		// multimodal parts without pretending they are provider token counts.
		if encoded, err := json.Marshal(message); err == nil {
			record.MessageBytes += len(encoded)
		}
	}
	for _, name := range record.VisibleTools {
		if tl := request.Tools[name]; tl != nil {
			if declaration := tl.Declaration(); declaration != nil {
				if encoded, err := json.Marshal(declaration); err == nil {
					record.ToolSchemaBytes += len(encoded)
				}
			}
		}
	}
	return record
}

func visibleToolNames(tools map[string]tool.Tool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func usageFromResponse(response *model.Response) TokenUsage {
	if response == nil || response.Usage == nil {
		return TokenUsage{}
	}
	return TokenUsage{
		PromptTokens:        response.Usage.PromptTokens,
		CompletionTokens:    response.Usage.CompletionTokens,
		TotalTokens:         response.Usage.TotalTokens,
		CachedPromptTokens:  response.Usage.PromptTokensDetails.CachedTokens,
		CacheCreationTokens: response.Usage.PromptTokensDetails.CacheCreationTokens,
		CacheReadTokens:     response.Usage.PromptTokensDetails.CacheReadTokens,
		ReasoningTokens:     response.Usage.CompletionTokensDetails.ReasoningTokens,
	}
}
