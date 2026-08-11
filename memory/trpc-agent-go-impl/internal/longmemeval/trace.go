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
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func traceLMEOperation(
	operation *extractor.Operation,
	mode lmeTraceContentMode,
) lmeTraceOperation {
	eventTime := ""
	if operation.EventTime != nil {
		eventTime = operation.EventTime.UTC().Format(time.RFC3339Nano)
	}
	return lmeTraceOperation{
		Type:         string(operation.Type),
		MemoryID:     operation.MemoryID,
		Memory:       traceLMEText(mode, operation.Memory, false),
		Topics:       traceLMEStrings(mode, operation.Topics),
		Kind:         string(operation.MemoryKind),
		EventTime:    eventTime,
		Participants: traceLMEStrings(mode, operation.Participants),
		Location:     traceLMEString(mode, operation.Location),
	}
}

func traceLMEStrings(mode lmeTraceContentMode, values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if traced := traceLMEString(mode, value); traced != "" {
			out = append(out, traced)
		}
	}
	return out
}

func traceLMEString(mode lmeTraceContentMode, value string) string {
	switch mode {
	case lmeTraceContentFull:
		return sanitizeLMETraceString(value)
	case lmeTraceContentHash:
		return lmeDigestAlgorithm + ":" + lmeSHA256(value)
	case lmeTraceContentNone:
		return ""
	default:
		return ""
	}
}

func traceLMEText(mode lmeTraceContentMode, value string, digestRequired bool) lmeTraceText {
	rawValue := value
	text := lmeTraceText{Bytes: len(rawValue)}
	if mode == lmeTraceContentFull {
		text.Value = sanitizeLMETraceString(rawValue)
	}
	if mode == lmeTraceContentFull || mode == lmeTraceContentHash || digestRequired {
		text.SHA256 = lmeSHA256(rawValue)
	}
	return text
}

func marshalLMETraceMessages(messages []model.Message) (string, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("marshal LongMemEval trace input: %w", err)
	}
	return string(data), nil
}

func buildLMEMemorySnapshot(
	entries []*memory.Entry,
	mode lmeTraceContentMode,
) lmeTraceMemorySnapshot {
	snapshot := lmeTraceMemorySnapshot{
		byID:          make(map[string]lmeTraceMemoryRef),
		contentHashes: make(map[string]string),
	}
	for _, entry := range entries {
		if entry == nil || entry.Memory == nil || entry.ID == "" {
			continue
		}
		ref := lmeTraceMemoryRef{
			ID:          entry.ID,
			Fingerprint: fingerprintLMEMemoryEntry(entry),
			Memory:      traceLMEText(mode, entry.Memory.Memory, false),
		}
		snapshot.refs = append(snapshot.refs, ref)
		snapshot.byID[ref.ID] = ref
		snapshot.contentHashes[ref.ID] = lmeSHA256(entry.Memory.Memory)
	}
	sort.Slice(snapshot.refs, func(i, j int) bool { return snapshot.refs[i].ID < snapshot.refs[j].ID })
	return snapshot
}

func fingerprintLMEMemoryEntry(entry *memory.Entry) string {
	if entry == nil || entry.Memory == nil {
		return lmeSHA256("null")
	}
	topics := sortedUniqueLMEStrings(entry.Memory.Topics)
	participants := sortedUniqueLMEStrings(entry.Memory.Participants)
	eventTime := ""
	if entry.Memory.EventTime != nil {
		eventTime = entry.Memory.EventTime.UTC().Format(time.RFC3339Nano)
	}
	canonical := struct {
		Memory       string   `json:"memory"`
		Topics       []string `json:"topics,omitempty"`
		Kind         string   `json:"kind,omitempty"`
		EventTime    string   `json:"event_time,omitempty"`
		Participants []string `json:"participants,omitempty"`
		Location     string   `json:"location,omitempty"`
	}{
		Memory:       entry.Memory.Memory,
		Topics:       topics,
		Kind:         string(entry.Memory.Kind),
		EventTime:    eventTime,
		Participants: participants,
		Location:     entry.Memory.Location,
	}
	data, _ := json.Marshal(canonical)
	return lmeSHA256(string(data))
}

