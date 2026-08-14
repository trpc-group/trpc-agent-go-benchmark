//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
)

func TestParseQuotas(t *testing.T) {
	got, err := parseQuotas("multi-session=3, temporal-reasoning=2")
	if err != nil {
		t.Fatalf("parseQuotas() error = %v", err)
	}
	want := map[string]int{
		"multi-session":      3,
		"temporal-reasoning": 2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseQuotas() = %#v, want %#v", got, want)
	}
}

func TestParseQuotasRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "missing equals", value: "multi-session", want: "want question-type=count"},
		{name: "invalid number", value: "multi-session=many", want: "parse quota"},
		{name: "negative", value: "multi-session=-1", want: "must not be negative"},
		{name: "duplicate", value: "multi-session=1,multi-session=2", want: "duplicate quota"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseQuotas(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseQuotas(%q) error = %v, want containing %q", test.value, err, test.want)
			}
		})
	}
}

func TestParseCommandOptionsDefaultsToStratifiedSelection(t *testing.T) {
	opts, err := parseCommandOptions(
		[]string{"-dataset", "dataset.json", "-output", "manifest.json"},
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatalf("parseCommandOptions() error = %v", err)
	}
	if opts.method != "stratified-sha256" {
		t.Fatalf("method = %q, want stratified-sha256", opts.method)
	}
	if opts.perType != 0 {
		t.Fatalf("perType = %d, want 0", opts.perType)
	}
}

func TestRunHelp(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"-h"}, &output); err != nil {
		t.Fatalf("run(-h) error = %v", err)
	}
	if !strings.Contains(output.String(), "Usage of longmemeval-manifest") {
		t.Fatalf("run(-h) output = %q", output.String())
	}
}

func TestRunGenerateAndVerify(t *testing.T) {
	datasetPath := writeTestDataset(t)
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	var output bytes.Buffer
	err := run([]string{
		"-dataset", datasetPath,
		"-method", "stratified-sha256",
		"-seed", "cli-seed",
		"-types", "type-a,type-b",
		"-total-size", "4",
		"-output", manifestPath,
	}, &output)
	if err != nil {
		t.Fatalf("run(generate) error = %v", err)
	}
	manifest, err := dataset.LoadLongMemEvalManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadLongMemEvalManifest() error = %v", err)
	}
	if manifest.TotalSize != 4 || manifest.SchemaVersion != dataset.LongMemEvalManifestSchemaVersion {
		t.Fatalf("generated manifest = %#v", manifest)
	}
	output.Reset()
	err = run([]string{
		"-action", "verify",
		"-dataset", datasetPath,
		"-manifest", manifestPath,
	}, &output)
	if err != nil {
		t.Fatalf("run(verify) error = %v", err)
	}
	if !strings.Contains(output.String(), "verified") {
		t.Fatalf("verify output = %q", output.String())
	}
	for _, selectionFlag := range [][]string{
		{"-method", "full-category"},
		{"-types", "type-a"},
	} {
		args := []string{
			"-action", "verify",
			"-dataset", datasetPath,
			"-manifest", manifestPath,
		}
		args = append(args, selectionFlag...)
		if err := run(args, &bytes.Buffer{}); err == nil ||
			!strings.Contains(err.Error(), "generation and split flags") {
			t.Fatalf("run(verify %v) error = %v", selectionFlag, err)
		}
	}
}

func TestRunGenerateIsByteStable(t *testing.T) {
	datasetPath := writeTestDataset(t)
	outputDir := t.TempDir()
	firstPath := filepath.Join(outputDir, "first.json")
	secondPath := filepath.Join(outputDir, "second.json")
	for _, outputPath := range []string{firstPath, secondPath} {
		err := run([]string{
			"-dataset", datasetPath,
			"-seed", "repeatable-cli-seed",
			"-types", "type-a,type-b",
			"-total-size", "7",
			"-output", outputPath,
		}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("run(generate %s) error = %v", outputPath, err)
		}
	}
	first, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("os.ReadFile(first) error = %v", err)
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("os.ReadFile(second) error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same dataset and seed produced different manifest bytes")
	}
}

