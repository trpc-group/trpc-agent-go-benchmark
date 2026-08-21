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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type lmeCostBucket struct {
	Calls            int  `json:"calls"`
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	CachedTokens     int  `json:"cached_tokens,omitempty"`
	TokensKnown      bool `json:"tokens_known"`
	CacheHits        int  `json:"cache_hits,omitempty"`
	Requests         int  `json:"requests,omitempty"`
}

type lmeLLMCost struct {
	Total       lmeCostBucket `json:"total"`
	MemoryBuild lmeCostBucket `json:"memory_build"`
	QA          lmeCostBucket `json:"qa"`
	Judge       lmeCostBucket `json:"judge"`
}

type lmeEmbeddingCost struct {
	Total       lmeCostBucket `json:"total"`
	MemoryBuild lmeCostBucket `json:"memory_build"`
	QARetrieval lmeCostBucket `json:"qa_retrieval"`
}

type lmeCostReport struct {
	LLM           lmeLLMCost       `json:"llm"`
	Embedding     lmeEmbeddingCost `json:"embedding"`
	Partial       bool             `json:"partial,omitempty"`
	PartialReason string           `json:"partial_reason,omitempty"`
}

type lmeLLMPhase string

const (
	lmeLLMPhaseMemoryBuild lmeLLMPhase = "memory_build"
	lmeLLMPhaseQA          lmeLLMPhase = "qa"
	lmeLLMPhaseJudge       lmeLLMPhase = "judge"
)

type lmeEmbeddingPhase string

const (
	lmeEmbeddingPhaseMemoryBuild lmeEmbeddingPhase = "memory_build"
	lmeEmbeddingPhaseQARetrieval lmeEmbeddingPhase = "qa_retrieval"
)

func setLMERunCost(
	result *lmeRunResult,
	evaluator lmeEvaluator,
	base *lmeCostReport,
) {
	reporter, ok := evaluator.(interface {
		CostReport() *lmeCostReport
	})
	if !ok {
		result.Cost = base
		return
	}
	result.Cost = mergeLMECostReports(base, reporter.CostReport())
}

type lmeCostTracker struct {
	mu     sync.Mutex
	report lmeCostReport
}

type lmeTrackedModel struct {
	inner   model.Model
	tracker *lmeCostTracker
	phase   lmeLLMPhase
}

type lmeTrackedEmbedder struct {
	inner embedder.Embedder
}

type lmeCostTrackerContextKey struct{}
type lmeEmbeddingPhaseContextKey struct{}

func newLMECostTracker() *lmeCostTracker {
	report := lmeCostReport{}
	normalizeLMECostReport(&report)
	return &lmeCostTracker{report: report}
}

func newLMETrackedModel(
	inner model.Model,
	tracker *lmeCostTracker,
	phase lmeLLMPhase,
) model.Model {
	if inner == nil || tracker == nil {
		return inner
	}
	return &lmeTrackedModel{
		inner:   inner,
		tracker: tracker,
		phase:   phase,
	}
}

func newLMETrackedEmbedder(inner embedder.Embedder) embedder.Embedder {
	if inner == nil {
		return nil
	}
	return &lmeTrackedEmbedder{inner: inner}
}

func withLMECostTracker(
	ctx context.Context,
	tracker *lmeCostTracker,
) context.Context {
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, lmeCostTrackerContextKey{}, tracker)
}

func withLMEEmbeddingPhase(
	ctx context.Context,
	phase lmeEmbeddingPhase,
) context.Context {
	return context.WithValue(ctx, lmeEmbeddingPhaseContextKey{}, phase)
}

func lmeCostTrackerFromContext(ctx context.Context) *lmeCostTracker {
	tracker, _ := ctx.Value(lmeCostTrackerContextKey{}).(*lmeCostTracker)
	return tracker
}

func lmeEmbeddingPhaseFromContext(ctx context.Context) lmeEmbeddingPhase {
	phase, _ := ctx.Value(lmeEmbeddingPhaseContextKey{}).(lmeEmbeddingPhase)
	return phase
}

