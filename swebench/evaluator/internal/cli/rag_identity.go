//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

const (
	workspaceRepresentationCurrentFixed  = "current-fixed"
	workspaceRepresentationFixedRaw      = "fixed-raw"
	workspaceRepresentationASTCode       = "ast-code"
	workspaceRepresentationASTStructured = "ast-structured"
	nativeCodeSearchToolOrder            = "bash,code_search"
	nativeCodeSearchInvocationDedup      = "disabled"
)

var workspaceRepresentationSchemas = map[string]string{
	workspaceRepresentationCurrentFixed:  "workspace-representation/v3;reader=text;chunk=fixed;size=1024;overlap=128;whitespace=normalize-lines;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled",
	workspaceRepresentationFixedRaw:      "workspace-representation/v3;reader=benchmark-local;chunk=fixed;size=1024;overlap=128;whitespace=preserve;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled",
	workspaceRepresentationASTCode:       "workspace-representation/v3;reader=public-python-ast@v0.0.0-20260728070417-4237accb70cb;tests=include;hidden=include;paths=stable;fallback=fixed-raw;embedding=code;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled",
	workspaceRepresentationASTStructured: "workspace-representation/v3;reader=public-python-ast@v0.0.0-20260728070417-4237accb70cb;tests=include;hidden=include;paths=stable;fallback=fixed-raw;embedding=structured-code;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled",
}

// nativeRetrievalTraceDocument mirrors the portable, source-free retrieval
// identity written by the Native runner. It intentionally lives in evaluator
// instead of importing an implementation-internal package.
type nativeRetrievalTraceDocument struct {
	ID            string  `json:"id,omitempty"`
	Path          string  `json:"path,omitempty"`
	Lines         string  `json:"lines,omitempty"`
	Symbol        string  `json:"symbol,omitempty"`
	Score         float64 `json:"score"`
	ContentSHA256 string  `json:"content_sha256"`
}

type nativeRetrievalTraceEntry struct {
	Call              int                            `json:"call"`
	ToolCallID        string                         `json:"tool_call_id"`
	Query             string                         `json:"query"`
	Status            string                         `json:"status"`
	Error             string                         `json:"error,omitempty"`
	ErrorSHA256       string                         `json:"error_sha256,omitempty"`
	ArgumentsSHA256   string                         `json:"arguments_sha256"`
	ResultSHA256      string                         `json:"result_sha256"`
	ObservationSHA256 string                         `json:"observation_sha256,omitempty"`
	ResultBytes       int                            `json:"result_bytes"`
	ObservationBytes  int                            `json:"observation_bytes,omitempty"`
	Documents         []nativeRetrievalTraceDocument `json:"documents"`
}

type nativeWorkspaceIndexStats struct {
	Representation        string         `json:"representation"`
	RepresentationSchema  string         `json:"representation_schema"`
	RepresentationSHA256  string         `json:"representation_sha256"`
	ParserDependency      string         `json:"parser_dependency,omitempty"`
	ParserRuntime         string         `json:"parser_runtime,omitempty"`
	ParserRuntimeSHA256   string         `json:"parser_runtime_sha256,omitempty"`
	Documents             int            `json:"documents"`
	EligibleFiles         int            `json:"eligible_files"`
	IndexedFiles          int            `json:"indexed_files"`
	FileCoverage          float64        `json:"file_coverage"`
	EligibleFileSetSHA256 string         `json:"eligible_file_set_sha256"`
	EligibleContentSHA256 string         `json:"eligible_content_sha256"`
	IndexedFileSetSHA256  string         `json:"indexed_file_set_sha256"`
	DocumentSetSHA256     string         `json:"document_set_sha256"`
	MissingFiles          []string       `json:"missing_files,omitempty"`
	FallbackDocuments     int            `json:"fallback_documents,omitempty"`
	FallbackReasons       map[string]int `json:"fallback_reasons,omitempty"`
	NodeTypes             map[string]int `json:"node_types,omitempty"`
	ContentChars          int            `json:"content_chars"`
	EmbeddingTextChars    int            `json:"embedding_text_chars"`
	DuplicateDocuments    int            `json:"duplicate_documents,omitempty"`
	DuplicateDocumentRate float64        `json:"duplicate_document_rate"`
	DurationMS            int64          `json:"duration_ms"`
	PreloadedDocuments    int            `json:"preloaded_documents,omitempty"`
	PreloadedChars        int            `json:"preloaded_chars,omitempty"`
	PreloadInjected       bool           `json:"preload_injected"`
	RetrievalMode         string         `json:"retrieval_mode"`
	InvocationDedup       string         `json:"invocation_dedup"`
}

type nativeEmbeddingMetrics struct {
	Requests      int64 `json:"requests"`
	BatchRequests int64 `json:"batch_requests"`
	Inputs        int64 `json:"inputs"`
	Errors        int64 `json:"errors"`
	PromptTokens  int64 `json:"prompt_tokens,omitempty"`
	TotalTokens   int64 `json:"total_tokens,omitempty"`
	DurationMS    int64 `json:"duration_ms"`
}

type nativeEmbeddingCacheMetrics struct {
	Requests        int64 `json:"requests"`
	BatchRequests   int64 `json:"batch_requests"`
	Inputs          int64 `json:"inputs"`
	Hits            int64 `json:"hits"`
	Misses          int64 `json:"misses"`
	Writes          int64 `json:"writes"`
	Corruptions     int64 `json:"corruptions"`
	Errors          int64 `json:"errors"`
	BytesRead       int64 `json:"bytes_read"`
	BytesWritten    int64 `json:"bytes_written"`
	ReadDurationMS  int64 `json:"read_duration_ms"`
	WriteDurationMS int64 `json:"write_duration_ms"`
}

