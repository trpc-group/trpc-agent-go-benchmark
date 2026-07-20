//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"math"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
)

func TestEvaluateRetrieval(t *testing.T) {
	result := &knowledge.SearchResult{Documents: []*knowledge.Result{
		retrievalResult("pkg/noise.py", "noise", 0.9),
		retrievalResult("pkg/a.py", "def old():\n    return 1\n", 0.8),
		retrievalResult("pkg/other.py", "other", 0.7),
		retrievalResult("pkg/more.py", "more", 0.6),
		retrievalResult("pkg/c.py", "class C:\n    value = 2\n", 0.5),
		retrievalResult("pkg/end.py", "end", 0.4),
	}}
	targets := patchTargets{
		TargetFiles: []string{"pkg/a.py", "pkg/c.py"},
		Anchors: []patchAnchor{
			{File: "pkg/a.py", Text: "def old(): return 1"},
			{File: "pkg/c.py", Text: "class C: value = 2"},
		},
	}
	metrics, retrieved := evaluateRetrieval(result, targets)
	if metrics.TargetFileRecallAt4 != 0.5 || metrics.TargetFileRecallAt6 != 1 {
		t.Fatalf("file recall = @4 %.2f @6 %.2f", metrics.TargetFileRecallAt4, metrics.TargetFileRecallAt6)
	}
	if metrics.TargetFileReciprocalRank != 0.5 {
		t.Fatalf("MRR = %.2f, want 0.5", metrics.TargetFileReciprocalRank)
	}
	if metrics.HunkAnchorRecallAt4 != 0.5 || metrics.HunkAnchorRecallAt6 != 1 {
		t.Fatalf("anchor recall = @4 %.2f @6 %.2f", metrics.HunkAnchorRecallAt4, metrics.HunkAnchorRecallAt6)
	}
	if metrics.TargetFileCharPrecisionAt6 <= 0 || metrics.TargetFileCharPrecisionAt6 >= 1 {
		t.Fatalf("target char precision = %.3f, want partial precision", metrics.TargetFileCharPrecisionAt6)
	}
	if len(retrieved) != 6 || retrieved[1].FilePath != "pkg/a.py" ||
		len(retrieved[1].ContentSHA256) != 64 {
		t.Fatalf("retrieved trace = %#v", retrieved)
	}
}

func TestAggregateResultsUsesSuccessfulArmDenominator(t *testing.T) {
	representation := "ast-code"
	results := []CaseResult{
		{Arms: []ArmResult{{
			Representation: representation,
			Metrics: RetrievalMetrics{
				TargetFileRecallAt4:      1,
				TargetFileRecallAt6:      1,
				TargetFileReciprocalRank: 0.5,
			},
			Index: tagagent.WorkspaceIndexStats{
				FileCoverage: 1,
				Documents:    10,
				DurationMS:   20,
			},
		}}},
		{Arms: []ArmResult{{
			Representation: representation,
			Error:          "failed",
		}}},
		{Error: "snapshot failed"},
	}
	aggregates := aggregateResults(results, []tagagent.WorkspaceRepresentation{
		tagagent.WorkspaceRepresentationASTCode,
	})
	got := aggregates[representation]
	if got.Cases != 3 || got.SuccessfulCases != 1 || got.Errors != 2 ||
		math.Abs(got.MeanTargetFileRecallAt4-1) > 1e-9 ||
		math.Abs(got.MeanDocuments-10) > 1e-9 {
		t.Fatalf("aggregate = %+v", got)
	}
}

func retrievalResult(path, content string, score float64) *knowledge.Result {
	return &knowledge.Result{
		Document: &document.Document{
			Content: content,
			Metadata: map[string]any{
				source.MetaFilePath: path,
			},
		},
		Score: score,
	}
}
