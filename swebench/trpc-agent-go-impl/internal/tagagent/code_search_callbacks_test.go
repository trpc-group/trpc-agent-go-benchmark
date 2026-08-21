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
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestCodeSearchCallbacksRecordTraceAndExcludeLoopWarning(t *testing.T) {
	state := &State{}
	tracker := newToolLoopTracker(true)
	modelCallbacks := modelCallbacks(state, tracker, true)
	toolCallbacks := toolCallbacks(state, observation.ObservationCodecXML, tracker)
	response := &knowledgetool.KnowledgeSearchResponse{Documents: []*knowledgetool.DocumentResult{{
		ID:   "doc-1",
		Text: "def find_user_by_email(email):\n    return users[email]\n",
		Metadata: map[string]any{
			"trpc_ast_file_path":  `pkg/users&roles.py`,
			"trpc_ast_line_start": 10,
			"trpc_ast_line_end":   11,
			"trpc_ast_full_name":  "pkg.users.find_user_by_email",
			"trpc_ast_type":       "function",
		},
		Score: 0.75,
	}}}

	for _, id := range []string{"search-1", "search-2"} {
		call := model.ToolCall{ID: id, Function: model.FunctionDefinitionParam{
			Name: "code_search", Arguments: []byte(`{"query":"find_user_by_email"}`),
		}}
		startToolLoopBatch(t, modelCallbacks, []model.ToolCall{call})
		if _, err := toolCallbacks.RunAfterTool(context.Background(), &tool.AfterToolArgs{
			ToolCallID: id,
			ToolName:   "code_search",
			Arguments:  call.Function.Arguments,
			Result:     response,
		}); err != nil {
			t.Fatalf("RunAfterTool() error = %v", err)
		}
		message, err := toolCallbacks.RunToolResultMessages(
			context.Background(),
			&tool.ToolResultMessagesInput{
				ToolCallID: id,
				ToolName:   "code_search",
				Arguments:  call.Function.Arguments,
				Result:     response,
			},
		)
		if err != nil {
			t.Fatalf("RunToolResultMessages() error = %v", err)
		}
		toolMessage, ok := message.(model.Message)
		if !ok {
			t.Fatalf("tool message type = %T", message)
		}
		if !strings.Contains(toolMessage.Content, `path="pkg/users&amp;roles.py"`) ||
			!strings.Contains(toolMessage.Content, `lines="10-11"`) ||
			!strings.Contains(toolMessage.Content, "def find_user_by_email") {
			t.Fatalf("code_search observation = %q", toolMessage.Content)
		}
	}

	request := &model.Request{}
	if _, err := modelCallbacks.RunBeforeModel(
		context.Background(), &model.BeforeModelArgs{Request: request},
	); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 0 {
		t.Fatalf("repeated code_search injected loop warning: %#v", request.Messages)
	}
	snapshot := state.Snapshot()
	if snapshot.ToolCalls != 2 || snapshot.CodeSearchCalls != 2 ||
		snapshot.CodeSearchErrors != 0 ||
		len(snapshot.RetrievalTrace) != 2 || len(snapshot.CodeSearchRawResults) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	entry := snapshot.RetrievalTrace[0]
	if entry.Status != "success" || entry.Query != "find_user_by_email" || entry.ResultSHA256 == "" ||
		entry.ObservationSHA256 == "" || len(entry.Documents) != 1 ||
		entry.Documents[0].Path != "pkg/users&roles.py" ||
		entry.Documents[0].Lines != "10-11" || entry.Documents[0].ContentSHA256 == "" {
		t.Fatalf("trace entry = %#v", entry)
	}
}

func TestCodeSearchCallbacksRecordExecutedError(t *testing.T) {
	state := &State{}
	callbacks := toolCallbacks(state, observation.ObservationCodecXML, newToolLoopTracker(true))
	arguments := []byte(`{"query":"missing symbol","provider_extension":true}`)
	if _, err := callbacks.RunAfterTool(context.Background(), &tool.AfterToolArgs{
		ToolCallID: "search-error",
		ToolName:   "code_search",
		Arguments:  arguments,
		Error:      errors.New("knowledge search returned no documents"),
	}); err != nil {
		t.Fatalf("RunAfterTool() error = %v", err)
	}
	snapshot := state.Snapshot()
	if snapshot.ToolCalls != 1 || snapshot.CodeSearchCalls != 1 ||
		snapshot.CodeSearchErrors != 1 || len(snapshot.RetrievalTrace) != 1 ||
		len(snapshot.CodeSearchRawResults) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	entry := snapshot.RetrievalTrace[0]
	if entry.Status != "error" || entry.Query != "missing symbol" ||
		entry.Error != "knowledge search returned no documents" ||
		entry.ErrorSHA256 == "" || entry.ResultSHA256 == "" ||
		entry.ObservationSHA256 != "" || len(entry.Documents) != 0 {
		t.Fatalf("trace entry = %#v", entry)
	}
	if !strings.Contains(string(snapshot.CodeSearchRawResults[0]), `"error":"knowledge search returned no documents"`) {
		t.Fatalf("raw result = %s", snapshot.CodeSearchRawResults[0])
	}
}

func TestCodeSearchRejectedWhenNotEnabled(t *testing.T) {
	state := &State{}
	callbacks := modelCallbacks(state, newToolLoopTracker(false))
	result, err := callbacks.RunAfterModel(context.Background(), &model.AfterModelArgs{Response: &model.Response{
		Done: true,
		Choices: []model.Choice{{Message: model.Message{ToolCalls: []model.ToolCall{{
			ID: "search",
			Function: model.FunctionDefinitionParam{
				Name: "code_search", Arguments: []byte(`{"query":"where"}`),
			},
		}}}}},
	}})
	if err != nil || result == nil || result.CustomResponse == nil {
		t.Fatalf("disabled code_search result = %#v, err = %v", result, err)
	}
}
