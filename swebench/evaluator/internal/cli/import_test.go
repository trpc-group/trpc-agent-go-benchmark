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
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
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
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, []byte(`{"resolved_ids":"case-a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHarnessReport(reportPath); err == nil {
		t.Fatal("readHarnessReport() accepted a non-array resolved_ids field")
	}
}
