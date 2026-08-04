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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

func TestLMEWriteImmutableJSONPublishesOneCompleteArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	values := []map[string]any{
		{"writer": "first", "items": []int{1, 2, 3}},
		{"writer": "second", "items": []int{4, 5, 6}},
	}
	var successes atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, value := range values {
		value := value
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := lmeWriteImmutableJSON(path, value); err == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful publishers = %d, want 1", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatalf("published artifact is incomplete: %v", err)
	}
	matched := false
	for _, value := range values {
		want, err := lmeMarshalArtifact(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(data, want) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("published artifact does not match any complete writer: %s", data)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".artifact.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary artifacts were not cleaned up: %v", matches)
	}
}

func TestLMEWriteImmutableJSONDoesNotOverwriteExistingArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := lmeWriteImmutableJSON(path, map[string]string{"value": "original"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lmeWriteImmutableJSON(path, map[string]string{"value": "replacement"}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second lmeWriteImmutableJSON() error = %v, want immutable conflict", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing immutable artifact was overwritten")
	}
}

func TestLMEWriteImmutableJSONAcceptsIdenticalExistingArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	value := map[string]string{"value": "stable"}
	if err := lmeWriteImmutableJSON(path, value); err != nil {
		t.Fatal(err)
	}
	if err := lmeWriteImmutableJSON(path, value); err != nil {
		t.Fatalf("identical immutable publication failed: %v", err)
	}
}

func TestLMEWriteImmutableJSONRejectsExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("protected\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "artifact.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := lmeWriteImmutableJSON(path, map[string]string{"value": "replacement"}); err == nil ||
		!strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("lmeWriteImmutableJSON() error = %v, want non-regular artifact rejection", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "protected\n" {
		t.Fatalf("symlink target was modified: %q", data)
	}
}

func TestBuildLMEReplayCaseIsDeterministicAndLeakFree(t *testing.T) {
	instance := testLMEArtifactInstance()
	first, err := buildLMEReplayCase(instance)
	if err != nil {
		t.Fatalf("buildLMEReplayCase() error = %v", err)
	}
	second, err := buildLMEReplayCase(instance)
	if err != nil {
		t.Fatalf("second buildLMEReplayCase() error = %v", err)
	}
	firstJSON, err := lmeMarshalArtifact(first)
	if err != nil {
		t.Fatalf("lmeMarshalArtifact(first) error = %v", err)
	}
	secondJSON, err := lmeMarshalArtifact(second)
	if err != nil {
		t.Fatalf("lmeMarshalArtifact(second) error = %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("canonical replay bytes are not deterministic")
	}
	for _, forbidden := range []string{
		"question", "gold answer", "answer_session_ids", "has_answer", "question_date",
	} {
		if strings.Contains(strings.ToLower(string(firstJSON)), forbidden) {
			t.Fatalf("canonical replay contains forbidden QA field or value %q", forbidden)
		}
	}
	if got := first.Sessions[0].SessionID; got != "session-early" {
		t.Fatalf("first session = %q, want chronological session-early", got)
	}
	if got := first.Sessions[1].Turns[0].Content; got != "  preserve surrounding bytes  " {
		t.Fatalf("turn content = %q, want exact source bytes", got)
	}
	if first.Sessions[0].Turns[0].TurnID == first.Sessions[1].Turns[0].TurnID {
		t.Fatal("stable turn IDs are not unique")
	}
}

func TestLMEReplayStrictDecoderRejectsQALeakage(t *testing.T) {
	for _, field := range []string{
		"question",
		"answer",
		"gold_evidence",
		"answer_session_ids",
		"has_answer",
	} {
		t.Run(field, func(t *testing.T) {
			payload := []byte(`{
  "version": 2,
  "case_id": "case-1",
  "sessions": [],
  "` + field + `": "leaked QA data"
}`)
			var replayCase lmeReplayCase
			if err := lmeDecodeStrict(payload, &replayCase); err == nil ||
				!strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("lmeDecodeStrict() error = %v, want unknown field rejection", err)
			}
		})
	}
}

func TestLMECaseArtifactFileDoesNotTrustCaseIDAsPath(t *testing.T) {
	file := lmeCaseArtifactFile("../../outside")
	if filepath.Dir(file) != "cases" || strings.Contains(file, "outside") || strings.Contains(file, "..") {
		t.Fatalf("lmeCaseArtifactFile() = %q, want digest-based path under cases", file)
	}
}

func TestEnsureLMEReplayArtifactsIsImmutableAndVerifiesDigests(t *testing.T) {
	root := t.TempDir()
	datasetPath := filepath.Join(root, "dataset.json")
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(datasetPath, []byte("dataset-v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("manifest-v1"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := lmeRunConfig{
		DatasetPath:  datasetPath,
		ManifestPath: manifestPath,
		ReplayRoot:   filepath.Join(root, "replay"),
	}
	instances := []*dataset.LongMemEvalInstance{testLMEArtifactInstance()}
	first, err := ensureLMEReplayArtifacts(cfg, instances)
	if err != nil {
		t.Fatalf("ensureLMEReplayArtifacts() error = %v", err)
	}
	entry := first.Index.Cases[0]
	casePath := filepath.Join(cfg.ReplayRoot, filepath.FromSlash(entry.File))
	before, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureLMEReplayArtifacts(cfg, instances)
	if err != nil {
		t.Fatalf("second ensureLMEReplayArtifacts() error = %v", err)
	}
	after, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || first.Index.ReplayDigest != second.Index.ReplayDigest {
		t.Fatal("existing immutable replay was rewritten")
	}
	if err := os.WriteFile(datasetPath, []byte("dataset-v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureLMEReplayArtifacts(cfg, instances); err == nil {
		t.Fatal("source digest mismatch was accepted")
	}
	unchanged, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, unchanged) {
		t.Fatal("mismatched source overwrote immutable replay")
	}
}

func TestLoadLMEReplayCorpusRejectsTamperedCase(t *testing.T) {
	root := t.TempDir()
	datasetPath := filepath.Join(root, "dataset.json")
	if err := os.WriteFile(datasetPath, []byte("dataset"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := lmeRunConfig{DatasetPath: datasetPath, ReplayRoot: filepath.Join(root, "replay")}
	corpus, err := ensureLMEReplayArtifacts(cfg, []*dataset.LongMemEvalInstance{testLMEArtifactInstance()})
	if err != nil {
		t.Fatal(err)
	}
	casePath := filepath.Join(cfg.ReplayRoot, filepath.FromSlash(corpus.Index.Cases[0].File))
	if err := os.WriteFile(casePath, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLMEReplayCorpus(cfg.ReplayRoot); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("loadLMEReplayCorpus() error = %v, want digest mismatch", err)
	}
}

func TestBuildLMEReplayCaseRejectsDuplicateSessionIDs(t *testing.T) {
	inst := testLMEArtifactInstance()
	inst.HaystackSessionIDs = []string{"duplicate-session", "duplicate-session"}
	if _, err := buildLMEReplayCase(inst); err == nil ||
		!strings.Contains(err.Error(), "duplicate LongMemEval replay session ID") {
		t.Fatalf("buildLMEReplayCase() error = %v, want duplicate session rejection", err)
	}
}

func testLMEArtifactInstance() *dataset.LongMemEvalInstance {
	return &dataset.LongMemEvalInstance{
		QuestionID:       "case-1",
		QuestionType:     "single-session-user",
		Question:         "question that must not leak",
		QuestionDate:     "2025/01/03 (Fri) 00:00",
		Answer:           "gold answer",
		AnswerSessionIDs: []string{"session-early"},
		HaystackDates: []string{
			"2025/01/02 (Thu) 00:00",
			"2025/01/01 (Wed) 00:00",
		},
		HaystackSessionIDs: []string{"session-late", "session-early"},
		HaystackSessions: [][]dataset.LongMemEvalTurn{
			{
				{Role: "user", Content: "  preserve surrounding bytes  ", HasAnswer: true},
				{Role: "assistant", Content: "late reply"},
			},
			{
				{Role: "user", Content: "early user"},
				{Role: "assistant", Content: "early assistant"},
			},
		},
	}
}

func TestExecuteLMEBuildCaseUsesSamePlanForAutoAndMem0(t *testing.T) {
	casePlan := runtimeLMECasePlan(t)
	cfg := lmeRunConfig{
		BuildMaxTokens:     100,
		ReplayDigest:       casePlan.ReplayDigest,
		AutoExtractionWait: time.Second,
	}
	auto := &recordingLMEBuildMemory{}
	if err := executeLMEBuildCase(context.Background(), cfg, casePlan, lmeBuildExecutionOptions{
		AppName:       lmeAppAuto,
		MemoryService: auto,
	}); err != nil {
		t.Fatalf("execute auto build case: %v", err)
	}
	mem0 := &recordingLMEBuildIngestor{}
	if err := executeLMEBuildCase(context.Background(), cfg, casePlan, lmeBuildExecutionOptions{
		AppName:  lmeAppMem0,
		Ingestor: mem0,
		Metadata: lmeMem0BuildMetadata(casePlan.CaseID),
	}); err != nil {
		t.Fatalf("execute mem0 build case: %v", err)
	}
	if !reflect.DeepEqual(auto.calls, mem0.calls) {
		t.Fatalf("Auto and Mem0 consumed different chunks:\nauto=%v\nmem0=%v", auto.calls, mem0.calls)
	}
	if got, want := len(auto.calls), casePlan.Stats.ChunkCount; got != want {
		t.Fatalf("build acknowledgements = %d, want one per chunk (%d)", got, want)
	}
	if auto.captureErr != nil {
		t.Fatalf("capture auto build input: %v", auto.captureErr)
	}
	wantCalls := [][]string{{"user one", "assistant one"}, {"odd user"}}
	if !reflect.DeepEqual(auto.calls, wantCalls) {
		t.Fatalf("runtime calls = %v, want %v", auto.calls, wantCalls)
	}
	wantCumulative := [][]string{
		{"user one", "assistant one"},
		{"user one", "assistant one", "odd user"},
	}
	if !reflect.DeepEqual(auto.cumulativeCalls, wantCumulative) ||
		!reflect.DeepEqual(mem0.cumulativeCalls, wantCumulative) {
		t.Fatalf(
			"runtime sessions did not accumulate by source session: auto=%v mem0=%v want=%v",
			auto.cumulativeCalls,
			mem0.cumulativeCalls,
			wantCumulative,
		)
	}
	wantSessionIDs := lmeRuntimeSourceSessionIDs(casePlan)
	if !reflect.DeepEqual(auto.sessionIDs, wantSessionIDs) ||
		!reflect.DeepEqual(mem0.sessionIDs, wantSessionIDs) {
		t.Fatalf(
			"runtime session IDs differ from plan: auto=%v mem0=%v want=%v",
			auto.sessionIDs,
			mem0.sessionIDs,
			wantSessionIDs,
		)
	}
	if !reflect.DeepEqual(mem0.runIDs, wantSessionIDs) {
		t.Fatalf("Mem0 run IDs = %v, want source sessions %v", mem0.runIDs, wantSessionIDs)
	}
	wantObservationTime, err := time.Parse(time.RFC3339Nano, casePlan.Sessions[0].ObservationTime)
	if err != nil {
		t.Fatal(err)
	}
	for callIndex, referenceDate := range auto.referenceDates {
		if !referenceDate.Equal(wantObservationTime) {
			t.Fatalf(
				"auto call %d reference date = %s, want %s",
				callIndex,
				referenceDate,
				wantObservationTime,
			)
		}
		if got := mem0.observationTimes[callIndex]; got != casePlan.Sessions[0].ObservationTime {
			t.Fatalf(
				"mem0 call %d observation time = %q, want %q",
				callIndex,
				got,
				casePlan.Sessions[0].ObservationTime,
			)
		}
	}
	if !reflect.DeepEqual(auto.eventTimes, mem0.eventTimes) {
		t.Fatalf("Auto and Mem0 event times differ: auto=%v mem0=%v", auto.eventTimes, mem0.eventTimes)
	}
	var previous time.Time
	for _, callTimes := range auto.eventTimes {
		for _, eventTime := range callTimes {
			if !eventTime.After(wantObservationTime) {
				t.Fatalf("event time = %s, want after observation time %s", eventTime, wantObservationTime)
			}
			if !previous.IsZero() && !eventTime.After(previous) {
				t.Fatalf("event times are not strictly increasing: previous=%s current=%s", previous, eventTime)
			}
			previous = eventTime
		}
	}
}

func TestExecuteLMEBuildCasePreservesLeadingAssistant(t *testing.T) {
	replayCase := &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "assistant-first-runtime",
		Sessions: []lmeReplaySession{{
			SessionIndex:    0,
			SessionID:       "assistant-first-session",
			ObservationTime: "2025-01-03T12:00:00Z",
			Turns: []lmeReplayTurn{{
				TurnIndex: 0,
				TurnID:    "assistant-first-turn",
				Role:      "assistant",
				Content:   "prefilled case summary",
			}},
		}},
	}
	planner := testLMEBuildPlanner(t, 100)
	casePlan, err := planner.buildCase(replayCase, "runtime-config")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingLMEBuildMemory{}
	err = executeLMEBuildCase(context.Background(), lmeRunConfig{
		BuildMaxTokens:     100,
		ReplayDigest:       casePlan.ReplayDigest,
		AutoExtractionWait: time.Second,
	}, casePlan, lmeBuildExecutionOptions{AppName: lmeAppAuto, MemoryService: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorder.calls; !reflect.DeepEqual(got, [][]string{{"prefilled case summary"}}) {
		t.Fatalf("assistant-first runtime calls = %v", got)
	}
	if got := recorder.sessionIDs; !reflect.DeepEqual(got, []string{"assistant-first-session"}) {
		t.Fatalf("assistant-first source sessions = %v", got)
	}
}

func TestExecuteLMEBuildCaseRunsOncePerTokenBoundedChunk(t *testing.T) {
	planner := testLMEBuildPlanner(t, 5)
	casePlan, err := planner.buildCase(
		singleSessionReplayCase("abcdefghijkl", "mnopqrst"),
		"runtime-config",
	)
	if err != nil {
		t.Fatal(err)
	}
	if casePlan.Stats.ChunkCount < 2 {
		t.Fatalf("chunk count = %d, want a split pair", casePlan.Stats.ChunkCount)
	}
	recorder := &recordingLMEBuildIngestor{}
	err = executeLMEBuildCase(context.Background(), lmeRunConfig{
		BuildMaxTokens: 5,
		ReplayDigest:   casePlan.ReplayDigest,
	}, casePlan, lmeBuildExecutionOptions{AppName: lmeAppMem0, Ingestor: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != casePlan.Stats.ChunkCount {
		t.Fatalf("runtime calls = %d, chunks = %d", len(recorder.calls), casePlan.Stats.ChunkCount)
	}
	if !reflect.DeepEqual(recorder.sessionIDs, lmeRuntimeSourceSessionIDs(casePlan)) {
		t.Fatalf("runtime session IDs = %v", recorder.sessionIDs)
	}
}

func TestExecuteLMEBuildCaseReusesSourceSessionAndIsolatesOtherSessions(t *testing.T) {
	replayCase := &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "continuous-runtime-case",
		Sessions: []lmeReplaySession{
			{
				SessionIndex:    0,
				SessionID:       "source-session-one",
				ObservationTime: "2025-01-01T12:00:00Z",
				Turns: []lmeReplayTurn{
					{TurnIndex: 0, TurnID: "u1", Role: "user", Content: "user one"},
					{TurnIndex: 1, TurnID: "a1", Role: "assistant", Content: "assistant one"},
					{TurnIndex: 2, TurnID: "u2", Role: "user", Content: "user two"},
					{TurnIndex: 3, TurnID: "a2", Role: "assistant", Content: "assistant two"},
				},
			},
			{
				SessionIndex:    1,
				SessionID:       "source-session-two",
				ObservationTime: "2025-01-02T12:00:00Z",
				Turns: []lmeReplayTurn{
					{TurnIndex: 0, TurnID: "u3", Role: "user", Content: "user three"},
					{TurnIndex: 1, TurnID: "a3", Role: "assistant", Content: "assistant three"},
				},
			},
		},
	}
	planner := testLMEBuildPlanner(t, 100)
	casePlan, err := planner.buildCase(replayCase, "runtime-config")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingLMEBuildMemory{}
	err = executeLMEBuildCase(context.Background(), lmeRunConfig{
		BuildMaxTokens:     100,
		ReplayDigest:       casePlan.ReplayDigest,
		AutoExtractionWait: time.Second,
	}, casePlan, lmeBuildExecutionOptions{AppName: lmeAppAuto, MemoryService: recorder})
	if err != nil {
		t.Fatal(err)
	}
	wantSessionIDs := []string{"source-session-one", "source-session-one", "source-session-two"}
	if !reflect.DeepEqual(recorder.sessionIDs, wantSessionIDs) {
		t.Fatalf("runtime session IDs = %v, want %v", recorder.sessionIDs, wantSessionIDs)
	}
	wantDeltas := [][]string{
		{"user one", "assistant one"},
		{"user two", "assistant two"},
		{"user three", "assistant three"},
	}
	if !reflect.DeepEqual(recorder.calls, wantDeltas) {
		t.Fatalf("runtime deltas = %v, want %v", recorder.calls, wantDeltas)
	}
	wantCumulative := [][]string{
		{"user one", "assistant one"},
		{"user one", "assistant one", "user two", "assistant two"},
		{"user three", "assistant three"},
	}
	if !reflect.DeepEqual(recorder.cumulativeCalls, wantCumulative) {
		t.Fatalf("runtime transcripts = %v, want %v", recorder.cumulativeCalls, wantCumulative)
	}
}

func TestExecuteLMEBuildCasePropagatesPerPairAcknowledgementFailure(t *testing.T) {
	casePlan := runtimeLMECasePlan(t)
	ingestor := &recordingLMEBuildIngestor{failAt: 2}
	err := executeLMEBuildCase(context.Background(), lmeRunConfig{
		BuildMaxTokens: 100,
		ReplayDigest:   casePlan.ReplayDigest,
	}, casePlan, lmeBuildExecutionOptions{AppName: lmeAppMem0, Ingestor: ingestor})
	if err == nil || !strings.Contains(err.Error(), "remote ingest failed") {
		t.Fatalf("executeLMEBuildCase() error = %v, want per-pair acknowledgement failure", err)
	}
	if len(ingestor.calls) != 2 {
		t.Fatalf("ingestor calls after failure = %d, want 2", len(ingestor.calls))
	}
}

func TestLMEReplayIngestorDetectsMissingAcknowledgement(t *testing.T) {
	wrapper := newLMEReplayIngestor(&recordingLMEBuildIngestor{})
	generation := wrapper.expect("source-session", nil)
	if err := wrapper.verify(generation); err == nil || !strings.Contains(err.Error(), "did not acknowledge") {
		t.Fatalf("verify() error = %v, want missing acknowledgement", err)
	}
}

func TestLMEReplaySessionServiceRejectsUnexpectedExtraMessage(t *testing.T) {
	service := &lmeReplaySessionService{}
	generation := service.expect(
		[]lmeReplayTurn{{
			TurnIndex: 0,
			TurnID:    "turn-0",
			Role:      string(model.RoleUser),
			Content:   "hello",
		}},
		"source-session",
		time.Now(),
	)
	userEvent := event.NewResponseEvent("invocation", "user", &model.Response{
		Choices: []model.Choice{{Message: model.NewUserMessage("hello")}},
	})
	if _, err := service.cleanReplayEvents("source-session", userEvent); err != nil {
		t.Fatalf("clean expected user event: %v", err)
	}
	assistantEvent := event.NewResponseEvent("invocation", lmeSeedAgentName, &model.Response{
		Choices: []model.Choice{{Message: model.NewAssistantMessage("unexpected")}},
	})
	_, err := service.cleanReplayEvents("source-session", assistantEvent)
	if err == nil || !strings.Contains(err.Error(), "unexpected extra message") {
		t.Fatalf("clean extra assistant event error = %v, want unexpected extra message", err)
	}
	if err := service.verify(generation); err == nil || !strings.Contains(err.Error(), "unexpected extra message") {
		t.Fatalf("verify() error = %v, want unexpected extra message", err)
	}
}

func TestValidateLMEResumeBuildInputsRejectsDigestChanges(t *testing.T) {
	checkpoint := newLMERunResult("auto", "pgvector", lmeRunConfig{
		ReplayDigest:    "replay-a",
		BuildPlanDigest: "plan-a",
	}, 1)
	if err := validateLMEResumeBuildInputs(checkpoint, checkpoint.Metadata.Config); err != nil {
		t.Fatalf("matching resume inputs rejected: %v", err)
	}
	changed := checkpoint.Metadata.Config
	changed.BuildPlanDigest = "plan-b"
	if err := validateLMEResumeBuildInputs(checkpoint, changed); err == nil {
		t.Fatal("build plan digest mismatch accepted")
	}
}

func runtimeLMECasePlan(t *testing.T) *lmeBuildCasePlan {
	t.Helper()
	replayCase := &lmeReplayCase{
		Version: lmeReplayVersion,
		CaseID:  "runtime-case",
		Sessions: []lmeReplaySession{{
			SessionIndex:    0,
			SessionID:       "runtime-session",
			ObservationTime: "2025-01-01T12:00:00Z",
			Turns: []lmeReplayTurn{
				{TurnIndex: 0, TurnID: "u1", Role: "user", Content: "user one"},
				{TurnIndex: 1, TurnID: "a1", Role: "assistant", Content: "assistant one"},
				{TurnIndex: 2, TurnID: "u2", Role: "user", Content: "odd user"},
			},
		}},
	}
	planner := testLMEBuildPlanner(t, 100)
	plan, err := planner.buildCase(replayCase, "runtime-config")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func lmeRuntimeSourceSessionIDs(plan *lmeBuildCasePlan) []string {
	var ids []string
	for _, sessionPlan := range plan.Sessions {
		for _, pair := range sessionPlan.Pairs {
			for range pair.Chunks {
				ids = append(ids, sessionPlan.SessionID)
			}
		}
	}
	return ids
}

type recordingLMEBuildMemory struct {
	memory.Service
	calls           [][]string
	cumulativeCalls [][]string
	sessionIDs      []string
	referenceDates  []time.Time
	eventTimes      [][]time.Time
	captureErr      error
}

func (*recordingLMEBuildMemory) Close() error { return nil }

func (r *recordingLMEBuildMemory) EnqueueAutoMemoryJob(
	ctx context.Context,
	sess *session.Session,
) error {
	delta, eventTimes, err := lmeSessionDelta(sess)
	if err != nil {
		return err
	}
	r.calls = append(r.calls, delta)
	r.cumulativeCalls = append(r.cumulativeCalls, lmeSessionContents(sess))
	r.sessionIDs = append(r.sessionIDs, sess.ID)
	referenceDate, ok := extractor.ReferenceDateFromContext(ctx)
	if !ok && r.captureErr == nil {
		r.captureErr = errors.New("reference date is missing")
	}
	r.referenceDates = append(r.referenceDates, referenceDate)
	r.eventTimes = append(r.eventTimes, eventTimes)
	lmeMarkBuildAcknowledged(sess)
	return nil
}

type recordingLMEBuildIngestor struct {
	calls            [][]string
	cumulativeCalls  [][]string
	sessionIDs       []string
	eventTimes       [][]time.Time
	runIDs           []string
	observationTimes []string
	failAt           int
}

func (r *recordingLMEBuildIngestor) IngestSession(
	_ context.Context,
	sess *session.Session,
	opts ...session.IngestOption,
) error {
	delta, eventTimes, err := lmeSessionDelta(sess)
	if err != nil {
		return err
	}
	r.calls = append(r.calls, delta)
	r.cumulativeCalls = append(r.cumulativeCalls, lmeSessionContents(sess))
	r.sessionIDs = append(r.sessionIDs, sess.ID)
	r.eventTimes = append(r.eventTimes, eventTimes)
	var ingestOpts session.IngestOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&ingestOpts)
		}
	}
	r.runIDs = append(r.runIDs, ingestOpts.RunID)
	observationTime, _ := ingestOpts.Metadata[lmeMem0MetadataObservationTime].(string)
	r.observationTimes = append(r.observationTimes, observationTime)
	if r.failAt > 0 && len(r.calls) == r.failAt {
		return errors.New("remote ingest failed")
	}
	lmeMarkBuildAcknowledged(sess)
	return nil
}

func lmeSessionContents(sess *session.Session) []string {
	if sess == nil {
		return nil
	}
	var contents []string
	sess.EventMu.RLock()
	defer sess.EventMu.RUnlock()
	for _, evt := range sess.Events {
		if evt.Response == nil || len(evt.Response.Choices) == 0 {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Message.Content != "" {
				contents = append(contents, choice.Message.Content)
			}
		}
	}
	return contents
}

func lmeSessionDelta(sess *session.Session) ([]string, []time.Time, error) {
	if sess == nil {
		return nil, nil, nil
	}
	since, err := lmeSessionWatermark(sess)
	if err != nil {
		return nil, nil, err
	}
	var contents []string
	var timestamps []time.Time
	sess.EventMu.RLock()
	defer sess.EventMu.RUnlock()
	for _, evt := range sess.Events {
		if !since.IsZero() && !evt.Timestamp.After(since) {
			continue
		}
		if evt.Response == nil || len(evt.Response.Choices) == 0 {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Message.Content == "" {
				continue
			}
			contents = append(contents, choice.Message.Content)
			timestamps = append(timestamps, evt.Timestamp)
		}
	}
	return contents, timestamps, nil
}

func lmeMarkBuildAcknowledged(sess *session.Session) {
	latest := latestLMEReplayEventTime(sess)
	if latest.IsZero() {
		return
	}
	sess.SetState(
		memory.SessionStateKeyAutoMemoryLastExtractAt,
		[]byte(latest.UTC().Format(time.RFC3339Nano)),
	)
}
