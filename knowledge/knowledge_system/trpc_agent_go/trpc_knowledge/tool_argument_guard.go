//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	toolArgumentPolicy              = "query-guard/v1"
	toolArgumentMaxRepairs          = 1
	toolArgumentValidationErrorType = "tool_argument_validation_error"
	toolArgumentRepairExhaustedType = "tool_argument_repair_exhausted"
	knowledgeSearchToolName         = "knowledge_search"
)

// toolArgumentValidationResult is returned to the model instead of executing
// a knowledge search with invalid arguments. It intentionally does not echo
// argument values.
type toolArgumentValidationResult struct {
	Type             string   `json:"type"`
	Policy           string   `json:"policy"`
	Message          string   `json:"message"`
	Allowed          []string `json:"allowed"`
	Missing          []string `json:"missing,omitempty"`
	Unexpected       []string `json:"unexpected,omitempty"`
	Invalid          []string `json:"invalid,omitempty"`
	Retryable        bool     `json:"retryable"`
	RemainingRepairs int      `json:"remaining_repairs"`
}

type toolArgumentValidation struct {
	missing    []string
	unexpected []string
	invalid    []string
}

func (v *toolArgumentValidation) valid() bool {
	return v != nil && len(v.missing) == 0 && len(v.unexpected) == 0 &&
		len(v.invalid) == 0
}

// queryArgumentGuard provides one model-visible correction opportunity per
// Agent execution. A second invalid call fails closed rather than silently
// rewriting model output or retrying the same arguments.
type queryArgumentGuard struct {
	mu              sync.Mutex
	invalidAttempts int
}

func newQueryArgumentGuard() *queryArgumentGuard {
	return &queryArgumentGuard{}
}

func (g *queryArgumentGuard) beforeTool(
	_ context.Context,
	args *tool.BeforeToolArgs,
) (*tool.BeforeToolResult, error) {
	if args == nil || args.ToolName != knowledgeSearchToolName {
		return nil, nil
	}
	validation := validateKnowledgeSearchArguments(args.Arguments)
	if validation.valid() {
		return nil, nil
	}

	g.mu.Lock()
	g.invalidAttempts++
	invalidAttempts := g.invalidAttempts
	g.mu.Unlock()
	if invalidAttempts > toolArgumentMaxRepairs {
		return nil, agent.NewStopError(toolArgumentRepairExhaustedType)
	}

	return &tool.BeforeToolResult{
		CustomResult: &toolArgumentValidationResult{
			Type:             toolArgumentValidationErrorType,
			Policy:           toolArgumentPolicy,
			Message:          "Call knowledge_search again with exactly one non-empty string field named query.",
			Allowed:          []string{"query"},
			Missing:          validation.missing,
			Unexpected:       validation.unexpected,
			Invalid:          validation.invalid,
			Retryable:        true,
			RemainingRepairs: toolArgumentMaxRepairs,
		},
	}, nil
}

func validateKnowledgeSearchArguments(arguments []byte) *toolArgumentValidation {
	validation := &toolArgumentValidation{}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &object); err != nil || object == nil {
		validation.invalid = []string{"arguments"}
		return validation
	}

	for field := range object {
		if field != "query" {
			validation.unexpected = append(validation.unexpected, field)
		}
	}
	sort.Strings(validation.unexpected)

	rawQuery, ok := object["query"]
	if !ok {
		validation.missing = []string{"query"}
		return validation
	}
	var query string
	if err := json.Unmarshal(rawQuery, &query); err != nil ||
		strings.TrimSpace(query) == "" {
		validation.invalid = []string{"query"}
	}
	return validation
}

func isToolArgumentValidationResponse(content string) bool {
	var payload struct {
		Type string `json:"type"`
	}
	return json.Unmarshal([]byte(content), &payload) == nil &&
		payload.Type == toolArgumentValidationErrorType
}
