//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package main provides memory evaluation benchmark for trpc-agent-go.
// Evaluates long-term conversational memory using the LoCoMo dataset.
//
// Evaluation Scenarios:
//   - Long-Context: Full conversation as context (baseline).
//   - Agentic: Agent with memory tools (add/update/search/load).
//   - Auto: Automatic memory extraction + search.
//
// Memory Backends:
//   - inmemory: In-memory storage (keyword-based).
//   - sqlite: SQLite storage (keyword-based).
//   - sqlitevec: SQLite + sqlite-vec (vector similarity).
//   - pgvector: PostgreSQL with vector similarity.
//   - mysql: MySQL storage (full-text search).
//
// Metrics (aligned with LoCoMo paper):
//   - F1 Score: Token-level F1.
//   - BLEU Score: N-gram overlap.
//   - LLM-score: LLM-as-Judge evaluation.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/internal/benchruntime"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/internal/longmemeval"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

// Command-line flags.
var (
	flagModel         = flag.String("model", "", "Model name (env LLM_NAME, MODEL_NAME, or gpt-4o-mini)")
	flagEvalModel     = flag.String("eval-model", "", "Evaluation model for LLM judge")
	flagDataset       = flag.String("dataset", "../data", "Dataset directory")
	flagDataFile      = flag.String("data-file", "locomo10.json", "Dataset file name")
	flagDatasetFormat = flag.String("dataset-format", "locomo", "Dataset format: locomo or longmemeval")
	flagOutput        = flag.String("output", "../results", "Output directory")

	flagScenario = flag.String(
		"scenario",
		"long_context",
		"Evaluation scenario (comma-separated): "+
			"long_context, session_recall, agentic, auto, replay, all "+
			"(LongMemEval supports long_context, replay, auto, mem0_oss, all)",
	)
	// Memory backend flags (comma-separated for multiple).
	flagMemoryBackends = flag.String(
		"memory-backend",
		"inmemory",
		"Memory backends (comma-separated): "+
			"inmemory, sqlite, sqlitevec, pgvector, mysql",
	)
	flagPGVectorDSN = flag.String(
		"pgvector-dsn",
		"",
		"PostgreSQL DSN for pgvector (env PGVECTOR_DSN)",
	)
	flagEmbedModel = flag.String(
		"embed-model",
		"",
		"Embedding model for vector backends (pgvector, sqlitevec) "+
			"(env EMBED_MODEL_NAME or text-embedding-3-small)",
	)
	flagVectorTopK = flag.Int(
		"vector-topk",
		30,
		"Top-k results for non-LongMemEval vector backends (pgvector, sqlitevec)",
	)
	flagSessionRecallMinScore = flag.Float64(
		"session-recall-min-score",
		0.3,
		"Minimum score for preloaded session recall hits",
	)
	flagMySQLDSN = flag.String(
		"mysql-dsn",
		"",
		"MySQL DSN for mysql backend (env MYSQL_DSN)",
	)

	flagSampleID          = flag.String("sample-id", "", "Filter by sample ID")
	flagCategory          = flag.String("category", "", "Filter by QA category")
	flagMaxTasks          = flag.Int("max-tasks", 0, "Maximum tasks (0=all)")
	flagMaxContext        = flag.Int("max-context", 128000, "Maximum context length")
	flagSessionEventLimit = flag.Int("session-event-limit", 1000, "Max events kept in each session (0=unlimited)")
	flagQAHistoryTurns    = flag.Int(
		"qa-history-turns", 0,
		"Recent conversation turns injected as context during QA (0=none, auto/agentic only)",
	)
	flagQASearchPasses = flag.Int(
		"qa-search-passes",
		2,
		"Number of memory_search calls per QA "+
			"(1=single search, auto/agentic only)",
	)
	flagLLMJudge          = flag.Bool("llm-judge", false, "Enable LLM-as-Judge evaluation")
	flagVerbose           = flag.Bool("verbose", false, "Verbose output")
	flagLMEQuestionTypes  = flag.String("lme-question-types", "", "LongMemEval question types (comma-separated)")
	flagLMEManifest       = flag.String("lme-manifest", "", "LongMemEval fixed case-id manifest path")
	flagLMEReplayRoot     = flag.String("lme-replay-root", "", "LongMemEval Runner replay artifact root")
	flagLMEBuildMaxTokens = flag.Int(
		"lme-build-max-tokens",
		7500,
		"Maximum embedding-model tokens per LongMemEval turn-pair chunk",
	)
	flagLMEBuildTokenizerModel = flag.String(
		"lme-build-tokenizer-model",
		"",
		"Tokenizer model for LongMemEval build plans (default: -embed-model)",
	)
	flagLMEBuildTokenizerEncoding = flag.String(
		"lme-build-tokenizer-encoding",
		"",
		"Explicit tiktoken encoding (known OpenAI embedding models default to cl100k_base)",
	)
	flagLMEAutoQAOnly       = flag.Bool("lme-auto-qa-only", false, "Reuse existing pgvector auto memories and run QA only")
	flagLMEAutoMemoryTable  = flag.String("lme-auto-memory-table", "", "Explicit pgvector table for LongMemEval auto memories")
	flagLMEAutoUpdatePolicy = flag.String(
		"lme-auto-update-policy",
		"merge_similar",
		"Auto-memory update policy for LongMemEval: merge_similar, preserve_history, or append_only",
	)
	flagLMEConversationExtraction = flag.String(
		"lme-conversation-extraction",
		"disabled",
		"Conversation extraction for LongMemEval auto memory: disabled or assistant-episode",
	)
	flagLMEMaxRetries      = flag.Int("lme-max-retries", lmeDefaultMaxRetries, "LongMemEval transport retry count")
	flagLMEAnswerMaxTokens = flag.Int("lme-answer-max-tokens", lmeDefaultAnswerMaxTokens, "LongMemEval answer max tokens")
	flagLMEJudgeMaxTokens  = flag.Int("lme-judge-max-tokens", lmeDefaultJudgeMaxTokens, "LongMemEval judge max tokens")
	flagLMEExtractionWait  = flag.Duration("lme-extraction-wait", 10*time.Minute, "LongMemEval auto extraction wait timeout")
	flagLMETraceContent    = flag.String(
		"lme-trace-content",
		"hash",
		"LongMemEval build trace content mode: full, hash, or none",
	)
	flagLMETraceGzip = flag.Bool(
		"lme-trace-gzip",
		false,
		"Compress LongMemEval build trace attempt files with gzip",
	)
	flagLMEEmbeddingCache    = flag.Bool("lme-embedding-cache", true, "Enable persistent embedding cache for LongMemEval")
	flagLMEEmbeddingCacheDir = flag.String(
		"lme-embedding-cache-dir",
		"",
		"LongMemEval embedding cache directory (default: <output>/longmemeval/.cache)",
	)
	flagMem0Host = flag.String(
		"mem0-host",
		"",
		"Mem0 OSS host for LongMemEval mem0_oss (env MEM0_HOST, e.g. http://localhost:8888)",
	)
	flagMem0APIKey = flag.String(
		"mem0-api-key",
		"",
		"Mem0 API key for LongMemEval mem0_oss (env MEM0_API_KEY; omit when local AUTH_DISABLED=true)",
	)
	flagMem0Version = flag.String(
		"mem0-version",
		"",
		"Mem0 OSS release version for provenance (env MEM0_VERSION)",
	)
	flagMem0Revision = flag.String(
		"mem0-revision",
		"",
		"Optional expected Mem0 OSS source commit; verified preflight value is authoritative (env MEM0_REVISION)",
	)
	flagMem0Preflight = flag.String(
		"mem0-preflight",
		"",
		"Sanitized Mem0 OSS preflight JSON for maintained LongMemEval runs (env MEM0_PREFLIGHT)",
	)
	flagMem0IngestTimeout = flag.Duration(
		"mem0-ingest-timeout",
		0,
		"Mem0 sync ingestion timeout for LongMemEval mem0_oss (default: -lme-extraction-wait)",
	)
	flagMem0ProxyUsageLog = flag.String(
		"mem0-proxy-usage-log",
		"",
		"Optional split proxy JSONL usage log for Mem0 internal LLM/embedding cost",
	)
	flagMem0ProxyRunID = flag.String(
		"mem0-proxy-run-id",
		"",
		"Unique split proxy run ID for scoped Mem0 usage accounting (env MEM0_PROXY_RUN_ID)",
	)
	// Debug flags (auto scenario diagnosis).
	flagDebugDumpMemories = flag.Bool("debug-dump-memories", false, "Dump extracted memories (auto scenario only)")
	flagDebugMemLimit     = flag.Int("debug-mem-limit", 200, "Max memories to dump when debug-dump-memories is enabled")
	flagDebugQALimit      = flag.Int("debug-qa-limit", 5, "Dump retrieval hits for the first N questions (auto scenario only)")
	flagResume            = flag.Bool("resume", false, "Resume LongMemEval scenarios from checkpoint")
	flagTableSuffix       = flag.String(
		"table-suffix",
		"",
		"Suffix appended to all DB table names for parallel runs "+
			"(e.g. _v2 → memory_eval_auto_v2)",
	)
)

