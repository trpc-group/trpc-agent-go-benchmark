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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	memorymem0 "trpc.group/trpc-go/trpc-agent-go/memory/mem0"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type lmeMem0OSSEvaluator struct {
	judgeLLM model.Model
	qaLLM    model.Model
	mem      memory.Service
	ingestor session.Ingestor
	cfg      lmeRunConfig
	cost     *lmeCostTracker
	proxyLog *lmeProxyUsageReader
	trace    *lmeTraceManager
}

// lmeMem0OSSService delegates reads and searches to the production Mem0
// service. Ingestion remains synchronous here because a benchmark must observe
// provider failures before advancing to QA, while Service.IngestSession queues
// work and uses one service-level prompt. LongMemEval supplies a different
// reference date for each ingestion request.
type lmeMem0OSSService struct {
	inner   *memorymem0.Service
	host    string
	apiKey  string
	client  *http.Client
	timeout time.Duration
}

func newLMEMem0OSSEvaluator(
	llm model.Model,
	cfg lmeRunConfig,
) (*lmeMem0OSSEvaluator, error) {
	cost := newLMECostTracker()
	trace, err := newLMETraceManager(cfg.TraceContentMode, cfg.TraceGzip)
	if err != nil {
		return nil, err
	}
	memSvc, err := newLMEMem0OSSService(cfg)
	if err != nil {
		return nil, err
	}
	proxyLog, err := newLMEProxyUsageReader(cfg.Mem0ProxyUsageLog, cfg.Mem0ProxyRunID)
	if err != nil {
		_ = memSvc.Close()
		return nil, err
	}
	return &lmeMem0OSSEvaluator{
		judgeLLM: newLMETrackedModel(llm, cost, lmeLLMPhaseJudge),
		qaLLM:    newLMETrackedModel(llm, cost, lmeLLMPhaseQA),
		mem:      memSvc,
		ingestor: memSvc,
		cfg:      cfg,
		cost:     cost,
		proxyLog: proxyLog,
		trace:    trace,
	}, nil
}

