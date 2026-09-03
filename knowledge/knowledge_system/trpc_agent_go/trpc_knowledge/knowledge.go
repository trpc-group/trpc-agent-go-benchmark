//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package main provides knowledge base and agent functionality for evaluation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	openaiopt "github.com/openai/openai-go/option"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder/openai"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source/file"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore/pgvector"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// searchModeKnowledge wraps a Knowledge instance and forces a specific search mode
// on every Search call. It is used only by isolated contextual-retrieval experiments.
type searchModeKnowledge struct {
	inner      knowledge.Knowledge
	searchMode int
	recorder   *agentSearchRecorder
}

func (w *searchModeKnowledge) Search(ctx context.Context, req *knowledge.SearchRequest) (*knowledge.SearchResult, error) {
	// Copy the request before applying the experiment-only override. This keeps
	// the caller-owned request unchanged while ensuring that the wrapped
	// knowledge implementation receives the configured mode.
	effectiveRequest := *req
	effectiveRequest.SearchMode = w.searchMode
	recordIndex := w.recorder.begin(&effectiveRequest)
	result, err := w.inner.Search(ctx, &effectiveRequest)
	w.recorder.complete(recordIndex, result, err)
	return result, err
}

type agentSearchRecorder struct {
	mu       sync.Mutex
	searches []AgentSearchTrace
}

func (r *agentSearchRecorder) begin(req *knowledge.SearchRequest) int {
	trace := AgentSearchTrace{
		Query: req.Query,
		Request: AgentSearchRequestTrace{
			MaxResults:    req.MaxResults,
			MinScore:      req.MinScore,
			SearchMode:    req.SearchMode,
			UserID:        req.UserID,
			SessionID:     req.SessionID,
			HistoryLength: len(req.History),
		},
	}
	if req.SearchFilter != nil {
		trace.Request.FilterDocumentIDs = append(
			[]string(nil),
			req.SearchFilter.DocumentIDs...,
		)
		trace.Request.FilterMetadata = cloneMetadata(req.SearchFilter.Metadata)
		trace.Request.HasFilterCondition = req.SearchFilter.FilterCondition != nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.searches = append(r.searches, trace)
	return len(r.searches) - 1
}

func (r *agentSearchRecorder) complete(index int, result *knowledge.SearchResult, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		// Keep the trace safe to serialize and avoid exposing connection details.
		r.searches[index].Error = "search_failed"
		return
	}
	if result == nil {
		return
	}
	for resultIndex, item := range result.Documents {
		if item == nil || item.Document == nil {
			continue
		}
		r.searches[index].Results = append(
			r.searches[index].Results,
			AgentSearchResultTrace{
				Rank:          resultIndex + 1,
				DocumentID:    item.Document.ID,
				ContentSHA256: digestText(item.Document.Content),
				Metadata:      cloneMetadata(item.Document.Metadata),
				Score:         item.Score,
			},
		)
	}
}

func (r *agentSearchRecorder) snapshot() []AgentSearchTrace {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AgentSearchTrace(nil), r.searches...)
}

// VectorStoreType defines the type of vector store.
type VectorStoreType string

// Vector store type constants.
const (
	VectorStoreInMemory VectorStoreType = "inmemory"
	VectorStorePGVector VectorStoreType = "pgvector"

	defaultEmbeddingModel  = "server:274214"
	benchmarkChunkSize     = 500
	benchmarkChunkOverlap  = 50
	benchmarkEmbeddingDims = 1024
)

// ServiceConfig holds all tunable parameters for the knowledge service.
type ServiceConfig struct {
	StoreType          VectorStoreType
	ModelName          string
	SearchMode         int
	HybridVectorWeight float64
	HybridTextWeight   float64
	PGTable            string // overrides PGVECTOR_TABLE env var if non-empty
	UseRRF             bool   // whether to use Reciprocal Rank Fusion instead of weighted fusion
	IndexVariant       string // legacy|baseline|contextual
	ChunkManifestPath  string // required by baseline/contextual variants
	ContextCachePath   string // required by the contextual variant
}

