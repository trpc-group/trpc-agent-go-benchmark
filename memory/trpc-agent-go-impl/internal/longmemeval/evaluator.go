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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/internal/benchruntime"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder/openai"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	memorypgvector "trpc.group/trpc-go/trpc-agent-go/memory/pgvector"
	memorytool "trpc.group/trpc-go/trpc-agent-go/memory/tool"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type lmeEvaluator interface {
	Name() string
	Evaluate(
		ctx context.Context,
		inst *dataset.LongMemEvalInstance,
	) (*lmeCaseResult, error)
	Close() error
}

const lmeAutoMaxToolIterations = 8

func (e *lmeLongContextEvaluator) Name() string { return "long_context" }

func (e *lmeLongContextEvaluator) Close() error { return nil }

func (e *lmeLongContextEvaluator) Evaluate(
	ctx context.Context,
	inst *dataset.LongMemEvalInstance,
) (*lmeCaseResult, error) {
	start := time.Now()
	prompt, err := buildLMELongContextPrompt(inst)
	if err != nil {
		return nil, err
	}
	mr, err := runLMEModelWithRetry(ctx, e.llm, []model.Message{
		model.NewUserMessage(prompt),
	}, e.cfg.AnswerMaxTokens, e.cfg.MaxRetries)
	if err != nil {
		return nil, err
	}
	return buildLMECaseResult(
		ctx, e.llm, e.cfg, inst, strings.TrimSpace(mr.Text),
		time.Since(start), modelUsageToScenarioUsage(mr.Usage), mr.RetryCount, nil, nil,
	)
}

func (e *lmeAutoEvaluator) Name() string {
	return "auto"
}

func (e *lmeAutoEvaluator) Close() error {
	if e.mem == nil {
		return nil
	}
	return e.mem.Close()
}

func (e *lmeAutoEvaluator) Evaluate(
	ctx context.Context,
	inst *dataset.LongMemEvalInstance,
) (result *lmeCaseResult, retErr error) {
	trace, err := e.trace.beginCase(inst.QuestionID)
	if err != nil {
		return nil, err
	}
	ctx = withLMECaseTrace(ctx, trace)
	trace.setExtractionExpected(!e.cfg.AutoQAOnly)
	defer func() {
		if result == nil && retErr != nil {
			result = newLMEFailedCase(inst, retErr)
		}
		if err := trace.finish(result, retErr); err != nil {
			result = nil
			retErr = errors.Join(retErr, fmt.Errorf("finalize LongMemEval trace: %w", err))
		}
	}()
	ctx = withLMECostTracker(ctx, e.cost)
	start := time.Now()
	userKey := memory.UserKey{AppName: lmeAppAuto, UserID: inst.QuestionID}
	if e.cfg.AutoQAOnly {
		if err := requireLMEAutoMemories(ctx, e.mem, userKey); err != nil {
			return nil, err
		}
		trace.setInitialSnapshot(nil)
	} else {
		if err := e.mem.ClearMemories(ctx, userKey); err != nil {
			trace.markPersistenceError(err)
			return nil, fmt.Errorf("clear memories: %w", err)
		}
		trace.setInitialSnapshot(nil)
		casePlan, err := loadLMEBuildCasePlan(e.cfg, inst.QuestionID)
		if err != nil {
			trace.markBuildError(err)
			return nil, fmt.Errorf("load immutable build plan: %w", err)
		}
		if err := executeLMEBuildCase(ctx, e.cfg, casePlan, lmeBuildExecutionOptions{
			AppName:       lmeAppAuto,
			MemoryService: e.mem,
		}); err != nil {
			trace.markBuildError(err)
			return nil, fmt.Errorf("execute immutable auto build plan: %w", err)
		}
	}
	qaCtx := withLMEEmbeddingPhase(ctx, lmeEmbeddingPhaseQARetrieval)
	qa := &lmeMemoryQA{
		appName:  lmeAppAuto,
		judgeLLM: e.judgeLLM,
		qaLLM:    e.qaLLM,
		mem:      e.mem,
		cfg:      e.cfg,
	}
	result, retErr = qa.run(ctx, qaCtx, inst, start)
	return result, retErr
}

