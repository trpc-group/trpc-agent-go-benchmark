//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.

package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode"

	openaiopt "github.com/openai/openai-go/option"
	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/runner"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

const (
	defaultAPIKeyEnv      = "OPENAI_API_KEY"
	defaultOpenAIBaseURL  = "https://api.openai.com/v1"
	openAIFunctionNameMax = 64
	openAIMaxRetries      = 0
)

// openAIConfig contains only the validated values needed to construct the
// framework model. The API key never appears in report types or error text.
type openAIConfig struct {
	model   string
	baseURL string
	apiKey  string
}

func openAIConfigFromEnv(modelName, baseURL, apiKeyEnv string) (openAIConfig, error) {
	if apiKeyEnv == "" {
		apiKeyEnv = defaultAPIKeyEnv
	} else {
		apiKeyEnv = strings.TrimSpace(apiKeyEnv)
		if apiKeyEnv == "" {
			return openAIConfig{}, fmt.Errorf("provider API key environment variable name must not be blank")
		}
	}
	if err := validateEnvironmentName(apiKeyEnv); err != nil {
		return openAIConfig{}, err
	}
	apiKey, ok := os.LookupEnv(apiKeyEnv)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return openAIConfig{}, fmt.Errorf("provider API key environment variable %q is not set", apiKeyEnv)
	}
	if strings.TrimSpace(apiKey) != apiKey {
		return openAIConfig{}, fmt.Errorf("provider API key must not have surrounding whitespace")
	}
	if containsControl(apiKey) {
		return openAIConfig{}, fmt.Errorf("provider API key contains a control character")
	}

	if modelName == "" {
		modelName = strings.TrimSpace(os.Getenv("MODEL_NAME"))
	} else {
		modelName = strings.TrimSpace(modelName)
	}
	if modelName == "" {
		return openAIConfig{}, fmt.Errorf("provider model name is required")
	}
	if containsControl(modelName) {
		return openAIConfig{}, fmt.Errorf("provider model name contains a control character")
	}

	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	} else {
		baseURL = strings.TrimSpace(baseURL)
		if baseURL == "" {
			return openAIConfig{}, fmt.Errorf("provider base URL must not be blank")
		}
	}
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	if err := validateOpenAIBaseURL(baseURL); err != nil {
		return openAIConfig{}, err
	}
	return openAIConfig{model: modelName, baseURL: baseURL, apiKey: apiKey}, nil
}

func (c openAIConfig) Factory() runner.ModelFactory {
	options := []openai.Option{
		openai.WithAPIKey(c.apiKey),
		openai.WithBaseURL(c.baseURL),
		openai.WithShowToolCallDelta(true),
		openai.WithOpenAIOptions(openaiopt.WithMaxRetries(openAIMaxRetries)),
	}
	return func(runner.ModelInput) model.Model {
		return openai.New(c.model, options...)
	}
}

func validateOpenAISuite(suite bench.Suite) error {
	if err := suite.Validate(); err != nil {
		return err
	}
	for _, spec := range suite.Tools {
		name := bench.QualifiedToolName(spec)
		if !validOpenAIFunctionName(name) {
			return fmt.Errorf("tool %q has an OpenAI-incompatible model-facing name %q", spec.Name, name)
		}
	}
	return nil
}

func validOpenAIFunctionName(name string) bool {
	if name == "" || len(name) > openAIFunctionNameMax {
		return false
	}
	for _, char := range []byte(name) {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validateOpenAIBaseURL(raw string) error {
	if strings.TrimSpace(raw) != raw || containsControl(raw) {
		return fmt.Errorf("provider base URL must not have surrounding whitespace or control characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("provider base URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("provider base URL scheme %q is not supported", parsed.Scheme)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("provider base URL must not contain userinfo, query, fragment, or opaque data")
	}
	return nil
}

func validateEnvironmentName(name string) error {
	if name == "" {
		return fmt.Errorf("provider API key environment variable name must not be blank")
	}
	for _, char := range name {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			continue
		}
		return fmt.Errorf("provider API key environment variable name %q is invalid", name)
	}
	return nil
}

func containsControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return true
		}
	}
	return false
}
