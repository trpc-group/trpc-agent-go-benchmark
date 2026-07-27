//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package tagagent builds the TAG-native SWE agent for one isolated case.
package tagagent

import (
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/minicompat"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const maxLLMCalls = 250

// New creates a TAG llmagent bound to one testbed and state holder.
func New(
	modelImpl model.Model,
	environment sweenv.Environment,
	codec minicompat.ObservationCodec,
	generationConfig model.GenerationConfig,
	state *State,
	extraTools ...tool.Tool,
) *llmagent.LLMAgent {
	bash := &bashTool{environment: environment}
	tools := append([]tool.Tool(nil), extraTools...)
	tools = append(tools, bash)
	instruction := minicompat.OfflineSystemPrompt
	if len(extraTools) > 0 {
		instruction = minicompat.OfflineSystemPromptWithCodeSearch
	}
	return llmagent.New(
		"tag-swe-agent",
		llmagent.WithModel(modelImpl),
		llmagent.WithGlobalInstruction(instruction),
		llmagent.WithGenerationConfig(generationConfig),
		llmagent.WithTools(tools),
		llmagent.WithMaxLLMCalls(maxLLMCalls),
		llmagent.WithEnableParallelTools(false),
		llmagent.WithPreserveSameBranch(true),
		llmagent.WithEnablePostToolPrompt(false),
		llmagent.WithEnableCodeExecutionResponseProcessor(false),
		llmagent.WithModelCallbacks(modelCallbacks(state, len(extraTools) > 0)),
		llmagent.WithToolCallbacks(toolCallbacks(state, codec)),
	)
}
