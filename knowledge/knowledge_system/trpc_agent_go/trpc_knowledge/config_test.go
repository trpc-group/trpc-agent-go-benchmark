//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore/inmemory"
)

type recordingKnowledge struct {
	request *knowledge.SearchRequest
	result  *knowledge.SearchResult
}

func (k *recordingKnowledge) Search(_ context.Context, req *knowledge.SearchRequest) (*knowledge.SearchResult, error) {
	requestCopy := *req
	k.request = &requestCopy
	return k.result, nil
}

func TestGatewayHeadersUseSeparatePrefixes(t *testing.T) {
	t.Setenv("LLM_SMG_ROUTING_KEY", "llm-route")
	t.Setenv("LLM_SMG_AGENT_NAME", "llm-agent")
	t.Setenv("EMBEDDING_SMG_ROUTING_KEY", "embedding-route")
	t.Setenv("EMBEDDING_SMG_AGENT_NAME", "embedding-agent")

	llmHeaders := gatewayHeaders("LLM")
	embeddingHeaders := gatewayHeaders("EMBEDDING")

	if got := llmHeaders["X-SMG-Routing-Key"]; got != "llm-route" {
		t.Fatalf("LLM routing header = %q, want llm-route", got)
	}
	if got := embeddingHeaders["X-SMG-Routing-Key"]; got != "embedding-route" {
		t.Fatalf("embedding routing header = %q, want embedding-route", got)
	}
	if got := llmHeaders["X-SMG-Agent-Name"]; got != "llm-agent" {
		t.Fatalf("LLM agent header = %q, want llm-agent", got)
	}
	if got := embeddingHeaders["X-SMG-Agent-Name"]; got != "embedding-agent" {
		t.Fatalf("embedding agent header = %q, want embedding-agent", got)
	}
}

func TestEndpointIdentityIsSecretFreeAndPathSensitive(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: ""},
		{
			name:  "https",
			value: "https://user:password@example.test:8443/secret/path?token=value#private",
			want: "https://example.test:8443|path_sha256=" +
				digestText("/secret/path"),
		},
		{
			name:  "schemeless",
			value: "user:password@embedding.test:9443/private/v1?token=value#private",
			want: "embedding.test:9443|path_sha256=" +
				digestText("/private/v1"),
		},
		{
			name:  "IPv6",
			value: "http://[2001:db8::1]:8080/v1",
			want: "http://[2001:db8::1]:8080|path_sha256=" +
				digestText("/v1"),
		},
		{name: "relative path", value: "/v1", want: "invalid_endpoint"},
		{name: "unix", value: "unix:///var/run/service.sock", want: "invalid_endpoint"},
		{name: "file", value: "file:///tmp/config", want: "invalid_endpoint"},
		{name: "invalid port", value: "https://example.test:bad/v1", want: "invalid_endpoint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := endpointIdentity(test.value)
			if got != test.want {
				t.Fatalf("endpointIdentity(%q) = %q, want %q", test.value, got, test.want)
			}
			for _, secret := range []string{"user", "password", "secret", "private", "token=value"} {
				if strings.Contains(got, secret) {
					t.Fatalf("endpoint identity leaked %q: %q", secret, got)
				}
			}
		})
	}
}

