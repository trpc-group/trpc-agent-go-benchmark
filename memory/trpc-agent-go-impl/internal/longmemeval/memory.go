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
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/internal/benchruntime"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// Run executes the immutable replay, memory build, QA, and fixed-denominator
// publication stages of the LongMemEval benchmark.
func Run(ctx context.Context, config Config) error {
	cfg, llm, err := buildLMERunConfig(config)
	if err != nil {
		return err
	}
	defer closeLMEEmbeddingCaches()
	instances, _, err := loadLMEInstances(cfg)
	if err != nil {
		return err
	}
	scenarioTypes, err := getLMEScenarios(config.Scenario)
	if err != nil {
		return err
	}
	if lmeNeedsBuildArtifacts(scenarioTypes) {
		cfg, err = prepareLMEInputArtifacts(cfg, instances)
		if err != nil {
			return err
		}
	}
	if err := validateLMEPrerequisites(cfg, scenarioTypes); err != nil {
		return err
	}
	rootDir := filepath.Join(config.OutputDir, "longmemeval")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return fmt.Errorf("create LongMemEval output dir: %w", err)
	}
	for _, scenarioType := range scenarioTypes {
		if scenarioType == scenarios.ScenarioReplay {
			continue
		}
		evaluator, backend, err := newLMEEvaluator(scenarioType, llm, cfg)
		if err != nil {
			return err
		}
		scenarioDir := lmeScenarioDir(rootDir, scenarioType, backend)
		if err := runLMEEvaluator(ctx, evaluator, cfg, instances, backend, scenarioDir); err != nil {
			_ = evaluator.Close()
			return err
		}
		if err := evaluator.Close(); err != nil {
			return fmt.Errorf("close %s evaluator: %w", evaluator.Name(), err)
		}
	}
	if hasLMEReportableScenario(scenarioTypes) {
		if err := writeLMEReports(rootDir, cfg, scenarioTypes); err != nil {
			return err
		}
	}
	if err := writeLMEEmbeddingCacheStats(rootDir); err != nil {
		return err
	}
	return nil
}

func lmeNeedsBuildArtifacts(scenarioTypes []scenarios.ScenarioType) bool {
	for _, scenarioType := range scenarioTypes {
		switch scenarioType {
		case scenarios.ScenarioReplay, scenarios.ScenarioAuto, scenarios.ScenarioMem0OSS:
			return true
		}
	}
	return false
}

func validateLMEPrerequisites(cfg lmeRunConfig, scenarioTypes []scenarios.ScenarioType) error {
	for _, scenarioType := range scenarioTypes {
		if scenarioType == scenarios.ScenarioMem0OSS {
			if cfg.Mem0Host == "" {
				return fmt.Errorf("MEM0_HOST or -mem0-host is required before running LongMemEval mem0_oss")
			}
			if cfg.Mem0PreflightDigest == "" || cfg.Mem0EnvironmentLockDigest == "" ||
				!cfg.Mem0ObservationPromptVerified {
				return fmt.Errorf(
					"MEM0_PREFLIGHT or -mem0-preflight is required and must verify the locked Mem0 OSS service",
				)
			}
			if cfg.Mem0Version == "" || !lmeImmutableRevisionPattern.MatchString(cfg.Mem0Revision) {
				return fmt.Errorf("Mem0 preflight did not provide an immutable runtime identity")
			}
		}
		if scenarioType != scenarios.ScenarioAuto {
			continue
		}
		if strings.TrimSpace(lmeRuntime.PGVectorDSN) == "" {
			return fmt.Errorf(
				"PGVECTOR_DSN or -pgvector-dsn is required before running LongMemEval %s",
				scenarioType,
			)
		}
	}
	return nil
}