type nativeRAGIdentity struct {
	CodeSearch                bool
	CodeSearchToolOrder       string
	CodeSearchInvocationDedup string
	WorkspacePreload          *bool
	WorkspaceRepresentation   string
	RepresentationSchema      string
	RepresentationSHA256      string
	EmbeddingConfigSHA256     string
	EmbeddingConfig           map[string]any
}

func validateNativeRAGIdentity(label string, identity nativeRAGIdentity) error {
	hasEnabledOnlyIdentity := identity.CodeSearchToolOrder != "" || identity.CodeSearchInvocationDedup != "" ||
		identity.WorkspacePreload != nil ||
		identity.WorkspaceRepresentation != "" || identity.RepresentationSchema != "" ||
		identity.RepresentationSHA256 != "" || identity.EmbeddingConfigSHA256 != "" ||
		identity.EmbeddingConfig != nil
	if !identity.CodeSearch {
		if hasEnabledOnlyIdentity {
			return fmt.Errorf("%s contains workspace retrieval identity with code_search=false", label)
		}
		return nil
	}
	if identity.WorkspacePreload == nil {
		return fmt.Errorf("%s is missing workspace_preload for code_search=true", label)
	}
	if identity.CodeSearchToolOrder != nativeCodeSearchToolOrder {
		return fmt.Errorf(
			"%s code_search_tool_order %q does not match canonical %q",
			label,
			identity.CodeSearchToolOrder,
			nativeCodeSearchToolOrder,
		)
	}
	if identity.CodeSearchInvocationDedup != nativeCodeSearchInvocationDedup {
		return fmt.Errorf(
			"%s code_search_invocation_dedup %q does not match canonical %q",
			label,
			identity.CodeSearchInvocationDedup,
			nativeCodeSearchInvocationDedup,
		)
	}
	expectedSchema, expectedSHA256, err := nativeWorkspaceRepresentationIdentity(identity.WorkspaceRepresentation)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if identity.RepresentationSchema != expectedSchema {
		return fmt.Errorf(
			"%s workspace_representation_schema %q does not match canonical schema %q",
			label,
			identity.RepresentationSchema,
			expectedSchema,
		)
	}
	if identity.RepresentationSHA256 != expectedSHA256 {
		return fmt.Errorf(
			"%s workspace_representation_sha256 %q does not match canonical digest %q",
			label,
			identity.RepresentationSHA256,
			expectedSHA256,
		)
	}
	if identity.EmbeddingConfigSHA256 == "" {
		if identity.EmbeddingConfig != nil {
			return fmt.Errorf("%s has embedding_config without embedding_config_sha256", label)
		}
		return nil
	}
	if !isSHA256Hex(identity.EmbeddingConfigSHA256) {
		return fmt.Errorf(
			"%s embedding_config_sha256 %q is not a SHA-256 digest",
			label,
			identity.EmbeddingConfigSHA256,
		)
	}
	if len(identity.EmbeddingConfig) == 0 {
		return fmt.Errorf("%s is missing embedding_config for embedding_config_sha256", label)
	}
	return validateRedactedEmbeddingConfig(label, identity.EmbeddingConfig)
}

func validateNativeTraceRAGInfo(instanceID string, raw nativeInfoJSON) (string, error) {
	enabled := raw.CodeSearch != nil && *raw.CodeSearch
	hasEnabledOnlyIdentity := raw.CodeSearchToolOrder != "" || raw.CodeSearchInvocationDedup != "" ||
		raw.WorkspacePreload != nil || raw.WorkspaceRepresentation != "" ||
		raw.RepresentationSHA256 != "" || raw.EmbeddingConfigSHA256 != ""
	if !enabled {
		if hasEnabledOnlyIdentity {
			return "", fmt.Errorf(
				"native trace for %s contains workspace retrieval info without info.code_search=true",
				instanceID,
			)
		}
		return "", nil
	}
	if raw.WorkspacePreload == nil {
		return "", missingNativeFieldError(instanceID, "info.workspace_preload")
	}
	if raw.CodeSearchToolOrder != nativeCodeSearchToolOrder {
		return "", fmt.Errorf(
			"native trace for %s info.code_search_tool_order %q does not match canonical %q",
			instanceID,
			raw.CodeSearchToolOrder,
			nativeCodeSearchToolOrder,
		)
	}
	if raw.CodeSearchInvocationDedup != nativeCodeSearchInvocationDedup {
		return "", fmt.Errorf(
			"native trace for %s info.code_search_invocation_dedup %q does not match canonical %q",
			instanceID,
			raw.CodeSearchInvocationDedup,
			nativeCodeSearchInvocationDedup,
		)
	}
	schema, expectedSHA256, err := nativeWorkspaceRepresentationIdentity(raw.WorkspaceRepresentation)
	if err != nil {
		return "", fmt.Errorf("native trace for %s: %w", instanceID, err)
	}
	if raw.RepresentationSHA256 != expectedSHA256 {
		return "", fmt.Errorf(
			"native trace for %s info.workspace_representation_sha256 %q does not match canonical digest %q",
			instanceID,
			raw.RepresentationSHA256,
			expectedSHA256,
		)
	}
	if raw.EmbeddingConfigSHA256 != "" && !isSHA256Hex(raw.EmbeddingConfigSHA256) {
		return "", fmt.Errorf(
			"native trace for %s has invalid info.embedding_config_sha256 %q",
			instanceID,
			raw.EmbeddingConfigSHA256,
		)
	}
	return schema, nil
}

