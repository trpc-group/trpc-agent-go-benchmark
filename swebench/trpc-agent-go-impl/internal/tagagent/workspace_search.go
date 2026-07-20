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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/chunking"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	docreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader"
	pythonreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/python"
	textreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/text"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source/repo"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	defaultWorkspaceChunkSize    = 1024
	defaultWorkspaceChunkOverlap = 128
)

var defaultWorkspaceSkipDirs = []string{
	".git", ".tox", ".venv", "venv", "node_modules", "build", "dist",
}

// WorkspaceRepresentation identifies the document representation used to
// build one workspace index.
type WorkspaceRepresentation string

const (
	// WorkspaceRepresentationCurrentFixed preserves the historical line-trimmed
	// fixed-size text chunks.
	WorkspaceRepresentationCurrentFixed WorkspaceRepresentation = "current-fixed"
	// WorkspaceRepresentationFixedRaw keeps Python indentation in fixed chunks.
	WorkspaceRepresentationFixedRaw WorkspaceRepresentation = "fixed-raw"
	// WorkspaceRepresentationASTCode embeds code from AST-bounded nodes.
	WorkspaceRepresentationASTCode WorkspaceRepresentation = "ast-code"
	// WorkspaceRepresentationASTStructured embeds stable AST fields plus code.
	WorkspaceRepresentationASTStructured WorkspaceRepresentation = "ast-structured"
)

// ParseWorkspaceRepresentation validates a representation CLI value.
func ParseWorkspaceRepresentation(value string) (WorkspaceRepresentation, error) {
	representation := WorkspaceRepresentation(strings.TrimSpace(value))
	if representation == "" {
		representation = WorkspaceRepresentationCurrentFixed
	}
	switch representation {
	case WorkspaceRepresentationCurrentFixed,
		WorkspaceRepresentationFixedRaw,
		WorkspaceRepresentationASTCode,
		WorkspaceRepresentationASTStructured:
		return representation, nil
	default:
		return "", fmt.Errorf(
			"unsupported workspace representation %q (want current-fixed, fixed-raw, ast-code, or ast-structured)",
			value,
		)
	}
}

// WorkspaceRepresentationSchema returns the canonical index schema string.
func WorkspaceRepresentationSchema(representation WorkspaceRepresentation) string {
	return representationSchema(representation)
}

// WorkspaceRepresentationSHA256 returns the canonical index schema hash.
func WorkspaceRepresentationSHA256(representation WorkspaceRepresentation) string {
	return hashStrings([]string{representationSchema(representation)})
}

// WorkspaceIndexStats captures per-case indexing cost, representation
// provenance, coverage, and stability signals separately from model usage.
type WorkspaceIndexStats struct {
	Representation        string         `json:"representation"`
	RepresentationSchema  string         `json:"representation_schema"`
	RepresentationSHA256  string         `json:"representation_sha256"`
	Documents             int            `json:"documents"`
	EligibleFiles         int            `json:"eligible_files"`
	IndexedFiles          int            `json:"indexed_files"`
	FileCoverage          float64        `json:"file_coverage"`
	EligibleFileSetSHA256 string         `json:"eligible_file_set_sha256"`
	IndexedFileSetSHA256  string         `json:"indexed_file_set_sha256"`
	DocumentSetSHA256     string         `json:"document_set_sha256"`
	MissingFiles          []string       `json:"missing_files,omitempty"`
	FallbackDocuments     int            `json:"fallback_documents,omitempty"`
	FallbackReasons       map[string]int `json:"fallback_reasons,omitempty"`
	NodeTypes             map[string]int `json:"node_types,omitempty"`
	ContentChars          int            `json:"content_chars"`
	EmbeddingTextChars    int            `json:"embedding_text_chars"`
	DuplicateDocuments    int            `json:"duplicate_documents,omitempty"`
	DuplicateDocumentRate float64        `json:"duplicate_document_rate"`
	DurationMS            int64          `json:"duration_ms"`
	PreloadedDocuments    int            `json:"preloaded_documents,omitempty"`
	PreloadedChars        int            `json:"preloaded_chars,omitempty"`
	PreloadInjected       bool           `json:"preload_injected"`
	RetrievalMode         string         `json:"retrieval_mode,omitempty"`
}

// WorkspaceSearchConfig controls representation, optional dense retrieval,
// and context bounds.
type WorkspaceSearchConfig struct {
	Embedder       embedder.Embedder
	SearchMode     vectorstore.SearchMode
	BatchSize      int
	DocConcurrency int
	MaxResults     int
	MaxChars       int
	Representation WorkspaceRepresentation
	RepositoryName string
}

