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
	"errors"
	"fmt"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const lmeReplayAutoJobPollInterval = 250 * time.Millisecond

type lmeBuildExecutionOptions struct {
	AppName                     string
	MemoryService               memory.Service
	MemoryReader                memory.Reader
	Ingestor                    session.Ingestor
	ExtractionUnavailableReason string
	Metadata                    func(lmeBuildSessionPlan, lmeBuildPairPlan, lmeBuildChunkPlan) map[string]any
}

type lmeBuildRuntime struct {
	appName            string
	opts               lmeBuildExecutionOptions
	baseSessionService *sessioninmemory.SessionService
	sessionService     *lmeReplaySessionService
	memoryWrapper      *lmeReplayMemoryService
	ingestorWrapper    *lmeReplayIngestor
	seedAgent          *lmeSeedAgent
	replayRunner       runner.Runner
}

// executeLMEBuildCase uses one Runner per case and preserves source-session
// identity while consuming each planned pair exactly once.
func executeLMEBuildCase(
	ctx context.Context,
	cfg lmeRunConfig,
	casePlan *lmeBuildCasePlan,
	opts lmeBuildExecutionOptions,
) error {
	if casePlan == nil {
		return fmt.Errorf("nil LongMemEval build case")
	}
	if casePlan.ReplayDigest != cfg.ReplayDigest || casePlan.ConfigDigest == "" {
		return fmt.Errorf("build case %s source digest mismatch", casePlan.CaseID)
	}
	if err := validateLMEBuildCasePlan(casePlan, cfg.BuildMaxTokens); err != nil {
		return fmt.Errorf("validate build case %s: %w", casePlan.CaseID, err)
	}
	runtime := newLMEBuildRuntime(cfg, opts)
	for _, sessionPlan := range casePlan.Sessions {
		if err := runtime.executeSession(ctx, cfg, casePlan.CaseID, sessionPlan); err != nil {
			buildErr := fmt.Errorf(
				"build case %s session %s: %w",
				casePlan.CaseID,
				sessionPlan.SessionID,
				err,
			)
			return errors.Join(buildErr, runtime.close())
		}
	}
	return runtime.close()
}

func newLMEBuildRuntime(
	cfg lmeRunConfig,
	opts lmeBuildExecutionOptions,
) *lmeBuildRuntime {
	appName := opts.AppName
	if appName == "" {
		appName = lmeAppAuto
	}
	baseSessionService := sessioninmemory.NewSessionService()
	sessionService := newLMEReplaySessionService(baseSessionService)
	memoryWrapper := newLMEReplayMemoryService(opts.MemoryService, lmeReplayAutoJobWaitTimeout(cfg))
	ingestorWrapper := newLMEReplayIngestor(opts.Ingestor)
	var memoryService memory.Service
	if opts.MemoryService != nil {
		memoryService = memoryWrapper
	}
	var ingestor session.Ingestor
	if opts.Ingestor != nil {
		ingestor = ingestorWrapper
	}
	seedAgent := &lmeSeedAgent{}
	runnerOptions := []runner.Option{
		runner.WithSessionService(sessionService),
		runner.WithMemoryService(memoryService),
	}
	if ingestor != nil {
		runnerOptions = append(runnerOptions, runner.WithSessionIngestor(ingestor))
	}
	return &lmeBuildRuntime{
		appName:            appName,
		opts:               opts,
		baseSessionService: baseSessionService,
		sessionService:     sessionService,
		memoryWrapper:      memoryWrapper,
		ingestorWrapper:    ingestorWrapper,
		seedAgent:          seedAgent,
		replayRunner:       runner.NewRunner(appName, seedAgent, runnerOptions...),
	}
}

func (r *lmeBuildRuntime) close() error {
	return errors.Join(r.replayRunner.Close(), r.baseSessionService.Close())
}