func validateNativeRAGManifest(label string, manifest runnerManifest) error {
	identity := nativeRAGIdentity{
		CodeSearch:                manifest.CodeSearch,
		CodeSearchToolOrder:       manifest.CodeSearchToolOrder,
		CodeSearchInvocationDedup: manifest.CodeSearchInvocationDedup,
		WorkspacePreload:          cloneBool(manifest.WorkspacePreload),
		WorkspaceRepresentation:   manifest.WorkspaceRepresentation,
		RepresentationSchema:      manifest.RepresentationSchema,
		RepresentationSHA256:      manifest.RepresentationSHA256,
		EmbeddingConfigSHA256:     manifest.EmbeddingConfigSHA256,
		EmbeddingConfig:           manifest.EmbeddingConfig,
	}
	if manifest.RunnerType != "trpc-agent-go-native" {
		if manifest.CodeSearch || identity.CodeSearchToolOrder != "" ||
			identity.CodeSearchInvocationDedup != "" || identity.WorkspacePreload != nil ||
			identity.WorkspaceRepresentation != "" || identity.RepresentationSchema != "" ||
			identity.RepresentationSHA256 != "" || identity.EmbeddingConfigSHA256 != "" ||
			identity.EmbeddingConfig != nil || manifest.Embedding != nil || manifest.EmbeddingCache != nil {
			return fmt.Errorf("%s runner type %q does not support workspace retrieval fields", label, manifest.RunnerType)
		}
		return nil
	}
	if err := validateNativeRAGIdentity(label, identity); err != nil {
		return err
	}
	if !manifest.CodeSearch {
		if manifest.Embedding != nil || manifest.EmbeddingCache != nil {
			return fmt.Errorf("%s contains embedding telemetry with code_search=false", label)
		}
		return nil
	}
	if manifest.EmbeddingConfigSHA256 == "" {
		if manifest.Embedding != nil || manifest.EmbeddingCache != nil {
			return fmt.Errorf("%s contains embedding telemetry without embedding_config_sha256", label)
		}
		return nil
	}
	if manifest.Embedding == nil {
		return fmt.Errorf("%s is missing embedding telemetry for configured embedding", label)
	}
	if err := validateNativeEmbeddingMetrics(label+" embedding", *manifest.Embedding); err != nil {
		return err
	}
	cacheEnabled, err := redactedEmbeddingCacheEnabled(manifest.EmbeddingConfig)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if cacheEnabled && manifest.EmbeddingCache == nil {
		return fmt.Errorf("%s is missing embedding_cache telemetry for enabled cache", label)
	}
	if !cacheEnabled && manifest.EmbeddingCache != nil {
		return fmt.Errorf("%s has embedding_cache telemetry for disabled cache", label)
	}
	if manifest.EmbeddingCache != nil {
		return validateNativeEmbeddingCacheMetrics(label+" embedding cache", *manifest.EmbeddingCache)
	}
	return nil
}

func validateShardsRAGManifest(
	label string,
	shards shardsManifest,
	canonical shardRunnerIdentity,
) error {
	if canonical.RunnerType != "trpc-agent-go-native" {
		if shards.Embedding != nil || shards.EmbeddingCache != nil {
			return fmt.Errorf("%s has embedding telemetry for non-Native runner", label)
		}
		return nil
	}
	manifest := runnerManifestForNativeShardIdentity(canonical)
	manifest.Embedding = cloneNativeEmbeddingMetrics(shards.Embedding)
	manifest.EmbeddingCache = cloneNativeEmbeddingCacheMetrics(shards.EmbeddingCache)
	if err := validateNativeRAGManifest(label, manifest); err != nil {
		return err
	}
	var embedding nativeEmbeddingMetrics
	var embeddingCache nativeEmbeddingCacheMetrics
	hasEmbedding := false
	hasEmbeddingCache := false
	for _, shard := range shards.Shards {
		shardManifest := runnerManifestForNativeShard(shard)
		if err := validateNativeRAGManifest("native shard "+shard.RunID, shardManifest); err != nil {
			return err
		}
		if shard.Embedding != nil {
			hasEmbedding = true
			embedding.add(*shard.Embedding)
		}
		if shard.EmbeddingCache != nil {
			hasEmbeddingCache = true
			embeddingCache.add(*shard.EmbeddingCache)
		}
	}
	if (shards.Embedding == nil && hasEmbedding) ||
		(shards.Embedding != nil && embedding != *shards.Embedding) {
		return fmt.Errorf("%s embedding telemetry does not match shard aggregate", label)
	}
	if (shards.EmbeddingCache == nil && hasEmbeddingCache) ||
		(shards.EmbeddingCache != nil && embeddingCache != *shards.EmbeddingCache) {
		return fmt.Errorf("%s embedding_cache telemetry does not match shard aggregate", label)
	}
	return nil
}

func nativeWorkspaceRepresentationIdentity(representation string) (string, string, error) {
	if representation != strings.TrimSpace(representation) || representation == "" {
		return "", "", fmt.Errorf("workspace_representation %q is not canonical", representation)
	}
	schema, ok := workspaceRepresentationSchemas[representation]
	if !ok {
		return "", "", fmt.Errorf(
			"unsupported workspace_representation %q (want current-fixed, fixed-raw, ast-code, or ast-structured)",
			representation,
		)
	}
	return schema, nativeHashStrings([]string{schema}), nil
}

