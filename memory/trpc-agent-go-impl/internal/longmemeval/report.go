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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
)

func saveLMECaseLog(outputDir string, cr *lmeCaseResult) error {
	if cr == nil {
		return fmt.Errorf("write LongMemEval case log: nil case")
	}
	path := filepath.Join(outputDir, safeLMECaseLogName(cr.QuestionID)+".log")
	var b strings.Builder
	fmt.Fprintf(&b, "QuestionID: %s\nQuestionType: %s\n", cr.QuestionID, cr.QuestionType)
	fmt.Fprintf(&b, "QuestionDate: %s\nCorrect: %v\n", cr.QuestionDate, cr.Correct)
	fmt.Fprintf(&b, "Status: %s\n", cr.Status)
	fmt.Fprintf(&b, "Metrics: accuracy=%.4f f1=%.4f bleu=%.4f rouge_l=%.4f\n",
		cr.Metrics.Accuracy, cr.Metrics.F1, cr.Metrics.BLEU, cr.Metrics.ROUGEL)
	fmt.Fprintf(&b, "\nQuestion:\n%s\n\nExpected:\n%s\n\nPredicted:\n%s\n",
		cr.Question, cr.Expected, cr.Predicted)
	if len(cr.ToolSteps) > 0 {
		fmt.Fprintf(&b, "\nTool Steps:\n")
		for _, step := range cr.ToolSteps {
			fmt.Fprintf(&b, "Step %d tokens=%d\n", step.Step, step.TotalTokens)
			for _, tc := range step.ToolCalls {
				result := tc.Result
				if len(cr.QATrace) == 0 {
					result = truncateLME(result, 600)
				}
				fmt.Fprintf(&b, "- %s args=%s result=%s\n", tc.Name, tc.Args, result)
			}
		}
	}
	if len(cr.QATrace) > 0 {
		fmt.Fprintf(&b, "\nQA Conversation:\n")
		for i, msg := range cr.QATrace {
			fmt.Fprintf(&b, "\n[%d] role=%s", i+1, msg.Role)
			if msg.Name != "" {
				fmt.Fprintf(&b, " name=%s", msg.Name)
			}
			if msg.Step > 0 {
				fmt.Fprintf(&b, " step=%d", msg.Step)
			}
			fmt.Fprintf(&b, "\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&b, "tool_call name=%s args=%s\n", tc.Name, tc.Args)
			}
			if msg.Content != "" {
				fmt.Fprintf(&b, "%s\n", msg.Content)
			}
		}
	}
	if cr.Error != "" {
		fmt.Fprintf(&b, "\nError:\n%s\n", sanitizeLMEResultText(cr.Error, 2048))
	}
	if err := writeLMEAtomicFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("write case log %s: %w", path, err)
	}
	return nil
}

func safeLMECaseLogName(questionID string) string {
	var b strings.Builder
	for _, r := range questionID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_-")
	if name != "" && name != "." && name != ".." {
		return name
	}
	digest := sha256.Sum256([]byte(questionID))
	return "case-" + hex.EncodeToString(digest[:6])
}

func printLMESummary(result *lmeRunResult) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 72))
	fmt.Printf("LongMemEval Memory Results - %s\n", result.Metadata.Scenario)
	fmt.Println(strings.Repeat("=", 72))
	fmt.Printf("Cases: %d/%d\n", result.Summary.CompletedCases, result.Summary.TotalCases)
	fmt.Printf("Accuracy: %.4f | Task-Avg Accuracy: %.4f\n",
		result.Summary.Overall.Accuracy, result.Summary.TaskAveragedAccuracy)
	fmt.Printf("F1/BLEU/ROUGE-L: %.4f / %.4f / %.4f\n",
		result.Summary.Overall.F1, result.Summary.Overall.BLEU, result.Summary.Overall.ROUGEL)
	fmt.Printf("Tokens prompt/completion/total: %d / %d / %d\n",
		result.Summary.TotalPromptTokens,
		result.Summary.TotalCompletionTokens,
		result.Summary.TotalTokens)
	fmt.Println(strings.Repeat("=", 72))
}