// Base table name constants (before suffix).
const (
	lmeDatasetFormat          = "longmemeval"
	lmeDefaultAnswerMaxTokens = 500
	lmeDefaultJudgeMaxTokens  = 10240
	lmeDefaultMaxRetries      = 3
	pgvectorTableDefaultBase  = "memory_eval"
	pgvectorTableAutoBase     = "memory_eval_auto"
	mysqlTableDefaultBase     = "memory_eval_mysql"
	mysqlTableAutoBase        = "memory_eval_auto_mysql"
	sqliteTableDefaultBase    = "memory_eval_sqlite"
	sqliteTableAutoBase       = "memory_eval_auto_sqlite"
	sqliteVecTableDefaultBase = "memory_eval_sqlitevec"
	sqliteVecTableAutoBase    = "memory_eval_auto_sqlitevec"
	sessionRecallTableBase    = "session_eval_recall"

	autoMemoryAsyncWorkers = 3
	autoMemoryQueueSize    = 200
	autoMemoryJobTimeout   = 2 * time.Minute

	maxQASearchPasses = 3
)

// tableNameWithSuffix appends the user-specified suffix to a base table name.
func tableNameWithSuffix(base string) string {
	return benchruntime.TableNameWithSuffix(base, *flagTableSuffix)
}

