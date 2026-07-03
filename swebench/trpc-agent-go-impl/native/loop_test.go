//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"strings"
	"testing"
)

func TestParseActionStrictJSON(t *testing.T) {
	action, err := parseAction(`{"thought":"inspect","command":"ls -la"}`)
	if err != nil {
		t.Fatalf("parseAction() error = %v", err)
	}
	if action.Thought != "inspect" || action.Command != "ls -la" {
		t.Fatalf("parseAction() = %+v", action)
	}
}

func TestParseActionLenientHeredocCommand(t *testing.T) {
	raw := `{"thought":"edit file","command":"python - <<'EOF'
print('hello')
EOF"}`
	action, err := parseAction(raw)
	if err != nil {
		t.Fatalf("parseAction() error = %v", err)
	}
	want := "python - <<'EOF'\nprint('hello')\nEOF"
	if action.Command != want {
		t.Fatalf("command = %q, want %q", action.Command, want)
	}
}

func TestParseActionLenientEscapedShellPipe(t *testing.T) {
	action, err := parseAction(`{"thought":"grep tests","command":"grep -n 'a\|b' file.py"}`)
	if err != nil {
		t.Fatalf("parseAction() error = %v", err)
	}
	if action.Command != "grep -n 'a|b' file.py" {
		t.Fatalf("command = %q", action.Command)
	}
}

func TestObservationMessageTruncatedOutput(t *testing.T) {
	msg := observationMessage(commandResult{
		Output:          "head\n... output truncated ...\ntail",
		ExitCode:        0,
		OutputTruncated: true,
		OutputBytes:     20000,
	})
	if !containsAll(msg, "<warning>", "<returncode>0</returncode>", "<output_head>", "<output_tail>") {
		t.Fatalf("observationMessage() missing expected sections:\n%s", msg)
	}
}

func TestCompactMessagesSummarizesOlderSteps(t *testing.T) {
	messages := []chatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "task"},
	}
	steps := make([]stepTrace, 0, 12)
	for i := 1; i <= 12; i++ {
		steps = append(steps, stepTrace{
			Step: i,
			Action: agentAction{
				Command: "echo step",
			},
			Command: commandResult{ExitCode: 0, Command: "echo step"},
		})
		messages = append(messages,
			chatMessage{Role: "assistant", Content: "assistant"},
			chatMessage{Role: "user", Content: "observation"},
		)
	}
	compacted := compactMessages(messages, steps)
	if len(compacted) >= len(messages) {
		t.Fatalf("compactMessages() did not compact: got %d want < %d", len(compacted), len(messages))
	}
	if compacted[0].Content != "system" || compacted[1].Content != "task" {
		t.Fatalf("compactMessages() did not preserve prefix: %+v", compacted[:2])
	}
	if !containsAll(compacted[2].Content, "<previous_steps_summary>", "step 1") {
		t.Fatalf("summary missing older steps: %q", compacted[2].Content)
	}
}

func TestExtractSubmittedPatch(t *testing.T) {
	patch := "diff --git a/a.py b/a.py\n--- a/a.py\n+++ b/a.py\n@@ -1 +1 @@\n-a\n+b\n"
	got := extractSubmittedPatch("COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\n" + patch)
	if got != strings.TrimSpace(patch) {
		t.Fatalf("extractSubmittedPatch() = %q", got)
	}
	files := patchChangedFiles(got)
	if len(files) != 1 || files[0] != "a.py" {
		t.Fatalf("patchChangedFiles() = %v", files)
	}
}

func TestRepeatedCommand(t *testing.T) {
	var last string
	var repeated int
	for i := 0; i < maxRepeatedCommands; i++ {
		if repeatedCommand("echo   hello", &last, &repeated) {
			t.Fatalf("repeat %d should still be allowed", i+1)
		}
	}
	if !repeatedCommand("echo hello", &last, &repeated) {
		t.Fatal("repeat beyond limit should be rejected")
	}
	if repeatedCommand("echo goodbye", &last, &repeated) {
		t.Fatal("different command should reset repeat counter")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
