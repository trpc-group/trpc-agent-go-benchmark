//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package main provides HTTP service for knowledge base evaluation.
// This service implements the KnowledgeBase interface (load, search, answer)
// and exposes them as HTTP endpoints for Python RAGAS evaluation.
//
// Required environment variables:
//   - OPENAI_API_KEY: Your OpenAI API key for LLM and embeddings
//   - OPENAI_BASE_URL: (Optional) Custom OpenAI API endpoint
//   - MODEL_NAME: (Optional) Model name to use, defaults to deepseek-v3.2
//   - PGVECTOR_HOST, PGVECTOR_PORT, PGVECTOR_USER, PGVECTOR_PASSWORD, PGVECTOR_DATABASE: PGVector config
//
// Example usage:
//
//	export OPENAI_API_KEY=sk-xxxx
//	go run main.go --port 8080
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

var (
	port           = flag.Int("port", 8765, "HTTP server port")
	vectorStoreArg = flag.String("vectorstore", "pgvector", "Vector store type: inmemory|pgvector")
	searchModeArg  = flag.Int("search-mode", 0, "Search mode: 0=hybrid (default), 1=vector, 2=keyword, 3=filter")
	modelName      = getEnvOrDefault("MODEL_NAME", "deepseek-v3.2")

	// Tunable parameters for vertical evaluation
	hybridVectorWeight = flag.Float64("hybrid-vector-weight", 0.99999, "Hybrid search vector weight (0.0-1.0)")
	hybridTextWeight   = flag.Float64("hybrid-text-weight", 0.00001, "Hybrid search text weight (0.0-1.0)")
	pgTable            = flag.String("pg-table", "", "PGVector table name (overrides PGVECTOR_TABLE env var)")
	useRRF             = flag.Bool("use-rrf", false, "Use Reciprocal Rank Fusion instead of weighted score fusion")
	parentManifestArg  = flag.String("parent-manifest", "", "Parent manifest used to prepare a stable chunk manifest")
	writeChunksArg     = flag.String("write-chunk-manifest", "", "Write a chunk manifest and exit")
	indexVariantArg    = flag.String("index-variant", indexVariantLegacy, "Index variant: legacy|baseline|contextual")
	chunkManifestArg   = flag.String("chunk-manifest", "", "Stable chunk manifest for baseline/contextual indexing")
	contextCacheArg    = flag.String("context-cache", "", "Context JSONL cache for contextual indexing")
)

// Global knowledge service
var knowledgeSvc *KnowledgeService

const frameworkModulePath = "trpc.group/trpc-go/trpc-agent-go"

// LoadRequest represents the request body for /load endpoint.
type LoadRequest struct {
	FilePaths []string `json:"file_paths"`
}

// LoadResponse represents the response for /load endpoint.
type LoadResponse struct {
	Success   bool    `json:"success"`
	Message   string  `json:"message"`
	Count     int     `json:"count"`
	ElapsedMS float64 `json:"elapsed_ms,omitempty"`
}

// SearchRequest represents the request body for /search endpoint.
type SearchRequest struct {
	Query string `json:"query"`
	K     int    `json:"k"`
}

// SearchResponse represents the response for /search endpoint.
type SearchResponse struct {
	Documents []*DocumentResult `json:"documents"`
	Message   string            `json:"message,omitempty"`
}

// AnswerRequest represents the request body for /answer endpoint.
type AnswerRequest struct {
	Question string `json:"question"`
	K        int    `json:"k"`
}

// AnswerResponse represents the response for /answer endpoint.
type AnswerResponse struct {
	Answer    string            `json:"answer"`
	Documents []*DocumentResult `json:"documents"`
	Trace     *AgentTrace       `json:"trace,omitempty"`
	Message   string            `json:"message,omitempty"`
}

