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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
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
	if golden.UpstreamCommit != upstreamCommit {
		t.Fatalf("fixture commit = %q, implementation commit = %q", golden.UpstreamCommit, upstreamCommit)
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
