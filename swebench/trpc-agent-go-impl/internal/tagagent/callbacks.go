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

func modelCallbacks(state *State, loopTracker *toolLoopTracker, codeSearch ...bool) *model.Callbacks {
	allowCodeSearch := len(codeSearch) > 0 && codeSearch[0]
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
		bashCalls, err := parseToolActions(toolCalls, allowCodeSearch)
		if err == nil {
			if len(bashCalls) > 0 {
				// code_search is intentionally excluded: the exact-repeat warning
				// remains a detector over executable bash batches.
				loopTracker.start(bashCalls)
			} else {
				loopTracker.reset()
			}
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

func parseToolActions(toolCalls []model.ToolCall, allowCodeSearch bool) ([]model.ToolCall, error) {
	if len(toolCalls) == 0 {
		_, err := protocol.ParseActions(nil)
		return nil, err
	}
	bashCalls := make([]model.ToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		switch call.Function.Name {
		case "bash":
			bashCalls = append(bashCalls, call)
		case "code_search":
			if !allowCodeSearch {
				_, err := protocol.ParseActions([]model.ToolCall{call})
				return nil, err
			}
		default:
			_, err := protocol.ParseActions([]model.ToolCall{call})
			return nil, err
		}
	}
	if len(bashCalls) > 0 {
		if _, err := protocol.ParseActions(bashCalls); err != nil {
			return nil, err
		}
	}
	return bashCalls, nil
}

func toolCallbacks(
	state *State,
	codec observation.ObservationCodec,
	loopTracker *toolLoopTracker,
) *tool.Callbacks {
	callbacks := tool.NewCallbacks()
	callbacks.RegisterAfterTool(func(_ context.Context, args *tool.AfterToolArgs) (*tool.AfterToolResult, error) {
		if args != nil {
			state.recordToolCall(args.ToolName)
		}
		if args == nil {
			loopTracker.reset()
			return nil, nil
		}
		if args.Error != nil {
			if args.ToolName == "code_search" {
				state.recordCodeSearchError(args.ToolCallID, args.Arguments, args.Error)
			} else if args.ToolName == "bash" {
				loopTracker.reset()
			}
			return nil, nil
		}
		if args.ToolName == "code_search" {
			state.recordCodeSearchResult(args.ToolCallID, args.Arguments, args.Result)
			return nil, nil
		}
		if args.ToolName != "bash" {
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
		if input.ToolName == "code_search" {
			formatted, err := formatCodeSearchXMLLike(input.Result)
			if err != nil {
				return nil, err
			}
			state.recordCodeSearchObservation(input.ToolCallID, formatted)
			return model.NewToolMessage(input.ToolCallID, "code_search", formatted), nil
		}
		if input.ToolName != "bash" {
			return nil, nil
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