func buildLMERunConfig(config Config) (lmeRunConfig, model.Model, error) {
	modelName := strings.TrimSpace(config.ModelName)
	if modelName == "" || config.Model == nil {
		return lmeRunConfig{}, nil, fmt.Errorf("LLM_NAME or -model is required for LongMemEval")
	}
	embedModelName := strings.TrimSpace(config.EmbedModelName)
	if embedModelName == "" {
		embedModelName = "text-embedding-3-small"
	}
	ensureLMEEmbeddingEnv(embedModelName)
	judgeMaxTokens := max(config.JudgeMaxTokens, 1)
	autoExtractionWait := config.AutoExtractionWait
	if autoExtractionWait <= 0 {
		autoExtractionWait = 10 * time.Minute
	}
	replayRoot := strings.TrimSpace(config.ReplayRoot)
	if replayRoot == "" {
		replayRoot = filepath.Join(config.OutputDir, "longmemeval", "replay")
	}
	buildMaxTokens := config.BuildMaxTokens
	if buildMaxTokens <= 0 {
		buildMaxTokens = lmeDefaultBuildMaxTokens
	}
	buildTokenizerModel := strings.TrimSpace(config.BuildTokenizerModel)
	buildTokenizerEncoding := strings.TrimSpace(config.BuildTokenizerEncoding)
	if buildTokenizerModel == "" {
		buildTokenizerModel = embedModelName
		if buildTokenizerEncoding == "" {
			buildTokenizerEncoding = defaultLMEBuildTokenizerEncoding(embedModelName)
		}
	}
	autoMemoryTable := strings.TrimSpace(config.AutoMemoryTable)
	if autoMemoryTable == "" {
		autoMemoryTable = benchruntime.TableNameWithSuffix(
			pgvectorTableAutoBase,
			config.TableSuffix,
		)
	}
	autoUpdatePolicy, err := parseLMEAutoUpdatePolicy(config.AutoUpdatePolicy)
	if err != nil {
		return lmeRunConfig{}, nil, err
	}
	conversationExtraction, err := parseLMEConversationExtraction(config.ConversationExtraction)
	if err != nil {
		return lmeRunConfig{}, nil, err
	}
	cfg := lmeRunConfig{
		ModelName:                    modelName,
		EmbedModelName:               embedModelName,
		LLMEndpointFingerprint:       fingerprintLMEEndpoint(config.LLMBaseURL),
		EmbeddingEndpointFingerprint: fingerprintLMEEndpoint(config.EmbeddingBaseURL),
		DatasetPath:                  strings.TrimSpace(config.DatasetPath),
		ManifestPath:                 strings.TrimSpace(config.ManifestPath),
		ReplayRoot:                   replayRoot,
		BuildMaxTokens:               buildMaxTokens,
		BuildTokenizerModel:          buildTokenizerModel,
		BuildTokenizerEncoding:       buildTokenizerEncoding,
		QuestionTypes:                config.QuestionTypes,
		MaxTasks:                     config.MaxTasks,
		RetrievalTopK:                lmeRetrievalTopK,
		MaxRetries:                   max(config.MaxRetries, 0),
		AnswerMaxTokens:              max(config.AnswerMaxTokens, 1),
		JudgeMaxTokens:               judgeMaxTokens,
		AutoExtractionWait:           autoExtractionWait,
		AutoQAOnly:                   config.AutoQAOnly,
		AutoMemoryTable:              autoMemoryTable,
		AutoUpdatePolicy:             autoUpdatePolicy,
		ConversationExtraction:       string(conversationExtraction),
		EmbeddingCacheEnabled:        config.EmbeddingCacheEnabled,
		TransportRetryEnabled:        config.MaxRetries > 0,
		TransportRetryStrategy:       "fixed prompt, same model, retry transport/rate-limit errors only",
		FullQALog:                    true,
		Mem0Host:                     strings.TrimSpace(config.Mem0Host),
		Mem0APIKeySet:                strings.TrimSpace(config.Mem0APIKey) != "",
		Mem0Version:                  strings.TrimSpace(config.Mem0Version),
		Mem0Revision:                 strings.TrimSpace(config.Mem0Revision),
		Mem0PreflightPath:            strings.TrimSpace(config.Mem0PreflightPath),
		Mem0IngestTimeout:            config.Mem0IngestTimeout,
		Mem0ProxyUsageLog:            strings.TrimSpace(config.Mem0ProxyUsageLog),
		Mem0ProxyRunID:               strings.TrimSpace(config.Mem0ProxyRunID),
		TraceContentMode:             lmeTraceContentMode(strings.ToLower(strings.TrimSpace(config.TraceContentMode))),
		TraceGzip:                    config.TraceGzip,
	}
	if err := completeLMERunConfig(&cfg, config); err != nil {
		return lmeRunConfig{}, nil, err
	}
	lmeRuntime = config
	return cfg, config.Model, nil
}

