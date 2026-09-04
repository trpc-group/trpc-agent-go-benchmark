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
	"os"
	"path/filepath"
	"strings"
	"testing"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/tasks"
)

func TestParseModeSeparatesComparisonFromSingleArm(t *testing.T) {
	tests := []struct {
		input   string
		want    bench.Mode
		compare bool
	}{
		{input: "static", want: bench.ModeStaticAll},
		{input: "dynamic-session", want: bench.ModeDynamicActivation},
		{input: "compare", compare: true},
		{input: "", compare: true},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			mode, compare, err := parseMode(test.input)
			if err != nil {
				t.Fatalf("parseMode(%q): %v", test.input, err)
			}
			if mode != test.want || compare != test.compare {
				t.Fatalf("parseMode(%q) = (%q, %t), want (%q, %t)", test.input, mode, compare, test.want, test.compare)
			}
		})
	}
	if _, _, err := parseMode("unknown"); err == nil {
		t.Fatal("parseMode should reject unknown mode")
	}
}

func TestSuiteWithTaskKeepsCapabilitiesAndSelectsOneTask(t *testing.T) {
	suite := tasks.MustDefaultSuite()
	wantSkills, wantTools := len(suite.Skills), len(suite.Tools)
	selected, err := suiteWithTask(suite, "files-archive-meeting")
	if err != nil {
		t.Fatalf("suiteWithTask: %v", err)
	}
	if len(selected.Tasks) != 1 || selected.Tasks[0].ID != "files-archive-meeting" {
		t.Fatalf("selected tasks = %#v, want files-archive-meeting only", selected.Tasks)
	}
	if len(selected.Skills) != wantSkills || len(selected.Tools) != wantTools {
		t.Fatalf("selection changed capability menu: skills=%d/%d tools=%d/%d", len(selected.Skills), wantSkills, len(selected.Tools), wantTools)
	}
	if len(suite.Tasks) == len(selected.Tasks) {
		t.Fatalf("suiteWithTask mutated the caller's task slice")
	}
}

func TestSuiteWithTaskRejectsBlankAndUnknownTask(t *testing.T) {
	suite := tasks.MustDefaultSuite()
	for _, taskID := range []string{"", "does-not-exist"} {
		t.Run(taskID, func(t *testing.T) {
			if _, err := suiteWithTask(suite, taskID); err == nil {
				t.Fatalf("suiteWithTask(%q) unexpectedly succeeded", taskID)
			}
		})
	}
}

func TestSuiteToolSetCountIncludesImplicitSkillToolSets(t *testing.T) {
	suite := bench.Suite{
		Tools: []bench.ToolSpec{{Name: "lookup", Skill: "research"}},
	}
	if got := suiteToolSetCount(suite); got != 1 {
		t.Fatalf("suiteToolSetCount = %d, want implicit default ToolSet", got)
	}
}

func TestSelectModelFailsClosedWithoutProviderKey(t *testing.T) {
	t.Setenv("ACTIVATIONBENCH_TEST_KEY", "")
	t.Setenv("MODEL_NAME", "test-model")
	_, err := selectModel("openai-compatible", "", "http://127.0.0.1:1/v1", "ACTIVATIONBENCH_TEST_KEY", tasks.MustDefaultSuite())
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("selectModel error = %v, want missing API key", err)
	}
}

func TestOpenAIConfigReadsExplicitEnvironment(t *testing.T) {
	t.Setenv("ACTIVATIONBENCH_TEST_KEY", "test-key")
	t.Setenv("MODEL_NAME", "ambient-model")
	t.Setenv("OPENAI_BASE_URL", "https://ambient.example/v1")
	config, err := openAIConfigFromEnv("explicit-model", "https://model.example/v1", "ACTIVATIONBENCH_TEST_KEY")
	if err != nil {
		t.Fatalf("openAIConfigFromEnv: %v", err)
	}
	if config.model != "explicit-model" || config.baseURL != "https://model.example/v1" {
		t.Fatalf("config model=%q baseURL=%q, want explicit model/base URL", config.model, config.baseURL)
	}
}

func TestOpenAIConfigRejectsUnsafeEndpoint(t *testing.T) {
	t.Setenv("ACTIVATIONBENCH_TEST_KEY", "test-key")
	if _, err := openAIConfigFromEnv("model", "file:///tmp/model", "ACTIVATIONBENCH_TEST_KEY"); err == nil || !strings.Contains(err.Error(), "HTTP(S)") {
		t.Fatalf("unsafe endpoint error = %v, want HTTP(S) validation", err)
	}
}

func TestSelectModelRejectsUnknownSource(t *testing.T) {
	_, err := selectModel("something-else", "", "", "", tasks.MustDefaultSuite())
	if err == nil || !strings.Contains(err.Error(), "model-source") {
		t.Fatalf("selectModel error = %v, want source validation", err)
	}
}

func TestSelectModelRejectsRemovedMockSource(t *testing.T) {
	_, err := selectModel("mock", "", "", "", tasks.MustDefaultSuite())
	if err == nil || !strings.Contains(err.Error(), "openai-compatible") {
		t.Fatalf("selectModel mock error = %v, want provider-only validation", err)
	}
}

func TestWriteReportKeepsIncompleteRunDiagnostics(t *testing.T) {
	aggregate := metrics.NewAggregate("static-all", []metrics.RunResult{{
		Passed: true,
	}, {
		Error: "context deadline exceeded",
	}})
	value := report{
		Benchmark: "report-test",
		Mode:      "static",
		Status:    "incomplete",
		RunErrors: []string{"static-all repetition 0: context deadline exceeded"},
		Aggregate: &aggregate,
		Results:   []metrics.RunResult{{TaskID: "task-1", Passed: true}, {TaskID: "task-2", Error: "context deadline exceeded"}},
	}
	dir := t.TempDir()
	if err := writeReport(dir, value); err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	text := string(data)
	for _, want := range []string{"\"status\": \"incomplete\"", "\"run_errors\"", "\"error_runs\": 1", "context deadline exceeded"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q: %s", want, text)
		}
	}
	summary, err := os.ReadFile(filepath.Join(dir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	for _, want := range []string{"status=incomplete", "run_errors=", "evaluated=1/2 errors=1", "observed_pass=", "streaming=false"} {
		if !strings.Contains(string(summary), want) {
			t.Fatalf("summary missing %q: %s", want, summary)
		}
	}
}