// WorkspaceIndex owns one loaded workspace knowledge base.
type WorkspaceIndex struct {
	knowledge *knowledge.BuiltinKnowledge
	config    *WorkspaceSearchConfig
}

// Search retrieves raw documents for offline replay and diagnostics.
func (i *WorkspaceIndex) Search(
	ctx context.Context,
	query string,
	maxResults int,
) (*knowledge.SearchResult, error) {
	if i == nil || i.knowledge == nil {
		return nil, fmt.Errorf("workspace index is not initialized")
	}
	if maxResults <= 0 {
		maxResults = 6
	}
	return i.knowledge.Search(ctx, &knowledge.SearchRequest{
		Query:      query,
		MaxResults: maxResults,
		MinScore:   0,
		SearchMode: int(i.config.SearchMode),
	})
}

// BuildContext retrieves bounded model-facing context.
func (i *WorkspaceIndex) BuildContext(
	ctx context.Context,
	query string,
) (*knowledge.ContextResult, error) {
	if i == nil || i.knowledge == nil {
		return nil, fmt.Errorf("workspace index is not initialized")
	}
	return knowledge.BuildContext(ctx, i.knowledge, &knowledge.ContextRequest{
		Query:      query,
		MaxResults: i.config.MaxResults,
		MaxChars:   i.config.MaxChars,
		SearchMode: int(i.config.SearchMode),
	})
}

// Tool returns the compact code_search tool backed by this index.
func (i *WorkspaceIndex) Tool() tool.Tool {
	return knowledgetool.NewCompactCodeSearchTool(
		i.knowledge,
		knowledgetool.WithCodeSearchMode(i.config.SearchMode),
		knowledgetool.WithCodeSearchMaxResults(6),
		knowledgetool.WithCodeSearchMinScore(0),
		knowledgetool.WithCodeSearchExtraExcludeMetadataKeys(
			"file_mode", "file_size", "modified_at", "repo_path", "source", "source_name",
		),
	)
}

// Close releases the knowledge base.
func (i *WorkspaceIndex) Close() error {
	if i == nil || i.knowledge == nil {
		return nil
	}
	return i.knowledge.Close()
}

// NewWorkspaceCodeSearch snapshots and indexes one environment, returning a
// compact code-search tool and a cleanup function.
func NewWorkspaceCodeSearch(
	ctx context.Context,
	environment sweenv.Environment,
	instanceID string,
	query string,
	config *WorkspaceSearchConfig,
) (tool.Tool, func() error, WorkspaceIndexStats, string, error) {
	index, stats, err := NewWorkspaceIndexFromEnvironment(
		ctx,
		environment,
		instanceID,
		config,
	)
	if err != nil {
		return nil, nil, WorkspaceIndexStats{}, "", err
	}
	preloaded, err := index.BuildContext(ctx, query)
	if err != nil {
		_ = index.Close()
		return nil, nil, WorkspaceIndexStats{}, "", fmt.Errorf("preload workspace context: %w", err)
	}
	stats.PreloadedDocuments = preloaded.Documents
	stats.PreloadedChars = preloaded.Chars
	return index.Tool(), index.Close, stats, preloaded.Text, nil
}

// NewWorkspaceIndexFromEnvironment snapshots and indexes one live environment.
func NewWorkspaceIndexFromEnvironment(
	ctx context.Context,
	environment sweenv.Environment,
	instanceID string,
	config *WorkspaceSearchConfig,
) (*WorkspaceIndex, WorkspaceIndexStats, error) {
	config, err := normalizeWorkspaceSearchConfig(config)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	snapshotter, ok := environment.(sweenv.WorkspaceSnapshotter)
	if !ok {
		return nil, WorkspaceIndexStats{}, fmt.Errorf("environment does not support workspace snapshots")
	}
	observation := &workspaceObservation{}
	materializer := &workspaceMaterializer{
		snapshotter: snapshotter,
		instanceID:  instanceID,
		observation: observation,
	}
	repositoryName := config.RepositoryName
	if repositoryName == "" {
		repositoryName = instanceID
	}
	config.RepositoryName = repositoryName
	src, err := newWorkspaceSource(repo.Repository{
		RepoName: repositoryName,
	}, materializer, config)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	return loadWorkspaceIndex(ctx, src, observation, config)
}

