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

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type fakeEnvironment struct {
	commands []string
	patch    string
}

func (f *fakeEnvironment) Execute(_ context.Context, command string) environment.CommandResult {
	f.commands = append(f.commands, command)
	if strings.Contains(command, "git diff") {
		return environment.CommandResult{Output: f.patch}
	}
	return environment.CommandResult{Output: "executed"}
}

func (*fakeEnvironment) Close(context.Context) error { return nil }

func TestSubmissionMarkerStopsInvocationWithoutShellExecution(t *testing.T) {
	env := &fakeEnvironment{}
	submission := &Submission{}
	callable := NewBashTool(env, submission).(tool.CallableTool)
	invocation := agent.NewInvocation()
	ctx := agent.NewInvocationContext(context.Background(), invocation)
	result, err := callable.Call(ctx, []byte(`{"command":"  COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\nfixed it"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.commands) != 0 {
		t.Fatalf("shell commands = %#v, want none", env.commands)
	}
	if !invocation.EndInvocation {
		t.Fatal("EndInvocation = false, want true")
	}
	if text, ok := submission.Value(); !ok || text != "fixed it" {
		t.Fatalf("submission = %q, %v", text, ok)
	}
	if result.(environment.CommandResult).ReturnCode != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestFormatObservationTruncatesUnicodeByCharacters(t *testing.T) {
	output := strings.Repeat("界", maxObservation+20)
	got := FormatObservation(environment.CommandResult{Output: output, ReturnCode: 7, ExceptionInfo: "boom"})
	if !strings.Contains(got, "<exception>boom</exception>") || !strings.Contains(got, "<returncode>7</returncode>") {
		t.Fatalf("observation metadata missing: %q", got[:100])
	}
	if !strings.Contains(got, "20 characters elided") {
		t.Fatalf("truncation marker missing")
	}
}
