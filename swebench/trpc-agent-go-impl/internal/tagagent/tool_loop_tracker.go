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

	"trpc.group/trpc-go/trpc-agent-go/model"
)

type expectedToolLoopCall struct {
	id   string
	name string
}

type toolLoopTracker struct {
	mu             sync.Mutex
	enabled        bool
	pending        []expectedToolLoopCall
	next           int
	entries        []toolLoopEntry
	detector       toolLoopDetector
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
) bool {
	if t == nil || !t.enabled {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.next >= len(t.pending) {
		t.resetLocked()
		return false
	}
	expected := t.pending[t.next]
	if expected.id != toolCallID || expected.name != toolName {
		t.resetLocked()
		return false
	}
	t.entries = append(t.entries, toolLoopEntry{
		ToolName: toolName, Arguments: append([]byte(nil), arguments...), Observation: observation,
	})
	t.next++
	if t.next < len(t.pending) {
		return false
	}
	entries := append([]toolLoopEntry(nil), t.entries...)
	t.pending = nil
	t.next = 0
	t.entries = t.entries[:0]
	warning, err := t.detector.observe(entries)
	if err != nil {
		return false
	}
	if warning {
		t.pendingWarning = true
	}
	return warning
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
	t.detector.reset()
	t.pendingWarning = false
}