type memoryMode string

const (
	memoryModeNone   memoryMode = "none"
	memoryModeManual memoryMode = "manual"
	memoryModeAuto   memoryMode = "auto"
)

type memoryConfig struct {
	backend string
	mode    memoryMode
}

// EvaluationResult holds the complete evaluation result.
type EvaluationResult struct {
	Metadata      *EvalMetadata                      `json:"metadata"`
	Summary       *EvalSummary                       `json:"summary"`
	ByCategory    map[string]metrics.CategoryMetrics `json:"by_category"`
	SampleResults []*scenarios.SampleResult          `json:"sample_results,omitempty"`
}

// EvalMetadata holds evaluation metadata.
type EvalMetadata struct {
	Framework      string    `json:"framework"`
	Version        string    `json:"version"`
	Timestamp      time.Time `json:"timestamp"`
	Model          string    `json:"model"`
	EvalModel      string    `json:"eval_model,omitempty"`
	Scenario       string    `json:"scenario"`
	MemoryBackend  string    `json:"memory_backend,omitempty"`
	MaxContext     int       `json:"max_context"`
	QAHistoryTurns int       `json:"qa_history_turns,omitempty"`
	QASearchPasses int       `json:"qa_search_passes,omitempty"`
	LLMJudge       bool      `json:"llm_judge"`
}

