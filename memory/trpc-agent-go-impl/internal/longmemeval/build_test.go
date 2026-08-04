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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestBuildLMERunConfigUsesDefaultBuildLimit(t *testing.T) {
	cfg, _, err := buildLMERunConfig(Config{
		ModelName:   "gpt-4o-mini",
		Model:       noopLMEModel{},
		OutputDir:   t.TempDir(),
		DatasetPath: "dataset.json",
	})
	if err != nil {
		t.Fatalf("buildLMERunConfig() error = %v", err)
	}
	if cfg.BuildMaxTokens != lmeDefaultBuildMaxTokens {
		t.Fatalf("BuildMaxTokens = %d, want %d", cfg.BuildMaxTokens, lmeDefaultBuildMaxTokens)
	}
	if cfg.BuildTokenizerModel != "text-embedding-3-small" {
		t.Fatalf("BuildTokenizerModel = %q, want embedding model", cfg.BuildTokenizerModel)
	}
	if cfg.BuildTokenizerEncoding != "cl100k_base" {
		t.Fatalf("BuildTokenizerEncoding = %q, want cl100k_base", cfg.BuildTokenizerEncoding)
	}
	if _, err := newLMETextTokenizer(
		cfg.BuildTokenizerModel,
		cfg.BuildTokenizerEncoding,
	); err != nil {
		t.Fatalf("newLMETextTokenizer() error = %v", err)
	}
	if cfg.RetrievalTopK != lmeRetrievalTopK {
		t.Fatalf("RetrievalTopK = %d, want %d", cfg.RetrievalTopK, lmeRetrievalTopK)
	}
}

func TestDefaultLMEBuildTokenizerEncoding(t *testing.T) {
	tests := map[string]string{
		"text-embedding-ada-002": "cl100k_base",
		"text-embedding-3-small": "cl100k_base",
		"text-embedding-3-large": "cl100k_base",
		"custom-embedding-model": "",
	}
	for modelName, want := range tests {
		t.Run(modelName, func(t *testing.T) {
			if got := defaultLMEBuildTokenizerEncoding(modelName); got != want {
				t.Fatalf("defaultLMEBuildTokenizerEncoding(%q) = %q, want %q", modelName, got, want)
			}
		})
	}
}

func TestBuildLMERunConfigUsesConfiguredBuildTokenizer(t *testing.T) {
	cfg, _, err := buildLMERunConfig(Config{
		ModelName:              "gpt-4o-mini",
		Model:                  noopLMEModel{},
		OutputDir:              t.TempDir(),
		DatasetPath:            "dataset.json",
		EmbedModelName:         "text-embedding-ada-002",
		BuildTokenizerModel:    "gpt-4o-mini",
		BuildTokenizerEncoding: "o200k_base",
	})
	if err != nil {
		t.Fatalf("buildLMERunConfig() error = %v", err)
	}
	if cfg.BuildTokenizerModel != "gpt-4o-mini" {
		t.Fatalf("BuildTokenizerModel = %q, want configured model", cfg.BuildTokenizerModel)
	}
	if cfg.BuildTokenizerEncoding != "o200k_base" {
		t.Fatalf("BuildTokenizerEncoding = %q, want configured encoding", cfg.BuildTokenizerEncoding)
	}
}

