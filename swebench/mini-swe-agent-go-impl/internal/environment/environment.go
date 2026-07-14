//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package environment defines the execution boundary used by the SWE agent.
package environment

import "context"

// CommandResult is the stable bash tool result shape used by mini-swe-agent.
type CommandResult struct {
	Output        string `json:"output"`
	ReturnCode    int    `json:"returncode"`
	ExceptionInfo string `json:"exception_info,omitempty"`
	TimedOut      bool   `json:"timed_out,omitempty"`
}

// Environment executes commands inside one isolated SWE-Bench testbed.
type Environment interface {
	Execute(ctx context.Context, command string) CommandResult
	Close(ctx context.Context) error
}

// Factory starts one isolated environment per SWE-Bench instance.
type Factory interface {
	Start(ctx context.Context, instanceID string) (Environment, error)
}
