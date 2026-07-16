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
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestParseBashActionsAllowsCodeSearchAlongsideBash(t *testing.T) {
	calls := []model.ToolCall{
		{Function: model.FunctionDefinitionParam{Name: "code_search", Arguments: []byte(`{"query":"FindUser"}`)}},
		{ID: "bash-1", Function: model.FunctionDefinitionParam{Name: "bash", Arguments: []byte(`{"command":"pwd"}`)}},
	}
	actions, err := parseBashActions(calls)
	if err != nil {
		t.Fatalf("parseBashActions() error = %v", err)
	}
	if len(actions) != 1 || actions[0].Command != "pwd" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestParseBashActionsStillRequiresBash(t *testing.T) {
	_, err := parseBashActions([]model.ToolCall{{
		Function: model.FunctionDefinitionParam{Name: "code_search", Arguments: []byte(`{"query":"FindUser"}`)},
	}})
	if err == nil {
		t.Fatal("expected mini-SWE bash requirement error")
	}
}