func TestPrepareLMEInputArtifactsBuildsAndReusesCanonicalPlan(t *testing.T) {
	root := t.TempDir()
	datasetPath := filepath.Join(root, "dataset.json")
	if err := os.WriteFile(datasetPath, []byte("canonical dataset bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := lmeRunConfig{
		DatasetPath:            datasetPath,
		ReplayRoot:             filepath.Join(root, "replay"),
		BuildMaxTokens:         100,
		BuildTokenizerModel:    "gpt-4o-mini",
		BuildTokenizerEncoding: "o200k_base",
	}
	instances := []*dataset.LongMemEvalInstance{testLMEArtifactInstance()}
	first, err := prepareLMEInputArtifacts(cfg, instances)
	if err != nil {
		t.Fatalf("prepareLMEInputArtifacts() error = %v", err)
	}
	indexPath := filepath.Join(first.BuildPlanRoot, lmeReplayIndexFile)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareLMEInputArtifacts(cfg, instances)
	if err != nil {
		t.Fatalf("second prepareLMEInputArtifacts() error = %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReplayDigest != second.ReplayDigest ||
		first.BuildPlanDigest != second.BuildPlanDigest ||
		string(before) != string(after) {
		t.Fatal("canonical replay or build plan changed across identical preparation")
	}
	if first.BuildStats.PairCount == 0 || first.BuildStats.ChunkCount == 0 {
		t.Fatalf("build statistics are incomplete: %+v", first.BuildStats)
	}
	relativePlan, err := filepath.Rel(cfg.ReplayRoot, first.BuildPlanRoot)
	if err != nil {
		t.Fatal(err)
	}
	if relativePlan != ".." && !strings.HasPrefix(relativePlan, ".."+string(filepath.Separator)) {
		t.Fatalf("build plan %q must not be nested under replay %q", first.BuildPlanRoot, cfg.ReplayRoot)
	}
	replayArtifactBefore, err := digestLMEArtifact(cfg.ReplayRoot)
	if err != nil {
		t.Fatal(err)
	}
	alternate := cfg
	alternate.BuildMaxTokens = cfg.BuildMaxTokens - 1
	third, err := prepareLMEInputArtifacts(alternate, instances)
	if err != nil {
		t.Fatalf("prepare alternate build plan: %v", err)
	}
	if third.BuildPlanRoot == first.BuildPlanRoot {
		t.Fatal("different build configuration reused the same build plan")
	}
	replayArtifactAfter, err := digestLMEArtifact(cfg.ReplayRoot)
	if err != nil {
		t.Fatal(err)
	}
	if replayArtifactAfter != replayArtifactBefore {
		t.Fatal("adding a build plan changed the canonical replay artifact")
	}
}

func TestLMEBuildPlanProtocolIsNotConfigurable(t *testing.T) {
	planner := testLMEBuildPlanner(t, 100)
	configJSON, err := json.Marshal(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configJSON), "protocol") {
		t.Fatalf("planner config unexpectedly exposes a protocol: %s", configJSON)
	}

	replayCase := singleSessionReplayCase("user", "assistant")
	replay := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:      lmeReplayVersion,
			ReplayDigest: "replay-digest",
			Cases:        []lmeArtifactEntry{{CaseID: replayCase.CaseID}},
		},
		Cases: map[string]*lmeReplayCase{replayCase.CaseID: replayCase},
	}
	configDigest, err := lmeJSONDigest(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Index.Protocol != lmeBuildProtocol {
		t.Fatalf("build protocol = %q, want fixed %q", plan.Index.Protocol, lmeBuildProtocol)
	}
}

func TestVerifyLMEBuildPlanSourceRejectsContentMismatch(t *testing.T) {
	replayCase := singleSessionReplayCase("user", "assistant")
	replay := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:      lmeReplayVersion,
			ReplayDigest: "replay-digest",
			Cases:        []lmeArtifactEntry{{CaseID: replayCase.CaseID}},
		},
		Cases: map[string]*lmeReplayCase{replayCase.CaseID: replayCase},
	}
	planner := testLMEBuildPlanner(t, 100)
	configDigest, err := lmeJSONDigest(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	plan.Cases[replayCase.CaseID].Sessions[0].Pairs[0].Chunks[0].Turns[0].Content += " changed"
	if err := verifyLMEBuildPlanSource(plan, replay); err == nil ||
		!strings.Contains(err.Error(), "source turn user-turn") {
		t.Fatalf("verifyLMEBuildPlanSource() error = %v, want source mismatch", err)
	}
}

