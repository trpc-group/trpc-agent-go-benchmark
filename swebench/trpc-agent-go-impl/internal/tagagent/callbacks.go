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

func modelCallbacks(
	state *State,
	allowCodeSearch bool,
	loopTracker *toolLoopTracker,
) *model.Callbacks {
	callbacks := model.NewCallbacks()
	callbacks.RegisterBeforeModel(func(_ context.Context, args *model.BeforeModelArgs) (*model.BeforeModelResult, error) {
		state.recordModelCall()
		// ToolResultMessages must return the protocol's Tool messages. The
		// session projection does not retain an additional User message returned
		// from that callback, so append the warning to the actual next request.
		// At this point all results from the repeated batch are already present.
		if args != nil && args.Request != nil && loopTracker.takeWarning() {
			args.Request.Messages = append(
				args.Request.Messages,
				model.NewUserMessage(toolLoopWarning),
			)
			state.recordToolLoopWarning()
		}
		if allowCodeSearch && args != nil && args.Request != nil {
			if _, ok := args.Request.Tools["code_search"]; ok {
				args.Request.ToolOrder = []string{"code_search", "bash"}
			}
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
		// A partial streaming response is not an assistant turn yet. Preserve the
		// previous completed batch until the terminal response arrives.
		if response.IsPartial || !response.Done {
			return nil, nil
		}
		if len(response.Choices) == 0 {
			loopTracker.reset()
			return nil, errors.New("model response contains no choices")
		}
		toolCalls := response.Choices[0].Message.ToolCalls
		_, err := parseToolActions(toolCalls, allowCodeSearch)
		if err == nil {
			loopTracker.start(toolCalls)
			return nil, nil
		}
		loopTracker.reset()
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

func parseToolActions(toolCalls []model.ToolCall, allowCodeSearch bool) ([]minicompat.Action, error) {
	bashCalls := make([]model.ToolCall, 0, len(toolCalls))
	hasCodeSearch := false
	for _, call := range toolCalls {
		switch call.Function.Name {
		case "bash":
			bashCalls = append(bashCalls, call)
		case "code_search":
			hasCodeSearch = true
		}
	}
	if len(bashCalls) > 0 {
		return minicompat.ParseActions(bashCalls)
	}
	if allowCodeSearch && hasCodeSearch {
		return nil, nil
	}
	return minicompat.ParseActions(nil)
}

func toolCallbacks(
	state *State,
	codec minicompat.ObservationCodec,
	loopTracker *toolLoopTracker,
) *tool.Callbacks {
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
					state.recordCodeSearchResult(payload)
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
		switch input.ToolName {
		case "code_search":
			observation, err := formatCodeSearchXMLLike(input.Result)
			if err != nil {
				return nil, err
			}
			state.recordCodeSearchObservationBytes(len([]byte(observation)))
			message := model.NewToolMessage(input.ToolCallID, "code_search", observation)
			return toolResultMessagesWithLoopWarning(state, loopTracker, input, message), nil
		case "bash":
		default:
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
		message := model.NewToolMessage(input.ToolCallID, "bash", observation)
		return toolResultMessagesWithLoopWarning(state, loopTracker, input, message), nil
	})
	return callbacks
}

func toolResultMessagesWithLoopWarning(
	state *State,
	loopTracker *toolLoopTracker,
	input *tool.ToolResultMessagesInput,
	toolMessage model.Message,
) any {
	if state.submittedValue() {
		loopTracker.reset()
		return toolMessage
	}
	loopTracker.add(
		input.ToolCallID,
		input.ToolName,
		input.Arguments,
		toolMessage.Content,
	)
	return toolMessage
}