func nativeHashStrings(values []string) string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	hasher := sha256.New()
	for _, value := range copied {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func validateRedactedEmbeddingConfig(label string, config map[string]any) error {
	for _, key := range []string{"provider", "endpoint_configured", "credentials_configured", "model", "dimensions", "batch_size", "concurrency", "retrieval_mode", "max_results", "max_chars", "cache"} {
		if _, ok := config[key]; !ok {
			return fmt.Errorf("%s embedding_config is missing redacted field %q", label, key)
		}
	}
	if _, err := redactedEmbeddingCacheEnabled(config); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	for _, key := range []string{"provider", "model", "retrieval_mode"} {
		value, ok := config[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s embedding_config.%s is not a non-empty string", label, key)
		}
	}
	for _, key := range []string{"endpoint_configured", "credentials_configured"} {
		if _, ok := config[key].(bool); !ok {
			return fmt.Errorf("%s embedding_config.%s is not a boolean", label, key)
		}
	}
	for _, key := range []string{"dimensions", "batch_size", "concurrency", "max_results", "max_chars"} {
		value, ok := jsonPositiveInteger(config[key])
		if !ok || value <= 0 {
			return fmt.Errorf("%s embedding_config.%s is not a positive integer", label, key)
		}
	}
	cache := config["cache"].(map[string]any)
	for _, key := range []string{"directory_configured", "model_fingerprint", "access"} {
		if _, ok := cache[key]; !ok {
			return fmt.Errorf("%s embedding_config.cache is missing redacted field %q", label, key)
		}
	}
	if _, ok := cache["directory_configured"].(bool); !ok {
		return fmt.Errorf("%s embedding_config.cache.directory_configured is not a boolean", label)
	}
	if _, ok := cache["model_fingerprint"].(string); !ok {
		return fmt.Errorf("%s embedding_config.cache.model_fingerprint is not a string", label)
	}
	if access, ok := cache["access"].(string); !ok || access != "readwrite" {
		return fmt.Errorf("%s embedding_config.cache.access is not canonical readwrite", label)
	}
	for key := range config {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "api_key") || strings.Contains(lower, "api_base") ||
			strings.Contains(lower, "header") || strings.Contains(lower, "directory") {
			return fmt.Errorf("%s embedding_config contains non-redacted key %q", label, key)
		}
	}
	return nil
}

func jsonPositiveInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func redactedEmbeddingCacheEnabled(config map[string]any) (bool, error) {
	value, ok := config["cache"]
	if !ok {
		return false, fmt.Errorf("embedding_config is missing cache metadata")
	}
	cache, ok := value.(map[string]any)
	if !ok {
		return false, fmt.Errorf("embedding_config.cache is not an object")
	}
	enabled, ok := cache["enabled"].(bool)
	if !ok {
		return false, fmt.Errorf("embedding_config.cache.enabled is not a boolean")
	}
	return enabled, nil
}