func TestVerifyLMEBuildPlanSourceRejectsRelabeledSessionBatch(t *testing.T) {
	replayCase := &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "case-relabeled-batch",
		Sessions: []lmeReplaySession{{
			SessionIndex:    0,
			SessionID:       "session-relabeled-batch",
			ObservationTime: "2025-01-01T00:00:00Z",
			Turns: []lmeReplayTurn{
				{TurnIndex: 0, TurnID: "u1", Role: "user", Content: "first"},
				{TurnIndex: 1, TurnID: "a1", Role: "assistant", Content: "reply"},
				{TurnIndex: 2, TurnID: "u2", Role: "user", Content: "second"},
			},
		}},
	}
	replay := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:      lmeReplayVersion,
			ReplayDigest: "replay-digest",
			Cases:        []lmeArtifactEntry{{CaseID: replayCase.CaseID}},
		},
		Cases: map[string]*lmeReplayCase{replayCase.CaseID: replayCase},
	}
	planner := testLMEBuildPlanner(t, 100)
	configDigest, err := lmeJSONDigest(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	sessionPlan := &plan.Cases[replayCase.CaseID].Sessions[0]
	sessionPlan.Pairs[0].SourceTurnIDs = []string{"u1", "a1", "u2"}
	sessionPlan.Pairs = sessionPlan.Pairs[:1]
	if err := verifyLMEBuildPlanSource(plan, replay); err == nil ||
		!strings.Contains(err.Error(), "turn pairs") {
		t.Fatalf("verifyLMEBuildPlanSource() error = %v, want pair-boundary rejection", err)
	}
}

func TestLMEBuildPlanTurnPairPreservesOddUserAndChronology(t *testing.T) {
	replayCase := &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "case-pairs",
		Sessions: []lmeReplaySession{
			{
				SessionIndex:    0,
				SessionID:       "early",
				ObservationTime: "2025-01-01T00:00:00Z",
				Turns: []lmeReplayTurn{
					{TurnIndex: 0, TurnID: "u1", Role: "user", Content: "first user"},
					{TurnIndex: 1, TurnID: "a1", Role: "assistant", Content: "first assistant"},
					{TurnIndex: 2, TurnID: "u2", Role: "user", Content: "odd trailing user"},
				},
			},
			{
				SessionIndex:    1,
				SessionID:       "late",
				ObservationTime: "2025-01-02T00:00:00Z",
				Turns: []lmeReplayTurn{
					{TurnIndex: 0, TurnID: "u3", Role: "user", Content: "later user"},
				},
			},
		},
	}
	planner := testLMEBuildPlanner(t, 100)
	plan, err := planner.buildCase(replayCase, "config-digest")
	if err != nil {
		t.Fatalf("buildCase() error = %v", err)
	}
	if len(plan.Sessions) != 2 || plan.Sessions[0].SessionID != "early" || plan.Sessions[1].SessionID != "late" {
		t.Fatalf("session chronology = %+v", plan.Sessions)
	}
	if got := len(plan.Sessions[0].Pairs); got != 2 {
		t.Fatalf("first session pairs = %d, want 2", got)
	}
	odd := plan.Sessions[0].Pairs[1]
	if len(odd.SourceTurnIDs) != 1 || odd.SourceTurnIDs[0] != "u2" {
		t.Fatalf("odd trailing user source IDs = %v", odd.SourceTurnIDs)
	}
	if got := concatenateLMEPlanContent(plan, "u2"); got != "odd trailing user" {
		t.Fatalf("odd trailing user content = %q", got)
	}
}