func (m *lmeTrackedModel) GenerateContent(
	ctx context.Context,
	request *model.Request,
) (<-chan *model.Response, error) {
	ch, err := m.inner.GenerateContent(ctx, request)
	if err != nil {
		return nil, err
	}
	out := make(chan *model.Response)
	go func() {
		defer close(out)
		var usage model.Usage
		tokensKnown := false
		for resp := range ch {
			if resp != nil && resp.Usage != nil {
				tokensKnown = true
				usage.PromptTokens += resp.Usage.PromptTokens
				usage.CompletionTokens += resp.Usage.CompletionTokens
				usage.TotalTokens += resp.Usage.TotalTokens
				usage.PromptTokensDetails.CachedTokens +=
					resp.Usage.PromptTokensDetails.CachedTokens
			}
			out <- resp
		}
		m.tracker.recordLLM(m.phase, &usage, tokensKnown)
	}()
	return out, nil
}

func (m *lmeTrackedModel) Info() model.Info {
	return m.inner.Info()
}

func (e *lmeTrackedEmbedder) GetEmbedding(
	ctx context.Context,
	text string,
) ([]float64, error) {
	embedding, usage, err := e.GetEmbeddingWithUsage(ctx, text)
	_ = usage
	return embedding, err
}

func (e *lmeTrackedEmbedder) GetEmbeddingWithUsage(
	ctx context.Context,
	text string,
) ([]float64, map[string]any, error) {
	embedding, usage, err := e.inner.GetEmbeddingWithUsage(ctx, text)
	if err != nil {
		return nil, nil, err
	}
	recordLMEEmbeddingUsage(ctx, usage, true)
	return embedding, usage, nil
}

func (e *lmeTrackedEmbedder) GetDimensions() int {
	return e.inner.GetDimensions()
}

func (t *lmeCostTracker) recordLLM(
	phase lmeLLMPhase,
	usage *model.Usage,
	tokensKnown bool,
) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	bucket := lmeCostBucketFromLLMUsage(usage, tokensKnown)
	addLMECostBucket(&t.report.LLM.Total, bucket)
	switch phase {
	case lmeLLMPhaseMemoryBuild:
		addLMECostBucket(&t.report.LLM.MemoryBuild, bucket)
	case lmeLLMPhaseQA:
		addLMECostBucket(&t.report.LLM.QA, bucket)
	case lmeLLMPhaseJudge:
		addLMECostBucket(&t.report.LLM.Judge, bucket)
	default:
	}
}

func (t *lmeCostTracker) recordEmbedding(
	phase lmeEmbeddingPhase,
	usage map[string]any,
	remoteCall bool,
) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	bucket := lmeCostBucketFromEmbeddingUsage(usage, remoteCall)
	addLMECostBucket(&t.report.Embedding.Total, bucket)
	switch phase {
	case lmeEmbeddingPhaseMemoryBuild:
		addLMECostBucket(&t.report.Embedding.MemoryBuild, bucket)
	case lmeEmbeddingPhaseQARetrieval:
		addLMECostBucket(&t.report.Embedding.QARetrieval, bucket)
	default:
	}
}

func (t *lmeCostTracker) recordExternalLLM(
	phase lmeLLMPhase,
	bucket lmeCostBucket,
) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	addLMECostBucket(&t.report.LLM.Total, bucket)
	switch phase {
	case lmeLLMPhaseMemoryBuild:
		addLMECostBucket(&t.report.LLM.MemoryBuild, bucket)
	case lmeLLMPhaseQA:
		addLMECostBucket(&t.report.LLM.QA, bucket)
	case lmeLLMPhaseJudge:
		addLMECostBucket(&t.report.LLM.Judge, bucket)
	default:
	}
}

func (t *lmeCostTracker) recordExternalEmbedding(
	phase lmeEmbeddingPhase,
	bucket lmeCostBucket,
) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	addLMECostBucket(&t.report.Embedding.Total, bucket)
	switch phase {
	case lmeEmbeddingPhaseMemoryBuild:
		addLMECostBucket(&t.report.Embedding.MemoryBuild, bucket)
	case lmeEmbeddingPhaseQARetrieval:
		addLMECostBucket(&t.report.Embedding.QARetrieval, bucket)
	default:
	}
}

