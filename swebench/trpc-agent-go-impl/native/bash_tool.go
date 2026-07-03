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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	amodel "trpc.group/trpc-go/trpc-agent-go/model"
	atool "trpc.group/trpc-go/trpc-agent-go/tool"
)

const bashToolName = "bash"

type bashTool struct {
	ws         workspace
	timeoutReq RunRequest

	mu               sync.Mutex
	lastCommand      string
	repeatedCommands int
	submittedPatch   string
	submitted        bool
	executions       []commandResult
	afterExecution   func()
}

type bashToolArgs struct {
	Command string `json:"command"`
}

func newBashTool(ws workspace, req RunRequest) *bashTool {
	return &bashTool{ws: ws, timeoutReq: req}
}

func (t *bashTool) Declaration() *atool.Declaration {
	return &atool.Declaration{
		Name:        bashToolName,
		Description: "Execute a bash command in the SWE-Bench repository environment.",
		InputSchema: &atool.Schema{
			Type:     "object",
			Required: []string{"command"},
			Properties: map[string]*atool.Schema{
				"command": {
					Type:        "string",
					Description: "The bash command to execute.",
				},
			},
		},
	}
}

func (t *bashTool) Call(ctx context.Context, jsonArgs []byte) (any, error) {
	var args bashToolArgs
	if err := json.Unmarshal(jsonArgs, &args); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return nil, fmt.Errorf("bash tool call missing command")
	}
	if t.repeatedCommand(command) {
		res := commandResult{
			Command:  command,
			Output:   fmt.Sprintf("the same bash command was repeated %d times; choose a different command, inspect/create patch.txt, or submit with %s", t.repeatedCommands, submissionSentinel),
			ExitCode: 2,
			Rejected: true,
		}
		t.recordExecution(res)
		t.notifyAfterExecution()
		return res, nil
	}
	res := t.ws.exec(ctx, command, commandTimeout(t.timeoutReq.Timeout), t.timeoutReq.MaxOutputChars)
	t.recordExecution(res)
	if strings.Contains(res.Output, submissionSentinel) || strings.Contains(command, submissionSentinel) {
		t.mu.Lock()
		t.submitted = true
		t.submittedPatch = extractSubmittedPatch(res.Output)
		t.mu.Unlock()
	}
	t.notifyAfterExecution()
	return res, nil
}

func (t *bashTool) recordExecution(res commandResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.executions = append(t.executions, res)
}

func (t *bashTool) setAfterExecution(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.afterExecution = fn
}

func (t *bashTool) notifyAfterExecution() {
	t.mu.Lock()
	fn := t.afterExecution
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (t *bashTool) repeatedCommand(command string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return repeatedCommand(command, &t.lastCommand, &t.repeatedCommands)
}

func (t *bashTool) submission() (patch string, submitted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.submittedPatch, t.submitted
}

func (t *bashTool) executionSnapshot() []commandResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]commandResult, len(t.executions))
	copy(out, t.executions)
	return out
}

func miniToolCallbacks(bash *bashTool) *atool.Callbacks {
	callbacks := atool.NewCallbacks()
	callbacks.RegisterAfterTool(func(ctx context.Context, args *atool.AfterToolArgs) (*atool.AfterToolResult, error) {
		if args.ToolName != bashToolName {
			return nil, nil
		}
		if _, submitted := bash.submission(); submitted {
			return &atool.AfterToolResult{SkipSummarization: true}, nil
		}
		return nil, nil
	})
	callbacks.RegisterToolResultMessages(func(ctx context.Context, in *atool.ToolResultMessagesInput) (any, error) {
		if in.ToolName != bashToolName {
			return nil, nil
		}
		res, ok := in.Result.(commandResult)
		if !ok {
			if ptr, ok := in.Result.(*commandResult); ok && ptr != nil {
				res = *ptr
				ok = true
			}
		}
		if !ok {
			return nil, nil
		}
		return amodel.NewToolMessage(in.ToolCallID, bashToolName, observationMessage(res)), nil
	})
	return callbacks
}
