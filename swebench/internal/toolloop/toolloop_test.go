//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package toolloop

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestDetectorWarnsOnConsecutiveEquivalentBatchThenResets(t *testing.T) {
	detector := &Detector{}
	first := []Entry{
		{ToolName: "bash", Arguments: []byte(`{"command":"pwd","timeout":1}`), Observation: "same"},
		{ToolName: "bash", Arguments: []byte(`{"command":"git status"}`), Observation: "clean"},
	}
	second := []Entry{
		{ToolName: "bash", Arguments: []byte("{\n  \"timeout\": 1, \"command\": \"pwd\"\n}"), Observation: "same"},
		{ToolName: "bash", Arguments: []byte(`{ "command" : "git status" }`), Observation: "clean"},
	}
	assertObservation(t, detector, first, false)
	assertObservation(t, detector, second, true)
	assertObservation(t, detector, first, false)
	assertObservation(t, detector, second, true)
}

func TestDetectorDistinguishesBatchChangesAndOrder(t *testing.T) {
	base := []Entry{
		{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "one"},
		{ToolName: "search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
	}
	tests := []struct {
		name  string
		batch []Entry
	}{
		{name: "arguments", batch: []Entry{
			{ToolName: "bash", Arguments: []byte(`{"command":"ls"}`), Observation: "one"},
			{ToolName: "search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
		}},
		{name: "observation", batch: []Entry{
			{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "changed"},
			{ToolName: "search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
		}},
		{name: "tool name", batch: []Entry{
			{ToolName: "shell", Arguments: []byte(`{"command":"pwd"}`), Observation: "one"},
			{ToolName: "search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
		}},
		{name: "order", batch: []Entry{
			{ToolName: "search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
			{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "one"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detector := &Detector{}
			assertObservation(t, detector, base, false)
			assertObservation(t, detector, test.batch, false)
			assertObservation(t, detector, test.batch, true)
		})
	}
}

func TestDetectorResetsOnInvalidOrEmptyBatch(t *testing.T) {
	valid := []Entry{{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "same"}}
	for _, invalid := range [][]Entry{
		nil,
		{{ToolName: "bash", Arguments: []byte(`{"command":`), Observation: "same"}},
		{{ToolName: "bash", Arguments: []byte(`{} {}`), Observation: "same"}},
	} {
		detector := &Detector{}
		assertObservation(t, detector, valid, false)
		if warning, err := detector.Observe(invalid); err == nil || warning {
			t.Fatalf("Observe(invalid) = %v, %v, want false, error", warning, err)
		}
		assertObservation(t, detector, valid, false)
	}
}

func TestDetectorConfirmsBytesAfterHash(t *testing.T) {
	detector := &Detector{}
	batch := []Entry{{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "same"}}
	canonical, err := CanonicalBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	detector.previousHash = sha256.Sum256(canonical)
	detector.previousBytes = []byte("different canonical bytes")
	detector.hasPrevious = true
	assertObservation(t, detector, batch, false)
	if !bytes.Equal(detector.previousBytes, canonical) {
		t.Fatalf("previous bytes = %q, want %q", detector.previousBytes, canonical)
	}
}

func assertObservation(t *testing.T, detector *Detector, batch []Entry, want bool) {
	t.Helper()
	got, err := detector.Observe(batch)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if got != want {
		t.Fatalf("Observe() = %v, want %v", got, want)
	}
}