func TestResolveEmbeddingClientConfigIsolatesLegacy(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "legacy-key")
	t.Setenv("OPENAI_BASE_URL", "https://legacy.example/v1")
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("EMBEDDING_API_KEY", "embedding-key")
	t.Setenv("EMBEDDING_BASE_URL", "https://embedding.example/v1")
	t.Setenv("EMBEDDING_SMG_ROUTING_KEY", "embedding-route")
	t.Setenv("EMBEDDING_SMG_AGENT_NAME", "embedding-agent")

	legacy := resolveEmbeddingClientConfig(false)
	if legacy.model != defaultEmbeddingModel ||
		legacy.dimensions != benchmarkEmbeddingDims ||
		legacy.apiKey != "legacy-key" ||
		legacy.baseURL != "https://legacy.example/v1" ||
		legacy.experiment || len(legacy.headers) != 0 {
		t.Fatalf("legacy embedding config = %#v", legacy)
	}

	experiment := resolveEmbeddingClientConfig(true)
	if experiment.model != "bge-m3" ||
		experiment.dimensions != benchmarkEmbeddingDims ||
		experiment.apiKey != "embedding-key" ||
		experiment.baseURL != "https://embedding.example/v1" ||
		!experiment.experiment ||
		experiment.headers["X-SMG-Routing-Key"] != "embedding-route" ||
		experiment.headers["X-SMG-Agent-Name"] != "embedding-agent" {
		t.Fatalf("experiment embedding config = %#v", experiment)
	}

	svc, err := NewKnowledgeService(
		VectorStoreInMemory,
		"deepseek-v3.2",
		0,
	)
	if err != nil {
		t.Fatalf("NewKnowledgeService() error = %v", err)
	}
	if svc.embeddingModel != defaultEmbeddingModel || svc.isExperimentLane() {
		t.Fatalf(
			"legacy service embedding model/lane = %q/%v",
			svc.embeddingModel,
			svc.isExperimentLane(),
		)
	}
}

func TestLLMGatewayHeadersAreExperimentOnly(t *testing.T) {
	t.Setenv("LLM_SMG_ROUTING_KEY", "llm-route")
	t.Setenv("LLM_SMG_AGENT_NAME", "llm-agent")

	legacy := &KnowledgeService{
		config: &ServiceConfig{IndexVariant: indexVariantLegacy},
	}
	if headers := legacy.llmGatewayHeaders(); len(headers) != 0 {
		t.Fatalf("legacy LLM headers = %#v, want none", headers)
	}

	experiment := &KnowledgeService{
		config: &ServiceConfig{IndexVariant: indexVariantBaseline},
	}
	headers := experiment.llmGatewayHeaders()
	if headers["X-SMG-Routing-Key"] != "llm-route" ||
		headers["X-SMG-Agent-Name"] != "llm-agent" {
		t.Fatalf("experiment LLM headers = %#v", headers)
	}
}

func TestBuildRuntimeConfigLegacyMatchesBaseline(t *testing.T) {
	t.Setenv("PGVECTOR_HOST", "legacy-pg")
	t.Setenv("PGVECTOR_PORT", "15432")
	t.Setenv("PGVECTOR_USER", "legacy-user")
	t.Setenv("PGVECTOR_DATABASE", "legacy-db")
	t.Setenv("PGVECTOR_TABLE", "legacy-table")
	// These experiment-only settings must not add fields to the legacy response.
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("LLM_SMG_ROUTING_KEY", "llm-route")
	t.Setenv("EMBEDDING_SMG_AGENT_NAME", "embedding-agent")

	svc := &KnowledgeService{
		config: &ServiceConfig{
			IndexVariant:       indexVariantLegacy,
			HybridVectorWeight: 0.99999,
			HybridTextWeight:   0.00001,
		},
		storeType:  VectorStorePGVector,
		modelName:  "deepseek-v3.2",
		searchMode: 0,
	}
	want := map[string]any{
		"model_name":           "deepseek-v3.2",
		"vectorstore":          string(VectorStorePGVector),
		"search_mode":          0,
		"use_rrf":              false,
		"hybrid_vector_weight": 0.99999,
		"hybrid_text_weight":   0.00001,
		"pg_table":             "legacy-table",
		"pg_connection": map[string]string{
			"host":     "legacy-pg",
			"port":     "15432",
			"user":     "legacy-user",
			"database": "legacy-db",
		},
	}
	if got := buildRuntimeConfig(context.Background(), svc); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy runtime config = %#v, want %#v", got, want)
	}
}