func writeLMEReports(
	rootDir string,
	cfg lmeRunConfig,
	scenarioTypes []scenarios.ScenarioType,
) error {
	results := make([]*lmeRunResult, 0, len(scenarioTypes)+1)
	seen := make(map[scenarios.ScenarioType]struct{}, len(scenarioTypes))
	if !slices.Contains(scenarioTypes, scenarios.ScenarioLongContext) {
		path := filepath.Join(
			lmeScenarioDir(rootDir, scenarios.ScenarioLongContext, ""),
			"results.json",
		)
		if _, err := os.Stat(path); err == nil {
			result, err := readLMEOfficialRunResult(path)
			if err != nil {
				return err
			}
			results = append(results, result)
			seen[scenarios.ScenarioLongContext] = struct{}{}
		}
	}
	for _, scenarioType := range scenarioTypes {
		if scenarioType == scenarios.ScenarioReplay {
			continue
		}
		if _, ok := seen[scenarioType]; ok {
			continue
		}
		backend := ""
		if scenarioType == scenarios.ScenarioAuto {
			backend = "pgvector"
		}
		if scenarioType == scenarios.ScenarioMem0OSS {
			backend = "mem0_oss"
		}
		path := filepath.Join(lmeScenarioDir(rootDir, scenarioType, backend), "results.json")
		result, err := readLMEOfficialRunResult(path)
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	if err := validateLMEReportCompatibility(results); err != nil {
		return err
	}
	en := renderLMEReport(results, cfg, false)
	zh := renderLMEReport(results, cfg, true)
	enName := "REPORT.md"
	zhName := "REPORT.zh_CN.md"
	if err := writeLMEAtomicFile(filepath.Join(rootDir, enName), []byte(en), 0644); err != nil {
		return err
	}
	if err := writeLMEAtomicFile(filepath.Join(rootDir, zhName), []byte(zh), 0644); err != nil {
		return err
	}
	return nil
}

func validateLMEReportCompatibility(results []*lmeRunResult) error {
	if len(results) == 0 {
		return nil
	}
	comparisonDigest := ""
	for _, result := range results {
		if result == nil || result.Metadata == nil || result.Publication == nil {
			return fmt.Errorf("LongMemEval report input lacks maintained run identity")
		}
		if result.Metadata.Config.RetrievalTopK != lmeRetrievalTopK {
			return fmt.Errorf("LongMemEval report input does not use fixed top-k %d", lmeRetrievalTopK)
		}
		digest := result.Publication.RunManifest.ComparisonDigest
		if !strings.HasPrefix(digest, lmeDigestAlgorithm+":") {
			return fmt.Errorf("LongMemEval report input lacks a comparison digest")
		}
		if comparisonDigest == "" {
			comparisonDigest = digest
			continue
		}
		if digest != comparisonDigest {
			return fmt.Errorf(
				"LongMemEval report inputs mix protocol, top-k, or immutable artifact digests",
			)
		}
	}
	return nil
}

func hasLMEReportableScenario(scenarioTypes []scenarios.ScenarioType) bool {
	for _, scenarioType := range scenarioTypes {
		switch scenarioType {
		case scenarios.ScenarioReplay:
			continue
		default:
			return true
		}
	}
	return false
}

func readLMEOfficialRunResult(path string) (*lmeRunResult, error) {
	data, err := readLMEBoundedResultFile(path)
	if err != nil {
		return nil, fmt.Errorf("read result %s: %w", path, err)
	}
	if blockers := validateRawLMECostSchema(data); len(blockers) > 0 {
		return nil, fmt.Errorf(
			"reject LongMemEval report input %s: %w",
			path,
			&lmeEligibilityError{Blockers: blockers},
		)
	}
	if blockers := validateRawLMEResultGovernance(data); len(blockers) > 0 {
		return nil, fmt.Errorf(
			"reject LongMemEval report input %s: %w",
			path,
			&lmeEligibilityError{Blockers: blockers},
		)
	}
	var result lmeRunResult
	if err := lmeDecodeStrict(data, &result); err != nil {
		return nil, fmt.Errorf("parse result %s: %w", path, err)
	}
	if err := validatePublishedLMEResult(path, &result); err != nil {
		return nil, fmt.Errorf("reject LongMemEval report input %s: %w", path, err)
	}
	return &result, nil
}

func validateRawLMEResultGovernance(data []byte) []string {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return []string{"result JSON is invalid"}
	}
	metadata, _ := root["metadata"].(map[string]any)
	if recovered, _ := root["recovered_from_logs"].(bool); recovered {
		return []string{"log-recovered result is diagnostic only"}
	}
	if recovered, _ := metadata["recovered_from_logs"].(bool); recovered {
		return []string{"log-recovered result is diagnostic only"}
	}
	if diagnostic, _ := metadata["diagnostic"].(bool); diagnostic {
		return []string{"diagnostic result cannot be a maintained baseline"}
	}
	return nil
}