// NewWorkspaceIndexFromDirectory indexes an already materialized checkout.
// It is used by retrieval replay so every representation sees the same bytes.
func NewWorkspaceIndexFromDirectory(
	ctx context.Context,
	directory string,
	repositoryName string,
	config *WorkspaceSearchConfig,
) (*WorkspaceIndex, WorkspaceIndexStats, error) {
	config, err := normalizeWorkspaceSearchConfig(config)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	if strings.TrimSpace(repositoryName) != "" {
		config.RepositoryName = strings.TrimSpace(repositoryName)
	} else if config.RepositoryName == "" {
		config.RepositoryName = "workspace"
	}
	eligibleFiles, err := eligiblePythonFiles(directory)
	if err != nil {
		return nil, WorkspaceIndexStats{}, fmt.Errorf("scan workspace files: %w", err)
	}
	observation := &workspaceObservation{eligibleFiles: eligibleFiles}
	src, err := newWorkspaceSource(repo.Repository{
		Dir:      directory,
		RepoName: config.RepositoryName,
	}, nil, config)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	return loadWorkspaceIndex(ctx, src, observation, config)
}

func newWorkspaceSource(
	repository repo.Repository,
	materializer repo.Materializer,
	config *WorkspaceSearchConfig,
) (*observingSource, error) {
	workspaceReader, err := readerForRepresentation(
		config.Representation,
		config.RepositoryName,
	)
	if err != nil {
		return nil, err
	}
	options := []repo.Option{
		repo.WithName("swebench-workspace"),
		repo.WithRepository(repository),
		repo.WithFileExtensions([]string{".py"}),
		repo.WithSkipDirs(defaultWorkspaceSkipDirs),
		repo.WithSkipSuffixes([]string{".pyc"}),
		repo.WithReader("python", workspaceReader),
	}
	if materializer != nil {
		options = append(options, repo.WithMaterializer(materializer))
	}
	return &observingSource{Source: repo.New(options...)}, nil
}

