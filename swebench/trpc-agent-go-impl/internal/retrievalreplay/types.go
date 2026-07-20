//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package retrievalreplay evaluates workspace retrieval representations
// without running the coding agent.
package retrievalreplay

import (
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
)

const reportSchemaVersion = 1

type goldLabel struct {
	InstanceID string `json:"instance_id"`
	Patch      string `json:"patch"`
}

type patchAnchor struct {
	File string
	Text string
}

type patchTargets struct {
	TargetFiles []string
	NewFiles    []string
	Anchors     []patchAnchor
}

// RetrievalMetrics captures gold-patch localization quality.
type RetrievalMetrics struct {
	TargetFiles                int     `json:"target_files"`
	HunkAnchors                int     `json:"hunk_anchors"`
	TargetFileRecallAt4        float64 `json:"target_file_recall_at_4"`
	TargetFileRecallAt6        float64 `json:"target_file_recall_at_6"`
	TargetFileReciprocalRank   float64 `json:"target_file_reciprocal_rank"`
	HunkAnchorRecallAt4        float64 `json:"hunk_anchor_recall_at_4"`
	HunkAnchorRecallAt6        float64 `json:"hunk_anchor_recall_at_6"`
	TargetFileCharPrecisionAt6 float64 `json:"target_file_char_precision_at_6"`
}

// RetrievedDocument is a content-free trace of one retrieval result.
type RetrievedDocument struct {
	Rank          int     `json:"rank"`
	Score         float64 `json:"score"`
	FilePath      string  `json:"file_path,omitempty"`
	ASTType       string  `json:"ast_type,omitempty"`
	ContentChars  int     `json:"content_chars"`
	ContentSHA256 string  `json:"content_sha256"`
}

// ArmResult records one representation on one exact workspace snapshot.
type ArmResult struct {
	Representation string                       `json:"representation"`
	DurationMS     int64                        `json:"duration_ms"`
	Index          tagagent.WorkspaceIndexStats `json:"index"`
	Metrics        RetrievalMetrics             `json:"metrics"`
	Embedding      *embeddingconfig.Metrics     `json:"embedding,omitempty"`
	EmbeddingCache *embeddingcache.Metrics      `json:"embedding_cache,omitempty"`
	Retrieved      []RetrievedDocument          `json:"retrieved,omitempty"`
	Error          string                       `json:"error,omitempty"`
}

// CaseResult records all representation arms for one case.
type CaseResult struct {
	InstanceID  string      `json:"instance_id"`
	Repo        string      `json:"repo,omitempty"`
	QuerySHA256 string      `json:"query_sha256"`
	TargetFiles int         `json:"target_files"`
	NewFiles    int         `json:"new_files,omitempty"`
	HunkAnchors int         `json:"hunk_anchors"`
	SnapshotMS  int64       `json:"snapshot_ms"`
	Arms        []ArmResult `json:"arms,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// Aggregate summarizes one representation across completed cases.
type Aggregate struct {
	Cases                          int     `json:"cases"`
	SuccessfulCases                int     `json:"successful_cases"`
	Errors                         int     `json:"errors"`
	MeanTargetFileRecallAt4        float64 `json:"mean_target_file_recall_at_4"`
	MeanTargetFileRecallAt6        float64 `json:"mean_target_file_recall_at_6"`
	MeanTargetFileReciprocalRank   float64 `json:"mean_target_file_reciprocal_rank"`
	MeanHunkAnchorRecallAt4        float64 `json:"mean_hunk_anchor_recall_at_4"`
	MeanHunkAnchorRecallAt6        float64 `json:"mean_hunk_anchor_recall_at_6"`
	MeanTargetFileCharPrecisionAt6 float64 `json:"mean_target_file_char_precision_at_6"`
	MeanFileCoverage               float64 `json:"mean_file_coverage"`
	MeanDocuments                  float64 `json:"mean_documents"`
	MeanIndexDurationMS            float64 `json:"mean_index_duration_ms"`
	FallbackDocuments              int     `json:"fallback_documents"`
	EmbeddingRequests              int64   `json:"embedding_requests"`
	EmbeddingInputs                int64   `json:"embedding_inputs"`
	EmbeddingErrors                int64   `json:"embedding_errors"`
	EmbeddingDurationMS            int64   `json:"embedding_duration_ms"`
	CacheHits                      int64   `json:"cache_hits"`
	CacheMisses                    int64   `json:"cache_misses"`
	CacheWrites                    int64   `json:"cache_writes"`
}

// Report is the checkpointed machine-readable replay artifact.
type Report struct {
	SchemaVersion       int                  `json:"schema_version"`
	RunID               string               `json:"run_id"`
	Status              string               `json:"status"`
	SourceRevision      string               `json:"source_revision,omitempty"`
	SourceModified      bool                 `json:"source_modified"`
	BinarySHA256        string               `json:"binary_sha256"`
	FrameworkRevision   string               `json:"framework_revision,omitempty"`
	StartedAt           time.Time            `json:"started_at"`
	FinishedAt          time.Time            `json:"finished_at,omitempty"`
	DurationMS          int64                `json:"duration_ms"`
	CasesPath           string               `json:"cases_path"`
	CasesSHA256         string               `json:"cases_sha256"`
	CaseListPath        string               `json:"case_list_path,omitempty"`
	CaseListSHA256      string               `json:"case_list_sha256,omitempty"`
	LabelsPath          string               `json:"labels_path"`
	LabelsSHA256        string               `json:"labels_sha256"`
	EnvironmentConfig   string               `json:"environment_config"`
	EnvironmentSHA256   string               `json:"environment_config_sha256"`
	EmbeddingConfig     map[string]any       `json:"embedding_config,omitempty"`
	EmbeddingConfigPath string               `json:"embedding_config_path,omitempty"`
	EmbeddingConfigSHA  string               `json:"embedding_config_sha256,omitempty"`
	EmbeddingCacheDB    string               `json:"embedding_cache_db,omitempty"`
	QueryMode           string               `json:"query_mode"`
	MaxResults          int                  `json:"max_results"`
	Representations     []string             `json:"representations"`
	SelectedCases       int                  `json:"selected_cases"`
	CompletedCases      int                  `json:"completed_cases"`
	CaseWorkers         int                  `json:"case_workers"`
	Cases               []CaseResult         `json:"cases"`
	Aggregate           map[string]Aggregate `json:"aggregate"`
}
