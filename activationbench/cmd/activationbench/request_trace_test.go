//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/runner"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestRequestTraceWriterWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "requests.jsonl")
	writer, err := newRequestTraceWriter(path)
	if err != nil {
		t.Fatalf("newRequestTraceWriter: %v", err)
	}
	writer.Observe(runner.ModelRequestTrace{
		TaskID:       "task-1",
		Mode:         "dynamic-activation",
		RequestIndex: 2,
		Messages: []model.Message{
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call-1"}}},
			{Role: model.RoleTool, ToolID: "call-1", ToolName: "files-tools_files_list", Content: "[]"},
		},
	})
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("trace has no JSONL record: %v", scanner.Err())
	}
	var got runner.ModelRequestTrace
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	if got.TaskID != "task-1" || got.RequestIndex != 2 || len(got.Messages) != 2 {
		t.Fatalf("trace = %+v, want task and two messages", got)
	}
	if scanner.Scan() {
		t.Fatal("trace has more than one record")
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan trace: %v", err)
	}
}