func newLMEMem0OSSService(cfg lmeRunConfig) (*lmeMem0OSSService, error) {
	host := strings.TrimSpace(cfg.Mem0Host)
	if host == "" {
		return nil, fmt.Errorf("mem0 host is required")
	}
	apiKey := strings.TrimSpace(lmeRuntime.Mem0APIKey)
	httpClient := &http.Client{}
	opts := []memorymem0.ServiceOpt{
		memorymem0.WithHost(host),
		memorymem0.WithSelfHostedOSS(),
		memorymem0.WithAsyncMode(false),
		memorymem0.WithHTTPClient(httpClient),
	}
	if apiKey != "" {
		opts = append(opts, memorymem0.WithAPIKey(apiKey))
	}
	if cfg.Mem0IngestTimeout > 0 {
		opts = append(opts,
			memorymem0.WithTimeout(cfg.Mem0IngestTimeout),
			memorymem0.WithMemoryJobTimeout(cfg.Mem0IngestTimeout),
		)
	}
	requestTimeout := cfg.Mem0IngestTimeout
	if requestTimeout <= 0 {
		requestTimeout = lmeMem0DefaultRequestTimeout
	}
	svc, err := memorymem0.NewService(opts...)
	if err != nil {
		return nil, err
	}
	checkTimeout := lmeMem0DefaultRequestTimeout
	if requestTimeout < checkTimeout {
		checkTimeout = requestTimeout
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	_, err = svc.ReadMemories(checkCtx, memory.UserKey{
		AppName: lmeAppMem0,
		UserID:  "__lme_healthcheck__",
	}, 1)
	if err != nil {
		_ = svc.Close()
		return nil, fmt.Errorf("mem0 health check: %w", err)
	}
	return &lmeMem0OSSService{
		inner:   svc,
		host:    host,
		apiKey:  apiKey,
		client:  httpClient,
		timeout: requestTimeout,
	}, nil
}

func (s *lmeMem0OSSService) AddMemory(
	context.Context,
	memory.UserKey,
	string,
	[]string,
	...memory.AddOption,
) error {
	return fmt.Errorf("mem0_oss benchmark service does not support direct AddMemory")
}

func (s *lmeMem0OSSService) UpdateMemory(
	context.Context,
	memory.Key,
	string,
	[]string,
	...memory.UpdateOption,
) error {
	return fmt.Errorf("mem0_oss benchmark service does not support direct UpdateMemory")
}

func (s *lmeMem0OSSService) DeleteMemory(ctx context.Context, key memory.Key) error {
	if err := key.CheckMemoryKey(); err != nil {
		return err
	}
	return s.doOSS(ctx, http.MethodDelete, "/memories/"+url.PathEscape(key.MemoryID), nil)
}

func (s *lmeMem0OSSService) ClearMemories(
	ctx context.Context,
	userKey memory.UserKey,
) error {
	if err := userKey.CheckUserKey(); err != nil {
		return err
	}
	q := url.Values{}
	q.Set("user_id", userKey.UserID)
	if err := s.doOSS(ctx, http.MethodDelete, "/memories", q); err != nil {
		return err
	}
	if err := clearResidualLMEMem0Memories(ctx, s, userKey); err != nil {
		return fmt.Errorf("clear residual mem0 memories: %w", err)
	}
	return nil
}

func (s *lmeMem0OSSService) ReadMemories(
	ctx context.Context,
	userKey memory.UserKey,
	limit int,
) ([]*memory.Entry, error) {
	requested := limit
	if limit > lmeMem0MemoryReadLimit {
		limit = lmeMem0MemoryReadLimit
	}
	entries, err := s.inner.ReadMemories(ctx, userKey, limit)
	if err != nil {
		return nil, err
	}
	if requested > limit && len(entries) == limit {
		return nil, fmt.Errorf(
			"mem0 snapshot reached the self-hosted OSS observable limit %d",
			limit,
		)
	}
	return entries, nil
}

func (s *lmeMem0OSSService) SearchMemories(
	ctx context.Context,
	userKey memory.UserKey,
	query string,
	opts ...memory.SearchOption,
) ([]*memory.Entry, error) {
	return s.inner.SearchMemories(ctx, userKey, query, opts...)
}

func (s *lmeMem0OSSService) Tools() []tool.Tool {
	return s.inner.Tools()
}

func (s *lmeMem0OSSService) EnqueueAutoMemoryJob(
	context.Context,
	*session.Session,
) error {
	return nil
}

func (s *lmeMem0OSSService) IngestSession(
	ctx context.Context,
	sess *session.Session,
	opts ...session.IngestOption,
) error {
	if sess == nil {
		return nil
	}
	userKey := memory.UserKey{AppName: sess.AppName, UserID: sess.UserID}
	if err := userKey.CheckUserKey(); err != nil {
		return err
	}
	latest, messages, err := lmeMem0DeltaMessages(sess)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	if latest.IsZero() {
		return errors.New("mem0_oss benchmark ingestion has no event timestamp")
	}
	var ingestOpts session.IngestOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&ingestOpts)
		}
	}
	metadata := cloneLMEStringAnyMap(ingestOpts.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[lmeMem0MetadataAppName] = userKey.AppName
	prompt, err := lmeMem0ReferenceDatePrompt(metadata)
	if err != nil {
		return err
	}
	req := lmeMem0OSSCreateRequest{
		Messages: messages,
		UserID:   userKey.UserID,
		AgentID:  ingestOpts.AgentID,
		RunID:    ingestOpts.RunID,
		Metadata: metadata,
		Infer:    true,
		Prompt:   prompt,
	}
	if err := s.doOSSJSON(ctx, http.MethodPost, "/memories", nil, req); err != nil {
		return fmt.Errorf("mem0_oss benchmark ingestion: %w", err)
	}
	sess.SetState(
		memory.SessionStateKeyAutoMemoryLastExtractAt,
		[]byte(latest.UTC().Format(time.RFC3339Nano)),
	)
	return nil
}

func (s *lmeMem0OSSService) Close() error {
	if s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *lmeMem0OSSService) doOSS(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
) error {
	return s.doOSSJSON(ctx, method, path, query, nil)
}

func (s *lmeMem0OSSService) doOSSJSON(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	payload any,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	req, err := s.newOSSRequest(ctx, method, path, query, payload)
	if err != nil {
		return err
	}
	client := s.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("mem0_oss benchmark service: request failed: %w", ctxErr)
		}
		return errors.New("mem0_oss benchmark service: request failed")
	}
	defer resp.Body.Close()
	return validateLMEMem0OSSResponse(resp)
}

