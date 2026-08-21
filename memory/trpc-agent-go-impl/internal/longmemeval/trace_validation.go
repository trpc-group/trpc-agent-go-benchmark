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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
)

var lmeTraceSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	lmeTraceMaxFileBytes    int64 = 64 << 20
	lmeTraceMaxDecodedBytes int64 = 256 << 20
	lmeTraceMaxRecords            = 250_000
)

type lmeTraceReadLimits struct {
	fileBytes    int64
	decodedBytes int64
	records      int
}

var defaultLMETraceReadLimits = lmeTraceReadLimits{
	fileBytes:    lmeTraceMaxFileBytes,
	decodedBytes: lmeTraceMaxDecodedBytes,
	records:      lmeTraceMaxRecords,
}

type lmeTraceOutcomeSummary struct {
	CaseID      string
	ContentMode lmeTraceContentMode
	Outcome     lmeTraceOutcome
	RecordCount int
	Sources     []lmeBuildTraceSource
}

type lmeTraceSourceLifecycle struct {
	source      *lmeBuildTraceSource
	extraction  *lmeTraceExtraction
	persistence *lmeTracePersistence
}

type lmeTraceLifecycle struct {
	sources     map[string]*lmeTraceSourceLifecycle
	sourceOrder []string
	gold        bool
	outcome     *lmeTraceOutcome
}

type lmeTraceDecoderState struct {
	limits      lmeTraceReadLimits
	lifecycle   *lmeTraceLifecycle
	summary     *lmeTraceOutcomeSummary
	caseID      string
	contentMode lmeTraceContentMode
	sequence    uint64
	recordCount int
}

func newLMETraceLifecycle() *lmeTraceLifecycle {
	return &lmeTraceLifecycle{sources: make(map[string]*lmeTraceSourceLifecycle)}
}

// observe enforces extraction, persistence, retrieval, gold-join, and outcome
// ordering for each trace source.
func (l *lmeTraceLifecycle) observe(record *lmeTraceRecord) error {
	if record == nil {
		return fmt.Errorf("nil trace record")
	}
	switch record.Event {
	case lmeTraceEventExtraction:
		return l.observeExtraction(record)
	case lmeTraceEventPersistence:
		return l.observePersistence(record)
	case lmeTraceEventRetrieval:
		return l.observeRetrieval(record)
	case lmeTraceEventGoldJoin:
		return l.observeGoldJoin(record)
	case lmeTraceEventOutcome:
		return l.observeOutcome(record)
	default:
		return fmt.Errorf("unsupported event %q", record.Event)
	}
}

func (l *lmeTraceLifecycle) observeExtraction(record *lmeTraceRecord) error {
	state, err := l.source(record)
	if err != nil {
		return err
	}
	if state.extraction != nil || state.persistence != nil {
		return fmt.Errorf("source %s has duplicate or late extraction", record.Source.SourceID)
	}
	state.extraction = record.Extraction
	return nil
}

func (l *lmeTraceLifecycle) observePersistence(record *lmeTraceRecord) error {
	state, err := l.source(record)
	if err != nil {
		return err
	}
	if state.extraction == nil || state.persistence != nil {
		return fmt.Errorf("source %s has out-of-order persistence", record.Source.SourceID)
	}
	if err := validateLMETracePersistence(state.extraction, record.Persistence); err != nil {
		return fmt.Errorf("source %s: %w", record.Source.SourceID, err)
	}
	state.persistence = record.Persistence
	return nil
}

func (l *lmeTraceLifecycle) observeRetrieval(record *lmeTraceRecord) error {
	if record.Source != nil {
		return fmt.Errorf("retrieval event unexpectedly has a build source")
	}
	return l.requireBuildComplete()
}

func (l *lmeTraceLifecycle) observeGoldJoin(record *lmeTraceRecord) error {
	if record.Source != nil {
		return fmt.Errorf("gold_join event unexpectedly has a build source")
	}
	if l.gold || !record.Gold.JoinedAfterQA {
		return fmt.Errorf("gold_join event is duplicate or precedes QA")
	}
	if err := l.requireBuildComplete(); err != nil {
		return err
	}
	l.gold = true
	return nil
}