func defaultLMEBuildTokenizerEncoding(modelName string) string {
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "text-embedding-ada-002", "text-embedding-3-small", "text-embedding-3-large":
		return "cl100k_base"
	default:
		return ""
	}
}

func completeLMERunConfig(cfg *lmeRunConfig, source Config) error {
	if cfg.TraceContentMode == "" {
		cfg.TraceContentMode = lmeTraceContentHash
	}
	if err := validateLMETraceContentMode(cfg.TraceContentMode); err != nil {
		return err
	}
	if cfg.Mem0IngestTimeout <= 0 {
		cfg.Mem0IngestTimeout = cfg.AutoExtractionWait
	}
	if cfg.EmbeddingCacheEnabled {
		cfg.EmbeddingCachePath = strings.TrimSpace(source.EmbeddingCachePath)
	}
	if cfg.EmbeddingCacheEnabled && cfg.EmbeddingCachePath == "" {
		cfg.EmbeddingCachePath = filepath.Join(
			source.OutputDir,
			"longmemeval",
			".cache",
			fmt.Sprintf(
				"embeddings_%s",
				benchruntime.SanitizeCacheFileName(cfg.EmbedModelName),
			),
		)
	}
	if cfg.DatasetPath == "" {
		return fmt.Errorf("LongMemEval dataset path is required")
	}
	if cfg.Mem0ProxyUsageLog != "" && cfg.Mem0ProxyRunID == "" {
		return fmt.Errorf("LongMemEval Mem0 proxy run ID is required when a proxy usage log is configured")
	}
	if err := validateLMEProxyRunID(cfg.Mem0ProxyRunID); err != nil {
		return err
	}
	if hasLMEScenario(source.Scenario, scenarios.ScenarioMem0OSS) {
		if err := completeLMEMem0Preflight(cfg); err != nil {
			return err
		}
	}
	return nil
}

func hasLMEScenario(raw string, target scenarios.ScenarioType) bool {
	for _, part := range strings.Split(raw, ",") {
		if scenarios.ScenarioType(strings.TrimSpace(part)) == target {
			return true
		}
	}
	return false
}

func completeLMEMem0Preflight(cfg *lmeRunConfig) error {
	if cfg == nil || cfg.Mem0PreflightPath == "" {
		return nil
	}
	summary, err := loadLMEMem0Preflight(cfg.Mem0PreflightPath)
	if err != nil {
		return err
	}
	if cfg.Mem0Host != "" && strings.TrimRight(sanitizeEndpoint(cfg.Mem0Host), "/") !=
		strings.TrimRight(summary.ServiceURL, "/") {
		return fmt.Errorf("Mem0 preflight service URL does not match the configured host")
	}
	if cfg.Mem0Version != "" && cfg.Mem0Version != summary.Version {
		return fmt.Errorf(
			"Mem0 preflight version %q does not match configured version %q",
			summary.Version,
			cfg.Mem0Version,
		)
	}
	if cfg.Mem0Revision != "" && !strings.EqualFold(cfg.Mem0Revision, summary.SourceCommit) {
		return fmt.Errorf(
			"Mem0 preflight revision %q does not match configured revision %q",
			summary.SourceCommit,
			cfg.Mem0Revision,
		)
	}
	if cfg.ModelName != summary.LLMModel {
		return fmt.Errorf(
			"Mem0 preflight LLM model %q does not match benchmark model %q",
			summary.LLMModel,
			cfg.ModelName,
		)
	}
	if cfg.EmbedModelName != summary.EmbedModel {
		return fmt.Errorf(
			"Mem0 preflight embedding model %q does not match benchmark model %q",
			summary.EmbedModel,
			cfg.EmbedModelName,
		)
	}
	cfg.Mem0Version = summary.Version
	cfg.Mem0Revision = summary.SourceCommit
	cfg.Mem0PreflightDigest = summary.Digest
	cfg.Mem0EnvironmentLockDigest = summary.EnvironmentLockDigest
	cfg.Mem0RuntimeLLMModel = summary.LLMModel
	cfg.Mem0RuntimeEmbedModel = summary.EmbedModel
	cfg.Mem0ObservationPromptVerified = true
	return nil
}

