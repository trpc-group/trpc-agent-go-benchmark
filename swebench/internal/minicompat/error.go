//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package minicompat

import "strings"

const (
	ErrorCategoryEndpointRateLimit   = "endpoint_rate_limit"
	ErrorCategoryEndpointUnavailable = "endpoint_unavailable"
	ErrorCategoryEndpointTimeout     = "endpoint_timeout"
	ErrorCategoryEndpointAuth        = "endpoint_auth"
	ErrorCategoryEndpointNetwork     = "endpoint_network"
	ErrorCategoryEndpoint            = "endpoint_error"
	ErrorCategoryAgent               = "agent_error"
	ErrorCategoryCaseTimeout         = "case_timeout"
	ErrorCategoryAgentLimit          = "agent_limit"
	ErrorCategoryEnvironment         = "environment"
	ErrorCategoryArtifact            = "artifact"
)

// ClassifyError separates retryable model-service failures from agent errors.
func ClassifyError(message string) (string, bool) {
	value := strings.ToLower(message)
	switch {
	case containsAny(value, "rate limit", "rate_limit", "too many requests", "status 429", "status code 429", "statuscode=429", "http 429", "resource exhausted"):
		return ErrorCategoryEndpointRateLimit, true
	case containsAny(value, "service unavailable", "temporarily unavailable", "status 502", "status 503", "status 504", "status code 502", "status code 503", "status code 504", "statuscode=502", "statuscode=503", "statuscode=504", "http 502", "http 503", "http 504", "bad gateway", "gateway timeout", "overloaded"):
		return ErrorCategoryEndpointUnavailable, true
	case containsAny(value, "deadline exceeded", "request timeout", "i/o timeout", "timeout awaiting response headers", "client.timeout"):
		return ErrorCategoryEndpointTimeout, true
	case containsAny(value, "connection refused", "connection reset", "broken pipe", "dial tcp", "no such host", "network is unreachable", "unexpected eof"):
		return ErrorCategoryEndpointNetwork, true
	case containsAny(value, "unauthorized", "authentication", "invalid api key", "invalid_api_key", "status 401", "status 403", "status code 401", "status code 403"):
		return ErrorCategoryEndpointAuth, false
	case containsAny(value, "api error", "model service", "chat completion", "openai", "status 400", "status code 400"):
		return ErrorCategoryEndpoint, false
	default:
		return ErrorCategoryAgent, false
	}
}

func containsAny(value string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}
