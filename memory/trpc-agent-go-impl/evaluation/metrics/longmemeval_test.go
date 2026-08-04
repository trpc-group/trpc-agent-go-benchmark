//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package metrics

import (
	"strings"
	"testing"
)

func TestParseLongMemEvalJudgeLabel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{
			name:  "exact yes",
			input: "yes",
			want:  true,
		},
		{
			name:  "punctuated no",
			input: "No.",
			want:  false,
		},
		{
			name:    "explanatory yes",
			input:   "The response contains the correct answer, so yes.",
			wantErr: true,
		},
		{
			name:    "final no",
			input:   "The response is incomplete. Final answer: no.",
			wantErr: true,
		},
		{
			name:    "last label rejected",
			input:   "It could be yes at first glance, but final answer: no.",
			wantErr: true,
		},
		{
			name:    "missing label",
			input:   "The user wants to know if the model's response",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLongMemEvalJudgeLabel(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseLongMemEvalJudgeLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLongMemEvalJudgePromptMentionsContradictoryContext(t *testing.T) {
	prompt, err := LongMemEvalJudgePrompt(
		"single-session-user",
		"What type of cocktail recipe did I try last weekend?",
		"lavender gin fizz",
		"I tried a lavender gin fizz, but not last weekend.",
		false,
	)
	if err != nil {
		t.Fatalf("LongMemEvalJudgePrompt() error = %v", err)
	}
	if !strings.Contains(prompt, "mentions the correct answer but clearly says it does not apply") {
		t.Fatalf("prompt does not contain contradiction instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "exactly one word: yes or no") {
		t.Fatalf("prompt does not contain exact output instruction:\n%s", prompt)
	}
}
