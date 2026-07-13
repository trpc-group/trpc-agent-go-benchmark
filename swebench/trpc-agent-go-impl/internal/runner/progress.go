//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/sweagent"
)

type progressDocument struct {
	RunID     string                             `json:"run_id"`
	UpdatedAt time.Time                          `json:"updated_at"`
	Cases     map[string]sweagent.ProgressUpdate `json:"cases"`
}

type progressReporter struct {
	mu        sync.Mutex
	path      string
	document  progressDocument
	lastWrite time.Time
}

func newProgressReporter(path, runID string) *progressReporter {
	return &progressReporter{
		path: path,
		document: progressDocument{
			RunID: runID,
			Cases: map[string]sweagent.ProgressUpdate{},
		},
	}
}

func (r *progressReporter) Update(update sweagent.ProgressUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.document.Cases[update.InstanceID] = update
	now := time.Now()
	if update.Phase == "running" && !r.lastWrite.IsZero() && now.Sub(r.lastWrite) < time.Second {
		return
	}
	r.document.UpdatedAt = now.UTC()
	if err := artifact.WriteJSONAtomic(r.path, r.document); err == nil {
		r.lastWrite = now
	}
}

func (r *progressReporter) MarkSkipped(instanceID string, result sweagent.CaseResult) {
	now := time.Now().UTC()
	r.Update(sweagent.ProgressUpdate{
		InstanceID:    instanceID,
		Phase:         "skipped_existing",
		StartedAt:     now,
		LastEventAt:   now,
		EventCount:    result.EventCount,
		LLMCalls:      result.LLMCalls,
		ToolCalls:     result.ToolCalls,
		ExitStatus:    result.Info.ExitStatus,
		ErrorCategory: result.Info.ErrorCategory,
	})
}