func TestLMEBuildPlanPreservesLeadingAssistantAsSingletonPair(t *testing.T) {
	replayCase := &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "leading-assistant-case",
		Sessions: []lmeReplaySession{{
			SessionIndex:    0,
			SessionID:       "leading-assistant-session",
			ObservationTime: "2025-01-01T00:00:00Z",
			Turns: []lmeReplayTurn{
				{
					TurnIndex: 0,
					TurnID:    "leading-assistant",
					Role:      "assistant",
					Content:   "leading assistant",
				},
				{TurnIndex: 1, TurnID: "following-user", Role: "user", Content: "user request"},
				{TurnIndex: 2, TurnID: "following-assistant", Role: "assistant", Content: "response"},
			},
		}},
	}
	planner := testLMEBuildPlanner(t, 100)
	plan, err := planner.buildCase(replayCase, "config-digest")
	if err != nil {
		t.Fatal(err)
	}
	pair := plan.Sessions[0].Pairs[0]
	if len(pair.SourceTurnIDs) != 1 || pair.SourceTurnIDs[0] != "leading-assistant" {
		t.Fatalf("leading assistant pair = %v", pair.SourceTurnIDs)
	}
	if got := plan.Sessions[0].Pairs[1].SourceTurnIDs; !reflect.DeepEqual(
		got,
		[]string{"following-user", "following-assistant"},
	) {
		t.Fatalf("following user/assistant pair = %v", got)
	}
	if got := concatenateLMEPlanContent(plan, "leading-assistant"); got != "leading assistant" {
		t.Fatalf("leading assistant content = %q", got)
	}
}

func TestLMEBuildPlanRejectsUnsupportedRole(t *testing.T) {
	replayCase := singleSessionReplayCase("user", "assistant")
	replayCase.Sessions[0].Turns[0].Role = "tool"
	planner := testLMEBuildPlanner(t, 100)
	_, err := planner.buildCase(replayCase, "config-digest")
	if err == nil || !strings.Contains(err.Error(), "unsupported role") {
		t.Fatalf("buildCase() error = %v, want unsupported role", err)
	}
}

func TestValidateLMEBuildCasePlanRejectsDuplicateSessionIDs(t *testing.T) {
	planner := testLMEBuildPlanner(t, 100)
	plan, err := planner.buildCase(singleSessionReplayCase("user", "assistant"), "config-digest")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := plan.Sessions[0]
	duplicate.SessionIndex = 1
	plan.Sessions = append(plan.Sessions, duplicate)
	if err := validateLMEBuildCasePlan(plan, planner.config.MaxTokens); err == nil ||
		!strings.Contains(err.Error(), "duplicate build session ID") {
		t.Fatalf("validateLMEBuildCasePlan() error = %v, want duplicate session rejection", err)
	}
}

func TestLMEBuildPlanAllowsStrictRetokenizationAccounting(t *testing.T) {
	tokenizer := retokenizingLMEPlanTokenizer{}
	planner, err := newLMEBuildPlanner(lmeBuildPlanConfig{
		Tokenizer:    "retokenizing-test",
		Model:        "test-model",
		MaxTokens:    2,
		ReplayDigest: "replay-digest",
	}, tokenizer.Tokenize)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.buildCase(singleSessionReplayCase("abc", ""), "config-digest")
	if err != nil {
		t.Fatal(err)
	}
	pair := plan.Sessions[0].Pairs[0]
	if pair.OriginalTokens == pair.FinalTokens {
		t.Fatalf("token counts unexpectedly equal: %+v", pair)
	}
	if pair.OriginalBytes != pair.FinalBytes {
		t.Fatalf("byte accounting is not lossless: %+v", pair)
	}
	if err := validateLMEBuildCasePlan(plan, planner.config.MaxTokens); err != nil {
		t.Fatalf("strictly retokenized plan rejected: %v", err)
	}
}