// EvalSummary holds aggregated evaluation summary.
type EvalSummary struct {
	TotalSamples    int     `json:"total_samples"`
	TotalQuestions  int     `json:"total_questions"`
	OverallF1       float64 `json:"overall_f1"`
	OverallBLEU     float64 `json:"overall_bleu"`
	OverallLLMScore float64 `json:"overall_llm_score,omitempty"`
	TotalTimeMs     int64   `json:"total_time_ms"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`

	// Token usage statistics.
	TotalPromptTokens     int     `json:"total_prompt_tokens"`
	TotalCompletionTokens int     `json:"total_completion_tokens"`
	TotalTokens           int     `json:"total_tokens"`
	TotalCachedTokens     int     `json:"total_cached_tokens,omitempty"`
	TotalLLMCalls         int     `json:"total_llm_calls"`
	AvgPromptTokensPerQA  float64 `json:"avg_prompt_tokens_per_qa"`
	AvgCompletionPerQA    float64 `json:"avg_completion_tokens_per_qa"`
	AvgCachedTokensPerQA  float64 `json:"avg_cached_tokens_per_qa,omitempty"`
	AvgLLMCallsPerQA      float64 `json:"avg_llm_calls_per_qa"`
	// CacheHitRate is the fraction of prompt tokens served
	// from the provider's prompt cache (0.0–1.0).
	CacheHitRate float64 `json:"cache_hit_rate,omitempty"`
}

func main() {
	flag.Parse()
	validateFlags()

	if *flagDatasetFormat == lmeDatasetFormat {
		ctx, stop := signal.NotifyContext(
			context.Background(),
			os.Interrupt,
			syscall.SIGTERM,
		)
		defer stop()
		llm := openaimodel.New(
			getModelName(),
			longMemEvalOpenAIOptions(lmeLLMAPIKey(), lmeLLMBaseURL())...,
		)
		if err := longmemeval.Run(ctx, buildLongMemEvalConfig(llm)); err != nil {
			log.Fatalf("LongMemEval memory benchmark failed: %v", err)
		}
		return
	}

	modelName := getModelName()
	evalModelName := getEvalModelName()
	outputDir := *flagOutput
	scenariosToRun := getScenarios(*flagScenario)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Parse memory backends.
	backends := parseMemoryBackends(*flagMemoryBackends)

	log.Printf("=== Memory Evaluation (LoCoMo Benchmark) ===")
	log.Printf("Model: %s", modelName)
	log.Printf("Eval Model: %s", evalModelName)
	log.Printf("Scenario: %s", *flagScenario)
	log.Printf("LLM Judge: %v", *flagLLMJudge)
	logScenarioConfig(scenariosToRun, backends)
	log.Printf("Output: %s", outputDir)
	if *flagTableSuffix != "" {
		log.Printf("Table Suffix: %s", *flagTableSuffix)
	}
	if *flagResume {
		log.Printf("Resume mode: enabled (checkpoint will be loaded if exists)")
	}

	// Load dataset.
	loader := dataset.NewLoader(*flagDataset)
	samples, err := loader.LoadSamples(*flagDataFile)
	if err != nil {
		log.Fatalf("Failed to load dataset: %v", err)
	}
	log.Printf("Loaded %d samples", len(samples))

	// Filter samples if specified.
	samples = filterSamples(samples)
	if len(samples) == 0 {
		log.Fatalf("No samples to evaluate")
	}

	// Apply max tasks limit.
	if *flagMaxTasks > 0 && *flagMaxTasks < len(samples) {
		samples = samples[:*flagMaxTasks]
		log.Printf("Limited to %d samples", len(samples))
	}

	// Create models.
	llm := openaimodel.New(modelName)
	var evalLLM = llm
	if evalModelName != "" && evalModelName != modelName {
		evalLLM = openaimodel.New(evalModelName)
	}

	// Base scenario config.
	baseConfig := scenarios.Config{
		MaxContext:            *flagMaxContext,
		EnableLLMJudge:        *flagLLMJudge,
		Verbose:               *flagVerbose,
		SessionEventLimit:     *flagSessionEventLimit,
		QAHistoryTurns:        *flagQAHistoryTurns,
		QASearchPasses:        *flagQASearchPasses,
		SessionRecallResults:  *flagVectorTopK,
		SessionRecallMinScore: *flagSessionRecallMinScore,
		DebugDumpMemories:     *flagDebugDumpMemories,
		DebugMemLimit:         *flagDebugMemLimit,
		DebugQALimit:          *flagDebugQALimit,
	}

	// Run evaluation for each scenario and backend combination.
	for _, scenarioType := range scenariosToRun {
		// Long-context doesn't need memory backends.
		if scenarioType == scenarios.ScenarioLongContext {
			runScenario(samples, llm, evalLLM, scenarioType, "", baseConfig, outputDir)
			continue
		}
		if scenarioType == scenarios.ScenarioSessionRecall {
			runScenario(
				samples,
				llm,
				evalLLM,
				scenarioType,
				"session_pgvector",
				baseConfig,
				outputDir,
			)
			continue
		}

		// Run each backend for memory-based scenarios.
		for _, backend := range backends {
			runScenario(samples, llm, evalLLM, scenarioType, backend, baseConfig, outputDir)
		}
	}
}

func parseCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseMemoryBackends(backendsStr string) []string {
	parts := strings.Split(backendsStr, ",")
	backends := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			backends = append(backends, p)
		}
	}
	return backends
}

func logScenarioConfig(
	scenariosToRun []scenarios.ScenarioType,
	backends []string,
) {
	hasMemoryScenarios := containsScenario(
		scenariosToRun,
		scenarios.ScenarioAgentic,
	) || containsScenario(
		scenariosToRun,
		scenarios.ScenarioAuto,
	)
	if containsScenario(scenariosToRun, scenarios.ScenarioLongContext) {
		log.Printf("Context Mode: long_context transcript preload")
	}
	if containsScenario(scenariosToRun, scenarios.ScenarioSessionRecall) {
		log.Printf("Session Backend: session_pgvector")
		log.Printf("Session Recall Results: %d", *flagVectorTopK)
		if *flagSessionRecallMinScore > 0 {
			log.Printf(
				"Session Recall Min Score: %.3f",
				*flagSessionRecallMinScore,
			)
		}
	}
	if !hasMemoryScenarios {
		return
	}
	log.Printf("Memory Backends: %v", backends)
	if *flagQAHistoryTurns > 0 {
		log.Printf("QA History Turns: %d", *flagQAHistoryTurns)
	}
	if *flagQASearchPasses > 1 {
		log.Printf("QA Search Passes: %d", *flagQASearchPasses)
	}
}

func containsScenario(
	scenariosToRun []scenarios.ScenarioType,
	target scenarios.ScenarioType,
) bool {
	for _, scenario := range scenariosToRun {
		if scenario == target {
			return true
		}
	}
	return false
}

func getScenarios(scenario string) []scenarios.ScenarioType {
	scenarioMap := map[string]scenarios.ScenarioType{
		"long_context":   scenarios.ScenarioLongContext,
		"session_recall": scenarios.ScenarioSessionRecall,
		"agentic":        scenarios.ScenarioAgentic,
		"auto":           scenarios.ScenarioAuto,
		"mem0_oss":       scenarios.ScenarioMem0OSS,
	}
	if scenario == "all" {
		return []scenarios.ScenarioType{
			scenarios.ScenarioLongContext,
			scenarios.ScenarioSessionRecall,
			scenarios.ScenarioAgentic,
			scenarios.ScenarioAuto,
		}
	}
	// Support comma-separated scenarios.
	var result []scenarios.ScenarioType
	seen := make(map[string]bool)
	for _, s := range strings.Split(scenario, ",") {
		s = strings.TrimSpace(s)
		if seen[s] {
			continue
		}
		seen[s] = true
		st, ok := scenarioMap[s]
		if !ok {
			log.Fatalf("Invalid scenario: %s", s)
		}
		result = append(result, st)
	}
	return result
}

