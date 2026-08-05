//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tagagent

import (
	"context"
	"encoding/json"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type bashTool struct {
	environment sweenv.Environment
}

func (t *bashTool) Declaration() *tool.Declaration {
	return protocol.BashDeclaration()
}

func (t *bashTool) Call(ctx context.Context, jsonArgs []byte) (any, error) {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(jsonArgs, &input); err != nil {
		return nil, fmt.Errorf("decode bash arguments: %w", err)
	}
	return t.environment.Execute(ctx, input.Command), nil
}