func (l *lmeTraceLifecycle) observeOutcome(record *lmeTraceRecord) error {
	if record.Source != nil {
		return fmt.Errorf("outcome event unexpectedly has a build source")
	}
	if l.outcome != nil {
		return fmt.Errorf("outcome event is duplicate")
	}
	if !l.gold {
		if err := validateLMEOutcomeWithoutGold(record.Outcome); err != nil {
			return fmt.Errorf("outcome precedes gold_join: %w", err)
		}
	}
	if err := l.requireBuildComplete(); err != nil {
		return err
	}
	l.outcome = record.Outcome
	return nil
}

func validateLMEOutcomeWithoutGold(outcome *lmeTraceOutcome) error {
	if outcome == nil {
		return fmt.Errorf("outcome is missing")
	}
	if outcome.Correct || outcome.GoldSessionRecall != nil || strings.TrimSpace(outcome.Error) == "" {
		return fmt.Errorf("only an errored, incorrect outcome without gold recall is allowed")
	}
	switch outcome.FailureStage {
	case lmeFailureBuildError,
		lmeFailurePersistenceError,
		lmeFailureAnswerGenerationMiss,
		lmeFailureUnknown:
		return nil
	default:
		return fmt.Errorf("failure stage %q requires a post-QA gold_join", outcome.FailureStage)
	}
}

func (l *lmeTraceLifecycle) source(
	record *lmeTraceRecord,
) (*lmeTraceSourceLifecycle, error) {
	if record.Source == nil || strings.TrimSpace(record.Source.SourceID) == "" ||
		strings.TrimSpace(record.Source.SessionID) == "" ||
		strings.TrimSpace(record.Source.ChunkID) == "" {
		return nil, fmt.Errorf("%s event has no complete build source", record.Event)
	}
	state := l.sources[record.Source.SourceID]
	if state == nil {
		state = &lmeTraceSourceLifecycle{source: cloneLMEBuildTraceSource(*record.Source)}
		l.sources[record.Source.SourceID] = state
		l.sourceOrder = append(l.sourceOrder, record.Source.SourceID)
	} else if !reflect.DeepEqual(state.source, record.Source) {
		return nil, fmt.Errorf("source %s identity changes between events", record.Source.SourceID)
	}
	return state, nil
}

func (l *lmeTraceLifecycle) requireBuildComplete() error {
	for _, sourceID := range l.sourceOrder {
		if l.sources[sourceID].persistence == nil {
			return fmt.Errorf("source %s has no persistence record", sourceID)
		}
	}
	return nil
}

func (l *lmeTraceLifecycle) finish() error {
	if l.outcome == nil {
		return fmt.Errorf("trace has no outcome lifecycle")
	}
	if len(l.sources) == 0 {
		if !l.gold {
			if err := validateLMEOutcomeWithoutGold(l.outcome); err == nil {
				return nil
			}
		}
		return fmt.Errorf("trace has no build evidence")
	}
	return l.requireBuildComplete()
}

func (l *lmeTraceLifecycle) orderedSources() []lmeBuildTraceSource {
	sources := make([]lmeBuildTraceSource, 0, len(l.sourceOrder))
	for _, sourceID := range l.sourceOrder {
		source := cloneLMEBuildTraceSource(*l.sources[sourceID].source)
		sources = append(sources, *source)
	}
	return sources
}

func validateLMETracePersistence(
	extraction *lmeTraceExtraction,
	persistence *lmeTracePersistence,
) error {
	if extraction == nil || persistence == nil {
		return fmt.Errorf("persistence lifecycle is incomplete")
	}
	if extraction.EffectiveOperations != lmeTraceEffectiveOperationsUnavailable ||
		strings.TrimSpace(extraction.UnavailableReason) == "" {
		return fmt.Errorf("build trace overstates backend operation visibility")
	}
	if persistence.ActualOperations != lmeTraceEffectiveOperationsUnavailable ||
		strings.TrimSpace(persistence.UnavailableReason) == "" {
		return fmt.Errorf("build trace overstates backend persistence visibility")
	}
	if persistence.Acknowledged && strings.TrimSpace(persistence.Error) != "" {
		return fmt.Errorf("acknowledged persistence has an error")
	}
	if !persistence.Acknowledged && strings.TrimSpace(persistence.Error) == "" {
		return fmt.Errorf("unacknowledged persistence has no error")
	}
	return nil
}