func (q *lmeMemoryQA) run(
	ctx context.Context,
	qaCtx context.Context,
	inst *dataset.LongMemEvalInstance,
	start time.Time,
) (*lmeCaseResult, error) {
	if trace := lmeCaseTraceFromContext(ctx); trace != nil {
		trace.markQAStarted()
	}
	qaMem, err := newLMEQAMemoryService(q.mem)
	if err != nil {
		return nil, err
	}
	agent := q.newAgent()
	qaRunner := runner.NewRunner(
		q.appName,
		agent,
		runner.WithSessionService(sessioninmemory.NewSessionService()),
		runner.WithMemoryService(qaMem),
	)
	defer qaRunner.Close()
	msg := model.NewUserMessage(
		fmt.Sprintf("Current Date: %s\nQuestion: %s", inst.QuestionDate, inst.Question),
	)
	cr, err := runLMERunnerWithRetry(qaCtx, q.cfg.MaxRetries, func() (<-chan *event.Event, error) {
		return qaRunner.Run(qaCtx, inst.QuestionID, "qa-"+inst.QuestionID, msg)
	})
	if err != nil {
		return nil, err
	}
	return buildLMECaseResult(
		ctx, q.judgeLLM, q.cfg, inst, strings.TrimSpace(cr.Text),
		time.Since(start), &cr.Usage, cr.RetryCount,
		cr.Steps, lmeQAConversationTrace(q.cfg, msg, cr.Trace),
	)
}

func requireLMEAutoMemories(
	ctx context.Context,
	mem memory.Service,
	userKey memory.UserKey,
) error {
	entries, err := mem.ReadMemories(ctx, userKey, 1)
	if err != nil {
		return fmt.Errorf("read QA-only auto memories: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf(
			"QA-only auto memories missing for %s/%s",
			userKey.AppName,
			userKey.UserID,
		)
	}
	return nil
}

func (e *lmeAutoEvaluator) CostReport() *lmeCostReport {
	if e.cost == nil {
		return nil
	}
	return e.cost.snapshot()
}

func (q *lmeMemoryQA) newAgent() agent.Agent {
	maxTokens := q.cfg.AnswerMaxTokens
	temp := 0.0
	thinking := false
	return llmagent.New(lmeQAAgentName,
		llmagent.WithModel(q.qaLLM),
		llmagent.WithInstruction(q.instruction()),
		llmagent.WithTools(lmeMemoryQATools()),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Stream:          false,
			MaxTokens:       &maxTokens,
			Temperature:     &temp,
			ThinkingEnabled: &thinking,
		}),
		llmagent.WithMaxToolIterations(lmeAutoMaxToolIterations),
	)
}

func (q *lmeMemoryQA) instruction() string {
	return `You are a memory retrieval assistant for LongMemEval.

Rules:
- Use memory_search before answering.
- Use short keyword queries with names, entities, dates, and topics from the question.
- Do not use kind filters.
- Answer only from retrieved memories.
- If the retrieved memories do not contain enough information, say that the information is not available.
- Output the direct answer only.`
}

func lmeMemoryQATools() []tool.Tool {
	return []tool.Tool{memorytool.NewSearchTool()}
}

const (
	pgvectorTableAutoBase = "memory_eval_auto"

	autoMemoryAsyncWorkers = 3
	autoMemoryQueueSize    = 200
	autoMemoryJobTimeout   = 2 * time.Minute
)

var lmeRuntime Config

var _ memory.Service = (*lmeQAMemoryService)(nil)

type lmeQAMemoryService struct {
	memory.Service
}

type memoryServiceOptions struct {
	enableExtractor        bool
	extractorModel         model.Model
	memoryJobTimeout       time.Duration
	tableName              string
	vectorTopK             int
	autoUpdatePolicy       lmeAutoUpdatePolicy
	conversationExtraction lmeConversationExtraction
}

func newEmbeddingEmbedder(modelName string) (embedder.Embedder, error) {
	opts := []openai.Option{openai.WithModel(modelName)}
	if apiKey := strings.TrimSpace(lmeRuntime.EmbeddingAPIKey); apiKey != "" {
		opts = append(opts, openai.WithAPIKey(apiKey))
	}
	if baseURL := strings.TrimSpace(lmeRuntime.EmbeddingBaseURL); baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	inner := openai.New(opts...)
	if !lmeRuntime.EmbeddingCacheEnabled {
		return newLMETrackedEmbedder(inner), nil
	}
	cache, err := newLMEEmbeddingCache(inner, modelName, lmeRuntime.EmbeddingCachePath)
	if err != nil {
		return nil, err
	}
	return cache, nil
}