func readerForRepresentation(
	representation WorkspaceRepresentation,
	repositoryName string,
) (docreader.Reader, error) {
	switch representation {
	case WorkspaceRepresentationCurrentFixed:
		return textreader.New(), nil
	case WorkspaceRepresentationFixedRaw:
		return textreader.New(docreader.WithCustomChunkingStrategy(
			chunking.NewFixedSizeChunking(
				chunking.WithChunkSize(defaultWorkspaceChunkSize),
				chunking.WithOverlap(defaultWorkspaceChunkOverlap),
				chunking.WithPreserveWhitespace(true),
			),
		)), nil
	case WorkspaceRepresentationASTCode:
		return pythonreader.NewWithConfig(pythonreader.Config{
			IncludeTestFiles:  true,
			IncludeHiddenDirs: true,
			StableRootModule:  repositoryName,
			StableFilePaths:   true,
			FallbackToFile:    true,
			EmbeddingTextMode: pythonreader.EmbeddingTextModeCode,
		}), nil
	case WorkspaceRepresentationASTStructured:
		return pythonreader.NewWithConfig(pythonreader.Config{
			IncludeTestFiles:  true,
			IncludeHiddenDirs: true,
			StableRootModule:  repositoryName,
			StableFilePaths:   true,
			FallbackToFile:    true,
			EmbeddingTextMode: pythonreader.EmbeddingTextModeStructuredCode,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported workspace representation %q", representation)
	}
}

func loadWorkspaceIndex(
	ctx context.Context,
	src *observingSource,
	observation *workspaceObservation,
	config *WorkspaceSearchConfig,
) (*WorkspaceIndex, WorkspaceIndexStats, error) {
	started := time.Now()
	store := inmemory.New(inmemory.WithBM25(true))
	knowledgeOptions := []knowledge.Option{
		knowledge.WithVectorStore(store),
		knowledge.WithSources([]source.Source{src}),
	}
	if config.Embedder != nil {
		knowledgeOptions = append(knowledgeOptions, knowledge.WithEmbedder(config.Embedder))
	}
	kb := knowledge.New(knowledgeOptions...)
	if err := kb.Load(
		ctx,
		knowledge.WithShowProgress(false),
		knowledge.WithShowStats(false),
		knowledge.WithDocConcurrency(config.DocConcurrency),
		knowledge.WithEmbeddingBatchSize(config.BatchSize),
	); err != nil {
		_ = kb.Close()
		return nil, WorkspaceIndexStats{}, fmt.Errorf("index workspace: %w", err)
	}
	count, err := store.Count(ctx)
	if err != nil {
		_ = kb.Close()
		return nil, WorkspaceIndexStats{}, fmt.Errorf("count workspace documents: %w", err)
	}
	stats := buildWorkspaceIndexStats(
		config.Representation,
		observation.eligibleFiles,
		src.documents,
	)
	stats.Documents = count
	stats.DurationMS = time.Since(started).Milliseconds()
	stats.RetrievalMode = searchModeName(config.SearchMode)
	return &WorkspaceIndex{knowledge: kb, config: config}, stats, nil
}

func normalizeWorkspaceSearchConfig(
	config *WorkspaceSearchConfig,
) (*WorkspaceSearchConfig, error) {
	if config == nil {
		config = &WorkspaceSearchConfig{SearchMode: vectorstore.SearchModeKeyword}
	}
	normalized := *config
	representation, err := ParseWorkspaceRepresentation(string(normalized.Representation))
	if err != nil {
		return nil, err
	}
	normalized.Representation = representation
	if normalized.BatchSize <= 0 {
		normalized.BatchSize = 1
	}
	if normalized.DocConcurrency <= 0 {
		normalized.DocConcurrency = 1
	}
	if normalized.MaxResults <= 0 {
		normalized.MaxResults = 4
	}
	if normalized.MaxChars <= 0 {
		normalized.MaxChars = 6000
	}
	return &normalized, nil
}

func searchModeName(mode vectorstore.SearchMode) string {
	switch mode {
	case vectorstore.SearchModeHybrid:
		return "hybrid"
	case vectorstore.SearchModeVector:
		return "vector"
	case vectorstore.SearchModeKeyword:
		return "keyword"
	default:
		return fmt.Sprintf("mode-%d", mode)
	}
}

type observingSource struct {
	source.Source
	documents []*document.Document
}

func (s *observingSource) ReadDocuments(ctx context.Context) ([]*document.Document, error) {
	documents, err := s.Source.ReadDocuments(ctx)
	if err == nil {
		s.documents = documents
	}
	return documents, err
}

func (s *observingSource) RepositoryDescriptor() (string, string, bool) {
	if descriptor, ok := s.Source.(interface {
		RepositoryDescriptor() (string, string, bool)
	}); ok {
		return descriptor.RepositoryDescriptor()
	}
	return "", "", false
}

type workspaceObservation struct {
	eligibleFiles []string
}

type workspaceMaterializer struct {
	snapshotter sweenv.WorkspaceSnapshotter
	instanceID  string
	observation *workspaceObservation
}

func (m *workspaceMaterializer) Materialize(ctx context.Context) (*repo.MaterializedRepository, error) {
	dir, err := os.MkdirTemp("", "tag-swe-workspace-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace snapshot: %w", err)
	}
	if err := m.snapshotter.SnapshotWorkspace(ctx, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	eligibleFiles, err := eligiblePythonFiles(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("scan workspace snapshot: %w", err)
	}
	m.observation.eligibleFiles = eligibleFiles
	return &repo.MaterializedRepository{
		Root:      dir,
		Name:      m.instanceID,
		StableURI: "workspace://" + m.instanceID,
		Cleanup:   func() { _ = os.RemoveAll(dir) },
	}, nil
}

func eligiblePythonFiles(root string) ([]string, error) {
	skipDirs := make(map[string]struct{}, len(defaultWorkspaceSkipDirs))
	for _, dir := range defaultWorkspaceSkipDirs {
		skipDirs[dir] = struct{}{}
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root {
				if _, skip := skipDirs[entry.Name()]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(entry.Name())) != ".py" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relPath))
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	return files, nil
}

func buildWorkspaceIndexStats(
	representation WorkspaceRepresentation,
	eligibleFiles []string,
	documents []*document.Document,
) WorkspaceIndexStats {
	schema := representationSchema(representation)
	stats := WorkspaceIndexStats{
		Representation:        string(representation),
		RepresentationSchema:  schema,
		RepresentationSHA256:  WorkspaceRepresentationSHA256(representation),
		EligibleFiles:         len(eligibleFiles),
		EligibleFileSetSHA256: hashStrings(eligibleFiles),
		FallbackReasons:       make(map[string]int),
		NodeTypes:             make(map[string]int),
	}
	indexedFiles := make(map[string]struct{})
	contentHashes := make(map[string]int)
	documentFingerprints := make([]string, 0, len(documents))
	for _, doc := range documents {
		if doc == nil {
			continue
		}
		path := documentFilePath(doc)
		if path != "" {
			indexedFiles[path] = struct{}{}
		}
		stats.ContentChars += utf8.RuneCountInString(doc.Content)
		stats.EmbeddingTextChars += utf8.RuneCountInString(doc.EmbeddingText)
		if reason, ok := metadataString(doc.Metadata, "trpc_ast_fallback_reason"); ok {
			stats.FallbackDocuments++
			stats.FallbackReasons[reason]++
		}
		if nodeType, ok := metadataString(doc.Metadata, "trpc_ast_type"); ok {
			stats.NodeTypes[nodeType]++
		}
		contentHash := sha256.Sum256([]byte(doc.Content))
		contentHashes[hex.EncodeToString(contentHash[:])]++
		documentFingerprints = append(documentFingerprints, stableDocumentFingerprint(doc))
	}
	indexedFilesList := make([]string, 0, len(indexedFiles))
	for path := range indexedFiles {
		indexedFilesList = append(indexedFilesList, path)
	}
	slices.Sort(indexedFilesList)
	stats.IndexedFiles = len(indexedFilesList)
	stats.IndexedFileSetSHA256 = hashStrings(indexedFilesList)
	stats.DocumentSetSHA256 = hashStrings(documentFingerprints)
	if stats.EligibleFiles > 0 {
		stats.FileCoverage = float64(stats.IndexedFiles) / float64(stats.EligibleFiles)
	}
	eligibleSet := make(map[string]struct{}, len(eligibleFiles))
	for _, path := range eligibleFiles {
		eligibleSet[path] = struct{}{}
	}
	for _, path := range eligibleFiles {
		if _, ok := indexedFiles[path]; !ok {
			stats.MissingFiles = append(stats.MissingFiles, path)
		}
	}
	for path := range indexedFiles {
		if _, ok := eligibleSet[path]; !ok {
			stats.MissingFiles = append(stats.MissingFiles, "unexpected:"+path)
		}
	}
	slices.Sort(stats.MissingFiles)
	for _, count := range contentHashes {
		if count > 1 {
			stats.DuplicateDocuments += count - 1
		}
	}
	if len(documents) > 0 {
		stats.DuplicateDocumentRate = float64(stats.DuplicateDocuments) / float64(len(documents))
	}
	if len(stats.FallbackReasons) == 0 {
		stats.FallbackReasons = nil
	}
	if len(stats.NodeTypes) == 0 {
		stats.NodeTypes = nil
	}
	return stats
}

func stableDocumentFingerprint(doc *document.Document) string {
	parts := []string{
		documentFilePath(doc),
		doc.Name,
		doc.Content,
		doc.EmbeddingText,
	}
	for _, key := range []string{
		"trpc_ast_type",
		"trpc_ast_name",
		"trpc_ast_full_name",
		"trpc_ast_package",
		"trpc_ast_signature",
		"trpc_ast_line_start",
		"trpc_ast_line_end",
		"trpc_ast_fallback_reason",
	} {
		if value, ok := doc.Metadata[key]; ok {
			parts = append(parts, key, fmt.Sprint(value))
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func documentFilePath(doc *document.Document) string {
	if doc == nil {
		return ""
	}
	for _, key := range []string{source.MetaFilePath, "trpc_ast_file_path"} {
		if path, ok := metadataString(doc.Metadata, key); ok {
			return filepath.ToSlash(filepath.Clean(path))
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) (string, bool) {
	if metadata == nil {
		return "", false
	}
	value, ok := metadata[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func hashStrings(values []string) string {
	copied := append([]string(nil), values...)
	slices.Sort(copied)
	hasher := sha256.New()
	for _, value := range copied {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func representationSchema(representation WorkspaceRepresentation) string {
	switch representation {
	case WorkspaceRepresentationCurrentFixed:
		return "workspace-representation/v1;reader=text;chunk=fixed;size=1024;overlap=128;whitespace=trim-lines"
	case WorkspaceRepresentationFixedRaw:
		return "workspace-representation/v1;reader=text;chunk=fixed;size=1024;overlap=128;whitespace=preserve"
	case WorkspaceRepresentationASTCode:
		return "workspace-representation/v1;reader=python-ast;tests=include;hidden=include;paths=stable;fallback=file;embedding=code"
	case WorkspaceRepresentationASTStructured:
		return "workspace-representation/v1;reader=python-ast;tests=include;hidden=include;paths=stable;fallback=file;embedding=structured-code"
	default:
		return "workspace-representation/v1;unknown=" + string(representation)
	}
}
