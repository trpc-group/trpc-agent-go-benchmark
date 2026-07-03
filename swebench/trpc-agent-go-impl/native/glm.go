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
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
)

const defaultGLMAgentName = "trpc-agent-go-benchmark"

// GLMConfig contains OpenAI-compatible GLM-5 connection settings.
type GLMConfig struct {
	APIBase         string
	APIKey          string
	RoutingKey      string
	AgentName       string
	ModelName       string
	ReasoningEffort string
}

type chatClient struct {
	cfg        GLMConfig
	httpClient *http.Client
}

type chatMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	Content          string
	ReasoningContent string
	ToolCalls        []toolCall
	Usage            result.Usage
}

func newChatClient(cfg GLMConfig, timeout time.Duration) (*chatClient, error) {
	cfg = cfg.withEnv()
	if cfg.APIBase == "" {
		return nil, fmt.Errorf("GLM5_API_BASE or -glm-api-base is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("GLM5_API_KEY or -glm-api-key is required")
	}
	return &chatClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

func (c *chatClient) chat(ctx context.Context, messages []chatMessage) (chatResponse, error) {
	reqBody := map[string]any{
		"model":               c.cfg.actualModelName(),
		"messages":            messages,
		"temperature":         0,
		"reasoning_effort":    c.cfg.actualReasoningEffort(),
		"tools":               bashTools(),
		"parallel_tool_calls": true,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return chatResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.chatCompletionsURL(), bytes.NewReader(data))
	if err != nil {
		return chatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	if c.cfg.RoutingKey != "" {
		req.Header.Set("X-SMG-Routing-Key", c.cfg.RoutingKey)
	}
	if c.cfg.AgentName != "" {
		req.Header.Set("X-SMG-Agent-Name", c.cfg.AgentName)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return chatResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyPreview, _, _ := limitOutput(string(body), 2000)
		return chatResponse{}, fmt.Errorf("chat completion failed: status=%d body=%s", resp.StatusCode, bodyPreview)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
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
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return chatResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return chatResponse{}, fmt.Errorf("chat completion returned no choices")
	}
	return chatResponse{
		Content:          decoded.Choices[0].Message.Content,
		ReasoningContent: decoded.Choices[0].Message.ReasoningContent,
		ToolCalls:        decoded.Choices[0].Message.ToolCalls,
		Usage: result.Usage{
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
			APICalls:         1,
		},
	}, nil
}

func bashTools() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "bash",
				"description": "Execute a bash command in the SWE-Bench repository environment.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "The bash command to execute.",
						},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

func (c GLMConfig) withEnv() GLMConfig {
	if c.APIBase == "" {
		c.APIBase = os.Getenv("GLM5_API_BASE")
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("GLM5_API_KEY")
	}
	if c.RoutingKey == "" {
		c.RoutingKey = os.Getenv("GLM5_ROUTING_KEY")
	}
	if c.AgentName == "" {
		c.AgentName = os.Getenv("GLM5_AGENT_NAME")
	}
	if c.AgentName == "" {
		c.AgentName = defaultGLMAgentName
	}
	if c.ModelName == "" {
		c.ModelName = os.Getenv("GLM5_MODEL")
	}
	if c.ReasoningEffort == "" {
		c.ReasoningEffort = os.Getenv("GLM5_REASONING_EFFORT")
	}
	if c.ReasoningEffort == "" {
		c.ReasoningEffort = "high"
	}
	return c
}

func (c GLMConfig) actualModelName() string {
	if c.ModelName != "" {
		return c.ModelName
	}
	return "glm50"
}

func (c GLMConfig) actualReasoningEffort() string {
	if c.ReasoningEffort != "" {
		return c.ReasoningEffort
	}
	return "high"
}

func (c GLMConfig) chatCompletionsURL() string {
	base := strings.TrimRight(c.APIBase, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}
