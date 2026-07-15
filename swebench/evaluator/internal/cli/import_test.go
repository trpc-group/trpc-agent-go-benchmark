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
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

func TestRunImportWritesMiniGoField(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {ModelNameOrPath: "model", InstanceID: "case-a", ModelPatch: "patch"},
	})
	output := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", targetMiniGo,
		"--predictions", filepath.Join(rawDir, "preds.json"),
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
	if row.MiniGo == nil {
		t.Fatalf("imported row = %+v", row)
	}
}

func TestRunImportWritesTAGFieldAndUsage(t *testing.T) {
	dir := t.TempDir()
	rawDir := filepath.Join(dir, "raw")
	writeTestPreds(t, rawDir, map[string]contract.Prediction{
		"case-a": {ModelNameOrPath: "tag/model", InstanceID: "case-a", ModelPatch: "patch"},
	})
	caseDir := filepath.Join(rawDir, "case-a")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case-a.tag.json"), []byte(`{"llm_calls":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case-a.responses.json"), []byte(`[{"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "imported")
	if err := runImport([]string{
		"--target", targetTAG,
		"--predictions", filepath.Join(rawDir, "preds.json"),
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
	if row.TAG == nil || row.MiniGo != nil {
		t.Fatalf("imported row = %+v", row)
	}
	if row.TAG.Usage.PromptTokens != 10 || row.TAG.Usage.PromptCachedTokens != 4 || row.TAG.Usage.APICalls != 1 {
		t.Fatalf("usage = %+v", row.TAG.Usage)
	}
}
