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
	"errors"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/toolloop"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func modelCallbacks(state *State, loopTracker *toolLoopTracker) *model.Callbacks {
	callbacks := model.NewCallbacks()
	callbacks.RegisterBeforeModel(func(_ context.Context, args *model.BeforeModelArgs) (*model.BeforeModelResult, error) {
		llmCall := state.recordModelCall()
		// ToolResultMessages must return protocol-compatible Tool messages. The
		// warning is therefore appended only when the next real request exists,
		// after all results from the repeated batch are already present.
		if args != nil && args.Request != nil && loopTracker.takeWarning() {
			args.Request.Messages = append(args.Request.Messages, model.NewUserMessage(toolloop.Warning))
			state.recordToolLoopWarning(llmCall)
		}
		return nil, nil
	})
	callbacks.RegisterAfterModel(func(_ context.Context, args *model.AfterModelArgs) (*model.AfterModelResult, error) {
		if args == nil || args.Response == nil {
			loopTracker.reset()
			return nil, nil
		}
		state.recordResponse(args.Response)
		response := args.Response
		if args.Error != nil || response.Error != nil {
			loopTracker.reset()
			return nil, nil
		}
		// A partial streaming response is not an assistant turn yet. Preserve
		// the preceding completed batch until the terminal response arrives.
		if response.IsPartial || !response.Done {
			return nil, nil
		}
		if len(response.Choices) == 0 {
			loopTracker.reset()
			return nil, errors.New("model response contains no choices")
		}
		toolCalls := response.Choices[0].Message.ToolCalls
		_, err := protocol.ParseActions(toolCalls)
		if err == nil {
			loopTracker.start(toolCalls)
			return nil, nil
		}
		loopTracker.reset()
		var formatErr protocol.FormatError
		if !errors.As(err, &formatErr) {
			return nil, err
		}
		return &model.AfterModelResult{CustomResponse: &model.Response{
			Object:  model.ObjectTypeChatCompletion,
			Choices: []model.Choice{{Message: model.NewUserMessage(formatErr.Error())}},
			Done:    false,
		}}, nil
	})
	return callbacks
}

func toolCallbacks(
	state *State,
	codec observation.ObservationCodec,
	loopTracker *toolLoopTracker,
) *tool.Callbacks {
	callbacks := tool.NewCallbacks()
	callbacks.RegisterAfterTool(func(_ context.Context, args *tool.AfterToolArgs) (*tool.AfterToolResult, error) {
		state.recordToolCall()
		if args == nil || args.Error != nil {
			loopTracker.reset()
			return nil, nil
		}
		result, ok := args.Result.(sweenv.CommandResult)
		if !ok {
			loopTracker.reset()
			return nil, fmt.Errorf("bash returned %T, want sweenv.CommandResult", args.Result)
		}
		patch, submitted := protocol.SubmissionFromResult(result)
		if !submitted {
			return nil, nil
		}
		loopTracker.reset()
		state.setSubmission(patch)
		// The pinned mini-SWE-agent loop stops the current action batch as soon
		// as submission succeeds. A StopError is required here because
		// SkipSummarization is applied only after the framework has otherwise
		// finished dispatching every tool call in the response.
		return nil, agent.NewStopError(protocol.SubmissionStopMessage)
	})
	callbacks.RegisterToolResultMessages(func(_ context.Context, input *tool.ToolResultMessagesInput) (any, error) {
		if input == nil {
			loopTracker.reset()
			return nil, errors.New("nil tool result input")
		}
		result, ok := input.Result.(sweenv.CommandResult)
		if !ok {
			loopTracker.reset()
			return nil, fmt.Errorf("bash returned %T, want sweenv.CommandResult", input.Result)
		}
		formatted, err := observation.FormatWithCodec(result, codec)
		if err != nil {
			loopTracker.reset()
			return nil, err
		}
		loopTracker.add(input.ToolCallID, input.ToolName, input.Arguments, formatted)
		return model.NewToolMessage(input.ToolCallID, "bash", formatted), nil
	})
	return callbacks
}