func parseLMEAutoUpdatePolicy(raw string) (lmeAutoUpdatePolicy, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return lmeAutoUpdatePolicyMergeSimilar, nil
	}
	switch normalized {
	case string(lmeAutoUpdatePolicyMergeSimilar):
		return lmeAutoUpdatePolicyMergeSimilar, nil
	case string(lmeAutoUpdatePolicyPreserveHistory):
		return lmeAutoUpdatePolicyPreserveHistory, nil
	case string(lmeAutoUpdatePolicyAppendOnly):
		return lmeAutoUpdatePolicyAppendOnly, nil
	default:
		return "", fmt.Errorf(
			"invalid LongMemEval auto update policy %q: expected merge_similar, preserve_history, or append_only",
			raw,
		)
	}
}

func ensureLMEEmbeddingEnv(embedModelName string) {
	if os.Getenv("EMBED_MODEL_NAME") == "" && embedModelName != "" {
		_ = os.Setenv("EMBED_MODEL_NAME", embedModelName)
	}
	if os.Getenv("OPENAI_EMBEDDING_API_KEY") != "" {
		return
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		_ = os.Setenv("OPENAI_EMBEDDING_API_KEY", os.Getenv("OPENAI_API_KEY"))
	}
}

func loadLMEInstances(
	cfg lmeRunConfig,
) ([]*dataset.LongMemEvalInstance, *dataset.LongMemEvalManifest, error) {
	instances, err := dataset.LoadLongMemEval(cfg.DatasetPath)
	if err != nil {
		return nil, nil, err
	}
	instances = dataset.FilterLongMemEval(instances, cfg.QuestionTypes)
	var manifest *dataset.LongMemEvalManifest
	if cfg.ManifestPath != "" {
		manifest, err = dataset.LoadLongMemEvalManifest(cfg.ManifestPath)
		if err != nil {
			return nil, nil, err
		}
		instances, err = dataset.FilterLongMemEvalByManifest(instances, manifest)
		if err != nil {
			return nil, nil, err
		}
		if err := validateLMEManifestTaskLimit(cfg.MaxTasks, len(instances)); err != nil {
			return nil, nil, err
		}
	}
	if len(instances) == 0 {
		return nil, nil, fmt.Errorf("no LongMemEval cases remain after filtering")
	}
	if manifest == nil && cfg.MaxTasks > 0 && cfg.MaxTasks < len(instances) {
		instances = instances[:cfg.MaxTasks]
	}
	log.Printf("Loaded %d LongMemEval cases from %s", len(instances), cfg.DatasetPath)
	log.Printf("Question types: %s", strings.Join(dataset.LongMemEvalQuestionTypes(instances), ", "))
	return instances, manifest, nil
}

func validateLMEManifestTaskLimit(maxTasks, manifestTasks int) error {
	if maxTasks == 0 || maxTasks == manifestTasks {
		return nil
	}
	return fmt.Errorf(
		"LongMemEval manifest selects %d cases but max-tasks is %d; "+
			"a manifest is authoritative, so use max-tasks=0 or a separate manifest",
		manifestTasks,
		maxTasks,
	)
}

func getLMEScenarios(raw string) ([]scenarios.ScenarioType, error) {
	allowed := map[string]scenarios.ScenarioType{
		"long_context": scenarios.ScenarioLongContext,
		"replay":       scenarios.ScenarioReplay,
		"auto":         scenarios.ScenarioAuto,
		"mem0_oss":     scenarios.ScenarioMem0OSS,
	}
	seen := make(map[string]struct{})
	var out []scenarios.ScenarioType
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		scenarioType, ok := allowed[part]
		if !ok {
			return nil, fmt.Errorf(
				"longmemeval supports long_context, replay, auto, and mem0_oss; got %q",
				part,
			)
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, scenarioType)
	}
	if len(out) == 0 {
		return nil, errors.New("no longmemeval scenarios selected")
	}
	return out, nil
}

