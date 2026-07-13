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
	"fmt"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

const (
	SubmissionMarker = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"
	maxObservation   = 10000
)

type bashInput struct {
	Command string `json:"command" jsonschema:"description=Shell command to execute inside the repository testbed"`
}

// Submission stores the marker payload in a concurrency-safe form.
type Submission struct {
	mu      sync.Mutex
	content string
	found   bool
}

// Value returns the submitted final text and whether the marker was seen.
func (s *Submission) Value() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.content, s.found
}

func (s *Submission) set(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content = value
	s.found = true
}

// NewBashTool creates the single native tool exposed by mini-swe-agent.
func NewBashTool(env environment.Environment, submission *Submission) tool.Tool {
	fn := func(ctx context.Context, input bashInput) (environment.CommandResult, error) {
		trimmed := strings.TrimLeft(input.Command, " \t\r\n")
		first, rest, _ := strings.Cut(trimmed, "\n")
		if strings.TrimSpace(first) == SubmissionMarker {
			submission.set(strings.TrimSpace(rest))
			if invocation, ok := agent.InvocationFromContext(ctx); ok {
				invocation.EndInvocation = true
			}
			return environment.CommandResult{Output: "Submission accepted.", ReturnCode: 0}, nil
		}
		return env.Execute(ctx, input.Command), nil
	}
	return function.NewFunctionTool(fn,
		function.WithName("bash"),
		function.WithDescription("Execute a non-interactive shell command in the repository testbed."),
	)
}

// ToolCallbacks preserves mini-swe-agent's XML-like observation protocol.
func ToolCallbacks() *tool.Callbacks {
	return tool.NewCallbacks().RegisterToolResultMessages(func(_ context.Context, in *tool.ToolResultMessagesInput) (any, error) {
		result, ok := in.Result.(environment.CommandResult)
		if !ok {
			if pointer, pointerOK := in.Result.(*environment.CommandResult); pointerOK && pointer != nil {
				result = *pointer
			} else {
				return nil, nil
			}
		}
		return []model.Message{model.NewToolMessage(in.ToolCallID, in.ToolName, FormatObservation(result))}, nil
	})
}

// FormatObservation formats and truncates output like mini-swe-agent v2.1.0.
func FormatObservation(result environment.CommandResult) string {
	var b strings.Builder
	if result.ExceptionInfo != "" {
		fmt.Fprintf(&b, "<exception>%s</exception>\n", result.ExceptionInfo)
	}
	fmt.Fprintf(&b, "<returncode>%d</returncode>\n<output>\n%s\n</output>", result.ReturnCode, truncateOutput(result.Output))
	return b.String()
}

func truncateOutput(output string) string {
	runes := []rune(output)
	if len(runes) <= maxObservation {
		return output
	}
	elided := len(runes) - maxObservation
	return fmt.Sprintf("%s\n\n... %d characters elided ...\n\n%s", string(runes[:maxObservation/2]), elided, string(runes[len(runes)-maxObservation/2:]))
}
