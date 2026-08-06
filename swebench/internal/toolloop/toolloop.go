//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package toolloop detects exact consecutive tool-use/result cycles.
package toolloop

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Warning is appended to the next real model request after an exact repeated
// tool batch has completed.
const Warning = `<tool_loop_detected>
A repeated tool-use/result cycle was detected: T -> T.
The repeated cycle produced no new model-visible information.
Do not immediately repeat the same cycle. Reuse the existing observations, reconsider the approach in light of the original task, and choose a different next action that can make progress.
</tool_loop_detected>`

const (
	// Algorithm names the comparison represented by Detector.
	Algorithm = "exact-complete-tool-batch"
	// Version identifies the frozen detector semantics for runtime/replay parity.
	Version = "toolloop-v1"
)

// Entry is one completed tool call and its final model-visible observation.
type Entry struct {
	ToolName    string
	Arguments   []byte
	Observation string
}

type canonicalEntry struct {
	ToolName    string          `json:"tool_name"`
	Arguments   json.RawMessage `json:"arguments"`
	Observation string          `json:"observation"`
}

// Detector compares consecutive complete tool batches. The zero value is
// ready to use.
type Detector struct {
	previousHash  [sha256.Size]byte
	previousBytes []byte
	hasPrevious   bool
}

// Observe records a complete batch and reports whether it exactly repeats the
// preceding complete batch. A reported match clears the detector, so T,T,T
// reports only for the second T. Invalid input also clears the detector.
func (d *Detector) Observe(entries []Entry) (bool, error) {
	canonical, err := CanonicalBatch(entries)
	if err != nil {
		d.Reset()
		return false, err
	}
	hash := sha256.Sum256(canonical)
	if d.hasPrevious && hash == d.previousHash && bytes.Equal(canonical, d.previousBytes) {
		d.Reset()
		return true, nil
	}
	d.previousHash = hash
	d.previousBytes = append(d.previousBytes[:0], canonical...)
	d.hasPrevious = true
	return false, nil
}

// Reset clears the preceding batch.
func (d *Detector) Reset() {
	if d == nil {
		return
	}
	d.previousHash = [sha256.Size]byte{}
	d.previousBytes = nil
	d.hasPrevious = false
}

// CanonicalBatch returns stable bytes for a complete tool batch. JSON object
// key order and insignificant whitespace in Arguments do not affect the result.
func CanonicalBatch(entries []Entry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("tool loop batch is empty")
	}
	canonical := make([]canonicalEntry, 0, len(entries))
	for i, entry := range entries {
		arguments, err := canonicalJSON(entry.Arguments)
		if err != nil {
			return nil, fmt.Errorf("canonicalize tool loop arguments %d: %w", i, err)
		}
		canonical = append(canonical, canonicalEntry{
			ToolName: entry.ToolName, Arguments: arguments, Observation: entry.Observation,
		})
	}
	return json.Marshal(canonical)
}

func canonicalJSON(input []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}