func TestLMEBuildPlanOverLimitPairIsLosslessWithoutLegacyLimit(t *testing.T) {
	userContent := strings.Repeat("界", 2500)
	assistantContent := strings.Repeat("a", 5000)
	replayCase := singleSessionReplayCase(userContent, assistantContent)
	planner := testLMEBuildPlanner(t, 100)
	plan, err := planner.buildCase(replayCase, "config-digest")
	if err != nil {
		t.Fatal(err)
	}
	pair := plan.Sessions[0].Pairs[0]
	if len(pair.Chunks) <= 1 {
		t.Fatalf("over-limit pair chunks = %d, want > 1", len(pair.Chunks))
	}
	if got := concatenateLMEPlanContent(plan, "user-turn"); got != userContent {
		t.Fatalf("user bytes changed: got=%d want=%d", len(got), len(userContent))
	}
	if got := concatenateLMEPlanContent(plan, "assistant-turn"); got != assistantContent {
		t.Fatalf("assistant bytes changed: got=%d want=%d", len(got), len(assistantContent))
	}
	if pair.OriginalBytes != pair.FinalBytes {
		t.Fatalf("lossless accounting mismatch: %+v", pair)
	}
	for _, chunk := range pair.Chunks {
		if chunk.TokenCount > planner.config.MaxTokens {
			t.Fatalf("chunk token count = %d, limit = %d", chunk.TokenCount, planner.config.MaxTokens)
		}
	}
	if pair.OriginalTokens <= 4000 {
		t.Fatalf("test input tokens = %d, want to exercise former 4000-token boundary", pair.OriginalTokens)
	}
	stats := plan.Stats
	if stats.SessionCount != 1 || stats.TurnCount != 2 || stats.PairCount != 1 ||
		stats.ChunkedSessionCount != 1 || stats.ChunkedPairCount != 1 ||
		stats.SplitTurnCount != 2 {
		t.Fatalf("build audit counters = %+v", stats)
	}
	if stats.MaxOriginalPairTokens != pair.OriginalTokens ||
		stats.MaxSessionTokens != pair.OriginalTokens ||
		stats.MaxOriginalTurnTokens <= 0 ||
		stats.MaxChunkTokens <= 0 || stats.MaxChunkTokens > planner.config.MaxTokens {
		t.Fatalf("build audit maxima = %+v", stats)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "runner_session_id") {
		t.Fatal("build chunks must not define synthetic Runner sessions")
	}
	for _, legacy := range []string{"content truncated", "longmemeval_mem0_content_truncated"} {
		if strings.Contains(strings.ToLower(string(data)), legacy) {
			t.Fatalf("build plan contains legacy truncation marker %q", legacy)
		}
	}
}

func TestLoadLMEBuildPlanRejectsConfigDigestMismatch(t *testing.T) {
	replayCase := singleSessionReplayCase("user", "assistant")
	replay := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:      lmeReplayVersion,
			ReplayDigest: "replay-digest",
			Cases:        []lmeArtifactEntry{{CaseID: replayCase.CaseID}},
		},
		Cases: map[string]*lmeReplayCase{replayCase.CaseID: replayCase},
	}
	planner := testLMEBuildPlanner(t, 100)
	configDigest, err := lmeJSONDigest(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "plan")
	if err := writeLMEBuildPlan(root, corpus); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, lmeReplayIndexFile)
	var index map[string]any
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	index["config_digest"] = "wrong"
	data, err = json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLMEBuildPlan(root); err == nil || !strings.Contains(err.Error(), "config digest mismatch") {
		t.Fatalf("loadLMEBuildPlan() error = %v, want config digest mismatch", err)
	}
}

func TestLoadLMEBuildPlanRejectsNonTurnPairProtocol(t *testing.T) {
	replayCase := singleSessionReplayCase("user", "assistant")
	replay := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:      lmeReplayVersion,
			ReplayDigest: "replay-digest",
			Cases:        []lmeArtifactEntry{{CaseID: replayCase.CaseID}},
		},
		Cases: map[string]*lmeReplayCase{replayCase.CaseID: replayCase},
	}
	planner := testLMEBuildPlanner(t, 100)
	configDigest, err := lmeJSONDigest(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	corpus.Index.Protocol = "unsupported"
	corpus.Index.BuildPlanDigest, err = lmeBuildPlanIndexDigest(corpus.Index)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "plan")
	if err := writeLMEBuildPlan(root, corpus); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLMEBuildPlan(root); err == nil ||
		!strings.Contains(err.Error(), "unsupported LongMemEval build protocol") {
		t.Fatalf("loadLMEBuildPlan() error = %v, want unsupported protocol", err)
	}
}

