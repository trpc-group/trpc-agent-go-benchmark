//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	amodel "trpc.group/trpc-go/trpc-agent-go/model"
)

func TestChatClientSendsDefaultReasoningEffort(t *testing.T) {
	t.Setenv("GLM5_REASONING_EFFORT", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["reasoning_effort"] != "high" {
			t.Fatalf("reasoning_effort = %v, want high", body["reasoning_effort"])
		}
		if body["parallel_tool_calls"] != true {
			t.Fatalf("parallel_tool_calls = %v, want true", body["parallel_tool_calls"])
		}
		if _, ok := body["tools"].([]any); !ok {
			t.Fatalf("tools missing from request body: %#v", body["tools"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	client, err := newChatClient(GLMConfig{
		APIBase: server.URL,
		APIKey:  "test-key",
	}, time.Second)
	if err != nil {
		t.Fatalf("newChatClient() error = %v", err)
	}
	if _, err := client.chat(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("chat() error = %v", err)
	}
}

func TestGLMAgentModelSendsConfiguredReasoningEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["reasoning_effort"] != "medium" {
			t.Fatalf("reasoning_effort = %v, want medium", body["reasoning_effort"])
		}
		if body["temperature"] != float64(0) {
			t.Fatalf("temperature = %v, want 0", body["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"glm50","choices":[{"index":0,"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	model, err := newGLMAgentModel(GLMConfig{
		APIBase:         server.URL,
		APIKey:          "test-key",
		ReasoningEffort: "medium",
	}, time.Second)
	if err != nil {
		t.Fatalf("newGLMAgentModel() error = %v", err)
	}
	resp, err := model.generate(context.Background(), &amodel.Request{
		Messages: []amodel.Message{{Role: amodel.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("response choices = %+v", resp.Choices)
	}
}
