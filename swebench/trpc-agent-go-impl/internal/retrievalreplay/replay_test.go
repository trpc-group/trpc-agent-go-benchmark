//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndReplayAreDeterministic(t *testing.T) {
	fixture := newReplayFixture(t)
	loaded, err := Load(fixture.bundlePath, fixture.runDir)
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{
		descriptor: fixture.bundle.Engine,
		identity:   indexIdentity(fixture.bundle.Cases[0]),
		documents:  fixture.documents,
	}
	first, err := Replay(context.Background(), loaded, engine)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Replay(context.Background(), loaded, engine)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("reports are not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	if first.Status != "completed" || first.Summary != (Summary{Cases: 1, Queries: 1, Matches: 1}) {
		t.Fatalf("unexpected report: %+v", first)
	}
	if len(first.Cases) != 1 || !first.Cases[0].Match || len(first.Cases[0].Queries) != 1 {
		t.Fatalf("unexpected case report: %+v", first.Cases)
	}
	if engine.builds != 2 || engine.searches != 2 || engine.closes != 2 {
		t.Fatalf("engine lifecycle builds=%d searches=%d closes=%d", engine.builds, engine.searches, engine.closes)
	}
}

func TestReplayReportsMismatchWithoutTreatingItAsExecutionFailure(t *testing.T) {
	fixture := newReplayFixture(t)
	loaded, err := Load(fixture.bundlePath, fixture.runDir)
	if err != nil {
		t.Fatal(err)
	}
	documents := append([]Document(nil), fixture.documents...)
	documents[0].Score = 0.5
	report, err := Replay(context.Background(), loaded, &fakeEngine{
		descriptor: fixture.bundle.Engine,
		identity:   indexIdentity(fixture.bundle.Cases[0]), documents: documents,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "mismatch" || report.Summary.Mismatches != 1 || report.Cases[0].Match {
		t.Fatalf("unexpected mismatch report: %+v", report)
	}
}

func TestReplayReproducesRecordedSearchError(t *testing.T) {
	fixture := newReplayFixture(t)
	message := "search failed: no relevant documents found"
	nativePath := filepath.Join(fixture.runDir, "case-a", "case-a.native.json")
	var native map[string]any
	data, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	errorPayload, err := json.Marshal(map[string]any{"error": message})
	if err != nil {
		t.Fatal(err)
	}
	native["code_search_errors"] = 1
	native["code_search_result_bytes"] = len(errorPayload)
	native["code_search_raw_results"] = []json.RawMessage{errorPayload}
	trace := native["retrieval_trace"].([]any)[0].(map[string]any)
	trace["status"] = "error"
	trace["error"] = message
	trace["error_sha256"] = digestBytes([]byte(message))
	trace["result_sha256"] = digestBytes(errorPayload)
	trace["result_bytes"] = len(errorPayload)
	trace["documents"] = []any{}
	nativeData := writeJSONFile(t, nativePath, native)

	spec := fixture.bundle.Cases[0]
	spec.NativeArtifactSHA256 = digestBytes(nativeData)
	spec.InputSHA256, err = InputSHA256(
		fixture.bundle.NativeRun.ManifestSHA256,
		fixture.bundle.Engine,
		fixture.bundle.EngineConfig,
		spec,
	)
	if err != nil {
		t.Fatal(err)
	}
	errorSHA := digestBytes([]byte(message))
	resultSHA, err := OutcomeSHA256(
		fixture.queries.Queries[0].QuerySHA256,
		"no_hit",
		errorSHA,
		[]RankedDocument{},
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := ExpectedResultSet{
		SchemaVersion: ResultSetSchemaVersion, Kind: ResultSetKind,
		InstanceID: "case-a", InputSHA256: spec.InputSHA256,
		Results: []ExpectedQueryResult{{
			Ordinal: 1, QuerySHA256: fixture.queries.Queries[0].QuerySHA256,
			Status: "no_hit", ErrorSHA256: errorSHA,
			ResultSHA256: resultSHA, Documents: []RankedDocument{},
		}},
	}
	expectedData := writeJSONFile(t, fixture.expectedPath, expected)
	spec.ExpectedResults.SHA256 = digestBytes(expectedData)
	fixture.bundle.Cases[0] = spec
	writeJSONFile(t, fixture.bundlePath, fixture.bundle)

	loaded, err := Load(fixture.bundlePath, fixture.runDir)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Replay(context.Background(), loaded, &fakeEngine{
		descriptor: fixture.bundle.Engine,
		identity:   indexIdentity(spec), searchErr: errors.New(message),
	})
	if err != nil {
		t.Fatal(err)
	}
	comparison := report.Cases[0].Queries[0]
	if report.Status != "completed" || !comparison.Match ||
		comparison.ExpectedStatus != "no_hit" || comparison.ActualStatus != "no_hit" ||
		comparison.ActualErrorSHA256 != errorSHA {
		t.Fatalf("unexpected error replay report: %+v", report)
	}

	report, err = Replay(context.Background(), loaded, &fakeEngine{
		descriptor: fixture.bundle.Engine,
		identity:   indexIdentity(spec), searchErr: errors.New("different failure"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "mismatch" || report.Cases[0].Queries[0].Match {
		t.Fatalf("unexpected mismatched error report: %+v", report)
	}
}

func TestReplayFailsClosedOnEngineAndIndexIdentity(t *testing.T) {
	fixture := newReplayFixture(t)
	loaded, err := Load(fixture.bundlePath, fixture.runDir)
	if err != nil {
		t.Fatal(err)
	}
	wrongEngine := fixture.bundle.Engine
	wrongEngine.Version = "different"
	_, err = Replay(context.Background(), loaded, &fakeEngine{descriptor: wrongEngine})
	if err == nil || !strings.Contains(err.Error(), "runtime engine identity") {
		t.Fatalf("engine identity error = %v", err)
	}
	identity := indexIdentity(fixture.bundle.Cases[0])
	identity.DocumentSetSHA256 = strings.Repeat("f", 64)
	_, err = Replay(context.Background(), loaded, &fakeEngine{
		descriptor: fixture.bundle.Engine, identity: identity, documents: fixture.documents,
	})
	if err == nil || !strings.Contains(err.Error(), "rebuilt index identity") {
		t.Fatalf("index identity error = %v", err)
	}
}

func TestLoadRejectsArtifactTamperingAndOpenRechecksAfterLoad(t *testing.T) {
	t.Run("before load", func(t *testing.T) {
		fixture := newReplayFixture(t)
		if err := os.WriteFile(fixture.queryPath, []byte(`{"tampered":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(fixture.bundlePath, fixture.runDir)
		if err == nil || !strings.Contains(err.Error(), "sha256=") {
			t.Fatalf("tamper error = %v", err)
		}
	})

	t.Run("after load", func(t *testing.T) {
		fixture := newReplayFixture(t)
		loaded, err := Load(fixture.bundlePath, fixture.runDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.corpusPath, []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loaded.Cases[0].Corpus.Open(); err == nil || !strings.Contains(err.Error(), "sha256=") {
			t.Fatalf("post-load tamper error = %v", err)
		}
	})
}

func TestLoadRejectsUnsafePortablePaths(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "absolute", path: "/server/private/corpus.tar"},
		{name: "traversal", path: "../corpus.tar"},
		{name: "backslash", path: `artifacts\corpus.tar`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReplayFixture(t)
			fixture.bundle.Cases[0].Corpus.Path = test.path
			writeJSONFile(t, fixture.bundlePath, fixture.bundle)
			_, err := Load(fixture.bundlePath, fixture.runDir)
			if err == nil || !strings.Contains(err.Error(), "artifact path") {
				t.Fatalf("unsafe path error = %v", err)
			}
		})
	}
}

func TestLoadRejectsSymlinkedArtifact(t *testing.T) {
	fixture := newReplayFixture(t)
	target := filepath.Join(filepath.Dir(fixture.corpusPath), "target.tar")
	data, err := os.ReadFile(fixture.corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.corpusPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fixture.corpusPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err = Load(fixture.bundlePath, fixture.runDir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestLoadRejectsUnknownGoldPatchField(t *testing.T) {
	fixture := newReplayFixture(t)
	data, err := os.ReadFile(fixture.bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["gold_patch"] = "forbidden"
	writeJSONFile(t, fixture.bundlePath, raw)
	_, err = Load(fixture.bundlePath, fixture.runDir)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("gold patch field error = %v", err)
	}
}

func TestLoadRejectsDuplicateJSONKey(t *testing.T) {
	fixture := newReplayFixture(t)
	if err := os.WriteFile(
		fixture.bundlePath,
		[]byte(`{"schema_version":1,"schema_version":1}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	_, err := Load(fixture.bundlePath, fixture.runDir)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate key error = %v", err)
	}
}

func TestLoadRejectsQueryArtifactThatDiffersFromNativeTrajectory(t *testing.T) {
	fixture := newReplayFixture(t)
	query := fixture.queries
	query.Queries[0].Query = "different recorded query"
	query.Queries[0].QuerySHA256 = digestBytes([]byte(query.Queries[0].Query))
	query.Queries[0].Arguments = json.RawMessage(`{"query":"different recorded query"}`)
	query.Queries[0].ArgumentsSHA256 = digestBytes(query.Queries[0].Arguments)
	query.Queries[0].NativeArguments = string(query.Queries[0].Arguments)
	query.Queries[0].NativeArgumentsSHA256 = digestBytes(query.Queries[0].Arguments)
	queryData := writeJSONFile(t, fixture.queryPath, query)
	fixture.bundle.Cases[0].Queries.SHA256 = digestBytes(queryData)

	spec := fixture.bundle.Cases[0]
	spec.InputSHA256 = ""
	inputSHA, err := InputSHA256(
		fixture.bundle.NativeRun.ManifestSHA256,
		fixture.bundle.Engine,
		fixture.bundle.EngineConfig,
		spec,
	)
	if err != nil {
		t.Fatal(err)
	}
	spec.InputSHA256 = inputSHA
	expected := fixture.expected
	expected.InputSHA256 = inputSHA
	expected.Results[0].QuerySHA256 = query.Queries[0].QuerySHA256
	expected.Results[0].ResultSHA256, err = ResultSHA256(
		query.Queries[0].QuerySHA256,
		expected.Results[0].Documents,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedData := writeJSONFile(t, fixture.expectedPath, expected)
	spec.ExpectedResults.SHA256 = digestBytes(expectedData)
	fixture.bundle.Cases[0] = spec
	writeJSONFile(t, fixture.bundlePath, fixture.bundle)

	_, err = Load(fixture.bundlePath, fixture.runDir)
	if err == nil || !strings.Contains(err.Error(), "does not match Native trajectory") {
		t.Fatalf("query mismatch error = %v", err)
	}
}

func TestRunWithEngineRequiresEngineAndWritesMismatchReport(t *testing.T) {
	fixture := newReplayFixture(t)
	args := []string{
		"retrieval-replay", "replay", "--run-dir", fixture.runDir,
		"--bundle", fixture.bundlePath, "--output", fixture.outputPath,
	}
	if err := RunWithEngine(args, nil); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("RunWithEngine nil error = %v", err)
	}
	if _, err := os.Stat(fixture.outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone Run unexpectedly wrote output: %v", err)
	}
	documents := append([]Document(nil), fixture.documents...)
	documents[0].ID = "different-doc"
	err := RunWithEngine(args, &fakeEngine{
		descriptor: fixture.bundle.Engine,
		identity:   indexIdentity(fixture.bundle.Cases[0]), documents: documents,
	})
	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("RunWithEngine error = %v", err)
	}
	data, err := os.ReadFile(fixture.outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "mismatch" || report.Summary.Mismatches != 1 {
		t.Fatalf("written report = %+v", report)
	}
}

func TestRankEngineDocumentsRejectsNonFiniteScore(t *testing.T) {
	_, err := rankEngineDocuments([]Document{{Score: math.NaN()}}, 1)
	if err == nil || !strings.Contains(err.Error(), "non-finite") {
		t.Fatalf("NaN error = %v", err)
	}
}

func TestPublicReplayRejectsInvocationDedupAndDenseFallback(t *testing.T) {
	fixture := newReplayFixture(t)
	spec := fixture.bundle.Cases[0]
	spec.Retrieval.InvocationDedup = true
	if err := validateCaseSpec(spec); err == nil || !strings.Contains(err.Error(), "invocation_dedup=true") {
		t.Fatalf("dedup validation error = %v", err)
	}

	configData, err := defaultEngineConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(fixture.root, "offline-engine-config.json")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	provenance := fixture.bundle.Cases[0].Retrieval
	provenance.RetrievalMode = "vector"
	_, err = NewOfflineEngine().Build(context.Background(), BuildInput{
		InstanceID: "case-a", Repository: "fixture/repo",
		Corpus: artifactHandle(filepath.Dir(fixture.corpusPath), ArtifactRef{
			Path: filepath.Base(fixture.corpusPath), SHA256: digestBytes([]byte("portable deterministic workspace archive")),
		}),
		EngineConfig: artifactHandle(filepath.Dir(configPath), ArtifactRef{
			Path: filepath.Base(configPath), SHA256: digestBytes(configData),
		}),
		Provenance: provenance,
	})
	if err == nil || !strings.Contains(err.Error(), "portable all-hit embedding cache") {
		t.Fatalf("dense replay error = %v", err)
	}
}

type replayFixture struct {
	root         string
	bundlePath   string
	runDir       string
	queryPath    string
	expectedPath string
	corpusPath   string
	outputPath   string
	bundle       Bundle
	queries      QuerySet
	expected     ExpectedResultSet
	documents    []Document
}

func newReplayFixture(t *testing.T) replayFixture {
	t.Helper()
	root := t.TempDir()
	bundleRoot := filepath.Join(root, "bundle")
	runDir := filepath.Join(root, "native-run")
	artifactsDir := filepath.Join(bundleRoot, "artifacts", "case-a")
	caseDir := filepath.Join(runDir, "case-a")
	for _, directory := range []string{artifactsDir, caseDir, filepath.Join(root, "output")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	corpusPath := filepath.Join(artifactsDir, "workspace.tar")
	corpusData := []byte("portable deterministic workspace archive")
	if err := os.WriteFile(corpusPath, corpusData, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(bundleRoot, "artifacts", "engine-config.json")
	configData := []byte(`{"engine":"fixture","version":1}`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	arguments := json.RawMessage(`{"query":"find parser"}`)
	query := RecordedQuery{
		Ordinal: 1, ToolCallID: "call-1", ToolName: "code_search", Query: "find parser",
		QuerySHA256: digestBytes([]byte("find parser")), Arguments: arguments,
		ArgumentsSHA256: digestBytes(arguments), NativeArguments: string(arguments),
		NativeArgumentsSHA256: digestBytes(arguments),
	}
	queries := QuerySet{
		SchemaVersion: QuerySetSchemaVersion, Kind: QuerySetKind,
		InstanceID: "case-a", EmittedCodeSearchCalls: 1,
		SkippedEmittedCodeSearchCalls: 0, Queries: []RecordedQuery{query},
	}
	queryPath := filepath.Join(artifactsDir, "queries.json")
	queryData := writeJSONFile(t, queryPath, queries)

	responses := []map[string]any{{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id":       "call-1",
					"function": map[string]any{"name": "code_search", "arguments": string(arguments)},
				}},
			},
		}},
		"done": true, "is_partial": false,
	}}
	responsesPath := filepath.Join(caseDir, "case-a.responses.json")
	responsesData := writeJSONFile(t, responsesPath, responses)
	responsesSHA := digestBytes(responsesData)

	hashes := func(character string) string { return strings.Repeat(character, 64) }
	representationSchema := "fixture-workspace-schema-v1"
	representationSHA := hashes("e")
	parserDependency := "fixture/python-reader@v1"
	parserRuntimeSHA := hashes("f")
	manifest := map[string]any{
		"run_id": "native-run-1", "runner_type": "trpc-agent-go-native",
		"framework_module":  "trpc.group/trpc-go/trpc-agent-go",
		"framework_version": "v1.2.3", "agent_protocol": "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1",
		"upstream_commit": strings.Repeat("a", 40), "observation_codec": "xml",
		"source_revision": strings.Repeat("b", 40), "source_modified": false,
		"binary_sha256": hashes("1"), "cases_sha256": hashes("2"),
		"model_config_sha256": hashes("3"), "environment_config_sha256": hashes("4"),
		"selected_instances_sha256": hashes("5"), "clean_room": true,
		"tool_loop_warning": true, "clean_room_policy_sha256": hashes("6"),
		"image_set_sha256": hashes("7"), "command_timeout": "1m0s", "case_timeout": "4h0m0s",
		"case_count": 1, "completed_count": 1, "prediction_count": 1,
		"workers": 1, "status": "completed", "code_search": true,
		"workspace_preload": false, "workspace_representation": "ast-structured",
		"workspace_representation_schema": representationSchema,
		"workspace_representation_sha256": representationSHA,
	}
	manifestPath := filepath.Join(runDir, NativeManifestFilename)
	manifestData := writeJSONFile(t, manifestPath, manifest)

	info := map[string]any{
		"run_id": "native-run-1", "observation_codec": "xml",
		"source_revision": strings.Repeat("b", 40), "source_modified": false,
		"binary_sha256": hashes("1"), "model_config_sha256": hashes("3"),
		"environment_config_sha256": hashes("4"), "cases_sha256": hashes("2"),
		"command_timeout": "1m0s", "case_timeout": "4h0m0s",
		"selected_instances_sha256": hashes("5"), "clean_room": true,
		"tool_loop_warning": true, "clean_room_policy_sha256": hashes("6"),
		"code_search": true, "workspace_preload": false,
		"workspace_representation":        "ast-structured",
		"workspace_representation_sha256": representationSHA,
		"image_set_sha256":                hashes("7"), "repo": "fixture/repo", "base_commit": strings.Repeat("c", 40),
		"verified_base_commit": strings.Repeat("c", 40), "workers": 1, "exit_status": "Submitted",
	}
	content := "def parse():\n    pass\n"
	metadataSHA := RetrievalMetadataSHA256("pkg/a.py", "1-2", "parse")
	rawResult, err := json.Marshal(map[string]any{
		"documents": []any{map[string]any{
			"id": "doc-a", "text": content,
			"metadata": map[string]any{
				"trpc_ast_file_path": "pkg/a.py", "trpc_ast_line_start": 1,
				"trpc_ast_line_end": 2, "trpc_ast_full_name": "parse",
			},
			"score": 0.75,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	native := map[string]any{
		"instance_id": "case-a", "info": info, "model_patch": "candidate patch is ignored",
		"duration_ms": 100, "llm_calls": 1, "tool_calls": 1,
		"tool_loop_warning_count": 0, "first_tool_loop_warning_llm_call": nil,
		"tool_loop_warning_llm_calls": []int{}, "usage": map[string]any{},
		"events": responses, "response_count": 1, "responses_sha256": responsesSHA,
		"code_search_calls": 1, "code_search_errors": 0,
		"code_search_result_bytes": len(rawResult),
		"code_search_raw_results":  []json.RawMessage{rawResult},
		"retrieval_trace": []any{map[string]any{
			"call": 1, "tool_call_id": "call-1", "query": "find parser",
			"status":           "success",
			"arguments_sha256": digestBytes(arguments), "result_sha256": digestBytes(rawResult),
			"result_bytes": len(rawResult),
			"documents": []any{map[string]any{
				"id": "doc-a", "path": "pkg/a.py", "lines": "1-2", "symbol": "parse",
				"score": 0.75, "content_sha256": digestBytes([]byte(content)),
			}},
		}},
		"workspace_index": map[string]any{
			"representation": "ast-structured", "representation_schema": representationSchema,
			"representation_sha256": representationSHA,
			"parser_dependency":     parserDependency, "parser_runtime_sha256": parserRuntimeSHA,
			"eligible_file_set_sha256": hashes("b"), "eligible_content_sha256": hashes("c"),
			"document_set_sha256": hashes("d"), "retrieval_mode": "keyword",
		},
	}
	nativePath := filepath.Join(caseDir, "case-a.native.json")
	nativeData := writeJSONFile(t, nativePath, native)

	documents := []Document{{
		ID: "doc-a", SourcePath: "pkg/a.py", ContentSHA256: digestBytes([]byte(content)),
		MetadataSHA256: metadataSHA, Score: 0.75,
	}}
	ranked, err := rankEngineDocuments(documents, 6)
	if err != nil {
		t.Fatal(err)
	}
	resultSHA, err := ResultSHA256(query.QuerySHA256, ranked)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := EngineDescriptor{
		Name: "fixture-offline-engine", Version: "1",
		ImplementationSHA256: hashes("a"), OfflineOnly: true,
	}
	spec := CaseSpec{
		InstanceID: "case-a", NativeArtifactSHA256: digestBytes(nativeData),
		ResponsesArtifactSHA256: responsesSHA,
		Corpus:                  ArtifactRef{Path: "artifacts/case-a/workspace.tar", SHA256: digestBytes(corpusData)},
		Queries:                 ArtifactRef{Path: "artifacts/case-a/queries.json", SHA256: digestBytes(queryData)},
		ExpectedResults:         ArtifactRef{Path: "artifacts/case-a/expected.json", SHA256: hashes("0")},
		Retrieval: RetrievalProvenance{
			CorpusFormat: "workspace-tar-v1", CorpusSHA256: digestBytes(corpusData),
			EligibleFileSetSHA256: hashes("b"), EligibleContentSHA256: hashes("c"),
			DocumentSetSHA256: hashes("d"), WorkspaceRepresentation: "ast-structured",
			WorkspaceRepresentationSchema: representationSchema,
			WorkspaceRepresentationSHA256: representationSHA, RetrievalMode: "keyword",
			ParserDependency: parserDependency, ParserRuntimeSHA256: parserRuntimeSHA,
			MaxResults: 6, InvocationDedup: false,
		},
	}
	engineConfig := ArtifactRef{Path: "artifacts/engine-config.json", SHA256: digestBytes(configData)}
	spec.InputSHA256, err = InputSHA256(digestBytes(manifestData), descriptor, engineConfig, spec)
	if err != nil {
		t.Fatal(err)
	}
	expected := ExpectedResultSet{
		SchemaVersion: ResultSetSchemaVersion, Kind: ResultSetKind,
		InstanceID: "case-a", InputSHA256: spec.InputSHA256,
		Results: []ExpectedQueryResult{{
			Ordinal: 1, QuerySHA256: query.QuerySHA256,
			Status: "success", ResultSHA256: resultSHA, Documents: ranked,
		}},
	}
	expectedPath := filepath.Join(artifactsDir, "expected.json")
	expectedData := writeJSONFile(t, expectedPath, expected)
	spec.ExpectedResults.SHA256 = digestBytes(expectedData)
	caseSetSHA, err := CaseSetSHA256([]string{"case-a"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := Bundle{
		SchemaVersion: BundleSchemaVersion, Kind: BundleKind,
		NativeRun: NativeRunBinding{RunID: "native-run-1", ManifestSHA256: digestBytes(manifestData)},
		Engine:    descriptor, EngineConfig: engineConfig, CaseSetSHA256: caseSetSHA,
		Cases: []CaseSpec{spec},
	}
	bundlePath := filepath.Join(bundleRoot, "replay-bundle.json")
	writeJSONFile(t, bundlePath, bundle)
	return replayFixture{
		root: root, bundlePath: bundlePath, runDir: runDir,
		queryPath: queryPath, expectedPath: expectedPath, corpusPath: corpusPath,
		outputPath: filepath.Join(root, "output", "report.json"), bundle: bundle,
		queries: queries, expected: expected, documents: documents,
	}
}

func writeJSONFile(t *testing.T, path string, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func indexIdentity(spec CaseSpec) IndexIdentity {
	embeddingSHA, cacheSHA := "", ""
	if spec.Retrieval.Embedding != nil {
		embeddingSHA = spec.Retrieval.Embedding.IdentitySHA256
	}
	if spec.EmbeddingCache != nil {
		cacheSHA = spec.EmbeddingCache.SHA256
	}
	return IndexIdentity{
		CorpusSHA256:                  spec.Retrieval.CorpusSHA256,
		EligibleFileSetSHA256:         spec.Retrieval.EligibleFileSetSHA256,
		EligibleContentSHA256:         spec.Retrieval.EligibleContentSHA256,
		DocumentSetSHA256:             spec.Retrieval.DocumentSetSHA256,
		WorkspaceRepresentation:       spec.Retrieval.WorkspaceRepresentation,
		WorkspaceRepresentationSchema: spec.Retrieval.WorkspaceRepresentationSchema,
		WorkspaceRepresentationSHA256: spec.Retrieval.WorkspaceRepresentationSHA256,
		ParserDependency:              spec.Retrieval.ParserDependency,
		ParserRuntimeSHA256:           spec.Retrieval.ParserRuntimeSHA256,
		RetrievalMode:                 spec.Retrieval.RetrievalMode, MaxResults: spec.Retrieval.MaxResults,
		InvocationDedup:         spec.Retrieval.InvocationDedup,
		EmbeddingIdentitySHA256: embeddingSHA, EmbeddingCacheSHA256: cacheSHA,
	}
}

type fakeEngine struct {
	descriptor EngineDescriptor
	identity   IndexIdentity
	documents  []Document
	searchErr  error
	builds     int
	searches   int
	closes     int
}

func (e *fakeEngine) Descriptor() EngineDescriptor { return e.descriptor }

func (e *fakeEngine) Build(_ context.Context, input BuildInput) (Index, error) {
	e.builds++
	if input.InstanceID == "" || input.Repository == "" {
		return nil, errors.New("missing build identity")
	}
	if _, err := input.Corpus.ReadAll(1 << 20); err != nil {
		return nil, err
	}
	if _, err := input.EngineConfig.ReadAll(1 << 20); err != nil {
		return nil, err
	}
	return &fakeIndex{
		owner: e, identity: e.identity, documents: e.documents,
		searchErr: e.searchErr,
	}, nil
}

type fakeIndex struct {
	owner     *fakeEngine
	identity  IndexIdentity
	documents []Document
	searchErr error
}

func (i *fakeIndex) Identity() IndexIdentity { return i.identity }

func (i *fakeIndex) Search(_ context.Context, request SearchRequest) ([]Document, error) {
	i.owner.searches++
	if request.Ordinal != 1 || request.Query != "find parser" ||
		request.QuerySHA256 != digestBytes([]byte(request.Query)) || request.MaxResults != 6 {
		return nil, fmt.Errorf("unexpected request: %+v", request)
	}
	if i.searchErr != nil {
		return nil, i.searchErr
	}
	return append([]Document(nil), i.documents...), nil
}

func (i *fakeIndex) Close() error {
	i.owner.closes++
	return nil
}
