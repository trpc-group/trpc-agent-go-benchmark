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
	"io"
	"net/http"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type declarationOnlyTool struct{ name string }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (t declarationOnlyTool) Declaration() *tool.Declaration {
	return &tool.Declaration{
		Name:        t.name,
		Description: t.name,
		InputSchema: &tool.Schema{Type: "object"},
	}
}

func TestPinnedOpenAIAdapterEmitsRecordedCodeSearchToolOrder(t *testing.T) {
	var names []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		for _, item := range payload.Tools {
			names = append(names, item.Function.Name)
		}
		body := `{
			"id":"order-test","object":"chat.completion","created":1,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})

	adapter := openaimodel.New(
		"test-model",
		openaimodel.WithBaseURL("https://adapter-order.invalid/v1"),
		openaimodel.WithAPIKey("test-key"),
		openaimodel.WithHTTPClientOptions(model.WithHTTPClientTransport(transport)),
	)
	responses, err := adapter.GenerateContent(context.Background(), &model.Request{
		Messages: []model.Message{model.NewUserMessage("inspect order")},
		Tools: map[string]tool.Tool{
			"code_search": declarationOnlyTool{name: "code_search"},
			"bash":        declarationOnlyTool{name: "bash"},
		},
		GenerationConfig: model.GenerationConfig{Stream: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range responses {
	}
	if got := strings.Join(names, ","); got != CodeSearchProviderToolOrder {
		t.Fatalf("provider tool order = %q, want %q", got, CodeSearchProviderToolOrder)
	}
}
