//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

const (
	lmeDatasetFormat                 = "longmemeval"
	lmeAppAuto                       = "memory-lme-auto"
	lmeAppMem0                       = "memory-lme-mem0"
	lmeSeedAgentName, lmeQAAgentName = "memory-lme-seed", "memory-lme-agent"
	lmeDefaultAnswerMaxTokens        = 500
	lmeDefaultJudgeMaxTokens         = 10240
	lmeDefaultMaxRetries             = 3
	lmeDefaultBuildMaxTokens         = 7500
	lmeBuildProtocol                 = "turn-pair"
	lmeBuildRunnerLifecycle          = "one-runner-per-case"
	lmeBuildSessionLifecycle         = "source-session-continuous"
	lmeTemporalReferenceSource       = "build_plan_session_observation_time"
	lmeTemporalReferenceFormat       = "YYYY-MM-DD"
	lmeAutoTemporalContext           = "extractor_reference_date"
	lmeMem0TemporalContext           = "custom_prompt_reference_date"
	lmeRetrievalTopK                 = 20
	lmeMemoryReadLimit               = 10000
)

type lmeConversationExtraction string

const (
	lmeConversationExtractionDisabled         lmeConversationExtraction = "disabled"
	lmeConversationExtractionAssistantEpisode lmeConversationExtraction = "assistant-episode"
)

func parseLMEConversationExtraction(raw string) (lmeConversationExtraction, error) {
	mode := lmeConversationExtraction(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		return lmeConversationExtractionDisabled, nil
	}
	switch mode {
	case lmeConversationExtractionDisabled,
		lmeConversationExtractionAssistantEpisode:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"invalid LongMemEval conversation extraction %q: expected disabled or assistant-episode",
			raw,
		)
	}
}

// Config contains all LongMemEval CLI/runtime settings.
type Config struct {
	ModelName              string
	Model                  model.Model
	OutputDir              string
	Scenario               string
	DatasetPath            string
	ManifestPath           string
	ReplayRoot             string
	BuildMaxTokens         int
	BuildTokenizerModel    string
	BuildTokenizerEncoding string
	QuestionTypes          []string
	MaxTasks               int
	MaxRetries             int
	AnswerMaxTokens        int
	JudgeMaxTokens         int
	AutoExtractionWait     time.Duration
	AutoQAOnly             bool
	AutoMemoryTable        string
	AutoUpdatePolicy       string
	ConversationExtraction string
	EmbeddingCacheEnabled  bool
	EmbeddingCachePath     string
	EmbedModelName         string
	PGVectorDSN            string
	LLMBaseURL             string
	EmbeddingAPIKey        string
	EmbeddingBaseURL       string
	TableSuffix            string
	Resume                 bool
	Mem0Host               string
	Mem0APIKey             string
	Mem0Version            string
	Mem0Revision           string
	Mem0PreflightPath      string
	Mem0IngestTimeout      time.Duration
	Mem0ProxyUsageLog      string
	Mem0ProxyRunID         string
	TraceContentMode       string
	TraceGzip              bool
}

type lmeRunConfig struct {
	ModelName                     string                 `json:"model_name"`
	EmbedModelName                string                 `json:"embed_model_name,omitempty"`
	LLMEndpointFingerprint        string                 `json:"llm_endpoint_fingerprint,omitempty"`
	EmbeddingEndpointFingerprint  string                 `json:"embedding_endpoint_fingerprint,omitempty"`
	DatasetPath                   string                 `json:"dataset_path"`
	ManifestPath                  string                 `json:"manifest_path,omitempty"`
	ReplayRoot                    string                 `json:"replay_root,omitempty"`
	BuildPlanRoot                 string                 `json:"build_plan_root,omitempty"`
	ReplayDigest                  string                 `json:"replay_digest,omitempty"`
	BuildPlanDigest               string                 `json:"build_plan_digest,omitempty"`
	BuildMaxTokens                int                    `json:"build_max_tokens"`
	BuildTokenizer                string                 `json:"build_tokenizer,omitempty"`
	BuildTokenizerModel           string                 `json:"build_tokenizer_model,omitempty"`
	BuildTokenizerEncoding        string                 `json:"build_tokenizer_encoding,omitempty"`
	BuildStats                    lmeBuildStats          `json:"build_stats"`
	QuestionTypes                 []string               `json:"question_types,omitempty"`
	MaxTasks                      int                    `json:"max_tasks,omitempty"`
	RetrievalTopK                 int                    `json:"retrieval_top_k"`
	MaxRetries                    int                    `json:"max_retries"`
	AnswerMaxTokens               int                    `json:"answer_max_tokens"`
	JudgeMaxTokens                int                    `json:"judge_max_tokens"`
	AutoExtractionWait            time.Duration          `json:"auto_extraction_wait"`
	AutoQAOnly                    bool                   `json:"auto_qa_only,omitempty"`
	AutoMemoryTable               string                 `json:"auto_memory_table,omitempty"`
	AutoUpdatePolicy              extractor.UpdatePolicy `json:"auto_update_policy"`
	ConversationExtraction        string                 `json:"conversation_extraction"`
	EmbeddingCacheEnabled         bool                   `json:"embedding_cache_enabled,omitempty"`
	EmbeddingCachePath            string                 `json:"embedding_cache_path,omitempty"`
	TransportRetryEnabled         bool                   `json:"transport_retry_enabled"`
	TransportRetryStrategy        string                 `json:"transport_retry_strategy"`
	FullQALog                     bool                   `json:"full_qa_log,omitempty"`
	Mem0Host                      string                 `json:"mem0_host,omitempty"`
	Mem0APIKeySet                 bool                   `json:"mem0_api_key_set,omitempty"`
	Mem0Version                   string                 `json:"mem0_version,omitempty"`
	Mem0Revision                  string                 `json:"mem0_revision,omitempty"`
	Mem0PreflightPath             string                 `json:"mem0_preflight_path,omitempty"`
	Mem0PreflightDigest           string                 `json:"mem0_preflight_digest,omitempty"`
	Mem0EnvironmentLockDigest     string                 `json:"mem0_environment_lock_digest,omitempty"`
	Mem0RuntimeLLMModel           string                 `json:"mem0_runtime_llm_model,omitempty"`
	Mem0RuntimeEmbedModel         string                 `json:"mem0_runtime_embed_model,omitempty"`
	Mem0ObservationPromptVerified bool                   `json:"mem0_observation_prompt_verified,omitempty"`
	Mem0IngestTimeout             time.Duration          `json:"mem0_ingest_timeout,omitempty"`
	Mem0ProxyUsageLog             string                 `json:"mem0_proxy_usage_log,omitempty"`
	Mem0ProxyRunID                string                 `json:"mem0_proxy_run_id,omitempty"`
	TraceContentMode              lmeTraceContentMode    `json:"trace_content_mode"`
	TraceGzip                     bool                   `json:"trace_gzip,omitempty"`
}

