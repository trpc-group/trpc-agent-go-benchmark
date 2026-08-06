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
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateNativeRAGManifestPreservesLegacyDisabledDefault(t *testing.T) {
	legacy := runnerManifest{RunnerType: "trpc-agent-go-native"}
	if err := validateNativeRAGManifest("legacy", legacy); err != nil {
		t.Fatalf("validateNativeRAGManifest() legacy error = %v", err)
	}
	nonNative := runnerManifest{RunnerType: "mini-swe-agent-go"}
	if err := validateNativeRAGManifest("mini", nonNative); err != nil {
		t.Fatalf("validateNativeRAGManifest() non-Native legacy error = %v", err)
	}
}

func TestValidateNativeRAGManifestRequiresEnabledIdentity(t *testing.T) {
	valid := validNativeRAGManifest(t, workspaceRepresentationCurrentFixed)
	if err := validateNativeRAGManifest("native", valid); err != nil {
		t.Fatalf("validateNativeRAGManifest() error = %v", err)
	}

	tests := []struct {
		name string
		edit func(*runnerManifest)
		want string
	}{
		{name: "missing preload", edit: func(m *runnerManifest) { m.WorkspacePreload = nil }, want: "missing workspace_preload"},
		{name: "missing tool order", edit: func(m *runnerManifest) { m.CodeSearchToolOrder = "" }, want: "code_search_tool_order"},
		{name: "dedup mismatch", edit: func(m *runnerManifest) { m.CodeSearchInvocationDedup = "enabled" }, want: "code_search_invocation_dedup"},
		{name: "schema mismatch", edit: func(m *runnerManifest) { m.RepresentationSchema += "-other" }, want: "workspace_representation_schema"},
		{name: "digest mismatch", edit: func(m *runnerManifest) { m.RepresentationSHA256 = strings.Repeat("0", 64) }, want: "workspace_representation_sha256"},
		{name: "disabled provenance", edit: func(m *runnerManifest) { m.CodeSearch = false }, want: "code_search=false"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.WorkspacePreload = cloneBool(valid.WorkspacePreload)
			test.edit(&candidate)
			err := validateNativeRAGManifest("native", candidate)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateNativeRAGManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunnerManifestEnabledPreloadPresenceSurvivesJSON(t *testing.T) {
	manifest := validNativeRAGManifest(t, workspaceRepresentationCurrentFixed)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded runnerManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WorkspacePreload == nil || *decoded.WorkspacePreload {
		t.Fatalf("WorkspacePreload = %v, want explicit false", decoded.WorkspacePreload)
	}

	var missing runnerManifest
	if err := json.Unmarshal([]byte(`{
		"runner_type":"trpc-agent-go-native",
		"code_search":true,
		"workspace_representation":"current-fixed",
		"workspace_representation_schema":"`+manifest.RepresentationSchema+`",
		"workspace_representation_sha256":"`+manifest.RepresentationSHA256+`"
	}`), &missing); err != nil {
		t.Fatal(err)
	}
	err = validateNativeRAGManifest("native", missing)
	if err == nil || !strings.Contains(err.Error(), "missing workspace_preload") {
		t.Fatalf("missing workspace_preload error = %v", err)
	}
}

func TestShardRAGIdentityMismatchAndConversion(t *testing.T) {
	manifest := validNativeRAGManifest(t, workspaceRepresentationASTStructured)
	manifest.AgentProtocol = "mini-swe-agent-v2.1-on-trpc-agent-go"
	manifest.UpstreamCommit = strings.Repeat("f", 40)
	manifest.ModelConfig = map[string]string{"MODEL_NAME": "model-a"}
	identity := nativeRunnerIdentity(manifest)
	candidate := cloneShardRunnerIdentity(identity)
	falseValue := false
	candidate.WorkspacePreload = &falseValue
	trueValue := true
	identity.WorkspacePreload = &trueValue
	if mismatch := shardRunnerIdentityMismatch(identity, candidate); !strings.Contains(mismatch, "workspace_preload") {
		t.Fatalf("shardRunnerIdentityMismatch() = %q, want workspace_preload", mismatch)
	}

	converted := runnerManifestForNativeShardIdentity(candidate)
	if !converted.CodeSearch || converted.WorkspacePreload == nil || *converted.WorkspacePreload ||
		converted.CodeSearchToolOrder != nativeCodeSearchToolOrder ||
		converted.CodeSearchInvocationDedup != nativeCodeSearchInvocationDedup ||
		converted.WorkspaceRepresentation != candidate.WorkspaceRepresentation ||
		converted.RepresentationSchema != candidate.RepresentationSchema ||
		converted.RepresentationSHA256 != candidate.RepresentationSHA256 {
		t.Fatalf("runnerManifestForNativeShardIdentity() = %+v", converted)
	}
}

func TestParseNativeTraceValidatesWorkspaceRetrievalTelemetry(t *testing.T) {
	artifact := enabledNativeArtifact(t, workspaceRepresentationCurrentFixed)
	trace, err := parseNativeTraceEnvelope(marshalJSONObject(t, artifact), "case-a")
	if err != nil {
		t.Fatalf("parseNativeTraceEnvelope() error = %v", err)
	}
	if !trace.Info.CodeSearch || trace.Info.WorkspacePreload || trace.WorkspaceIndex == nil {
		t.Fatalf("trace RAG identity = %+v", trace)
	}

	missingPreload := cloneJSONObject(t, artifact)
	delete(missingPreload["info"].(map[string]any), "workspace_preload")
	if _, err := parseNativeTraceEnvelope(marshalJSONObject(t, missingPreload), "case-a"); err == nil ||
		!strings.Contains(err.Error(), "info.workspace_preload") {
		t.Fatalf("missing preload error = %v", err)
	}

	disabledTelemetry := cloneJSONObject(t, validNativeArtifact())
	disabledTelemetry["code_search_calls"] = 1
	if _, err := parseNativeTraceEnvelope(marshalJSONObject(t, disabledTelemetry), "case-a"); err == nil ||
		!strings.Contains(err.Error(), "info.code_search=false") {
		t.Fatalf("disabled telemetry error = %v", err)
	}

	dedupMismatch := cloneJSONObject(t, artifact)
	dedupMismatch["workspace_index"].(map[string]any)["invocation_dedup"] = "enabled"
	if _, err := parseNativeTraceEnvelope(marshalJSONObject(t, dedupMismatch), "case-a"); err == nil ||
		!strings.Contains(err.Error(), "invocation_dedup") {
		t.Fatalf("workspace index dedup mismatch error = %v", err)
	}
}

func TestParseNativeTraceBindsSuccessAndErrorRetrievalEvents(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		artifact := enabledNativeArtifact(t, workspaceRepresentationCurrentFixed)
		raw := json.RawMessage(`{"documents":[]}`)
		arguments := []byte(`{"query":"needle"}`)
		observation := []byte("rendered result")
		artifact["code_search_calls"] = 1
		artifact["code_search_result_bytes"] = len(raw)
		artifact["code_search_observation_bytes"] = len(observation)
		artifact["code_search_raw_results"] = []json.RawMessage{raw}
		artifact["retrieval_trace"] = []map[string]any{{
			"call": 1, "tool_call_id": "call-1", "query": "needle", "status": "success",
			"arguments_sha256": digestBytes(arguments), "result_sha256": digestBytes(raw),
			"observation_sha256": digestBytes(observation), "result_bytes": len(raw),
			"observation_bytes": len(observation), "documents": []any{},
		}}
		trace, err := parseNativeTraceEnvelope(marshalJSONObject(t, artifact), "case-a")
		if err != nil {
			t.Fatal(err)
		}
		if trace.CodeSearchCalls != 1 || trace.CodeSearchErrors != 0 || len(trace.RetrievalTrace) != 1 {
			t.Fatalf("success telemetry = %+v", trace)
		}
	})

	t.Run("error", func(t *testing.T) {
		artifact := enabledNativeArtifact(t, workspaceRepresentationCurrentFixed)
		message := "no deterministic result"
		raw := json.RawMessage(`{"error":"no deterministic result"}`)
		arguments := []byte(`{"query":"missing"}`)
		artifact["code_search_calls"] = 1
		artifact["code_search_errors"] = 1
		artifact["code_search_result_bytes"] = len(raw)
		artifact["code_search_raw_results"] = []json.RawMessage{raw}
		artifact["retrieval_trace"] = []map[string]any{{
			"call": 1, "tool_call_id": "call-1", "query": "missing", "status": "error",
			"error": message, "error_sha256": digestBytes([]byte(message)),
			"arguments_sha256": digestBytes(arguments), "result_sha256": digestBytes(raw),
			"result_bytes": len(raw), "documents": []any{},
		}}
		trace, err := parseNativeTraceEnvelope(marshalJSONObject(t, artifact), "case-a")
		if err != nil {
			t.Fatal(err)
		}
		if trace.CodeSearchErrors != 1 || trace.RetrievalTrace[0].Status != "error" {
			t.Fatalf("error telemetry = %+v", trace)
		}

		artifact["code_search_errors"] = 0
		if _, err := parseNativeTraceEnvelope(marshalJSONObject(t, artifact), "case-a"); err == nil ||
			!strings.Contains(err.Error(), "code_search_errors") {
			t.Fatalf("error count mismatch = %v", err)
		}
	})
}

func TestValidateNativeRAGAggregateRejectsMixedASTRuntime(t *testing.T) {
	manifest := validNativeRAGManifest(t, workspaceRepresentationASTCode)
	rows := []importedCase{
		{InstanceID: "case-a", Result: targetResult{WorkspaceIndex: validWorkspaceIndex(t, workspaceRepresentationASTCode, "Python 3.11.1")}},
		{InstanceID: "case-b", Result: targetResult{WorkspaceIndex: validWorkspaceIndex(t, workspaceRepresentationASTCode, "Python 3.12.1")}},
	}
	err := validateNativeRAGAggregate("native", rows, manifest)
	if err == nil || !strings.Contains(err.Error(), "parser identity") {
		t.Fatalf("mixed parser error = %v", err)
	}
}

func TestValidateNativeRAGAggregateBindsEmbeddingModeAndCacheTelemetry(t *testing.T) {
	manifest := validNativeRAGManifest(t, workspaceRepresentationCurrentFixed)
	manifest.EmbeddingConfigSHA256 = strings.Repeat("e", 64)
	manifest.EmbeddingConfig = validRedactedEmbeddingConfig()
	manifest.Embedding = &nativeEmbeddingMetrics{}
	manifest.EmbeddingCache = &nativeEmbeddingCacheMetrics{}
	index := validWorkspaceIndex(t, workspaceRepresentationCurrentFixed, "")
	index.RetrievalMode = "hybrid"
	rows := []importedCase{{
		InstanceID: "case-a",
		Result: targetResult{
			WorkspaceIndex: index,
			Embedding:      &nativeEmbeddingMetrics{},
			EmbeddingCache: &nativeEmbeddingCacheMetrics{},
		},
	}}
	if err := validateNativeRAGAggregate("native", rows, manifest); err != nil {
		t.Fatalf("validateNativeRAGAggregate() error = %v", err)
	}
	rows[0].Result.WorkspaceIndex.RetrievalMode = "vector"
	if err := validateNativeRAGAggregate("native", rows, manifest); err == nil ||
		!strings.Contains(err.Error(), "retrieval_mode") {
		t.Fatalf("retrieval mode mismatch error = %v", err)
	}
}

func validNativeRAGManifest(t *testing.T, representation string) runnerManifest {
	t.Helper()
	schema, digest, err := nativeWorkspaceRepresentationIdentity(representation)
	if err != nil {
		t.Fatal(err)
	}
	preload := false
	return runnerManifest{
		RunnerType:                "trpc-agent-go-native",
		CodeSearch:                true,
		CodeSearchToolOrder:       nativeCodeSearchToolOrder,
		CodeSearchInvocationDedup: nativeCodeSearchInvocationDedup,
		WorkspacePreload:          &preload,
		WorkspaceRepresentation:   representation,
		RepresentationSchema:      schema,
		RepresentationSHA256:      digest,
	}
}

func enabledNativeArtifact(t *testing.T, representation string) map[string]any {
	t.Helper()
	artifact := cloneJSONObject(t, validNativeArtifact())
	schema, digest, err := nativeWorkspaceRepresentationIdentity(representation)
	if err != nil {
		t.Fatal(err)
	}
	info := artifact["info"].(map[string]any)
	info["code_search"] = true
	info["code_search_tool_order"] = nativeCodeSearchToolOrder
	info["code_search_invocation_dedup"] = nativeCodeSearchInvocationDedup
	info["workspace_preload"] = false
	info["workspace_representation"] = representation
	info["workspace_representation_sha256"] = digest
	index := validWorkspaceIndex(t, representation, "Python 3.11.1")
	index.RepresentationSchema = schema
	artifact["workspace_index"] = index
	return artifact
}

func validWorkspaceIndex(t *testing.T, representation, runtime string) *nativeWorkspaceIndexStats {
	t.Helper()
	schema, digest, err := nativeWorkspaceRepresentationIdentity(representation)
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest := nativeHashStrings(nil)
	index := &nativeWorkspaceIndexStats{
		Representation:        representation,
		RepresentationSchema:  schema,
		RepresentationSHA256:  digest,
		EligibleFileSetSHA256: emptyDigest,
		EligibleContentSHA256: emptyDigest,
		IndexedFileSetSHA256:  emptyDigest,
		DocumentSetSHA256:     emptyDigest,
		RetrievalMode:         "keyword",
		InvocationDedup:       nativeCodeSearchInvocationDedup,
	}
	if representation == workspaceRepresentationASTCode || representation == workspaceRepresentationASTStructured {
		index.ParserDependency = "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/python@v0.0.0-20260728070417-4237accb70cb"
		index.ParserRuntime = runtime
		index.ParserRuntimeSHA256 = nativeHashStrings([]string{runtime})
	}
	return index
}

func validRedactedEmbeddingConfig() map[string]any {
	return map[string]any{
		"provider":               "openai",
		"endpoint_configured":    true,
		"credentials_configured": true,
		"model":                  "embedding-model",
		"dimensions":             1024,
		"batch_size":             64,
		"concurrency":            4,
		"retrieval_mode":         "hybrid",
		"max_results":            4,
		"max_chars":              6000,
		"cache": map[string]any{
			"enabled":              true,
			"directory_configured": true,
			"model_fingerprint":    "fingerprint",
			"access":               "readwrite",
		},
	}
}
