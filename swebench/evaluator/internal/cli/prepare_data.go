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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	benchmarkdata "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/data"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

type prepareDataOutput struct {
	Revision string          `json:"revision"`
	Cases    []contract.Case `json:"cases"`
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

type expectedCaseFile struct {
	Source string
	Data   []byte
}

func runPrepareData(ctx context.Context, args []string) error {
	fs := newFlagSet("prepare-data")
	output := fs.String("output", "data/generated", "output directory for cases.jsonl and cases.sha256")
	dataset := fs.String("dataset", defaultDatasetName, "SWE-Bench dataset name")
	split := fs.String("split", defaultSplit, "dataset split")
	python := fs.String("python", envOrDefault("PYTHON", "python"), "python executable")
	includeHints := fs.Bool("include-hints", false, "include hints_text when present")
	expectedCaseIDs := fs.String("expected-case-ids", "", "expected case id list; required by default for SWE-Bench Verified/test")
	expectedCaseHash := fs.String("expected-case-hash", "", "expected case id list hash; required by default for SWE-Bench Verified/test")
	if err := fs.Parse(args); err != nil {
		return err
	}
	expectedIDs, expectedHash, err := loadExpectedCaseFiles(
		*dataset,
		*split,
		*expectedCaseIDs,
		*expectedCaseHash,
	)
	if err != nil {
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
	if len(expectedIDs.Data) > 0 {
		if err := validateExpectedCaseIDsData(expectedIDs.Source, expectedIDs.Data, loaded.Cases); err != nil {
			return err
		}
	}
	if len(expectedHash.Data) > 0 {
		if err := validateExpectedCaseHashData(expectedHash.Source, expectedHash.Data, hash); err != nil {
			return err
		}
	}
	if err := ensureDir(*output); err != nil {
		return err
	}
	casesPath := filepath.Join(*output, "cases.jsonl")
	if err := artifact.WriteCasesJSONL(casesPath, loaded.Cases); err != nil {
		return err
	}
	if err := artifact.WriteFileAtomic(filepath.Join(*output, "cases.sha256"), []byte(hash+"\n"), 0o644); err != nil {
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

func validateCaseList(cases []contract.Case) error {
	seen := map[string]bool{}
	for _, c := range cases {
		if err := validateArtifactName("instance id", c.InstanceID); err != nil {
			return err
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

func caseListHash(cases []contract.Case) string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.InstanceID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

func validateExpectedCaseIDs(path string, cases []contract.Case) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read expected case ids: %w", err)
	}
	return validateExpectedCaseIDsData(path, data, cases)
}

func validateExpectedCaseIDsData(source string, data []byte, cases []contract.Case) error {
	expected := nonEmptyLines(string(data))
	if len(expected) == 0 {
		return fmt.Errorf("expected case id list %s is empty", source)
	}
	for i, id := range expected {
		if err := validateArtifactName("expected instance id", id); err != nil {
			return fmt.Errorf("%s line %d: %w", source, i+1, err)
		}
		if i > 0 && expected[i-1] >= id {
			if expected[i-1] == id {
				return fmt.Errorf("expected case id list %s contains duplicate %q", source, id)
			}
			return fmt.Errorf("expected case id list %s is not sorted at %q", source, id)
		}
	}
	actual := make([]string, 0, len(cases))
	for _, c := range cases {
		actual = append(actual, c.InstanceID)
	}
	sort.Strings(actual)
	if len(expected) != len(actual) {
		return fmt.Errorf("case id list mismatch: expected %d cases from %s, got %d", len(expected), source, len(actual))
	}
	for i := range expected {
		if expected[i] != actual[i] {
			return fmt.Errorf("case id list mismatch at sorted index %d: expected %q, got %q", i, expected[i], actual[i])
		}
	}
	return nil
}

func validateExpectedCaseHash(path, actual string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read expected case hash: %w", err)
	}
	return validateExpectedCaseHashData(path, data, actual)
}

func validateExpectedCaseHashData(source string, data []byte, actual string) error {
	expected := strings.TrimSpace(string(data))
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("invalid case list hash in %s: expected %d hex characters", source, sha256.Size*2)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("invalid case list hash in %s: %w", source, err)
	}
	if expected != actual {
		return fmt.Errorf("case list hash mismatch: expected %s from %s, got %s", expected, source, actual)
	}
	return nil
}

func nonEmptyLines(s string) []string {
	lines := []string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func loadExpectedCaseFiles(dataset, split, idsPath, hashPath string) (expectedCaseFile, expectedCaseFile, error) {
	ids, err := loadExpectedCaseFile("expected case id list", idsPath)
	if err != nil {
		return expectedCaseFile{}, expectedCaseFile{}, err
	}
	hash, err := loadExpectedCaseFile("expected case hash", hashPath)
	if err != nil {
		return expectedCaseFile{}, expectedCaseFile{}, err
	}
	if dataset == defaultDatasetName && split == defaultSplit {
		if len(ids.Data) == 0 {
			ids = expectedCaseFile{
				Source: "embedded:data/case-lists/verified-test-500.case_ids.txt",
				Data:   benchmarkdata.VerifiedTest500CaseIDs(),
			}
		}
		if len(hash.Data) == 0 {
			hash = expectedCaseFile{
				Source: "embedded:data/case-lists/verified-test-500.case_ids.sha256",
				Data:   benchmarkdata.VerifiedTest500CaseHash(),
			}
		}
	}
	return ids, hash, nil
}

func loadExpectedCaseFile(kind, path string) (expectedCaseFile, error) {
	if strings.TrimSpace(path) == "" {
		return expectedCaseFile{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return expectedCaseFile{}, fmt.Errorf("%s %s: %w", kind, path, err)
	}
	if !info.Mode().IsRegular() {
		return expectedCaseFile{}, fmt.Errorf("%s %s is not a regular file", kind, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return expectedCaseFile{}, fmt.Errorf("read %s %s: %w", kind, path, err)
	}
	if len(data) == 0 {
		return expectedCaseFile{}, fmt.Errorf("%s %s is empty", kind, path)
	}
	return expectedCaseFile{Source: path, Data: data}, nil
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
