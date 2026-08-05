//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestUpstreamV21PromptGolden(t *testing.T) {
	assertGoldenText(t, "system", SystemPrompt, 95, "f554fe1eeb6b4f3ce9021cf87edbafc12ceee37b8b3a6838b0aebb4125ab3714")
	assertGoldenText(t, "instance", PromptForTask("fix 100% behavior\nsecond line"), 4520, "007c6e7e66c34b5ff2b8d2826fe0962625d40f0bb04535185ba31d3b530c4319")
}

func TestUpstreamV21FormatErrorGolden(t *testing.T) {
	cases := []struct {
		name   string
		calls  []model.ToolCall
		length int
		digest string
	}{
		{name: "missing", length: 554, digest: "1a74a233aaaf5a8254b94cd91236e30d6eb2af98435fee5e8608bd612266456a"},
		{
			name: "unknown",
			calls: []model.ToolCall{{Function: model.FunctionDefinitionParam{
				Name: "other", Arguments: []byte(`{}`),
			}}},
			length: 532, digest: "3dc1cc45d59c00e00a715dbf6a984984717c9e639fc3dc91d6862ce066a73c4f",
		},
		{
			name: "invalid-json",
			calls: []model.ToolCall{{Function: model.FunctionDefinitionParam{
				Name: "bash", Arguments: []byte(`{"command":`),
			}}},
			length: 591, digest: "55ea2138a42693d0b6e3aa6a6f1e28f32d99c388ef3bfa0a975c9b7273347609",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseActions(tc.calls)
			if err == nil {
				t.Fatal("ParseActions unexpectedly succeeded")
			}
			var formatErr FormatError
			if !errors.As(err, &formatErr) {
				t.Fatalf("error type = %T, want FormatError", err)
			}
			assertGoldenText(t, "format error", err.Error(), tc.length, tc.digest)
		})
	}
}

func TestParseActionsAndSubmission(t *testing.T) {
	calls := []model.ToolCall{{
		ID: "call-1",
		Function: model.FunctionDefinitionParam{
			Name: "bash", Arguments: []byte(`{"command":"git status --short"}`),
		},
	}}
	actions, err := ParseActions(calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Command != "git status --short" || actions[0].ToolCallID != "call-1" {
		t.Fatalf("actions = %#v", actions)
	}

	patch, submitted := SubmissionFromResult(sweenv.CommandResult{
		Output: " \n" + SubmissionMarker + "\n", ReturnCode: 0,
	})
	if !submitted || patch != "" {
		t.Fatalf("submission = (%q, %v), want empty successful patch", patch, submitted)
	}
	if _, submitted := SubmissionFromResult(sweenv.CommandResult{
		Output: SubmissionMarker + "\npatch", ReturnCode: 1,
	}); submitted {
		t.Fatal("failed command was accepted as a submission")
	}
}

func TestClassifyError(t *testing.T) {
	category, retryable := ClassifyError("POST /chat/completions: context deadline exceeded")
	if category != ErrorCategoryEndpointTimeout || !retryable {
		t.Fatalf("classification = (%q, %v)", category, retryable)
	}
	category, retryable = ClassifyError("model response contains no choices")
	if category != ErrorCategoryAgent || retryable {
		t.Fatalf("classification = (%q, %v)", category, retryable)
	}
}

func assertGoldenText(t *testing.T, name, value string, length int, digest string) {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	gotDigest := hex.EncodeToString(sum[:])
	gotLength := utf8.RuneCountInString(value)
	if gotLength != length || gotDigest != digest {
		t.Fatalf("%s mismatch: length=%d sha256=%s, want length=%d sha256=%s", name, gotLength, gotDigest, length, digest)
	}
}