func validateNativeRetrievalTelemetry(instanceID string, trace nativeTraceEnvelope) error {
	for _, value := range []struct {
		name  string
		value int
	}{
		{"code_search_calls", trace.CodeSearchCalls},
		{"code_search_errors", trace.CodeSearchErrors},
		{"code_search_result_bytes", trace.CodeSearchResultBytes},
		{"code_search_observation_bytes", trace.CodeSearchObservationBytes},
	} {
		if value.value < 0 {
			return fmt.Errorf("native trace for %s has negative %s %d", instanceID, value.name, value.value)
		}
	}
	hasTelemetry := trace.CodeSearchCalls != 0 || trace.CodeSearchErrors != 0 || trace.CodeSearchResultBytes != 0 ||
		trace.CodeSearchObservationBytes != 0 || len(trace.CodeSearchRawResults) != 0 ||
		len(trace.RetrievalTrace) != 0 || trace.WorkspaceIndex != nil ||
		trace.Embedding != nil || trace.EmbeddingCache != nil
	if !trace.Info.CodeSearch {
		if hasTelemetry {
			return fmt.Errorf("native trace for %s has workspace retrieval telemetry with info.code_search=false", instanceID)
		}
		return nil
	}
	if len(trace.CodeSearchRawResults) != len(trace.RetrievalTrace) {
		return fmt.Errorf(
			"native trace for %s has %d code_search_raw_results and %d retrieval_trace entries",
			instanceID,
			len(trace.CodeSearchRawResults),
			len(trace.RetrievalTrace),
		)
	}
	if len(trace.RetrievalTrace) != trace.CodeSearchCalls {
		return fmt.Errorf(
			"native trace for %s has %d retrieval_trace entries, want code_search_calls=%d",
			instanceID,
			len(trace.RetrievalTrace),
			trace.CodeSearchCalls,
		)
	}
	resultBytes := 0
	observationBytes := 0
	errorCount := 0
	for index, entry := range trace.RetrievalTrace {
		if entry.Call != index+1 {
			return fmt.Errorf("native trace for %s retrieval_trace call %d is not sequential", instanceID, entry.Call)
		}
		if strings.TrimSpace(entry.ToolCallID) == "" {
			return fmt.Errorf("native trace for %s retrieval_trace call %d has empty tool_call_id", instanceID, entry.Call)
		}
		switch entry.Status {
		case "success":
			if entry.Error != "" || entry.ErrorSHA256 != "" {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has error fields with status=success", instanceID, entry.Call)
			}
			if entry.ObservationBytes <= 0 || !isSHA256Hex(entry.ObservationSHA256) {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has incomplete success observation", instanceID, entry.Call)
			}
		case "error":
			errorCount++
			if strings.TrimSpace(entry.Error) == "" || entry.ErrorSHA256 != digestBytes([]byte(entry.Error)) {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has invalid error identity", instanceID, entry.Call)
			}
			if len(entry.Documents) != 0 {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has documents with status=error", instanceID, entry.Call)
			}
		default:
			return fmt.Errorf("native trace for %s retrieval_trace call %d has invalid status %q", instanceID, entry.Call, entry.Status)
		}
		for _, digest := range []struct {
			name  string
			value string
		}{
			{"arguments_sha256", entry.ArgumentsSHA256},
			{"result_sha256", entry.ResultSHA256},
		} {
			if !isSHA256Hex(digest.value) {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has invalid %s %q", instanceID, entry.Call, digest.name, digest.value)
			}
		}
		if entry.ResultBytes < 0 || entry.ObservationBytes < 0 {
			return fmt.Errorf("native trace for %s retrieval_trace call %d has negative byte count", instanceID, entry.Call)
		}
		if entry.ObservationBytes == 0 && entry.ObservationSHA256 != "" {
			return fmt.Errorf("native trace for %s retrieval_trace call %d has observation digest without bytes", instanceID, entry.Call)
		}
		if entry.ObservationBytes > 0 && !isSHA256Hex(entry.ObservationSHA256) {
			return fmt.Errorf("native trace for %s retrieval_trace call %d has invalid observation_sha256", instanceID, entry.Call)
		}
		raw, err := compactNativeRawResult(trace.CodeSearchRawResults[index])
		if err != nil {
			return fmt.Errorf("native trace for %s retrieval_trace call %d has invalid raw result: %w", instanceID, entry.Call, err)
		}
		if len(raw) != entry.ResultBytes || digestBytes(raw) != entry.ResultSHA256 {
			return fmt.Errorf("native trace for %s retrieval_trace call %d does not match raw result", instanceID, entry.Call)
		}
		if entry.Status == "error" {
			var payload struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil || payload.Error != entry.Error {
				return fmt.Errorf("native trace for %s retrieval_trace call %d error does not match raw result", instanceID, entry.Call)
			}
		}
		for _, document := range entry.Documents {
			if math.IsNaN(document.Score) || math.IsInf(document.Score, 0) {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has non-finite document score", instanceID, entry.Call)
			}
			if !isSHA256Hex(document.ContentSHA256) {
				return fmt.Errorf("native trace for %s retrieval_trace call %d has invalid document content_sha256", instanceID, entry.Call)
			}
		}
		resultBytes += entry.ResultBytes
		observationBytes += entry.ObservationBytes
	}
	if errorCount != trace.CodeSearchErrors {
		return fmt.Errorf(
			"native trace for %s has %d retrieval errors, want code_search_errors=%d",
			instanceID,
			errorCount,
			trace.CodeSearchErrors,
		)
	}
	if resultBytes != trace.CodeSearchResultBytes || observationBytes != trace.CodeSearchObservationBytes {
		return fmt.Errorf(
			"native trace for %s retrieval byte totals %d/%d do not match declared %d/%d",
			instanceID,
			resultBytes,
			observationBytes,
			trace.CodeSearchResultBytes,
			trace.CodeSearchObservationBytes,
		)
	}
	if trace.WorkspaceIndex == nil {
		if trace.CodeSearchCalls != 0 {
			return fmt.Errorf(
				"native trace for %s has code_search calls without workspace_index",
				instanceID,
			)
		}
		if trace.Info.ExitStatus != "Error" || trace.Info.ErrorCategory != "environment" {
			return fmt.Errorf("native trace for %s is missing workspace_index for code_search=true", instanceID)
		}
	} else if err := validateNativeWorkspaceIndex(instanceID, trace.Info, *trace.WorkspaceIndex); err != nil {
		return err
	}
	if trace.Info.EmbeddingConfigSHA256 == "" {
		if trace.Embedding != nil || trace.EmbeddingCache != nil {
			return fmt.Errorf("native trace for %s has embedding telemetry without embedding_config_sha256", instanceID)
		}
		return nil
	}
	if trace.Embedding == nil && !(trace.Info.ExitStatus == "Error" && trace.Info.ErrorCategory == "environment") {
		return fmt.Errorf("native trace for %s is missing configured embedding telemetry", instanceID)
	}
	if trace.Embedding != nil {
		if err := validateNativeEmbeddingMetrics("native trace for "+instanceID+" embedding", *trace.Embedding); err != nil {
			return err
		}
	}
	if trace.EmbeddingCache != nil {
		return validateNativeEmbeddingCacheMetrics("native trace for "+instanceID+" embedding cache", *trace.EmbeddingCache)
	}
	return nil
}

