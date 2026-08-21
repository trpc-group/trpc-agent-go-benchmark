//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package observation

import (
	"strconv"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

func TestFormatObservationCodecsExactBytes(t *testing.T) {
	result := sweenv.CommandResult{Output: "<x>&\n", ReturnCode: 7, ExceptionInfo: "boom<&"}
	tests := []struct {
		name  string
		codec ObservationCodec
		want  string
	}{
		{
			name:  "xml-like unescaped",
			codec: ObservationCodecXML,
			want:  "<exception>boom<&</exception>\n<returncode>7</returncode>\n<output>\n<x>&\n</output>",
		},
		{
			name:  "json html unescaped",
			codec: ObservationCodecJSON,
			want:  `{"exception":"boom<&","returncode":7,"output":"<x>&\n"}`,
		},
		{
			name:  "text",
			codec: ObservationCodecText,
			want:  "exception: boom<&\nreturncode: 7\noutput:\n<x>&\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := FormatWithCodec(result, test.codec)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("observation bytes = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatObservationIncludesEmptyOutput(t *testing.T) {
	tests := []struct {
		codec ObservationCodec
		want  string
	}{
		{ObservationCodecXML, "<returncode>0</returncode>\n<output>\n</output>"},
		{ObservationCodecJSON, `{"returncode":0,"output":""}`},
		{ObservationCodecText, "returncode: 0\noutput:\n"},
	}
	for _, test := range tests {
		got, err := FormatWithCodec(sweenv.CommandResult{}, test.codec)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("codec %s observation = %q, want %q", test.codec, got, test.want)
		}
	}
}

func TestFormatObservationRuneBoundariesExactBytes(t *testing.T) {
	short := strings.Repeat("界", MaxObservationRunes-1)
	got, err := FormatWithCodec(sweenv.CommandResult{Output: short}, ObservationCodecJSON)
	if err != nil {
		t.Fatal(err)
	}
	wantShort := `{"returncode":0,"output":"` + short + `"}`
	if got != wantShort {
		t.Fatalf("9999-rune observation was not preserved exactly")
	}

	for _, count := range []int{MaxObservationRunes, MaxObservationRunes + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			head := strings.Repeat("头", MaxObservationRunes/2)
			tail := strings.Repeat("尾", MaxObservationRunes/2)
			middle := strings.Repeat("中", count-MaxObservationRunes)
			input := head + middle + tail
			elided := strconv.Itoa(len([]rune(middle)))
			tests := []struct {
				codec ObservationCodec
				want  string
			}{
				{
					ObservationCodecXML,
					"<returncode>-1</returncode>\n<warning>\n" + observationWarning +
						"\n</warning><output_head>\n" + head +
						"\n</output_head>\n<elided_chars>\n" + elided + " characters elided" +
						"\n</elided_chars>\n<output_tail>\n" + tail + "\n</output_tail>",
				},
				{
					ObservationCodecJSON,
					`{"returncode":-1,"warning":` + quotedJSON(observationWarning) +
						`,"output_head":` + quotedJSON(head) +
						`,"elided_chars":` + elided +
						`,"output_tail":` + quotedJSON(tail) + `}`,
				},
				{
					ObservationCodecText,
					"returncode: -1\nwarning:\n" + observationWarning +
						"\noutput_head:\n" + head +
						"\nelided_chars: " + elided +
						"\noutput_tail:\n" + tail,
				},
			}
			for _, test := range tests {
				got, err := FormatWithCodec(sweenv.CommandResult{Output: input, ReturnCode: -1}, test.codec)
				if err != nil {
					t.Fatal(err)
				}
				if got != test.want {
					t.Fatalf("%d-rune %s observation bytes differ", count, test.codec)
				}
			}
		})
	}
}

func quotedJSON(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return `"` + value + `"`
}

func TestFormatObservationDefaultAndValidation(t *testing.T) {
	result := sweenv.CommandResult{Output: "ok"}
	if got := Format(result); got != "<returncode>0</returncode>\n<output>\nok</output>" {
		t.Fatalf("default observation = %q", got)
	}
	got, err := FormatWithCodec(result, "")
	if err != nil || got != Format(result) {
		t.Fatalf("empty codec = %q, %v", got, err)
	}
	for _, value := range []string{"", "xml", "JSON", " text "} {
		if _, err := ParseObservationCodec(value); err != nil {
			t.Fatalf("ParseObservationCodec(%q): %v", value, err)
		}
	}
	if _, err := ParseObservationCodec("yaml"); err == nil {
		t.Fatal("invalid codec accepted")
	}
	if _, err := FormatWithCodec(result, "yaml"); err == nil {
		t.Fatal("invalid codec formatted")
	}
}
