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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

func TestPrepareAndReplayKeywordEndToEnd(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "native-run")
	corpusRoot := filepath.Join(root, "task-start-corpora")
	caseCorpus := filepath.Join(corpusRoot, "case-a")
	caseRun := filepath.Join(runDir, "case-a")
	for _, directory := range []string{caseCorpus, caseRun} {
		if err := os.MkdirAll(filepath.Join(directory, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(caseCorpus, "pkg", "parser.py"),
		[]byte("def parse_helper(value):\n    return value.strip()\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	const repository = "fixture/repository"
	const queryText = "parse helper value"
	const rawArguments = "{ \"query\" : \"parse helper value\", \"future_filter\" : {\"language\":\"python\"} }"
	index, stats, err := tagagent.NewWorkspaceIndexFromDirectory(
		context.Background(),
		caseCorpus,
		repository,
		&tagagent.WorkspaceSearchConfig{
			SearchMode:     vectorstore.SearchModeKeyword,
			MaxResults:     6,
			Representation: tagagent.WorkspaceRepresentationASTStructured,
			RepositoryName: repository,
			BatchSize:      1,
			DocConcurrency: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	callable, ok := index.Tool().(interface {
		Call(context.Context, []byte) (any, error)
	})
	if !ok {
		t.Fatal("workspace code_search tool is not callable")
	}
	toolResult, err := callable.Call(context.Background(), []byte(rawArguments))
	if err != nil {
		t.Fatal(err)
	}
	rawResult, err := json.Marshal(toolResult)
	if err != nil {
		t.Fatal(err)
	}
	traceDocuments, err := traceDocumentsFromToolResult(rawResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(traceDocuments) == 0 {
		t.Fatal("keyword fixture unexpectedly returned no documents")
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	executedResponse := replayResponse("call-executed", rawArguments)
	postSubmitResponse := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id": "call-submit",
					"function": map[string]any{
						"name": "bash", "arguments": `{"command":"echo submit"}`,
					},
				},
				map[string]any{
					"id": "call-skipped",
					"function": map[string]any{
						"name": "code_search", "arguments": `{"query":"never executed"}`,
					},
				},
			},
		}}},
		"done": true, "is_partial": false,
	}
	responses := []any{executedResponse, postSubmitResponse}
	responsesPath := filepath.Join(caseRun, "case-a.responses.json")
	responsesData := writeJSONFile(t, responsesPath, responses)
	responsesSHA := digestBytes(responsesData)

	hash := func(character string) string { return strings.Repeat(character, 64) }
	manifest := map[string]any{
		"run_id": "native-keyword-run", "runner_type": "trpc-agent-go-native",
		"framework_module":  "trpc.group/trpc-go/trpc-agent-go",
		"framework_version": "v1.10.1", "agent_protocol": "mini-swe-agent-v2.1-on-trpc-agent-go+clean-room-v1",
		"upstream_commit": strings.Repeat("a", 40), "observation_codec": "xml",
		"source_revision": strings.Repeat("b", 40), "source_modified": false,
		"binary_sha256": hash("1"), "cases_sha256": hash("2"),
		"model_config_sha256": hash("3"), "environment_config_sha256": hash("4"),
		"selected_instances_sha256": hash("5"), "clean_room": true,
		"tool_loop_warning": true, "clean_room_policy_sha256": hash("6"),
		"image_set_sha256": hash("7"), "command_timeout": "1m0s", "case_timeout": "4h0m0s",
		"case_count": 1, "completed_count": 1, "prediction_count": 1,
		"workers": 1, "status": "completed", "code_search": true,
		"workspace_preload": false, "workspace_representation": stats.Representation,
		"workspace_representation_schema": stats.RepresentationSchema,
		"workspace_representation_sha256": stats.RepresentationSHA256,
	}
	writeJSONFile(t, filepath.Join(runDir, NativeManifestFilename), manifest)
	info := map[string]any{
		"run_id": "native-keyword-run", "observation_codec": "xml",
		"source_revision": strings.Repeat("b", 40), "source_modified": false,
		"binary_sha256": hash("1"), "model_config_sha256": hash("3"),
		"environment_config_sha256": hash("4"), "cases_sha256": hash("2"),
		"command_timeout": "1m0s", "case_timeout": "4h0m0s",
		"selected_instances_sha256": hash("5"), "clean_room": true,
		"tool_loop_warning": true, "clean_room_policy_sha256": hash("6"),
		"code_search": true, "workspace_preload": false,
		"workspace_representation":        stats.Representation,
		"workspace_representation_sha256": stats.RepresentationSHA256,
		"image_set_sha256":                hash("7"), "repo": repository,
		"base_commit":          strings.Repeat("c", 40),
		"verified_base_commit": strings.Repeat("c", 40),
		"workers":              1, "exit_status": "Submitted",
	}
	native := map[string]any{
		"instance_id": "case-a", "info": info,
		"model_patch": "candidate patch must never enter the replay bundle",
		"llm_calls":   2, "tool_calls": 2,
		"events":         []any{executedResponse},
		"response_count": 2, "responses_sha256": responsesSHA,
		"code_search_calls": 1, "code_search_errors": 0,
		"code_search_result_bytes": len(rawResult),
		"code_search_raw_results":  []json.RawMessage{rawResult},
		"retrieval_trace": []any{map[string]any{
			"call": 1, "tool_call_id": "call-executed", "query": queryText,
			"status": "success", "arguments_sha256": digestBytes([]byte(rawArguments)),
			"result_sha256": digestBytes(rawResult), "result_bytes": len(rawResult),
			"documents": traceDocuments,
		}},
		"workspace_index": map[string]any{
			"representation":           stats.Representation,
			"representation_schema":    stats.RepresentationSchema,
			"representation_sha256":    stats.RepresentationSHA256,
			"parser_dependency":        stats.ParserDependency,
			"parser_runtime_sha256":    stats.ParserRuntimeSHA256,
			"eligible_file_set_sha256": stats.EligibleFileSetSHA256,
			"eligible_content_sha256":  stats.EligibleContentSHA256,
			"document_set_sha256":      stats.DocumentSetSHA256,
			"retrieval_mode":           stats.RetrievalMode,
		},
	}
	writeJSONFile(t, filepath.Join(caseRun, "case-a.native.json"), native)

	caseList := filepath.Join(root, "cases.txt")
	if err := os.WriteFile(caseList, []byte("case-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleDirectory := filepath.Join(root, "portable-bundle")
	if err := Run([]string{
		"retrieval-replay", "prepare",
		"--run-dir", runDir,
		"--corpus-root", corpusRoot,
		"--case-list", caseList,
		"--output-dir", bundleDirectory,
	}); err != nil {
		t.Fatal(err)
	}
	secondBundleDirectory := filepath.Join(root, "portable-bundle-second")
	if err := Run([]string{
		"retrieval-replay", "prepare",
		"--run-dir", runDir,
		"--corpus-root", corpusRoot,
		"--case-list", caseList,
		"--output-dir", secondBundleDirectory,
	}); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		preparedBundleFilename,
		preparedReportFilename,
		engineConfigFilename,
		"artifacts/case-a/workspace.tar",
		"artifacts/case-a/queries.json",
		"artifacts/case-a/expected.json",
	} {
		first, err := os.ReadFile(filepath.Join(bundleDirectory, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		second, err := os.ReadFile(filepath.Join(secondBundleDirectory, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Fatalf("prepared artifact %s is not deterministic", relative)
		}
	}
	bundlePath := filepath.Join(bundleDirectory, preparedBundleFilename)
	bundleData, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, "candidate patch", "gold_patch", "model_patch", "endpoint"} {
		if strings.Contains(string(bundleData), forbidden) {
			t.Fatalf("portable bundle contains forbidden value %q", forbidden)
		}
	}
	loaded, err := Load(bundlePath, runDir)
	if err != nil {
		t.Fatal(err)
	}
	queries := loaded.Cases[0].Queries
	if queries.EmittedCodeSearchCalls != 2 || queries.SkippedEmittedCodeSearchCalls != 1 ||
		len(queries.Queries) != 1 || queries.Queries[0].NativeArguments != rawArguments {
		t.Fatalf("unexpected executed query selection: %+v", queries)
	}

	replayOutput := filepath.Join(root, "replay-report.json")
	if err := Run([]string{
		"retrieval-replay", "replay",
		"--run-dir", runDir,
		"--bundle", bundlePath,
		"--output", replayOutput,
	}); err != nil {
		t.Fatal(err)
	}
	var report Report
	reportData, err := os.ReadFile(replayOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Summary != (Summary{Cases: 1, Queries: 1, Matches: 1}) ||
		report.Cases[0].EmittedCodeSearchCalls != 2 ||
		report.Cases[0].SkippedEmittedCodeSearchCalls != 1 {
		t.Fatalf("unexpected concrete replay report: %+v", report)
	}

	if err := os.WriteFile(
		filepath.Join(caseCorpus, "pkg", "parser.py"),
		[]byte("def changed_after_freeze():\n    return True\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	mismatchOutput := filepath.Join(root, "mismatched-corpus-bundle")
	err = Run([]string{
		"retrieval-replay", "prepare",
		"--run-dir", runDir,
		"--corpus-root", corpusRoot,
		"--case-list", caseList,
		"--output-dir", mismatchOutput,
	})
	if err == nil || !strings.Contains(err.Error(), "index hashes do not match") {
		t.Fatalf("corpus hash mismatch error = %v", err)
	}
	if _, statErr := os.Stat(mismatchOutput); !os.IsNotExist(statErr) {
		t.Fatalf("failed prepare published output: %v", statErr)
	}
}

func replayResponse(callID, arguments string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"id":       callID,
				"function": map[string]any{"name": "code_search", "arguments": arguments},
			}},
		}}},
		"done": true, "is_partial": false,
	}
}

func traceDocumentsFromToolResult(payload []byte) ([]map[string]any, error) {
	var response struct {
		Documents []struct {
			ID       string         `json:"id"`
			Text     string         `json:"text"`
			Metadata map[string]any `json:"metadata"`
			Score    float64        `json:"score"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(response.Documents))
	for _, one := range response.Documents {
		path := metadataText(one.Metadata, "trpc_ast_file_path")
		symbol := metadataText(one.Metadata, "trpc_ast_full_name")
		if symbol == path {
			symbol = ""
		}
		lines := ""
		start, startOK := positiveMetadataLine(one.Metadata, "trpc_ast_line_start")
		end, endOK := positiveMetadataLine(one.Metadata, "trpc_ast_line_end")
		if startOK && endOK && start <= end {
			lines = strconv.FormatInt(start, 10)
			if start != end {
				lines += "-" + strconv.FormatInt(end, 10)
			}
		}
		result = append(result, map[string]any{
			"id": one.ID, "path": path, "lines": lines, "symbol": symbol,
			"score": one.Score, "content_sha256": digestBytes([]byte(one.Text)),
		})
	}
	return result, nil
}

func metadataText(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func positiveMetadataLine(metadata map[string]any, key string) (int64, bool) {
	value := metadataText(metadata, key)
	line, err := strconv.ParseInt(value, 10, 64)
	return line, err == nil && line > 0
}

func TestPrepareRejectsMissingExtraAndSymlinkedCaseCorpora(t *testing.T) {
	root := t.TempDir()
	caseList := filepath.Join(root, "cases.txt")
	if err := os.WriteFile(caseList, []byte("case-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		setup func(string) error
		want  string
	}{
		{
			name:  "missing",
			setup: func(string) error { return nil },
			want:  "missing selected case",
		},
		{
			name: "extra",
			setup: func(root string) error {
				if err := os.Mkdir(filepath.Join(root, "case-a"), 0o755); err != nil {
					return err
				}
				return os.Mkdir(filepath.Join(root, "case-extra"), 0o755)
			},
			want: "unselected entry",
		},
		{
			name: "symlink",
			setup: func(root string) error {
				target := filepath.Join(root, "target")
				if err := os.Mkdir(target, 0o755); err != nil {
					return err
				}
				return os.Symlink(target, filepath.Join(root, "case-a"))
			},
			want: "real directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			corpusRoot := filepath.Join(root, test.name)
			if err := os.Mkdir(corpusRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := test.setup(corpusRoot); err != nil {
				if test.name == "symlink" {
					t.Skipf("symlink unavailable: %v", err)
				}
				t.Fatal(err)
			}
			_, err := validateCorpusRootSelection(corpusRoot, []string{"case-a"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("selection error = %v, want %q", err, test.want)
			}
		})
	}
}