func readLMETraceOutcome(path string) (*lmeTraceOutcomeSummary, error) {
	return readLMETraceOutcomeWithLimits(path, defaultLMETraceReadLimits)
}

func readLMETraceOutcomeWithLimits(
	path string,
	limits lmeTraceReadLimits,
) (*lmeTraceOutcomeSummary, error) {
	if limits.fileBytes <= 0 || limits.decodedBytes <= 0 || limits.records <= 0 {
		return nil, fmt.Errorf("trace read limits must be positive")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect trace: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("trace must be a regular file")
	}
	if info.Size() > limits.fileBytes {
		return nil, fmt.Errorf("trace file exceeds the compressed size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open trace: %w", err)
	}
	defer file.Close()
	var reader io.Reader = bufio.NewReader(file)
	var gzipReader *gzip.Reader
	if strings.HasSuffix(path, ".gz") {
		gzipReader, err = gzip.NewReader(reader)
		if err != nil {
			return nil, fmt.Errorf("open gzip trace: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	limited := &io.LimitedReader{R: reader, N: limits.decodedBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	state := lmeTraceDecoderState{
		limits:    limits,
		lifecycle: newLMETraceLifecycle(),
	}
	for {
		var record lmeTraceRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode trace record %d: %w", state.recordCount+1, err)
		}
		if err := state.observe(&record); err != nil {
			return nil, err
		}
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("trace exceeds the decoded size limit")
	}
	return state.finish()
}

func (s *lmeTraceDecoderState) observe(record *lmeTraceRecord) error {
	s.recordCount++
	if s.recordCount > s.limits.records {
		return fmt.Errorf("trace exceeds the record limit")
	}
	if s.summary != nil {
		return fmt.Errorf("outcome is not the terminal trace record")
	}
	if record.SchemaVersion != lmeTraceSchemaVersion {
		return fmt.Errorf("record %d has schema %q", s.recordCount, record.SchemaVersion)
	}
	if record.Sequence != s.sequence+1 {
		return fmt.Errorf(
			"record %d has sequence %d, want %d",
			s.recordCount,
			record.Sequence,
			s.sequence+1,
		)
	}
	s.sequence = record.Sequence
	if record.RecordedAt.IsZero() {
		return fmt.Errorf("record %d has no recorded_at", s.recordCount)
	}
	if err := s.observeIdentity(record); err != nil {
		return err
	}
	if err := validateLMETraceRecordPayload(record); err != nil {
		return fmt.Errorf("record %d: %w", s.recordCount, err)
	}
	if err := validateLMETraceRecordRedaction(record); err != nil {
		return fmt.Errorf("record %d: %w", s.recordCount, err)
	}
	if err := s.lifecycle.observe(record); err != nil {
		return fmt.Errorf("record %d lifecycle: %w", s.recordCount, err)
	}
	if record.Event == lmeTraceEventOutcome {
		s.summary = &lmeTraceOutcomeSummary{
			CaseID:      s.caseID,
			ContentMode: s.contentMode,
			Outcome:     *record.Outcome,
		}
	}
	return nil
}

func (s *lmeTraceDecoderState) observeIdentity(record *lmeTraceRecord) error {
	if strings.TrimSpace(record.CaseID) == "" {
		return fmt.Errorf("record %d has no case_id", s.recordCount)
	}
	if s.caseID == "" {
		s.caseID = record.CaseID
	} else if record.CaseID != s.caseID {
		return fmt.Errorf("record %d changes case_id", s.recordCount)
	}
	if err := validateLMETraceContentMode(record.ContentMode); err != nil {
		return fmt.Errorf("record %d: %w", s.recordCount, err)
	}
	if s.contentMode == "" {
		s.contentMode = record.ContentMode
	} else if record.ContentMode != s.contentMode {
		return fmt.Errorf("record %d changes content mode", s.recordCount)
	}
	return nil
}

func (s *lmeTraceDecoderState) finish() (*lmeTraceOutcomeSummary, error) {
	if s.summary == nil {
		return nil, fmt.Errorf("trace has no outcome record")
	}
	if err := s.lifecycle.finish(); err != nil {
		return nil, err
	}
	s.summary.RecordCount = s.recordCount
	s.summary.Sources = s.lifecycle.orderedSources()
	return s.summary, nil
}

func validateLMETraceRecordPayload(record *lmeTraceRecord) error {
	payloads := countLMETracePayloads(record)
	if payloads != 1 {
		return fmt.Errorf("event %q has %d payloads", record.Event, payloads)
	}
	switch record.Event {
	case lmeTraceEventExtraction:
		if record.Extraction == nil ||
			record.Extraction.OperationCount != len(record.Extraction.Operations) {
			return fmt.Errorf("extraction event has the wrong payload")
		}
	case lmeTraceEventPersistence:
		if record.Persistence == nil {
			return fmt.Errorf("persistence event has the wrong payload")
		}
	case lmeTraceEventRetrieval:
		return validateLMERetrievalPayload(record.Retrieval)
	case lmeTraceEventGoldJoin:
		if record.Gold == nil {
			return fmt.Errorf("gold_join event has the wrong payload")
		}
	case lmeTraceEventOutcome:
		if record.Outcome == nil || record.Outcome.FailureStage == "" ||
			!validLMEBuildObservability(record.Outcome.BuildObservability) {
			return fmt.Errorf("outcome event is incomplete")
		}
	default:
		return fmt.Errorf("unsupported event %q", record.Event)
	}
	return nil
}

func countLMETracePayloads(record *lmeTraceRecord) int {
	payloads := 0
	for _, present := range []bool{
		record.Extraction != nil,
		record.Persistence != nil,
		record.Retrieval != nil,
		record.Gold != nil,
		record.Outcome != nil,
	} {
		if present {
			payloads++
		}
	}
	return payloads
}

func validateLMERetrievalPayload(retrieval *lmeTraceRetrieval) error {
	if retrieval == nil || retrieval.Step < 0 {
		return fmt.Errorf("retrieval event has the wrong payload")
	}
	for index, hit := range retrieval.Hits {
		if hit.Rank != index+1 || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
			return fmt.Errorf("retrieval event has invalid ranked hits")
		}
	}
	return nil
}