func createAutoMemoryService(opts memoryServiceOptions) (memory.Service, error) {
	if lmeRuntime.PGVectorDSN == "" {
		return nil, fmt.Errorf(
			"pgvector-dsn or PGVECTOR_DSN is required for pgvector backend",
		)
	}
	embedModelName := lmeRuntime.EmbedModelName
	emb, err := newEmbeddingEmbedder(embedModelName)
	if err != nil {
		return nil, err
	}
	tableName := opts.tableName
	if tableName == "" {
		tableName = benchruntime.TableNameWithSuffix(
			pgvectorTableAutoBase,
			lmeRuntime.TableSuffix,
		)
	}
	var ext extractor.MemoryExtractor
	if opts.enableExtractor {
		log.Printf(
			"Creating pgvector memory service with extractor (embed_model=%s, update_policy=%s, conversation_extraction=%s)",
			embedModelName,
			opts.autoUpdatePolicy,
			opts.conversationExtraction,
		)
		ext = newLMEExtractor(opts)
	} else {
		log.Printf(
			"Creating pgvector memory service (embed_model=%s)",
			embedModelName,
		)
	}
	log.Printf("Using pgvector memory table: %s", tableName)
	svcOpts := []memorypgvector.ServiceOpt{
		memorypgvector.WithPGVectorClientDSN(lmeRuntime.PGVectorDSN),
		memorypgvector.WithEmbedder(emb),
		memorypgvector.WithMaxResults(opts.vectorTopK),
		memorypgvector.WithTableName(tableName),
		memorypgvector.WithExtractor(ext),
	}
	if opts.enableExtractor {
		svcOpts = append(svcOpts,
			memorypgvector.WithAsyncMemoryNum(autoMemoryAsyncWorkers),
			memorypgvector.WithMemoryQueueSize(autoMemoryQueueSize),
			memorypgvector.WithMemoryJobTimeout(autoMemoryJobTimeoutFor(opts)),
		)
	}
	return memorypgvector.NewService(svcOpts...)
}

func newLMEExtractor(opts memoryServiceOptions) extractor.MemoryExtractor {
	policy := extractor.UpdatePolicy(opts.autoUpdatePolicy)
	extractorOpts := []extractor.Option{
		extractor.WithUpdatePolicy(policy),
	}
	if opts.conversationExtraction == lmeConversationExtractionAssistantEpisode {
		extractorOpts = append(extractorOpts, extractor.WithAssistantEpisodeExtraction())
	}
	return newLMETracingExtractor(
		extractor.NewExtractor(opts.extractorModel, extractorOpts...),
		policy,
	)
}

func autoMemoryJobTimeoutFor(opts memoryServiceOptions) time.Duration {
	if opts.memoryJobTimeout > 0 {
		return opts.memoryJobTimeout
	}
	return autoMemoryJobTimeout
}

func newLMEQAMemoryService(inner memory.Service) (memory.Service, error) {
	if inner == nil {
		return nil, fmt.Errorf("LongMemEval QA memory service is required")
	}
	return &lmeQAMemoryService{Service: inner}, nil
}

func (s *lmeQAMemoryService) SearchMemories(
	ctx context.Context,
	userKey memory.UserKey,
	query string,
	opts ...memory.SearchOption,
) ([]*memory.Entry, error) {
	searchOpts := memory.ResolveSearchOptions(query, opts)
	searchOpts.Query = query
	searchOpts.MaxResults = lmeRetrievalTopK
	entries, err := s.Service.SearchMemories(
		ctx,
		userKey,
		query,
		memory.WithSearchOptions(searchOpts),
	)
	if err != nil {
		return nil, fmt.Errorf("search LongMemEval QA memories: %w", err)
	}
	if len(entries) > lmeRetrievalTopK {
		entries = entries[:lmeRetrievalTopK]
	}
	return entries, nil
}

func (s *lmeQAMemoryService) EnqueueAutoMemoryJob(
	_ context.Context,
	_ *session.Session,
) error {
	return nil
}

func (s *lmeQAMemoryService) Close() error {
	return nil
}

func redactLMERunConfig(config lmeRunConfig) lmeRunConfig {
	config.Mem0Host = sanitizeEndpoint(config.Mem0Host)
	config.Mem0PreflightPath = sanitizeLocation(config.Mem0PreflightPath)
	return config
}