type lmeMetadata struct {
	Framework             string                       `json:"framework"`
	Version               string                       `json:"version"`
	Timestamp             time.Time                    `json:"timestamp"`
	DatasetFormat         string                       `json:"dataset_format"`
	Scenario              string                       `json:"scenario"`
	MemoryBackend         string                       `json:"memory_backend,omitempty"`
	MemoryOnlyCompliant   bool                         `json:"memory_only_compliant,omitempty"`
	NativeMemoryPreserved bool                         `json:"native_memory_preserved,omitempty"`
	FairlyComparable      bool                         `json:"fairly_comparable"`
	ComparisonStatus      string                       `json:"comparison_status,omitempty"`
	ComparisonBlockers    []string                     `json:"comparison_blockers,omitempty"`
	ComparisonLimitations []string                     `json:"comparison_limitations,omitempty"`
	MemoryBuildMethod     string                       `json:"memory_build_method,omitempty"`
	MemoryBuild           map[string]any               `json:"memory_build,omitempty"`
	MemoryOnlyPolicy      map[string]any               `json:"memory_only_policy,omitempty"`
	MemoryOnlySummary     map[string]any               `json:"memory_only_summary,omitempty"`
	QAContextPolicy       string                       `json:"qa_context_policy,omitempty"`
	RetrievalLimits       map[string]lmeRetrievalLimit `json:"retrieval_limits,omitempty"`
	RunManifestVersion    int                          `json:"run_manifest_version,omitempty"`
	RunCompatibility      string                       `json:"run_compatibility_digest,omitempty"`
	RunComparison         string                       `json:"run_comparison_digest,omitempty"`
	OfficialStatus        string                       `json:"official_status,omitempty"`
	OfficialBlockers      []string                     `json:"official_blockers,omitempty"`
	Config                lmeRunConfig                 `json:"config"`
}

type lmeRetrievalLimit struct {
	RequestedTopK int `json:"requested_top_k"`
	EffectiveTopK int `json:"effective_top_k"`
}

type lmeRunResult struct {
	Metadata    *lmeMetadata              `json:"metadata"`
	Cost        *lmeCostReport            `json:"cost,omitempty"`
	Summary     *lmeSummary               `json:"summary"`
	ByType      map[string]*lmeTypeMetric `json:"by_type"`
	Cases       []*lmeCaseResult          `json:"cases"`
	Publication *lmePublication           `json:"publication,omitempty"`
	LastError   *lmeRunError              `json:"last_error,omitempty"`
}

