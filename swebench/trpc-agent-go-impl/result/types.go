//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package result defines shared run, case, and comparison artifacts.
package result

import "time"

// RunnerKind identifies the source that produced predictions.
type RunnerKind string

const (
	RunnerMini   RunnerKind = "mini"
	RunnerNative RunnerKind = "native"
)

// Usage tracks model service resource consumption.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	APICalls         int `json:"api_calls,omitempty"`
	Retries          int `json:"retries,omitempty"`
}

// RunConfig captures enough metadata to audit one run.
type RunConfig struct {
	RunID       string    `json:"run_id"`
	Runner      string    `json:"runner,omitempty"`
	Model       string    `json:"model"`
	Dataset     string    `json:"dataset,omitempty"`
	InstanceCnt int       `json:"instance_count,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	StepLimit   int       `json:"step_limit,omitempty"`
	TokenLimit  int       `json:"token_limit,omitempty"`
	Timeout     string    `json:"timeout,omitempty"`
	DryRun      bool      `json:"dry_run,omitempty"`
	CommandLine string    `json:"command_line,omitempty"`
	Verifier    string    `json:"verifier,omitempty"`
	Source      string    `json:"source,omitempty"`
}

// CaseResult is the per-instance normalized result.
type CaseResult struct {
	InstanceID   string   `json:"instance_id"`
	Runner       string   `json:"runner,omitempty"`
	Status       string   `json:"status"`
	Resolved     bool     `json:"resolved"`
	PatchPath    string   `json:"patch_path,omitempty"`
	TracePath    string   `json:"trace_path,omitempty"`
	LogPath      string   `json:"log_path,omitempty"`
	ReportRef    string   `json:"report_ref,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	PatchAdded   int      `json:"patch_added,omitempty"`
	PatchDeleted int      `json:"patch_deleted,omitempty"`
	DurationMs   int64    `json:"duration_ms,omitempty"`
	Usage        Usage    `json:"usage,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Summary aggregates case-level outcomes.
type Summary struct {
	RunID        string  `json:"run_id"`
	Runner       string  `json:"runner,omitempty"`
	Total        int     `json:"total"`
	Resolved     int     `json:"resolved"`
	Unresolved   int     `json:"unresolved"`
	Errors       int     `json:"errors"`
	Incomplete   int     `json:"incomplete"`
	ResolvedRate float64 `json:"resolved_rate"`
	TotalTokens  int     `json:"total_tokens,omitempty"`
	APICalls     int     `json:"api_calls,omitempty"`
	DurationMs   int64   `json:"duration_ms,omitempty"`
}

// Comparison is the top-level report data.
type Comparison struct {
	RunID   string       `json:"run_id"`
	Mini    Summary      `json:"mini"`
	Native  Summary      `json:"native"`
	Delta   DeltaSummary `json:"delta"`
	CaseIDs []string     `json:"case_ids,omitempty"`
}

// DeltaSummary stores native-minus-baseline differences.
type DeltaSummary struct {
	Resolved     int     `json:"resolved"`
	ResolvedRate float64 `json:"resolved_rate"`
	TotalTokens  int     `json:"total_tokens,omitempty"`
	DurationMs   int64   `json:"duration_ms,omitempty"`
}
