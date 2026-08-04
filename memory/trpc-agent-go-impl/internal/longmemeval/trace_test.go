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
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type lmeMemoryTraceSink struct {
	mu      sync.Mutex
	records []lmeTraceRecord
	closed  bool
	err     error
}

func (s *lmeMemoryTraceSink) Write(record *lmeTraceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var clone lmeTraceRecord
	if err := json.Unmarshal(data, &clone); err != nil {
		return err
	}
	s.records = append(s.records, clone)
	return nil
}

func (s *lmeMemoryTraceSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.err
}

type lmeTraceTestExtractor struct {
	calls      int
	operations []*extractor.Operation
	err        error
	prompt     string
	modelSet   bool
	enabled    map[string]struct{}
}

func (e *lmeTraceTestExtractor) Extract(
	context.Context,
	[]model.Message,
	[]*memory.Entry,
) ([]*extractor.Operation, error) {
	e.calls++
	return e.operations, e.err
}

func (*lmeTraceTestExtractor) ShouldExtract(*extractor.ExtractionContext) bool { return true }
func (e *lmeTraceTestExtractor) SetPrompt(prompt string)                       { e.prompt = prompt }
func (e *lmeTraceTestExtractor) SetModel(model.Model)                          { e.modelSet = true }
func (*lmeTraceTestExtractor) Metadata() map[string]any {
	return map[string]any{"name": "test"}
}
func (e *lmeTraceTestExtractor) SetEnabledTools(enabled map[string]struct{}) {
	e.enabled = enabled
}

type lmeTraceTestReader struct {
	entries     []*memory.Entry
	readErr     error
	readCalls   int
	searchCalls int
}

func (r *lmeTraceTestReader) ReadMemories(
	context.Context,
	memory.UserKey,
	int,
) ([]*memory.Entry, error) {
	r.readCalls++
	return r.entries, r.readErr
}

func (r *lmeTraceTestReader) SearchMemories(
	context.Context,
	memory.UserKey,
	string,
	...memory.SearchOption,
) ([]*memory.Entry, error) {
	r.searchCalls++
	return nil, nil
}

func TestTracingExtractorAndPersistenceArePassive(t *testing.T) {
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case-1", lmeTraceContentHash, sink)
	trace.setInitialSnapshot(nil)
	source := lmeBuildTraceSource{
		SourceID:  "source-1",
		SessionID: "session-1",
		TurnIDs:   []string{"turn-1", "turn-2"},
	}
	inner := &lmeTraceTestExtractor{operations: []*extractor.Operation{{
		Type:   extractor.OperationAdd,
		Memory: "Alice met Bob at 4pm.",
	}}}
	wrapper := newLMETracingExtractor(inner, extractor.UpdatePolicyMergeSimilar)
	ctx := withLMECaseTrace(context.Background(), trace)
	ctx = withLMEBuildTraceSource(ctx, source)
	operations, err := wrapper.Extract(ctx, []model.Message{
		model.NewUserMessage("Alice met Bob at 4pm."),
	}, nil)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if inner.calls != 1 || len(operations) != 1 {
		t.Fatalf("delegate calls/operations = %d/%d, want 1/1", inner.calls, len(operations))
	}

	reader := &lmeTraceTestReader{entries: []*memory.Entry{{
		ID: "memory-1",
		Memory: &memory.Memory{
			Memory: "Alice met Bob at 4pm.",
		},
	}}}
	if err := trace.recordPersistence(
		ctx,
		source,
		reader,
		memory.UserKey{AppName: "app", UserID: "user"},
		nil,
	); err != nil {
		t.Fatalf("recordPersistence() error = %v", err)
	}
	if reader.readCalls != 1 {
		t.Fatalf("ReadMemories() calls = %d, want 1", reader.readCalls)
	}
	if reader.searchCalls != 0 {
		t.Fatalf("SearchMemories() calls = %d, want 0", reader.searchCalls)
	}
	if len(sink.records) != 2 {
		t.Fatalf("trace records = %d, want 2", len(sink.records))
	}
	persistence := sink.records[1].Persistence
	if persistence == nil || !persistence.Acknowledged || len(persistence.Diff.Added) != 1 {
		t.Fatalf("persistence record = %#v", persistence)
	}
	if persistence.ActualOperations != lmeTraceEffectiveOperationsUnavailable {
		t.Fatalf("actual operations = %q", persistence.ActualOperations)
	}
}