func diffLMEMemorySnapshots(
	before lmeTraceMemorySnapshot,
	after lmeTraceMemorySnapshot,
) lmeTraceMemoryDiff {
	var diff lmeTraceMemoryDiff
	for id, ref := range after.byID {
		previous, ok := before.byID[id]
		switch {
		case !ok:
			diff.Added = append(diff.Added, ref)
		case previous.Fingerprint != ref.Fingerprint:
			diff.Updated = append(diff.Updated, ref)
		default:
			diff.Unchanged++
		}
	}
	for id, ref := range before.byID {
		if _, ok := after.byID[id]; !ok {
			diff.Deleted = append(diff.Deleted, ref)
		}
	}
	sort.Slice(diff.Added, func(i, j int) bool { return diff.Added[i].ID < diff.Added[j].ID })
	sort.Slice(diff.Updated, func(i, j int) bool { return diff.Updated[i].ID < diff.Updated[j].ID })
	sort.Slice(diff.Deleted, func(i, j int) bool { return diff.Deleted[i].ID < diff.Deleted[j].ID })
	return diff
}

func parseLMERetrievalTrace(
	mode lmeTraceContentMode,
	step int,
	args string,
	result string,
) lmeTraceRetrieval {
	var arguments struct {
		Query string `json:"query"`
	}
	retrieval := lmeTraceRetrieval{Step: step}
	if err := json.Unmarshal([]byte(args), &arguments); err != nil {
		retrieval.ParseError = fmt.Sprintf("parse memory_search arguments: %v", err)
	}
	var response struct {
		Query   string `json:"query"`
		Results []struct {
			ID    string  `json:"id"`
			Score float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		if retrieval.ParseError != "" {
			retrieval.ParseError += "; "
		}
		retrieval.ParseError += fmt.Sprintf("parse memory_search result: %v", err)
	}
	query := arguments.Query
	if query == "" {
		query = response.Query
	}
	retrieval.Query = traceLMEText(mode, query, true)
	for i, item := range response.Results {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		retrieval.Hits = append(retrieval.Hits, lmeTraceRetrievalHit{
			MemoryID: item.ID,
			Score:    item.Score,
			Rank:     i + 1,
		})
	}
	return retrieval
}

func hasLMETraceWriteOperation(operations []lmeTraceOperation) bool {
	for _, operation := range operations {
		if operation.Type == string(extractor.OperationAdd) ||
			operation.Type == string(extractor.OperationUpdate) {
			return true
		}
	}
	return false
}

func hasLMETraceUpdateOperation(operations []lmeTraceOperation) bool {
	for _, operation := range operations {
		if operation.Type == string(extractor.OperationUpdate) {
			return true
		}
	}
	return false
}

func cloneLMEBuildTraceSource(source lmeBuildTraceSource) *lmeBuildTraceSource {
	clone := source
	clone.TurnIDs = append([]string(nil), source.TurnIDs...)
	return &clone
}

func newLMEBuildTraceSource(
	caseID string,
	sessionID string,
	runnerSessionID string,
	turnIDs []string,
	chunkID string,
	observationTime string,
) lmeBuildTraceSource {
	turnIDs = append([]string(nil), turnIDs...)
	identity, _ := json.Marshal(struct {
		CaseID    string   `json:"case_id"`
		SessionID string   `json:"session_id"`
		TurnIDs   []string `json:"turn_ids"`
		ChunkID   string   `json:"chunk_id,omitempty"`
	}{
		CaseID:    caseID,
		SessionID: sessionID,
		TurnIDs:   turnIDs,
		ChunkID:   chunkID,
	})
	return lmeBuildTraceSource{
		SourceID:        lmeSHA256(string(identity)),
		SessionID:       sessionID,
		RunnerSession:   runnerSessionID,
		TurnIDs:         turnIDs,
		ChunkID:         chunkID,
		ObservationTime: observationTime,
	}
}

func sortedUniqueLMEStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func lmeTraceError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeLMETraceString(err.Error())
}

func lmeSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

const lmeTraceSchemaVersion = "longmemeval.build_trace/v4"

type lmeTraceContentMode string

const (
	lmeTraceContentFull lmeTraceContentMode = "full"
	lmeTraceContentHash lmeTraceContentMode = "hash"
	lmeTraceContentNone lmeTraceContentMode = "none"
)

type lmeTraceEvent string

const (
	lmeTraceEventExtraction  lmeTraceEvent = "extraction"
	lmeTraceEventPersistence lmeTraceEvent = "persistence"
	lmeTraceEventRetrieval   lmeTraceEvent = "retrieval"
	lmeTraceEventGoldJoin    lmeTraceEvent = "gold_join"
	lmeTraceEventOutcome     lmeTraceEvent = "outcome"
)

type lmeFailureStage string