// KnowledgeService manages knowledge base operations.
type KnowledgeService struct {
	kb               *knowledge.BuiltinKnowledge
	vs               vectorstore.VectorStore
	emb              embedder.Embedder
	lock             sync.RWMutex
	config           *ServiceConfig
	storeType        VectorStoreType
	modelName        string
	embeddingModel   string
	searchMode       int // default search mode: 0=hybrid, 1=vector, 2=keyword, 3=filter
	experimentSource *experimentManifestSource
}

type embeddingClientConfig struct {
	model      string
	dimensions int
	apiKey     string
	baseURL    string
	headers    map[string]string
	experiment bool
}

// NewKnowledgeService creates a new KnowledgeService instance with default config.
// searchMode: 0=hybrid (default), 1=vector, 2=keyword, 3=filter.
func NewKnowledgeService(storeType VectorStoreType, modelName string, searchMode int) (*KnowledgeService, error) {
	return NewKnowledgeServiceWithConfig(&ServiceConfig{
		StoreType:          storeType,
		ModelName:          modelName,
		SearchMode:         searchMode,
		HybridVectorWeight: 0.99999,
		HybridTextWeight:   0.00001,
	})
}

// NewKnowledgeServiceWithConfig creates a new KnowledgeService with full configuration.
func NewKnowledgeServiceWithConfig(cfg *ServiceConfig) (*KnowledgeService, error) {
	if cfg.IndexVariant == "" {
		cfg.IndexVariant = indexVariantLegacy
	}
	if cfg.IndexVariant != indexVariantLegacy &&
		cfg.IndexVariant != indexVariantBaseline &&
		cfg.IndexVariant != indexVariantContextual {
		return nil, fmt.Errorf("unsupported index variant %q", cfg.IndexVariant)
	}
	svc := &KnowledgeService{
		config:     cfg,
		storeType:  cfg.StoreType,
		modelName:  cfg.ModelName,
		searchMode: cfg.SearchMode,
	}
	if cfg.IndexVariant != indexVariantLegacy {
		experimentSource, err := newExperimentManifestSource(
			cfg.IndexVariant,
			cfg.ChunkManifestPath,
			cfg.ContextCachePath,
		)
		if err != nil {
			return nil, fmt.Errorf("configure experiment source: %w", err)
		}
		svc.experimentSource = experimentSource
	}

	var err error
	svc.vs, err = svc.newVectorStoreByType(cfg.StoreType)
	if err != nil {
		return nil, fmt.Errorf("failed to create vector store: %w", err)
	}

	embeddingConfig := resolveEmbeddingClientConfig(svc.isExperimentLane())
	svc.embeddingModel = embeddingConfig.model
	svc.emb = newEmbeddingClient(embeddingConfig)
	svc.kb = knowledge.New(
		knowledge.WithVectorStore(svc.vs),
		knowledge.WithEmbedder(svc.emb),
	)

	return svc, nil
}

func resolveEmbeddingClientConfig(experiment bool) embeddingClientConfig {
	if !experiment {
		return embeddingClientConfig{
			model:      defaultEmbeddingModel,
			dimensions: benchmarkEmbeddingDims,
			apiKey:     os.Getenv("OPENAI_API_KEY"),
			baseURL:    os.Getenv("OPENAI_BASE_URL"),
		}
	}

	apiKey := os.Getenv("EMBEDDING_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	baseURL := os.Getenv("EMBEDDING_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("OPENAI_BASE_URL")
	}
	return embeddingClientConfig{
		model:      getEnvOrDefault("EMBEDDING_MODEL", defaultEmbeddingModel),
		dimensions: benchmarkEmbeddingDims,
		apiKey:     apiKey,
		baseURL:    baseURL,
		headers:    gatewayHeaders("EMBEDDING"),
		experiment: true,
	}
}