func TestTraceRecordsUnavailableExtractionWithoutGuessingOperations(t *testing.T) {
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case-mem0", lmeTraceContentHash, sink)
	source := lmeBuildTraceSource{
		SourceID:  "source-mem0",
		SessionID: "session-mem0",
		TurnIDs:   []string{"turn-1", "turn-2"},
	}
	const reason = "Mem0 OSS does not expose extraction operations"
	if err := trace.recordExtractionUnavailable(source, []model.Message{
		model.NewUserMessage("Alice met Bob."),
	}, reason); err != nil {
		t.Fatalf("recordExtractionUnavailable() error = %v", err)
	}
	if len(sink.records) != 1 {
		t.Fatalf("trace records = %d, want 1", len(sink.records))
	}
	extraction := sink.records[0].Extraction
	if extraction == nil {
		t.Fatal("extraction record is nil")
	}
	if extraction.OperationCount != 0 || len(extraction.Operations) != 0 {
		t.Fatalf("unavailable extraction invented operations: %#v", extraction)
	}
	if extraction.EffectiveOperations != lmeTraceEffectiveOperationsUnavailable ||
		extraction.UnavailableReason != reason {
		t.Fatalf("unavailable extraction metadata = %#v", extraction)
	}
	if extraction.Input.SHA256 == "" || extraction.Input.Value != "" {
		t.Fatalf("hash-mode extraction input = %#v", extraction.Input)
	}
	if err := trace.recordExtractionUnavailable(source, nil, ""); err == nil {
		t.Fatal("recordExtractionUnavailable() accepted an empty reason")
	}
}

func TestTraceManagerLifecycle(t *testing.T) {
	if _, err := newLMETraceManager("invalid", false); err == nil {
		t.Fatal("newLMETraceManager() accepted invalid content mode")
	}
	manager, err := newLMETraceManager("", false)
	if err != nil {
		t.Fatalf("newLMETraceManager() error = %v", err)
	}
	if manager.mode != lmeTraceContentHash {
		t.Fatalf("default mode = %q", manager.mode)
	}
	if err := manager.setRoot(""); err == nil {
		t.Fatal("setRoot() accepted an empty root")
	}
	root := t.TempDir()
	if err := manager.setRoot(root); err != nil {
		t.Fatalf("setRoot() error = %v", err)
	}
	trace, err := manager.beginCase("case/unsafe")
	if err != nil {
		t.Fatalf("beginCase() error = %v", err)
	}
	if err := trace.finish(&lmeCaseResult{Correct: true}, nil); err != nil {
		t.Fatalf("finish() error = %v", err)
	}
	path := filepath.Join(root, "build_trace", "case_unsafe.attempt-0001.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("trace path %s: %v", path, err)
	}
	second, err := manager.beginCase("case/unsafe")
	if err != nil {
		t.Fatalf("beginCase() second attempt error = %v", err)
	}
	if err := second.finish(&lmeCaseResult{Correct: true}, nil); err != nil {
		t.Fatalf("second finish() error = %v", err)
	}
	secondPath := filepath.Join(root, "build_trace", "case_unsafe.attempt-0002.jsonl")
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("second trace path %s: %v", secondPath, err)
	}
	manager.writerFactory = func(string, bool) (lmeTraceSink, error) {
		return nil, errors.New("open failed")
	}
	if _, err := manager.beginCase("new-case"); err == nil {
		t.Fatal("beginCase() ignored writer factory failure")
	}
}

func TestEvaluatorTraceRootConfiguration(t *testing.T) {
	manager, err := newLMETraceManager(lmeTraceContentNone, false)
	if err != nil {
		t.Fatalf("newLMETraceManager() error = %v", err)
	}
	if err := (&lmeAutoEvaluator{trace: manager}).setLMETraceRoot(t.TempDir()); err != nil {
		t.Fatalf("auto setLMETraceRoot() error = %v", err)
	}
	if err := (&lmeMem0OSSEvaluator{trace: manager}).setLMETraceRoot(t.TempDir()); err != nil {
		t.Fatalf("Mem0 setLMETraceRoot() error = %v", err)
	}
	if err := (&lmeAutoEvaluator{}).setLMETraceRoot(t.TempDir()); err == nil {
		t.Fatal("auto setLMETraceRoot() accepted nil manager")
	}
	if err := (&lmeMem0OSSEvaluator{}).setLMETraceRoot(t.TempDir()); err == nil {
		t.Fatal("Mem0 setLMETraceRoot() accepted nil manager")
	}
}

