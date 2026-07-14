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
	"encoding/json"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/mini-swe-agent-go-impl/internal/environment"
)

func TestFormatObservationCodecsShort(t *testing.T) {
	result := environment.CommandResult{Output: "<x>\n", ReturnCode: 7, ExceptionInfo: "boom"}
	tests := []struct {
		codec ObservationCodec
		want  string
	}{
		{
			codec: ObservationCodecJSON,
			want:  `{"exception":"boom","returncode":7,"output":"<x>\n"}`,
		},
		{
			codec: ObservationCodecText,
			want:  "exception: boom\nreturncode: 7\noutput:\n<x>\n",
		},
	}
	for _, test := range tests {
		got, err := FormatObservationWithCodec(result, test.codec)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("codec %s observation = %q, want %q", test.codec, got, test.want)
		}
	}
}

func TestFormatJSONObservationLongBoundary(t *testing.T) {
	for _, count := range []int{maxObservation, maxObservation + 1} {
		result := environment.CommandResult{Output: strings.Repeat("界", count), ReturnCode: -1}
		got, err := FormatObservationWithCodec(result, ObservationCodecJSON)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(got), &doc); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := doc["output"]; ok {
			t.Fatalf("long observation contains output: %s", got)
		}
		if got := int(doc["elided_chars"].(float64)); got != count-maxObservation {
			t.Fatalf("elided_chars = %d, want %d", got, count-maxObservation)
		}
		if len([]rune(doc["output_head"].(string))) != 5000 || len([]rune(doc["output_tail"].(string))) != 5000 {
			t.Fatal("head/tail rune count mismatch")
		}
		if doc["warning"] != observationWarning {
			t.Fatal("warning mismatch")
		}
	}
}

func TestFormatJSONObservationIncludesEmptyOutput(t *testing.T) {
	got, err := FormatObservationWithCodec(environment.CommandResult{}, ObservationCodecJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"returncode":0,"output":""}` {
		t.Fatalf("observation = %s", got)
	}
}

func TestParseObservationCodec(t *testing.T) {
	for _, value := range []string{"", "xml", "JSON", " text "} {
		if _, err := ParseObservationCodec(value); err != nil {
			t.Fatalf("ParseObservationCodec(%q): %v", value, err)
		}
	}
	if _, err := ParseObservationCodec("yaml"); err == nil {
		t.Fatal("invalid codec accepted")
	}
}
