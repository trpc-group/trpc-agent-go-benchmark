//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type prepareDataOutput struct {
	Revision string         `json:"revision"`
	Cases    []caseManifest `json:"cases"`
}

type prepareDataManifest struct {
	Dataset         string    `json:"dataset"`
	Split           string    `json:"split"`
	Revision        string    `json:"revision,omitempty"`
	CaseCount       int       `json:"case_count"`
	CaseListHash    string    `json:"case_list_hash"`
	IncludeHints    bool      `json:"include_hints"`
	HintsTextPolicy string    `json:"hints_text_policy"`
	OutputDir       string    `json:"output_dir"`
	GeneratedAt     time.Time `json:"generated_at"`
	SourceFields    []string  `json:"source_fields"`
	ExcludedFields  []string  `json:"excluded_fields"`
}

func runPrepareData(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prepare-data", flag.ExitOnError)
	output := fs.String("output", "../data", "output directory for cases.jsonl and cases.sha256")
	dataset := fs.String("dataset", defaultDatasetName, "SWE-Bench dataset name")
	split := fs.String("split", defaultSplit, "dataset split")
	python := fs.String("python", envOrDefault("PYTHON", "python"), "python executable")
	includeHints := fs.Bool("include-hints", false, "include hints_text when present")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureDir(*output); err != nil {
		return err
	}

	loaded, err := loadSafeCases(ctx, *python, *dataset, *split, *includeHints)
	if err != nil {
		return err
	}
	if len(loaded.Cases) == 0 {
		return fmt.Errorf("dataset returned zero cases")
	}
	sort.Slice(loaded.Cases, func(i, j int) bool {
		return loaded.Cases[i].InstanceID < loaded.Cases[j].InstanceID
	})
	if err := validateCaseList(loaded.Cases); err != nil {
		return err
	}

	hash := caseListHash(loaded.Cases)
	casesPath := filepath.Join(*output, "cases.jsonl")
	if err := writeCasesJSONL(casesPath, loaded.Cases); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "cases.sha256"), []byte(hash+"\n"), 0o644); err != nil {
		return err
	}
	manifest := prepareDataManifest{
		Dataset:         *dataset,
		Split:           *split,
		Revision:        loaded.Revision,
		CaseCount:       len(loaded.Cases),
		CaseListHash:    hash,
		IncludeHints:    *includeHints,
		HintsTextPolicy: hintsPolicy(*includeHints),
		OutputDir:       absPath(*output),
		GeneratedAt:     time.Now().UTC(),
		SourceFields:    sourceFields(*includeHints),
		ExcludedFields:  []string{"patch", "test_patch", "FAIL_TO_PASS", "PASS_TO_PASS"},
	}
	if err := writeJSON(filepath.Join(*output, "cases.manifest.json"), manifest); err != nil {
		return err
	}
	fmt.Printf("wrote %d cases\ncases=%s\nsha256=%s\n", len(loaded.Cases), casesPath, hash)
	return nil
}

func loadSafeCases(ctx context.Context, python, dataset, split string, includeHints bool) (prepareDataOutput, error) {
	script := `
import json
import sys
from datasets import load_dataset

dataset = sys.argv[1]
split = sys.argv[2]
include_hints = sys.argv[3].lower() == "true"
ds = load_dataset(dataset, split=split)
revision = ""
try:
    revision = getattr(ds, "_fingerprint", "") or ""
except Exception:
    revision = ""
try:
    from huggingface_hub import HfApi
    revision = HfApi().dataset_info(dataset).sha or revision
except Exception:
    pass

cases = []
for row in ds:
    item = {
        "instance_id": row.get("instance_id", ""),
        "repo": row.get("repo", ""),
        "base_commit": row.get("base_commit", ""),
        "problem_statement": row.get("problem_statement", ""),
    }
    if include_hints and row.get("hints_text"):
        item["hints_text"] = row.get("hints_text", "")
    cases.append(item)
print(json.dumps({"revision": revision, "cases": cases}, ensure_ascii=False))
`
	res := runCapture(ctx, "", nil, python, "-c", script, dataset, split, fmt.Sprintf("%t", includeHints))
	if res.ExitCode != 0 {
		return prepareDataOutput{}, fmt.Errorf("load dataset failed: %s %s", res.Error, strings.TrimSpace(res.Stderr+"\n"+res.Stdout))
	}
	var out prepareDataOutput
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return prepareDataOutput{}, fmt.Errorf("parse dataset output: %w", err)
	}
	return out, nil
}

func validateCaseList(cases []caseManifest) error {
	seen := map[string]bool{}
	for _, c := range cases {
		if strings.TrimSpace(c.InstanceID) == "" {
			return fmt.Errorf("case with empty instance_id")
		}
		if seen[c.InstanceID] {
			return fmt.Errorf("duplicate instance_id %q", c.InstanceID)
		}
		seen[c.InstanceID] = true
		if strings.TrimSpace(c.ProblemStatement) == "" {
			return fmt.Errorf("case %s has empty problem_statement", c.InstanceID)
		}
	}
	return nil
}

func caseListHash(cases []caseManifest) string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.InstanceID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

func writeCasesJSONL(path string, cases []caseManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, c := range cases {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func hintsPolicy(includeHints bool) string {
	if includeHints {
		return "include-when-present"
	}
	return "not-used"
}

func sourceFields(includeHints bool) []string {
	fields := []string{"instance_id", "repo", "base_commit", "problem_statement"}
	if includeHints {
		fields = append(fields, "hints_text")
	}
	return fields
}
