//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

func TestReadPredictionsRejectsDuplicateArrayIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	writeTestFile(t, path, `[
  {"instance_id":"case-a","model_patch":"one"},
  {"instance_id":"case-a","model_patch":"two"}
]`)
	if _, err := ReadPredictions(path); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("ReadPredictions() error = %v, want duplicate error", err)
	}
}

func TestReadPredictionsRejectsDuplicateJSONLIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.jsonl")
	writeTestFile(t, path, "{\"instance_id\":\"case-a\"}\n{\"instance_id\":\"case-a\"}\n")
	if _, err := ReadPredictions(path); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("ReadPredictions() error = %v, want duplicate error", err)
	}
}

func TestReadPredictionsRejectsMapKeyMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	writeTestFile(t, path, `{
  "case-a":{"instance_id":"case-b","model_patch":"patch"}
}`)
	if _, err := ReadPredictions(path); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ReadPredictions() error = %v, want key mismatch error", err)
	}
}

func TestReadPredictionsRejectsDuplicateMapKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	writeTestFile(t, path, `{
  "case-a":{"model_patch":"one"},
  "case-a":{"model_patch":"two"}
}`)
	if _, err := ReadPredictions(path); err == nil || !strings.Contains(err.Error(), "duplicate top-level key") {
		t.Fatalf("ReadPredictions() error = %v, want duplicate key error", err)
	}
}

func TestReadPredictionsFillsMissingMapInstanceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	writeTestFile(t, path, `{"case-a":{"model_patch":"patch"}}`)
	preds, err := ReadPredictions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := preds["case-a"].InstanceID; got != "case-a" {
		t.Fatalf("InstanceID = %q, want case-a", got)
	}
}

func TestReadPredictionsRejectsEmptyInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	writeTestFile(t, path, "\n")
	if _, err := ReadPredictions(path); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("ReadPredictions() error = %v, want empty error", err)
	}
}

func TestReadPredictionsRejectsEmptyCollections(t *testing.T) {
	for _, data := range []string{"{}", "[]", "null"} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "preds.json")
			writeTestFile(t, path, data)
			if _, err := ReadPredictions(path); err == nil || !strings.Contains(err.Error(), "no predictions") {
				t.Fatalf("ReadPredictions() error = %v, want no predictions error", err)
			}
		})
	}
}

func TestCasesJSONLRoundTripLargeProblemAndRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cases.jsonl")
	cases := []contract.Case{{
		InstanceID:       "case-a",
		ProblemStatement: strings.Repeat("x", 128*1024),
	}}
	if err := WriteCasesJSONL(path, cases); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCasesJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ProblemStatement != cases[0].ProblemStatement {
		t.Fatalf("ReadCasesJSONL() did not preserve the case")
	}
	if err := WriteCasesJSONL(path, []contract.Case{{InstanceID: "dup"}, {InstanceID: "dup"}}); err == nil {
		t.Fatal("WriteCasesJSONL() accepted duplicate instance ids")
	}
}

func TestWriteJSONLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "value.json")
	if err := WriteJSON(path, map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "value.json" {
		t.Fatalf("directory entries = %v, want only value.json", entries)
	}
}

func TestWriteJSONMarshalFailurePreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.json")
	writeTestFile(t, path, "original\n")
	if err := WriteJSON(path, map[string]any{"invalid": make(chan int)}); err == nil {
		t.Fatal("WriteJSON() unexpectedly accepted an unsupported value")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "original\n" {
		t.Fatalf("destination = %q, want original content", got)
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