func validLMEBuildObservability(value lmeBuildObservability) bool {
	switch value {
	case lmeBuildObservabilityOperations,
		lmeBuildObservabilitySnapshotDiff,
		lmeBuildObservabilityUnknown:
		return true
	default:
		return false
	}
}

func validateLMETraceRecordRedaction(record *lmeTraceRecord) error {
	mode := record.ContentMode
	texts := make([]lmeTraceText, 0, 16)
	if record.Extraction != nil {
		texts = append(texts, record.Extraction.Input)
		for _, operation := range record.Extraction.Operations {
			texts = append(texts, operation.Memory)
			if err := validateLMETraceMetadata(mode, operation); err != nil {
				return err
			}
		}
	}
	if record.Persistence != nil {
		for _, ref := range append(append(
			append([]lmeTraceMemoryRef{}, record.Persistence.Before...),
			record.Persistence.After...),
			append(append([]lmeTraceMemoryRef{}, record.Persistence.Diff.Added...),
				append(record.Persistence.Diff.Updated, record.Persistence.Diff.Deleted...)...)...) {
			texts = append(texts, ref.Memory)
		}
	}
	if record.Retrieval != nil {
		texts = append(texts, record.Retrieval.Query)
	}
	for _, value := range texts {
		if err := validateLMETraceText(mode, value); err != nil {
			return err
		}
	}
	return nil
}