func TestTracingExtractorDelegatesConfiguration(t *testing.T) {
	if newLMETracingExtractor(nil, extractor.UpdatePolicyMergeSimilar) != nil {
		t.Fatal("newLMETracingExtractor(nil) returned non-nil")
	}
	inner := &lmeTraceTestExtractor{}
	wrapper := newLMETracingExtractor(
		inner,
		extractor.UpdatePolicyMergeSimilar,
	).(*lmeTracingExtractor)
	if !wrapper.ShouldExtract(&extractor.ExtractionContext{}) {
		t.Fatal("ShouldExtract() did not delegate")
	}
	wrapper.SetPrompt("custom")
	wrapper.SetModel(nil)
	wrapper.SetEnabledTools(map[string]struct{}{memory.AddToolName: {}})
	if inner.prompt != "custom" || !inner.modelSet {
		t.Fatalf("configuration was not delegated: %#v", inner)
	}
	if _, ok := inner.enabled[memory.AddToolName]; !ok {
		t.Fatal("enabled tools were not delegated")
	}
	if got := wrapper.Metadata()["name"]; got != "test" {
		t.Fatalf("Metadata()[name] = %v", got)
	}
}

func TestMemoryFingerprintIsDeterministic(t *testing.T) {
	eventTime := time.Date(2026, 7, 16, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	left := &memory.Entry{Memory: &memory.Memory{
		Memory:       "Alice met Bob.",
		Topics:       []string{"Bob", "Alice"},
		Kind:         memory.KindEpisode,
		EventTime:    &eventTime,
		Participants: []string{"Bob", "Alice"},
	}}
	right := &memory.Entry{Memory: &memory.Memory{
		Memory:       "Alice met Bob.",
		Topics:       []string{"Alice", "Bob"},
		Kind:         memory.KindEpisode,
		EventTime:    &eventTime,
		Participants: []string{"Alice", "Bob"},
	}}
	if got, want := fingerprintLMEMemoryEntry(left), fingerprintLMEMemoryEntry(right); got != want {
		t.Fatalf("fingerprints differ: %s != %s", got, want)
	}
	right.Memory.Location = "Paris"
	if fingerprintLMEMemoryEntry(left) == fingerprintLMEMemoryEntry(right) {
		t.Fatal("semantic metadata change did not change fingerprint")
	}
}

func TestBuildTraceSourceIDIsStable(t *testing.T) {
	turnIDs := []string{"session_1", "session_2"}
	left := newLMEBuildTraceSource(
		"case", "session", "runner", turnIDs,
		"chunk-1", "2026-07-16T00:00:00Z",
	)
	right := newLMEBuildTraceSource(
		"case", "session", "runner", turnIDs,
		"chunk-1", "2026-07-16T00:00:00Z",
	)
	if left.SourceID == "" || !reflect.DeepEqual(left, right) {
		t.Fatalf("sources are not stable: %#v != %#v", left, right)
	}
	if len(left.TurnIDs) != 2 {
		t.Fatalf("source = %#v", left)
	}
	chunk := newLMEBuildTraceSource(
		"case", "session", "runner", left.TurnIDs,
		"chunk-2", "2026-07-16T00:00:00Z",
	)
	if chunk.SourceID == left.SourceID || chunk.ChunkID != "chunk-2" {
		t.Fatalf("chunk source = %#v", chunk)
	}
}

func TestTraceRetrievalAndGoldSessionRecall(t *testing.T) {
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case-qa", lmeTraceContentHash, sink)
	trace.setInitialSnapshot(nil)
	source := lmeBuildTraceSource{SourceID: "source", SessionID: "gold-session"}
	if err := trace.recordExtraction(source, nil, []*extractor.Operation{{
		Type:   extractor.OperationAdd,
		Memory: "The answer is blue.",
	}}, nil); err != nil {
		t.Fatalf("recordExtraction() error = %v", err)
	}
	reader := &lmeTraceTestReader{entries: []*memory.Entry{{
		ID:     "memory-blue",
		Memory: &memory.Memory{Memory: "The answer is blue."},
	}}}
	if err := trace.recordPersistence(
		context.Background(), source, reader,
		memory.UserKey{AppName: "app", UserID: "user"}, nil,
	); err != nil {
		t.Fatalf("recordPersistence() error = %v", err)
	}
	steps := []lmeStepTrace{{
		Step: 1,
		ToolCalls: []lmeToolCallTrace{{
			Name:   memory.SearchToolName,
			Args:   `{"query":"favorite color"}`,
			Result: `{"query":"favorite color","results":[{"id":"memory-blue","score":0.91}]}`,
		}},
	}}
	if err := trace.recordQA(steps); err != nil {
		t.Fatalf("recordQA() error = %v", err)
	}
	if err := trace.joinGold([]string{"gold-session"}); err != nil {
		t.Fatalf("joinGold() error = %v", err)
	}
	result := &lmeCaseResult{Correct: false}
	trace.markQAStarted()
	if err := trace.finish(result, nil); err != nil {
		t.Fatalf("finish() error = %v", err)
	}
	if result.FailureStage != lmeFailureAnswerGenerationMiss {
		t.Fatalf("FailureStage = %q", result.FailureStage)
	}
	if result.GoldSessionRecall == nil || *result.GoldSessionRecall != 1 {
		t.Fatalf("GoldSessionRecall = %v", result.GoldSessionRecall)
	}
	if !sink.closed {
		t.Fatal("trace sink was not closed")
	}
}

func TestGoldCannotJoinBeforeQA(t *testing.T) {
	trace := newLMECaseTrace("case", lmeTraceContentNone, &lmeMemoryTraceSink{})
	if err := trace.joinGold([]string{"session"}); err == nil {
		t.Fatal("joinGold() unexpectedly accepted gold before QA")
	}
}

func TestPersistenceReadFailureIsRecordedAndReturned(t *testing.T) {
	wantErr := errors.New("read unavailable")
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case", lmeTraceContentHash, sink)
	trace.setInitialSnapshot(nil)
	reader := &lmeTraceTestReader{readErr: wantErr}
	err := trace.recordPersistence(
		context.Background(),
		lmeBuildTraceSource{SourceID: "source", SessionID: "session"},
		reader,
		memory.UserKey{AppName: "app", UserID: "user"},
		nil,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("recordPersistence() error = %v, want %v", err, wantErr)
	}
	if len(sink.records) != 1 || sink.records[0].Persistence == nil {
		t.Fatalf("records = %#v", sink.records)
	}
	if !strings.Contains(sink.records[0].Persistence.Error, wantErr.Error()) {
		t.Fatalf("persistence error = %q", sink.records[0].Persistence.Error)
	}
}

func TestPersistenceAcknowledgementTracksBuildError(t *testing.T) {
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case", lmeTraceContentNone, sink)
	trace.setInitialSnapshot(nil)
	wantErr := errors.New("remote request rejected")
	err := trace.recordPersistence(
		context.Background(),
		lmeBuildTraceSource{SourceID: "source", SessionID: "session"},
		&lmeTraceTestReader{},
		memory.UserKey{AppName: "app", UserID: "user"},
		wantErr,
	)
	if err != nil {
		t.Fatalf("recordPersistence() error = %v", err)
	}
	if sink.records[0].Persistence.Acknowledged {
		t.Fatal("failed remote request was recorded as acknowledged")
	}
}

func TestUpdateWithoutPersistedEvidenceMarksReconciliationLoss(t *testing.T) {
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case", lmeTraceContentHash, sink)
	trace.setInitialSnapshot([]*memory.Entry{{
		ID:     "old-id",
		Memory: &memory.Memory{Memory: "old"},
	}})
	source := lmeBuildTraceSource{SourceID: "source", SessionID: "gold"}
	if err := trace.recordExtraction(source, nil, []*extractor.Operation{{
		Type:     extractor.OperationUpdate,
		MemoryID: "old-id",
		Memory:   "new",
	}}, nil); err != nil {
		t.Fatalf("recordExtraction() error = %v", err)
	}
	if err := trace.recordPersistence(
		context.Background(), source, &lmeTraceTestReader{},
		memory.UserKey{AppName: "app", UserID: "user"}, nil,
	); err != nil {
		t.Fatalf("recordPersistence() error = %v", err)
	}
	if _, ok := trace.reconciliationLoss["gold"]; !ok {
		t.Fatal("missing update was not marked as reconciliation loss evidence")
	}
}

func TestMalformedToolResultIsAudited(t *testing.T) {
	retrieval := parseLMERetrievalTrace(
		lmeTraceContentHash,
		2,
		`{"query":"Alice"}`,
		`{not-json}`,
	)
	if retrieval.ParseError == "" {
		t.Fatal("ParseError is empty")
	}
	if retrieval.Query.SHA256 == "" || len(retrieval.Hits) != 0 {
		t.Fatalf("retrieval = %#v", retrieval)
	}
}

func TestFailureStageClassification(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*lmeCaseTrace) *lmeCaseResult
		want  lmeFailureStage
	}{
		{
			name: "success",
			setup: func(*lmeCaseTrace) *lmeCaseResult {
				return &lmeCaseResult{Correct: true}
			},
			want: lmeFailureSuccess,
		},
		{
			name: "judge error",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.judgeError = true
				return &lmeCaseResult{}
			},
			want: lmeFailureJudgeError,
		},
		{
			name: "persistence error",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.persistenceError = true
				return nil
			},
			want: lmeFailurePersistenceError,
		},
		{
			name: "build error",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.buildError = true
				return nil
			},
			want: lmeFailureBuildError,
		},
		{
			name: "extraction miss",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.extractionExpected = true
				trace.goldSessionIDs = []string{"gold"}
				trace.sources["source"] = &lmeTraceSourceEvidence{sessionID: "gold"}
				return &lmeCaseResult{}
			},
			want: lmeFailureExtractionMiss,
		},
		{
			name: "reconciliation loss",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.goldSessionIDs = []string{"gold"}
				trace.sources["source"] = &lmeTraceSourceEvidence{
					sessionID: "gold",
					operations: []lmeTraceOperation{{
						Type: string(extractor.OperationUpdate),
					}},
				}
				trace.reconciliationLoss["gold"] = struct{}{}
				return &lmeCaseResult{}
			},
			want: lmeFailureReconciliationLoss,
		},
		{
			name: "unresolved build evidence",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.goldSessionIDs = []string{"gold"}
				trace.sources["source"] = &lmeTraceSourceEvidence{sessionID: "gold"}
				return &lmeCaseResult{}
			},
			want: lmeFailureBuildEvidenceMissing,
		},
		{
			name: "retrieval miss",
			setup: func(trace *lmeCaseTrace) *lmeCaseResult {
				trace.goldSessionIDs = []string{"gold"}
				trace.sources["source"] = &lmeTraceSourceEvidence{
					sessionID: "gold",
					operations: []lmeTraceOperation{{
						Type: string(extractor.OperationAdd),
					}},
				}
				trace.memorySources["memory-1"] = map[string]struct{}{"gold": {}}
				return &lmeCaseResult{}
			},
			want: lmeFailureRetrievalMiss,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace := newLMECaseTrace("case", lmeTraceContentNone, &lmeMemoryTraceSink{})
			result := test.setup(trace)
			stage, _ := trace.classifyLocked(result, nil)
			if stage != test.want {
				t.Fatalf("stage = %q, want %q", stage, test.want)
			}
		})
	}
}

