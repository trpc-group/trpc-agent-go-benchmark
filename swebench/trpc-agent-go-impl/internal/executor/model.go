//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package executor

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	openaiopt "github.com/openai/openai-go/option"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

func newModel(cfg modelconfig.EnvConfig) model.Model {
	opts := []openai.Option{
		openai.WithAPIKey(cfg["OPENAI_API_KEY"]),
		openai.WithBaseURL(cfg["OPENAI_BASE_URL"]),
		openai.WithOpenAIOptions(openaiopt.WithMaxRetries(9)),
	}
	headers := map[string]string{}
	for envKey, header := range map[string]string{
		"X_SMG_ROUTING_KEY": "X-SMG-Routing-Key",
		"X_SMG_AGENT_NAME":  "X-SMG-Agent-Name",
		"X_SMG_PROVIDER":    "X-SMG-Provider",
	} {
		if value := strings.TrimSpace(cfg[envKey]); value != "" {
			headers[header] = value
		}
	}
	if len(headers) > 0 {
		opts = append(opts, openai.WithHeaders(headers))
	}
	if seconds, err := strconv.ParseFloat(cfg["MODEL_TIMEOUT_SECONDS"], 64); err == nil && seconds > 0 {
		opts = append(opts, openai.WithHTTPClientOptions(openai.WithHTTPClientTransport(timeoutTransport{
			base:    http.DefaultTransport,
			timeout: time.Duration(seconds * float64(time.Second)),
		})))
	}
	return openai.New(cfg["MODEL_NAME"], opts...)
}

type timeoutTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t timeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(request.Context(), t.timeout)
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func generationConfig(cfg modelconfig.EnvConfig) model.GenerationConfig {
	temperature := 0.0
	if value, err := strconv.ParseFloat(cfg["MODEL_TEMPERATURE"], 64); err == nil {
		temperature = value
	}
	gen := model.GenerationConfig{Temperature: &temperature}
	if effort := strings.TrimSpace(cfg["MODEL_REASONING_EFFORT"]); effort != "" {
		gen.ReasoningEffort = &effort
	}
	return gen
}