func renderLMEReport(results []*lmeRunResult, cfg lmeRunConfig, zh bool) string {
	var b strings.Builder
	if zh {
		b.WriteString("# LongMemEval Memory Benchmark 报告\n\n")
		fmt.Fprintf(&b, "固定 manifest 评测；模型：`%s`，Embedding：`%s`。\n\n",
			cfg.ModelName, cfg.EmbedModelName)
		b.WriteString("## 总体结果\n\n")
		b.WriteString("| 场景 | 后端 | 成功/总数 | 失败 | Judge 失败 | Accuracy | F1 | BLEU | ROUGE-L | Prompt/QA | Calls/QA |\n")
	} else {
		b.WriteString("# LongMemEval Memory Benchmark Report\n\n")
		fmt.Fprintf(&b, "Fixed-manifest evaluation; model: `%s`; embedding: `%s`.\n\n",
			cfg.ModelName, cfg.EmbedModelName)
		b.WriteString("## Overall Results\n\n")
		b.WriteString("| Scenario | Backend | Success/Total | Failed | Judge Failed | Accuracy | F1 | BLEU | ROUGE-L | Prompt/QA | Calls/QA |\n")
	}
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, result := range results {
		backend := result.Metadata.MemoryBackend
		if backend == "" {
			backend = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %d | %d | %.4f | %.4f | %.4f | %.4f | %.0f | %.2f |\n",
			result.Metadata.Scenario,
			backend,
			result.Summary.SuccessfulCases,
			result.Summary.TotalCases,
			result.Summary.FailedCases,
			result.Summary.JudgeFailedCases,
			result.Summary.Overall.Accuracy,
			result.Summary.Overall.F1,
			result.Summary.Overall.BLEU,
			result.Summary.Overall.ROUGEL,
			result.Summary.AvgPromptTokensPerQA,
			result.Summary.AvgLLMCallsPerQA,
		)
	}
	appendLMEPublicationReport(&b, results, zh)
	appendLMECostReport(&b, results, zh)
	if zh {
		b.WriteString("\n## 按问题类型\n\n")
	} else {
		b.WriteString("\n## By Question Type\n\n")
	}
	for _, result := range results {
		fmt.Fprintf(&b, "### %s\n\n", result.Metadata.Scenario)
		b.WriteString("| Type | Count | Accuracy | F1 | BLEU | ROUGE-L |\n")
		b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: |\n")
		types := make([]string, 0, len(result.ByType))
		for t := range result.ByType {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, t := range types {
			m := result.ByType[t]
			fmt.Fprintf(&b, "| %s | %d | %.4f | %.4f | %.4f | %.4f |\n",
				t, m.Count, m.Metrics.Accuracy, m.Metrics.F1, m.Metrics.BLEU, m.Metrics.ROUGEL)
		}
		b.WriteString("\n")
	}
	if zh {
		b.WriteString("## 检索指标\n\n")
	} else {
		b.WriteString("## Retrieval Metrics\n\n")
	}
	if zh {
		b.WriteString("## 公平性说明\n\n")
		b.WriteString("- 官方 yes/no judge accuracy 是主指标；F1/BLEU/ROUGE 是确定性辅助指标。\n")
		b.WriteString("- judge 要求严格输出 yes/no；若修复重试后仍不是有效标签，则该样本记录 judge_error 并计为 incorrect，避免阻断整轮评测。\n")
		b.WriteString("- 固定 manifest 是分母；失败与 judge_error 样本保留在结果中并按错误计分。\n")
		b.WriteString("- `checkpoint.json` 只用于恢复；报告只接受完成资格校验后原子发布的 `results.json`。\n")
	} else {
		b.WriteString("## Fairness Notes\n\n")
		b.WriteString("- Official yes/no judge accuracy is the primary metric; F1/BLEU/ROUGE are deterministic auxiliary metrics.\n")
		b.WriteString("- Judge output must be exact yes/no; invalid output after a repair retry is recorded as judge_error and scored incorrect to avoid blocking the full run.\n")
		b.WriteString("- The fixed manifest is the denominator; failed and judge_error cases remain visible and score as incorrect.\n")
		b.WriteString("- `checkpoint.json` is resume state only; reports accept only atomically published `results.json` files that pass eligibility validation.\n")
	}
	return b.String()
}

func appendLMEPublicationReport(
	b *strings.Builder,
	results []*lmeRunResult,
	zh bool,
) {
	if zh {
		b.WriteString("\n## 正式结果资格与失败样本\n\n")
	} else {
		b.WriteString("\n## Maintained Result Eligibility And Bad Cases\n\n")
	}
	b.WriteString("| Scenario | Classification | Eligible | Run Compatibility | Bad Cases |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, result := range results {
		publication := result.Publication
		badCaseName := publication.Artifacts["bad_cases_en"].Path
		badCasePath := filepath.ToSlash(filepath.Join(
			lmeReportScenarioPath(result),
			badCaseName,
		))
		fmt.Fprintf(
			b,
			"| %s | %s | %t | `%s` | [bad cases](%s) |\n",
			result.Metadata.Scenario,
			publication.Classification,
			publication.Eligible,
			publication.RunManifest.CompatibilityDigest,
			badCasePath,
		)
	}
}

func lmeReportScenarioPath(result *lmeRunResult) string {
	if result == nil || result.Metadata == nil {
		return ""
	}
	scenario := scenarios.ScenarioType(result.Metadata.Scenario)
	return filepath.Base(lmeScenarioDir(".", scenario, result.Metadata.MemoryBackend))
}

func appendLMECostReport(
	b *strings.Builder,
	results []*lmeRunResult,
	zh bool,
) {
	hasCost := false
	for _, result := range results {
		if result != nil && result.Cost != nil {
			hasCost = true
			break
		}
	}
	if !hasCost {
		return
	}
	if zh {
		b.WriteString("\n## 模型调用成本总览\n\n")
	} else {
		b.WriteString("\n## Model Call Cost Summary\n\n")
	}
	b.WriteString("| Scenario | LLM Calls | LLM Tokens | Embedding Calls | Embedding Requests | Embedding Cache Hits | Embedding Tokens | Note |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, result := range results {
		cost := result.Cost
		if cost == nil {
			cost = newLMECostTracker().snapshot()
		}
		note := ""
		if cost.Partial {
			note = cost.PartialReason
		}
		fmt.Fprintf(
			b,
			"| %s | %d | %s | %d | %d | %d | %s | %s |\n",
			result.Metadata.Scenario,
			cost.LLM.Total.Calls,
			lmeCostTokensLabel(cost.LLM.Total),
			cost.Embedding.Total.Calls,
			cost.Embedding.Total.Requests,
			cost.Embedding.Total.CacheHits,
			lmeCostTokensLabel(cost.Embedding.Total),
			note,
		)
	}
	if zh {
		b.WriteString("\n## 分阶段模型成本\n\n")
	} else {
		b.WriteString("\n## Model Cost By Phase\n\n")
	}
	b.WriteString("| Scenario | Modality | Phase | Calls | Requests | Cache Hits | Prompt | Completion | Total | Cached | Tokens Known |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, result := range results {
		if result.Cost == nil {
			continue
		}
		appendLMECostPhaseRow(b, result, "llm", "memory_build", result.Cost.LLM.MemoryBuild)
		appendLMECostPhaseRow(b, result, "llm", "qa", result.Cost.LLM.QA)
		appendLMECostPhaseRow(b, result, "llm", "judge", result.Cost.LLM.Judge)
		appendLMECostPhaseRow(b, result, "embedding", "memory_build", result.Cost.Embedding.MemoryBuild)
		appendLMECostPhaseRow(b, result, "embedding", "qa_retrieval", result.Cost.Embedding.QARetrieval)
	}
	b.WriteString("\n")
}

func appendLMECostPhaseRow(
	b *strings.Builder,
	result *lmeRunResult,
	modality string,
	phase string,
	bucket lmeCostBucket,
) {
	fmt.Fprintf(
		b,
		"| %s | %s | %s | %d | %d | %d | %d | %d | %d | %d | %t |\n",
		result.Metadata.Scenario,
		modality,
		phase,
		bucket.Calls,
		bucket.Requests,
		bucket.CacheHits,
		bucket.PromptTokens,
		bucket.CompletionTokens,
		bucket.TotalTokens,
		bucket.CachedTokens,
		bucket.TokensKnown,
	)
}

func lmeCostTokensLabel(bucket lmeCostBucket) string {
	value := fmt.Sprintf("%d", bucket.TotalTokens)
	if !bucket.TokensKnown {
		return value + "?"
	}
	return value
}

// Generated result artifacts contain only sanitized, publication-safe data.
type lmeGeneratedResultArtifact struct {
	key      string
	baseName string
	data     []byte
}

func buildLMEGeneratedResultArtifacts(
	result *lmeRunResult,
	publication *lmePublication,
) ([]lmeGeneratedResultArtifact, error) {
	badCases := buildLMEBadCaseArtifact(result, publication)
	badCasesJSON, err := marshalLMEJSON(badCases)
	if err != nil {
		return nil, fmt.Errorf("marshal LongMemEval bad cases: %w", err)
	}
	aggregateJSON, err := marshalLMEJSON(buildLMEMachineAggregate(result, publication))
	if err != nil {
		return nil, fmt.Errorf("marshal LongMemEval aggregate: %w", err)
	}
	return []lmeGeneratedResultArtifact{
		{key: "bad_cases", baseName: lmeBadCasesJSONFileName, data: badCasesJSON},
		{key: "bad_cases_en", baseName: lmeBadCasesEnglishFileName, data: []byte(renderLMEBadCases(badCases, false))},
		{key: "bad_cases_zh_cn", baseName: lmeBadCasesChineseFileName, data: []byte(renderLMEBadCases(badCases, true))},
		{key: "aggregate", baseName: lmeAggregateFileName, data: aggregateJSON},
	}, nil
}

func buildLMEMachineAggregate(
	result *lmeRunResult,
	publication *lmePublication,
) *lmeMachineAggregate {
	aggregate := &lmeMachineAggregate{
		SchemaVersion:    lmeResultSchemaVersion,
		Classification:   lmeResultClassMaintained,
		RunCompatibility: publication.RunManifest.CompatibilityDigest,
		ComparisonDigest: publication.RunManifest.ComparisonDigest,
		Denominator:      publication.FixedDenominator,
		Summary:          result.Summary,
		ByType:           result.ByType,
		Cases:            make([]lmeAggregateCase, 0, len(result.Cases)),
	}
	if result.Metadata != nil {
		aggregate.Scenario = result.Metadata.Scenario
		aggregate.Backend = result.Metadata.MemoryBackend
	}
	for _, record := range result.Cases {
		if record == nil {
			continue
		}
		aggregate.Cases = append(aggregate.Cases, lmeAggregateCase{
			QuestionID:         record.QuestionID,
			QuestionType:       record.QuestionType,
			Status:             record.Status,
			Correct:            record.Correct,
			FailureStage:       lmeCaseFailureStage(record),
			BuildObservability: string(record.BuildObservability),
			Error:              sanitizeLMEResultText(record.Error, 2048),
			JudgeError:         sanitizeLMEResultText(record.JudgeError, 2048),
		})
	}
	return aggregate
}

func buildLMEBadCaseArtifact(
	result *lmeRunResult,
	publication *lmePublication,
) *lmeBadCaseArtifact {
	artifact := &lmeBadCaseArtifact{
		SchemaVersion:    lmeResultSchemaVersion,
		Classification:   lmeResultClassMaintained,
		RunCompatibility: publication.RunManifest.CompatibilityDigest,
		ComparisonDigest: publication.RunManifest.ComparisonDigest,
		Denominator:      publication.FixedDenominator,
		Cases:            make([]lmeBadCase, 0),
	}
	if result.Metadata != nil {
		artifact.Scenario = result.Metadata.Scenario
		artifact.Backend = result.Metadata.MemoryBackend
	}
	for _, record := range result.Cases {
		if record == nil || (record.Correct && record.Status == lmeCaseStatusSucceeded) {
			continue
		}
		artifact.Cases = append(artifact.Cases, lmeBadCase{
			QuestionID:         record.QuestionID,
			QuestionType:       record.QuestionType,
			Status:             record.Status,
			Correct:            record.Correct,
			FailureStage:       lmeCaseFailureStage(record),
			BuildObservability: string(record.BuildObservability),
			Error:              sanitizeLMEResultText(record.Error, 2048),
			JudgeError:         sanitizeLMEResultText(record.JudgeError, 2048),
			Expected:           sanitizeLMEResultText(record.Expected, 4096),
			Predicted:          sanitizeLMEResultText(record.Predicted, 4096),
		})
	}
	return artifact
}

func lmeCaseFailureStage(record *lmeCaseResult) string {
	if record == nil {
		return "missing"
	}
	if record.FailureStage != "" {
		return string(record.FailureStage)
	}
	switch record.Status {
	case lmeCaseStatusJudgeFailed:
		return "judge_error"
	case lmeCaseStatusFailed:
		return "evaluation_error"
	case lmeCaseStatusPending, lmeCaseStatusMissing:
		return "missing"
	case lmeCaseStatusSucceeded:
		if record.Correct {
			return "success"
		}
		return "answer_generation_miss"
	default:
		return "unknown"
	}
}

func renderLMEBadCases(artifact *lmeBadCaseArtifact, zh bool) string {
	var b strings.Builder
	if zh {
		b.WriteString("# LongMemEval 失败样本\n\n")
		fmt.Fprintf(&b, "场景：`%s`；后端：`%s`；固定分母：%d；失败样本：%d。\n\n",
			artifact.Scenario,
			artifact.Backend,
			artifact.Denominator.TotalCases,
			len(artifact.Cases),
		)
		b.WriteString("| Case | Type | Status | Failure stage | Build observability | Correct |\n")
	} else {
		b.WriteString("# LongMemEval Bad Cases\n\n")
		fmt.Fprintf(&b, "Scenario: `%s`; backend: `%s`; fixed denominator: %d; bad cases: %d.\n\n",
			artifact.Scenario,
			artifact.Backend,
			artifact.Denominator.TotalCases,
			len(artifact.Cases),
		)
		b.WriteString("| Case | Type | Status | Failure stage | Build observability | Correct |\n")
	}
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, record := range artifact.Cases {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %t |\n",
			escapeLMEMarkdown(record.QuestionID),
			escapeLMEMarkdown(record.QuestionType),
			escapeLMEMarkdown(string(record.Status)),
			escapeLMEMarkdown(record.FailureStage),
			escapeLMEMarkdown(record.BuildObservability),
			record.Correct,
		)
	}
	for _, record := range artifact.Cases {
		fmt.Fprintf(&b, "\n## %s\n\n", escapeLMEMarkdown(record.QuestionID))
		if record.Error != "" {
			label := "Error"
			if zh {
				label = "错误"
			}
			fmt.Fprintf(&b, "**%s:** `%s`\n\n", label, escapeLMEMarkdown(record.Error))
		}
		if record.JudgeError != "" {
			fmt.Fprintf(&b, "**Judge error:** `%s`\n\n", escapeLMEMarkdown(record.JudgeError))
		}
		if zh {
			fmt.Fprintf(&b, "**期望：** %s\n\n**预测：** %s\n",
				escapeLMEMarkdown(record.Expected),
				escapeLMEMarkdown(record.Predicted),
			)
		} else {
			fmt.Fprintf(&b, "**Expected:** %s\n\n**Predicted:** %s\n",
				escapeLMEMarkdown(record.Expected),
				escapeLMEMarkdown(record.Predicted),
			)
		}
	}
	return b.String()
}

func escapeLMEMarkdown(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}
