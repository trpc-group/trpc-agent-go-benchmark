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
	"errors"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/minicompat"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
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
		_, err := parseBashActions(response.Choices[0].Message.ToolCalls)
		if err == nil {
			return nil, nil
		}
		var formatErr minicompat.FormatError
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

func parseBashActions(toolCalls []model.ToolCall) ([]minicompat.Action, error) {
	bashCalls := make([]model.ToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		if call.Function.Name == "bash" {
			bashCalls = append(bashCalls, call)
		}
	}
	if len(bashCalls) == 0 {
		// Preserve the mini-SWE protocol requirement and its exact feedback for
		// responses that contain no bash call, including code_search-only turns.
		return minicompat.ParseActions(nil)
	}
	return minicompat.ParseActions(bashCalls)
}

func toolCallbacks(state *State, codec minicompat.ObservationCodec) *tool.Callbacks {
	callbacks := tool.NewCallbacks()
	callbacks.RegisterAfterTool(func(_ context.Context, args *tool.AfterToolArgs) (*tool.AfterToolResult, error) {
		if args != nil {
			state.recordToolCall(args.ToolName)
		}
		if args == nil || args.Error != nil {
			return nil, nil
		}
		if args.ToolName != "bash" {
			if args.ToolName == "code_search" {
				if payload, err := json.Marshal(args.Result); err == nil {
					state.recordCodeSearchResultBytes(len(payload))
				}
			}
			return nil, nil
		}
		result, ok := args.Result.(sweenv.CommandResult)
		if !ok {
			return nil, fmt.Errorf("bash returned %T, want sweenv.CommandResult", args.Result)
		}
		patch, submitted := minicompat.SubmissionFromResult(result)
		if !submitted {
			return nil, nil
		}
		state.setSubmission(patch)
		return &tool.AfterToolResult{SkipSummarization: true}, nil
	})
	callbacks.RegisterToolResultMessages(func(_ context.Context, input *tool.ToolResultMessagesInput) (any, error) {
		if input == nil {
			return nil, errors.New("nil tool result input")
		}
		if input.ToolName != "bash" {
			return nil, nil
		}
		result, ok := input.Result.(sweenv.CommandResult)
		if !ok {
			return nil, fmt.Errorf("bash returned %T, want sweenv.CommandResult", input.Result)
		}
		observation, err := minicompat.FormatObservation(result, codec)
		if err != nil {
			return nil, err
		}
		return model.NewToolMessage(input.ToolCallID, "bash", observation), nil
	})
	return callbacks
}