func TestBuildRuntimeConfigExperimentIsCompleteAndSecretFree(t *testing.T) {
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("LLM_SMG_ROUTING_KEY", "secret-llm-route")
	t.Setenv("EMBEDDING_SMG_AGENT_NAME", "secret-embedding-agent")
	t.Setenv("PGVECTOR_PASSWORD", "secret-pg-password")
	t.Setenv(
		"OPENAI_BASE_URL",
		"https://user:secret-llm-url@example.test/secret-llm-path/v1?token=secret#secret-llm-fragment",
	)
	t.Setenv(
		"EMBEDDING_BASE_URL",
		"user:secret-embedding-url@embedding.test:8443/secret-embedding-path/v1?token=secret#secret-embedding-fragment",
	)

	svc := &KnowledgeService{
		config: &ServiceConfig{
			IndexVariant:       indexVariantBaseline,
			HybridVectorWeight: 0.99999,
			HybridTextWeight:   0.00001,
		},
		storeType:      VectorStoreInMemory,
		modelName:      "deepseek-v3.2",
		embeddingModel: "bge-m3",
		searchMode:     1,
		vs:             inmemory.New(),
	}

	cfg := buildRuntimeConfig(context.Background(), svc)
	if got := cfg["model_name"]; got != "deepseek-v3.2" {
		t.Fatalf("model_name = %v, want deepseek-v3.2", got)
	}
	if got := cfg["embedding_model"]; got != "bge-m3" {
		t.Fatalf("embedding_model = %v, want bge-m3", got)
	}
	if got := cfg["agent_search_mode_enforced"]; got != true {
		t.Fatalf("agent_search_mode_enforced = %v, want true", got)
	}
	if got := cfg["agent_search_mode_effective"]; got != 1 {
		t.Fatalf("agent_search_mode_effective = %v, want 1", got)
	}
	if got := cfg["tool_argument_policy"]; got != toolArgumentPolicy {
		t.Fatalf("tool_argument_policy = %v, want %s", got, toolArgumentPolicy)
	}
	if got := cfg["max_argument_repairs"]; got != toolArgumentMaxRepairs {
		t.Fatalf("max_argument_repairs = %v, want %d", got, toolArgumentMaxRepairs)
	}
	if got := cfg["silent_argument_rewrite"]; got != false {
		t.Fatalf("silent_argument_rewrite = %v, want false", got)
	}
	if got := cfg["provider_strict"]; got != false {
		t.Fatalf("provider_strict = %v, want false", got)
	}
	for _, removedField := range []string{
		"prompt_max_searches",
		"hard_max_tool_iterations",
	} {
		if _, ok := cfg[removedField]; ok {
			t.Fatalf("removed runtime config field %q is still present", removedField)
		}
	}
	if got := cfg["index_document_count"]; got != 0 {
		t.Fatalf("index_document_count = %v, want 0", got)
	}
	if got := cfg["index_variant"]; got != indexVariantBaseline {
		t.Fatalf("index_variant = %v, want %s", got, indexVariantBaseline)
	}
	if got := cfg["chunk_manifest_digest"]; got != nil {
		t.Fatalf("chunk_manifest_digest = %v, want nil", got)
	}
	if got := cfg["context_set_digest"]; got != nil {
		t.Fatalf("context_set_digest = %v, want nil", got)
	}

	module, ok := cfg["framework_module"].(map[string]string)
	if !ok {
		t.Fatalf("framework_module type = %T, want map[string]string", cfg["framework_module"])
	}
	if module["path"] != frameworkModulePath || module["version"] == "" {
		t.Fatalf("framework_module = %#v, want path and version", module)
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}
	for _, secret := range []string{
		"secret-llm-route",
		"secret-embedding-agent",
		"secret-pg-password",
		"secret-llm-url",
		"secret-embedding-url",
		"secret-llm-path",
		"secret-embedding-path",
		"secret-llm-fragment",
		"secret-embedding-fragment",
		"token=secret",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("runtime config leaked secret %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), "X-SMG-Routing-Key") {
		t.Fatalf("runtime config does not include the LLM header name: %s", encoded)
	}
	if !strings.Contains(string(encoded), "X-SMG-Agent-Name") {
		t.Fatalf("runtime config does not include the embedding header name: %s", encoded)
	}
	wantLLMEndpoint := "https://example.test|path_sha256=" +
		digestText("/secret-llm-path/v1")
	if got := cfg["llm_endpoint"]; got != wantLLMEndpoint {
		t.Fatalf("llm_endpoint = %v, want %s", got, wantLLMEndpoint)
	}
	wantEmbeddingEndpoint := "embedding.test:8443|path_sha256=" +
		digestText("/secret-embedding-path/v1")
	if got := cfg["embedding_endpoint"]; got != wantEmbeddingEndpoint {
		t.Fatalf("embedding_endpoint = %v, want %s", got, wantEmbeddingEndpoint)
	}
}

func TestHandleConfigMethodRestrictionIsExperimentOnly(t *testing.T) {
	previous := knowledgeSvc
	t.Cleanup(func() { knowledgeSvc = previous })

	legacy := &KnowledgeService{
		config: &ServiceConfig{IndexVariant: indexVariantLegacy},
	}
	knowledgeSvc = legacy
	legacyRecorder := httptest.NewRecorder()
	handleConfig(
		legacyRecorder,
		httptest.NewRequest(http.MethodPost, "/config", nil),
	)
	if legacyRecorder.Code != http.StatusOK {
		t.Fatalf("legacy POST /config status = %d, want 200", legacyRecorder.Code)
	}
	var legacyPayload map[string]any
	if err := json.Unmarshal(legacyRecorder.Body.Bytes(), &legacyPayload); err != nil {
		t.Fatalf("decode legacy /config: %v", err)
	}
	if len(legacyPayload) != 8 {
		t.Fatalf("legacy /config fields = %v", legacyPayload)
	}

	experiment := &KnowledgeService{
		config: &ServiceConfig{IndexVariant: indexVariantBaseline},
	}
	knowledgeSvc = experiment
	experimentRecorder := httptest.NewRecorder()
	handleConfig(
		experimentRecorder,
		httptest.NewRequest(http.MethodPost, "/config", nil),
	)
	if experimentRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf(
			"experiment POST /config status = %d, want 405",
			experimentRecorder.Code,
		)
	}
}