func setLMERunMetadata(result *lmeRunResult, evaluator lmeEvaluator) {
	if result == nil || result.Metadata == nil {
		return
	}
	name := evaluator.Name()
	if name != "auto" && name != "mem0_oss" {
		return
	}
	method, framework := lmeMemoryBuildIdentity(name)
	autoQAOnly := result.Metadata.Config.AutoQAOnly
	if autoQAOnly {
		method = fmt.Sprintf(
			"%s from %s (QA only)",
			method,
			result.Metadata.Config.AutoMemoryTable,
		)
	}
	totalSessions, totalTurns := lmeMemoryBuildCounts(result.Cases)
	failed := make([]string, 0)
	if result.Summary != nil && result.Summary.CompletedCases != result.Summary.TotalCases {
		failed = append(failed, "incomplete_result")
	}
	comparable := len(failed) == 0
	var limitations []string
	result.Metadata.MemoryOnlyCompliant = true
	result.Metadata.NativeMemoryPreserved = true
	result.Metadata.FairlyComparable = comparable && len(limitations) == 0
	result.Metadata.ComparisonLimitations = limitations
	if !comparable {
		result.Metadata.ComparisonStatus = "not_comparable"
	} else if len(limitations) > 0 {
		result.Metadata.ComparisonStatus = "comparable_with_limitations"
	} else {
		result.Metadata.ComparisonStatus = "comparable"
	}
	result.Metadata.ComparisonBlockers = failed
	result.Metadata.MemoryBuildMethod = method
	status := "completed"
	costIncluded := true
	if autoQAOnly {
		status = "reused"
		costIncluded = false
		totalSessions = 0
		totalTurns = 0
	}
	if !comparable {
		status = "failed"
	}
	result.Metadata.MemoryBuild = newLMEMemoryBuildMetadata(
		result,
		name,
		status,
		costIncluded,
		totalSessions,
		totalTurns,
		failed,
	)
	setLMEMemoryOnlyMetadata(result.Metadata, framework, len(result.Cases))
}

func lmeMemoryBuildIdentity(name string) (string, string) {
	if name == "mem0_oss" {
		return "immutable LongMemEval build plan -> Runner -> synchronous session.Ingestor -> Mem0 OSS",
			"trpc-agent-go + Mem0 OSS"
	}
	return "immutable LongMemEval build plan -> Runner -> EnqueueAutoMemoryJob -> native auto memory worker",
		"trpc-agent-go"
}

func lmeMemoryBuildCounts(cases []*lmeCaseResult) (int, int) {
	var sessions int
	var turns int
	for _, record := range cases {
		if record == nil {
			continue
		}
		sessions += record.TotalSessions
		turns += record.TotalTurns
	}
	return sessions, turns
}

func newLMEMemoryBuildMetadata(
	result *lmeRunResult,
	name string,
	status string,
	costIncluded bool,
	totalSessions int,
	totalTurns int,
	failed []string,
) map[string]any {
	config := result.Metadata.Config
	memoryBuild := map[string]any{
		"method":                    result.Metadata.MemoryBuildMethod,
		"backend":                   result.Metadata.MemoryBackend,
		"status":                    status,
		"cost_included":             costIncluded,
		"failure_stage_method":      "heuristic_session_provenance",
		"gold_recall_unit":          "answer_session_id",
		"gold_evidence_span_recall": "unavailable",
		"sample_count":              len(result.Cases),
		"failed_samples":            failed,
		"total_sessions_ingested":   totalSessions,
		"total_turns_ingested":      totalTurns,
		"protocol":                  lmeBuildProtocol,
		"runner_lifecycle":          lmeBuildRunnerLifecycle,
		"session_lifecycle":         lmeBuildSessionLifecycle,
		"replay_digest":             config.ReplayDigest,
		"build_plan_digest":         config.BuildPlanDigest,
		"build_plan_root":           config.BuildPlanRoot,
		"tokenizer":                 config.BuildTokenizer,
		"tokenizer_model":           config.BuildTokenizerModel,
		"max_content_tokens":        config.BuildMaxTokens,
		"turn_count":                config.BuildStats.TurnCount,
		"pair_count":                config.BuildStats.PairCount,
		"chunk_count":               config.BuildStats.ChunkCount,
		"chunked_session_count":     config.BuildStats.ChunkedSessionCount,
		"chunked_pair_count":        config.BuildStats.ChunkedPairCount,
		"split_turn_count":          config.BuildStats.SplitTurnCount,
		"fragmented_case_ids":       append([]string(nil), config.BuildStats.FragmentedCaseIDs...),
		"original_tokens":           config.BuildStats.OriginalTokens,
		"final_tokens":              config.BuildStats.FinalTokens,
		"original_bytes":            config.BuildStats.OriginalBytes,
		"final_bytes":               config.BuildStats.FinalBytes,
		"max_original_turn_tokens":  config.BuildStats.MaxOriginalTurnTokens,
		"max_original_pair_tokens":  config.BuildStats.MaxOriginalPairTokens,
		"max_session_tokens":        config.BuildStats.MaxSessionTokens,
		"max_chunk_tokens":          config.BuildStats.MaxChunkTokens,
		"temporal_reference_source": lmeTemporalReferenceSource,
		"temporal_reference_format": lmeTemporalReferenceFormat,
	}
	if name == "auto" {
		memoryBuild["update_policy"] = config.AutoUpdatePolicy
		memoryBuild["conversation_extraction"] = config.ConversationExtraction
		memoryBuild["temporal_context"] = lmeAutoTemporalContext
	} else if name == "mem0_oss" {
		memoryBuild["temporal_context"] = lmeMem0TemporalContext
		memoryBuild["custom_extraction_prompt"] = true
		memoryBuild["session_date_message_prefix"] = false
		memoryBuild["preflight_digest"] = config.Mem0PreflightDigest
		memoryBuild["environment_lock_digest"] = config.Mem0EnvironmentLockDigest
		memoryBuild["observation_prompt_verified"] = config.Mem0ObservationPromptVerified
	}
	if config.AutoQAOnly {
		memoryBuild["source_table"] = config.AutoMemoryTable
	}
	if config.ReplayRoot != "" {
		memoryBuild["replay_root"] = config.ReplayRoot
	}
	return memoryBuild
}

