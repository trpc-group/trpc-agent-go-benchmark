//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package retrievalreplay re-executes recorded workspace-retrieval queries
// from portable, content-addressed inputs. It never invokes an LLM or an
// embedding endpoint and does not use SWE-Bench gold patches.
package retrievalreplay

import (
	"context"
	"encoding/json"
	"io"
)

const (
	// BundleSchemaVersion is the supported portable input schema.
	BundleSchemaVersion = 1
	// QuerySetSchemaVersion is the supported recorded-query schema.
	QuerySetSchemaVersion = 1
	// ResultSetSchemaVersion is the supported expected-result schema.
	ResultSetSchemaVersion = 1
	// ReportSchemaVersion is the deterministic replay report schema.
	ReportSchemaVersion = 1

	BundleKind    = "trpc-agent-go-swebench-retrieval-replay-bundle"
	QuerySetKind  = "trpc-agent-go-swebench-recorded-retrieval-queries"
	ResultSetKind = "trpc-agent-go-swebench-recorded-retrieval-results"
	ReportKind    = "trpc-agent-go-swebench-retrieval-replay-report"

	NativeManifestFilename = "native-runner-manifest.json"
)

// ArtifactRef identifies one file relative to the bundle manifest directory.
// Absolute paths and path traversal are rejected by Load.
type ArtifactRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// EngineDescriptor binds replay to a concrete, offline implementation.
// ImplementationSHA256 should identify the implementation source or binary;
// it is deliberately separate from the configuration artifact digest.
type EngineDescriptor struct {
	Name                 string `json:"name"`
	Version              string `json:"version"`
	ImplementationSHA256 string `json:"implementation_sha256"`
	OfflineOnly          bool   `json:"offline_only"`
}

