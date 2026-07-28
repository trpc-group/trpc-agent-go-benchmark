//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tagagent

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestToolLoopDetectorWarnsOnConsecutiveEquivalentBatch(t *testing.T) {
	detector := &toolLoopDetector{}
	first := []toolLoopEntry{
		{ToolName: "bash", Arguments: []byte(`{"command":"pwd","timeout":1}`), Observation: "same"},
		{ToolName: "bash", Arguments: []byte(`{"command":"git status"}`), Observation: "clean"},
	}
	second := []toolLoopEntry{
		{ToolName: "bash", Arguments: []byte("{\n  \"timeout\": 1, \"command\": \"pwd\"\n}"), Observation: "same"},
		{ToolName: "bash", Arguments: []byte(`{ "command" : "git status" }`), Observation: "clean"},
	}
	if warning, err := detector.observe(first); err != nil || warning {
		t.Fatalf("first observe = %v, %v, want false, nil", warning, err)
	}
	if warning, err := detector.observe(second); err != nil || !warning {
		t.Fatalf("second observe = %v, %v, want true, nil", warning, err)
	}
	if detector.hasPrevious {
		t.Fatal("detector retained history after warning")
	}
	if warning, err := detector.observe(first); err != nil || warning {
		t.Fatalf("third observe = %v, %v, want false, nil", warning, err)
	}
	if warning, err := detector.observe(second); err != nil || !warning {
		t.Fatalf("fourth observe = %v, %v, want true, nil", warning, err)
	}
}

func TestToolLoopDetectorDistinguishesBatchChanges(t *testing.T) {
	base := []toolLoopEntry{
		{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "one"},
		{ToolName: "code_search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
	}
	tests := []struct {
		name  string
		batch []toolLoopEntry
	}{
		{name: "arguments", batch: []toolLoopEntry{
			{ToolName: "bash", Arguments: []byte(`{"command":"ls"}`), Observation: "one"},
			{ToolName: "code_search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
		}},
		{name: "observation", batch: []toolLoopEntry{
			{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "changed"},
			{ToolName: "code_search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
		}},
		{name: "order", batch: []toolLoopEntry{
			{ToolName: "code_search", Arguments: []byte(`{"query":"symbol"}`), Observation: "two"},
			{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "one"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detector := &toolLoopDetector{}
			if warning, err := detector.observe(base); err != nil || warning {
				t.Fatalf("base observe = %v, %v", warning, err)
			}
			if warning, err := detector.observe(tt.batch); err != nil || warning {
				t.Fatalf("changed observe = %v, %v, want false, nil", warning, err)
			}
		})
	}
}

func TestToolLoopDetectorResetsOnInvalidBatch(t *testing.T) {
	detector := &toolLoopDetector{}
	valid := []toolLoopEntry{{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "same"}}
	if warning, err := detector.observe(valid); err != nil || warning {
		t.Fatalf("valid observe = %v, %v", warning, err)
	}
	invalid := []toolLoopEntry{{ToolName: "bash", Arguments: []byte(`{"command":`), Observation: "same"}}
	if warning, err := detector.observe(invalid); err == nil || warning {
		t.Fatalf("invalid observe = %v, %v, want false, error", warning, err)
	}
	if warning, err := detector.observe(valid); err != nil || warning {
		t.Fatalf("observe after reset = %v, %v, want false, nil", warning, err)
	}
}

func TestToolLoopDetectorConfirmsBytesAfterHash(t *testing.T) {
	detector := &toolLoopDetector{}
	batch := []toolLoopEntry{{ToolName: "bash", Arguments: []byte(`{"command":"pwd"}`), Observation: "same"}}
	canonical, err := canonicalToolLoopBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	detector.previousHash = sha256.Sum256(canonical)
	detector.previousBytes = []byte("different canonical bytes")
	detector.hasPrevious = true
	warning, err := detector.observe(batch)
	if err != nil || warning {
		t.Fatalf("observe = %v, %v, want false, nil", warning, err)
	}
	if !bytes.Equal(detector.previousBytes, canonical) {
		t.Fatalf("previous bytes = %q, want %q", detector.previousBytes, canonical)
	}
}

func TestCanonicalJSONRejectsMultipleValues(t *testing.T) {
	if _, err := canonicalJSON([]byte(`{} {}`)); err == nil {
		t.Fatal("canonicalJSON accepted multiple values")
	}
}