func validateLMETraceMetadata(mode lmeTraceContentMode, operation lmeTraceOperation) error {
	values := append(append([]string{}, operation.Topics...), operation.Participants...)
	if operation.Location != "" {
		values = append(values, operation.Location)
	}
	switch mode {
	case lmeTraceContentHash:
		for _, value := range values {
			if !strings.HasPrefix(value, lmeDigestAlgorithm+":") ||
				!lmeTraceSHA256Pattern.MatchString(strings.TrimPrefix(value, lmeDigestAlgorithm+":")) {
				return fmt.Errorf("hash trace contains unhashed operation metadata")
			}
		}
	case lmeTraceContentNone:
		if len(values) != 0 {
			return fmt.Errorf("none trace contains operation metadata")
		}
	case lmeTraceContentFull:
		for _, value := range values {
			if sanitizeLMETraceString(value) != value {
				return fmt.Errorf("full trace contains unsanitized operation metadata")
			}
		}
	}
	return nil
}

func validateLMETraceText(mode lmeTraceContentMode, value lmeTraceText) error {
	if value.Bytes < 0 || (value.SHA256 != "" && !lmeTraceSHA256Pattern.MatchString(value.SHA256)) {
		return fmt.Errorf("trace text has invalid length or digest")
	}
	switch mode {
	case lmeTraceContentHash:
		if value.Value != "" || value.SHA256 == "" {
			return fmt.Errorf("hash trace exposes content or omits its digest")
		}
	case lmeTraceContentNone:
		if value.Value != "" {
			return fmt.Errorf("none trace exposes content")
		}
	case lmeTraceContentFull:
		if sanitizeLMETraceString(value.Value) != value.Value {
			return fmt.Errorf("full trace contains unsanitized content")
		}
	}
	return nil
}

func validateLMETraceBuildSources(
	casePlan *lmeBuildCasePlan,
	summary *lmeTraceOutcomeSummary,
) error {
	if casePlan == nil || summary == nil {
		return fmt.Errorf("build plan case or trace summary is missing")
	}
	expected := expectedLMETraceBuildSources(casePlan)
	actual := summary.Sources
	if len(actual) > len(expected) {
		return fmt.Errorf("trace has %d sources, want at most %d", len(actual), len(expected))
	}
	for index := range actual {
		if !reflect.DeepEqual(actual[index], expected[index]) {
			return fmt.Errorf("source %d differs from the immutable build plan", index)
		}
	}
	if len(actual) == len(expected) {
		return nil
	}
	if summary.Outcome.FailureStage == lmeFailureBuildError ||
		summary.Outcome.FailureStage == lmeFailurePersistenceError {
		return nil
	}
	return fmt.Errorf("trace has %d sources, want %d", len(actual), len(expected))
}

func expectedLMETraceBuildSources(casePlan *lmeBuildCasePlan) []lmeBuildTraceSource {
	if casePlan == nil {
		return nil
	}
	sources := make([]lmeBuildTraceSource, 0, casePlan.Stats.ChunkCount)
	for _, sessionPlan := range casePlan.Sessions {
		for _, pair := range sessionPlan.Pairs {
			for _, chunk := range pair.Chunks {
				sources = append(sources, lmeBuildPlanTraceSource(
					casePlan.CaseID,
					sessionPlan,
					chunk,
				))
			}
		}
	}
	return sources
}

func validateLMETraceOutcome(record *lmeCaseResult, summary *lmeTraceOutcomeSummary) error {
	if record == nil || summary == nil {
		return fmt.Errorf("case result or trace outcome is missing")
	}
	if summary.Outcome.Correct != record.Correct {
		return fmt.Errorf("correct disagrees with the case result")
	}
	if summary.Outcome.FailureStage != record.FailureStage {
		return fmt.Errorf("failure stage disagrees with the case result")
	}
	if summary.Outcome.BuildObservability != record.BuildObservability {
		return fmt.Errorf("build observability disagrees with the case result")
	}
	if !equalLMEOptionalMetric(summary.Outcome.GoldSessionRecall, record.GoldSessionRecall) {
		return fmt.Errorf("gold session recall disagrees with the case result")
	}
	return nil
}

func equalLMEOptionalMetric(first, second *float64) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return closeLMEMetric(*first, *second)
}