func TestRunRejectsInvalidManifestConfig(t *testing.T) {
	datasetPath := writeTestDataset(t)
	outputDir := t.TempDir()
	samePath := filepath.Join(outputDir, "same.json")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "default selection requires seed",
			args: []string{
				"-dataset", datasetPath, "-types", "type-a", "-total-size", "2",
				"-output", filepath.Join(outputDir, "missing-seed.json"),
			},
			want: "seed is required",
		},
		{
			name: "negative per type",
			args: []string{
				"-dataset", datasetPath, "-per-type", "-1",
				"-output", filepath.Join(outputDir, "negative.json"),
			},
			want: "must not be negative",
		},
		{
			name: "quota shortage",
			args: []string{
				"-dataset", datasetPath, "-seed", "quota-shortage", "-types", "type-a",
				"-quotas", "type-a=7", "-output", filepath.Join(outputDir, "shortage.json"),
			},
			want: "only 6 cases are available",
		},
		{
			name: "same split output",
			args: []string{
				"-action", "split", "-dataset", datasetPath, "-seed", "same-output",
				"-types", "type-a", "-dev-size", "2", "-holdout-size", "2",
				"-dev-output", samePath, "-holdout-output", filepath.Join(outputDir, ".", "same.json"),
			},
			want: "must be different files",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRunSplitAndVerifyPair(t *testing.T) {
	datasetPath := writeTestDataset(t)
	outputDir := t.TempDir()
	devPath := filepath.Join(outputDir, "dev.json")
	holdoutPath := filepath.Join(outputDir, "holdout.json")
	var output bytes.Buffer
	err := run([]string{
		"-action", "split",
		"-dataset", datasetPath,
		"-seed", "split-cli-seed",
		"-types", "type-a,type-b",
		"-dev-size", "4",
		"-holdout-size", "4",
		"-dev-output", devPath,
		"-holdout-output", holdoutPath,
	}, &output)
	if err != nil {
		t.Fatalf("run(split) error = %v", err)
	}
	holdout, err := dataset.LoadLongMemEvalManifest(holdoutPath)
	if err != nil {
		t.Fatalf("LoadLongMemEvalManifest(holdout) error = %v", err)
	}
	if len(holdout.RankOffsets) != 2 {
		t.Fatalf("holdout.RankOffsets = %#v, want both question types", holdout.RankOffsets)
	}
	output.Reset()
	err = run([]string{
		"-action", "verify",
		"-dataset", datasetPath,
		"-manifest", holdoutPath,
	}, &output)
	if err != nil {
		t.Fatalf("run(verify standalone holdout) error = %v", err)
	}
	output.Reset()
	err = run([]string{
		"-action", "verify",
		"-dataset", datasetPath,
		"-manifest", devPath,
		"-holdout-manifest", holdoutPath,
	}, &output)
	if err != nil {
		t.Fatalf("run(verify pair) error = %v", err)
	}
	if !strings.Contains(output.String(), "holdout=") {
		t.Fatalf("verify pair output = %q", output.String())
	}
}

func writeTestDataset(t *testing.T) string {
	t.Helper()
	instances := make([]*dataset.LongMemEvalInstance, 0, 12)
	for _, questionType := range []string{"type-a", "type-b"} {
		for i := 0; i < 6; i++ {
			instances = append(instances, &dataset.LongMemEvalInstance{
				QuestionID:         questionType + "-" + string(rune('a'+i)),
				QuestionType:       questionType,
				Question:           "Question",
				QuestionDate:       "2026-01-02",
				RawAnswer:          json.RawMessage(`"Answer"`),
				AnswerSessionIDs:   []string{"session-1"},
				HaystackDates:      []string{"2026-01-01"},
				HaystackSessionIDs: []string{"session-1"},
				HaystackSessions: [][]dataset.LongMemEvalTurn{{
					{Role: "user", Content: "Hello"},
					{Role: "assistant", Content: "Hi"},
				}},
			})
		}
	}
	data, err := json.Marshal(instances)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "dataset.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