func compactNativeRawResult(value []byte) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func validateImportedNativeRAGProjection(row importedCase) error {
	rawInfo := nativeInfoJSON{
		CodeSearchToolOrder:       row.CodeSearchToolOrder,
		CodeSearchInvocationDedup: row.CodeSearchInvocationDedup,
		WorkspacePreload:          cloneBool(row.WorkspacePreload),
		WorkspaceRepresentation:   row.WorkspaceRepresentation,
		RepresentationSHA256:      row.RepresentationSHA256,
		EmbeddingConfigSHA256:     row.EmbeddingConfigSHA256,
	}
	if row.CodeSearch {
		rawInfo.CodeSearch = new(bool)
		*rawInfo.CodeSearch = true
	}
	schema, err := validateNativeTraceRAGInfo(row.InstanceID, rawInfo)
	if err != nil {
		return err
	}
	if schema != row.RepresentationSchema {
		return fmt.Errorf(
			"imported native case %s workspace_representation_schema %q does not match canonical %q",
			row.InstanceID,
			row.RepresentationSchema,
			schema,
		)
	}
	result := row.Result
	hasTelemetry := result.CodeSearchCalls != 0 || result.CodeSearchErrors != 0 || result.CodeSearchResultBytes != 0 ||
		result.CodeSearchObservationBytes != 0 || len(result.RetrievalTrace) != 0 ||
		result.WorkspaceIndex != nil || result.Embedding != nil || result.EmbeddingCache != nil
	if !row.CodeSearch {
		if hasTelemetry {
			return fmt.Errorf("imported native case %s has workspace retrieval telemetry with code_search=false", row.InstanceID)
		}
		return nil
	}
	for _, value := range []struct {
		name  string
		value int
	}{
		{"code_search_calls", result.CodeSearchCalls},
		{"code_search_errors", result.CodeSearchErrors},
		{"code_search_result_bytes", result.CodeSearchResultBytes},
		{"code_search_observation_bytes", result.CodeSearchObservationBytes},
	} {
		if value.value < 0 {
			return fmt.Errorf("imported native case %s has negative %s", row.InstanceID, value.name)
		}
	}
	if len(result.RetrievalTrace) != result.CodeSearchCalls {
		return fmt.Errorf("imported native case %s retrieval entries do not match calls", row.InstanceID)
	}
	errors := 0
	for _, entry := range result.RetrievalTrace {
		if entry.Status == "error" {
			errors++
		}
	}
	if errors != result.CodeSearchErrors {
		return fmt.Errorf("imported native case %s retrieval errors do not match code_search_errors", row.InstanceID)
	}
	info := nativeInfoEnvelope{
		CodeSearch:                    true,
		CodeSearchToolOrder:           row.CodeSearchToolOrder,
		CodeSearchInvocationDedup:     row.CodeSearchInvocationDedup,
		WorkspacePreload:              row.WorkspacePreload != nil && *row.WorkspacePreload,
		WorkspaceRepresentation:       row.WorkspaceRepresentation,
		WorkspaceRepresentationSchema: row.RepresentationSchema,
		RepresentationSHA256:          row.RepresentationSHA256,
		EmbeddingConfigSHA256:         row.EmbeddingConfigSHA256,
	}
	if result.WorkspaceIndex != nil {
		if err := validateNativeWorkspaceIndex(row.InstanceID, info, *result.WorkspaceIndex); err != nil {
			return err
		}
	}
	if result.Embedding != nil {
		if err := validateNativeEmbeddingMetrics("imported native case "+row.InstanceID+" embedding", *result.Embedding); err != nil {
			return err
		}
	}
	if result.EmbeddingCache != nil {
		if err := validateNativeEmbeddingCacheMetrics("imported native case "+row.InstanceID+" embedding cache", *result.EmbeddingCache); err != nil {
			return err
		}
	}
	return nil
}

func importedCaseHasNativeRAGFields(row importedCase) bool {
	return row.CodeSearch || row.CodeSearchToolOrder != "" || row.CodeSearchInvocationDedup != "" ||
		row.WorkspacePreload != nil || row.WorkspaceRepresentation != "" ||
		row.RepresentationSchema != "" || row.RepresentationSHA256 != "" ||
		row.EmbeddingConfigSHA256 != "" || row.Result.CodeSearchCalls != 0 ||
		row.Result.CodeSearchErrors != 0 || row.Result.CodeSearchResultBytes != 0 || row.Result.CodeSearchObservationBytes != 0 ||
		len(row.Result.RetrievalTrace) != 0 || row.Result.WorkspaceIndex != nil ||
		row.Result.Embedding != nil || row.Result.EmbeddingCache != nil
}

func nativeRAGProjectionMatches(row importedCase, trace nativeTraceEnvelope) bool {
	preload := false
	if row.WorkspacePreload != nil {
		preload = *row.WorkspacePreload
	}
	return row.CodeSearch == trace.Info.CodeSearch &&
		(row.CodeSearch == (row.WorkspacePreload != nil)) &&
		row.CodeSearchToolOrder == trace.Info.CodeSearchToolOrder &&
		row.CodeSearchInvocationDedup == trace.Info.CodeSearchInvocationDedup &&
		preload == trace.Info.WorkspacePreload &&
		row.WorkspaceRepresentation == trace.Info.WorkspaceRepresentation &&
		row.RepresentationSchema == trace.Info.WorkspaceRepresentationSchema &&
		row.RepresentationSHA256 == trace.Info.RepresentationSHA256 &&
		row.EmbeddingConfigSHA256 == trace.Info.EmbeddingConfigSHA256 &&
		row.Result.CodeSearchCalls == trace.CodeSearchCalls &&
		row.Result.CodeSearchErrors == trace.CodeSearchErrors &&
		row.Result.CodeSearchResultBytes == trace.CodeSearchResultBytes &&
		row.Result.CodeSearchObservationBytes == trace.CodeSearchObservationBytes &&
		equalJSONValue(row.Result.RetrievalTrace, trace.RetrievalTrace) &&
		equalJSONValue(row.Result.WorkspaceIndex, trace.WorkspaceIndex) &&
		equalJSONValue(row.Result.Embedding, trace.Embedding) &&
		equalJSONValue(row.Result.EmbeddingCache, trace.EmbeddingCache)
}