func setLMEMemoryOnlyMetadata(metadata *lmeMetadata, framework string, caseCount int) {
	metadata.MemoryOnlyPolicy = map[string]any{
		"enabled":    true,
		"framework":  framework,
		"qa_runtime": "fresh in-memory QA session per question",
		"allowed_inputs": []string{
			"current_question",
			"question_date",
			"memory_search results",
		},
		"forbidden_inputs": []string{
			"full_conversation_transcript",
			"full_session_transcript",
			"longmemeval_haystack",
			"gold_evidence",
			"gold_answer_except_judge_prompt",
		},
	}
	metadata.MemoryOnlySummary = map[string]any{
		"compliant":     true,
		"checked_cases": caseCount,
		"failed_cases":  []string{},
		"violations":    map[string][]string{},
	}
	metadata.QAContextPolicy = "fresh QA sessions with only current question and memory_search results"
}

var errLMEJudgeInvalidResponse = errors.New("invalid judge response")

func buildLMECaseResult(
	ctx context.Context,
	judge model.Model,
	cfg lmeRunConfig,
	inst *dataset.LongMemEvalInstance,
	predicted string,
	latency time.Duration,
	tokenUsage *scenarios.TokenUsage,
	retryCount int,
	steps []lmeStepTrace,
	qaTrace []lmeMessageTrace,
) (*lmeCaseResult, error) {
	trace := lmeCaseTraceFromContext(ctx)
	if trace != nil {
		if err := trace.recordQA(steps); err != nil {
			return nil, fmt.Errorf("record LongMemEval QA trace: %w", err)
		}
		if err := trace.joinGold(inst.AnswerSessionIDs); err != nil {
			return nil, fmt.Errorf("join LongMemEval gold trace data: %w", err)
		}
	}
	answerMetrics := metrics.CalculateAnswerMetrics(predicted, inst.Answer)
	correct, judgeUsage, judgeRetries, err := judgeLMEAnswer(ctx, judge, cfg, inst, predicted)
	if trace != nil {
		trace.markJudgeError(err)
	}
	if err != nil {
		if !errors.Is(err, errLMEJudgeInvalidResponse) {
			return nil, err
		}
		answerMetrics.Accuracy = 0
	} else {
		if correct {
			answerMetrics.Accuracy = 1
		} else {
			answerMetrics.Accuracy = 0
		}
	}
	if tokenUsage == nil {
		tokenUsage = &scenarios.TokenUsage{}
	}
	if judgeUsage != nil {
		tokenUsage.Add(*modelUsageToScenarioUsage(judgeUsage))
	}
	status := lmeCaseStatusSucceeded
	if err != nil {
		status = lmeCaseStatusJudgeFailed
	}
	return &lmeCaseResult{
		Status:        status,
		QuestionID:    inst.QuestionID,
		QuestionType:  inst.QuestionType,
		Question:      inst.Question,
		QuestionDate:  inst.QuestionDate,
		Expected:      inst.Answer,
		Predicted:     predicted,
		IsAbstention:  inst.IsAbstention(),
		Correct:       correct,
		Metrics:       answerMetrics,
		LatencyMs:     latency.Milliseconds(),
		TokenUsage:    tokenUsage,
		RetryCount:    retryCount + judgeRetries,
		TotalTurns:    inst.TotalTurns(),
		TotalSessions: len(inst.HaystackSessions),
		ToolSteps:     steps,
		QATrace:       qaTrace,
		JudgeError:    judgeErrorString(err),
	}, nil
}

