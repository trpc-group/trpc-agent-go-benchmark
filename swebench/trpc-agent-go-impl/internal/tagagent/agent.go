//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package tagagent builds a tRPC-Agent-Go SWE agent for one isolated case.
package tagagent

import (
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// MaxLLMCalls is the pinned mini-SWE-agent v2.1 model-request limit.
const MaxLLMCalls = 250

// Config controls optional runner-local agent behavior.
type Config struct {
	CleanRoom       bool
	ToolLoopWarning bool
}

// New creates a llmagent bound to one testbed and state holder.
func New(
	modelImpl model.Model,
	environment sweenv.Environment,
	codec observation.ObservationCodec,
	generationConfig model.GenerationConfig,
	state *State,
	config Config,
	extraTools ...tool.Tool,
) *llmagent.LLMAgent {
	bash := &bashTool{environment: environment}
	tools := append([]tool.Tool(nil), extraTools...)
	tools = append(tools, bash)
	instruction := protocol.SystemPrompt
	if config.CleanRoom {
		instruction = protocol.OfflineSystemPrompt
	}
	if len(extraTools) > 0 {
		instruction = protocol.SystemPromptWithCodeSearch
		if config.CleanRoom {
			instruction = protocol.OfflineSystemPromptWithCodeSearch
		}
	}
	loopTracker := newToolLoopTracker(config.ToolLoopWarning)
	return llmagent.New(
		"swe-agent",
		llmagent.WithModel(modelImpl),
		llmagent.WithGlobalInstruction(instruction),
		llmagent.WithGenerationConfig(generationConfig),
		llmagent.WithTools(tools),
		llmagent.WithMaxLLMCalls(MaxLLMCalls),
		llmagent.WithEnableParallelTools(false),
		llmagent.WithPreserveSameBranch(true),
		llmagent.WithEnablePostToolPrompt(false),
		llmagent.WithEnableCodeExecutionResponseProcessor(false),
		llmagent.WithModelCallbacks(modelCallbacks(state, loopTracker, len(extraTools) > 0)),
		llmagent.WithToolCallbacks(toolCallbacks(state, codec, loopTracker)),
	)
}