func TestTraceWriteFailureFailsExtraction(t *testing.T) {
	wantErr := errors.New("disk full")
	sink := &lmeMemoryTraceSink{err: wantErr}
	trace := newLMECaseTrace("case", lmeTraceContentHash, sink)
	source := lmeBuildTraceSource{SourceID: "source", SessionID: "session"}
	inner := &lmeTraceTestExtractor{operations: []*extractor.Operation{{
		Type:   extractor.OperationAdd,
		Memory: "memory",
	}}}
	wrapper := newLMETracingExtractor(inner, extractor.UpdatePolicyMergeSimilar)
	ctx := withLMECaseTrace(context.Background(), trace)
	ctx = withLMEBuildTraceSource(ctx, source)
	_, err := wrapper.Extract(ctx, []model.Message{model.NewUserMessage("hello")}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Extract() error = %v, want %v", err, wantErr)
	}
	if inner.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", inner.calls)
	}
}

func TestExtractorFailureIsAuditedWithoutRetry(t *testing.T) {
	wantErr := errors.New("model unavailable")
	sink := &lmeMemoryTraceSink{}
	trace := newLMECaseTrace("case", lmeTraceContentHash, sink)
	inner := &lmeTraceTestExtractor{err: wantErr}
	wrapper := newLMETracingExtractor(inner, extractor.UpdatePolicyMergeSimilar)
	ctx := withLMECaseTrace(context.Background(), trace)
	ctx = withLMEBuildTraceSource(ctx, lmeBuildTraceSource{
		SourceID: "source", SessionID: "session",
	})
	_, err := wrapper.Extract(ctx, []model.Message{model.NewUserMessage("hello")}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Extract() error = %v, want %v", err, wantErr)
	}
	if inner.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", inner.calls)
	}
	if len(sink.records) != 1 || sink.records[0].Extraction == nil ||
		!strings.Contains(sink.records[0].Extraction.Error, wantErr.Error()) {
		t.Fatalf("records = %#v", sink.records)
	}
}