func validateNativeRAGAggregate(label string, rows []importedCase, manifest runnerManifest) error {
	if err := validateNativeRAGManifest(label, manifest); err != nil {
		return err
	}
	var embedding nativeEmbeddingMetrics
	var embeddingCache nativeEmbeddingCacheMetrics
	hasEmbedding := false
	hasEmbeddingCache := false
	parserDependency := ""
	parserRuntimeSHA256 := ""
	retrievalMode, err := nativeManifestRetrievalMode(manifest)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	for _, row := range rows {
		if row.Result.Embedding != nil {
			hasEmbedding = true
			embedding.add(*row.Result.Embedding)
		}
		if row.Result.EmbeddingCache != nil {
			hasEmbeddingCache = true
			embeddingCache.add(*row.Result.EmbeddingCache)
		}
		index := row.Result.WorkspaceIndex
		if index != nil && index.RetrievalMode != retrievalMode {
			return fmt.Errorf(
				"%s workspace_index retrieval_mode %q for case %s does not match manifest mode %q",
				label,
				index.RetrievalMode,
				row.InstanceID,
				retrievalMode,
			)
		}
		if index == nil || (index.Representation != workspaceRepresentationASTCode &&
			index.Representation != workspaceRepresentationASTStructured) {
			continue
		}
		if parserDependency == "" {
			parserDependency = index.ParserDependency
			parserRuntimeSHA256 = index.ParserRuntimeSHA256
			continue
		}
		if index.ParserDependency != parserDependency || index.ParserRuntimeSHA256 != parserRuntimeSHA256 {
			return fmt.Errorf(
				"%s AST parser identity for case %s does not match canonical dependency/runtime digest",
				label,
				row.InstanceID,
			)
		}
	}
	if (manifest.Embedding == nil && hasEmbedding) ||
		(manifest.Embedding != nil && embedding != *manifest.Embedding) {
		return fmt.Errorf("%s embedding telemetry does not match imported case aggregate", label)
	}
	if (manifest.EmbeddingCache == nil && hasEmbeddingCache) ||
		(manifest.EmbeddingCache != nil && embeddingCache != *manifest.EmbeddingCache) {
		return fmt.Errorf("%s embedding_cache telemetry does not match imported case aggregate", label)
	}
	return nil
}