func newEmbeddingClient(cfg embeddingClientConfig) embedder.Embedder {
	// Preserve the historical constructor exactly for the legacy lane, including
	// passing empty OPENAI_* values through to the original options.
	if !cfg.experiment {
		return openai.New(
			openai.WithModel(cfg.model),
			openai.WithDimensions(cfg.dimensions),
			openai.WithAPIKey(cfg.apiKey),
			openai.WithBaseURL(cfg.baseURL),
		)
	}

	options := []openai.Option{
		openai.WithModel(cfg.model),
		openai.WithDimensions(cfg.dimensions),
	}
	if cfg.apiKey != "" {
		options = append(options, openai.WithAPIKey(cfg.apiKey))
	}
	if cfg.baseURL != "" {
		options = append(options, openai.WithBaseURL(cfg.baseURL))
	}
	if len(cfg.headers) > 0 {
		requestOptions := make([]openaiopt.RequestOption, 0, len(cfg.headers))
		for key, value := range cfg.headers {
			requestOptions = append(requestOptions, openaiopt.WithHeader(key, value))
		}
		options = append(options, openai.WithRequestOptions(requestOptions...))
	}
	return openai.New(options...)
}

func (s *KnowledgeService) newVectorStoreByType(storeType VectorStoreType) (vectorstore.VectorStore, error) {
	switch storeType {
	case VectorStorePGVector:
		return s.newPGVectorStore()
	case VectorStoreInMemory:
		fallthrough
	default:
		return inmemory.New(), nil
	}
}

func (s *KnowledgeService) newPGVectorStore() (vectorstore.VectorStore, error) {
	host := getEnvOrDefault("PGVECTOR_HOST", "127.0.0.1")
	portStr := getEnvOrDefault("PGVECTOR_PORT", "5432")
	port, _ := strconv.Atoi(portStr)
	user := getEnvOrDefault("PGVECTOR_USER", "root")
	password := getEnvOrDefault("PGVECTOR_PASSWORD", "123")
	database := getEnvOrDefault("PGVECTOR_DATABASE", "rgb")
	table := getEnvOrDefault("PGVECTOR_TABLE", "trpc_agent_go_eval")

	// Command-line --pg-table overrides env var
	if s.config != nil && s.config.PGTable != "" {
		table = s.config.PGTable
	}

	vectorWeight := 0.99999
	textWeight := 0.00001
	if s.config != nil {
		vw := s.config.HybridVectorWeight
		tw := s.config.HybridTextWeight
		if vw < 0 || vw > 1 || tw < 0 || tw > 1 {
			return nil, fmt.Errorf("hybrid weights must be in [0, 1], got vector=%.5f text=%.5f", vw, tw)
		}
		vectorWeight = vw
		textWeight = tw
	}

	encodedUser := url.QueryEscape(user)
	encodedPassword := url.QueryEscape(password)
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		encodedUser, encodedPassword, host, port, database)

	opts := []pgvector.Option{
		pgvector.WithPGVectorClientDSN(dsn),
		pgvector.WithTable(table),
		pgvector.WithIndexDimension(1024),
	}

	if s.config != nil && s.config.UseRRF {
		// Use Reciprocal Rank Fusion instead of weighted score fusion
		opts = append(opts,
			pgvector.WithHybridFusionMode(pgvector.HybridFusionRRF),
			pgvector.WithRRFParams(&pgvector.RRFParams{
				K:              60,
				CandidateRatio: 3,
			}),
		)
	} else {
		// Default weighted score fusion
		opts = append(opts, pgvector.WithHybridSearchWeights(vectorWeight, textWeight))
	}

	return pgvector.New(opts...)
}

// Load loads documents from file paths into the knowledge base.
func (s *KnowledgeService) Load(ctx context.Context, filePaths []string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	var src source.Source
	if s.experimentSource != nil {
		src = s.experimentSource
	} else {
		// Create file source from paths with chunk size=500, overlap=50 (same as LangChain)
		src = file.New(
			filePaths,
			file.WithChunkSize(benchmarkChunkSize),
			file.WithChunkOverlap(benchmarkChunkOverlap),
		)
	}

	// Recreate knowledge base with new source
	s.kb = knowledge.New(
		knowledge.WithVectorStore(s.vs),
		knowledge.WithEmbedder(s.emb),
		knowledge.WithSources([]source.Source{src}),
		knowledge.WithEnableSourceSync(true),
	)

	// Load documents
	if s.isExperimentLane() {
		log.Infof("[Load] Starting document loading for variant %s (%d input document(s))...",
			s.config.IndexVariant, s.ExpectedLoadCount(len(filePaths)))
	} else {
		log.Infof("[Load] Starting document loading from %d file(s)...", len(filePaths))
	}
	if err := s.kb.Load(ctx, knowledge.WithShowProgress(true), knowledge.WithDocConcurrency(30)); err != nil {
		return fmt.Errorf("failed to load documents: %w", err)
	}

	// Verify loading by checking final count
	finalCount, err := s.vs.Count(ctx)
	if err != nil {
		log.Warnf("[Load] Failed to verify final vector store count: %v", err)
	} else {
		log.Infof("[Load] Vector store count after loading: %d documents", finalCount)
	}

	return nil
}