func filterSamples(samples []*dataset.LoCoMoSample) []*dataset.LoCoMoSample {
	// Filter by sample ID.
	if *flagSampleID != "" {
		filtered := make([]*dataset.LoCoMoSample, 0)
		for _, s := range samples {
			if s.SampleID == *flagSampleID {
				filtered = append(filtered, s)
			}
		}
		samples = filtered
		log.Printf("Filtered to %d samples (sample_id=%s)", len(samples), *flagSampleID)
	}

	// Filter by category.
	if *flagCategory != "" {
		for _, s := range samples {
			filtered := make([]dataset.QAItem, 0)
			for _, qa := range s.QA {
				if qa.Category == *flagCategory {
					filtered = append(filtered, qa)
				}
			}
			s.QA = filtered
		}
		log.Printf("Filtered QA by category: %s", *flagCategory)
	}
	return samples
}

func runScenario(
	samples []*dataset.LoCoMoSample,
	llm, evalLLM model.Model,
	scenarioType scenarios.ScenarioType,
	backend string,
	baseConfig scenarios.Config,
	outputDir string,
) {
	config := baseConfig
	config.Scenario = scenarioType

	var evaluator scenarios.Evaluator
	var memSvc memory.Service
	var sessionSvc session.Service
	var err error
	memCfg := buildMemoryConfig(scenarioType, backend)
	memOpts := buildMemoryServiceOptions(memCfg, llm)

	switch scenarioType {
	case scenarios.ScenarioLongContext:
		evaluator = scenarios.NewLongContextEvaluator(llm, evalLLM, config)
	case scenarios.ScenarioSessionRecall:
		sessionSvc, err = createSessionRecallService(config)
		if err != nil {
			log.Printf("Failed to create session recall service: %v", err)
			return
		}
		evaluator = scenarios.NewSessionRecallEvaluator(
			llm, evalLLM, sessionSvc, config,
		)
	case scenarios.ScenarioAgentic:
		memSvc, err = createMemoryService(memCfg, memOpts)
		if err != nil {
			log.Printf("Failed to create %s memory service: %v", backend, err)
			return
		}
		evaluator = scenarios.NewAgenticEvaluator(llm, evalLLM, memSvc, config)
	case scenarios.ScenarioAuto:
		memSvc, err = createMemoryService(memCfg, memOpts)
		if err != nil {
			log.Printf("Failed to create %s memory service: %v", backend, err)
			return
		}
		evaluator = scenarios.NewAutoEvaluator(llm, evalLLM, memSvc, config)
	}

	// Determine output directory.
	scenarioDir := buildScenarioDir(outputDir, scenarioType, backend)
	if err := os.MkdirAll(scenarioDir, 0755); err != nil {
		log.Printf("Failed to create scenario directory: %v", err)
		return
	}

	log.Printf("")
	log.Printf("=== Running: %s (backend=%s) ===", evaluator.Name(), backend)

	result := runEvaluation(
		samples, evaluator, config, backend, scenarioDir,
	)
	saveResults(scenarioDir, result)
	printSummary(result)

	// Cleanup memory service.
	if memSvc != nil {
		memSvc.Close()
	}
	if sessionSvc != nil {
		sessionSvc.Close()
	}
}

func buildScenarioDir(outputDir string, scenario scenarios.ScenarioType, backend string) string {
	if scenario == scenarios.ScenarioLongContext {
		return filepath.Join(outputDir, string(scenario))
	}
	return filepath.Join(outputDir, fmt.Sprintf("%s_%s", scenario, backend))
}

