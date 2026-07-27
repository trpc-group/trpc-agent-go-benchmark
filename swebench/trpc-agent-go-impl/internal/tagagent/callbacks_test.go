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
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/minicompat"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestParseToolActionsAllowsCodeSearchAlongsideBash(t *testing.T) {
	calls := []model.ToolCall{
		{Function: model.FunctionDefinitionParam{Name: "code_search", Arguments: []byte(`{"query":"FindUser"}`)}},
		{ID: "bash-1", Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(`{"command":"pwd"}`)}},
	}
	actions, err := parseToolActions(calls, true)
	if err != nil {
		t.Fatalf("parseToolActions() error = %v", err)
	}
	if len(actions) != 1 || actions[0].Command != "pwd" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestParseToolActionsAllowsCodeSearchOnlyInRAGMode(t *testing.T) {
	calls := []model.ToolCall{{
		Function: model.FunctionDefinitionParam{Name: "code_search", Arguments: []byte(`{"query":"FindUser"}`)},
	}}
	if actions, err := parseToolActions(calls, true); err != nil || len(actions) != 0 {
		t.Fatalf("parseToolActions() = %#v, %v, want no actions and no error", actions, err)
	}
	if _, err := parseToolActions(calls, false); err == nil {
		t.Fatal("expected native mini-SWE bash requirement error")
	}
}

func TestParseToolActionsStillRequiresToolCall(t *testing.T) {
	_, err := parseToolActions(nil, true)
	if err == nil {
		t.Fatal("expected mini-SWE tool requirement error")
	}
}

func TestParseToolActionsStillRejectsMalformedBash(t *testing.T) {
	_, err := parseToolActions([]model.ToolCall{{
		Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(`{"command":`)},
	}}, true)
	if err == nil {
		t.Fatal("expected malformed Bash arguments error")
	}
}

func TestCodeSearchToolResultUsesXMLLikeObservation(t *testing.T) {
	state := &State{}
	callbacks := toolCallbacks(state, minicompat.ObservationCodecXML)
	searchResult := &knowledgetool.KnowledgeSearchResponse{Documents: []*knowledgetool.DocumentResult{{
		ID:    "internal-node-1",
		Text:  "func FindUser() {}",
		Score: 0.75,
		Metadata: map[string]any{
			"trpc_ast_file_path": "users.go",
			"trpc_ast_type":      "Function",
			"trpc_ast_full_name": "FindUser",
		},
	}}}
	if _, err := callbacks.RunAfterTool(context.Background(), &tool.AfterToolArgs{
		ToolName: "code_search",
		Result:   searchResult,
	}); err != nil {
		t.Fatalf("RunAfterTool() error = %v", err)
	}
	result, err := callbacks.RunToolResultMessages(context.Background(), &tool.ToolResultMessagesInput{
		ToolName:   "code_search",
		ToolCallID: "search-1",
		Result:     searchResult,
	})
	if err != nil {
		t.Fatalf("RunToolResultMessages() error = %v", err)
	}
	message, ok := result.(model.Message)
	if !ok {
		t.Fatalf("tool result message type = %T, want model.Message", result)
	}
	if message.Role != model.RoleTool || message.ToolID != "search-1" ||
		message.ToolName != "code_search" {
		t.Fatalf("tool result message = %#v", message)
	}
	if message.Content != `<code_search_results snapshot="task_start">
<result path="users.go" symbol="FindUser">
<code>
func FindUser() {}
</code>
</result>
</code_search_results>` {
		t.Fatalf("tool result content = %q", message.Content)
	}
	snapshot := state.Snapshot()
	if got := snapshot.CodeSearchObservationBytes; got != len([]byte(message.Content)) {
		t.Fatalf("observation bytes = %d, want %d", got, len([]byte(message.Content)))
	}
	if len(snapshot.CodeSearchRawResults) != 1 {
		t.Fatalf("raw results = %d, want 1", len(snapshot.CodeSearchRawResults))
	}
	var raw knowledgetool.KnowledgeSearchResponse
	if err := json.Unmarshal(snapshot.CodeSearchRawResults[0], &raw); err != nil {
		t.Fatalf("unmarshal raw result: %v", err)
	}
	if len(raw.Documents) != 1 || raw.Documents[0].ID != "internal-node-1" ||
		raw.Documents[0].Score != 0.75 || raw.Documents[0].Text != "func FindUser() {}" {
		t.Fatalf("raw result = %#v", raw)
	}
	// Snapshot data must not alias mutable state.
	snapshot.CodeSearchRawResults[0][0] = 'x'
	if state.Snapshot().CodeSearchRawResults[0][0] == 'x' {
		t.Fatal("raw result snapshot aliases state")
	}
}
