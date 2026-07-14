//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweagent

import "testing"

func TestClassifyError(t *testing.T) {
	tests := []struct {
		message   string
		category  string
		retryable bool
	}{
		{"status 429: too many requests", ErrorCategoryEndpointRateLimit, true},
		{"503 service unavailable", ErrorCategoryEndpointUnavailable, true},
		{"context deadline exceeded", ErrorCategoryEndpointTimeout, true},
		{"dial tcp: connection refused", ErrorCategoryEndpointNetwork, true},
		{"status 401: invalid api key", ErrorCategoryEndpointAuth, false},
		{"openai api error: status 400", ErrorCategoryEndpoint, false},
		{"agent stopped unexpectedly", ErrorCategoryAgent, false},
	}
	for _, test := range tests {
		t.Run(test.category, func(t *testing.T) {
			category, retryable := ClassifyError(test.message)
			if category != test.category || retryable != test.retryable {
				t.Fatalf("ClassifyError(%q) = (%q, %v), want (%q, %v)", test.message, category, retryable, test.category, test.retryable)
			}
		})
	}
}