// AnswerErrorResponse exposes only a stable, non-sensitive error category.
type AnswerErrorResponse struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func main() {
	flag.Parse()
	if *writeChunksArg != "" {
		if *parentManifestArg == "" {
			log.Fatalf("--parent-manifest is required with --write-chunk-manifest")
		}
		manifest, err := writeChunkManifest(*parentManifestArg, *writeChunksArg)
		if err != nil {
			log.Fatalf("Failed to prepare chunk manifest: %v", err)
		}
		fmt.Printf(
			"Wrote %d stable chunks from %d parents to %s (digest=%s)\n",
			manifest.ChunksCount,
			manifest.ParentsCount,
			*writeChunksArg,
			manifest.ArtifactDigest,
		)
		return
	}

	searchModeNames := map[int]string{0: "hybrid", 1: "vector", 2: "keyword", 3: "filter"}
	fmt.Println("Knowledge Base HTTP Service")
	fmt.Printf("Model: %s\n", modelName)
	fmt.Printf("Vector Store: %s\n", *vectorStoreArg)
	fmt.Printf("Search Mode: %s (%d)\n", searchModeNames[*searchModeArg], *searchModeArg)
	fmt.Printf("Use RRF: %v\n", *useRRF)
	if !*useRRF {
		fmt.Printf("Hybrid Weights: vector=%.5f text=%.5f\n", *hybridVectorWeight, *hybridTextWeight)
	}
	if *pgTable != "" {
		fmt.Printf("PG Table: %s (override)\n", *pgTable)
	}
	if *indexVariantArg != indexVariantLegacy {
		fmt.Printf("Index Variant: %s\n", *indexVariantArg)
	}
	fmt.Printf("PG Host: %s:%s\n", getEnvOrDefault("PGVECTOR_HOST", "127.0.0.1"), getEnvOrDefault("PGVECTOR_PORT", "5432"))
	fmt.Printf("PG Database: %s (User: %s)\n", getEnvOrDefault("PGVECTOR_DATABASE", "rgb"), getEnvOrDefault("PGVECTOR_USER", "root"))
	apiKeyStatus := "(not set)"
	if os.Getenv("OPENAI_API_KEY") != "" {
		apiKeyStatus = "(set)"
	}
	fmt.Printf("OPENAI_API_KEY: %s\n", apiKeyStatus)
	fmt.Printf(
		"OPENAI_BASE_URL: %s\n",
		endpointIdentity(os.Getenv("OPENAI_BASE_URL")),
	)
	fmt.Println(strings.Repeat("=", 50))

	svcConfig := &ServiceConfig{
		StoreType:          VectorStoreType(*vectorStoreArg),
		ModelName:          modelName,
		SearchMode:         *searchModeArg,
		HybridVectorWeight: *hybridVectorWeight,
		HybridTextWeight:   *hybridTextWeight,
		PGTable:            *pgTable,
		UseRRF:             *useRRF,
		IndexVariant:       *indexVariantArg,
		ChunkManifestPath:  *chunkManifestArg,
		ContextCachePath:   *contextCacheArg,
	}

	var err error
	knowledgeSvc, err = NewKnowledgeServiceWithConfig(svcConfig)
	if err != nil {
		log.Fatalf("Failed to initialize knowledge service: %v", err)
	}

	http.HandleFunc("/load", handleLoad)
	http.HandleFunc("/search", handleSearch)
	http.HandleFunc("/answer", handleAnswer)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/config", handleConfig)

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Server listening on http://localhost%s\n", addr)
	fmt.Println("\nEndpoints:")
	fmt.Println("  POST /load   - Load documents into knowledge base")
	fmt.Println("  POST /search - Search for relevant documents")
	fmt.Println("  POST /answer - Answer a question using RAG")
	fmt.Println("  GET  /health - Health check")
	fmt.Println("  GET  /config - Current service configuration")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func waitForIndexRefresh() {
	time.Sleep(100 * time.Millisecond)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if knowledgeSvc.isExperimentLane() && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(buildRuntimeConfig(r.Context(), knowledgeSvc))
}

func buildRuntimeConfig(ctx context.Context, svc *KnowledgeService) map[string]any {
	// Collect PG connection info from environment (masking password)
	host := getEnvOrDefault("PGVECTOR_HOST", "127.0.0.1")
	portStr := getEnvOrDefault("PGVECTOR_PORT", "5432")
	user := getEnvOrDefault("PGVECTOR_USER", "root")
	database := getEnvOrDefault("PGVECTOR_DATABASE", "rgb")

	// Resolve the effective table name: command-line flag overrides env var.
	effectiveTable := getEnvOrDefault("PGVECTOR_TABLE", "trpc_agent_go_eval")
	if svc.config.PGTable != "" {
		effectiveTable = svc.config.PGTable
	}

	cfg := map[string]any{
		"model_name":           svc.modelName,
		"vectorstore":          string(svc.storeType),
		"search_mode":          svc.searchMode,
		"use_rrf":              svc.config.UseRRF,
		"hybrid_vector_weight": svc.config.HybridVectorWeight,
		"hybrid_text_weight":   svc.config.HybridTextWeight,
		"pg_table":             effectiveTable,
		"pg_connection": map[string]string{
			"host":     host,
			"port":     portStr,
			"user":     user,
			"database": database,
			// Password intentionally omitted for security
		},
	}
	if !svc.isExperimentLane() {
		return cfg
	}

	embeddingBaseURL := os.Getenv("EMBEDDING_BASE_URL")
	if embeddingBaseURL == "" {
		embeddingBaseURL = getEnvOrDefault(
			"OPENAI_BASE_URL",
			"https://api.openai.com/v1",
		)
	}
	cfg["embedding_model"] = svc.embeddingModel
	cfg["llm_endpoint"] = endpointIdentity(
		getEnvOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
	)
	cfg["embedding_endpoint"] = endpointIdentity(embeddingBaseURL)
	cfg["embedding_dimensions"] = benchmarkEmbeddingDims
	cfg["chunk_size"] = benchmarkChunkSize
	cfg["chunk_overlap"] = benchmarkChunkOverlap
	cfg["agent_search_mode_enforced"] = true
	cfg["agent_search_mode_effective"] = svc.searchMode
	cfg["tool_argument_policy"] = toolArgumentPolicy
	cfg["max_argument_repairs"] = toolArgumentMaxRepairs
	cfg["silent_argument_rewrite"] = false
	cfg["provider_strict"] = false
	cfg["index_variant"] = svc.config.IndexVariant
	cfg["llm_header_names"] = gatewayHeaderNames("LLM")
	cfg["embedding_header_names"] = gatewayHeaderNames("EMBEDDING")
	cfg["framework_module"] = frameworkModuleProvenance()

	if svc.experimentSource != nil {
		cfg["chunk_manifest_digest"] = svc.experimentSource.manifest.ArtifactDigest
		cfg["parent_manifest_digest"] = svc.experimentSource.manifest.ParentManifestDigest
		cfg["manifest_chunks_count"] = svc.experimentSource.manifest.ChunksCount
		cfg["context_cache_identity"] = svc.experimentSource.contextCacheIdentity
		if svc.config.IndexVariant == indexVariantContextual {
			cfg["context_set_digest"] = svc.experimentSource.contextSetDigest
		} else {
			cfg["context_set_digest"] = nil
		}
	} else {
		cfg["chunk_manifest_digest"] = nil
		cfg["parent_manifest_digest"] = nil
		cfg["manifest_chunks_count"] = nil
		cfg["context_cache_identity"] = nil
		cfg["context_set_digest"] = nil
	}
	count, err := svc.DocumentCount(ctx)
	if err != nil {
		cfg["index_document_count"] = nil
		// Do not serialize driver errors: they can contain connection details.
		cfg["index_document_count_error"] = "count_failed"
	} else {
		cfg["index_document_count"] = count
	}
	return cfg
}

func frameworkModuleProvenance() map[string]string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return map[string]string{
			"path":    frameworkModulePath,
			"version": "unknown",
			"source":  "unavailable",
		}
	}
	for _, dependency := range info.Deps {
		if dependency.Path == frameworkModulePath {
			return moduleProvenance(dependency)
		}
	}
	if info.Main.Path == frameworkModulePath {
		return moduleProvenance(&info.Main)
	}
	return map[string]string{
		"path":    frameworkModulePath,
		"version": "unknown",
		"source":  "unavailable",
	}
}