type lmeSummary struct {
	TotalCases            int                   `json:"total_cases"`
	CompletedCases        int                   `json:"completed_cases"`
	SuccessfulCases       int                   `json:"successful_cases"`
	FailedCases           int                   `json:"failed_cases"`
	JudgeFailedCases      int                   `json:"judge_failed_cases"`
	Overall               metrics.AnswerMetrics `json:"overall"`
	TaskAveragedAccuracy  float64               `json:"task_averaged_accuracy"`
	AbstentionAccuracy    float64               `json:"abstention_accuracy,omitempty"`
	AbstentionCount       int                   `json:"abstention_count,omitempty"`
	NonAbstentionCount    int                   `json:"non_abstention_count,omitempty"`
	TotalTimeMs           int64                 `json:"total_time_ms"`
	AvgLatencyMs          float64               `json:"avg_latency_ms"`
	TotalPromptTokens     int                   `json:"total_prompt_tokens"`
	TotalCompletionTokens int                   `json:"total_completion_tokens"`
	TotalTokens           int                   `json:"total_tokens"`
	TotalCachedTokens     int                   `json:"total_cached_tokens,omitempty"`
	TotalLLMCalls         int                   `json:"total_llm_calls"`
	AvgPromptTokensPerQA  float64               `json:"avg_prompt_tokens_per_qa"`
	AvgCompletionPerQA    float64               `json:"avg_completion_tokens_per_qa"`
	AvgLLMCallsPerQA      float64               `json:"avg_llm_calls_per_qa"`
}

type lmeTypeMetric struct {
	Count   int                   `json:"count"`
	Metrics metrics.AnswerMetrics `json:"metrics"`
}

type lmeRunError struct {
	QuestionID string `json:"question_id,omitempty"`
	Scenario   string `json:"scenario,omitempty"`
	Message    string `json:"message"`
}

type lmeCaseResult struct {
	Status             lmeCaseStatus         `json:"status"`
	QuestionID         string                `json:"question_id"`
	QuestionType       string                `json:"question_type"`
	Question           string                `json:"question"`
	QuestionDate       string                `json:"question_date"`
	Expected           string                `json:"expected"`
	Predicted          string                `json:"predicted"`
	IsAbstention       bool                  `json:"is_abstention"`
	Correct            bool                  `json:"correct"`
	Metrics            metrics.AnswerMetrics `json:"metrics"`
	LatencyMs          int64                 `json:"latency_ms"`
	TokenUsage         *scenarios.TokenUsage `json:"token_usage,omitempty"`
	RetryCount         int                   `json:"retry_count,omitempty"`
	TotalTurns         int                   `json:"total_turns"`
	TotalSessions      int                   `json:"total_sessions"`
	ToolSteps          []lmeStepTrace        `json:"tool_steps,omitempty"`
	QATrace            []lmeMessageTrace     `json:"qa_trace,omitempty"`
	JudgeError         string                `json:"judge_error,omitempty"`
	Error              string                `json:"error,omitempty"`
	FailureStage       lmeFailureStage       `json:"failure_stage,omitempty"`
	BuildObservability lmeBuildObservability `json:"build_observability"`
	GoldSessionRecall  *float64              `json:"gold_session_recall,omitempty"`
}

type lmeStepTrace struct {
	Step             int                `json:"step"`
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	TotalTokens      int                `json:"total_tokens"`
	CachedTokens     int                `json:"cached_tokens,omitempty"`
	ToolCalls        []lmeToolCallTrace `json:"tool_calls,omitempty"`
}

type lmeToolCallTrace struct {
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
}