// ExpectedLoadCount returns the number of source documents/chunks represented by a load request.
func (s *KnowledgeService) ExpectedLoadCount(fileCount int) int {
	if s.experimentSource != nil {
		return s.experimentSource.manifest.ChunksCount
	}
	return fileCount
}

// DocumentCount returns the number of indexed chunks in the active vector store.
func (s *KnowledgeService) DocumentCount(ctx context.Context) (int, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.vs.Count(ctx)
}

// DocumentResult represents a single document result with metadata and score.
type DocumentResult struct {
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Score    float64        `json:"score"`
}

// Search searches for relevant documents.
func (s *KnowledgeService) Search(ctx context.Context, query string, k int) ([]*DocumentResult, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	result, err := s.kb.Search(ctx, &knowledge.SearchRequest{
		Query:      query,
		MaxResults: k,
		SearchMode: s.searchMode,
	})
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	var documents []*DocumentResult
	for i, doc := range result.Documents {
		denseScore, hasDenseScore := metadataScore(doc.Document.Metadata, source.MetadataDenseScore)
		sparseScore, hasSparseScore := metadataScore(doc.Document.Metadata, source.MetadataSparseScore)
		log.Infof(
			"[Search] [%d/%d] score=%.4f dense=%s sparse=%s text=%s",
			i+1,
			len(result.Documents),
			doc.Score,
			formatMetadataScore(denseScore, hasDenseScore),
			formatMetadataScore(sparseScore, hasSparseScore),
			truncateForLog(doc.Document.Content, 200),
		)
		documents = append(documents, &DocumentResult{
			Text:     doc.Document.Content,
			Score:    doc.Score,
			Metadata: doc.Document.Metadata,
		})
	}

	return documents, nil
}

// AgentTrace captures intermediate reasoning and tool interactions.
type AgentTrace struct {
	ToolCalls     []ToolCallTrace     `json:"tool_calls,omitempty"`
	ToolResponses []ToolResponseTrace `json:"tool_responses,omitempty"`
	Reasoning     []string            `json:"reasoning,omitempty"`
	Searches      []AgentSearchTrace  `json:"searches,omitempty"`
}

// AgentSearchTrace captures one effective knowledge search made by an Agent.
// It is populated only for the isolated baseline/contextual experiment lanes.
type AgentSearchTrace struct {
	Query   string                   `json:"query"`
	Request AgentSearchRequestTrace  `json:"request"`
	Results []AgentSearchResultTrace `json:"results,omitempty"`
	Error   string                   `json:"error,omitempty"`
}

// AgentSearchRequestTrace captures the effective, non-content request fields.
type AgentSearchRequestTrace struct {
	MaxResults         int            `json:"max_results"`
	MinScore           float64        `json:"min_score"`
	SearchMode         int            `json:"search_mode"`
	UserID             string         `json:"user_id,omitempty"`
	SessionID          string         `json:"session_id,omitempty"`
	HistoryLength      int            `json:"history_length"`
	FilterDocumentIDs  []string       `json:"filter_document_ids,omitempty"`
	FilterMetadata     map[string]any `json:"filter_metadata,omitempty"`
	HasFilterCondition bool           `json:"has_filter_condition"`
}

