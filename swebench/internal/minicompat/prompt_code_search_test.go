//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package minicompat

import (
	"strings"
	"testing"
)

func TestCodeSearchSystemPrompt(t *testing.T) {
	const want = "You are a helpful assistant that can interact with a computer shell to solve programming tasks. You can also use code_search to search a static task-start snapshot of the workspace."
	if SystemPromptWithCodeSearch != want {
		t.Fatalf("SystemPromptWithCodeSearch = %q, want %q", SystemPromptWithCodeSearch, want)
	}
}

func TestPromptForTaskWithCodeSearch(t *testing.T) {
	const task = "Fix Builder validation."
	base := PromptForTask(task)
	got := PromptForTaskWithCodeSearch(task)

	assertContainsOnce(t, got, "## Tool Execution Rules")
	if strings.Contains(got, "## Command Execution Rules") {
		t.Fatal("code-search prompt retained the command-only section title")
	}
	assertContainsOnce(t, got, codeSearchRouting)
	assertContainsOnce(t, got, codeSearchExample)

	routingAt := strings.Index(got, codeSearchRouting)
	workflowAt := strings.Index(got, "## Recommended Workflow")
	workflowListAt := strings.Index(got, "1. Analyze the codebase by finding and reading relevant files")
	if workflowAt < 0 || routingAt < workflowAt || workflowListAt < routingAt {
		t.Fatalf("routing placement is invalid: workflow=%d routing=%d list=%d", workflowAt, routingAt, workflowListAt)
	}

	codeSearchExampleAt := strings.Index(got, codeSearchExample)
	bashExampleAt := strings.Index(got, `[Makes multiple bash tool calls: {"command": "ls -la"}`)
	if codeSearchExampleAt < 0 || bashExampleAt < codeSearchExampleAt {
		t.Fatalf("example order is invalid: code_search=%d bash=%d", codeSearchExampleAt, bashExampleAt)
	}

	criticalLines := []string{
		"**CRITICAL REQUIREMENTS:**",
		"- Your response SHOULD include reasoning text explaining what you're doing",
		"- Your response MUST include AT LEAST ONE tool call. You can make MULTIPLE tool calls in a single response when the calls are independent (e.g., searching multiple files, reading different parts of the codebase).",
		"- Directory or environment variable changes are not persistent. Every Bash action is executed in a new subshell.",
		"- However, you can prefix any Bash action with `MY_ENV_VAR=MY_VALUE cd /path/to/working/dir && ...` or write/load environment variables from files",
	}
	for _, line := range criticalLines {
		assertContainsOnce(t, got, line)
	}

	const unchangedSuffix = "## Environment Details"
	baseSuffix := strings.Index(base, unchangedSuffix)
	gotSuffix := strings.Index(got, unchangedSuffix)
	if baseSuffix < 0 || gotSuffix < 0 || base[baseSuffix:] != got[gotSuffix:] {
		t.Fatal("environment and submission sections changed in code-search prompt")
	}
}

func TestPromptForTaskWithCodeSearchDoesNotRewriteTask(t *testing.T) {
	task := "Document ## Command Execution Rules and the phrase issue at least one command."
	got := PromptForTaskWithCodeSearch(task)
	if !strings.Contains(got, "<pr_description>\nConsider the following PR description:\n"+task+"\n</pr_description>") {
		t.Fatalf("task was rewritten in code-search prompt: %q", got)
	}
}

func assertContainsOnce(t *testing.T, value, substring string) {
	t.Helper()
	if count := strings.Count(value, substring); count != 1 {
		t.Fatalf("substring %q occurs %d times, want 1", substring, count)
	}
}
