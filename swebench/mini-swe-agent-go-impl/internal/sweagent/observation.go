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
	observationcodec "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	environment "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

// Keep the source-aligned boundary visible to parity tests while delegating
// the actual encoding implementation to the shared observation package.
const maxObservation = observationcodec.MaxObservationRunes

// ObservationCodec selects the model-facing bash result representation.
type ObservationCodec = observationcodec.ObservationCodec

const (
	ObservationCodecXML  = observationcodec.ObservationCodecXML
	ObservationCodecJSON = observationcodec.ObservationCodecJSON
	ObservationCodecText = observationcodec.ObservationCodecText
)

// ParseObservationCodec validates a command-line codec value.
func ParseObservationCodec(value string) (ObservationCodec, error) {
	return observationcodec.ParseObservationCodec(value)
}

func normalizeObservationCodec(codec ObservationCodec) ObservationCodec {
	if codec == "" {
		return ObservationCodecXML
	}
	return codec
}

// FormatObservation retains the upstream v2.1.0 XML-like default.
func FormatObservation(result environment.CommandResult) string {
	return observationcodec.Format(result)
}

// FormatObservationWithCodec renders a bash result for the model.
func FormatObservationWithCodec(result environment.CommandResult, codec ObservationCodec) (string, error) {
	return observationcodec.FormatWithCodec(result, codec)
}