type lmeMessageTrace struct {
	Step      int                `json:"step,omitempty"`
	Role      string             `json:"role"`
	Name      string             `json:"name,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []lmeToolCallTrace `json:"tool_calls,omitempty"`
}

type lmeModelResult struct {
	Text       string
	Usage      *model.Usage
	RetryCount int
}

type lmeCollectResult struct {
	Text       string
	Usage      scenarios.TokenUsage
	Steps      []lmeStepTrace
	Trace      []lmeMessageTrace
	RetryCount int
}

type lmeLongContextEvaluator struct {
	llm model.Model
	cfg lmeRunConfig
}

type lmeAutoEvaluator struct {
	judgeLLM model.Model
	qaLLM    model.Model
	mem      memory.Service
	cfg      lmeRunConfig
	cost     *lmeCostTracker
	trace    *lmeTraceManager
}

type lmeMemoryQA struct {
	appName  string
	judgeLLM model.Model
	qaLLM    model.Model
	mem      memory.Service
	cfg      lmeRunConfig
}

const (
	lmeResultSchemaVersion       = 2
	lmeResultClassMaintained     = "maintained"
	lmeResultOriginNativeRunner  = "native_runner"
	lmeTraceArtifactPurpose      = "best-effort-diagnostic"
	lmeTraceArtifactComparable   = "backend-specific-not-cross-comparable"
	lmeCaseStatusPending         = lmeCaseStatus("pending")
	lmeCaseStatusSucceeded       = lmeCaseStatus("succeeded")
	lmeCaseStatusFailed          = lmeCaseStatus("failed")
	lmeCaseStatusJudgeFailed     = lmeCaseStatus("judge_failed")
	lmeCaseStatusMissing         = lmeCaseStatus("missing")
	lmeBadCasesJSONFileName      = "bad_cases.json"
	lmeBadCasesEnglishFileName   = "bad_cases.md"
	lmeBadCasesChineseFileName   = "bad_cases.zh_CN.md"
	lmeAggregateFileName         = "aggregate.json"
	lmeResultsFileName           = "results.json"
	lmeCheckpointFileName        = "checkpoint.json"
	lmeRunManifestResultFileName = "run_manifest.json"
)

type lmeCaseStatus string

type lmePublication struct {
	SchemaVersion    int                          `json:"schema_version"`
	Classification   string                       `json:"classification"`
	Origin           string                       `json:"origin"`
	Eligible         bool                         `json:"eligible"`
	Blockers         []string                     `json:"blockers,omitempty"`
	FinalizedAt      time.Time                    `json:"finalized_at,omitempty"`
	RunManifest      lmePublishedRunManifest      `json:"run_manifest"`
	FixedDenominator lmeFixedDenominator          `json:"fixed_denominator"`
	Artifacts        map[string]lmeResultArtifact `json:"artifacts,omitempty"`
}

type lmePublishedRunManifest struct {
	SchemaVersion       int    `json:"schema_version"`
	CompatibilityDigest string `json:"compatibility_digest"`
	ComparisonDigest    string `json:"comparison_digest"`
}

type lmeFixedDenominator struct {
	TotalCases int      `json:"total_cases"`
	CaseIDs    []string `json:"case_ids"`
	Digest     string   `json:"digest"`
}

type lmeResultArtifact struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Purpose       string `json:"purpose,omitempty"`
	Comparability string `json:"comparability,omitempty"`
	ContentMode   string `json:"content_mode,omitempty"`
	SelectedCases int    `json:"selected_cases,omitempty"`
}

type lmeMachineAggregate struct {
	SchemaVersion    int                       `json:"schema_version"`
	Classification   string                    `json:"classification"`
	Scenario         string                    `json:"scenario"`
	Backend          string                    `json:"backend,omitempty"`
	RunCompatibility string                    `json:"run_compatibility_digest"`
	ComparisonDigest string                    `json:"comparison_digest"`
	Denominator      lmeFixedDenominator       `json:"fixed_denominator"`
	Summary          *lmeSummary               `json:"summary"`
	ByType           map[string]*lmeTypeMetric `json:"by_type"`
	Cases            []lmeAggregateCase        `json:"cases"`
}

type lmeAggregateCase struct {
	QuestionID         string        `json:"question_id"`
	QuestionType       string        `json:"question_type"`
	Status             lmeCaseStatus `json:"status"`
	Correct            bool          `json:"correct"`
	FailureStage       string        `json:"failure_stage,omitempty"`
	BuildObservability string        `json:"build_observability"`
	Error              string        `json:"error,omitempty"`
	JudgeError         string        `json:"judge_error,omitempty"`
}

type lmeBadCaseArtifact struct {
	SchemaVersion    int                 `json:"schema_version"`
	Classification   string              `json:"classification"`
	Scenario         string              `json:"scenario"`
	Backend          string              `json:"backend,omitempty"`
	RunCompatibility string              `json:"run_compatibility_digest"`
	ComparisonDigest string              `json:"comparison_digest"`
	Denominator      lmeFixedDenominator `json:"fixed_denominator"`
	Cases            []lmeBadCase        `json:"cases"`
}

type lmeBadCase struct {
	QuestionID         string        `json:"question_id"`
	QuestionType       string        `json:"question_type"`
	Status             lmeCaseStatus `json:"status"`
	Correct            bool          `json:"correct"`
	FailureStage       string        `json:"failure_stage,omitempty"`
	BuildObservability string        `json:"build_observability"`
	Error              string        `json:"error,omitempty"`
	JudgeError         string        `json:"judge_error,omitempty"`
	Expected           string        `json:"expected,omitempty"`
	Predicted          string        `json:"predicted,omitempty"`
}

type lmeEligibilityError struct {
	Blockers []string
}

func (e *lmeEligibilityError) Error() string {
	return "LongMemEval result is not eligible for maintained publication: " +
		joinLMEBlockers(e.Blockers)
}

type lmeCaseTotals struct {
	overall            metrics.AnswerMetrics
	usage              scenarios.TokenUsage
	totalLatency       int64
	completed          int
	successful         int
	failed             int
	judgeFailed        int
	abstentionCount    int
	abstentionCorrect  int
	nonAbstentionCount int
	typeMetrics        map[string]metrics.AnswerMetrics
	typeCounts         map[string]int
	terminalTypeCounts map[string]int
}

func newLMECaseTotals() *lmeCaseTotals {
	return &lmeCaseTotals{
		typeMetrics:        make(map[string]metrics.AnswerMetrics),
		typeCounts:         make(map[string]int),
		terminalTypeCounts: make(map[string]int),
	}
}

func (t *lmeCaseTotals) add(record *lmeCaseResult) {
	if record == nil {
		return
	}
	t.typeCounts[record.QuestionType]++
	if !isLMETerminalCaseStatus(record.Status) {
		return
	}
	t.completed++
	t.addStatus(record.Status)
	t.overall.Add(record.Metrics)
	t.totalLatency += record.LatencyMs
	if record.TokenUsage != nil {
		t.usage.Add(*record.TokenUsage)
	}
	typeMetric := t.typeMetrics[record.QuestionType]
	typeMetric.Add(record.Metrics)
	t.typeMetrics[record.QuestionType] = typeMetric
	t.terminalTypeCounts[record.QuestionType]++
	if record.IsAbstention {
		t.abstentionCount++
		if record.Correct {
			t.abstentionCorrect++
		}
		return
	}
	t.nonAbstentionCount++
}

func (t *lmeCaseTotals) addStatus(status lmeCaseStatus) {
	switch status {
	case lmeCaseStatusSucceeded:
		t.successful++
	case lmeCaseStatusFailed:
		t.failed++
	case lmeCaseStatusJudgeFailed:
		t.judgeFailed++
	}
}

func (t *lmeCaseTotals) byType() map[string]*lmeTypeMetric {
	byType := make(map[string]*lmeTypeMetric, len(t.typeMetrics))
	for questionType, metric := range t.typeMetrics {
		count := t.typeCounts[questionType]
		metric.Divide(float64(max(count, 1)))
		byType[questionType] = &lmeTypeMetric{Count: count, Metrics: metric}
	}
	return byType
}

func (t *lmeCaseTotals) taskAveragedAccuracy() float64 {
	if len(t.terminalTypeCounts) == 0 {
		return 0
	}
	var total float64
	for questionType, count := range t.terminalTypeCounts {
		total += t.typeMetrics[questionType].Accuracy / float64(max(count, 1))
	}
	return total / float64(len(t.terminalTypeCounts))
}

func (t *lmeCaseTotals) summary(elapsed time.Duration, totalCases int) *lmeSummary {
	denominator := float64(max(totalCases, 1))
	overall := t.overall
	overall.Divide(denominator)
	summary := &lmeSummary{
		TotalCases:            totalCases,
		CompletedCases:        t.completed,
		SuccessfulCases:       t.successful,
		FailedCases:           t.failed,
		JudgeFailedCases:      t.judgeFailed,
		Overall:               overall,
		TaskAveragedAccuracy:  t.taskAveragedAccuracy(),
		AbstentionCount:       t.abstentionCount,
		NonAbstentionCount:    t.nonAbstentionCount,
		TotalTimeMs:           elapsed.Milliseconds(),
		AvgLatencyMs:          float64(t.totalLatency) / denominator,
		TotalPromptTokens:     t.usage.PromptTokens,
		TotalCompletionTokens: t.usage.CompletionTokens,
		TotalTokens:           t.usage.TotalTokens,
		TotalCachedTokens:     t.usage.CachedTokens,
		TotalLLMCalls:         t.usage.LLMCalls,
		AvgPromptTokensPerQA:  float64(t.usage.PromptTokens) / denominator,
		AvgCompletionPerQA:    float64(t.usage.CompletionTokens) / denominator,
		AvgLLMCallsPerQA:      float64(t.usage.LLMCalls) / denominator,
	}
	if t.abstentionCount > 0 {
		summary.AbstentionAccuracy = float64(t.abstentionCorrect) / float64(t.abstentionCount)
	}
	return summary
}

func aggregateLMERunResult(result *lmeRunResult, elapsed time.Duration, totalCases int) {
	totals := newLMECaseTotals()
	for _, record := range result.Cases {
		if record == nil {
			continue
		}
		normalizeLMECaseStatus(record)
		totals.add(record)
	}
	result.ByType = totals.byType()
	result.Summary = totals.summary(elapsed, totalCases)
}

func newLMERunResult(
	scenarioName string,
	backend string,
	cfg lmeRunConfig,
	totalCases int,
) *lmeRunResult {
	metadata := &lmeMetadata{
		Framework:     "trpc-agent-go",
		Version:       "1.0.0",
		Timestamp:     time.Now(),
		DatasetFormat: lmeDatasetFormat,
		Scenario:      scenarioName,
		MemoryBackend: backend,
		Config:        redactLMERunConfig(cfg),
	}
	if backend != "" {
		metadata.RetrievalLimits = map[string]lmeRetrievalLimit{
			backend: {
				RequestedTopK: lmeRetrievalTopK,
				EffectiveTopK: lmeRetrievalTopK,
			},
		}
	}
	return &lmeRunResult{
		Metadata: metadata,
		Summary:  &lmeSummary{TotalCases: totalCases},
		ByType:   make(map[string]*lmeTypeMetric),
		Cases:    make([]*lmeCaseResult, 0, totalCases),
	}
}

func prepareLMECaseRecords(
	result *lmeRunResult,
	instances []*dataset.LongMemEvalInstance,
) error {
	if result == nil {
		return fmt.Errorf("prepare LongMemEval cases: nil result")
	}
	expected := make(map[string]*dataset.LongMemEvalInstance, len(instances))
	order := make([]string, 0, len(instances))
	for _, instance := range instances {
		if instance == nil {
			return fmt.Errorf("prepare LongMemEval cases: nil instance")
		}
		if _, ok := expected[instance.QuestionID]; ok {
			return fmt.Errorf("duplicate selected case ID %q", instance.QuestionID)
		}
		expected[instance.QuestionID] = instance
		order = append(order, instance.QuestionID)
	}
	stored := make(map[string]*lmeCaseResult, len(result.Cases))
	for _, record := range result.Cases {
		if record == nil {
			return fmt.Errorf("checkpoint contains a nil case record")
		}
		if _, ok := expected[record.QuestionID]; !ok {
			return fmt.Errorf("checkpoint contains extra case ID %q", record.QuestionID)
		}
		if _, ok := stored[record.QuestionID]; ok {
			return fmt.Errorf("checkpoint contains duplicate case ID %q", record.QuestionID)
		}
		normalizeLMECaseStatus(record)
		stored[record.QuestionID] = record
	}
	ordered := make([]*lmeCaseResult, 0, len(order))
	for _, id := range order {
		if record := stored[id]; record != nil {
			ordered = append(ordered, record)
			continue
		}
		ordered = append(ordered, newLMEPendingCase(expected[id]))
	}
	result.Cases = ordered
	result.Publication = nil
	return nil
}

func newLMEPendingCase(instance *dataset.LongMemEvalInstance) *lmeCaseResult {
	return &lmeCaseResult{
		Status:        lmeCaseStatusPending,
		QuestionID:    instance.QuestionID,
		QuestionType:  instance.QuestionType,
		Question:      instance.Question,
		QuestionDate:  instance.QuestionDate,
		Expected:      instance.Answer,
		IsAbstention:  instance.IsAbstention(),
		TotalTurns:    instance.TotalTurns(),
		TotalSessions: len(instance.HaystackSessions),
	}
}

func newLMEFailedCase(
	instance *dataset.LongMemEvalInstance,
	err error,
) *lmeCaseResult {
	record := newLMEPendingCase(instance)
	record.Status = lmeCaseStatusFailed
	record.Error = sanitizeLMEResultText(err.Error(), 2048)
	return record
}

func replaceLMECaseRecord(result *lmeRunResult, record *lmeCaseResult) error {
	if result == nil || record == nil {
		return fmt.Errorf("replace LongMemEval case: nil result or record")
	}
	normalizeLMECaseStatus(record)
	for i, current := range result.Cases {
		if current != nil && current.QuestionID == record.QuestionID {
			result.Cases[i] = record
			return nil
		}
	}
	return fmt.Errorf("replace LongMemEval case %q: not selected", record.QuestionID)
}

func normalizeLMECaseStatus(record *lmeCaseResult) {
	if record == nil || record.Status != "" {
		return
	}
	switch {
	case strings.TrimSpace(record.Error) != "":
		record.Status = lmeCaseStatusFailed
	case strings.TrimSpace(record.JudgeError) != "":
		record.Status = lmeCaseStatusJudgeFailed
	default:
		record.Status = lmeCaseStatusSucceeded
	}
}

func isLMETerminalCaseStatus(status lmeCaseStatus) bool {
	switch status {
	case lmeCaseStatusSucceeded, lmeCaseStatusFailed, lmeCaseStatusJudgeFailed:
		return true
	default:
		return false
	}
}

func isLMECheckpointCompleted(record *lmeCaseResult) bool {
	return record != nil && record.Status == lmeCaseStatusSucceeded
}

func countLMEProcessedCases(records []*lmeCaseResult) int {
	count := 0
	for _, record := range records {
		if record != nil && isLMETerminalCaseStatus(record.Status) {
			count++
		}
	}
	return count
}

func lmeExpectedCaseIDs(instances []*dataset.LongMemEvalInstance) []string {
	ids := make([]string, 0, len(instances))
	for _, instance := range instances {
		if instance != nil {
			ids = append(ids, instance.QuestionID)
		}
	}
	return ids
}

const lmeMetricTolerance = 1e-12

func validateLMESummaryConsistency(
	result *lmeRunResult,
	expectedCaseCount int,
) []string {
	if result == nil || result.Summary == nil {
		return nil
	}
	totals := newLMECaseTotals()
	var blockers []string
	for _, record := range result.Cases {
		if record == nil || !isLMETerminalCaseStatus(record.Status) {
			continue
		}
		blockers = append(blockers, totals.validateCase(record)...)
	}
	summary := result.Summary
	blockers = append(blockers, validateLMEAnswerMetrics("summary overall", summary.Overall)...)
	blockers = append(blockers, validateLMESummaryCounts(summary, totals, expectedCaseCount)...)
	denominator := float64(max(expectedCaseCount, 1))
	totals.overall.Divide(denominator)
	blockers = append(blockers, compareLMEAnswerMetrics(
		"summary overall",
		summary.Overall,
		totals.overall,
	)...)
	blockers = append(blockers, validateLMEByTypeSummary(result.ByType, summary, totals)...)
	blockers = append(blockers, validateLMEAbstentionSummary(summary, totals)...)
	blockers = append(blockers, compareLMESummaryUsage(summary, totals.usage)...)
	wantAverageLatency := float64(totals.totalLatency) / denominator
	if !closeLMEMetric(summary.AvgLatencyMs, wantAverageLatency) {
		blockers = append(blockers, fmt.Sprintf(
			"summary avg_latency_ms is %.12g, want %.12g",
			summary.AvgLatencyMs,
			wantAverageLatency,
		))
	}
	return blockers
}

func (t *lmeCaseTotals) validateCase(record *lmeCaseResult) []string {
	questionID := record.QuestionID
	if questionID == "" {
		questionID = "<empty>"
	}
	blockers := validateLMEAnswerMetrics("case "+questionID, record.Metrics)
	if record.LatencyMs < 0 {
		blockers = append(blockers, "case "+questionID+" has negative latency")
	}
	if record.TokenUsage != nil {
		blockers = append(blockers, validateLMETokenUsage("case "+questionID, *record.TokenUsage)...)
	}
	if record.Status != lmeCaseStatusSucceeded && record.Correct {
		blockers = append(blockers, fmt.Sprintf(
			"case %s has status %s but is marked correct",
			questionID,
			record.Status,
		))
	}
	blockers = append(blockers, validateLMECaseAccuracy(questionID, record)...)
	t.add(record)
	return blockers
}

func validateLMECaseAccuracy(questionID string, record *lmeCaseResult) []string {
	want := 0.0
	if record.Correct {
		want = 1
	}
	if closeLMEMetric(record.Metrics.Accuracy, want) {
		return nil
	}
	return []string{fmt.Sprintf(
		"case %s accuracy is %.12g, want %.0f from correct",
		questionID,
		record.Metrics.Accuracy,
		want,
	)}
}

func validateLMESummaryCounts(
	summary *lmeSummary,
	totals *lmeCaseTotals,
	expected int,
) []string {
	terminal := totals.successful + totals.failed + totals.judgeFailed
	values := []struct {
		name string
		got  int
		want int
	}{
		{name: "terminal status counts total", got: terminal, want: expected},
		{name: "summary successful_cases", got: summary.SuccessfulCases, want: totals.successful},
		{name: "summary failed_cases", got: summary.FailedCases, want: totals.failed},
		{name: "summary judge_failed_cases", got: summary.JudgeFailedCases, want: totals.judgeFailed},
		{name: "summary total_cases", got: summary.TotalCases, want: expected},
		{name: "summary completed_cases", got: summary.CompletedCases, want: terminal},
	}
	var blockers []string
	if summary.TotalTimeMs < 0 {
		blockers = append(blockers, "summary total_time_ms is negative")
	}
	for _, value := range values {
		if value.got != value.want {
			blockers = append(blockers, fmt.Sprintf("%s is %d, want %d", value.name, value.got, value.want))
		}
	}
	return blockers
}

func validateLMEByTypeSummary(
	byType map[string]*lmeTypeMetric,
	summary *lmeSummary,
	totals *lmeCaseTotals,
) []string {
	types := make([]string, 0, len(totals.typeCounts))
	for questionType := range totals.typeCounts {
		types = append(types, questionType)
	}
	sort.Strings(types)
	var blockers []string
	taskAccuracy := 0.0
	for _, questionType := range types {
		metric := byType[questionType]
		if metric == nil {
			blockers = append(blockers, "summary by_type is missing "+questionType)
			continue
		}
		accuracy, metricBlockers := validateLMETypeMetric(questionType, metric, totals)
		blockers = append(blockers, metricBlockers...)
		taskAccuracy += accuracy
	}
	if len(types) > 0 {
		taskAccuracy /= float64(len(types))
	}
	if !closeLMEMetric(summary.TaskAveragedAccuracy, taskAccuracy) {
		blockers = append(blockers, fmt.Sprintf(
			"summary task_averaged_accuracy is %.12g, want %.12g",
			summary.TaskAveragedAccuracy,
			taskAccuracy,
		))
	}
	for questionType := range byType {
		if _, ok := totals.typeCounts[questionType]; !ok {
			blockers = append(blockers, "summary by_type contains extra type "+questionType)
		}
	}
	return blockers
}

func validateLMETypeMetric(
	questionType string,
	metric *lmeTypeMetric,
	totals *lmeCaseTotals,
) (float64, []string) {
	count := totals.typeCounts[questionType]
	label := "summary by_type " + questionType
	var blockers []string
	if metric.Count != count {
		blockers = append(blockers, fmt.Sprintf(
			"summary by_type %s count is %d, want %d",
			questionType,
			metric.Count,
			count,
		))
	}
	blockers = append(blockers, validateLMEAnswerMetrics(label, metric.Metrics)...)
	want := totals.typeMetrics[questionType]
	want.Divide(float64(max(count, 1)))
	blockers = append(blockers, compareLMEAnswerMetrics(label, metric.Metrics, want)...)
	return want.Accuracy, blockers
}

func validateLMEAbstentionSummary(summary *lmeSummary, totals *lmeCaseTotals) []string {
	var blockers []string
	if summary.AbstentionCount != totals.abstentionCount {
		blockers = append(blockers, fmt.Sprintf(
			"summary abstention_count is %d, want %d",
			summary.AbstentionCount,
			totals.abstentionCount,
		))
	}
	if summary.NonAbstentionCount != totals.nonAbstentionCount {
		blockers = append(blockers, fmt.Sprintf(
			"summary non_abstention_count is %d, want %d",
			summary.NonAbstentionCount,
			totals.nonAbstentionCount,
		))
	}
	wantAccuracy := 0.0
	if totals.abstentionCount > 0 {
		wantAccuracy = float64(totals.abstentionCorrect) / float64(totals.abstentionCount)
	}
	if !closeLMEMetric(summary.AbstentionAccuracy, wantAccuracy) {
		blockers = append(blockers, fmt.Sprintf(
			"summary abstention_accuracy is %.12g, want %.12g",
			summary.AbstentionAccuracy,
			wantAccuracy,
		))
	}
	return blockers
}

func validateLMEAnswerMetrics(label string, value metrics.AnswerMetrics) []string {
	values := []struct {
		name  string
		value float64
	}{
		{name: "f1", value: value.F1},
		{name: "bleu", value: value.BLEU},
		{name: "rouge_1", value: value.ROUGE1},
		{name: "rouge_2", value: value.ROUGE2},
		{name: "rouge_l", value: value.ROUGEL},
		{name: "accuracy", value: value.Accuracy},
	}
	var blockers []string
	for _, item := range values {
		if math.IsNaN(item.value) || math.IsInf(item.value, 0) ||
			item.value < 0 || item.value > 1 {
			blockers = append(blockers, fmt.Sprintf(
				"%s %s is outside [0,1]",
				label,
				item.name,
			))
		}
	}
	return blockers
}

func compareLMEAnswerMetrics(
	label string,
	got metrics.AnswerMetrics,
	want metrics.AnswerMetrics,
) []string {
	values := []struct {
		name string
		got  float64
		want float64
	}{
		{name: "f1", got: got.F1, want: want.F1},
		{name: "bleu", got: got.BLEU, want: want.BLEU},
		{name: "rouge_1", got: got.ROUGE1, want: want.ROUGE1},
		{name: "rouge_2", got: got.ROUGE2, want: want.ROUGE2},
		{name: "rouge_l", got: got.ROUGEL, want: want.ROUGEL},
		{name: "accuracy", got: got.Accuracy, want: want.Accuracy},
	}
	var blockers []string
	for _, item := range values {
		if !closeLMEMetric(item.got, item.want) {
			blockers = append(blockers, fmt.Sprintf(
				"%s %s is %.12g, want %.12g",
				label,
				item.name,
				item.got,
				item.want,
			))
		}
	}
	return blockers
}

func validateLMETokenUsage(label string, usage scenarios.TokenUsage) []string {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 ||
		usage.TotalTokens < 0 || usage.CachedTokens < 0 || usage.LLMCalls < 0 {
		return []string{label + " has negative token usage"}
	}
	return nil
}

func compareLMESummaryUsage(summary *lmeSummary, usage scenarios.TokenUsage) []string {
	values := []struct {
		name string
		got  int
		want int
	}{
		{name: "total_prompt_tokens", got: summary.TotalPromptTokens, want: usage.PromptTokens},
		{name: "total_completion_tokens", got: summary.TotalCompletionTokens, want: usage.CompletionTokens},
		{name: "total_tokens", got: summary.TotalTokens, want: usage.TotalTokens},
		{name: "total_cached_tokens", got: summary.TotalCachedTokens, want: usage.CachedTokens},
		{name: "total_llm_calls", got: summary.TotalLLMCalls, want: usage.LLMCalls},
	}
	var blockers []string
	for _, item := range values {
		if item.got != item.want {
			blockers = append(blockers, fmt.Sprintf(
				"summary %s is %d, want %d",
				item.name,
				item.got,
				item.want,
			))
		}
	}
	denominator := float64(max(summary.TotalCases, 1))
	averages := []struct {
		name string
		got  float64
		want float64
	}{
		{name: "avg_prompt_tokens_per_qa", got: summary.AvgPromptTokensPerQA, want: float64(usage.PromptTokens) / denominator},
		{name: "avg_completion_tokens_per_qa", got: summary.AvgCompletionPerQA, want: float64(usage.CompletionTokens) / denominator},
		{name: "avg_llm_calls_per_qa", got: summary.AvgLLMCallsPerQA, want: float64(usage.LLMCalls) / denominator},
	}
	for _, item := range averages {
		if !closeLMEMetric(item.got, item.want) {
			blockers = append(blockers, fmt.Sprintf(
				"summary %s is %.12g, want %.12g",
				item.name,
				item.got,
				item.want,
			))
		}
	}
	return blockers
}

func closeLMEMetric(got, want float64) bool {
	return !math.IsNaN(got) && !math.IsInf(got, 0) &&
		!math.IsNaN(want) && !math.IsInf(want, 0) &&
		math.Abs(got-want) <= lmeMetricTolerance
}
