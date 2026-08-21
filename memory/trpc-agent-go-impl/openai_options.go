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
	"os"
	"strings"

	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
)

const (
	envLLMBaseURL = "LLM_BASE_URL"
	envLLMAPIKey  = "LLM_API_KEY" // #nosec G101 -- This is an environment variable name, not a credential.
	envLLMName    = "LLM_NAME"
)

func longMemEvalOpenAIOptions(apiKey, baseURL string) []openaimodel.Option {
	opts := make([]openaimodel.Option, 0, 2)
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		opts = append(opts, openaimodel.WithAPIKey(apiKey))
	}
	if baseURL = strings.TrimSpace(baseURL); baseURL != "" {
		opts = append(opts, openaimodel.WithBaseURL(baseURL))
	}
	return opts
}

func lmeLLMBaseURL() string {
	return strings.TrimSpace(os.Getenv(envLLMBaseURL))
}

func lmeLLMAPIKey() string {
	return strings.TrimSpace(os.Getenv(envLLMAPIKey))
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