func validateFlags() {
	validateDatasetFlags()
	validateScenarioFlags()
	backends := validateMemoryBackendFlags()
	validateLMEAutoQAOnlyFlags(backends)
	validateSessionFlags()
}

func validateDatasetFlags() {
	if *flagDatasetFormat != "locomo" && *flagDatasetFormat != lmeDatasetFormat {
		log.Fatalf("Invalid dataset-format: %s", *flagDatasetFormat)
	}
	if *flagDatasetFormat != lmeDatasetFormat && *flagVectorTopK < 1 {
		log.Fatalf("Invalid vector-topk: %d", *flagVectorTopK)
	}
	if *flagQASearchPasses < 1 ||
		*flagQASearchPasses > maxQASearchPasses {
		log.Fatalf(
			"Invalid qa-search-passes: %d (range: 1-%d)",
			*flagQASearchPasses,
			maxQASearchPasses,
		)
	}
}

func validateScenarioFlags() {
	validScenarios := map[string]bool{
		"long_context":   true,
		"session_recall": true,
		"agentic":        true,
		"auto":           true,
		"mem0_oss":       true,
		"replay":         true,
		"all":            true,
	}
	for _, s := range strings.Split(*flagScenario, ",") {
		s = strings.TrimSpace(s)
		if !validScenarios[s] {
			log.Fatalf("Invalid scenario: %s", s)
		}
		if (s == "replay" || s == "mem0_oss") &&
			*flagDatasetFormat != lmeDatasetFormat {
			log.Fatalf("%s is only supported with dataset-format longmemeval", s)
		}
	}
}

func validateMemoryBackendFlags() []string {
	validBackends := map[string]bool{
		"inmemory":  true,
		"sqlite":    true,
		"sqlitevec": true,
		"pgvector":  true,
		"mysql":     true,
	}
	backends := parseMemoryBackends(*flagMemoryBackends)
	for _, b := range backends {
		if !validBackends[b] {
			log.Fatalf("Invalid memory backend: %s", b)
		}
	}
	return backends
}

func validateLMEAutoQAOnlyFlags(backends []string) {
	if *flagLMEAutoQAOnly {
		if *flagDatasetFormat != lmeDatasetFormat {
			log.Fatalf("lme-auto-qa-only requires dataset-format longmemeval")
		}
		scenario := strings.TrimSpace(*flagScenario)
		if scenario != "auto" {
			log.Fatalf("lme-auto-qa-only requires scenario auto")
		}
		if len(backends) != 1 || backends[0] != "pgvector" {
			log.Fatalf("lme-auto-qa-only requires memory-backend pgvector")
		}
		if strings.TrimSpace(*flagTableSuffix) == "" &&
			strings.TrimSpace(*flagLMEAutoMemoryTable) == "" {
			log.Fatalf(
				"lme-auto-qa-only requires table-suffix or lme-auto-memory-table",
			)
		}
	}
}

func validateSessionFlags() {
	if *flagMaxContext <= 0 {
		log.Fatalf("Invalid max-context: %d", *flagMaxContext)
	}
	if *flagSessionEventLimit < 0 {
		log.Fatalf("Invalid session-event-limit: %d", *flagSessionEventLimit)
	}
	if *flagSessionRecallMinScore < 0 {
		log.Fatalf(
			"Invalid session-recall-min-score: %f",
			*flagSessionRecallMinScore,
		)
	}
}

func getModelName() string {
	if *flagModel != "" {
		return *flagModel
	}
	if env := os.Getenv(envLLMName); env != "" {
		return env
	}
	if env := os.Getenv("MODEL_NAME"); env != "" {
		return env
	}
	return "gpt-4o-mini"
}

func getEvalModelName() string {
	if *flagEvalModel != "" {
		return *flagEvalModel
	}
	if env := os.Getenv("EVAL_MODEL_NAME"); env != "" {
		return env
	}
	return getModelName()
}