func TestLoadLMEBuildCasePlanIsReadOnlyAndRejectsTampering(t *testing.T) {
	replayCase := singleSessionReplayCase("user", "assistant")
	replay := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:      lmeReplayVersion,
			ReplayDigest: "replay-digest",
			Cases:        []lmeArtifactEntry{{CaseID: replayCase.CaseID}},
		},
		Cases: map[string]*lmeReplayCase{replayCase.CaseID: replayCase},
	}
	planner := testLMEBuildPlanner(t, 100)
	configDigest, err := lmeJSONDigest(planner.config)
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "plan")
	if err := writeLMEBuildPlan(root, corpus); err != nil {
		t.Fatal(err)
	}
	entry := corpus.Index.Cases[0]
	casePath := filepath.Join(root, filepath.FromSlash(entry.File))
	before, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := lmeRunConfig{
		BuildPlanRoot:   root,
		BuildPlanDigest: corpus.Index.BuildPlanDigest,
		ReplayDigest:    planner.config.ReplayDigest,
	}
	if _, err := loadLMEBuildCasePlan(cfg, replayCase.CaseID); err != nil {
		t.Fatalf("loadLMEBuildCasePlan() error = %v", err)
	}
	after, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("evaluator build-plan load modified the immutable artifact")
	}
	if err := os.WriteFile(casePath, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLMEBuildCasePlan(cfg, replayCase.CaseID); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("loadLMEBuildCasePlan() error = %v, want per-case digest mismatch", err)
	}
}

func testLMEBuildPlanner(
	t *testing.T,
	maxTokens int,
) *lmeBuildPlanner {
	t.Helper()
	tokenizer := runeLMEPlanTokenizer{}
	planner, err := newLMEBuildPlanner(lmeBuildPlanConfig{
		Tokenizer:    "rune-test",
		Model:        "test-model",
		MaxTokens:    maxTokens,
		ReplayDigest: "replay-digest",
	}, tokenizer.Tokenize)
	if err != nil {
		t.Fatalf("newLMEBuildPlanner() error = %v", err)
	}
	return planner
}

func singleSessionReplayCase(userContent string, assistantContent string) *lmeReplayCase {
	return &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "case-plan",
		Sessions: []lmeReplaySession{{
			SessionIndex:    0,
			SessionID:       "session-plan",
			ObservationTime: "2025-01-01T00:00:00Z",
			Turns: []lmeReplayTurn{
				{TurnIndex: 0, TurnID: "user-turn", Role: "user", Content: userContent},
				{TurnIndex: 1, TurnID: "assistant-turn", Role: "assistant", Content: assistantContent},
			},
		}},
	}
}

func concatenateLMEPlanContent(plan *lmeBuildCasePlan, sourceTurnID string) string {
	var builder strings.Builder
	for _, sessionPlan := range plan.Sessions {
		for _, pair := range sessionPlan.Pairs {
			for _, chunk := range pair.Chunks {
				for _, part := range chunk.Turns {
					if part.SourceTurnID == sourceTurnID {
						builder.WriteString(part.Content)
					}
				}
			}
		}
	}
	return builder.String()
}

type runeLMEPlanTokenizer struct{}

func (runeLMEPlanTokenizer) Tokenize(text string) ([]string, error) {
	pieces := make([]string, 0, utf8.RuneCountInString(text))
	for _, value := range text {
		pieces = append(pieces, string(value))
	}
	return pieces, nil
}

type retokenizingLMEPlanTokenizer struct{}

func (retokenizingLMEPlanTokenizer) Tokenize(text string) ([]string, error) {
	if text == "ab" {
		return []string{"ab"}, nil
	}
	pieces := make([]string, 0, len(text))
	for _, value := range text {
		pieces = append(pieces, string(value))
	}
	return pieces, nil
}

type noopLMEModel struct{}

func (noopLMEModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response)
	close(responses)
	return responses, nil
}

func (noopLMEModel) Info() model.Info { return model.Info{Name: "noop"} }