func TestBuildLoadResponsePreservesLegacyJSON(t *testing.T) {
	legacy := &KnowledgeService{
		config: &ServiceConfig{IndexVariant: indexVariantLegacy},
	}
	legacyJSON, err := json.Marshal(buildLoadResponse(legacy, 2, 1500*time.Millisecond))
	if err != nil {
		t.Fatalf("marshal legacy load response: %v", err)
	}
	wantLegacy := `{"success":true,"message":"Documents loaded successfully","count":2}`
	if string(legacyJSON) != wantLegacy {
		t.Fatalf("legacy load response = %s, want %s", legacyJSON, wantLegacy)
	}

	experiment := &KnowledgeService{
		config: &ServiceConfig{IndexVariant: indexVariantBaseline},
		experimentSource: &experimentManifestSource{
			manifest: &chunkManifest{ChunksCount: 13},
		},
	}
	experimentJSON, err := json.Marshal(buildLoadResponse(
		experiment,
		2,
		1500*time.Millisecond,
	))
	if err != nil {
		t.Fatalf("marshal experiment load response: %v", err)
	}
	wantExperiment := `{"success":true,"message":"Documents loaded successfully","count":13,"elapsed_ms":1500}`
	if string(experimentJSON) != wantExperiment {
		t.Fatalf(
			"experiment load response = %s, want %s",
			experimentJSON,
			wantExperiment,
		)
	}
}