func (r *lmeBuildRuntime) executeSession(
	ctx context.Context,
	cfg lmeRunConfig,
	caseID string,
	plan lmeBuildSessionPlan,
) error {
	observedAt, err := time.Parse(time.RFC3339Nano, plan.ObservationTime)
	if err != nil {
		return fmt.Errorf("parse observation time: %w", err)
	}
	chunkOrdinal := 0
	for _, pair := range plan.Pairs {
		for _, chunk := range pair.Chunks {
			chunkTurns, userMessage, assistantMessage, err := lmeChunkExecutionMessages(chunk)
			if err != nil {
				return err
			}
			chunkOrdinal++
			eventTime := observedAt.Add(time.Duration(chunkOrdinal) * time.Microsecond)
			metadata := map[string]any(nil)
			if r.opts.Metadata != nil {
				metadata = r.opts.Metadata(plan, pair, chunk)
			}
			sessionToken := r.sessionService.expect(chunkTurns, plan.SessionID, eventTime)
			memoryToken := r.memoryWrapper.expect(plan.SessionID, observedAt)
			ingestorToken := r.ingestorWrapper.expect(plan.SessionID, metadata)
			r.seedAgent.setAssistantMessage(assistantMessage)
			runCtx := withLMEEmbeddingPhase(ctx, lmeEmbeddingPhaseMemoryBuild)
			source := lmeBuildPlanTraceSource(caseID, plan, chunk)
			runCtx = withLMEBuildTraceSource(runCtx, source)
			if err := recordLMEUnavailableExtraction(
				runCtx,
				source,
				lmeBuildChunkTraceMessages(chunk),
				r.opts,
			); err != nil {
				return err
			}
			_, err = runLMERunnerCompletionWithRetry(runCtx, cfg.MaxRetries, func() (<-chan *event.Event, error) {
				return r.replayRunner.Run(runCtx, caseID, plan.SessionID, userMessage)
			})
			var buildErr error
			if err != nil {
				buildErr = fmt.Errorf("run build chunk %s: %w", chunk.ChunkID, err)
			}
			if err := r.sessionService.verify(sessionToken); err != nil {
				buildErr = errors.Join(
					buildErr,
					fmt.Errorf("verify replay build chunk %s: %w", chunk.ChunkID, err),
				)
			}
			if err := r.memoryWrapper.verify(memoryToken); err != nil {
				buildErr = errors.Join(
					buildErr,
					fmt.Errorf("acknowledge auto build chunk %s: %w", chunk.ChunkID, err),
				)
			}
			if err := r.ingestorWrapper.verify(ingestorToken); err != nil {
				buildErr = errors.Join(
					buildErr,
					fmt.Errorf("acknowledge ingestor build chunk %s: %w", chunk.ChunkID, err),
				)
			}
			if err := finishLMEBuildChunkTrace(
				runCtx,
				source,
				r.opts,
				r.appName,
				caseID,
				buildErr,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func lmeBuildPlanTraceSource(
	caseID string,
	sessionPlan lmeBuildSessionPlan,
	chunk lmeBuildChunkPlan,
) lmeBuildTraceSource {
	turnIDs := make([]string, 0, len(chunk.Turns))
	for _, turn := range chunk.Turns {
		turnIDs = append(turnIDs, turn.SourceTurnID)
	}
	return newLMEBuildTraceSource(
		caseID,
		sessionPlan.SessionID,
		sessionPlan.SessionID,
		turnIDs,
		chunk.ChunkID,
		sessionPlan.ObservationTime,
	)
}

func recordLMEUnavailableExtraction(
	ctx context.Context,
	source lmeBuildTraceSource,
	messages []model.Message,
	opts lmeBuildExecutionOptions,
) error {
	if opts.ExtractionUnavailableReason == "" {
		return nil
	}
	trace := lmeCaseTraceFromContext(ctx)
	if trace == nil {
		return nil
	}
	if err := trace.recordExtractionUnavailable(
		source,
		messages,
		opts.ExtractionUnavailableReason,
	); err != nil {
		return fmt.Errorf("record unavailable extraction trace: %w", err)
	}
	return nil
}

func lmeBuildChunkTraceMessages(chunk lmeBuildChunkPlan) []model.Message {
	messages := make([]model.Message, 0, len(chunk.Turns))
	for _, turn := range chunk.Turns {
		if turn.Content == "" {
			continue
		}
		role := model.RoleUser
		if turn.Role == "assistant" {
			role = model.RoleAssistant
		}
		messages = append(messages, model.Message{Role: role, Content: turn.Content})
	}
	return messages
}

func finishLMEBuildChunkTrace(
	ctx context.Context,
	source lmeBuildTraceSource,
	opts lmeBuildExecutionOptions,
	appName string,
	caseID string,
	buildErr error,
) error {
	trace := lmeCaseTraceFromContext(ctx)
	if trace == nil {
		return buildErr
	}
	reader := opts.MemoryReader
	if reader == nil && opts.MemoryService != nil {
		reader = opts.MemoryService
	}
	traceErr := trace.recordPersistence(
		ctx,
		source,
		reader,
		memory.UserKey{AppName: appName, UserID: caseID},
		buildErr,
	)
	return errors.Join(buildErr, traceErr)
}

func lmeChunkExecutionMessages(
	chunk lmeBuildChunkPlan,
) ([]lmeReplayTurn, model.Message, model.Message, error) {
	var userTurn *lmeReplayTurn
	var assistantTurn *lmeReplayTurn
	for _, part := range chunk.Turns {
		if part.Role != "user" && part.Role != "assistant" {
			return nil, model.Message{}, model.Message{}, fmt.Errorf(
				"chunk %s has unsupported role %q",
				chunk.ChunkID,
				part.Role,
			)
		}
		turn := lmeReplayTurn{
			TurnIndex: part.SourceTurnIndex,
			TurnID:    part.SourceTurnID,
			Role:      part.Role,
			Content:   part.Content,
		}
		if part.Role == "user" {
			if err := mergeLMEChunkTurn(&userTurn, turn); err != nil {
				return nil, model.Message{}, model.Message{}, err
			}
			continue
		}
		if err := mergeLMEChunkTurn(&assistantTurn, turn); err != nil {
			return nil, model.Message{}, model.Message{}, err
		}
	}
	var expected []lmeReplayTurn
	var userMessage model.Message
	assistantMessage := model.NewAssistantMessage("")
	if userTurn != nil {
		if userTurn.Content != "" {
			expected = append(expected, *userTurn)
		}
		userMessage = model.NewUserMessage(userTurn.Content)
	}
	if assistantTurn != nil {
		if assistantTurn.Content != "" {
			expected = append(expected, *assistantTurn)
			assistantMessage = model.NewAssistantMessage(assistantTurn.Content)
		}
	}
	return expected, userMessage, assistantMessage, nil
}

func mergeLMEChunkTurn(target **lmeReplayTurn, part lmeReplayTurn) error {
	if *target == nil {
		copyPart := part
		*target = &copyPart
		return nil
	}
	if (*target).TurnID != part.TurnID || (*target).TurnIndex != part.TurnIndex {
		return fmt.Errorf("chunk combines multiple %s source turns", part.Role)
	}
	(*target).Content += part.Content
	return nil
}

type lmeReplaySessionService struct {
	session.Service

	mu              sync.Mutex
	sessions        map[session.Key]*session.Session
	expected        []lmeReplayTurn
	expectedSession string
	eventTime       time.Time
	matched         int
	generation      uint64
	err             error
}

func newLMEReplaySessionService(base session.Service) *lmeReplaySessionService {
	return &lmeReplaySessionService{Service: base, sessions: make(map[session.Key]*session.Session)}
}

func (s *lmeReplaySessionService) CreateSession(
	ctx context.Context,
	key session.Key,
	state session.StateMap,
	options ...session.Option,
) (*session.Session, error) {
	sess, err := s.Service.CreateSession(ctx, key, state, options...)
	if err != nil || sess == nil {
		return sess, err
	}
	s.store(sess)
	return sess, nil
}

func (s *lmeReplaySessionService) GetSession(
	ctx context.Context,
	key session.Key,
	options ...session.Option,
) (*session.Session, error) {
	s.mu.Lock()
	stored := s.sessions[key]
	s.mu.Unlock()
	if stored != nil {
		return stored.Clone(), nil
	}
	return s.Service.GetSession(ctx, key, options...)
}

func (s *lmeReplaySessionService) AppendEvent(
	_ context.Context,
	sess *session.Session,
	evt *event.Event,
	_ ...session.Option,
) error {
	if sess == nil {
		return session.ErrNilSession
	}
	key := session.Key{AppName: sess.AppName, UserID: sess.UserID, SessionID: sess.ID}
	if err := key.CheckSessionKey(); err != nil {
		return err
	}
	replayEvents, err := s.cleanReplayEvents(sess.ID, evt)
	if err != nil {
		return err
	}
	if len(replayEvents) > 0 {
		sess.EventMu.Lock()
		sess.Events = append(sess.Events, replayEvents...)
		sess.EventMu.Unlock()
		sess.UpdatedAt = replayEvents[len(replayEvents)-1].Timestamp
	} else {
		sess.UpdatedAt = time.Now()
	}
	if evt != nil {
		sess.ApplyEventStateDelta(evt)
	}
	s.store(sess)
	return nil
}

func (s *lmeReplaySessionService) expect(
	expected []lmeReplayTurn,
	sessionID string,
	eventTime time.Time,
) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.expected = append([]lmeReplayTurn(nil), expected...)
	s.expectedSession = sessionID
	s.eventTime = eventTime
	s.matched = 0
	s.err = nil
	return s.generation
}

func (s *lmeReplaySessionService) cleanReplayEvents(
	sessionID string,
	evt *event.Event,
) ([]event.Event, error) {
	if evt == nil || evt.Response == nil || evt.IsPartial || !evt.IsValidContent() {
		return nil, nil
	}
	payloads := lmeReplayEventPayloads(*evt)
	if len(payloads) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID != s.expectedSession {
		s.err = fmt.Errorf("replay event uses session %q, want %q", sessionID, s.expectedSession)
		return nil, s.err
	}
	cleaned := make([]event.Event, 0, len(payloads))
	for _, payload := range payloads {
		if s.matched >= len(s.expected) {
			s.err = fmt.Errorf(
				"lme replay unexpected extra message after %d turns: role=%s content=%q",
				len(s.expected),
				payload.role,
				truncateLME(payload.content, 160),
			)
			return nil, s.err
		}
		turn := s.expected[s.matched]
		if payload.role != turn.Role || payload.content != turn.Content {
			s.err = fmt.Errorf(
				"lme replay mismatch at expected turn %d: got role=%s content=%q, want role=%s content=%q",
				s.matched,
				payload.role,
				truncateLME(payload.content, 160),
				turn.Role,
				truncateLME(turn.Content, 160),
			)
			return nil, s.err
		}
		cleanEvent, err := cleanLMEReplayEvent(
			payload.event,
			payload.choice,
			turn,
			s.eventTime.Add(time.Duration(s.matched)*time.Nanosecond),
		)
		if err != nil {
			s.err = err
			return nil, err
		}
		cleaned = append(cleaned, cleanEvent)
		s.matched++
	}
	return cleaned, nil
}

func (s *lmeReplaySessionService) verify(generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return fmt.Errorf("replay session service generation is %d, want %d", s.generation, generation)
	}
	if s.err != nil {
		return s.err
	}
	if s.matched != len(s.expected) {
		return fmt.Errorf("lme replay matched %d turns, want %d", s.matched, len(s.expected))
	}
	return nil
}

func (s *lmeReplaySessionService) store(sess *session.Session) {
	if sess == nil {
		return
	}
	key := session.Key{AppName: sess.AppName, UserID: sess.UserID, SessionID: sess.ID}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Keep the live session so asynchronous memory extraction can persist its
	// watermark before the next turn loads this session.
	s.sessions[key] = sess
}

type lmeReplayMemoryService struct {
	memory.Service

	waitTimeout   time.Duration
	mu            sync.Mutex
	sessionID     string
	referenceDate time.Time
	generation    uint64
	acknowledged  uint64
	err           error
}

func newLMEReplayMemoryService(service memory.Service, waitTimeout time.Duration) *lmeReplayMemoryService {
	return &lmeReplayMemoryService{Service: service, waitTimeout: waitTimeout}
}

func (s *lmeReplayMemoryService) expect(
	sessionID string,
	referenceDate time.Time,
) uint64 {
	if s == nil || s.Service == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.sessionID = sessionID
	s.referenceDate = referenceDate
	s.err = nil
	return s.generation
}

func (s *lmeReplayMemoryService) EnqueueAutoMemoryJob(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	sessionID := s.sessionID
	referenceDate := s.referenceDate
	generation := s.generation
	s.mu.Unlock()
	var err error
	if sess == nil {
		err = session.ErrNilSession
	} else if sess.ID != sessionID {
		err = fmt.Errorf("auto memory received session %q, want %q", sess.ID, sessionID)
	}
	if err == nil {
		ctx = extractor.WithReferenceDate(ctx, referenceDate)
		err = s.Service.EnqueueAutoMemoryJob(ctx, sess)
	}
	if err == nil {
		err = waitForLMEReplayAutoMemoryJob(ctx, sess, s.waitTimeout)
	}
	s.mu.Lock()
	s.acknowledged = generation
	s.err = err
	s.mu.Unlock()
	return err
}

func (s *lmeReplayMemoryService) verify(generation uint64) error {
	if generation == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acknowledged != generation {
		return fmt.Errorf("memory service did not acknowledge generation %d", generation)
	}
	return s.err
}

type lmeReplayIngestor struct {
	session.Ingestor

	mu           sync.Mutex
	sessionID    string
	metadata     map[string]any
	generation   uint64
	acknowledged uint64
	err          error
}

func newLMEReplayIngestor(ingestor session.Ingestor) *lmeReplayIngestor {
	return &lmeReplayIngestor{Ingestor: ingestor}
}

func (s *lmeReplayIngestor) expect(
	sessionID string,
	metadata map[string]any,
) uint64 {
	if s == nil || s.Ingestor == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.sessionID = sessionID
	s.metadata = cloneLMEStringAnyMap(metadata)
	s.err = nil
	return s.generation
}

func (s *lmeReplayIngestor) IngestSession(
	ctx context.Context,
	sess *session.Session,
	opts ...session.IngestOption,
) error {
	s.mu.Lock()
	sessionID := s.sessionID
	metadata := cloneLMEStringAnyMap(s.metadata)
	generation := s.generation
	s.mu.Unlock()
	var err error
	if sess == nil {
		err = session.ErrNilSession
	} else if sess.ID != sessionID {
		err = fmt.Errorf("session ingestor received session %q, want %q", sess.ID, sessionID)
	}
	if err == nil && len(metadata) > 0 {
		opts = append(opts, session.WithIngestMetadata(metadata))
	}
	if err == nil {
		opts = append(opts, session.WithIngestRunID(sessionID))
	}
	if err == nil {
		err = s.Ingestor.IngestSession(ctx, sess, opts...)
	}
	s.mu.Lock()
	s.acknowledged = generation
	s.err = err
	s.mu.Unlock()
	return err
}

func (s *lmeReplayIngestor) verify(generation uint64) error {
	if generation == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acknowledged != generation {
		return fmt.Errorf("session ingestor did not acknowledge generation %d", generation)
	}
	return s.err
}

func cloneLMEStringAnyMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func lmeReplayAutoJobWaitTimeout(cfg lmeRunConfig) time.Duration {
	if cfg.AutoExtractionWait > 0 {
		return cfg.AutoExtractionWait + 30*time.Second
	}
	return autoMemoryJobTimeout + 30*time.Second
}

func waitForLMEReplayAutoMemoryJob(
	ctx context.Context,
	sess *session.Session,
	timeout time.Duration,
) error {
	latest := latestLMEReplayEventTime(sess)
	if latest.IsZero() {
		return nil
	}
	if timeout <= 0 {
		timeout = autoMemoryJobTimeout + 30*time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(lmeReplayAutoJobPollInterval)
	defer ticker.Stop()
	for {
		raw, ok := sess.GetState(memory.SessionStateKeyAutoMemoryLastExtractAt)
		if ok && len(raw) > 0 {
			timestamp, err := time.Parse(time.RFC3339Nano, string(raw))
			if err != nil {
				return fmt.Errorf("invalid auto memory extraction timestamp %q: %w", string(raw), err)
			}
			if !timestamp.Before(latest.UTC()) {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf(
				"auto memory job did not finish for %s/%s/%s before %s",
				sess.AppName,
				sess.UserID,
				sess.ID,
				timeout,
			)
		case <-ticker.C:
		}
	}
}

func latestLMEReplayEventTime(sess *session.Session) time.Time {
	if sess == nil {
		return time.Time{}
	}
	var latest time.Time
	sess.EventMu.RLock()
	defer sess.EventMu.RUnlock()
	for _, evt := range sess.Events {
		if evt.Timestamp.After(latest) {
			latest = evt.Timestamp
		}
	}
	return latest
}

func runLMERunnerCompletionWithRetry(
	ctx context.Context,
	maxRetries int,
	run func() (<-chan *event.Event, error),
) (int, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		ch, err := run()
		if err == nil {
			err = drainLMERunnerEvents(ch)
			if err == nil {
				return attempt, nil
			}
		}
		lastErr = err
		if !isLMETransportError(err) || attempt == maxRetries {
			return attempt, err
		}
		if sleepErr := lmeRetrySleep(ctx, attempt); sleepErr != nil {
			return attempt, sleepErr
		}
	}
	return maxRetries, lastErr
}

func drainLMERunnerEvents(ch <-chan *event.Event) error {
	for evt := range ch {
		if evt == nil {
			continue
		}
		if evt.Error != nil {
			return fmt.Errorf("%s", evt.Error.Message)
		}
		if evt.Response != nil && evt.Response.Error != nil {
			return fmt.Errorf("%s", evt.Response.Error.Message)
		}
	}
	return nil
}