const (
	lmeFailureBuildError           lmeFailureStage = "build_error"
	lmeFailureBuildEvidenceMissing lmeFailureStage = "build_evidence_missing_unresolved"
	lmeFailureExtractionMiss       lmeFailureStage = "extraction_miss"
	lmeFailureReconciliationLoss   lmeFailureStage = "reconciliation_loss"
	lmeFailurePersistenceError     lmeFailureStage = "persistence_error"
	lmeFailureRetrievalMiss        lmeFailureStage = "retrieval_miss"
	lmeFailureAnswerGenerationMiss lmeFailureStage = "answer_generation_miss"
	lmeFailureJudgeError           lmeFailureStage = "judge_error"
	lmeFailureSuccess              lmeFailureStage = "success"
	lmeFailureUnknown              lmeFailureStage = "unknown"
)

type lmeBuildObservability string

const (
	lmeBuildObservabilityOperations   lmeBuildObservability = "operations"
	lmeBuildObservabilitySnapshotDiff lmeBuildObservability = "snapshot_diff"
	lmeBuildObservabilityUnknown      lmeBuildObservability = "unknown"
)

type lmeTraceText struct {
	Value  string `json:"value,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int    `json:"bytes"`
}

type lmeBuildTraceSource struct {
	SourceID        string   `json:"source_id"`
	SessionID       string   `json:"session_id"`
	RunnerSession   string   `json:"runner_session_id,omitempty"`
	TurnIDs         []string `json:"turn_ids"`
	ChunkID         string   `json:"chunk_id,omitempty"`
	ObservationTime string   `json:"observation_time,omitempty"`
}

type lmeTraceOperation struct {
	Type         string       `json:"type"`
	MemoryID     string       `json:"memory_id,omitempty"`
	Memory       lmeTraceText `json:"memory"`
	Topics       []string     `json:"topics,omitempty"`
	Kind         string       `json:"kind,omitempty"`
	EventTime    string       `json:"event_time,omitempty"`
	Participants []string     `json:"participants,omitempty"`
	Location     string       `json:"location,omitempty"`
}

type lmeTraceMemoryRef struct {
	ID          string       `json:"id"`
	Fingerprint string       `json:"fingerprint"`
	Memory      lmeTraceText `json:"memory"`
}

type lmeTraceMemoryDiff struct {
	Added     []lmeTraceMemoryRef `json:"added,omitempty"`
	Updated   []lmeTraceMemoryRef `json:"updated,omitempty"`
	Deleted   []lmeTraceMemoryRef `json:"deleted,omitempty"`
	Unchanged int                 `json:"unchanged"`
}

type lmeTraceExtraction struct {
	Input               lmeTraceText        `json:"input"`
	Operations          []lmeTraceOperation `json:"operations,omitempty"`
	OperationCount      int                 `json:"operation_count"`
	EffectiveOperations string              `json:"effective_operations"`
	UnavailableReason   string              `json:"unavailable_reason,omitempty"`
	Error               string              `json:"error,omitempty"`
}

type lmeTracePersistence struct {
	Acknowledged      bool                `json:"acknowledged"`
	Before            []lmeTraceMemoryRef `json:"before,omitempty"`
	After             []lmeTraceMemoryRef `json:"after,omitempty"`
	Diff              lmeTraceMemoryDiff  `json:"diff"`
	ActualOperations  string              `json:"actual_operations"`
	UnavailableReason string              `json:"unavailable_reason,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type lmeTraceRetrievalHit struct {
	MemoryID string  `json:"memory_id"`
	Score    float64 `json:"score"`
	Rank     int     `json:"rank"`
}

type lmeTraceRetrieval struct {
	Step       int                    `json:"step"`
	Query      lmeTraceText           `json:"query"`
	Hits       []lmeTraceRetrievalHit `json:"hits,omitempty"`
	ParseError string                 `json:"parse_error,omitempty"`
}

type lmeTraceGoldJoin struct {
	AnswerSessionIDs []string `json:"answer_session_ids,omitempty"`
	JoinedAfterQA    bool     `json:"joined_after_qa"`
}

type lmeTraceOutcome struct {
	FailureStage       lmeFailureStage       `json:"failure_stage"`
	BuildObservability lmeBuildObservability `json:"build_observability"`
	GoldSessionRecall  *float64              `json:"gold_session_recall,omitempty"`
	Correct            bool                  `json:"correct"`
	Error              string                `json:"error,omitempty"`
}