func (t *lmeCostTracker) markPartial(reason string) {
	if t == nil || reason == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.report.Partial = true
	if t.report.PartialReason == "" {
		t.report.PartialReason = reason
	}
}

func (t *lmeCostTracker) snapshot() *lmeCostReport {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	report := t.report
	normalizeLMECostReport(&report)
	return &report
}

func recordLMEEmbeddingUsage(
	ctx context.Context,
	usage map[string]any,
	remoteCall bool,
) {
	tracker := lmeCostTrackerFromContext(ctx)
	if tracker == nil {
		return
	}
	tracker.recordEmbedding(lmeEmbeddingPhaseFromContext(ctx), usage, remoteCall)
}

func lmeCostBucketFromLLMUsage(
	usage *model.Usage,
	tokensKnown bool,
) lmeCostBucket {
	bucket := lmeCostBucket{
		Calls:       1,
		TokensKnown: tokensKnown,
	}
	if usage == nil || !tokensKnown {
		return bucket
	}
	bucket.PromptTokens = usage.PromptTokens
	bucket.CompletionTokens = usage.CompletionTokens
	bucket.TotalTokens = usage.TotalTokens
	bucket.CachedTokens = usage.PromptTokensDetails.CachedTokens
	return bucket
}

func lmeCostBucketFromEmbeddingUsage(
	usage map[string]any,
	remoteCall bool,
) lmeCostBucket {
	bucket := lmeCostBucket{
		TokensKnown: true,
		Requests:    1,
	}
	if !remoteCall {
		bucket.CacheHits = 1
		return bucket
	}
	bucket.Calls = 1
	prompt, promptOK := intFromLMEUsageMap(usage, "prompt_tokens")
	total, totalOK := intFromLMEUsageMap(usage, "total_tokens")
	bucket.PromptTokens = prompt
	bucket.TotalTokens = total
	bucket.TokensKnown = promptOK || totalOK
	return bucket
}

func addLMECostBucket(dst *lmeCostBucket, src lmeCostBucket) {
	if dst.Calls == 0 && dst.Requests == 0 && !dst.TokensKnown {
		dst.TokensKnown = true
	}
	dst.Calls += src.Calls
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.TotalTokens += src.TotalTokens
	dst.CachedTokens += src.CachedTokens
	dst.CacheHits += src.CacheHits
	dst.Requests += src.Requests
	dst.TokensKnown = dst.TokensKnown && src.TokensKnown
}

func mergeLMECostReports(
	base *lmeCostReport,
	next *lmeCostReport,
) *lmeCostReport {
	if base == nil {
		return next
	}
	if next == nil {
		return base
	}
	merged := *base
	addLMECostBucket(&merged.LLM.Total, next.LLM.Total)
	addLMECostBucket(&merged.LLM.MemoryBuild, next.LLM.MemoryBuild)
	addLMECostBucket(&merged.LLM.QA, next.LLM.QA)
	addLMECostBucket(&merged.LLM.Judge, next.LLM.Judge)
	addLMECostBucket(&merged.Embedding.Total, next.Embedding.Total)
	addLMECostBucket(&merged.Embedding.MemoryBuild, next.Embedding.MemoryBuild)
	addLMECostBucket(&merged.Embedding.QARetrieval, next.Embedding.QARetrieval)
	merged.Partial = base.Partial || next.Partial
	if merged.PartialReason == "" {
		merged.PartialReason = next.PartialReason
	}
	normalizeLMECostReport(&merged)
	return &merged
}

func partialLMECostReport(reason string) *lmeCostReport {
	report := lmeCostReport{
		Partial:       true,
		PartialReason: reason,
	}
	normalizeLMECostReport(&report)
	return &report
}