// AgentSearchResultTrace captures the identity and score of one returned chunk.
type AgentSearchResultTrace struct {
	Rank          int            `json:"rank"`
	DocumentID    string         `json:"document_id"`
	ContentSHA256 string         `json:"content_sha256"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Score         float64        `json:"score"`
}

// ToolCallTrace captures a tool call request.
type ToolCallTrace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResponseTrace captures a tool response.
type ToolResponseTrace struct {
	ToolID  string `json:"tool_id"`
	Content string `json:"content"`
}

// AnswerResult contains the final answer and trace information.
type AnswerResult struct {
	Answer   string      `json:"answer"`
	Trace    *AgentTrace `json:"trace,omitempty"`
	Contexts []string    `json:"contexts,omitempty"`
}

// Answer answers a question using RAG with a fresh session.
// Returns answer, documents (contexts from tool responses), trace, and error.
func (s *KnowledgeService) Answer(ctx context.Context, question string, k int) (string, []*DocumentResult, *AgentTrace, error) {
	result, err := s.runAgent(ctx, question, k)
	if err != nil {
		return "", nil, nil, err
	}

	// Convert contexts from trace to DocumentResult.
	// Use make() to ensure an empty slice (JSON []) instead of nil (JSON null),
	// preventing Python-side "'NoneType' object is not iterable" errors.
	documents := make([]*DocumentResult, 0, len(result.Contexts))
	for _, c := range result.Contexts {
		documents = append(documents, &DocumentResult{
			Text: c,
		})
	}

	return result.Answer, documents, result.Trace, nil
}

// Tool description matching LangChain's search_knowledge_base tool

func (s *KnowledgeService) runAgent(ctx context.Context, question string, k int) (*AnswerResult, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	// Create search tool with description matching LangChain.
	// Preserve the legacy benchmark behavior. Only isolated contextual-retrieval
	// experiment variants enforce and record the configured Agent search mode.
	var agentKnowledge knowledge.Knowledge = s.kb
	var searchRecorder *agentSearchRecorder
	if s.agentSearchModeEnforced() {
		searchRecorder = &agentSearchRecorder{}
		agentKnowledge = &searchModeKnowledge{
			inner:      s.kb,
			searchMode: s.searchMode,
			recorder:   searchRecorder,
		}
	}

	toolDescription := "this is a search tool that help search information you need. It's your knowledgebase, you search information by the tool to answer user's question."
	searchTool := knowledgetool.NewKnowledgeSearchTool(
		agentKnowledge,
		knowledgetool.WithMaxResults(k),
		knowledgetool.WithToolDescription(toolDescription),
	)

	// Temperature = 0 to match LangChain configuration
	temperature := float64(0)
	genConfig := model.GenerationConfig{
		Temperature: &temperature,
	}

	llmOptions := make([]openaimodel.Option, 0, 1)
	if headers := s.llmGatewayHeaders(); len(headers) > 0 {
		llmOptions = append(llmOptions, openaimodel.WithHeaders(headers))
	}

	agentOptions := []llmagent.Option{
		llmagent.WithModel(openaimodel.New(s.modelName, llmOptions...)),
		llmagent.WithTools([]tool.Tool{searchTool}),
		llmagent.WithInstruction(
			"You are a helpful assistant that answers questions using a knowledge base search tool.\n\n" +
				"CRITICAL RULES(IMPORTANT !!!):\n" +
				"1. You MUST call the search tool AT LEAST ONCE before answering. NEVER answer without searching first.\n" +
				"2. Answer ONLY using information retrieved from the search tool.\n" +
				"3. Do NOT add external knowledge, explanations, or context not found in the retrieved documents.\n" +
				"4. Do NOT provide additional details, synonyms, or interpretations beyond what is explicitly stated in the search results.\n" +
				"5. Use the search tool at most 3 times. If you haven't found the answer after 3 searches, provide the best answer from what you found.\n" +
				"6. Be concise and stick strictly to the facts from the retrieved information.\n" +
				"7. Give only the direct answer.",
		),
		llmagent.WithGenerationConfig(genConfig),
	}
	if s.isExperimentLane() {
		toolCallbacks := tool.NewCallbacks()
		toolCallbacks.RegisterBeforeTool(newQueryArgumentGuard().beforeTool)
		agentOptions = append(
			agentOptions,
			llmagent.WithToolCallbacks(toolCallbacks),
		)
	}
	agent := llmagent.New("evaluation-assistant", agentOptions...)

	sessionService := sessioninmemory.NewSessionService()

	r := runner.NewRunner(
		"eval-runner",
		agent,
		runner.WithSessionService(sessionService),
	)
	defer r.Close()

	eventChan, err := r.Run(ctx, "eval-user", "fresh-session", model.NewUserMessage(question))
	if err != nil {
		return nil, fmt.Errorf("runner failed: %w", err)
	}

	result := &AnswerResult{
		Trace: &AgentTrace{},
	}

	var (
		contentBuilder     strings.Builder // Accumulates streaming content
		hasToolCalls       bool            // Whether any tool calls have been made
		processedToolIDs   = make(map[string]bool)
		lastContentWasTool bool // Track if last processed event was tool-related
		reasoningStepCount int  // Counter for reasoning steps
	)

	log.Infof("[Agent] ========== Processing question: %s ==========", question)

	for evt := range eventChan {
		if evt.Error != nil {
			log.Errorf("[Agent] Event error: %v", evt.Error)
			return nil, fmt.Errorf("event error: %v", evt.Error)
		}

		if evt.Response == nil || len(evt.Response.Choices) == 0 {
			continue
		}

		// Handle tool calls - flush any accumulated content as reasoning first
		if s.isToolCallEvent(evt) {
			// First, extract any content from THIS event before processing tool calls
			// (some LLMs may include reasoning content in the same event as tool calls)
			if content := s.extractContent(evt); content != "" {
				contentBuilder.WriteString(content)
			}

			// Flush all accumulated content as reasoning before tool call
			if contentBuilder.Len() > 0 {
				reasoning := strings.TrimSpace(contentBuilder.String())
				if reasoning != "" {
					reasoningStepCount++
					result.Trace.Reasoning = append(result.Trace.Reasoning, reasoning)
					log.Infof("[Agent] [Reasoning Step %d - Before Tool Call]: %s", reasoningStepCount, truncateForLog(reasoning, 300))
				}
				contentBuilder.Reset()
			}

			s.captureToolCalls(evt, result.Trace)
			hasToolCalls = true
			lastContentWasTool = true
			continue
		}

		// Handle tool responses and extract contexts
		if s.isToolResponseEvent(evt) {
			s.captureToolResponses(evt, result.Trace, processedToolIDs, result)
			lastContentWasTool = true
			continue
		}

		// Handle content (reasoning or final answer)
		content := s.extractContent(evt)
		if content != "" {
			// If we just came from a tool response, log that we're receiving post-tool content
			if lastContentWasTool {
				log.Debugf("[Agent] Receiving content after tool response...")
				lastContentWasTool = false
			}
			contentBuilder.WriteString(content)
		}

		if evt.IsFinalResponse() {
			// Everything accumulated is the final answer
			finalContent := strings.TrimSpace(contentBuilder.String())
			if finalContent == "" && len(evt.Response.Choices) > 0 {
				finalContent = evt.Response.Choices[0].Message.Content
			}

			// If we had tool calls, the final content is the answer based on tool results
			// If no tool calls, it might be reasoning + answer mixed
			if hasToolCalls {
				result.Answer = finalContent
				log.Infof("[Agent] [Final Answer (after tool calls)]: %s", truncateForLog(finalContent, 300))
			} else {
				// No tool calls - content is direct answer (possibly with reasoning)
				result.Answer = finalContent
				log.Infof("[Agent] [Final Answer (direct)]: %s", truncateForLog(finalContent, 300))
			}

			log.Infof("[Agent] ========== Trace Summary ==========")
			log.Infof("[Agent] Tool calls: %d", len(result.Trace.ToolCalls))
			for i, tc := range result.Trace.ToolCalls {
				log.Infof("[Agent]   [%d] %s (ID: %s) Args: %s", i+1, tc.Name, tc.ID, truncateForLog(tc.Arguments, 100))
			}
			log.Infof("[Agent] Tool responses: %d", len(result.Trace.ToolResponses))
			for i, tr := range result.Trace.ToolResponses {
				log.Infof("[Agent]   [%d] ID: %s Content: %s", i+1, tr.ToolID, truncateForLog(tr.Content, 100))
			}
			log.Infof("[Agent] Reasoning steps: %d", len(result.Trace.Reasoning))
			for i, r := range result.Trace.Reasoning {
				log.Infof("[Agent]   [%d]: %s", i+1, truncateForLog(r, 200))
			}
			log.Infof("[Agent] Contexts extracted: %d", len(result.Contexts))
			log.Infof("[Agent] ====================================")
			break
		}
	}

	if searchRecorder != nil {
		result.Trace.Searches = searchRecorder.snapshot()
	}
	log.Infof("[Agent] Final answer: %s", result.Answer)

	return result, nil
}

func (s *KnowledgeService) agentSearchModeEnforced() bool {
	return s.isExperimentLane()
}

func (s *KnowledgeService) isExperimentLane() bool {
	if s == nil || s.config == nil {
		return false
	}
	return s.config.IndexVariant == indexVariantBaseline ||
		s.config.IndexVariant == indexVariantContextual
}

func (s *KnowledgeService) llmGatewayHeaders() map[string]string {
	if !s.isExperimentLane() {
		return nil
	}
	return gatewayHeaders("LLM")
}

// isToolCallEvent checks if the event contains tool call requests.
func (s *KnowledgeService) isToolCallEvent(evt *event.Event) bool {
	if evt.Response == nil || len(evt.Response.Choices) == 0 {
		return false
	}
	choice := evt.Response.Choices[0]
	return len(choice.Message.ToolCalls) > 0 || len(choice.Delta.ToolCalls) > 0
}

// isToolResponseEvent checks if the event contains tool response.
func (s *KnowledgeService) isToolResponseEvent(evt *event.Event) bool {
	if evt.Response == nil || len(evt.Response.Choices) == 0 {
		return false
	}
	for _, choice := range evt.Response.Choices {
		if choice.Message.Role == model.RoleTool || choice.Message.ToolID != "" {
			return true
		}
	}
	return false
}

// captureToolCalls captures tool call information from the event.
func (s *KnowledgeService) captureToolCalls(evt *event.Event, trace *AgentTrace) {
	if evt.Response == nil || len(evt.Response.Choices) == 0 {
		return
	}

	choice := evt.Response.Choices[0]
	toolCalls := choice.Message.ToolCalls
	if len(toolCalls) == 0 {
		toolCalls = choice.Delta.ToolCalls
	}

	for _, toolCall := range toolCalls {
		tc := ToolCallTrace{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: string(toolCall.Function.Arguments),
		}
		trace.ToolCalls = append(trace.ToolCalls, tc)
		log.Infof("[Agent] [Tool Call] %s (ID: %s) Args: %s", tc.Name, tc.ID, truncateForLog(tc.Arguments, 200))
	}
}

// knowledgeSearchResponse matches the format returned by KnowledgeSearchTool.
type knowledgeSearchResponse struct {
	Documents []struct {
		Text     string         `json:"text"`
		Score    float64        `json:"score"`
		Metadata map[string]any `json:"metadata,omitempty"`
	} `json:"documents"`
	Message string `json:"message,omitempty"`
}

// captureToolResponses captures tool response information from the event.
func (s *KnowledgeService) captureToolResponses(evt *event.Event, trace *AgentTrace, processedToolIDs map[string]bool, result *AnswerResult) {
	if evt.Response == nil || len(evt.Response.Choices) == 0 {
		return
	}

	for _, choice := range evt.Response.Choices {
		if choice.Message.Role == model.RoleTool && choice.Message.ToolID != "" {
			if processedToolIDs[choice.Message.ToolID] {
				continue
			}
			processedToolIDs[choice.Message.ToolID] = true

			content := strings.TrimSpace(choice.Message.Content)
			tr := ToolResponseTrace{
				ToolID:  choice.Message.ToolID,
				Content: content,
			}
			trace.ToolResponses = append(trace.ToolResponses, tr)

			// Try to parse as KnowledgeSearchResponse to extract individual document texts
			var searchResp knowledgeSearchResponse
			if err := json.Unmarshal([]byte(content), &searchResp); err == nil && len(searchResp.Documents) > 0 {
				// Successfully parsed - extract each document's text as a separate context
				for _, doc := range searchResp.Documents {
					if doc.Text != "" {
						result.Contexts = append(result.Contexts, doc.Text)
						denseScore, hasDenseScore := metadataScore(doc.Metadata, source.MetadataDenseScore)
						sparseScore, hasSparseScore := metadataScore(doc.Metadata, source.MetadataSparseScore)
						log.Infof(
							"[Agent] [Context Extracted] (score=%.4f dense=%s sparse=%s): %s",
							doc.Score,
							formatMetadataScore(denseScore, hasDenseScore),
							formatMetadataScore(sparseScore, hasSparseScore),
							truncateForLog(doc.Text, 200),
						)
					}
				}
			} else if s.isExperimentLane() && isToolArgumentValidationResponse(content) {
				// Argument validation feedback is model-visible control data, not
				// retrieved evidence. Keep it in the tool trace without polluting
				// the top-level contexts consumed by the evaluator.
				log.Infof("[Agent] [Tool Argument Validation] (ID: %s)", tr.ToolID)
			} else if s.isExperimentLane() {
				// Dispatcher/runtime errors and other non-search responses are
				// control data, not retrieved evidence. Preserve the raw response
				// in the trace, but never expose it as an evaluator context.
				log.Warnf("[Agent] [Tool Response] Could not parse as KnowledgeSearchResponse; excluded from contexts")
			} else {
				// Preserve the historical legacy behavior: non-search tool responses
				// are exposed as raw contexts.
				result.Contexts = append(result.Contexts, content)
				log.Warnf("[Agent] [Tool Response] Could not parse as KnowledgeSearchResponse, using raw content")
			}

			log.Infof("[Agent] [Tool Response] (ID: %s): %s", tr.ToolID, truncateForLog(content, 300))
		}
	}
}

// extractContent extracts content from an event (handles both streaming delta and full message).
func (s *KnowledgeService) extractContent(evt *event.Event) string {
	if evt.Response == nil || len(evt.Response.Choices) == 0 {
		return ""
	}

	choice := evt.Response.Choices[0]

	// Skip tool role messages
	if choice.Message.Role == model.RoleTool {
		return ""
	}

	// Extract content from delta (streaming) or message
	content := choice.Delta.Content
	if content == "" {
		content = choice.Message.Content
	}

	return content
}

func truncateForLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func metadataScore(metadata map[string]any, key string) (float64, bool) {
	if metadata == nil {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func formatMetadataScore(score float64, hasScore bool) string {
	if !hasScore {
		return "N/A"
	}
	return fmt.Sprintf("%.4f", score)
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func endpointIdentity(rawValue string) string {
	value := strings.TrimSpace(rawValue)
	if value == "" {
		return ""
	}
	lowerValue := strings.ToLower(value)
	hasScheme := strings.HasPrefix(lowerValue, "http://") ||
		strings.HasPrefix(lowerValue, "https://")
	if strings.Contains(value, "://") && !hasScheme {
		return "invalid_endpoint"
	}
	parseValue := value
	if !hasScheme {
		parseValue = "https://" + value
	}
	parsed, err := url.Parse(parseValue)
	if err != nil || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "invalid_endpoint"
	}
	host := parsed.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	identity := host
	if hasScheme {
		identity = parsed.Scheme + "://" + host
	}
	if path := parsed.EscapedPath(); path != "" && path != "/" {
		identity += "|path_sha256=" + digestText(path)
	}
	return identity
}

func gatewayHeaders(prefix string) map[string]string {
	headers := make(map[string]string)
	if value := os.Getenv(prefix + "_SMG_ROUTING_KEY"); value != "" {
		headers["X-SMG-Routing-Key"] = value
	}
	if value := os.Getenv(prefix + "_SMG_AGENT_NAME"); value != "" {
		headers["X-SMG-Agent-Name"] = value
	}
	return headers
}

func gatewayHeaderNames(prefix string) []string {
	headers := gatewayHeaders(prefix)
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