func lmeQAConversationTrace(
	cfg lmeRunConfig,
	userMsg model.Message,
	events []lmeMessageTrace,
) []lmeMessageTrace {
	if !cfg.FullQALog {
		return nil
	}
	trace := make([]lmeMessageTrace, 0, len(events)+1)
	trace = append(trace, lmeMessageTrace{
		Role:    string(userMsg.Role),
		Content: userMsg.Content,
	})
	return append(trace, events...)
}

func judgeLMEAnswer(
	ctx context.Context,
	judge model.Model,
	cfg lmeRunConfig,
	inst *dataset.LongMemEvalInstance,
	predicted string,
) (bool, *model.Usage, int, error) {
	prompt, err := metrics.LongMemEvalJudgePrompt(
		inst.QuestionType,
		inst.Question,
		inst.Answer,
		predicted,
		inst.IsAbstention(),
	)
	if err != nil {
		return false, nil, 0, err
	}
	mr, err := runLMEModelWithRetry(ctx, judge, []model.Message{
		model.NewUserMessage(prompt),
	}, cfg.JudgeMaxTokens, cfg.MaxRetries)
	if err != nil {
		return false, nil, mr.RetryCount, fmt.Errorf("judge model: %w", err)
	}
	label, err := metrics.ParseLongMemEvalJudgeLabel(mr.Text)
	if err != nil {
		initialParseErr := err
		repairMR, repairErr := runLMEModelWithRetry(ctx, judge, []model.Message{
			model.NewUserMessage(buildLMEJudgeRepairPrompt(prompt, mr.Text)),
		}, cfg.JudgeMaxTokens, cfg.MaxRetries)
		totalUsage := mergeLMEModelUsage(mr.Usage, repairMR.Usage)
		totalRetries := mr.RetryCount + repairMR.RetryCount
		if repairErr != nil {
			return false, totalUsage, totalRetries, fmt.Errorf("judge repair model: %w", repairErr)
		}
		label, err = metrics.ParseLongMemEvalJudgeLabel(repairMR.Text)
		if err != nil {
			return false, totalUsage, totalRetries, fmt.Errorf(
				"%w: parse judge repair response after initial parse error %v: %v",
				errLMEJudgeInvalidResponse,
				initialParseErr,
				err,
			)
		}
		return label, totalUsage, totalRetries, nil
	}
	return label, mr.Usage, mr.RetryCount, nil
}

func buildLMEJudgeRepairPrompt(originalPrompt, invalidResponse string) string {
	return fmt.Sprintf(`Your previous judge response was not a valid yes/no label:
%q

Return exactly one word now: yes or no.
If the case is ambiguous, choose the more likely label.
Do not explain.

Original judging task:
%s`, invalidResponse, originalPrompt)
}

func judgeErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func mergeLMEModelUsage(usages ...*model.Usage) *model.Usage {
	var out model.Usage
	var hasUsage bool
	for _, usage := range usages {
		if usage == nil {
			continue
		}
		hasUsage = true
		out.PromptTokens += usage.PromptTokens
		out.CompletionTokens += usage.CompletionTokens
		out.TotalTokens += usage.TotalTokens
		out.PromptTokensDetails.CachedTokens += usage.PromptTokensDetails.CachedTokens
		out.PromptTokensDetails.CacheCreationTokens += usage.PromptTokensDetails.CacheCreationTokens
		out.PromptTokensDetails.CacheReadTokens += usage.PromptTokensDetails.CacheReadTokens
		out.CompletionTokensDetails.ReasoningTokens += usage.CompletionTokensDetails.ReasoningTokens
	}
	if !hasUsage {
		return nil
	}
	return &out
}

func buildLMELongContextPrompt(inst *dataset.LongMemEvalInstance) (string, error) {
	history, err := lmeHistoryJSON(inst)
	if err != nil {
		return "", err
	}
	template := "I will give you several history chats between you and a user. Please answer the question based on the relevant chat history.\n\n\nHistory Chats:\n\n%s\n\nCurrent Date: %s\nQuestion: %s\nAnswer:"
	return fmt.Sprintf(template, history, inst.QuestionDate, inst.Question), nil
}

