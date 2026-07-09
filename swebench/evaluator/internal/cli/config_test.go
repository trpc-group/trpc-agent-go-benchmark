//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import "testing"

func TestLoadMiniSWEAgentYAML(t *testing.T) {
	cfg, err := loadModelConfig("../../../config/models/minimax-m2.5.yaml.example")
	if err != nil {
		t.Fatalf("loadModelConfig() error = %v", err)
	}
	want := map[string]string{
		"MODEL_NAME":             "minimax-m2.5",
		"OPENAI_BASE_URL":        "http://03-llm.woa.com/gateway/v1",
		"MODEL_TIMEOUT_SECONDS":  "120",
		"MODEL_REASONING_EFFORT": "high",
		"X_SMG_ROUTING_KEY":      "haoranchang",
		"X_SMG_AGENT_NAME":       "BenchSWE",
	}
	for key, val := range want {
		if got := cfg[key]; got != val {
			t.Fatalf("cfg[%q] = %q, want %q", key, got, val)
		}
	}
	if got := cfg["OPENAI_API_KEY"]; got != "" {
		t.Fatalf("OPENAI_API_KEY = %q, want empty", got)
	}
}

func TestLoadGLM52YAML(t *testing.T) {
	cfg, err := loadModelConfig("../../../config/models/glm-5.2.yaml.example")
	if err != nil {
		t.Fatalf("loadModelConfig() error = %v", err)
	}
	want := map[string]string{
		"MODEL_NAME":             "glm-5.2",
		"MODEL_TIMEOUT_SECONDS":  "120",
		"MODEL_REASONING_EFFORT": "high",
		"X_SMG_AGENT_NAME":       "BenchSWE",
	}
	for key, val := range want {
		if got := cfg[key]; got != val {
			t.Fatalf("cfg[%q] = %q, want %q", key, got, val)
		}
	}
	if got := cfg["OPENAI_BASE_URL"]; got != "" {
		t.Fatalf("OPENAI_BASE_URL = %q, want empty", got)
	}
	if got := cfg["OPENAI_API_KEY"]; got != "" {
		t.Fatalf("OPENAI_API_KEY = %q, want empty", got)
	}
}
