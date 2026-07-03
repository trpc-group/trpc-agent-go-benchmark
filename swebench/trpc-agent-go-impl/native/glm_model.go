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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	amodel "trpc.group/trpc-go/trpc-agent-go/model"
	atool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type glmAgentModel struct {
	cfg        GLMConfig
	httpClient *http.Client
}

func newGLMAgentModel(cfg GLMConfig, timeout time.Duration) (*glmAgentModel, error) {
	cfg = cfg.withEnv()
	if cfg.APIBase == "" {
		return nil, fmt.Errorf("GLM5_API_BASE or -glm-api-base is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("GLM5_API_KEY or -glm-api-key is required")
	}
	return &glmAgentModel{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

func (m *glmAgentModel) Info() amodel.Info {
	return amodel.Info{Name: m.cfg.actualModelName()}
}

func (m *glmAgentModel) GenerateContent(ctx context.Context, request *amodel.Request) (<-chan *amodel.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("nil model request")
	}
	out := make(chan *amodel.Response, 1)
	resp, err := m.generate(ctx, request)
	if err != nil {
		return nil, err
	}
	out <- resp
	close(out)
	return out, nil
}

func (m *glmAgentModel) generate(ctx context.Context, request *amodel.Request) (*amodel.Response, error) {
	reqBody := map[string]any{
		"model":            m.cfg.actualModelName(),
		"messages":         trpcMessagesToOpenAI(request.Messages),
		"temperature":      0,
		"reasoning_effort": m.cfg.actualReasoningEffort(),
	}
	if request.Temperature != nil {
		reqBody["temperature"] = *request.Temperature
	}
	if request.MaxTokens != nil {
		reqBody["max_tokens"] = *request.MaxTokens
	}
	if len(request.Tools) > 0 {
		reqBody["tools"] = trpcToolsToOpenAI(request.Tools)
		reqBody["parallel_tool_calls"] = true
	}
	for k, v := range request.ExtraFields {
		reqBody[k] = v
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.chatCompletionsURL(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	if m.cfg.RoutingKey != "" {
		httpReq.Header.Set("X-SMG-Routing-Key", m.cfg.RoutingKey)
	}
	if m.cfg.AgentName != "" {
		httpReq.Header.Set("X-SMG-Agent-Name", m.cfg.AgentName)
	}
	for k, v := range request.Headers {
		httpReq.Header.Set(k, v)
	}
	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		bodyPreview, _, _ := limitOutput(string(body), 2000)
		return nil, fmt.Errorf("chat completion failed: status=%d body=%s", httpResp.StatusCode, bodyPreview)
	}
	var decoded struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int     `json:"index"`
			FinishReason *string `json:"finish_reason"`
			Message      struct {
				Role             string     `json:"role"`
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				ToolCalls        []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		SystemFingerprint *string `json:"system_fingerprint"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("chat completion returned no choices")
	}
	choices := make([]amodel.Choice, 0, len(decoded.Choices))
	for _, choice := range decoded.Choices {
		choices = append(choices, amodel.Choice{
			Index: choice.Index,
			Message: amodel.Message{
				Role:             amodel.RoleAssistant,
				Content:          choice.Message.Content,
				ReasoningContent: choice.Message.ReasoningContent,
				ToolCalls:        openAIToolCallsToTRPC(choice.Message.ToolCalls),
			},
			FinishReason: choice.FinishReason,
		})
	}
	return &amodel.Response{
		ID:      decoded.ID,
		Object:  decoded.Object,
		Created: decoded.Created,
		Model:   decoded.Model,
		Choices: choices,
		Usage: &amodel.Usage{
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
		},
		SystemFingerprint: decoded.SystemFingerprint,
		Timestamp:         time.Now(),
		Done:              true,
	}, nil
}

func trpcMessagesToOpenAI(messages []amodel.Message) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, chatMessage{
			Role:             msg.Role.String(),
			Content:          msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCalls:        trpcToolCallsToOpenAI(msg.ToolCalls),
			ToolCallID:       msg.ToolID,
		})
	}
	return out
}

func trpcToolCallsToOpenAI(calls []amodel.ToolCall) []toolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]toolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, toolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: toolFunction{
				Name:      call.Function.Name,
				Arguments: string(call.Function.Arguments),
			},
		})
	}
	return out
}

func openAIToolCallsToTRPC(calls []toolCall) []amodel.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]amodel.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, amodel.ToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: amodel.FunctionDefinitionParam{
				Name:      call.Function.Name,
				Arguments: []byte(call.Function.Arguments),
			},
		})
	}
	return out
}

func trpcToolsToOpenAI(tools map[string]atool.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tl := range tools {
		decl := tl.Declaration()
		if decl == nil {
			continue
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        decl.Name,
				"description": decl.Description,
				"parameters":  decl.InputSchema,
			},
		})
	}
	return out
}
