//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
)

// standardCategories is the ordered list of QA categories.
var standardCategories = []string{
	"single-hop", "multi-hop", "temporal",
	"open-domain", "adversarial",
}

func runEvaluation(
	samples []*dataset.LoCoMoSample,
	evaluator scenarios.Evaluator,
	config scenarios.Config,
	backend string,
	scenarioDir string,
) *EvaluationResult {
	startTime := time.Now()
	catAgg := metrics.NewCategoryAggregator()
	sampleResults := make([]*scenarios.SampleResult, 0, len(samples))
	var totalQuestions int
	var totalUsage scenarios.TokenUsage

	for i, sample := range samples {
		log.Printf("[%d/%d] Evaluating sample: %s (%d QA)",
			i+1, len(samples), sample.SampleID, len(sample.QA))

		sampleStart := time.Now()
		result, err := evaluator.Evaluate(context.Background(), sample)
		if err != nil {
			log.Printf("  Error: %v", err)
			continue
		}

		sampleResults = append(sampleResults, result)
		totalQuestions += len(result.QAResults)

		// Aggregate category metrics.
		for _, qaResult := range result.QAResults {
			catAgg.Add(qaResult.Category, qaResult.Metrics)
		}

		// Aggregate token usage.
		if result.TokenUsage != nil {
			totalUsage.Add(*result.TokenUsage)
		}

		log.Printf("  Completed in %v | F1=%.3f BLEU=%.3f",
			time.Since(sampleStart).Round(time.Millisecond),
			result.Overall.F1,
			result.Overall.BLEU)
		if result.TokenUsage != nil &&
			result.TokenUsage.LLMCalls > 0 {
			if result.TokenUsage.CachedTokens > 0 {
				log.Printf(
					"  Tokens: prompt=%d cached=%d"+
						" completion=%d calls=%d",
					result.TokenUsage.PromptTokens,
					result.TokenUsage.CachedTokens,
					result.TokenUsage.CompletionTokens,
					result.TokenUsage.LLMCalls,
				)
			} else {
				log.Printf(
					"  Tokens: prompt=%d"+
						" completion=%d calls=%d",
					result.TokenUsage.PromptTokens,
					result.TokenUsage.CompletionTokens,
					result.TokenUsage.LLMCalls,
				)
			}
		}

		// Log per-sample category breakdown.
		logSampleCategoryBreakdown(result)

		// Incremental checkpoint: save partial results after
		// each sample so progress is not lost.
		partial := buildEvaluationResult(
			config, backend, startTime,
			sampleResults, catAgg, totalQuestions, totalUsage,
		)
		saveResults(scenarioDir, partial)
	}

	return buildEvaluationResult(
		config, backend, startTime,
		sampleResults, catAgg, totalQuestions, totalUsage,
	)
}

// logSampleCategoryBreakdown prints a one-line per-category
// summary for the completed sample.
func logSampleCategoryBreakdown(result *scenarios.SampleResult) {
	if len(result.ByCategory) == 0 {
		return
	}
	parts := make([]string, 0, len(standardCategories))
	for _, cat := range standardCategories {
		m, ok := result.ByCategory[cat]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"%s: F1=%.3f", cat, m.F1,
		))
	}
	if len(parts) > 0 {
		log.Printf("  Categories: %s", strings.Join(parts, " | "))
	}
}

// buildEvaluationResult constructs the full result from
// accumulated data.
func buildEvaluationResult(
	config scenarios.Config,
	backend string,
	startTime time.Time,
	sampleResults []*scenarios.SampleResult,
	catAgg *metrics.CategoryAggregator,
	totalQuestions int,
	totalUsage scenarios.TokenUsage,
) *EvaluationResult {
	totalTime := time.Since(startTime)
	overall := catAgg.GetOverall()
	qCount := max(totalQuestions, 1)
	var cacheHitRate float64
	if totalUsage.PromptTokens > 0 {
		cacheHitRate = float64(totalUsage.CachedTokens) /
			float64(totalUsage.PromptTokens)
	}
	return &EvaluationResult{
		Metadata: &EvalMetadata{
			Framework:      "trpc-agent-go",
			Version:        "1.0.0",
			Timestamp:      time.Now(),
			Model:          getModelName(),
			EvalModel:      getEvalModelName(),
			Scenario:       string(config.Scenario),
			MemoryBackend:  backend,
			MaxContext:     config.MaxContext,
			QAHistoryTurns: config.QAHistoryTurns,
			QASearchPasses: config.QASearchPasses,
			LLMJudge:       config.EnableLLMJudge,
		},
		Summary: &EvalSummary{
			TotalSamples:          len(sampleResults),
			TotalQuestions:        totalQuestions,
			OverallF1:             overall.F1,
			OverallBLEU:           overall.BLEU,
			OverallLLMScore:       overall.LLMScore,
			TotalTimeMs:           totalTime.Milliseconds(),
			AvgLatencyMs:          float64(totalTime.Milliseconds()) / float64(qCount),
			TotalPromptTokens:     totalUsage.PromptTokens,
			TotalCompletionTokens: totalUsage.CompletionTokens,
			TotalTokens:           totalUsage.TotalTokens,
			TotalCachedTokens:     totalUsage.CachedTokens,
			TotalLLMCalls:         totalUsage.LLMCalls,
			AvgPromptTokensPerQA:  float64(totalUsage.PromptTokens) / float64(qCount),
			AvgCompletionPerQA:    float64(totalUsage.CompletionTokens) / float64(qCount),
			AvgCachedTokensPerQA:  float64(totalUsage.CachedTokens) / float64(qCount),
			AvgLLMCallsPerQA:      float64(totalUsage.LLMCalls) / float64(qCount),
			CacheHitRate:          cacheHitRate,
		},
		ByCategory:    catAgg.GetCategoryMetrics(),
		SampleResults: sampleResults,
	}
}