func (s *lmeMem0OSSService) newOSSRequest(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	payload any,
) (*http.Request, error) {
	u, err := url.Parse(strings.TrimRight(s.host, "/"))
	if err != nil {
		return nil, fmt.Errorf("mem0_oss benchmark service: invalid host: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("mem0_oss benchmark service: marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("mem0_oss benchmark service: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("X-API-Key", s.apiKey)
	}
	return req, nil
}

func validateLMEMem0OSSResponse(resp *http.Response) error {
	readBytes, readErr := io.Copy(
		io.Discard,
		io.LimitReader(resp.Body, lmeMem0MaxResponseBodySize+1),
	)
	if readErr != nil {
		return fmt.Errorf("mem0_oss benchmark service: read response: %w", readErr)
	}
	if readBytes > lmeMem0MaxResponseBodySize {
		return errors.New("mem0_oss benchmark service: response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mem0_oss benchmark service: status=%d", resp.StatusCode)
	}
	return nil
}

const (
	lmeMem0MetadataAppName         = "trpc_app_name"
	lmeMem0MetadataObservationTime = "longmemeval_observation_time"
	lmeMem0MaxResponseBodySize     = 10 << 20
	lmeMem0MaxPreflightSize        = 1 << 20
	lmeMem0DefaultRequestTimeout   = 10 * time.Second
)

type lmeMem0OSSMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type lmeMem0OSSCreateRequest struct {
	Messages []lmeMem0OSSMessage `json:"messages"`
	UserID   string              `json:"user_id,omitempty"`
	AgentID  string              `json:"agent_id,omitempty"`
	RunID    string              `json:"run_id,omitempty"`
	Metadata map[string]any      `json:"metadata,omitempty"`
	Infer    bool                `json:"infer"`
	Prompt   string              `json:"prompt"`
}

type lmeMem0PreflightDocument struct {
	Status          string `json:"status"`
	ServiceURL      string `json:"service_url"`
	EnvironmentLock struct {
		SHA256 string `json:"sha256"`
	} `json:"environment_lock"`
	Runtime struct {
		Source struct {
			Commit string `json:"commit"`
		} `json:"source"`
		Distribution struct {
			Version string `json:"version"`
		} `json:"distribution"`
		Runtime struct {
			LLMModel      string `json:"llm_model"`
			EmbedderModel string `json:"embedder_model"`
		} `json:"runtime"`
	} `json:"runtime"`
	Capabilities map[string]bool `json:"capabilities"`
}

type lmeMem0PreflightSummary struct {
	Digest                string
	EnvironmentLockDigest string
	ServiceURL            string
	SourceCommit          string
	Version               string
	LLMModel              string
	EmbedModel            string
}

func loadLMEMem0Preflight(path string) (lmeMem0PreflightSummary, error) {
	data, err := readLMEMem0Preflight(path)
	if err != nil {
		return lmeMem0PreflightSummary{}, err
	}
	var document lmeMem0PreflightDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return lmeMem0PreflightSummary{}, fmt.Errorf("decode Mem0 preflight: %w", err)
	}
	if document.Status != "ok" {
		return lmeMem0PreflightSummary{}, errors.New("Mem0 preflight status is not ok")
	}
	for _, capability := range []string{
		"bm25_scoring",
		"configuration",
		"entity_scoring",
		"llm_generation",
		"memory_create",
		"memory_search",
		"memory_delete",
		"observation_prompt",
		"search_explain",
	} {
		if !document.Capabilities[capability] {
			return lmeMem0PreflightSummary{}, fmt.Errorf(
				"Mem0 preflight capability %q is not verified",
				capability,
			)
		}
	}
	lockDigest := strings.TrimSpace(document.EnvironmentLock.SHA256)
	if len(lockDigest) != sha256.Size*2 {
		return lmeMem0PreflightSummary{}, errors.New("Mem0 preflight environment lock digest is invalid")
	}
	if _, err := hex.DecodeString(lockDigest); err != nil {
		return lmeMem0PreflightSummary{}, errors.New("Mem0 preflight environment lock digest is invalid")
	}
	sourceCommit := strings.TrimSpace(document.Runtime.Source.Commit)
	if len(sourceCommit) != 40 || !lmeImmutableRevisionPattern.MatchString(sourceCommit) {
		return lmeMem0PreflightSummary{}, errors.New("Mem0 preflight source commit is not immutable")
	}
	version := strings.TrimSpace(document.Runtime.Distribution.Version)
	llmModel := strings.TrimSpace(document.Runtime.Runtime.LLMModel)
	embedModel := strings.TrimSpace(document.Runtime.Runtime.EmbedderModel)
	if version == "" || llmModel == "" || embedModel == "" {
		return lmeMem0PreflightSummary{}, errors.New("Mem0 preflight runtime identity is incomplete")
	}
	serviceURL := sanitizeEndpoint(document.ServiceURL)
	if serviceURL == "" || serviceURL == lmeRedactedValue {
		return lmeMem0PreflightSummary{}, errors.New("Mem0 preflight service URL is invalid")
	}
	digest := sha256.Sum256(data)
	return lmeMem0PreflightSummary{
		Digest:                lmeDigestAlgorithm + ":" + hex.EncodeToString(digest[:]),
		EnvironmentLockDigest: lmeDigestAlgorithm + ":" + strings.ToLower(lockDigest),
		ServiceURL:            serviceURL,
		SourceCommit:          strings.ToLower(sourceCommit),
		Version:               version,
		LLMModel:              llmModel,
		EmbedModel:            embedModel,
	}, nil
}

func readLMEMem0Preflight(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("Mem0 preflight path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Mem0 preflight: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > lmeMem0MaxPreflightSize {
		return nil, errors.New("Mem0 preflight must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Mem0 preflight: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened Mem0 preflight: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("Mem0 preflight changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, lmeMem0MaxPreflightSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Mem0 preflight: %w", err)
	}
	if len(data) > lmeMem0MaxPreflightSize {
		return nil, errors.New("Mem0 preflight exceeds the size limit")
	}
	return data, nil
}

func lmeMem0ReferenceDatePrompt(metadata map[string]any) (string, error) {
	raw, ok := metadata[lmeMem0MetadataObservationTime]
	if !ok {
		return "", errors.New("mem0_oss benchmark ingestion has no observation time metadata")
	}
	value, ok := raw.(string)
	if !ok {
		return "", errors.New("mem0_oss benchmark observation time metadata is not a string")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("parse mem0_oss benchmark observation time: %w", err)
	}
	referenceDate := observedAt.UTC().Format(time.DateOnly)
	return fmt.Sprintf(
		"The authoritative observation date for this memory extraction request is %s. "+
			"Resolve relative dates in the new messages against this date, and do not use "+
			"the server wall-clock date for temporal resolution.",
		referenceDate,
	), nil
}

func lmeMem0DeltaMessages(
	sess *session.Session,
) (time.Time, []lmeMem0OSSMessage, error) {
	if sess == nil {
		return time.Time{}, nil, nil
	}
	since, err := lmeSessionWatermark(sess)
	if err != nil {
		return time.Time{}, nil, err
	}
	var latest time.Time
	var messages []lmeMem0OSSMessage
	sess.EventMu.RLock()
	defer sess.EventMu.RUnlock()
	for _, evt := range sess.Events {
		if !since.IsZero() && !evt.Timestamp.After(since) {
			continue
		}
		if evt.Timestamp.After(latest) {
			latest = evt.Timestamp
		}
		if evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			msg := choice.Message
			if msg.Role != model.RoleUser && msg.Role != model.RoleAssistant {
				continue
			}
			if msg.ToolID != "" || len(msg.ToolCalls) > 0 {
				continue
			}
			content := lmeMem0MessageText(msg)
			if content == "" {
				continue
			}
			messages = append(messages, lmeMem0OSSMessage{
				Role:    msg.Role.String(),
				Content: content,
			})
		}
	}
	return latest, messages, nil
}

func lmeSessionWatermark(sess *session.Session) (time.Time, error) {
	if sess == nil {
		return time.Time{}, nil
	}
	raw, ok := sess.GetState(memory.SessionStateKeyAutoMemoryLastExtractAt)
	if !ok || len(raw) == 0 {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse mem0_oss ingestion watermark: %w", err)
	}
	return value, nil
}

func lmeMem0MessageText(msg model.Message) string {
	if content := strings.TrimSpace(msg.Content); content != "" {
		return content
	}
	parts := make([]string, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		if part.Type != model.ContentTypeText || part.Text == nil {
			continue
		}
		if text := strings.TrimSpace(*part.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func (e *lmeMem0OSSEvaluator) Name() string { return "mem0_oss" }

func (e *lmeMem0OSSEvaluator) Close() error {
	if e.mem == nil {
		return nil
	}
	return e.mem.Close()
}

func (e *lmeMem0OSSEvaluator) CostReport() *lmeCostReport {
	if e.cost == nil {
		return nil
	}
	if e.proxyLog == nil {
		e.cost.markPartial("mem0 proxy usage log not configured")
		return e.cost.snapshot()
	}
	if err := e.proxyLog.RecordSinceStart(e.cost, lmeLLMPhaseMemoryBuild, lmeEmbeddingPhaseMemoryBuild); err != nil {
		e.cost.markPartial(fmt.Sprintf("mem0 proxy usage log unavailable: %v", err))
	}
	return e.cost.snapshot()
}

func (e *lmeMem0OSSEvaluator) Evaluate(
	ctx context.Context,
	inst *dataset.LongMemEvalInstance,
) (result *lmeCaseResult, retErr error) {
	trace, err := e.trace.beginCase(inst.QuestionID)
	if err != nil {
		return nil, err
	}
	ctx = withLMECaseTrace(ctx, trace)
	defer func() {
		if result == nil && retErr != nil {
			result = newLMEFailedCase(inst, retErr)
		}
		if err := trace.finish(result, retErr); err != nil {
			result = nil
			retErr = errors.Join(retErr, fmt.Errorf("finalize LongMemEval trace: %w", err))
		}
	}()
	ctx = withLMECostTracker(ctx, e.cost)
	start := time.Now()
	userKey := memory.UserKey{AppName: lmeAppMem0, UserID: inst.QuestionID}
	if err := e.mem.ClearMemories(ctx, userKey); err != nil {
		trace.markPersistenceError(err)
		return nil, fmt.Errorf("clear mem0 memories: %w", err)
	}
	trace.setInitialSnapshot(nil)
	casePlan, err := loadLMEBuildCasePlan(e.cfg, inst.QuestionID)
	if err != nil {
		trace.markBuildError(err)
		return nil, fmt.Errorf("load immutable build plan: %w", err)
	}
	if err := executeLMEBuildCase(ctx, e.cfg, casePlan, lmeBuildExecutionOptions{
		AppName:                     lmeAppMem0,
		MemoryReader:                e.mem,
		Ingestor:                    e.ingestor,
		ExtractionUnavailableReason: "Mem0 OSS does not expose extraction operations through its public API",
		Metadata:                    lmeMem0BuildMetadata(inst.QuestionID),
	}); err != nil {
		trace.markBuildError(err)
		return nil, fmt.Errorf("execute immutable mem0 build plan: %w", err)
	}

	qa := &lmeMemoryQA{
		appName:  lmeAppMem0,
		judgeLLM: e.judgeLLM,
		qaLLM:    e.qaLLM,
		mem:      e.mem,
		cfg:      e.cfg,
	}
	result, retErr = qa.run(ctx, ctx, inst, start)
	return result, retErr
}

func lmeMem0BuildMetadata(caseID string) func(
	lmeBuildSessionPlan,
	lmeBuildPairPlan,
	lmeBuildChunkPlan,
) map[string]any {
	return func(
		sessionPlan lmeBuildSessionPlan,
		pair lmeBuildPairPlan,
		chunk lmeBuildChunkPlan,
	) map[string]any {
		return map[string]any{
			"longmemeval_case_id":           caseID,
			"longmemeval_session_id":        sessionPlan.SessionID,
			"longmemeval_runner_session_id": sessionPlan.SessionID,
			lmeMem0MetadataObservationTime:  sessionPlan.ObservationTime,
			"longmemeval_build_protocol":    lmeBuildProtocol,
			"longmemeval_runner_lifecycle":  lmeBuildRunnerLifecycle,
			"longmemeval_session_lifecycle": lmeBuildSessionLifecycle,
			"longmemeval_build_pair_id":     pair.PairID,
			"longmemeval_build_chunk_id":    chunk.ChunkID,
		}
	}
}

const (
	lmeMem0MemoryReadLimit = 1000
	lmeMem0ClearMaxPasses  = 100
)

func clearResidualLMEMem0Memories(
	ctx context.Context,
	mem memory.Service,
	userKey memory.UserKey,
) error {
	for pass := 0; pass < lmeMem0ClearMaxPasses; pass++ {
		entries, err := mem.ReadMemories(ctx, userKey, lmeMem0MemoryReadLimit)
		if err != nil {
			return fmt.Errorf("read residual memories: %w", err)
		}
		if len(entries) == 0 {
			return nil
		}
		for _, entry := range entries {
			if entry == nil || strings.TrimSpace(entry.ID) == "" {
				return fmt.Errorf("residual memory has empty id")
			}
			key := memory.Key{
				AppName:  userKey.AppName,
				UserID:   userKey.UserID,
				MemoryID: entry.ID,
			}
			if err := mem.DeleteMemory(ctx, key); err != nil {
				return fmt.Errorf("delete residual memory %s: %w", entry.ID, err)
			}
		}
	}
	return fmt.Errorf("residual memories remain after %d clear passes", lmeMem0ClearMaxPasses)
}