func normalizeLMECostReport(report *lmeCostReport) {
	if report == nil {
		return
	}
	normalizeLMECostBucket(&report.LLM.Total)
	normalizeLMECostBucket(&report.LLM.MemoryBuild)
	normalizeLMECostBucket(&report.LLM.QA)
	normalizeLMECostBucket(&report.LLM.Judge)
	normalizeLMECostBucket(&report.Embedding.Total)
	normalizeLMECostBucket(&report.Embedding.MemoryBuild)
	normalizeLMECostBucket(&report.Embedding.QARetrieval)
}

func normalizeLMECostBucket(bucket *lmeCostBucket) {
	if bucket.Calls == 0 && bucket.Requests == 0 && bucket.CacheHits == 0 {
		bucket.TokensKnown = true
	}
}

func intFromLMEUsageMap(
	usage map[string]any,
	key string,
) (int, bool) {
	if usage == nil {
		return 0, false
	}
	value, ok := usage[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

type lmeProxyUsageReader struct {
	path   string
	runID  string
	offset int64
	mu     sync.Mutex
}

type lmeProxyUsageEntry struct {
	RunID       string         `json:"run_id"`
	Kind        string         `json:"kind"`
	Type        string         `json:"type"`
	RequestType string         `json:"request_type"`
	Usage       map[string]any `json:"usage"`
	TokensKnown *bool          `json:"tokens_known"`
}

func newLMEProxyUsageReader(path string, runID string) (*lmeProxyUsageReader, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("Mem0 proxy run ID is required for usage accounting")
	}
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat proxy usage log: %w", err)
	}
	return &lmeProxyUsageReader{path: path, runID: runID, offset: offset}, nil
}

func (r *lmeProxyUsageReader) RecordSinceStart(
	tracker *lmeCostTracker,
	llmPhase lmeLLMPhase,
	embeddingPhase lmeEmbeddingPhase,
) error {
	if r == nil || tracker == nil || r.path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.Open(r.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(r.offset, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	nextOffset := r.offset
	for scanner.Scan() {
		line := scanner.Text()
		nextOffset += int64(len(line)) + 1
		var entry lmeProxyUsageEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.RunID != r.runID {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(
			entry.Kind,
			entry.Type,
			entry.RequestType,
		)))
		switch kind {
		case "chat", "chat.completions", "chat_completion":
			tracker.recordExternalLLM(llmPhase, lmeProxyUsageLLMBucket(entry))
		case "embedding", "embeddings":
			tracker.recordExternalEmbedding(embeddingPhase, lmeProxyUsageEmbeddingBucket(entry))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	r.offset = nextOffset
	return nil
}

func lmeProxyUsageLLMBucket(entry lmeProxyUsageEntry) lmeCostBucket {
	tokensKnown := lmeProxyTokensKnown(entry)
	bucket := lmeCostBucket{Calls: 1, TokensKnown: tokensKnown}
	if !tokensKnown {
		return bucket
	}
	bucket.PromptTokens, _ = intFromLMEUsageMap(entry.Usage, "prompt_tokens")
	bucket.CompletionTokens, _ = intFromLMEUsageMap(entry.Usage, "completion_tokens")
	bucket.TotalTokens, _ = intFromLMEUsageMap(entry.Usage, "total_tokens")
	bucket.CachedTokens, _ = intFromLMEUsageMap(entry.Usage, "cached_tokens")
	return bucket
}

func lmeProxyUsageEmbeddingBucket(entry lmeProxyUsageEntry) lmeCostBucket {
	tokensKnown := lmeProxyTokensKnown(entry)
	bucket := lmeCostBucket{Calls: 1, Requests: 1, TokensKnown: tokensKnown}
	if !tokensKnown {
		return bucket
	}
	bucket.PromptTokens, _ = intFromLMEUsageMap(entry.Usage, "prompt_tokens")
	bucket.TotalTokens, _ = intFromLMEUsageMap(entry.Usage, "total_tokens")
	return bucket
}

func lmeProxyTokensKnown(entry lmeProxyUsageEntry) bool {
	if entry.TokensKnown != nil {
		return *entry.TokensKnown
	}
	if _, ok := intFromLMEUsageMap(entry.Usage, "total_tokens"); ok {
		return true
	}
	if _, ok := intFromLMEUsageMap(entry.Usage, "prompt_tokens"); ok {
		return true
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