type lmeTraceRecord struct {
	SchemaVersion string               `json:"schema_version"`
	Sequence      uint64               `json:"sequence"`
	RecordedAt    time.Time            `json:"recorded_at"`
	CaseID        string               `json:"case_id"`
	ContentMode   lmeTraceContentMode  `json:"content_mode"`
	Event         lmeTraceEvent        `json:"event"`
	Source        *lmeBuildTraceSource `json:"source,omitempty"`
	Extraction    *lmeTraceExtraction  `json:"extraction,omitempty"`
	Persistence   *lmeTracePersistence `json:"persistence,omitempty"`
	Retrieval     *lmeTraceRetrieval   `json:"retrieval,omitempty"`
	Gold          *lmeTraceGoldJoin    `json:"gold,omitempty"`
	Outcome       *lmeTraceOutcome     `json:"outcome,omitempty"`
}

type lmeTraceSink interface {
	Write(*lmeTraceRecord) error
	Close() error
}

type lmeJSONLTraceWriter struct {
	mu       sync.Mutex
	file     *os.File
	gzip     *gzip.Writer
	encoder  *json.Encoder
	sequence uint64
	closed   bool
	writeErr error
}

func newLMEJSONLTraceWriter(path string, compressed bool) (*lmeJSONLTraceWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create LongMemEval trace directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("create LongMemEval trace %s: %w", path, err)
	}
	w := &lmeJSONLTraceWriter{file: file}
	var output io.Writer = file
	if compressed {
		w.gzip = gzip.NewWriter(file)
		output = w.gzip
	}
	w.encoder = json.NewEncoder(output)
	w.encoder.SetEscapeHTML(false)
	return w, nil
}

func (w *lmeJSONLTraceWriter) Write(record *lmeTraceRecord) error {
	if record == nil {
		return fmt.Errorf("write LongMemEval trace: nil record")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("write LongMemEval trace: writer is closed")
	}
	if w.writeErr != nil {
		return w.writeErr
	}
	w.sequence++
	record.SchemaVersion = lmeTraceSchemaVersion
	record.Sequence = w.sequence
	record.RecordedAt = time.Now().UTC()
	if err := w.encoder.Encode(record); err != nil {
		w.writeErr = fmt.Errorf("encode LongMemEval trace record: %w", err)
		return w.writeErr
	}
	if w.gzip != nil {
		if err := w.gzip.Flush(); err != nil {
			w.writeErr = fmt.Errorf("flush LongMemEval gzip trace: %w", err)
			return w.writeErr
		}
	}
	return nil
}

func (w *lmeJSONLTraceWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.writeErr
	}
	w.closed = true
	var closeErr error
	if w.gzip != nil {
		if err := w.gzip.Close(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("close LongMemEval gzip trace: %w", err))
		}
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("close LongMemEval trace file: %w", err))
		}
	}
	return errors.Join(w.writeErr, closeErr)
}

var lmeTraceSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s,;"']+`),
	regexp.MustCompile(`(?i)((?:api[_ -]?key|access[_ -]?token|token|secret|password)\s*[:=]\s*)[^\s,;"']+`),
	regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(^|[\s=:])(/(?:[^/\s]+/)*[^/\s]+)`),
	regexp.MustCompile(`(?i)(^|[\s=:])([a-z]:\\(?:[^\\\s]+\\)*[^\\\s]+)`),
}

func sanitizeLMETraceString(value string) string {
	for _, pattern := range lmeTraceSecretPatterns {
		value = pattern.ReplaceAllString(value, "${1}[REDACTED]")
	}
	return value
}

const lmeTraceEffectiveOperationsUnavailable = "unavailable"

const lmeTraceMaxAttempts = 10000

type lmeTraceWriterFactory func(string, bool) (lmeTraceSink, error)

type lmeTraceManager struct {
	mu            sync.RWMutex
	root          string
	mode          lmeTraceContentMode
	compressed    bool
	writerFactory lmeTraceWriterFactory
}

func newLMETraceManager(mode lmeTraceContentMode, compressed bool) (*lmeTraceManager, error) {
	if mode == "" {
		mode = lmeTraceContentHash
	}
	if err := validateLMETraceContentMode(mode); err != nil {
		return nil, err
	}
	return &lmeTraceManager{
		mode:       mode,
		compressed: compressed,
		writerFactory: func(path string, gzip bool) (lmeTraceSink, error) {
			return newLMEJSONLTraceWriter(path, gzip)
		},
	}, nil
}

func validateLMETraceContentMode(mode lmeTraceContentMode) error {
	switch mode {
	case lmeTraceContentFull, lmeTraceContentHash, lmeTraceContentNone:
		return nil
	default:
		return fmt.Errorf("unsupported LongMemEval trace content mode %q", mode)
	}
}

func (m *lmeTraceManager) setRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("LongMemEval trace root is required")
	}
	m.mu.Lock()
	m.root = filepath.Join(root, "build_trace")
	m.mu.Unlock()
	return nil
}

func (m *lmeTraceManager) beginCase(caseID string) (*lmeCaseTrace, error) {
	m.mu.RLock()
	root := m.root
	mode := m.mode
	compressed := m.compressed
	factory := m.writerFactory
	m.mu.RUnlock()
	if root == "" {
		return nil, fmt.Errorf("LongMemEval trace root is not configured")
	}
	ext := ".jsonl"
	if compressed {
		ext += ".gz"
	}
	base := lmeTraceFileName(caseID)
	for attempt := 1; attempt <= lmeTraceMaxAttempts; attempt++ {
		path := filepath.Join(root, fmt.Sprintf("%s.attempt-%04d%s", base, attempt, ext))
		sink, err := factory(path, compressed)
		if err == nil {
			return newLMECaseTrace(caseID, mode, sink), nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf(
		"LongMemEval trace for %s exceeds %d attempts",
		caseID,
		lmeTraceMaxAttempts,
	)
}

func (e *lmeAutoEvaluator) setLMETraceRoot(root string) error {
	if e.trace == nil {
		return fmt.Errorf("LongMemEval auto trace manager is unavailable")
	}
	return e.trace.setRoot(root)
}

func (e *lmeMem0OSSEvaluator) setLMETraceRoot(root string) error {
	if e.trace == nil {
		return fmt.Errorf("LongMemEval Mem0 trace manager is unavailable")
	}
	return e.trace.setRoot(root)
}

func lmeTraceFileName(caseID string) string {
	var b strings.Builder
	for _, r := range caseID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "case-" + lmeSHA256(caseID)[:12]
	}
	return name
}

type lmeTraceContextKey uint8

const (
	lmeTraceCaseContextKey lmeTraceContextKey = iota + 1
	lmeTraceSourceContextKey
)

func withLMECaseTrace(ctx context.Context, trace *lmeCaseTrace) context.Context {
	return context.WithValue(ctx, lmeTraceCaseContextKey, trace)
}

func lmeCaseTraceFromContext(ctx context.Context) *lmeCaseTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(lmeTraceCaseContextKey).(*lmeCaseTrace)
	return trace
}

func withLMEBuildTraceSource(
	ctx context.Context,
	source lmeBuildTraceSource,
) context.Context {
	return context.WithValue(ctx, lmeTraceSourceContextKey, source)
}

func lmeBuildTraceSourceFromContext(ctx context.Context) (lmeBuildTraceSource, bool) {
	if ctx == nil {
		return lmeBuildTraceSource{}, false
	}
	source, ok := ctx.Value(lmeTraceSourceContextKey).(lmeBuildTraceSource)
	return source, ok
}

type lmeTraceMemorySnapshot struct {
	refs          []lmeTraceMemoryRef
	byID          map[string]lmeTraceMemoryRef
	contentHashes map[string]string
}

type lmeTraceSourceEvidence struct {
	sessionID       string
	operations      []lmeTraceOperation
	operationHashes []string
}

type lmeCaseTrace struct {
	mu sync.Mutex

	caseID string
	mode   lmeTraceContentMode
	sink   lmeTraceSink

	currentSnapshot    lmeTraceMemorySnapshot
	hasInitialSnapshot bool
	sources            map[string]*lmeTraceSourceEvidence
	memorySources      map[string]map[string]struct{}
	reconciliationLoss map[string]struct{}
	retrievedIDs       map[string]struct{}
	goldSessionIDs     []string
	qaComplete         bool
	qaStarted          bool
	judgeError         bool
	buildError         bool
	persistenceError   bool
	extractionExpected bool
	buildObservability lmeBuildObservability
	closed             bool
}

func newLMECaseTrace(
	caseID string,
	mode lmeTraceContentMode,
	sink lmeTraceSink,
) *lmeCaseTrace {
	return &lmeCaseTrace{
		caseID:             caseID,
		mode:               mode,
		sink:               sink,
		sources:            make(map[string]*lmeTraceSourceEvidence),
		memorySources:      make(map[string]map[string]struct{}),
		reconciliationLoss: make(map[string]struct{}),
		retrievedIDs:       make(map[string]struct{}),
	}
}

func (t *lmeCaseTrace) setInitialSnapshot(entries []*memory.Entry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentSnapshot = buildLMEMemorySnapshot(entries, t.mode)
	t.hasInitialSnapshot = true
}

func (t *lmeCaseTrace) setExtractionExpected(expected bool) {
	t.mu.Lock()
	t.extractionExpected = expected
	t.mu.Unlock()
}

func (t *lmeCaseTrace) markBuildError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.buildError = true
	t.mu.Unlock()
}

func (t *lmeCaseTrace) markPersistenceError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.persistenceError = true
	t.mu.Unlock()
}

func (t *lmeCaseTrace) markQAStarted() {
	t.mu.Lock()
	t.qaStarted = true
	t.mu.Unlock()
}

func (t *lmeCaseTrace) markJudgeError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.judgeError = true
	t.mu.Unlock()
}

func (t *lmeCaseTrace) recordExtraction(
	source lmeBuildTraceSource,
	messages []model.Message,
	operations []*extractor.Operation,
	extractErr error,
) error {
	inputJSON, marshalErr := marshalLMETraceMessages(messages)
	if marshalErr != nil {
		return marshalErr
	}
	traceOps := make([]lmeTraceOperation, 0, len(operations))
	opHashes := make([]string, 0, len(operations))
	for _, operation := range operations {
		if operation == nil {
			continue
		}
		traceOps = append(traceOps, traceLMEOperation(operation, t.mode))
		opHashes = append(opHashes, lmeSHA256(operation.Memory))
	}
	t.mu.Lock()
	t.buildObservability = lmeBuildObservabilityOperations
	t.sources[source.SourceID] = &lmeTraceSourceEvidence{
		sessionID:       source.SessionID,
		operations:      traceOps,
		operationHashes: opHashes,
	}
	if extractErr != nil {
		t.buildError = true
	}
	t.mu.Unlock()
	record := &lmeTraceRecord{
		CaseID:      t.caseID,
		ContentMode: t.mode,
		Event:       lmeTraceEventExtraction,
		Source:      cloneLMEBuildTraceSource(source),
		Extraction: &lmeTraceExtraction{
			Input:               traceLMEText(t.mode, inputJSON, true),
			Operations:          traceOps,
			OperationCount:      len(traceOps),
			EffectiveOperations: lmeTraceEffectiveOperationsUnavailable,
			UnavailableReason: "the memory service does not expose post-reconcile " +
				"operations through its public API",
			Error: lmeTraceError(extractErr),
		},
	}
	return t.sink.Write(record)
}

func (t *lmeCaseTrace) recordExtractionUnavailable(
	source lmeBuildTraceSource,
	messages []model.Message,
	reason string,
) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("LongMemEval extraction trace unavailable reason is required")
	}
	inputJSON, err := marshalLMETraceMessages(messages)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.buildObservability = lmeBuildObservabilitySnapshotDiff
	t.sources[source.SourceID] = &lmeTraceSourceEvidence{
		sessionID: source.SessionID,
	}
	t.mu.Unlock()
	return t.sink.Write(&lmeTraceRecord{
		CaseID:      t.caseID,
		ContentMode: t.mode,
		Event:       lmeTraceEventExtraction,
		Source:      cloneLMEBuildTraceSource(source),
		Extraction: &lmeTraceExtraction{
			Input:               traceLMEText(t.mode, inputJSON, true),
			EffectiveOperations: lmeTraceEffectiveOperationsUnavailable,
			UnavailableReason:   reason,
		},
	})
}

func (t *lmeCaseTrace) recordPersistence(
	ctx context.Context,
	source lmeBuildTraceSource,
	reader memory.Reader,
	userKey memory.UserKey,
	buildErr error,
) error {
	t.mu.Lock()
	before := t.currentSnapshot
	hasBefore := t.hasInitialSnapshot
	t.mu.Unlock()
	if !hasBefore {
		return fmt.Errorf("LongMemEval trace has no initial memory snapshot for %s", t.caseID)
	}
	var entries []*memory.Entry
	var readErr error
	if reader == nil {
		readErr = fmt.Errorf("memory reader is unavailable")
	} else {
		entries, readErr = reader.ReadMemories(ctx, userKey, lmeMemoryReadLimit)
	}
	after := buildLMEMemorySnapshot(entries, t.mode)
	diff := diffLMEMemorySnapshots(before, after)
	persistErr := errors.Join(buildErr, readErr)

	t.mu.Lock()
	if buildErr != nil {
		t.buildError = true
	}
	if readErr != nil {
		t.persistenceError = true
	} else {
		t.currentSnapshot = after
		t.associateSourceMemoriesLocked(source, after, diff)
	}
	t.mu.Unlock()
	record := &lmeTraceRecord{
		CaseID:      t.caseID,
		ContentMode: t.mode,
		Event:       lmeTraceEventPersistence,
		Source:      cloneLMEBuildTraceSource(source),
		Persistence: &lmeTracePersistence{
			Acknowledged:     buildErr == nil,
			Before:           before.refs,
			After:            after.refs,
			Diff:             diff,
			ActualOperations: lmeTraceEffectiveOperationsUnavailable,
			UnavailableReason: "the memory service exposes neither reconciled " +
				"operations nor per-operation persistence results",
			Error: lmeTraceError(persistErr),
		},
	}
	return errors.Join(readErr, t.sink.Write(record))
}

func (t *lmeCaseTrace) associateSourceMemoriesLocked(
	source lmeBuildTraceSource,
	after lmeTraceMemorySnapshot,
	diff lmeTraceMemoryDiff,
) {
	associate := func(memoryID string) {
		if t.memorySources[memoryID] == nil {
			t.memorySources[memoryID] = make(map[string]struct{})
		}
		t.memorySources[memoryID][source.SessionID] = struct{}{}
	}
	for _, ref := range append(append([]lmeTraceMemoryRef{}, diff.Added...), diff.Updated...) {
		associate(ref.ID)
	}
	evidence := t.sources[source.SourceID]
	if evidence == nil {
		return
	}
	matched := false
	for _, operationHash := range evidence.operationHashes {
		for memoryID, contentHash := range after.contentHashes {
			if operationHash == contentHash {
				associate(memoryID)
				matched = true
			}
		}
	}
	for _, operation := range evidence.operations {
		if operation.Type != string(extractor.OperationUpdate) || operation.MemoryID == "" {
			continue
		}
		if _, ok := after.byID[operation.MemoryID]; ok {
			associate(operation.MemoryID)
			matched = true
		}
	}
	if !matched && hasLMETraceUpdateOperation(evidence.operations) {
		t.reconciliationLoss[source.SessionID] = struct{}{}
	}
}

func (t *lmeCaseTrace) recordQA(steps []lmeStepTrace) error {
	t.mu.Lock()
	t.qaComplete = true
	t.mu.Unlock()
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Name != memory.SearchToolName {
				continue
			}
			retrieval := parseLMERetrievalTrace(t.mode, step.Step, call.Args, call.Result)
			t.mu.Lock()
			for _, hit := range retrieval.Hits {
				t.retrievedIDs[hit.MemoryID] = struct{}{}
			}
			t.mu.Unlock()
			if err := t.sink.Write(&lmeTraceRecord{
				CaseID:      t.caseID,
				ContentMode: t.mode,
				Event:       lmeTraceEventRetrieval,
				Retrieval:   &retrieval,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *lmeCaseTrace) joinGold(answerSessionIDs []string) error {
	t.mu.Lock()
	if !t.qaComplete {
		t.mu.Unlock()
		return fmt.Errorf("LongMemEval gold data cannot be joined before QA completes")
	}
	t.goldSessionIDs = sortedUniqueLMEStrings(answerSessionIDs)
	gold := append([]string(nil), t.goldSessionIDs...)
	t.mu.Unlock()
	return t.sink.Write(&lmeTraceRecord{
		CaseID:      t.caseID,
		ContentMode: t.mode,
		Event:       lmeTraceEventGoldJoin,
		Gold: &lmeTraceGoldJoin{
			AnswerSessionIDs: gold,
			JoinedAfterQA:    true,
		},
	})
}

func (t *lmeCaseTrace) finish(result *lmeCaseResult, runErr error) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("LongMemEval trace for %s is already closed", t.caseID)
	}
	t.closed = true
	stage, recall := t.classifyLocked(result, runErr)
	observability := t.buildObservability
	if observability == "" {
		observability = lmeBuildObservabilityUnknown
	}
	if result != nil {
		result.FailureStage = stage
		result.BuildObservability = observability
		result.GoldSessionRecall = recall
	}
	correct := result != nil && result.Correct
	t.mu.Unlock()
	writeErr := t.sink.Write(&lmeTraceRecord{
		CaseID:      t.caseID,
		ContentMode: t.mode,
		Event:       lmeTraceEventOutcome,
		Outcome: &lmeTraceOutcome{
			FailureStage:       stage,
			BuildObservability: observability,
			GoldSessionRecall:  recall,
			Correct:            correct,
			Error:              lmeTraceError(runErr),
		},
	})
	return errors.Join(writeErr, t.sink.Close())
}

func (t *lmeCaseTrace) classifyLocked(
	result *lmeCaseResult,
	runErr error,
) (lmeFailureStage, *float64) {
	recall := t.goldSessionRecallLocked()
	if result != nil && result.Correct {
		return lmeFailureSuccess, recall
	}
	if t.judgeError || (result != nil && result.JudgeError != "") {
		return lmeFailureJudgeError, recall
	}
	if t.persistenceError {
		return lmeFailurePersistenceError, recall
	}
	if t.buildError {
		return lmeFailureBuildError, recall
	}
	if t.hasGoldExtractionMissLocked() {
		return lmeFailureExtractionMiss, recall
	}
	if t.hasGoldReconciliationLossLocked() {
		return lmeFailureReconciliationLoss, recall
	}
	if recall != nil && *recall == 0 {
		if !t.hasGoldPersistedEvidenceLocked() {
			return lmeFailureBuildEvidenceMissing, recall
		}
		return lmeFailureRetrievalMiss, recall
	}
	if t.qaStarted || result != nil {
		return lmeFailureAnswerGenerationMiss, recall
	}
	if runErr != nil {
		return lmeFailureUnknown, recall
	}
	return lmeFailureUnknown, recall
}

func (t *lmeCaseTrace) hasGoldPersistedEvidenceLocked() bool {
	for _, sourceSessions := range t.memorySources {
		for _, goldSessionID := range t.goldSessionIDs {
			if _, ok := sourceSessions[goldSessionID]; ok {
				return true
			}
		}
	}
	return false
}

func (t *lmeCaseTrace) goldSessionRecallLocked() *float64 {
	if len(t.goldSessionIDs) == 0 {
		return nil
	}
	recalled := make(map[string]struct{})
	for memoryID := range t.retrievedIDs {
		for sessionID := range t.memorySources[memoryID] {
			recalled[sessionID] = struct{}{}
		}
	}
	matched := 0
	for _, sessionID := range t.goldSessionIDs {
		if _, ok := recalled[sessionID]; ok {
			matched++
		}
	}
	value := float64(matched) / float64(len(t.goldSessionIDs))
	return &value
}

func (t *lmeCaseTrace) hasGoldExtractionMissLocked() bool {
	if len(t.goldSessionIDs) == 0 || !t.extractionExpected {
		return false
	}
	for _, goldSessionID := range t.goldSessionIDs {
		for _, evidence := range t.sources {
			if evidence.sessionID == goldSessionID && hasLMETraceWriteOperation(evidence.operations) {
				return false
			}
		}
	}
	return true
}

func (t *lmeCaseTrace) hasGoldReconciliationLossLocked() bool {
	for _, sessionID := range t.goldSessionIDs {
		if _, ok := t.reconciliationLoss[sessionID]; ok {
			return true
		}
	}
	return false
}

type lmeTracingExtractor struct {
	inner        extractor.MemoryExtractor
	updatePolicy extractor.UpdatePolicy
}

func newLMETracingExtractor(
	inner extractor.MemoryExtractor,
	updatePolicy extractor.UpdatePolicy,
) extractor.MemoryExtractor {
	if inner == nil {
		return nil
	}
	return &lmeTracingExtractor{
		inner:        inner,
		updatePolicy: updatePolicy,
	}
}

func (e *lmeTracingExtractor) Extract(
	ctx context.Context,
	messages []model.Message,
	existing []*memory.Entry,
) ([]*extractor.Operation, error) {
	operations, extractErr := e.inner.Extract(ctx, messages, existing)
	trace := lmeCaseTraceFromContext(ctx)
	source, ok := lmeBuildTraceSourceFromContext(ctx)
	if trace == nil || !ok {
		return operations, extractErr
	}
	traceErr := trace.recordExtraction(source, messages, operations, extractErr)
	if traceErr != nil {
		return nil, errors.Join(extractErr, fmt.Errorf("record extraction trace: %w", traceErr))
	}
	return operations, extractErr
}

func (e *lmeTracingExtractor) ShouldExtract(ctx *extractor.ExtractionContext) bool {
	return e.inner.ShouldExtract(ctx)
}

func (e *lmeTracingExtractor) SetPrompt(prompt string) {
	e.inner.SetPrompt(prompt)
}

func (e *lmeTracingExtractor) SetModel(m model.Model) {
	e.inner.SetModel(m)
}

func (e *lmeTracingExtractor) Metadata() map[string]any {
	return e.inner.Metadata()
}

func (e *lmeTracingExtractor) UnwrapMemoryExtractor() extractor.MemoryExtractor {
	return e.inner
}

func (e *lmeTracingExtractor) UpdatePolicy() extractor.UpdatePolicy {
	return e.updatePolicy
}

func (e *lmeTracingExtractor) SetEnabledTools(enabled map[string]struct{}) {
	if configurable, ok := e.inner.(interface {
		SetEnabledTools(map[string]struct{})
	}); ok {
		configurable.SetEnabledTools(enabled)
	}
}
