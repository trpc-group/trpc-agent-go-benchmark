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

func TestExtractResponseUsageSumsCallsAndCache(t *testing.T) {
	data := []byte(`[
		{"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":80}}},
		{"usage":{"prompt_tokens":200,"completion_tokens":20,"total_tokens":220,"prompt_tokens_details":{"cached_tokens":150}}},
		{"done":true}
	]`)
	got, err := extractResponseUsage(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.PromptTokens != 300 || got.PromptCachedTokens != 230 || got.PromptUncached != 70 ||
		got.CompletionTokens != 30 || got.TotalTokens != 330 {
		t.Fatalf("usage = %+v", got)
	}
	if got.ResponseObjects != 3 || got.UsageObjects != 2 || got.MissingUsage != 1 {
		t.Fatalf("coverage = %+v", got)
	}
}

func TestAddUsageSumsAllFields(t *testing.T) {
	total := usageStats{PromptTokens: 1, PromptCachedTokens: 1, TotalTokens: 2, APICalls: 1}
	addUsage(&total, usageStats{
		PromptTokens: 10, PromptCachedTokens: 8, PromptUncached: 2,
		CompletionTokens: 3, TotalTokens: 13, ResponseObjects: 2,
		UsageObjects: 1, MissingUsage: 1, APICalls: 2,
	})
	if total.PromptTokens != 11 || total.PromptCachedTokens != 9 || total.PromptUncached != 2 ||
		total.CompletionTokens != 3 || total.TotalTokens != 15 || total.ResponseObjects != 2 ||
		total.UsageObjects != 1 || total.MissingUsage != 1 || total.APICalls != 3 {
		t.Fatalf("total = %+v", total)
	}
}
