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
	"sync"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/toolloop"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type expectedToolLoopCall struct {
	id   string
	name string
}

// toolLoopTracker joins a final assistant tool-call batch to the final
// model-visible messages produced after each tool completes.
type toolLoopTracker struct {
	mu             sync.Mutex
	enabled        bool
	pending        []expectedToolLoopCall
	next           int
	entries        []toolloop.Entry
	detector       toolloop.Detector
	pendingWarning bool
}

func newToolLoopTracker(enabled bool) *toolLoopTracker {
	return &toolLoopTracker{enabled: enabled}
}

func (t *toolLoopTracker) start(toolCalls []model.ToolCall) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) > 0 || len(toolCalls) == 0 {
		t.resetLocked()
	}
	if len(toolCalls) == 0 {
		return
	}
	t.pending = make([]expectedToolLoopCall, len(toolCalls))
	for i, call := range toolCalls {
		t.pending[i] = expectedToolLoopCall{id: call.ID, name: call.Function.Name}
	}
	t.next = 0
	t.entries = t.entries[:0]
}

func (t *toolLoopTracker) add(
	toolCallID string,
	toolName string,
	arguments []byte,
	observation string,
) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.next >= len(t.pending) {
		t.resetLocked()
		return
	}
	expected := t.pending[t.next]
	if expected.id != toolCallID || expected.name != toolName {
		t.resetLocked()
		return
	}
	t.entries = append(t.entries, toolloop.Entry{
		ToolName: toolName, Arguments: append([]byte(nil), arguments...), Observation: observation,
	})
	t.next++
	if t.next < len(t.pending) {
		return
	}
	entries := append([]toolloop.Entry(nil), t.entries...)
	t.pending = nil
	t.next = 0
	t.entries = t.entries[:0]
	warning, err := t.detector.Observe(entries)
	if err != nil {
		t.pendingWarning = false
		return
	}
	if warning {
		t.pendingWarning = true
	}
}

func (t *toolLoopTracker) takeWarning() bool {
	if t == nil || !t.enabled {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.pendingWarning {
		return false
	}
	t.pendingWarning = false
	return true
}

func (t *toolLoopTracker) reset() {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetLocked()
}

func (t *toolLoopTracker) resetLocked() {
	t.pending = nil
	t.next = 0
	t.entries = nil
	t.detector.Reset()
	t.pendingWarning = false
}