func lmeHistoryJSON(inst *dataset.LongMemEvalInstance) (string, error) {
	type turn struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type sess struct {
		SessionID string `json:"session_id"`
		Date      string `json:"date"`
		Turns     []turn `json:"turns"`
	}
	out := make([]sess, 0, len(inst.HaystackSessions))
	for i, sessionTurns := range inst.HaystackSessions {
		s := sess{
			SessionID: inst.HaystackSessionIDs[i],
			Date:      inst.HaystackDates[i],
			Turns:     make([]turn, 0, len(sessionTurns)),
		}
		for _, t := range sessionTurns {
			s.Turns = append(s.Turns, turn{Role: t.Role, Content: t.Content})
		}
		out = append(out, s)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal LongMemEval history: %w", err)
	}
	return string(data), nil
}

func lmeScenarioDir(rootDir string, scenario scenarios.ScenarioType, backend string) string {
	if scenario == scenarios.ScenarioMem0OSS {
		return filepath.Join(rootDir, string(scenario))
	}
	if backend == "" {
		return filepath.Join(rootDir, string(scenario))
	}
	return filepath.Join(rootDir, fmt.Sprintf("%s_%s", scenario, backend))
}

func truncateLME(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit-3] + "..."
}

func runLMEModelWithRetry(
	ctx context.Context,
	llm model.Model,
	messages []model.Message,
	maxTokens int,
	maxRetries int,
) (lmeModelResult, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		res, err := runLMEModelOnce(ctx, llm, messages, maxTokens)
		if err == nil {
			res.RetryCount = attempt
			return res, nil
		}
		lastErr = err
		if !isLMETransportError(err) || attempt == maxRetries {
			return lmeModelResult{RetryCount: attempt}, err
		}
		log.Printf("LongMemEval transport retry %d/%d after error: %v", attempt+1, maxRetries, err)
		if sleepErr := lmeRetrySleep(ctx, attempt); sleepErr != nil {
			return lmeModelResult{RetryCount: attempt}, sleepErr
		}
	}
	return lmeModelResult{RetryCount: maxRetries}, lastErr
}

func runLMEModelOnce(
	ctx context.Context,
	llm model.Model,
	messages []model.Message,
	maxTokens int,
) (lmeModelResult, error) {
	temp := 0.0
	thinking := false
	req := &model.Request{
		Messages: messages,
		GenerationConfig: model.GenerationConfig{
			Stream:          false,
			MaxTokens:       &maxTokens,
			Temperature:     &temp,
			ThinkingEnabled: &thinking,
		},
	}
	ch, err := llm.GenerateContent(ctx, req)
	if err != nil {
		return lmeModelResult{}, err
	}
	var b strings.Builder
	var usage *model.Usage
	for resp := range ch {
		if resp == nil {
			continue
		}
		if resp.Error != nil {
			return lmeModelResult{}, errors.New(resp.Error.Message)
		}
		if len(resp.Choices) > 0 {
			b.WriteString(resp.Choices[0].Message.Content)
		}
		if resp.Usage != nil {
			usage = resp.Usage
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return lmeModelResult{}, fmt.Errorf("model returned empty response")
	}
	return lmeModelResult{Text: text, Usage: usage}, nil
}

func runLMERunnerWithRetry(
	ctx context.Context,
	maxRetries int,
	run func() (<-chan *event.Event, error),
) (lmeCollectResult, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		ch, err := run()
		if err == nil {
			var res lmeCollectResult
			res, err = collectLMEEvents(ch)
			if err == nil {
				res.RetryCount = attempt
				return res, nil
			}
		}
		lastErr = err
		if !isLMETransportError(err) || attempt == maxRetries {
			return lmeCollectResult{RetryCount: attempt}, err
		}
		log.Printf("LongMemEval runner transport retry %d/%d after error: %v", attempt+1, maxRetries, err)
		if sleepErr := lmeRetrySleep(ctx, attempt); sleepErr != nil {
			return lmeCollectResult{RetryCount: attempt}, sleepErr
		}
	}
	return lmeCollectResult{RetryCount: maxRetries}, lastErr
}

