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
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func modelCallbacks(state *State) *model.Callbacks {
	callbacks := model.NewCallbacks()
	callbacks.RegisterBeforeModel(func(context.Context, *model.BeforeModelArgs) (*model.BeforeModelResult, error) {
		state.recordModelCall()
		return nil, nil
	})
	callbacks.RegisterAfterModel(func(_ context.Context, args *model.AfterModelArgs) (*model.AfterModelResult, error) {
		if args == nil || args.Response == nil {
			return nil, nil
		}
		state.recordResponse(args.Response)
		response := args.Response
		if args.Error != nil || response.Error != nil || response.IsPartial || !response.Done {
			return nil, nil
		}
		if len(response.Choices) == 0 {
			return nil, errors.New("model response contains no choices")
		}
		_, err := protocol.ParseActions(response.Choices[0].Message.ToolCalls)
		if err == nil {
			return nil, nil
		}
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

func toolCallbacks(state *State, codec observation.ObservationCodec) *tool.Callbacks {
	callbacks := tool.NewCallbacks()
	callbacks.RegisterAfterTool(func(_ context.Context, args *tool.AfterToolArgs) (*tool.AfterToolResult, error) {
		state.recordToolCall()
		if args == nil || args.Error != nil {
			return nil, nil
		}
		result, ok := args.Result.(sweenv.CommandResult)
		if !ok {
			return nil, fmt.Errorf("bash returned %T, want sweenv.CommandResult", args.Result)
		}
		patch, submitted := protocol.SubmissionFromResult(result)
		if !submitted {
			return nil, nil
		}
		state.setSubmission(patch)
		// The pinned mini-SWE-agent loop stops the current action batch as soon
		// as submission succeeds. A StopError is required here because
		// SkipSummarization is applied only after the framework has otherwise
		// finished dispatching every tool call in the response.
		return nil, agent.NewStopError("SWE-Bench submission accepted")
	})
	callbacks.RegisterToolResultMessages(func(_ context.Context, input *tool.ToolResultMessagesInput) (any, error) {
		if input == nil {
			return nil, errors.New("nil tool result input")
		}
		result, ok := input.Result.(sweenv.CommandResult)
		if !ok {
			return nil, fmt.Errorf("bash returned %T, want sweenv.CommandResult", input.Result)
		}
		formatted, err := observation.FormatWithCodec(result, codec)
		if err != nil {
			return nil, err
		}
		return model.NewToolMessage(input.ToolCallID, "bash", formatted), nil
	})
	return callbacks
}