func nativeManifestRetrievalMode(manifest runnerManifest) (string, error) {
	if !manifest.CodeSearch || manifest.EmbeddingConfigSHA256 == "" {
		return "keyword", nil
	}
	value, ok := manifest.EmbeddingConfig["retrieval_mode"].(string)
	if !ok {
		return "", fmt.Errorf("embedding_config.retrieval_mode is not a string")
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hybrid":
		return "hybrid", nil
	case "vector", "dense":
		return "vector", nil
	case "keyword", "bm25":
		return "keyword", nil
	default:
		return "", fmt.Errorf("embedding_config.retrieval_mode %q is unsupported", value)
	}
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	if values == nil {
		return nil
	}
	cloned := make([]json.RawMessage, len(values))
	for index, value := range values {
		cloned[index] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func cloneNativeRetrievalTrace(values []nativeRetrievalTraceEntry) []nativeRetrievalTraceEntry {
	if values == nil {
		return nil
	}
	cloned := make([]nativeRetrievalTraceEntry, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Documents = append([]nativeRetrievalTraceDocument(nil), value.Documents...)
	}
	return cloned
}

func cloneNativeWorkspaceIndex(value *nativeWorkspaceIndexStats) *nativeWorkspaceIndexStats {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.MissingFiles = append([]string(nil), value.MissingFiles...)
	cloned.FallbackReasons = cloneStringIntMap(value.FallbackReasons)
	cloned.NodeTypes = cloneStringIntMap(value.NodeTypes)
	return &cloned
}

func cloneStringIntMap(value map[string]int) map[string]int {
	if value == nil {
		return nil
	}
	cloned := make(map[string]int, len(value))
	for key, count := range value {
		cloned[key] = count
	}
	return cloned
}

func validateNativeWorkspaceIndex(instanceID string, info nativeInfoEnvelope, stats nativeWorkspaceIndexStats) error {
	if stats.Representation != info.WorkspaceRepresentation ||
		stats.RepresentationSchema != info.WorkspaceRepresentationSchema ||
		stats.RepresentationSHA256 != info.RepresentationSHA256 {
		return fmt.Errorf("native trace for %s workspace_index representation identity does not match info", instanceID)
	}
	if stats.InvocationDedup != info.CodeSearchInvocationDedup ||
		stats.InvocationDedup != nativeCodeSearchInvocationDedup {
		return fmt.Errorf(
			"native trace for %s workspace_index invocation_dedup %q does not match info/canonical %q",
			instanceID,
			stats.InvocationDedup,
			nativeCodeSearchInvocationDedup,
		)
	}
	isAST := stats.Representation == workspaceRepresentationASTCode ||
		stats.Representation == workspaceRepresentationASTStructured
	if isAST {
		if strings.TrimSpace(stats.ParserDependency) == "" || strings.TrimSpace(stats.ParserRuntime) == "" {
			return fmt.Errorf("native trace for %s AST workspace_index is missing parser identity", instanceID)
		}
		if stats.ParserRuntimeSHA256 != nativeHashStrings([]string{stats.ParserRuntime}) {
			return fmt.Errorf("native trace for %s AST workspace_index has invalid parser_runtime_sha256", instanceID)
		}
	} else if stats.ParserDependency != "" || stats.ParserRuntime != "" || stats.ParserRuntimeSHA256 != "" {
		return fmt.Errorf("native trace for %s fixed workspace_index unexpectedly carries parser identity", instanceID)
	}
	for _, digest := range []struct {
		name  string
		value string
	}{
		{"eligible_file_set_sha256", stats.EligibleFileSetSHA256},
		{"eligible_content_sha256", stats.EligibleContentSHA256},
		{"indexed_file_set_sha256", stats.IndexedFileSetSHA256},
		{"document_set_sha256", stats.DocumentSetSHA256},
	} {
		if !isSHA256Hex(digest.value) {
			return fmt.Errorf("native trace for %s workspace_index has invalid %s %q", instanceID, digest.name, digest.value)
		}
	}
	for _, value := range []struct {
		name  string
		value int
	}{
		{"documents", stats.Documents},
		{"eligible_files", stats.EligibleFiles},
		{"indexed_files", stats.IndexedFiles},
		{"fallback_documents", stats.FallbackDocuments},
		{"content_chars", stats.ContentChars},
		{"embedding_text_chars", stats.EmbeddingTextChars},
		{"duplicate_documents", stats.DuplicateDocuments},
		{"preloaded_documents", stats.PreloadedDocuments},
		{"preloaded_chars", stats.PreloadedChars},
	} {
		if value.value < 0 {
			return fmt.Errorf("native trace for %s workspace_index has negative %s %d", instanceID, value.name, value.value)
		}
	}
	if stats.DurationMS < 0 || math.IsNaN(stats.FileCoverage) || math.IsInf(stats.FileCoverage, 0) ||
		stats.FileCoverage < 0 || stats.FileCoverage > 1 || math.IsNaN(stats.DuplicateDocumentRate) ||
		math.IsInf(stats.DuplicateDocumentRate, 0) || stats.DuplicateDocumentRate < 0 || stats.DuplicateDocumentRate > 1 {
		return fmt.Errorf("native trace for %s workspace_index has invalid duration or rate", instanceID)
	}
	if stats.PreloadInjected != info.WorkspacePreload {
		return fmt.Errorf(
			"native trace for %s workspace_index preload_injected=%t does not match info.workspace_preload=%t",
			instanceID,
			stats.PreloadInjected,
			info.WorkspacePreload,
		)
	}
	if !stats.PreloadInjected && (stats.PreloadedDocuments != 0 || stats.PreloadedChars != 0) {
		return fmt.Errorf("native trace for %s workspace_index has preload totals with preload_injected=false", instanceID)
	}
	if strings.TrimSpace(stats.RetrievalMode) == "" {
		return fmt.Errorf("native trace for %s workspace_index has empty retrieval_mode", instanceID)
	}
	for reason, count := range stats.FallbackReasons {
		if strings.TrimSpace(reason) == "" || count < 0 {
			return fmt.Errorf("native trace for %s workspace_index has invalid fallback reason count", instanceID)
		}
	}
	for nodeType, count := range stats.NodeTypes {
		if strings.TrimSpace(nodeType) == "" || count < 0 {
			return fmt.Errorf("native trace for %s workspace_index has invalid node type count", instanceID)
		}
	}
	return nil
}

func validateNativeEmbeddingMetrics(label string, metrics nativeEmbeddingMetrics) error {
	values := []int64{
		metrics.Requests, metrics.BatchRequests, metrics.Inputs, metrics.Errors,
		metrics.PromptTokens, metrics.TotalTokens, metrics.DurationMS,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("%s has negative metric %d", label, value)
		}
	}
	if metrics.BatchRequests > metrics.Requests || metrics.Errors > metrics.Requests {
		return fmt.Errorf("%s has inconsistent request totals", label)
	}
	return nil
}

func validateNativeEmbeddingCacheMetrics(label string, metrics nativeEmbeddingCacheMetrics) error {
	values := []int64{
		metrics.Requests, metrics.BatchRequests, metrics.Inputs, metrics.Hits, metrics.Misses,
		metrics.Writes, metrics.Corruptions, metrics.Errors, metrics.BytesRead, metrics.BytesWritten,
		metrics.ReadDurationMS, metrics.WriteDurationMS,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("%s has negative metric %d", label, value)
		}
	}
	if metrics.BatchRequests > metrics.Requests {
		return fmt.Errorf("%s has inconsistent request totals", label)
	}
	return nil
}

func (m *nativeEmbeddingMetrics) add(other nativeEmbeddingMetrics) {
	m.Requests += other.Requests
	m.BatchRequests += other.BatchRequests
	m.Inputs += other.Inputs
	m.Errors += other.Errors
	m.PromptTokens += other.PromptTokens
	m.TotalTokens += other.TotalTokens
	m.DurationMS += other.DurationMS
}

func (m *nativeEmbeddingCacheMetrics) add(other nativeEmbeddingCacheMetrics) {
	m.Requests += other.Requests
	m.BatchRequests += other.BatchRequests
	m.Inputs += other.Inputs
	m.Hits += other.Hits
	m.Misses += other.Misses
	m.Writes += other.Writes
	m.Corruptions += other.Corruptions
	m.Errors += other.Errors
	m.BytesRead += other.BytesRead
	m.BytesWritten += other.BytesWritten
	m.ReadDurationMS += other.ReadDurationMS
	m.WriteDurationMS += other.WriteDurationMS
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalOptionalBool(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneNativeEmbeddingMetrics(value *nativeEmbeddingMetrics) *nativeEmbeddingMetrics {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneNativeEmbeddingCacheMetrics(value *nativeEmbeddingCacheMetrics) *nativeEmbeddingCacheMetrics {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return value
	}
	return cloned
}

func equalJSONValue(left, right any) bool {
	return reflect.DeepEqual(left, right)
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