func TestMemorySnapshotDiff(t *testing.T) {
	before := buildLMEMemorySnapshot([]*memory.Entry{
		{ID: "same", Memory: &memory.Memory{Memory: "same"}},
		{ID: "update", Memory: &memory.Memory{Memory: "old"}},
		{ID: "delete", Memory: &memory.Memory{Memory: "gone"}},
	}, lmeTraceContentHash)
	after := buildLMEMemorySnapshot([]*memory.Entry{
		{ID: "same", Memory: &memory.Memory{Memory: "same"}},
		{ID: "update", Memory: &memory.Memory{Memory: "new"}},
		{ID: "add", Memory: &memory.Memory{Memory: "added"}},
	}, lmeTraceContentHash)
	diff := diffLMEMemorySnapshots(before, after)
	if diff.Unchanged != 1 ||
		!reflect.DeepEqual(traceMemoryIDs(diff.Added), []string{"add"}) ||
		!reflect.DeepEqual(traceMemoryIDs(diff.Updated), []string{"update"}) ||
		!reflect.DeepEqual(traceMemoryIDs(diff.Deleted), []string{"delete"}) {
		t.Fatalf("diff = %#v", diff)
	}
}

func traceMemoryIDs(refs []lmeTraceMemoryRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

func TestTraceErrorsAreSanitized(t *testing.T) {
	err := errors.New("request failed: password=hunter2")
	if got := lmeTraceError(err); strings.Contains(got, "hunter2") {
		t.Fatalf("trace error leaked secret: %q", got)
	}
}

func TestReadLMETraceOutcomeEnforcesResourceLimits(t *testing.T) {
	t.Run("file bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trace.jsonl")
		if err := os.WriteFile(path, []byte("too large"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readLMETraceOutcomeWithLimits(path, lmeTraceReadLimits{
			fileBytes: 1, decodedBytes: 100, records: 10,
		})
		if err == nil || !strings.Contains(err.Error(), "compressed size limit") {
			t.Fatalf("read error = %v", err)
		}
	})

	t.Run("decoded bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trace.jsonl.gz")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := gzip.NewWriter(file)
		if _, err := writer.Write([]byte(strings.Repeat(" ", 128))); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = readLMETraceOutcomeWithLimits(path, lmeTraceReadLimits{
			fileBytes: 1024, decodedBytes: 16, records: 10,
		})
		if err == nil || !strings.Contains(err.Error(), "decoded size limit") {
			t.Fatalf("read error = %v", err)
		}
	})

	t.Run("records", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trace.jsonl")
		source := newLMEBuildTraceSource(
			"case-1", "session-1", "seed-chunk-1",
			[]string{"turn-1"}, "chunk-1",
			"2025-01-01T00:00:00Z",
		)
		writer, err := newLMEJSONLTraceWriter(path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Write(&lmeTraceRecord{
			CaseID: "case-1", ContentMode: lmeTraceContentHash,
			Event: lmeTraceEventExtraction, Source: &source,
			Extraction: &lmeTraceExtraction{
				Input:               traceLMEText(lmeTraceContentHash, "input", true),
				EffectiveOperations: lmeTraceEffectiveOperationsUnavailable,
				UnavailableReason:   "backend operation details are unavailable",
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := writer.Write(&lmeTraceRecord{
			CaseID: "case-1", ContentMode: lmeTraceContentHash,
			Event: lmeTraceEventPersistence, Source: &source,
			Persistence: &lmeTracePersistence{
				Acknowledged:      true,
				ActualOperations:  lmeTraceEffectiveOperationsUnavailable,
				UnavailableReason: "backend persistence details are unavailable",
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = readLMETraceOutcomeWithLimits(path, lmeTraceReadLimits{
			fileBytes: 1 << 20, decodedBytes: 1 << 20, records: 1,
		})
		if err == nil || !strings.Contains(err.Error(), "record limit") {
			t.Fatalf("read error = %v", err)
		}
	})
}

func TestDiscoverLMETraceAttemptsRejectsUnsafeArtifacts(t *testing.T) {
	root := t.TempDir()
	traceRoot := filepath.Join(root, "build_trace")
	if err := os.MkdirAll(traceRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(traceRoot, "extra.attempt-0001.jsonl"),
		[]byte("{}\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(traceRoot, "regular")
	if err := os.WriteFile(regular, []byte("trace"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(regular, filepath.Join(
		traceRoot,
		lmeTraceFileName("case-1")+".attempt-0001.jsonl",
	)); err != nil {
		t.Skipf("create trace symlink: %v", err)
	}
	fifo := filepath.Join(traceRoot, lmeTraceFileName("case-1")+".attempt-0002.jsonl")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skipf("create FIFO: %v", err)
	}
	_, blockers := discoverLMETraceAttempts(root, []string{"case-1"})
	joined := strings.Join(blockers, "\n")
	if !strings.Contains(joined, "regular files") {
		t.Fatalf("blockers = %v, want non-regular rejection", blockers)
	}
	if !strings.Contains(joined, "outside the fixed denominator") {
		t.Fatalf("blockers = %v, want denominator rejection", blockers)
	}
}

func TestLMETraceLifecycleAllowsOnlyTerminalFailuresWithoutGold(t *testing.T) {
	tests := []struct {
		name    string
		outcome *lmeTraceOutcome
		wantErr bool
	}{
		{
			name: "build error",
			outcome: &lmeTraceOutcome{
				FailureStage:       lmeFailureBuildError,
				BuildObservability: lmeBuildObservabilityUnknown,
				Error:              "build failed",
			},
		},
		{
			name: "qa error",
			outcome: &lmeTraceOutcome{
				FailureStage:       lmeFailureAnswerGenerationMiss,
				BuildObservability: lmeBuildObservabilityUnknown,
				Error:              "qa failed",
			},
		},
		{
			name: "retrieval classification needs gold",
			outcome: &lmeTraceOutcome{
				FailureStage:       lmeFailureRetrievalMiss,
				BuildObservability: lmeBuildObservabilityUnknown,
				Error:              "qa failed",
			},
			wantErr: true,
		},
		{
			name: "success needs gold",
			outcome: &lmeTraceOutcome{
				FailureStage:       lmeFailureSuccess,
				BuildObservability: lmeBuildObservabilityUnknown,
				Correct:            true,
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := newLMETraceLifecycle()
			err := lifecycle.observe(&lmeTraceRecord{
				Event:   lmeTraceEventOutcome,
				Outcome: test.outcome,
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("observe() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestTraceContentModesAndSanitization(t *testing.T) {
	const secret = "api_key=top-secret"
	tests := []struct {
		name       string
		mode       lmeTraceContentMode
		wantValue  bool
		wantDigest bool
	}{
		{name: "full", mode: lmeTraceContentFull, wantValue: true, wantDigest: true},
		{name: "hash", mode: lmeTraceContentHash, wantDigest: true},
		{name: "none", mode: lmeTraceContentNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text := traceLMEText(test.mode, secret, false)
			if got := text.Value != ""; got != test.wantValue {
				t.Fatalf("Value present = %v, want %v", got, test.wantValue)
			}
			if got := text.SHA256 != ""; got != test.wantDigest {
				t.Fatalf("SHA256 present = %v, want %v", got, test.wantDigest)
			}
			if strings.Contains(text.Value, "top-secret") {
				t.Fatalf("Value leaked secret: %q", text.Value)
			}
		})
	}

	mandatory := traceLMEText(lmeTraceContentNone, secret, true)
	if mandatory.SHA256 == "" {
		t.Fatal("mandatory input digest is empty in none mode")
	}
	if mandatory.SHA256 != lmeSHA256(secret) {
		t.Fatal("mandatory digest was not calculated from the original input")
	}
	if got := sanitizeLMETraceString("Authorization: Bearer abc.def"); strings.Contains(got, "abc.def") {
		t.Fatalf("authorization token was not sanitized: %q", got)
	}
}

func TestTraceOperationMetadataFollowsContentMode(t *testing.T) {
	operation := &extractor.Operation{
		Memory:       "memory api_key=memory-secret",
		Topics:       []string{"api_key=topic-secret"},
		Participants: []string{"Authorization: Bearer participant-secret"},
		Location:     "/private/location-secret",
	}
	for _, mode := range []lmeTraceContentMode{
		lmeTraceContentFull,
		lmeTraceContentHash,
		lmeTraceContentNone,
	} {
		t.Run(string(mode), func(t *testing.T) {
			traced := traceLMEOperation(operation, mode)
			payload, err := json.Marshal(traced)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, secret := range []string{
				"memory-secret", "topic-secret", "participant-secret", "location-secret",
			} {
				if strings.Contains(text, secret) {
					t.Fatalf("%s trace leaked %q: %s", mode, secret, text)
				}
			}
			switch mode {
			case lmeTraceContentHash:
				if len(traced.Topics) != 1 || len(traced.Participants) != 1 ||
					!strings.HasPrefix(traced.Location, "sha256:") {
					t.Fatalf("hash metadata = %#v", traced)
				}
			case lmeTraceContentNone:
				if len(traced.Topics) != 0 || len(traced.Participants) != 0 || traced.Location != "" {
					t.Fatalf("none metadata = %#v", traced)
				}
			}
		})
	}
}

func TestJSONLTraceWriterGzipConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl.gz")
	writer, err := newLMEJSONLTraceWriter(path, true)
	if err != nil {
		t.Fatalf("newLMEJSONLTraceWriter() error = %v", err)
	}
	const count = 32
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- writer.Write(&lmeTraceRecord{
				CaseID:      "case-concurrent",
				ContentMode: lmeTraceContentHash,
				Event:       lmeTraceEventOutcome,
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gzipReader.Close()
	scanner := bufio.NewScanner(gzipReader)
	sequences := make(map[uint64]struct{}, count)
	for scanner.Scan() {
		var record lmeTraceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if record.SchemaVersion != lmeTraceSchemaVersion {
			t.Fatalf("SchemaVersion = %q", record.SchemaVersion)
		}
		sequences[record.Sequence] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Scanner() error = %v", err)
	}
	if len(sequences) != count {
		t.Fatalf("records = %d, want %d", len(sequences), count)
	}
	for sequence := uint64(1); sequence <= count; sequence++ {
		if _, ok := sequences[sequence]; !ok {
			t.Fatalf("missing sequence %d", sequence)
		}
	}
}

func TestJSONLTraceWriterIsImmutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	writer, err := newLMEJSONLTraceWriter(path, false)
	if err != nil {
		t.Fatalf("first writer error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := newLMEJSONLTraceWriter(path, false); err == nil {
		t.Fatal("second writer unexpectedly replaced an immutable trace")
	}
}

func TestJSONLTraceWriterRejectsInvalidWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	writer, err := newLMEJSONLTraceWriter(path, false)
	if err != nil {
		t.Fatalf("newLMEJSONLTraceWriter() error = %v", err)
	}
	if err := writer.Write(nil); err == nil {
		t.Fatal("Write() accepted nil record")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := writer.Write(&lmeTraceRecord{}); err == nil {
		t.Fatal("Write() accepted record after Close()")
	}
}
