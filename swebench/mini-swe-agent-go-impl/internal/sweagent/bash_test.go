//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweagent

import (
	"context"
	"strings"
	"testing"

	environment "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type fakeEnvironment struct {
	commands []string
	results  []environment.CommandResult
}

func (f *fakeEnvironment) Execute(_ context.Context, command string) environment.CommandResult {
	f.commands = append(f.commands, command)
	if len(f.results) == 0 {
		return environment.CommandResult{Output: "executed"}
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

func (*fakeEnvironment) Close(context.Context) error { return nil }

func TestSubmissionFromActualCommandOutput(t *testing.T) {
	tests := []struct {
		name   string
		result environment.CommandResult
		want   string
		ok     bool
	}{
		{
			name:   "submitted",
			result: environment.CommandResult{Output: "  \nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\ndiff --git a/a b/a\n"},
			want:   "diff --git a/a b/a\n",
			ok:     true,
		},
		{
			name:   "marker only",
			result: environment.CommandResult{Output: SubmissionMarker},
			want:   "",
			ok:     true,
		},
		{
			name:   "python splitlines boundary",
			result: environment.CommandResult{Output: SubmissionMarker + "\rpatch\r"},
			want:   "patch\r",
			ok:     true,
		},
		{
			name:   "nonzero",
			result: environment.CommandResult{Output: SubmissionMarker + "\npatch", ReturnCode: 1},
		},
		{
			name:   "not first line",
			result: environment.CommandResult{Output: "noise\n" + SubmissionMarker + "\npatch"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := SubmissionFromResult(test.result)
			if got != test.want || ok != test.ok {
				t.Fatalf("SubmissionFromResult() = (%q, %v), want (%q, %v)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestParseActions(t *testing.T) {
	calls := []model.ToolCall{
		{ID: "one", Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(`{"command":"pwd"}`)}},
		{ID: "two", Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(`{"command":"git status"}`)}},
	}
	actions, err := ParseActions(calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].Command != "pwd" || actions[1].ToolCallID != "two" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestParseActionsReturnsFormatError(t *testing.T) {
	_, err := ParseActions(nil)
	if err == nil || !strings.Contains(err.Error(), "No tool calls found") || !strings.Contains(err.Error(), "Every response needs to use the 'bash' tool") {
		t.Fatalf("error = %v", err)
	}
	_, err = ParseActions([]model.ToolCall{{Function: model.FunctionDefinitionParam{Name: "other", Arguments: []byte(`{}`)}}})
	if err == nil || !strings.Contains(err.Error(), "Unknown tool 'other'.Missing 'command'") {
		t.Fatalf("error = %v", err)
	}
	_, err = ParseActions([]model.ToolCall{{Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(`{"command":`)}}})
	want := formatError("Error parsing tool call arguments: Expecting value: line 1 column 12 (char 11). Missing 'command' argument in bash tool call.").Error()
	if err == nil || err.Error() != want {
		t.Fatalf("malformed JSON error = %q, want %q", err, want)
	}
}

func TestFormatObservationLengthBoundary(t *testing.T) {
	short := FormatObservation(environment.CommandResult{Output: strings.Repeat("界", maxObservation-1), ReturnCode: 7})
	if strings.Contains(short, "<warning>") || !strings.Contains(short, "<returncode>7</returncode>") {
		t.Fatalf("short observation boundary mismatch")
	}
	exact := FormatObservation(environment.CommandResult{Output: strings.Repeat("界", maxObservation), ExceptionInfo: "boom"})
	if !strings.Contains(exact, "<exception>boom</exception>") || !strings.Contains(exact, "0 characters elided") {
		t.Fatalf("exact observation boundary mismatch")
	}
	long := FormatObservation(environment.CommandResult{Output: strings.Repeat("界", maxObservation+20)})
	if !strings.Contains(long, "20 characters elided") {
		t.Fatalf("truncation marker missing")
	}
}