func moduleProvenance(module *debug.Module) map[string]string {
	version := module.Version
	checksum := module.Sum
	source := "module"
	provenance := map[string]string{
		"path": module.Path,
	}
	if module.Replace != nil {
		source = "replacement"
		if module.Replace.Version != "" {
			version = module.Replace.Version
		}
		if module.Replace.Sum != "" {
			checksum = module.Replace.Sum
		}
		if !filepath.IsAbs(module.Replace.Path) {
			provenance["replacement_path"] = module.Replace.Path
		} else {
			provenance["replacement_path"] = "local"
		}
	}
	if version == "" {
		version = "(devel)"
	}
	provenance["version"] = version
	provenance["source"] = source
	if checksum != "" {
		provenance["sum"] = checksum
	}
	return provenance
}

func handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if len(req.FilePaths) == 0 && knowledgeSvc.config.IndexVariant == indexVariantLegacy {
		http.Error(w, "No file paths provided", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	experiment := knowledgeSvc.isExperimentLane()
	var started time.Time
	if experiment {
		started = time.Now()
	}
	if err := knowledgeSvc.Load(ctx, req.FilePaths); err != nil {
		http.Error(w, fmt.Sprintf("Failed to load documents: %v", err), http.StatusInternalServerError)
		return
	}

	waitForIndexRefresh()

	w.Header().Set("Content-Type", "application/json")
	var elapsed time.Duration
	if experiment {
		elapsed = time.Since(started)
	}
	json.NewEncoder(w).Encode(buildLoadResponse(
		knowledgeSvc,
		len(req.FilePaths),
		elapsed,
	))
}

func buildLoadResponse(
	svc *KnowledgeService,
	fileCount int,
	elapsed time.Duration,
) LoadResponse {
	response := LoadResponse{
		Success: true,
		Message: "Documents loaded successfully",
		Count:   fileCount,
	}
	if svc.isExperimentLane() {
		response.Count = svc.ExpectedLoadCount(fileCount)
		response.ElapsedMS = float64(elapsed.Microseconds()) / 1000
	}
	return response
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.Query == "" {
		http.Error(w, "Query is required", http.StatusBadRequest)
		return
	}

	if req.K <= 0 {
		req.K = 4
	}

	ctx := context.Background()
	documents, err := knowledgeSvc.Search(ctx, req.Query, req.K)
	if err != nil {
		http.Error(w, fmt.Sprintf("Search failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SearchResponse{
		Documents: documents,
		Message:   fmt.Sprintf("Found %d relevant document(s)", len(documents)),
	})
}

func handleAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.Question == "" {
		http.Error(w, "Question is required", http.StatusBadRequest)
		return
	}

	if req.K <= 0 {
		req.K = 4
	}

	ctx := answerRequestContext(r, knowledgeSvc)
	answer, documents, trace, err := knowledgeSvc.Answer(ctx, req.Question, req.K)
	if err != nil {
		if knowledgeSvc.isExperimentLane() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(AnswerErrorResponse{
				ErrorType: answerErrorType(err),
				Message:   "Agent execution failed",
			})
			return
		}
		http.Error(w, fmt.Sprintf("Answer failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AnswerResponse{
		Answer:    answer,
		Documents: documents,
		Trace:     trace,
		Message:   fmt.Sprintf("Found %d relevant document(s)", len(documents)),
	})
}

func answerErrorType(err error) string {
	if err != nil && strings.Contains(err.Error(), toolArgumentRepairExhaustedType) {
		return toolArgumentRepairExhaustedType
	}
	return "agent_execution_error"
}

func answerRequestContext(r *http.Request, svc *KnowledgeService) context.Context {
	if svc != nil && svc.agentSearchModeEnforced() {
		return r.Context()
	}
	// Preserve the historical benchmark behavior for the legacy lane: client
	// cancellation does not cancel an in-flight Agent run.
	return context.Background()
}
