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
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const toolLoopWarning = `<tool_loop_detected>
A repeated tool-use/result cycle was detected: T -> T.
The repeated cycle produced no new model-visible information.
Do not immediately repeat the same cycle. Reuse the existing observations, reconsider the approach in light of the original task, and choose a different next action that can make progress.
</tool_loop_detected>`

type toolLoopEntry struct {
	ToolName    string
	Arguments   []byte
	Observation string
}

type canonicalToolLoopEntry struct {
	ToolName    string          `json:"tool_name"`
	Arguments   json.RawMessage `json:"arguments"`
	Observation string          `json:"observation"`
}

type toolLoopDetector struct {
	previousHash  [sha256.Size]byte
	previousBytes []byte
	hasPrevious   bool
}

func (d *toolLoopDetector) observe(entries []toolLoopEntry) (bool, error) {
	canonical, err := canonicalToolLoopBatch(entries)
	if err != nil {
		d.reset()
		return false, err
	}
	hash := sha256.Sum256(canonical)
	if d.hasPrevious && hash == d.previousHash && bytes.Equal(canonical, d.previousBytes) {
		d.reset()
		return true, nil
	}
	d.previousHash = hash
	d.previousBytes = append(d.previousBytes[:0], canonical...)
	d.hasPrevious = true
	return false, nil
}

func (d *toolLoopDetector) reset() {
	d.previousHash = [sha256.Size]byte{}
	d.previousBytes = nil
	d.hasPrevious = false
}

func canonicalToolLoopBatch(entries []toolLoopEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("tool loop batch is empty")
	}
	canonical := make([]canonicalToolLoopEntry, 0, len(entries))
	for i, entry := range entries {
		arguments, err := canonicalJSON(entry.Arguments)
		if err != nil {
			return nil, fmt.Errorf("canonicalize tool loop arguments %d: %w", i, err)
		}
		canonical = append(canonical, canonicalToolLoopEntry{
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