// NativeRunBinding binds a bundle to one immutable Native run manifest.
type NativeRunBinding struct {
	RunID          string `json:"run_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

// EmbeddingIdentity prevents vectors produced by different embedding models
// from being silently mixed. It contains no endpoint or credential.
type EmbeddingIdentity struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	ModelFingerprint string `json:"model_fingerprint"`
	Dimensions       int    `json:"dimensions"`
	IdentitySHA256   string `json:"identity_sha256"`
}

// RetrievalProvenance describes the exact corpus/index/search contract that
// an Engine must reconstruct. SHA fields are logical fingerprints emitted by
// the original indexer, not host paths.
type RetrievalProvenance struct {
	CorpusFormat                  string             `json:"corpus_format"`
	CorpusSHA256                  string             `json:"corpus_sha256"`
	EligibleFileSetSHA256         string             `json:"eligible_file_set_sha256"`
	EligibleContentSHA256         string             `json:"eligible_content_sha256"`
	DocumentSetSHA256             string             `json:"document_set_sha256"`
	WorkspaceRepresentation       string             `json:"workspace_representation"`
	WorkspaceRepresentationSchema string             `json:"workspace_representation_schema"`
	WorkspaceRepresentationSHA256 string             `json:"workspace_representation_sha256"`
	ParserDependency              string             `json:"parser_dependency,omitempty"`
	ParserRuntimeSHA256           string             `json:"parser_runtime_sha256,omitempty"`
	RetrievalMode                 string             `json:"retrieval_mode"`
	MaxResults                    int                `json:"max_results"`
	InvocationDedup               bool               `json:"invocation_dedup"`
	Embedding                     *EmbeddingIdentity `json:"embedding,omitempty"`
}

// CaseSpec binds one replay case to its Native trajectory, portable corpus,
// recorded queries, optional embedding cache, and expected ranked results.
// InputSHA256 excludes ExpectedResults so output changes cannot redefine the
// input being replayed.
type CaseSpec struct {
	InstanceID              string              `json:"instance_id"`
	NativeArtifactSHA256    string              `json:"native_artifact_sha256"`
	ResponsesArtifactSHA256 string              `json:"responses_artifact_sha256"`
	Corpus                  ArtifactRef         `json:"corpus"`
	Queries                 ArtifactRef         `json:"queries"`
	EmbeddingCache          *ArtifactRef        `json:"embedding_cache,omitempty"`
	ExpectedResults         ArtifactRef         `json:"expected_results"`
	Retrieval               RetrievalProvenance `json:"retrieval"`
	InputSHA256             string              `json:"input_sha256"`
}

// Bundle is the portable replay manifest. Cases must be sorted by instance ID
// and CaseSetSHA256 binds that exact ordered selection.
type Bundle struct {
	SchemaVersion int              `json:"schema_version"`
	Kind          string           `json:"kind"`
	NativeRun     NativeRunBinding `json:"native_run"`
	Engine        EngineDescriptor `json:"engine"`
	EngineConfig  ArtifactRef      `json:"engine_config"`
	CaseSetSHA256 string           `json:"case_set_sha256"`
	Cases         []CaseSpec       `json:"cases"`
}

// RecordedQuery is one exact code_search invocation recovered from the Native
// response/event artifacts. Arguments contains canonical JSON and is supplied
// to the offline engine so filters or future query fields are not discarded.
type RecordedQuery struct {
	Ordinal               int             `json:"ordinal"`
	ToolCallID            string          `json:"tool_call_id"`
	ToolName              string          `json:"tool_name"`
	Query                 string          `json:"query"`
	QuerySHA256           string          `json:"query_sha256"`
	Arguments             json.RawMessage `json:"arguments"`
	ArgumentsSHA256       string          `json:"arguments_sha256"`
	NativeArguments       string          `json:"native_arguments"`
	NativeArgumentsSHA256 string          `json:"native_arguments_sha256"`
}

// QuerySet is the portable sequence of queries for one agent invocation.
type QuerySet struct {
	SchemaVersion                 int             `json:"schema_version"`
	Kind                          string          `json:"kind"`
	InstanceID                    string          `json:"instance_id"`
	EmittedCodeSearchCalls        int             `json:"emitted_code_search_calls"`
	SkippedEmittedCodeSearchCalls int             `json:"skipped_emitted_code_search_calls"`
	Queries                       []RecordedQuery `json:"queries"`
}

// RankedDocument is a content-free, deterministic result identity. ScoreBits
// is the lowercase 16-digit hexadecimal IEEE-754 float64 representation,
// avoiding JSON float-format ambiguity across replay implementations.
type RankedDocument struct {
	Rank           int    `json:"rank"`
	DocumentID     string `json:"document_id"`
	SourcePath     string `json:"source_path,omitempty"`
	ContentSHA256  string `json:"content_sha256"`
	MetadataSHA256 string `json:"metadata_sha256"`
	ScoreBits      string `json:"score_bits"`
}

// ExpectedQueryResult binds the expected ordering to one recorded query.
type ExpectedQueryResult struct {
	Ordinal      int              `json:"ordinal"`
	QuerySHA256  string           `json:"query_sha256"`
	Status       string           `json:"status"`
	ErrorSHA256  string           `json:"error_sha256,omitempty"`
	ResultSHA256 string           `json:"result_sha256"`
	Documents    []RankedDocument `json:"documents"`
}

// ExpectedResultSet is captured from the original online retrieval run. It
// contains no source text and is never used as replay engine input.
type ExpectedResultSet struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	InstanceID    string                `json:"instance_id"`
	InputSHA256   string                `json:"input_sha256"`
	Results       []ExpectedQueryResult `json:"results"`
}

// VerifiedArtifact is an already path-bounded and content-addressed portable
// artifact. Open rechecks both filesystem safety and SHA-256 to fail closed if
// the bundle changes after Load. Host paths are intentionally not exposed.
type VerifiedArtifact struct {
	RelativePath string
	SHA256       string
	root         string
}

// Open returns a verified reader positioned at the start of the artifact.
func (a VerifiedArtifact) Open() (io.ReadCloser, error) {
	return openVerifiedArtifact(a.root, ArtifactRef{Path: a.RelativePath, SHA256: a.SHA256})
}

// ReadAll returns verified artifact bytes up to limit bytes. A non-positive
// limit is rejected rather than interpreted as unlimited.
func (a VerifiedArtifact) ReadAll(limit int64) ([]byte, error) {
	return readVerifiedArtifact(a.root, ArtifactRef{Path: a.RelativePath, SHA256: a.SHA256}, limit)
}

// NativeCaseIdentity is the subset of Native case metadata relevant to
// retrieval provenance. Candidate patches are deliberately absent.
type NativeCaseIdentity struct {
	InstanceID      string
	RunID           string
	Repo            string
	BaseCommit      string
	ExitStatus      string
	LLMCalls        int
	ToolCalls       int
	ResponseCount   int
	ResponsesSHA256 string
}

// LoadedCase is one fully validated case ready for offline execution.
type LoadedCase struct {
	Spec            CaseSpec
	Native          NativeCaseIdentity
	Corpus          VerifiedArtifact
	EmbeddingCache  *VerifiedArtifact
	Queries         QuerySet
	Expected        ExpectedResultSet
	NativeSHA256    string
	ResponsesSHA256 string
	QuerySetSHA256  string
	ExpectedSHA256  string
}

// LoadedBundle contains validated local handles and deterministic identities.
// BundleRoot and RunRoot are intentionally private and never enter reports.
type LoadedBundle struct {
	Spec                 Bundle
	BundleSHA256         string
	NativeManifestSHA256 string
	EngineConfig         VerifiedArtifact
	Cases                []LoadedCase
	bundleRoot           string
	runRoot              string
}

// BuildInput contains the only inputs an offline engine may use to construct
// an index. Expected results and model responses are intentionally absent.
type BuildInput struct {
	InstanceID     string
	Repository     string
	Corpus         VerifiedArtifact
	EmbeddingCache *VerifiedArtifact
	EngineConfig   VerifiedArtifact
	Provenance     RetrievalProvenance
}

// IndexIdentity is independently reported by a rebuilt index and checked
// against RetrievalProvenance before any query is executed.
type IndexIdentity struct {
	CorpusSHA256                  string
	EligibleFileSetSHA256         string
	EligibleContentSHA256         string
	DocumentSetSHA256             string
	WorkspaceRepresentation       string
	WorkspaceRepresentationSchema string
	WorkspaceRepresentationSHA256 string
	ParserDependency              string
	ParserRuntimeSHA256           string
	RetrievalMode                 string
	MaxResults                    int
	InvocationDedup               bool
	EmbeddingIdentitySHA256       string
	EmbeddingCacheSHA256          string
}

// SearchRequest preserves the exact recorded arguments and ordering.
type SearchRequest struct {
	Ordinal         int
	Query           string
	QuerySHA256     string
	Arguments       json.RawMessage
	ArgumentsSHA256 string
	MaxResults      int
}

// Document is the engine-facing retrieved document. Source content is reduced
// to a digest before it enters the deterministic report.
type Document struct {
	ID             string
	SourcePath     string
	ContentSHA256  string
	MetadataSHA256 string
	Score          float64
}

// Index is a rebuilt offline index. Search calls are made sequentially on one
// instance so invocation-scoped dedup semantics are preserved.
type Index interface {
	Identity() IndexIdentity
	Search(context.Context, SearchRequest) ([]Document, error)
	Close() error
}

// Engine reconstructs an index from a portable corpus and optional frozen
// embedding cache. Implementations must not invoke models or endpoints.
type Engine interface {
	Descriptor() EngineDescriptor
	Build(context.Context, BuildInput) (Index, error)
}

// QueryComparison records an exact ordering/fingerprint comparison.
type QueryComparison struct {
	Ordinal              int              `json:"ordinal"`
	QuerySHA256          string           `json:"query_sha256"`
	ExpectedStatus       string           `json:"expected_status"`
	ActualStatus         string           `json:"actual_status"`
	ExpectedErrorSHA256  string           `json:"expected_error_sha256,omitempty"`
	ActualErrorSHA256    string           `json:"actual_error_sha256,omitempty"`
	ExpectedResultSHA256 string           `json:"expected_result_sha256"`
	ActualResultSHA256   string           `json:"actual_result_sha256"`
	Match                bool             `json:"match"`
	ExpectedDocuments    []RankedDocument `json:"expected_documents"`
	ActualDocuments      []RankedDocument `json:"actual_documents"`
}

// CaseReport is deterministic and path-free.
type CaseReport struct {
	InstanceID                    string            `json:"instance_id"`
	InputSHA256                   string            `json:"input_sha256"`
	NativeArtifactSHA256          string            `json:"native_artifact_sha256"`
	ResponsesSHA256               string            `json:"responses_artifact_sha256"`
	CorpusSHA256                  string            `json:"corpus_sha256"`
	QuerySetSHA256                string            `json:"query_set_sha256"`
	ExpectedResultsSHA256         string            `json:"expected_results_sha256"`
	EmittedCodeSearchCalls        int               `json:"emitted_code_search_calls"`
	ExecutedCodeSearchCalls       int               `json:"executed_code_search_calls"`
	SkippedEmittedCodeSearchCalls int               `json:"skipped_emitted_code_search_calls"`
	Queries                       []QueryComparison `json:"queries"`
	Match                         bool              `json:"match"`
}

// Summary aggregates comparisons without introducing timing noise.
type Summary struct {
	Cases      int `json:"cases"`
	Queries    int `json:"queries"`
	Matches    int `json:"matches"`
	Mismatches int `json:"mismatches"`
}

// Report is the deterministic, machine-readable replay output.
type Report struct {
	SchemaVersion           int              `json:"schema_version"`
	Kind                    string           `json:"kind"`
	Status                  string           `json:"status"`
	BundleSHA256            string           `json:"bundle_sha256"`
	NativeRunManifestSHA256 string           `json:"native_run_manifest_sha256"`
	NativeRunID             string           `json:"native_run_id"`
	Engine                  EngineDescriptor `json:"engine"`
	EngineConfigSHA256      string           `json:"engine_config_sha256"`
	CaseSetSHA256           string           `json:"case_set_sha256"`
	Cases                   []CaseReport     `json:"cases"`
	Summary                 Summary          `json:"summary"`
}