func newLMEEvaluator(
	scenarioType scenarios.ScenarioType,
	llm model.Model,
	cfg lmeRunConfig,
) (lmeEvaluator, string, error) {
	switch scenarioType {
	case scenarios.ScenarioLongContext:
		return &lmeLongContextEvaluator{llm: llm, cfg: cfg}, "", nil
	case scenarios.ScenarioAuto:
		cost := newLMECostTracker()
		trace, err := newLMETraceManager(cfg.TraceContentMode, cfg.TraceGzip)
		if err != nil {
			return nil, "", err
		}
		memoryBuildLLM := newLMETrackedModel(
			llm,
			cost,
			lmeLLMPhaseMemoryBuild,
		)
		memOpts := memoryServiceOptions{
			enableExtractor:        true,
			extractorModel:         memoryBuildLLM,
			memoryJobTimeout:       cfg.AutoExtractionWait,
			tableName:              cfg.AutoMemoryTable,
			vectorTopK:             lmeRetrievalTopK,
			autoUpdatePolicy:       cfg.AutoUpdatePolicy,
			conversationExtraction: lmeConversationExtraction(cfg.ConversationExtraction),
		}
		cfg.AutoMemoryTable = memOpts.tableName
		memSvc, err := createAutoMemoryService(memOpts)
		if err != nil {
			return nil, "", err
		}
		return &lmeAutoEvaluator{
			judgeLLM: newLMETrackedModel(llm, cost, lmeLLMPhaseJudge),
			qaLLM:    newLMETrackedModel(llm, cost, lmeLLMPhaseQA),
			mem:      memSvc,
			cfg:      cfg,
			cost:     cost,
			trace:    trace,
		}, "pgvector", nil
	case scenarios.ScenarioMem0OSS:
		evaluator, err := newLMEMem0OSSEvaluator(llm, cfg)
		if err != nil {
			return nil, "", err
		}
		return evaluator, "mem0_oss", nil
	default:
		return nil, "", fmt.Errorf("unsupported LongMemEval scenario %s", scenarioType)
	}
}

func runLMEEvaluator(
	ctx context.Context,
	evaluator lmeEvaluator,
	cfg lmeRunConfig,
	instances []*dataset.LongMemEvalInstance,
	backend string,
	outputDir string,
) error {
	if evaluator.Name() == "auto" {
		if table := strings.TrimSpace(lmeRuntime.AutoMemoryTable); table != "" {
			cfg.AutoMemoryTable = table
		}
	}
	result, checkpointCost, startTime, err := prepareLMEEvaluation(
		ctx,
		evaluator,
		cfg,
		instances,
		backend,
		outputDir,
	)
	if err != nil {
		return err
	}
	for i, inst := range instances {
		if err := ctx.Err(); err != nil {
			return stopLMEEvaluation(outputDir, evaluator, result, checkpointCost, startTime, err)
		}
		if isLMECheckpointCompleted(result.Cases[i]) {
			log.Printf("[%d/%d] %s skipped from checkpoint", i+1, len(instances), inst.QuestionID)
			continue
		}
		log.Printf(
			"[%d/%d] LongMemEval %s %s (%s)",
			i+1,
			len(instances),
			evaluator.Name(),
			inst.QuestionID,
			inst.QuestionType,
		)
		caseResult, evaluateErr := evaluator.Evaluate(ctx, inst)
		if err := persistLMECaseEvaluation(
			outputDir,
			evaluator,
			result,
			checkpointCost,
			startTime,
			len(instances),
			inst,
			caseResult,
			evaluateErr,
		); err != nil {
			return err
		}
	}
	return finishLMEEvaluation(outputDir, evaluator, result, checkpointCost, startTime, instances)
}