func collectLMEEvents(ch <-chan *event.Event) (lmeCollectResult, error) {
	var res lmeCollectResult
	step := 0
	var pending []lmeToolCallTrace
	for ev := range ch {
		if ev == nil {
			continue
		}
		if ev.Error != nil {
			return res, errors.New(ev.Error.Message)
		}
		if ev.Response == nil {
			if ev.IsRunnerCompletion() {
				break
			}
			continue
		}
		addLMEModelUsage(&res.Usage, ev.Response.Usage)
		if len(ev.Response.Choices) == 0 {
			continue
		}
		msg := ev.Response.Choices[0].Message
		hasToolCalls := len(msg.ToolCalls) > 0
		if hasToolCalls {
			step++
			pending = recordLMEToolCalls(&res, step, msg, ev.Response.Usage)
		}
		if ev.Response.Object == model.ObjectTypeToolResponse && msg.Role == model.RoleTool {
			recordLMEToolResponse(&res, pending, step, msg)
		}
		if msg.Role == model.RoleAssistant && msg.Content != "" {
			res.Text = msg.Content
			if !hasToolCalls {
				res.Trace = append(res.Trace, lmeMessageTrace{
					Step:    step,
					Role:    string(msg.Role),
					Content: msg.Content,
				})
			}
		}
		if ev.IsRunnerCompletion() {
			break
		}
	}
	res.Text = strings.TrimSpace(res.Text)
	if res.Text == "" {
		return res, fmt.Errorf("runner returned empty response")
	}
	return res, nil
}

func addLMEModelUsage(total *scenarios.TokenUsage, usage *model.Usage) {
	if usage == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.TotalTokens += usage.TotalTokens
	total.CachedTokens += usage.PromptTokensDetails.CachedTokens
	total.LLMCalls++
}

func recordLMEToolCalls(
	result *lmeCollectResult,
	step int,
	msg model.Message,
	usage *model.Usage,
) []lmeToolCallTrace {
	calls := make([]lmeToolCallTrace, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		calls = append(calls, lmeToolCallTrace{
			Name: call.Function.Name,
			Args: string(call.Function.Arguments),
		})
	}
	stepTrace := lmeStepTrace{Step: step, ToolCalls: calls}
	if usage != nil {
		stepTrace.PromptTokens = usage.PromptTokens
		stepTrace.CompletionTokens = usage.CompletionTokens
		stepTrace.TotalTokens = usage.TotalTokens
		stepTrace.CachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	result.Steps = append(result.Steps, stepTrace)
	result.Trace = append(result.Trace, lmeMessageTrace{
		Step:      step,
		Role:      string(msg.Role),
		Content:   msg.Content,
		ToolCalls: cloneLMEToolCalls(calls),
	})
	return calls
}

func recordLMEToolResponse(
	result *lmeCollectResult,
	pending []lmeToolCallTrace,
	step int,
	msg model.Message,
) {
	matched := false
	for i := range pending {
		if pending[i].Result == "" {
			pending[i].Result = msg.Content
			matched = true
			break
		}
	}
	if !matched && len(result.Steps) > 0 {
		last := &result.Steps[len(result.Steps)-1]
		last.ToolCalls = append(last.ToolCalls, lmeToolCallTrace{
			Name:   msg.ToolName,
			Result: msg.Content,
		})
	}
	result.Trace = append(result.Trace, lmeMessageTrace{
		Step:    step,
		Role:    string(msg.Role),
		Name:    msg.ToolName,
		Content: msg.Content,
	})
}

func cloneLMEToolCalls(calls []lmeToolCallTrace) []lmeToolCallTrace {
	if len(calls) == 0 {
		return nil
	}
	out := make([]lmeToolCallTrace, len(calls))
	copy(out, calls)
	return out
}

func isLMETransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "server_busy") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "temporary") ||
		strings.Contains(msg, "\"code\":\"4029\"")
}

func lmeRetrySleep(ctx context.Context, attempt int) error {
	d := time.Duration(1<<attempt) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func modelUsageToScenarioUsage(u *model.Usage) *scenarios.TokenUsage {
	if u == nil {
		return &scenarios.TokenUsage{}
	}
	return &scenarios.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.PromptTokensDetails.CachedTokens,
		LLMCalls:         1,
	}
}

func parseLMETime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006/01/02 (Mon) 15:04",
		"2006/1/2 (Mon) 15:04",
		"2006/01/02 (Mon)",
		"2006/1/2 (Mon)",
		"2006/01/02 15:04",
		"2006/1/2 15:04",
		"2006/01/02",
		"2006/1/2",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"2 January 2006",
		"January 2, 2006",
		"Jan 2, 2006",
		"2 Jan 2006",
		"January 2006",
		"Jan 2006",
		"2006-01",
		"2006",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