func saveResults(outputDir string, result *EvaluationResult) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Printf("Failed to create output directory: %v", err)
		return
	}

	// Save full results.
	resultsPath := filepath.Join(outputDir, "results.json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal results: %v", err)
		return
	}
	// #nosec G306 -- Results are shareable benchmark artifacts, not private application state.
	if err := os.WriteFile(resultsPath, data, 0o644); err != nil {
		log.Printf("Failed to write results: %v", err)
		return
	}
	log.Printf("Results saved to: %s", resultsPath)

	// Save checkpoint (same as results for now).
	checkpointPath := filepath.Join(outputDir, "checkpoint.json")
	// #nosec G306 -- Checkpoints are shareable benchmark artifacts, not private application state.
	if err := os.WriteFile(checkpointPath, data, 0o644); err != nil {
		log.Printf("Failed to write checkpoint: %v", err)
	}
}

func printSummary(result *EvaluationResult) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Memory Evaluation Results - %s\n", result.Metadata.Scenario)
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\nModel: %s\n", result.Metadata.Model)
	fmt.Printf("Scenario: %s\n", result.Metadata.Scenario)
	if result.Metadata.MemoryBackend != "" {
		fmt.Printf("Memory Backend: %s\n",
			result.Metadata.MemoryBackend)
	}
	if result.Metadata.QAHistoryTurns > 0 {
		fmt.Printf("QA History Turns: %d\n",
			result.Metadata.QAHistoryTurns)
	}
	if result.Metadata.QASearchPasses > 1 {
		fmt.Printf("QA Search Passes: %d\n",
			result.Metadata.QASearchPasses)
	}
	fmt.Printf("Samples: %d | Questions: %d\n",
		result.Summary.TotalSamples, result.Summary.TotalQuestions)

	fmt.Println("\n--- Overall Metrics ---")
	fmt.Printf("F1 Score:   %.4f (%.1f)\n", result.Summary.OverallF1, result.Summary.OverallF1*100)
	fmt.Printf("BLEU Score: %.4f\n", result.Summary.OverallBLEU)
	if result.Summary.OverallLLMScore > 0 {
		fmt.Printf("LLM Score:  %.4f\n", result.Summary.OverallLLMScore)
	}
	fmt.Printf("Total Time: %dms | Avg Latency: %.1fms\n",
		result.Summary.TotalTimeMs, result.Summary.AvgLatencyMs)

	if result.Summary.TotalLLMCalls > 0 {
		fmt.Println("\n--- Token Usage ---")
		fmt.Printf("Prompt Tokens:     %d (avg %.0f/QA)\n",
			result.Summary.TotalPromptTokens,
			result.Summary.AvgPromptTokensPerQA)
		fmt.Printf("Completion Tokens: %d (avg %.0f/QA)\n",
			result.Summary.TotalCompletionTokens,
			result.Summary.AvgCompletionPerQA)
		fmt.Printf("Total Tokens:      %d\n",
			result.Summary.TotalTokens)
		fmt.Printf("LLM Calls:         %d (avg %.1f/QA)\n",
			result.Summary.TotalLLMCalls,
			result.Summary.AvgLLMCallsPerQA)
	}

	fmt.Println("\n--- By Category ---")
	fmt.Printf("%-15s %8s %8s %8s %8s\n", "Category", "Count", "F1", "BLEU", "LLM")
	fmt.Println(strings.Repeat("-", 51))

	categories := []string{"single-hop", "multi-hop", "temporal", "open-domain", "adversarial"}
	for _, cat := range categories {
		if m, ok := result.ByCategory[cat]; ok {
			llmStr := "-"
			if m.LLMScore > 0 {
				llmStr = fmt.Sprintf("%.3f", m.LLMScore)
			}
			fmt.Printf("%-15s %8d %8.3f %8.3f %8s\n",
				cat, m.Count, m.F1, m.BLEU, llmStr)
		}
	}

	// Print any other categories not in the standard list.
	for cat, m := range result.ByCategory {
		found := false
		for _, c := range categories {
			if c == cat {
				found = true
				break
			}
		}
		if !found {
			llmStr := "-"
			if m.LLMScore > 0 {
				llmStr = fmt.Sprintf("%.3f", m.LLMScore)
			}
			fmt.Printf("%-15s %8d %8.3f %8.3f %8s\n",
				cat, m.Count, m.F1, m.BLEU, llmStr)
		}
	}

	fmt.Println(strings.Repeat("=", 60))
}
