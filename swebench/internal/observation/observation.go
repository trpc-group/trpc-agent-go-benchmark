//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package observation formats command results for a coding agent model.
package observation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

// MaxObservationRunes is the source-aligned output threshold. Output with
// exactly this many runes is represented as a head/tail observation.
const MaxObservationRunes = 10000

const observationWarning = `The output of your last command was too long.
Please try a different command that produces less output.
If you're looking at a file you can try use head, tail or sed to view a smaller number of lines selectively.
If you're using grep or find and it produced too much output, you can use a more selective search pattern.
If you really need to see something from the full command's output, you can redirect output to a file and then search in that file.`

// ObservationCodec selects the model-facing command result representation.
type ObservationCodec string

const (
	// ObservationCodecXML is the source-aligned XML-like representation.
	ObservationCodecXML ObservationCodec = "xml"
	// ObservationCodecJSON is a compact JSON representation.
	ObservationCodecJSON ObservationCodec = "json"
	// ObservationCodecText is a plain-text representation.
	ObservationCodecText ObservationCodec = "text"
)

// ParseObservationCodec validates a codec value. An empty value selects XML.
func ParseObservationCodec(value string) (ObservationCodec, error) {
	codec := ObservationCodec(strings.ToLower(strings.TrimSpace(value)))
	if codec == "" {
		return ObservationCodecXML, nil
	}
	switch codec {
	case ObservationCodecXML, ObservationCodecJSON, ObservationCodecText:
		return codec, nil
	default:
		return "", fmt.Errorf("observation codec must be xml, json, or text")
	}
}

func normalizeObservationCodec(codec ObservationCodec) ObservationCodec {
	if codec == "" {
		return ObservationCodecXML
	}
	return codec
}

type observationValue struct {
	ExceptionInfo string
	ReturnCode    int
	Output        *string
	Warning       string
	OutputHead    string
	ElidedRunes   *int
	OutputTail    string
}

func normalize(result sweenv.CommandResult) observationValue {
	value := observationValue{ExceptionInfo: result.ExceptionInfo, ReturnCode: result.ReturnCode}
	runes := []rune(result.Output)
	if len(runes) < MaxObservationRunes {
		output := result.Output
		value.Output = &output
		return value
	}
	elided := len(runes) - MaxObservationRunes
	value.Warning = observationWarning
	value.OutputHead = string(runes[:MaxObservationRunes/2])
	value.ElidedRunes = &elided
	value.OutputTail = string(runes[len(runes)-MaxObservationRunes/2:])
	return value
}

// Format renders a command result using the source-aligned XML-like
// default. The XML-like form intentionally does not escape result contents.
func Format(result sweenv.CommandResult) string {
	value, err := FormatWithCodec(result, ObservationCodecXML)
	if err != nil {
		panic(err)
	}
	return value
}

// FormatWithCodec renders a command result for the model.
func FormatWithCodec(result sweenv.CommandResult, codec ObservationCodec) (string, error) {
	codec = normalizeObservationCodec(codec)
	value := normalize(result)
	switch codec {
	case ObservationCodecXML:
		return formatXMLObservation(value), nil
	case ObservationCodecJSON:
		return formatJSONObservation(value)
	case ObservationCodecText:
		return formatTextObservation(value), nil
	default:
		return "", fmt.Errorf("observation codec must be xml, json, or text")
	}
}

func formatXMLObservation(value observationValue) string {
	var b strings.Builder
	if value.ExceptionInfo != "" {
		fmt.Fprintf(&b, "<exception>%s</exception>\n", value.ExceptionInfo)
	}
	fmt.Fprintf(&b, "<returncode>%d</returncode>\n", value.ReturnCode)
	if value.Output != nil {
		fmt.Fprintf(&b, "<output>\n%s</output>", *value.Output)
		return b.String()
	}
	fmt.Fprintf(&b, `<warning>
%s
</warning><output_head>
%s
</output_head>
<elided_chars>
%d characters elided
</elided_chars>
<output_tail>
%s
</output_tail>`, value.Warning, value.OutputHead, *value.ElidedRunes, value.OutputTail)
	return b.String()
}

type jsonObservation struct {
	ExceptionInfo string  `json:"exception,omitempty"`
	ReturnCode    int     `json:"returncode"`
	Output        *string `json:"output,omitempty"`
	Warning       string  `json:"warning,omitempty"`
	OutputHead    string  `json:"output_head,omitempty"`
	ElidedRunes   *int    `json:"elided_chars,omitempty"`
	OutputTail    string  `json:"output_tail,omitempty"`
}

func formatJSONObservation(value observationValue) (string, error) {
	doc := jsonObservation{
		ExceptionInfo: value.ExceptionInfo,
		ReturnCode:    value.ReturnCode,
		Output:        value.Output,
		Warning:       value.Warning,
		OutputHead:    value.OutputHead,
		ElidedRunes:   value.ElidedRunes,
		OutputTail:    value.OutputTail,
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func formatTextObservation(value observationValue) string {
	var b strings.Builder
	if value.ExceptionInfo != "" {
		fmt.Fprintf(&b, "exception: %s\n", value.ExceptionInfo)
	}
	fmt.Fprintf(&b, "returncode: %d\n", value.ReturnCode)
	if value.Output != nil {
		fmt.Fprintf(&b, "output:\n%s", *value.Output)
		return b.String()
	}
	fmt.Fprintf(&b, "warning:\n%s\noutput_head:\n%s\nelided_chars: %d\noutput_tail:\n%s",
		value.Warning, value.OutputHead, *value.ElidedRunes, value.OutputTail)
	return b.String()
}
