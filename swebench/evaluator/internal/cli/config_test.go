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

func TestLoadModelYAML(t *testing.T) {
	cfg, err := loadModelConfig("../../../config/models/glm-5.2.yaml.example")
	if err != nil {
		t.Fatalf("loadModelConfig() error = %v", err)
	}
	want := map[string]string{
		"MODEL_NAME":             "<model-name>",
		"OPENAI_BASE_URL":        "***/v1",
		"OPENAI_API_KEY":         "sk-***",
		"MODEL_TIMEOUT_SECONDS":  "300",
		"MODEL_REASONING_EFFORT": "high",
		"X_SMG_AGENT_NAME":       "BenchSWE",
	}
	for key, val := range want {
		if got := cfg[key]; got != val {
			t.Fatalf("cfg[%q] = %q, want %q", key, got, val)
		}
	}
}
