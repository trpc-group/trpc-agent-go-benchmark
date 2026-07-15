//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package minicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

const maxObservation = 10000

const observationWarning = `The output of your last command was too long.
Please try a different command that produces less output.
If you're looking at a file you can try use head, tail or sed to view a smaller number of lines selectively.
If you're using grep or find and it produced too much output, you can use a more selective search pattern.
If you really need to see something from the full command's output, you can redirect output to a file and then search in that file.`

// ObservationCodec selects the model-facing bash result representation.
type ObservationCodec string

const (
	ObservationCodecXML  ObservationCodec = "xml"
	ObservationCodecJSON ObservationCodec = "json"
	ObservationCodecText ObservationCodec = "text"
)

// ParseObservationCodec validates a command-line codec value.
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

type observationValue struct {
	ExceptionInfo string
	ReturnCode    int
	Output        *string
	Warning       string
	OutputHead    string
	ElidedChars   *int
	OutputTail    string
}

func normalizeObservation(result sweenv.CommandResult) observationValue {
	value := observationValue{ExceptionInfo: result.ExceptionInfo, ReturnCode: result.ReturnCode}
	runes := []rune(result.Output)
	if len(runes) < maxObservation {
		output := result.Output
		value.Output = &output
		return value
	}
	elided := len(runes) - maxObservation
	value.Warning = observationWarning
	value.OutputHead = string(runes[:5000])
	value.ElidedChars = &elided
	value.OutputTail = string(runes[len(runes)-5000:])
	return value
}

// FormatObservation renders a bash result for the model.
func FormatObservation(result sweenv.CommandResult, codec ObservationCodec) (string, error) {
	if codec == "" {
		codec = ObservationCodecXML
	}
	value := normalizeObservation(result)
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
</output_tail>`, value.Warning, value.OutputHead, *value.ElidedChars, value.OutputTail)
	return b.String()
}

type jsonObservation struct {
	ExceptionInfo string  `json:"exception,omitempty"`
	ReturnCode    int     `json:"returncode"`
	Output        *string `json:"output,omitempty"`
	Warning       string  `json:"warning,omitempty"`
	OutputHead    string  `json:"output_head,omitempty"`
	ElidedChars   *int    `json:"elided_chars,omitempty"`
	OutputTail    string  `json:"output_tail,omitempty"`
}

func formatJSONObservation(value observationValue) (string, error) {
	doc := jsonObservation{
		ExceptionInfo: value.ExceptionInfo,
		ReturnCode:    value.ReturnCode,
		Output:        value.Output,
		Warning:       value.Warning,
		OutputHead:    value.OutputHead,
		ElidedChars:   value.ElidedChars,
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
		value.Warning, value.OutputHead, *value.ElidedChars, value.OutputTail)
	return b.String()
}
