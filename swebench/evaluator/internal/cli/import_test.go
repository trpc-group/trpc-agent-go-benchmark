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
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

func TestRunImportWritesTargetNeutralSchema(t *testing.T) {
	dir := t.TempDir()
	predictionsPath := filepath.Join(dir, "preds.json")
	if err := writeJSON(predictionsPath, map[string]contract.Prediction{
		"case-a": {
			InstanceID:      "case-a",
			ModelNameOrPath: "model",
			ModelPatch:      "diff --git a/a b/a\n--- a/a\n+++ b/a\n+value\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", "mini-go",
		"--predictions", predictionsPath,
		"--output", output,
	}); err != nil {
		t.Fatalf("runImport() error = %v", err)
	}

	file, err := os.Open(filepath.Join(output, "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("missing imported case: %v", scanner.Err())
	}
	var row importedCase
	if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.SchemaVersion != importSchemaVersion || row.Target != "mini-go" || row.InstanceID != "case-a" {
		t.Fatalf("imported row = %+v", row)
	}
	if row.Result.ModelNameOrPath != "model" || row.Result.MainStatus != "incomplete" {
		t.Fatalf("result = %+v", row.Result)
	}
	data, err := os.ReadFile(filepath.Join(output, "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"baseline"`) || strings.Contains(string(data), `"native"`) {
		t.Fatalf("target-specific legacy field leaked into %s", data)
	}

	var summary importSummary
	if err := readJSONFile(filepath.Join(output, "summary", "mini-go.json"), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != importSchemaVersion || summary.Target != "mini-go" || summary.Total != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestRunImportAcceptsOfficialEmptyPatchOnlyReport(t *testing.T) {
	dir := t.TempDir()
	predictionsPath := filepath.Join(dir, "preds.json")
	if err := writeJSON(predictionsPath, map[string]contract.Prediction{
		"case-a": {
			InstanceID:      "case-a",
			ModelNameOrPath: "model",
			ModelPatch:      "",
		},
	}); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dir, "report.json")
	if err := writeJSON(reportPath, map[string]any{"empty_patch_ids": []string{"case-a"}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", "native",
		"--predictions", predictionsPath,
		"--harness-report", reportPath,
		"--output", output,
	}); err != nil {
		t.Fatalf("runImport() error = %v", err)
	}
	rows, err := readAndValidateImportedCases(
		filepath.Join(output, "cases.jsonl"),
		importSummary{SchemaVersion: importSchemaVersion, Target: "native", Total: 1, Counts: map[string]int{"empty_patch": 1}},
		"native",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Result.MainStatus != "empty_patch" || rows[0].Result.FailureReason != "empty model_patch" {
		t.Fatalf("imported rows = %+v", rows)
	}
}

func TestRunImportRejectsUnsafeTargetBeforeCreatingOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	err := runImport([]string{
		"--target", "../escape",
		"--predictions", filepath.Join(dir, "missing.json"),
		"--output", output,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid target") {
		t.Fatalf("runImport() error = %v, want invalid target", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output was created before target validation: %v", statErr)
	}
}

func TestReadCasesRejectsUnsafePredictionID(t *testing.T) {
	_, err := readCases("", map[string]contract.Prediction{
		"../escape": {InstanceID: "../escape"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid instance id") {
		t.Fatalf("readCases() error = %v, want unsafe instance id", err)
	}
}

func TestReadHarnessReportRejectsDuplicateTerminalOutcome(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	if err := os.WriteFile(reportPath, []byte(`{
  "resolved_ids":["case-a"],
  "unresolved_ids":["case-a"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := readHarnessReport(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	err = validateHarnessIndex(index, []contract.Case{{InstanceID: "case-a"}})
	if err == nil || !strings.Contains(err.Error(), "multiple terminal") {
		t.Fatalf("validateHarnessIndex() error = %v, want overlap error", err)
	}
}

func TestReadHarnessReportRejectsMalformedOutcomeList(t *testing.T) {
	for _, field := range []string{"resolved_ids", "empty_patch_ids", "incomplete_ids"} {
		t.Run(field, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(reportPath, []byte(`{"`+field+`":"case-a"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readHarnessReport(reportPath); err == nil {
				t.Fatalf("readHarnessReport() accepted a non-array %s field", field)
			}
		})
	}
}

func TestReadHarnessReportSupportsOfficialNonExecutionOutcomes(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, []byte(`{
  "resolved_ids":["case-a"],
  "empty_patch_ids":["case-b"],
  "incomplete_ids":["case-c"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := readHarnessReport(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !index.Resolved["case-a"] || !index.EmptyPatch["case-b"] || !index.Incomplete["case-c"] {
		t.Fatalf("readHarnessReport() = %+v", index)
	}
	if err := validateHarnessIndex(index, []contract.Case{
		{InstanceID: "case-a"},
		{InstanceID: "case-b"},
		{InstanceID: "case-c"},
	}); err != nil {
		t.Fatalf("validateHarnessIndex() error = %v", err)
	}
}

func TestValidateHarnessIndexOfficialCompletedSemantics(t *testing.T) {
	t.Run("completed error", func(t *testing.T) {
		index := contract.NewHarnessIndex()
		index.Errors["case-a"] = true
		index.Completed["case-a"] = true
		if err := validateHarnessIndex(index, []contract.Case{{InstanceID: "case-a"}}); err != nil {
			t.Fatalf("validateHarnessIndex() rejected completed error: %v", err)
		}
	})
	t.Run("completed only", func(t *testing.T) {
		index := contract.NewHarnessIndex()
		index.Completed["case-a"] = true
		err := validateHarnessIndex(index, []contract.Case{{InstanceID: "case-a"}})
		if err == nil || !strings.Contains(err.Error(), "has no resolved, unresolved, or error outcome") {
			t.Fatalf("validateHarnessIndex() error = %v, want missing execution outcome", err)
		}
	})
	t.Run("cross outcome overlap", func(t *testing.T) {
		index := contract.NewHarnessIndex()
		index.EmptyPatch["case-a"] = true
		index.Incomplete["case-a"] = true
		err := validateHarnessIndex(index, []contract.Case{{InstanceID: "case-a"}})
		if err == nil || !strings.Contains(err.Error(), "multiple terminal") {
			t.Fatalf("validateHarnessIndex() error = %v, want overlap", err)
		}
	})
	t.Run("unknown non-execution outcome", func(t *testing.T) {
		index := contract.NewHarnessIndex()
		index.EmptyPatch["case-z"] = true
		err := validateHarnessIndex(index, []contract.Case{{InstanceID: "case-a"}})
		if err == nil || !strings.Contains(err.Error(), "not present in case manifest") {
			t.Fatalf("validateHarnessIndex() error = %v, want unknown case", err)
		}
	})
}

func TestRunImportReadsNativeTraceUsageAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	predictionsPath := filepath.Join(dir, "preds.json")
	if err := writeJSON(predictionsPath, map[string]contract.Prediction{
		"case-a": {
			InstanceID:      "case-a",
			ModelNameOrPath: "model",
			ModelPatch:      "diff --git a/a b/a\n--- a/a\n+++ b/a\n+value\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	rawDir := filepath.Join(dir, "raw")
	caseDir := filepath.Join(rawDir, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nativeArtifact := validNativeArtifact()
	nativeInfo := nativeArtifact["info"].(map[string]any)
	nativeInfo["repo"] = "owner/repo"
	nativeInfo["base_commit"] = strings.Repeat("a", 40)
	nativeInfo["tool_loop_warning"] = true
	nativeArtifact["llm_calls"] = 3
	nativeArtifact["tool_loop_warning_count"] = 2
	nativeArtifact["first_tool_loop_warning_llm_call"] = 2
	nativeArtifact["tool_loop_warning_llm_calls"] = []int{2, 3}
	usage := nativeArtifact["usage"].(map[string]any)
	usage["prompt_tokens"] = 100
	usage["completion_tokens"] = 40
	usage["total_tokens"] = 140
	usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] = 70
	usage["completion_tokens_details"].(map[string]any)["reasoning_tokens"] = 25
	nativeArtifact["api_key"] = "sk-native-secret"
	nativeTrace, err := json.Marshal(nativeArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case-a.native.json"), nativeTrace, 0o600); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", "tag",
		"--predictions", predictionsPath,
		"--raw-dir", rawDir,
		"--output", output,
	}); err != nil {
		t.Fatalf("runImport() error = %v", err)
	}

	file, err := os.Open(filepath.Join(output, "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("missing imported case: %v", scanner.Err())
	}
	var row importedCase
	if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	wantUsage := usageStats{
		PromptTokens: 100, CachedTokens: 70, UncachedTokens: 30,
		CompletionTokens: 40, ReasoningTokens: 25, TotalTokens: 140,
		APICalls: 3,
	}
	if row.Result.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", row.Result.Usage, wantUsage)
	}
	if !row.ToolLoopWarning || row.Result.ToolLoopWarningCount != 2 ||
		row.Result.FirstToolLoopWarningLLMCall == nil ||
		*row.Result.FirstToolLoopWarningLLMCall != 2 ||
		!reflect.DeepEqual(row.Result.ToolLoopWarningLLMCalls, []int{2, 3}) {
		t.Fatalf("tool-loop warning projection = %+v", row)
	}
	if row.Result.TracePath == "" {
		t.Fatal("native trace path is empty")
	}
	trace, err := os.ReadFile(filepath.Join(output, row.Result.TracePath))
	if err != nil {
		t.Fatal(err)
	}
	var scrubbed map[string]any
	if err := json.Unmarshal(trace, &scrubbed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), "sk-native-secret") || scrubbed["api_key"] != "<redacted>" {
		t.Fatalf("native trace was not redacted: %s", trace)
	}
	if row.CleanRoom || row.EnvironmentProvenance != nil {
		t.Fatalf("legacy default-off native trace gained clean-room provenance: %+v", row)
	}
	if row.Repo != "owner/repo" || row.BaseCommit != strings.Repeat("a", 40) {
		t.Fatalf("default-off native trace metadata was not imported: %+v", row)
	}
}

func TestImportedNativeInfoValidatesPredictionDefinedTraceMetadata(t *testing.T) {
	validBaseCommit := strings.Repeat("a", 40)
	for _, tt := range []struct {
		name      string
		cleanRoom bool
		edit      func(map[string]any)
		wantRepo  string
		wantBase  string
		wantErr   string
	}{
		{name: "default-off legacy empty metadata"},
		{
			name: "default-off paired metadata",
			edit: func(info map[string]any) {
				info["repo"] = "owner/repo"
				info["base_commit"] = validBaseCommit
			},
			wantRepo: "owner/repo",
			wantBase: validBaseCommit,
		},
		{
			name: "default-off repo only",
			edit: func(info map[string]any) {
				info["repo"] = "owner/repo"
			},
			wantErr: "info.repo and info.base_commit together",
		},
		{
			name: "default-off base only",
			edit: func(info map[string]any) {
				info["base_commit"] = validBaseCommit
			},
			wantErr: "info.repo and info.base_commit together",
		},
		{
			name: "default-off invalid base commit",
			edit: func(info map[string]any) {
				info["repo"] = "owner/repo"
				info["base_commit"] = "abc"
			},
			wantErr: "40-hex trace info.base_commit",
		},
		{
			name: "default-off blank repo",
			edit: func(info map[string]any) {
				info["repo"] = "   "
				info["base_commit"] = validBaseCommit
			},
			wantErr: "non-empty trace info.repo",
		},
		{
			name:      "clean-room missing repo",
			cleanRoom: true,
			edit: func(info map[string]any) {
				delete(info, "repo")
			},
			wantErr: "required field info.repo",
		},
		{
			name:      "clean-room invalid base commit",
			cleanRoom: true,
			edit: func(info map[string]any) {
				info["base_commit"] = "abc"
			},
			wantErr: "invalid info.base_commit",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rawDir := t.TempDir()
			caseDir := filepath.Join(rawDir, "case-a")
			if err := os.MkdirAll(caseDir, 0o755); err != nil {
				t.Fatal(err)
			}
			artifact := validNativeArtifact()
			if tt.cleanRoom {
				artifact, _, _ = cleanRoomNativeArtifact(t, "case-a", "owner/repo", validBaseCommit)
			}
			info := artifact["info"].(map[string]any)
			if tt.edit != nil {
				tt.edit(info)
			}
			if err := os.WriteFile(
				filepath.Join(caseDir, "case-a.native.json"),
				marshalJSONObject(t, artifact),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			got, err := importedNativeInfo(rawDir, contract.Case{InstanceID: "case-a"}, true)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("importedNativeInfo() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("importedNativeInfo() error = %v", err)
			}
			if got.Repo != tt.wantRepo || got.BaseCommit != tt.wantBase {
				t.Fatalf("importedNativeInfo() = %+v, want repo/base %q/%q", got, tt.wantRepo, tt.wantBase)
			}
		})
	}
}

func TestRunImportBindsCleanRoomNativeProvenance(t *testing.T) {
	dir := t.TempDir()
	instanceID := "psf__requests-2317"
	repo := "psf/requests"
	baseCommit := strings.Repeat("c", 40)
	predictionsPath := filepath.Join(dir, "preds.json")
	if err := writeJSON(predictionsPath, map[string]contract.Prediction{
		instanceID: {InstanceID: instanceID, ModelNameOrPath: "model"},
	}); err != nil {
		t.Fatal(err)
	}
	casesPath := filepath.Join(dir, "cases.jsonl")
	caseData, err := json.Marshal(contract.Case{InstanceID: instanceID, Repo: repo, BaseCommit: baseCommit})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(casesPath, append(caseData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	rawDir := filepath.Join(dir, "raw")
	caseDir := filepath.Join(rawDir, instanceID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, _, _ := cleanRoomNativeArtifact(t, instanceID, repo, baseCommit)
	if err := os.WriteFile(
		filepath.Join(caseDir, instanceID+".native.json"),
		marshalJSONObject(t, artifact),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", "native",
		"--cases", casesPath,
		"--predictions", predictionsPath,
		"--raw-dir", rawDir,
		"--output", output,
	}); err != nil {
		t.Fatalf("runImport() error = %v", err)
	}
	rows, err := readAndValidateImportedCases(
		filepath.Join(output, "cases.jsonl"),
		importSummary{SchemaVersion: importSchemaVersion, Target: "native", Total: 1, Counts: map[string]int{"empty_patch": 1}},
		"native",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("imported rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if !row.CleanRoom || row.Repo != repo || row.BaseCommit != baseCommit || row.VerifiedBaseCommit != baseCommit {
		t.Fatalf("imported clean-room identity = %+v", row)
	}
	if !isSHA256Hex(row.CleanRoomPolicySHA256) || !isSHA256Hex(row.OfflineAssetsSHA256) ||
		!isSHA256Hex(row.ImageSetSHA256) || row.EnvironmentProvenance == nil {
		t.Fatalf("imported clean-room provenance = %+v", row)
	}
}

func TestValidateCaseEnvironmentProvenanceFixesAuxiliaryRoles(t *testing.T) {
	instanceID := "psf__requests-2317"
	artifact, _, _ := cleanRoomNativeArtifact(t, instanceID, "psf/requests", strings.Repeat("c", 40))
	base := artifact["info"].(map[string]any)["environment_provenance"].(sweenv.Provenance)

	wrongHTTPBin := *cloneEnvironmentProvenance(&base)
	wrongHTTPBin.AuxiliaryImages["httpbin"] = sweenv.ImageIdentity{
		Reference: "example.test/not-httpbin:latest",
		ID:        "sha256:" + strings.Repeat("d", 64),
	}
	if err := validateCaseEnvironmentProvenance(instanceID, wrongHTTPBin, nil); err == nil ||
		!strings.Contains(err.Error(), "httpbin image reference") {
		t.Fatalf("validateCaseEnvironmentProvenance() error = %v, want fixed httpbin reference rejection", err)
	}

	wrongHelper := *cloneEnvironmentProvenance(&base)
	wrongHelper.AuxiliaryImages["network-helper"] = wrongHelper.AuxiliaryImages["httpbin"]
	if err := validateCaseEnvironmentProvenance(instanceID, wrongHelper, nil); err == nil ||
		!strings.Contains(err.Error(), "network-helper image") {
		t.Fatalf("validateCaseEnvironmentProvenance() error = %v, want helper/testbed mismatch", err)
	}
}

func TestCopyScrubbedTraceRejectsAmbiguousAgentArtifacts(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	caseDir := filepath.Join(rawDir, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"case-a.traj.json", "case-a.native.json"} {
		if err := os.WriteFile(filepath.Join(caseDir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := copyScrubbedTrace(rawDir, filepath.Join(dir, "traces"), "case-a")
	if err == nil || !strings.Contains(err.Error(), "ambiguous trace") {
		t.Fatalf("copyScrubbedTrace() error = %v, want ambiguity", err)
	}
}

func TestCopyScrubbedTraceRejectsMalformedNativeArtifact(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	caseDir := filepath.Join(rawDir, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case-a.native.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := copyScrubbedTrace(rawDir, filepath.Join(dir, "traces"), "case-a")
	if err == nil || !strings.Contains(err.Error(), "parse native trace") {
		t.Fatalf("copyScrubbedTrace() error = %v, want malformed native trace", err)
	}
}

func TestCopyScrubbedTraceRejectsMismatchedNativeInstance(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	caseDir := filepath.Join(rawDir, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := validNativeArtifact()
	artifact["instance_id"] = "case-b"
	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(caseDir, "case-a.native.json"),
		data,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	_, _, err = copyScrubbedTrace(rawDir, filepath.Join(dir, "traces"), "case-a")
	if err == nil || !strings.Contains(err.Error(), `instance_id "case-b" does not match "case-a"`) {
		t.Fatalf("copyScrubbedTrace() error = %v, want instance mismatch", err)
	}
}

func TestExtractNativeUsageRejectsInvalidCounts(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{name: "workers", field: "info.workers", want: "non-positive info.workers"},
		{name: "duration", field: "duration_ms", want: "negative duration_ms"},
		{name: "llm calls", field: "llm_calls", want: "negative llm_calls"},
		{name: "tool calls", field: "tool_calls", want: "negative tool_calls"},
		{name: "response count", field: "response_count", want: "negative response_count"},
		{name: "prompt tokens", field: "usage.prompt_tokens", want: "negative usage.prompt_tokens"},
		{
			name:  "cached tokens",
			field: "usage.prompt_tokens_details.cached_tokens",
			want:  "negative usage.prompt_tokens_details.cached_tokens",
		},
		{
			name:  "cache creation tokens",
			field: "usage.prompt_tokens_details.cache_creation_tokens",
			want:  "negative usage.prompt_tokens_details.cache_creation_tokens",
		},
		{
			name:  "cache read tokens",
			field: "usage.prompt_tokens_details.cache_read_tokens",
			want:  "negative usage.prompt_tokens_details.cache_read_tokens",
		},
		{
			name:  "completion tokens",
			field: "usage.completion_tokens",
			want:  "negative usage.completion_tokens",
		},
		{
			name:  "reasoning tokens",
			field: "usage.completion_tokens_details.reasoning_tokens",
			want:  "negative usage.completion_tokens_details.reasoning_tokens",
		},
		{name: "total tokens", field: "usage.total_tokens", want: "negative usage.total_tokens"},
		{
			name:  "time to first token",
			field: "usage.timing_info.time_to_first_token",
			want:  "negative usage.timing_info.time_to_first_token",
		},
		{
			name:  "reasoning duration",
			field: "usage.timing_info.reasoning_duration",
			want:  "negative usage.timing_info.reasoning_duration",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cloneJSONObject(t, validNativeArtifact())
			setJSONField(t, candidate, tt.field, -1)
			_, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("extractNativeUsage() error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Run("cached exceeds prompt", func(t *testing.T) {
		candidate := cloneJSONObject(t, validNativeArtifact())
		setJSONField(t, candidate, "usage.prompt_tokens", 2)
		setJSONField(t, candidate, "usage.prompt_tokens_details.cached_tokens", 3)
		_, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a")
		if err == nil || !strings.Contains(err.Error(), "cached_tokens 3 greater than prompt_tokens 2") {
			t.Fatalf("extractNativeUsage() error = %v, want cached/prompt error", err)
		}
	})
}

func TestExtractNativeUsageRequiresSerializedCaseResultStructure(t *testing.T) {
	required := []struct {
		name  string
		field string
	}{
		{name: "instance id", field: "instance_id"},
		{name: "info", field: "info"},
		{name: "model patch", field: "model_patch"},
		{name: "duration", field: "duration_ms"},
		{name: "llm calls", field: "llm_calls"},
		{name: "tool calls", field: "tool_calls"},
		{name: "usage", field: "usage"},
		{name: "response count", field: "response_count"},
		{name: "response sha", field: "responses_sha256"},
		{name: "workers", field: "info.workers"},
		{name: "exit status", field: "info.exit_status"},
		{name: "prompt tokens", field: "usage.prompt_tokens"},
		{name: "completion tokens", field: "usage.completion_tokens"},
		{name: "total tokens", field: "usage.total_tokens"},
		{name: "prompt token details", field: "usage.prompt_tokens_details"},
		{name: "cached tokens", field: "usage.prompt_tokens_details.cached_tokens"},
		{name: "completion token details", field: "usage.completion_tokens_details"},
	}
	for _, tt := range required {
		t.Run("missing "+tt.name, func(t *testing.T) {
			candidate := cloneJSONObject(t, validNativeArtifact())
			removeJSONField(t, candidate, tt.field)
			_, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a")
			want := "required field " + tt.field
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("extractNativeUsage() error = %v, want %q", err, want)
			}
		})
	}

	for _, tt := range required {
		t.Run("null "+tt.name, func(t *testing.T) {
			candidate := cloneJSONObject(t, validNativeArtifact())
			setJSONField(t, candidate, tt.field, nil)
			if _, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a"); err == nil {
				t.Fatalf("extractNativeUsage() accepted null %s", tt.field)
			}
		})
		t.Run("wrong type "+tt.name, func(t *testing.T) {
			candidate := cloneJSONObject(t, validNativeArtifact())
			setJSONField(t, candidate, tt.field, wrongNativeFieldType(tt.field))
			if _, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a"); err == nil {
				t.Fatalf("extractNativeUsage() accepted wrong type for %s", tt.field)
			}
		})
	}

	t.Run("optional reasoning omitted", func(t *testing.T) {
		candidate := cloneJSONObject(t, validNativeArtifact())
		removeJSONField(t, candidate, "usage.completion_tokens_details.reasoning_tokens")
		if _, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a"); err != nil {
			t.Fatalf("extractNativeUsage() rejected omitted optional reasoning_tokens: %v", err)
		}
	})

	t.Run("optional field wrong type", func(t *testing.T) {
		candidate := cloneJSONObject(t, validNativeArtifact())
		setJSONField(t, candidate, "usage.completion_tokens_details.reasoning_tokens", "zero")
		if _, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a"); err == nil {
			t.Fatal("extractNativeUsage() accepted wrong-type optional reasoning_tokens")
		}
	})

	t.Run("non-positive workers", func(t *testing.T) {
		candidate := cloneJSONObject(t, validNativeArtifact())
		setJSONField(t, candidate, "info.workers", 0)
		_, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a")
		if err == nil || !strings.Contains(err.Error(), "non-positive info.workers") {
			t.Fatalf("extractNativeUsage() error = %v, want non-positive workers error", err)
		}
	})

	t.Run("empty exit status", func(t *testing.T) {
		candidate := cloneJSONObject(t, validNativeArtifact())
		setJSONField(t, candidate, "info.exit_status", " ")
		_, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a")
		if err == nil || !strings.Contains(err.Error(), "empty info.exit_status") {
			t.Fatalf("extractNativeUsage() error = %v, want empty exit status error", err)
		}
	})

	data := marshalJSONObject(t, validNativeArtifact())
	trace, err := parseNativeTraceEnvelope(data, "case-a")
	if err != nil {
		t.Fatalf("parseNativeTraceEnvelope() explicit zero artifact error = %v", err)
	}
	if trace.InstanceID != "case-a" || trace.Info.ExitStatus != "Submitted" || trace.ResponsesSHA256 != emptyResponsesSHA256 {
		t.Fatalf("parseNativeTraceEnvelope() = %+v", trace)
	}
	if trace.Info.ToolLoopWarning || trace.ToolLoopWarningCount != 0 ||
		trace.FirstToolLoopWarningLLMCall != nil || len(trace.ToolLoopWarningLLMCalls) != 0 {
		t.Fatalf("legacy missing warning fields were not interpreted as disabled zero telemetry: %+v", trace)
	}
	usage, err := extractNativeUsage(data, "case-a")
	if err != nil {
		t.Fatalf("extractNativeUsage() explicit zero artifact error = %v", err)
	}
	if usage != (usageStats{}) {
		t.Fatalf("extractNativeUsage() explicit zero usage = %+v, want zero", usage)
	}
}

func TestParseNativeTraceValidatesToolLoopWarningTelemetry(t *testing.T) {
	valid := func() map[string]any {
		artifact := cloneJSONObject(t, validNativeArtifact())
		artifact["llm_calls"] = 4
		artifact["tool_loop_warning_count"] = 2
		artifact["first_tool_loop_warning_llm_call"] = 2
		artifact["tool_loop_warning_llm_calls"] = []int{2, 4}
		artifact["info"].(map[string]any)["tool_loop_warning"] = true
		return artifact
	}
	trace, err := parseNativeTraceEnvelope(marshalJSONObject(t, valid()), "case-a")
	if err != nil {
		t.Fatal(err)
	}
	if trace.ToolLoopWarningCount != 2 || trace.FirstToolLoopWarningLLMCall == nil ||
		*trace.FirstToolLoopWarningLLMCall != 2 ||
		!reflect.DeepEqual(trace.ToolLoopWarningLLMCalls, []int{2, 4}) {
		t.Fatalf("trace warning telemetry = %+v", trace)
	}

	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{name: "missing count", change: func(artifact map[string]any) {
			delete(artifact, "tool_loop_warning_count")
		}, want: "missing required field tool_loop_warning_count"},
		{name: "missing first call", change: func(artifact map[string]any) {
			delete(artifact, "first_tool_loop_warning_llm_call")
		}, want: "missing required field first_tool_loop_warning_llm_call"},
		{name: "missing call list", change: func(artifact map[string]any) {
			delete(artifact, "tool_loop_warning_llm_calls")
		}, want: "missing required field tool_loop_warning_llm_calls"},
		{name: "disabled with telemetry", change: func(artifact map[string]any) {
			artifact["info"].(map[string]any)["tool_loop_warning"] = false
		}, want: "tool_loop_warning=false"},
		{name: "count mismatch", change: func(artifact map[string]any) {
			artifact["tool_loop_warning_count"] = 1
		}, want: "want count"},
		{name: "first mismatch", change: func(artifact map[string]any) {
			artifact["first_tool_loop_warning_llm_call"] = 3
		}, want: "inconsistent first"},
		{name: "unsorted", change: func(artifact map[string]any) {
			artifact["tool_loop_warning_llm_calls"] = []int{2, 2}
		}, want: "strictly increasing"},
		{name: "beyond calls", change: func(artifact map[string]any) {
			artifact["tool_loop_warning_llm_calls"] = []int{2, 5}
		}, want: "beyond llm_calls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact := valid()
			tc.change(artifact)
			_, err := parseNativeTraceEnvelope(marshalJSONObject(t, artifact), "case-a")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestExtractNativeUsageRejectsInvalidResponsesSHA256(t *testing.T) {
	for _, value := range []string{
		"",
		"abc",
		strings.Repeat("g", 64),
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
	} {
		t.Run(value, func(t *testing.T) {
			candidate := cloneJSONObject(t, validNativeArtifact())
			candidate["responses_sha256"] = value
			_, err := extractNativeUsage(marshalJSONObject(t, candidate), "case-a")
			if err == nil || !strings.Contains(err.Error(), "invalid responses_sha256") {
				t.Fatalf("extractNativeUsage() error = %v, want invalid SHA", err)
			}
		})
	}
}

const emptyResponsesSHA256 = "37517e5f3dc66819f61f5a7bb8ace1921282415f10551d2defa5c3eb0985b570"

func validNativeArtifact() map[string]any {
	return map[string]any{
		"instance_id": "case-a",
		"info": map[string]any{
			"workers":     1,
			"exit_status": "Submitted",
		},
		"model_patch": "",
		"duration_ms": 0,
		"llm_calls":   0,
		"tool_calls":  0,
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
			"prompt_tokens_details": map[string]any{
				"cached_tokens":         0,
				"cache_creation_tokens": 0,
				"cache_read_tokens":     0,
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": 0,
			},
			"timing_info": map[string]any{
				"time_to_first_token": 0,
				"reasoning_duration":  0,
			},
		},
		"response_count":   0,
		"responses_sha256": emptyResponsesSHA256,
	}
}

func cleanRoomNativeArtifact(
	t *testing.T,
	instanceID string,
	repo string,
	baseCommit string,
) (map[string]any, map[string]sweenv.ImageIdentity, *sweenv.OfflineAssetIdentity) {
	t.Helper()
	testbedReference := sweenv.ImageForInstance(instanceID)
	testbed := sweenv.ImageIdentity{Reference: testbedReference, ID: "sha256:" + strings.Repeat("a", 64)}
	httpbin := sweenv.ImageIdentity{
		Reference: "docker.io/kennethreitz/httpbin:latest",
		ID:        "sha256:" + strings.Repeat("b", 64),
	}
	images := map[string]sweenv.ImageIdentity{
		testbed.Reference: testbed,
		httpbin.Reference: httpbin,
	}
	imageSetSHA256, err := sweenv.ImageSetSHA256(images)
	if err != nil {
		t.Fatal(err)
	}
	offlineAssets := &sweenv.OfflineAssetIdentity{
		Schema:         "swebench-offline-assets-v1",
		SHA256:         strings.Repeat("1", 64),
		ManifestSHA256: strings.Repeat("2", 64),
		FileCount:      3,
	}
	artifact := validNativeArtifact()
	artifact["instance_id"] = instanceID
	info := artifact["info"].(map[string]any)
	info["clean_room"] = true
	info["clean_room_policy_sha256"] = strings.Repeat("3", 64)
	info["offline_assets_sha256"] = offlineAssets.SHA256
	info["image_set_sha256"] = imageSetSHA256
	info["repo"] = repo
	info["base_commit"] = baseCommit
	info["verified_base_commit"] = baseCommit
	info["environment_provenance"] = sweenv.Provenance{
		Testbed: testbed,
		AuxiliaryImages: map[string]sweenv.ImageIdentity{
			"httpbin":        httpbin,
			"network-helper": testbed,
		},
	}
	return artifact, images, offlineAssets
}

func wrongNativeFieldType(path string) any {
	switch path {
	case "info", "usage", "usage.prompt_tokens_details", "usage.completion_tokens_details":
		return "not-an-object"
	case "instance_id", "model_patch", "responses_sha256", "info.exit_status":
		return 1
	default:
		return "not-an-integer"
	}
}

func marshalJSONObject(t *testing.T, value map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func cloneJSONObject(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	var clone map[string]any
	if err := json.Unmarshal(marshalJSONObject(t, value), &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func setJSONField(t *testing.T, value map[string]any, path string, replacement any) {
	t.Helper()
	parts := strings.Split(path, ".")
	current := value
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			t.Fatalf("field %s is not an object in %+v", part, current)
		}
		current = next
	}
	current[parts[len(parts)-1]] = replacement
}

func removeJSONField(t *testing.T, value map[string]any, path string) {
	t.Helper()
	parts := strings.Split(path, ".")
	current := value
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			t.Fatalf("field %s is not an object in %+v", part, current)
		}
		current = next
	}
	delete(current, parts[len(parts)-1])
}

func TestCopyScrubbedTraceRejectsMissingAgentArtifact(t *testing.T) {
	dir := t.TempDir()
	_, _, err := copyScrubbedTrace(filepath.Join(dir, "raw"), filepath.Join(dir, "traces"), "case-a")
	if err == nil || !strings.Contains(err.Error(), "trace for case-a not found") {
		t.Fatalf("copyScrubbedTrace() error = %v, want missing trace", err)
	}
}

func TestCopyScrubbedTraceKeepsMiniGoTrajectoryBehavior(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	caseDir := filepath.Join(rawDir, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	trajectory := []byte(`{
  "info": {"api_calls": 2},
  "usage": {
    "prompt_tokens": 20,
    "completion_tokens": 5,
    "total_tokens": 25,
    "prompt_tokens_details": {"cached_tokens": 8},
    "completion_tokens_details": {"reasoning_tokens": 3}
  }
}`)
	if err := os.WriteFile(filepath.Join(caseDir, "case-a.traj.json"), trajectory, 0o600); err != nil {
		t.Fatal(err)
	}
	traceDir := filepath.Join(dir, "traces")
	path, usage, err := copyScrubbedTrace(rawDir, traceDir, "case-a")
	if err != nil {
		t.Fatal(err)
	}
	want := usageStats{
		PromptTokens: 20, CachedTokens: 8, UncachedTokens: 12,
		CompletionTokens: 5, ReasoningTokens: 3, TotalTokens: 25,
		APICalls: 2,
	}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
	if path != filepath.Join(traceDir, "case-a.json") {
		t.Fatalf("trace path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scrubbed Mini-Go trace: %v", err)
	}
}