func TestAgentSearchModeIsEnforcedOnlyForExperimentVariants(t *testing.T) {
	tests := []struct {
		variant string
		want    bool
	}{
		{variant: indexVariantLegacy, want: false},
		{variant: indexVariantBaseline, want: true},
		{variant: indexVariantContextual, want: true},
	}
	for _, test := range tests {
		t.Run(test.variant, func(t *testing.T) {
			svc := &KnowledgeService{
				config: &ServiceConfig{IndexVariant: test.variant},
			}
			if got := svc.agentSearchModeEnforced(); got != test.want {
				t.Fatalf("agentSearchModeEnforced() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSearchModeKnowledgeEnforcesModeAndRecordsStructuredResults(t *testing.T) {
	metadata := map[string]any{
		"contextual_retrieval_chunk_id": "chunk-1",
		"title":                         "Example",
	}
	underlyingResult := &knowledge.SearchResult{
		Documents: []*knowledge.Result{
			{
				Document: &document.Document{
					ID:       "chunk-1",
					Content:  "retrieved text",
					Metadata: metadata,
				},
				Score: 0.75,
			},
		},
	}
	inner := &recordingKnowledge{result: underlyingResult}
	recorder := &agentSearchRecorder{}
	wrapper := &searchModeKnowledge{
		inner:      inner,
		searchMode: 1,
		recorder:   recorder,
	}
	request := &knowledge.SearchRequest{
		Query:      "example query",
		MaxResults: 4,
		SearchMode: 0,
	}

	gotResult, err := wrapper.Search(context.Background(), request)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if gotResult != underlyingResult {
		t.Fatal("Search() did not return the underlying result unchanged")
	}
	if request.SearchMode != 0 {
		t.Fatalf("caller request SearchMode = %d, want unchanged 0", request.SearchMode)
	}
	if inner.request.SearchMode != 1 {
		t.Fatalf("effective SearchMode = %d, want 1", inner.request.SearchMode)
	}

	searches := recorder.snapshot()
	if len(searches) != 1 {
		t.Fatalf("recorded searches = %d, want 1", len(searches))
	}
	search := searches[0]
	if search.Query != "example query" || search.Request.MaxResults != 4 ||
		search.Request.SearchMode != 1 {
		t.Fatalf("recorded request = %#v", search)
	}
	if len(search.Results) != 1 {
		t.Fatalf("recorded results = %d, want 1", len(search.Results))
	}
	result := search.Results[0]
	if result.Rank != 1 || result.DocumentID != "chunk-1" || result.Score != 0.75 {
		t.Fatalf("recorded result = %#v", result)
	}
	if result.ContentSHA256 != digestText("retrieved text") {
		t.Fatalf("content_sha256 = %q", result.ContentSHA256)
	}
	if got := result.Metadata["contextual_retrieval_chunk_id"]; got != "chunk-1" {
		t.Fatalf("recorded chunk ID metadata = %v, want chunk-1", got)
	}
}

func TestAnswerRequestContextCancellationIsExperimentOnly(t *testing.T) {
	tests := []struct {
		name             string
		variant          string
		wantCancellation bool
	}{
		{
			name:             "legacy ignores request cancellation",
			variant:          indexVariantLegacy,
			wantCancellation: false,
		},
		{
			name:             "baseline inherits request cancellation",
			variant:          indexVariantBaseline,
			wantCancellation: true,
		},
		{
			name:             "contextual inherits request cancellation",
			variant:          indexVariantContextual,
			wantCancellation: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestContext, cancel := context.WithCancel(context.Background())
			request := httptest.NewRequest("POST", "/answer", nil).WithContext(requestContext)
			svc := &KnowledgeService{
				config: &ServiceConfig{IndexVariant: test.variant},
			}
			answerContext := answerRequestContext(request, svc)

			cancel()
			select {
			case <-answerContext.Done():
				if !test.wantCancellation {
					t.Fatal("answer context inherited cancellation for legacy lane")
				}
			default:
				if test.wantCancellation {
					t.Fatal("answer context did not inherit request cancellation")
				}
			}
		})
	}
}
