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
	"os"
	"path/filepath"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestLMEProxyUsageReaderRequiresAndFiltersRunID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if _, err := newLMEProxyUsageReader(path, ""); err == nil {
		t.Fatal("newLMEProxyUsageReader() error = nil, want missing run ID error")
	}
	reader, err := newLMEProxyUsageReader(path, "run-a")
	if err != nil {
		t.Fatalf("newLMEProxyUsageReader() error = %v", err)
	}
	lines := "" +
		`{"run_id":"run-b","kind":"chat","tokens_known":true,"usage":{"total_tokens":99}}` + "\n" +
		`{"kind":"chat","tokens_known":true,"usage":{"total_tokens":88}}` + "\n" +
		`{"run_id":"run-a","kind":"chat","tokens_known":true,"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}` + "\n" +
		`{"run_id":"run-a","kind":"embedding","tokens_known":true,"usage":{"prompt_tokens":4,"total_tokens":4}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	tracker := newLMECostTracker()
	if err := reader.RecordSinceStart(
		tracker,
		lmeLLMPhaseMemoryBuild,
		lmeEmbeddingPhaseMemoryBuild,
	); err != nil {
		t.Fatalf("RecordSinceStart() error = %v", err)
	}
	report := tracker.snapshot()
	if report.LLM.MemoryBuild.Calls != 1 || report.LLM.MemoryBuild.TotalTokens != 3 {
		t.Fatalf("LLM memory-build cost = %+v, want one call and three tokens", report.LLM.MemoryBuild)
	}
	if report.Embedding.MemoryBuild.Calls != 1 || report.Embedding.MemoryBuild.TotalTokens != 4 {
		t.Fatalf("embedding memory-build cost = %+v, want one call and four tokens", report.Embedding.MemoryBuild)
	}
	if err := reader.RecordSinceStart(
		tracker,
		lmeLLMPhaseMemoryBuild,
		lmeEmbeddingPhaseMemoryBuild,
	); err != nil {
		t.Fatalf("second RecordSinceStart() error = %v", err)
	}
	if calls := tracker.snapshot().LLM.MemoryBuild.Calls; calls != 1 {
		t.Fatalf("LLM calls after second read = %d, want 1", calls)
	}
}

func TestLMECostTrackerRecordLLMPhases(t *testing.T) {
	tracker := newLMECostTracker()
	usage := &model.Usage{
		PromptTokens:     11,
		CompletionTokens: 7,
		TotalTokens:      18,
	}
	usage.PromptTokensDetails.CachedTokens = 3

	tracker.recordLLM(lmeLLMPhaseQA, usage, true)
	tracker.recordLLM(lmeLLMPhaseJudge, usage, true)
	report := tracker.snapshot()

	if report.LLM.Total.Calls != 2 {
		t.Fatalf("total calls = %d, want 2", report.LLM.Total.Calls)
	}
	if report.LLM.QA.Calls != 1 || report.LLM.Judge.Calls != 1 {
		t.Fatalf("phase calls = qa:%d judge:%d, want 1 each", report.LLM.QA.Calls, report.LLM.Judge.Calls)
	}
	if report.LLM.Total.TotalTokens != 36 || report.LLM.Total.CachedTokens != 6 {
		t.Fatalf("total tokens = %d cached = %d, want 36 and 6", report.LLM.Total.TotalTokens, report.LLM.Total.CachedTokens)
	}
	if !report.LLM.Total.TokensKnown {
		t.Fatal("total tokens should be known")
	}
}

func TestLMECostTrackerPreservesUnknownTokens(t *testing.T) {
	tracker := newLMECostTracker()
	tracker.recordLLM(lmeLLMPhaseQA, nil, false)
	report := tracker.snapshot()

	if report.LLM.QA.Calls != 1 {
		t.Fatalf("qa calls = %d, want 1", report.LLM.QA.Calls)
	}
	if report.LLM.QA.TokensKnown {
		t.Fatal("qa tokens should be unknown")
	}
}

func TestLMECostTrackerEmbeddingCallsAndCacheHits(t *testing.T) {
	tracker := newLMECostTracker()
	tracker.recordEmbedding(
		lmeEmbeddingPhaseMemoryBuild,
		map[string]any{"prompt_tokens": 5, "total_tokens": 5},
		true,
	)
	tracker.recordEmbedding(lmeEmbeddingPhaseQARetrieval, nil, false)
	report := tracker.snapshot()

	if report.Embedding.Total.Calls != 1 || report.Embedding.Total.CacheHits != 1 {
		t.Fatalf("embedding calls = %d hits = %d, want 1 each", report.Embedding.Total.Calls, report.Embedding.Total.CacheHits)
	}
	if report.Embedding.Total.Requests != 2 {
		t.Fatalf("embedding requests = %d, want 2", report.Embedding.Total.Requests)
	}
	if report.Embedding.MemoryBuild.TotalTokens != 5 {
		t.Fatalf("memory-build tokens = %d, want 5", report.Embedding.MemoryBuild.TotalTokens)
	}
}