func prepareLMEEvaluation(
	ctx context.Context,
	evaluator lmeEvaluator,
	cfg lmeRunConfig,
	instances []*dataset.LongMemEvalInstance,
	backend string,
	outputDir string,
) (*lmeRunResult, *lmeCostReport, time.Time, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("create scenario output dir: %w", err)
	}
	if traced, ok := evaluator.(interface {
		setLMETraceRoot(string) error
	}); ok {
		if err := traced.setLMETraceRoot(outputDir); err != nil {
			return nil, nil, time.Time{}, err
		}
	}
	runManifest, err := ensureLMERunManifest(
		ctx,
		outputDir,
		newLMERunManifestRequest(evaluator.Name(), backend, cfg, instances),
		lmeRuntime.Resume,
	)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("prepare LongMemEval run provenance: %w", err)
	}
	result := newLMERunResult(evaluator.Name(), backend, cfg, len(instances))
	bindLMERunManifest(result, runManifest)
	startTime := time.Now()
	if lmeRuntime.Resume {
		var checkpointCost *lmeCostReport
		result, checkpointCost, err = resumeLMEEvaluation(
			outputDir,
			evaluator,
			backend,
			cfg,
			runManifest,
			result,
		)
		if err != nil {
			return nil, nil, time.Time{}, err
		}
		if err := prepareLMECaseRecords(result, instances); err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("prepare LongMemEval fixed denominator: %w", err)
		}
		aggregateLMERunResult(result, time.Since(startTime), len(instances))
		if err := saveLMECheckpoint(outputDir, result); err != nil {
			return nil, nil, time.Time{}, err
		}
		return result, checkpointCost, startTime, nil
	}
	if err := prepareLMECaseRecords(result, instances); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("prepare LongMemEval fixed denominator: %w", err)
	}
	aggregateLMERunResult(result, time.Since(startTime), len(instances))
	if err := saveLMECheckpoint(outputDir, result); err != nil {
		return nil, nil, time.Time{}, err
	}
	return result, nil, startTime, nil
}

func resumeLMEEvaluation(
	outputDir string,
	evaluator lmeEvaluator,
	backend string,
	cfg lmeRunConfig,
	runManifest *lmeRunManifest,
	initial *lmeRunResult,
) (*lmeRunResult, *lmeCostReport, error) {
	checkpoint, err := loadLMERunResult(outputDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("load LongMemEval checkpoint: %w", err)
	}
	if checkpoint == nil {
		return initial, nil, nil
	}
	if err := verifyLMECheckpointManifest(checkpoint, runManifest); err != nil {
		return nil, nil, err
	}
	if err := validateLMEResumeRetrievalLimit(checkpoint, backend); err != nil {
		return nil, nil, err
	}
	if err := validateLMEResumeBuildInputs(checkpoint, cfg); err != nil {
		return nil, nil, err
	}
	var checkpointCost *lmeCostReport
	if checkpoint.Cost != nil {
		checkpointCost = checkpoint.Cost
	} else if processed := countLMEProcessedCases(checkpoint.Cases); processed > 0 {
		checkpointCost = partialLMECostReport(fmt.Sprintf(
			"resumed checkpoint has %d completed case(s) without cost metadata",
			processed,
		))
	}
	log.Printf("Resumed %s checkpoint", evaluator.Name())
	return checkpoint, checkpointCost, nil
}

func persistLMECaseEvaluation(
	outputDir string,
	evaluator lmeEvaluator,
	result *lmeRunResult,
	checkpointCost *lmeCostReport,
	startTime time.Time,
	totalCases int,
	inst *dataset.LongMemEvalInstance,
	caseResult *lmeCaseResult,
	evaluateErr error,
) error {
	if evaluateErr != nil {
		if caseResult == nil {
			caseResult = newLMEFailedCase(inst, evaluateErr)
		} else {
			caseResult.Status = lmeCaseStatusFailed
			if strings.TrimSpace(caseResult.Error) == "" {
				caseResult.Error = sanitizeLMEResultText(evaluateErr.Error(), 2048)
			}
		}
		result.LastError = &lmeRunError{
			QuestionID: inst.QuestionID,
			Scenario:   evaluator.Name(),
			Message:    sanitizeLMEResultText(evaluateErr.Error(), 2048),
		}
	} else {
		result.LastError = nil
	}
	if err := replaceLMECaseRecord(result, caseResult); err != nil {
		return err
	}
	aggregateLMERunResult(result, time.Since(startTime), totalCases)
	setLMERunMetadata(result, evaluator)
	setLMERunCost(result, evaluator, checkpointCost)
	if err := saveLMECaseLog(outputDir, caseResult); err != nil {
		return err
	}
	if err := saveLMECheckpoint(outputDir, result); err != nil {
		return err
	}
	if evaluateErr != nil {
		log.Printf(
			"LongMemEval case %s failed and remains in the fixed denominator: %v",
			inst.QuestionID,
			evaluateErr,
		)
	}
	return nil
}

