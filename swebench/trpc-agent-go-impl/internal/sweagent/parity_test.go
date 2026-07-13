//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type goldenText struct {
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}

type goldenObservation struct {
	OutputChars   int    `json:"output_chars"`
	ExceptionInfo string `json:"exception_info"`
	ReturnCode    int    `json:"returncode"`
	goldenText
}

type goldenFormatError struct {
	Error string `json:"error"`
	goldenText
}

type upstreamGolden struct {
	UpstreamCommit string              `json:"upstream_commit"`
	Task           string              `json:"task"`
	System         goldenText          `json:"system"`
	Instance       goldenText          `json:"instance"`
	Observations   []goldenObservation `json:"observations"`
	FormatErrors   []goldenFormatError `json:"format_errors"`
}

func loadGolden(t *testing.T) upstreamGolden {
	t.Helper()
	data, err := os.ReadFile("testdata/upstream_v2_1_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden upstreamGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

func assertGoldenText(t *testing.T, name, value string, golden goldenText) {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	if length := utf8.RuneCountInString(value); length != golden.Length || hex.EncodeToString(sum[:]) != golden.SHA256 {
		t.Fatalf("%s mismatch: length=%d sha256=%s, want length=%d sha256=%s", name, length, hex.EncodeToString(sum[:]), golden.Length, golden.SHA256)
	}
}

func TestUpstreamV21PromptGolden(t *testing.T) {
	golden := loadGolden(t)
	if golden.UpstreamCommit != UpstreamCommit {
		t.Fatalf("fixture commit = %q, implementation commit = %q", golden.UpstreamCommit, UpstreamCommit)
	}
	assertGoldenText(t, "system", SystemPrompt, golden.System)
	assertGoldenText(t, "instance", PromptForTask(golden.Task), golden.Instance)
}

func TestUpstreamV21ObservationGolden(t *testing.T) {
	golden := loadGolden(t)
	for _, fixture := range golden.Observations {
		fixture := fixture
		t.Run(strconv.Itoa(fixture.OutputChars), func(t *testing.T) {
			value := FormatObservation(environment.CommandResult{
				Output:        repeatRune('界', fixture.OutputChars),
				ReturnCode:    fixture.ReturnCode,
				ExceptionInfo: fixture.ExceptionInfo,
			})
			assertGoldenText(t, "observation", value, fixture.goldenText)
		})
	}
}

func TestUpstreamV21FormatErrorGolden(t *testing.T) {
	golden := loadGolden(t)
	for _, fixture := range golden.FormatErrors {
		assertGoldenText(t, fixture.Error, formatError(fixture.Error).Error(), fixture.goldenText)
	}
}

func repeatRune(value rune, count int) string {
	runes := make([]rune, count)
	for index := range runes {
		runes[index] = value
	}
	return string(runes)
}

func TestUpstreamV21NormalizedLoopGolden(t *testing.T) {
	const task = "fix deterministic issue"
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("invalid assistant is not retained"),
		assistantResponse("inspect", bashCall("inspect", "inspect")),
		assistantResponse("finish", bashCall("before", "before-submit"), bashCall("submit", "submit")),
	}}
	env := &fakeEnvironment{results: []environment.CommandResult{
		{Output: "inspected\n"},
		{Output: "before\n"},
		{Output: SubmissionMarker + "\ndiff --git a/a b/a\n"},
	}}
	result := (&MiniAgent{Model: modelImpl, Environment: env}).Run(context.Background(), task)
	messages := make([]map[string]any, 0, len(result.Messages))
	for _, message := range result.Messages {
		item := map[string]any{"role": message.Role}
		switch message.Role {
		case "system", "user", "tool":
			item["content"] = textDigest(message.Content)
		default:
			item["content"] = message.Content
		}
		switch message.Role {
		case "assistant":
			item["actions"] = message.Extra["actions"]
		case "tool":
			item["tool_call_id"] = message.ToolCallID
			item["raw_output"] = message.Extra["raw_output"]
			item["returncode"] = message.Extra["returncode"]
		case "exit":
			item["exit_status"] = message.Extra["exit_status"]
			item["submission"] = message.Extra["submission"]
		}
		messages = append(messages, item)
	}
	actual := map[string]any{
		"upstream_commit": UpstreamCommit,
		"task":            task,
		"info": map[string]any{
			"exit_status": result.Info.ExitStatus,
			"submission":  result.Info.Submission,
		},
		"api_calls": result.LLMCalls,
		"commands":  env.commands,
		"messages":  messages,
	}
	actualData, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var normalizedActual any
	if err := json.Unmarshal(actualData, &normalizedActual); err != nil {
		t.Fatal(err)
	}
	expectedData, err := os.ReadFile("testdata/upstream_v2_1_loop_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err := json.Unmarshal(expectedData, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalizedActual, expected) {
		pretty, _ := json.MarshalIndent(normalizedActual, "", "  ")
		t.Fatalf("normalized Go loop differs from upstream oracle:\n%s", pretty)
	}
}

func textDigest(value string) map[string]any {
	sum := sha256.Sum256([]byte(value))
	return map[string]any{
		"length": utf8.RuneCountInString(value),
		"sha256": hex.EncodeToString(sum[:]),
	}
}