func stopLMEEvaluation(
	outputDir string,
	evaluator lmeEvaluator,
	result *lmeRunResult,
	checkpointCost *lmeCostReport,
	startTime time.Time,
	cause error,
) error {
	result.LastError = &lmeRunError{Scenario: evaluator.Name(), Message: cause.Error()}
	result.Summary.TotalTimeMs = time.Since(startTime).Milliseconds()
	setLMERunMetadata(result, evaluator)
	setLMERunCost(result, evaluator, checkpointCost)
	if err := saveLMECheckpoint(outputDir, result); err != nil {
		return fmt.Errorf("%s stopped: %v; save checkpoint: %w", evaluator.Name(), cause, err)
	}
	return fmt.Errorf("%s stopped: %w", evaluator.Name(), cause)
}

func finishLMEEvaluation(
	outputDir string,
	evaluator lmeEvaluator,
	result *lmeRunResult,
	checkpointCost *lmeCostReport,
	startTime time.Time,
	instances []*dataset.LongMemEvalInstance,
) error {
	aggregateLMERunResult(result, time.Since(startTime), len(instances))
	setLMERunMetadata(result, evaluator)
	setLMERunCost(result, evaluator, checkpointCost)
	if err := saveLMECheckpoint(outputDir, result); err != nil {
		return err
	}
	if err := finalizeLMERunResult(outputDir, result, lmeExpectedCaseIDs(instances)); err != nil {
		if saveErr := saveLMECheckpoint(outputDir, result); saveErr != nil {
			return fmt.Errorf("finalize LongMemEval result: %v; save validation checkpoint: %w", err, saveErr)
		}
		return err
	}
	if err := saveLMECheckpoint(outputDir, result); err != nil {
		return err
	}
	printLMESummary(result)
	return nil
}

func validateLMEResumeBuildInputs(checkpoint *lmeRunResult, cfg lmeRunConfig) error {
	if checkpoint == nil || checkpoint.Metadata == nil {
		return fmt.Errorf("resume checkpoint is missing metadata")
	}
	previous := checkpoint.Metadata.Config
	if previous.ReplayDigest != cfg.ReplayDigest ||
		previous.BuildPlanDigest != cfg.BuildPlanDigest {
		return fmt.Errorf(
			"resume checkpoint build input mismatch: previous replay=%s plan=%s, current replay=%s plan=%s",
			previous.ReplayDigest,
			previous.BuildPlanDigest,
			cfg.ReplayDigest,
			cfg.BuildPlanDigest,
		)
	}
	return nil
}

func validateLMEResumeRetrievalLimit(
	checkpoint *lmeRunResult,
	backend string,
) error {
	if backend == "" {
		return nil
	}
	if checkpoint == nil || checkpoint.Metadata == nil {
		return fmt.Errorf("resume checkpoint is missing LongMemEval metadata")
	}
	if checkpoint.Metadata.Config.RetrievalTopK != lmeRetrievalTopK {
		return fmt.Errorf(
			"resume checkpoint retrieval top-k is %d, want %d",
			checkpoint.Metadata.Config.RetrievalTopK,
			lmeRetrievalTopK,
		)
	}
	limit, ok := checkpoint.Metadata.RetrievalLimits[backend]
	if !ok {
		return fmt.Errorf(
			"resume checkpoint is missing effective retrieval limit for backend %q",
			backend,
		)
	}
	if limit.RequestedTopK != lmeRetrievalTopK ||
		limit.EffectiveTopK != lmeRetrievalTopK {
		return fmt.Errorf(
			"resume checkpoint retrieval limit is incompatible for backend %q: "+
				"requested=%d effective=%d, want requested=%d effective=%d",
			backend,
			limit.RequestedTopK,
			limit.EffectiveTopK,
			lmeRetrievalTopK,
			lmeRetrievalTopK,
		)
	}
	return nil
}
