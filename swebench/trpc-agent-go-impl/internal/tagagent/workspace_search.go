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
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	docreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader"
	pythonreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/python"
	textreader "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/text"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	defaultWorkspaceChunkSize    = 1024
	defaultWorkspaceChunkOverlap = 128
	pythonReaderModuleVersion    = "v0.0.0-20260728070417-4237accb70cb"
	// CodeSearchInvocationDedup records that the public generic knowledge tool
	// does not suppress documents returned by earlier calls in one invocation.
	CodeSearchInvocationDedup = "disabled"
	// CodeSearchProviderToolOrder is the order emitted by the pinned public
	// OpenAI adapter after it sorts tool names.
	CodeSearchProviderToolOrder = "bash,code_search"
)

var defaultWorkspaceSkipDirs = []string{
	".git", ".tox", ".venv", "venv", "node_modules", "build", "dist", "__pycache__",
}

// WorkspaceRepresentation identifies the document representation used to
// build one workspace index.
type WorkspaceRepresentation string

const (
	// WorkspaceRepresentationCurrentFixed preserves the historical
	// line-normalized fixed-size text chunks.
	WorkspaceRepresentationCurrentFixed WorkspaceRepresentation = "current-fixed"
	// WorkspaceRepresentationFixedRaw preserves Python indentation in fixed
	// chunks.
	WorkspaceRepresentationFixedRaw WorkspaceRepresentation = "fixed-raw"
	// WorkspaceRepresentationASTCode embeds the source text of AST nodes.
	WorkspaceRepresentationASTCode WorkspaceRepresentation = "ast-code"
	// WorkspaceRepresentationASTStructured builds benchmark-local structured
	// embedding text from public Python-reader AST documents while returning
	// source text to the model.
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
	if representation == "" {
		representation = WorkspaceRepresentationCurrentFixed
	}
	return representationSchema(representation)
}

// WorkspaceRepresentationSHA256 returns the canonical index schema hash.
func WorkspaceRepresentationSHA256(representation WorkspaceRepresentation) string {
	return hashStrings([]string{WorkspaceRepresentationSchema(representation)})
}

// WorkspaceIndexStats captures per-case indexing cost, representation
// provenance, coverage, and stability signals separately from model usage.
type WorkspaceIndexStats struct {
	Representation        string         `json:"representation"`
	RepresentationSchema  string         `json:"representation_schema"`
	RepresentationSHA256  string         `json:"representation_sha256"`
	ParserDependency      string         `json:"parser_dependency,omitempty"`
	ParserRuntime         string         `json:"parser_runtime,omitempty"`
	ParserRuntimeSHA256   string         `json:"parser_runtime_sha256,omitempty"`
	Documents             int            `json:"documents"`
	EligibleFiles         int            `json:"eligible_files"`
	IndexedFiles          int            `json:"indexed_files"`
	FileCoverage          float64        `json:"file_coverage"`
	EligibleFileSetSHA256 string         `json:"eligible_file_set_sha256"`
	EligibleContentSHA256 string         `json:"eligible_content_sha256"`
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
	RetrievalMode         string         `json:"retrieval_mode"`
	InvocationDedup       string         `json:"invocation_dedup"`
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

// WorkspaceContextResult is a bounded, model-facing preload rendering.
type WorkspaceContextResult struct {
	Text      string
	Documents int
	Chars     int
}

// WorkspaceIndex owns one loaded workspace knowledge base.
type WorkspaceIndex struct {
	knowledge *knowledge.BuiltinKnowledge
	config    WorkspaceSearchConfig
}

const workspaceCodeSearchDescription = "Search the static task-start workspace index and return relevant source excerpts with paths, line ranges, and symbols. Results may reflect code before your edits."

// Search retrieves raw documents for offline replay and diagnostics.
func (i *WorkspaceIndex) Search(ctx context.Context, query string, maxResults int) (*knowledge.SearchResult, error) {
	if i == nil || i.knowledge == nil {
		return nil, fmt.Errorf("workspace index is not initialized")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("workspace search query cannot be empty")
	}
	if maxResults <= 0 {
		maxResults = i.config.MaxResults
	}
	return i.knowledge.Search(ctx, &knowledge.SearchRequest{
		Query:      query,
		MaxResults: maxResults,
		MinScore:   0,
		SearchMode: int(i.config.SearchMode),
	})
}

// BuildContext retrieves and renders bounded model-facing context. The
// rendering intentionally mirrors the historical benchmark helper without
// requiring a framework-only helper API.
func (i *WorkspaceIndex) BuildContext(ctx context.Context, query string) (*WorkspaceContextResult, error) {
	result, err := i.Search(ctx, query, i.config.MaxResults)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	chars := 0
	count := 0
	for _, item := range result.Documents {
		if item == nil || item.Document == nil || strings.TrimSpace(item.Document.Content) == "" {
			continue
		}
		header := workspaceContextHeader(count+1, item)
		remaining := i.config.MaxChars - chars
		if remaining <= utf8.RuneCountInString(header) {
			break
		}
		b.WriteString(header)
		chars += utf8.RuneCountInString(header)
		remaining = i.config.MaxChars - chars
		content := truncateWorkspaceRunes(strings.TrimSpace(item.Document.Content), remaining)
		b.WriteString(content)
		chars += utf8.RuneCountInString(content)
		if chars < i.config.MaxChars {
			b.WriteByte('\n')
			chars++
		}
		count++
		if chars >= i.config.MaxChars {
			break
		}
	}
	text := strings.TrimSpace(b.String())
	return &WorkspaceContextResult{Text: text, Documents: count, Chars: utf8.RuneCountInString(text)}, nil
}

// Tool returns a query-only code_search tool backed by this index. A wrapper
// forces the frozen retrieval mode because the public generic tool otherwise
// uses the framework default.
func (i *WorkspaceIndex) Tool() tool.Tool {
	return knowledgetool.NewKnowledgeSearchTool(
		workspaceModeKnowledge{Knowledge: i.knowledge, mode: i.config.SearchMode},
		knowledgetool.WithToolName("code_search"),
		knowledgetool.WithToolDescription(workspaceCodeSearchDescription),
		knowledgetool.WithMaxResults(i.config.MaxResults),
		knowledgetool.WithMinScore(0),
		knowledgetool.WithExcludeMetadataKeys(
			"file_mode", "file_size", "modified_at", "repo_path", "source", "source_name",
			"trpc_agent_go_file_mode", "trpc_agent_go_file_size", "trpc_agent_go_modified_at",
			"trpc_agent_go_repo_path", "trpc_agent_go_source", "trpc_agent_go_source_name",
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

// NewWorkspaceIndexFromEnvironment snapshots and indexes one live environment.
func NewWorkspaceIndexFromEnvironment(
	ctx context.Context,
	environment sweenv.Environment,
	instanceID string,
	config *WorkspaceSearchConfig,
) (*WorkspaceIndex, WorkspaceIndexStats, error) {
	snapshotter, ok := environment.(sweenv.WorkspaceSnapshotter)
	if !ok {
		return nil, WorkspaceIndexStats{}, fmt.Errorf("environment does not support workspace snapshots")
	}
	directory, err := os.MkdirTemp("", "tag-swe-workspace-*")
	if err != nil {
		return nil, WorkspaceIndexStats{}, fmt.Errorf("create workspace snapshot: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := snapshotter.SnapshotWorkspace(ctx, directory); err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	repositoryName := strings.TrimSpace(instanceID)
	if config != nil && strings.TrimSpace(config.RepositoryName) != "" {
		repositoryName = strings.TrimSpace(config.RepositoryName)
	}
	return NewWorkspaceIndexFromDirectory(ctx, directory, repositoryName, config)
}

// NewWorkspaceIndexFromDirectory indexes an already materialized checkout.
// Retrieval replay uses this entry point so every representation sees the
// same frozen bytes.
func NewWorkspaceIndexFromDirectory(
	ctx context.Context,
	directory string,
	repositoryName string,
	config *WorkspaceSearchConfig,
) (*WorkspaceIndex, WorkspaceIndexStats, error) {
	started := time.Now()
	normalized, err := normalizeWorkspaceSearchConfig(config)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	if strings.TrimSpace(repositoryName) != "" {
		normalized.RepositoryName = strings.TrimSpace(repositoryName)
	}
	if normalized.RepositoryName == "" {
		normalized.RepositoryName = "workspace"
	}
	files, err := scanEligiblePythonFiles(ctx, directory)
	if err != nil {
		return nil, WorkspaceIndexStats{}, fmt.Errorf("scan workspace files: %w", err)
	}
	documents, err := buildWorkspaceDocuments(ctx, files, normalized)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	index, stats, err := loadWorkspaceIndex(ctx, files, documents, normalized)
	if err != nil {
		return nil, WorkspaceIndexStats{}, err
	}
	stats.DurationMS = time.Since(started).Milliseconds()
	return index, stats, nil
}

type eligibleWorkspaceFile struct {
	Path    string
	Content []byte
}

func scanEligiblePythonFiles(ctx context.Context, root string) ([]eligibleWorkspaceFile, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("workspace directory must be absolute")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("workspace directory must be a real directory")
	}
	skipDirs := make(map[string]struct{}, len(defaultWorkspaceSkipDirs))
	for _, dir := range defaultWorkspaceSkipDirs {
		skipDirs[dir] = struct{}{}
	}
	var files []eligibleWorkspaceFile
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if _, skip := skipDirs[entry.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Type()&os.ModeType != 0 {
			return nil
		}
		if strings.ToLower(filepath.Ext(entry.Name())) != ".py" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(filepath.Clean(relPath))
		if relPath == ".." || strings.HasPrefix(relPath, "../") {
			return fmt.Errorf("workspace path escapes snapshot: %s", relPath)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, eligibleWorkspaceFile{Path: relPath, Content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b eligibleWorkspaceFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

func buildWorkspaceDocuments(
	ctx context.Context,
	files []eligibleWorkspaceFile,
	config WorkspaceSearchConfig,
) ([]*document.Document, error) {
	return buildWorkspaceDocumentsWithReader(ctx, files, config, nil)
}

func buildWorkspaceDocumentsWithReader(
	ctx context.Context,
	files []eligibleWorkspaceFile,
	config WorkspaceSearchConfig,
	reader docreader.Reader,
) ([]*document.Document, error) {
	if reader == nil {
		switch config.Representation {
		case WorkspaceRepresentationCurrentFixed:
			reader = textreader.New(
				docreader.WithChunkSize(defaultWorkspaceChunkSize),
				docreader.WithChunkOverlap(defaultWorkspaceChunkOverlap),
			)
		case WorkspaceRepresentationASTCode, WorkspaceRepresentationASTStructured:
			reader = pythonreader.New()
		}
	}
	var documents []*document.Document
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("build workspace documents: %w", err)
		}
		var fileDocuments []*document.Document
		var err error
		switch config.Representation {
		case WorkspaceRepresentationFixedRaw:
			fileDocuments = rawFixedDocuments(file.Path, string(file.Content))
		case WorkspaceRepresentationCurrentFixed,
			WorkspaceRepresentationASTCode,
			WorkspaceRepresentationASTStructured:
			read := func() ([]*document.Document, error) {
				return reader.ReadFromReader(file.Path, strings.NewReader(string(file.Content)))
			}
			if config.Representation == WorkspaceRepresentationCurrentFixed {
				fileDocuments, err = read()
			} else {
				fileDocuments, err = readWorkspaceDocumentsWithContext(ctx, read)
			}
		default:
			err = fmt.Errorf("unsupported workspace representation %q", config.Representation)
		}
		if err != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("read workspace file %s: %w", file.Path, err)
		}
		if err != nil && (config.Representation == WorkspaceRepresentationASTCode ||
			config.Representation == WorkspaceRepresentationASTStructured) {
			fileDocuments = rawFixedDocuments(file.Path, string(file.Content))
			for _, doc := range fileDocuments {
				ensureDocumentMetadata(doc)["trpc_ast_type"] = "file"
				ensureDocumentMetadata(doc)["trpc_ast_fallback_reason"] = "parse_error"
			}
			err = nil
		}
		if err != nil {
			return nil, fmt.Errorf("read workspace file %s: %w", file.Path, err)
		}
		for index, doc := range fileDocuments {
			if doc == nil || doc.Content == "" {
				continue
			}
			metadata := ensureDocumentMetadata(doc)
			metadata[source.MetaSource] = source.TypeRepo
			metadata[source.MetaSourceName] = "swebench-workspace"
			metadata[source.MetaFilePath] = file.Path
			metadata[source.MetaFileName] = filepath.Base(file.Path)
			metadata[source.MetaFileExt] = ".py"
			metadata[source.MetaURI] = workspaceURI(config.RepositoryName, file.Path)
			metadata[source.MetaRepoName] = config.RepositoryName
			metadata["trpc_ast_repo_name"] = config.RepositoryName
			metadata["trpc_ast_file_path"] = file.Path
			metadata[source.MetaChunkIndex] = index
			if (config.Representation == WorkspaceRepresentationASTCode ||
				config.Representation == WorkspaceRepresentationASTStructured) &&
				fmt.Sprint(metadata["trpc_ast_type"]) == "file" {
				if _, ok := metadata["trpc_ast_fallback_reason"]; !ok {
					metadata["trpc_ast_fallback_reason"] = "no_ast_nodes"
				}
			}
			if config.Representation == WorkspaceRepresentationASTCode {
				doc.EmbeddingText = doc.Content
			} else if config.Representation == WorkspaceRepresentationASTStructured {
				doc.EmbeddingText = structuredWorkspaceEmbeddingText(doc, file.Path)
			}
			doc.ID = stableWorkspaceDocumentID(config.Representation, file.Path, index, doc)
			documents = append(documents, doc)
		}
	}
	return documents, nil
}

type workspaceDocumentReadResult struct {
	documents []*document.Document
	err       error
}

func readWorkspaceDocumentsWithContext(
	ctx context.Context,
	read func() ([]*document.Document, error),
) ([]*document.Document, error) {
	result := make(chan workspaceDocumentReadResult, 1)
	go func() {
		documents, err := read()
		result <- workspaceDocumentReadResult{documents: documents, err: err}
	}()
	select {
	case completed := <-result:
		return completed.documents, completed.err
	case <-ctx.Done():
		return nil, fmt.Errorf("python reader exceeded case context: %w", ctx.Err())
	}
}

type structuredWorkspaceEmbedding struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	Package   string `json:"package,omitempty"`
	FilePath  string `json:"file_path"`
	Signature string `json:"signature,omitempty"`
	Comment   string `json:"comment,omitempty"`
	Code      string `json:"code"`
}

func structuredWorkspaceEmbeddingText(doc *document.Document, fallbackPath string) string {
	path := filepath.ToSlash(filepath.Clean(fallbackPath))
	nodeType, _ := metadataString(doc.Metadata, "trpc_ast_type")
	if nodeType == "" {
		nodeType = "file"
	}
	name, _ := metadataString(doc.Metadata, "trpc_ast_name")
	if name == "" {
		name = doc.Name
	}
	if name == "" {
		name = path
	}
	fullName, _ := metadataString(doc.Metadata, "trpc_ast_full_name")
	if fullName == "" {
		fullName = name
	}
	if metadataPath, ok := metadataString(doc.Metadata, "trpc_ast_file_path"); ok {
		path = filepath.ToSlash(filepath.Clean(metadataPath))
	}
	packageName, _ := metadataString(doc.Metadata, "trpc_ast_package")
	signature, _ := metadataString(doc.Metadata, "trpc_ast_signature")
	comment, _ := metadataString(doc.Metadata, "trpc_ast_comment")
	encoded, _ := json.Marshal(structuredWorkspaceEmbedding{
		Type:      nodeType,
		Name:      name,
		FullName:  fullName,
		Package:   packageName,
		FilePath:  path,
		Signature: signature,
		Comment:   strings.TrimSpace(comment),
		Code:      doc.Content,
	})
	return string(encoded)
}

func rawFixedDocuments(path, content string) []*document.Document {
	runes := []rune(content)
	if len(runes) == 0 {
		return nil
	}
	var documents []*document.Document
	for start, index := 0, 0; start < len(runes); index++ {
		end := min(start+defaultWorkspaceChunkSize, len(runes))
		chunkStart := start
		if index > 0 {
			chunkStart = max(0, start-defaultWorkspaceChunkOverlap)
		}
		documents = append(documents, &document.Document{
			Name:    path,
			Content: string(runes[chunkStart:end]),
			Metadata: map[string]any{
				source.MetaChunkIndex: index,
				source.MetaChunkSize:  end - chunkStart,
			},
		})
		start = end
	}
	return documents
}

func loadWorkspaceIndex(
	ctx context.Context,
	files []eligibleWorkspaceFile,
	documents []*document.Document,
	config WorkspaceSearchConfig,
) (*WorkspaceIndex, WorkspaceIndexStats, error) {
	started := time.Now()
	store := newWorkspaceVectorStore()
	src := &workspaceDocumentSource{documents: documents, repositoryName: config.RepositoryName}
	options := []knowledge.Option{
		knowledge.WithVectorStore(store),
		knowledge.WithSources([]source.Source{src}),
	}
	if config.Embedder != nil {
		options = append(options, knowledge.WithEmbedder(config.Embedder))
	}
	kb := knowledge.New(options...)
	if err := kb.Load(
		ctx,
		knowledge.WithShowProgress(false),
		knowledge.WithShowStats(false),
		knowledge.WithDocConcurrency(config.DocConcurrency),
	); err != nil {
		_ = kb.Close()
		return nil, WorkspaceIndexStats{}, fmt.Errorf("index workspace: %w", err)
	}
	count, err := store.Count(ctx)
	if err != nil {
		_ = kb.Close()
		return nil, WorkspaceIndexStats{}, fmt.Errorf("count workspace documents: %w", err)
	}
	stats := buildWorkspaceIndexStats(config.Representation, files, documents)
	if config.Representation == WorkspaceRepresentationASTCode ||
		config.Representation == WorkspaceRepresentationASTStructured {
		if err := recordPythonParserIdentity(ctx, &stats); err != nil {
			_ = kb.Close()
			return nil, WorkspaceIndexStats{}, err
		}
	}
	stats.Documents = count
	stats.DurationMS = time.Since(started).Milliseconds()
	stats.RetrievalMode = searchModeName(config.SearchMode)
	stats.InvocationDedup = CodeSearchInvocationDedup
	return &WorkspaceIndex{knowledge: kb, config: config}, stats, nil
}

func recordPythonParserIdentity(ctx context.Context, stats *WorkspaceIndexStats) error {
	if stats == nil {
		return fmt.Errorf("workspace index stats are required")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(commandCtx, "python3", "--version").CombinedOutput() //nolint:gosec
	if err != nil {
		return fmt.Errorf(
			"identify public Python-reader runtime: %w: %s",
			err,
			strings.TrimSpace(string(out)),
		)
	}
	runtimeIdentity := strings.TrimSpace(string(out))
	if runtimeIdentity == "" {
		return fmt.Errorf("identify public Python-reader runtime: empty python3 --version output")
	}
	stats.ParserDependency = "trpc.group/trpc-go/trpc-agent-go/knowledge/document/reader/python@" +
		pythonReaderModuleVersion
	stats.ParserRuntime = runtimeIdentity
	stats.ParserRuntimeSHA256 = hashStrings([]string{runtimeIdentity})
	return nil
}

func normalizeWorkspaceSearchConfig(config *WorkspaceSearchConfig) (WorkspaceSearchConfig, error) {
	normalized := WorkspaceSearchConfig{SearchMode: vectorstore.SearchModeKeyword}
	if config != nil {
		normalized = *config
	}
	representation, err := ParseWorkspaceRepresentation(string(normalized.Representation))
	if err != nil {
		return WorkspaceSearchConfig{}, err
	}
	normalized.Representation = representation
	if normalized.BatchSize <= 0 {
		normalized.BatchSize = 1
	}
	if normalized.DocConcurrency <= 0 {
		normalized.DocConcurrency = 1
	}
	if normalized.MaxResults <= 0 {
		normalized.MaxResults = 6
	}
	if normalized.MaxChars <= 0 {
		normalized.MaxChars = 6000
	}
	if normalized.Embedder == nil && normalized.SearchMode != vectorstore.SearchModeKeyword {
		return WorkspaceSearchConfig{}, fmt.Errorf("workspace %s retrieval requires an embedder", searchModeName(normalized.SearchMode))
	}
	return normalized, nil
}

type workspaceDocumentSource struct {
	documents      []*document.Document
	repositoryName string
}

func (s *workspaceDocumentSource) ReadDocuments(context.Context) ([]*document.Document, error) {
	return append([]*document.Document(nil), s.documents...), nil
}
func (s *workspaceDocumentSource) Name() string { return "swebench-workspace" }
func (s *workspaceDocumentSource) Type() string { return source.TypeRepo }
func (s *workspaceDocumentSource) GetMetadata() map[string]any {
	return map[string]any{source.MetaRepoName: s.repositoryName}
}

type workspaceModeKnowledge struct {
	knowledge.Knowledge
	mode vectorstore.SearchMode
}

func (k workspaceModeKnowledge) Search(ctx context.Context, request *knowledge.SearchRequest) (*knowledge.SearchResult, error) {
	if request == nil {
		return nil, fmt.Errorf("workspace search request cannot be nil")
	}
	copy := *request
	copy.SearchMode = int(k.mode)
	return k.Knowledge.Search(ctx, &copy)
}

func buildWorkspaceIndexStats(
	representation WorkspaceRepresentation,
	files []eligibleWorkspaceFile,
	documents []*document.Document,
) WorkspaceIndexStats {
	eligiblePaths := make([]string, 0, len(files))
	eligibleContent := make([]string, 0, len(files))
	for _, file := range files {
		eligiblePaths = append(eligiblePaths, file.Path)
		eligibleContent = append(eligibleContent, file.Path+"\x00"+string(file.Content))
	}
	stats := WorkspaceIndexStats{
		Representation:        string(representation),
		RepresentationSchema:  representationSchema(representation),
		RepresentationSHA256:  WorkspaceRepresentationSHA256(representation),
		EligibleFiles:         len(files),
		EligibleFileSetSHA256: hashStrings(eligiblePaths),
		EligibleContentSHA256: hashStrings(eligibleContent),
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
	indexedPaths := make([]string, 0, len(indexedFiles))
	for path := range indexedFiles {
		indexedPaths = append(indexedPaths, path)
	}
	slices.Sort(indexedPaths)
	stats.IndexedFiles = len(indexedPaths)
	stats.IndexedFileSetSHA256 = hashStrings(indexedPaths)
	stats.DocumentSetSHA256 = hashStrings(documentFingerprints)
	eligibleSet := make(map[string]struct{}, len(eligiblePaths))
	for _, path := range eligiblePaths {
		eligibleSet[path] = struct{}{}
		if _, ok := indexedFiles[path]; !ok {
			stats.MissingFiles = append(stats.MissingFiles, path)
		}
	}
	if len(eligiblePaths) > 0 {
		stats.FileCoverage = float64(len(eligiblePaths)-len(stats.MissingFiles)) / float64(len(eligiblePaths))
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

func workspaceContextHeader(index int, item *knowledge.Result) string {
	parts := []string{fmt.Sprintf("result=%d", index), fmt.Sprintf("score=%.4f", item.Score)}
	for _, key := range []string{source.MetaFilePath, "trpc_ast_full_name", "trpc_ast_signature"} {
		if value, ok := metadataString(item.Document.Metadata, key); ok {
			parts = append(parts, key+"="+value)
		}
	}
	return "\n--- " + strings.Join(parts, " ") + " ---\n"
}

func truncateWorkspaceRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	if maximum == 1 {
		return string(runes[:1])
	}
	return string(runes[:maximum-1]) + "…"
}

func ensureDocumentMetadata(doc *document.Document) map[string]any {
	if doc.Metadata == nil {
		doc.Metadata = make(map[string]any)
	}
	return doc.Metadata
}

func stableWorkspaceDocumentID(
	representation WorkspaceRepresentation,
	path string,
	index int,
	doc *document.Document,
) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%d\x00%s\x00%s",
		representation,
		path,
		index,
		doc.Content,
		doc.EmbeddingText,
	)))
	return hex.EncodeToString(sum[:])
}

func workspaceURI(repositoryName, path string) string {
	return (&url.URL{Scheme: "workspace", Host: repositoryName, Path: "/" + filepath.ToSlash(path)}).String()
}

func stableDocumentFingerprint(doc *document.Document) string {
	parts := []string{documentFilePath(doc), doc.Name, doc.Content, doc.EmbeddingText}
	for _, key := range []string{
		"trpc_ast_type", "trpc_ast_name", "trpc_ast_full_name", "trpc_ast_package",
		"trpc_ast_signature", "trpc_ast_line_start", "trpc_ast_line_end", "trpc_ast_fallback_reason",
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
	value, ok := metadata[key]
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	return text, text != ""
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

func representationSchema(representation WorkspaceRepresentation) string {
	switch representation {
	case WorkspaceRepresentationCurrentFixed:
		return "workspace-representation/v3;reader=text;chunk=fixed;size=1024;overlap=128;whitespace=normalize-lines;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled"
	case WorkspaceRepresentationFixedRaw:
		return "workspace-representation/v3;reader=benchmark-local;chunk=fixed;size=1024;overlap=128;whitespace=preserve;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled"
	case WorkspaceRepresentationASTCode:
		return "workspace-representation/v3;reader=public-python-ast@" + pythonReaderModuleVersion + ";tests=include;hidden=include;paths=stable;fallback=fixed-raw;embedding=code;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled"
	case WorkspaceRepresentationASTStructured:
		return "workspace-representation/v3;reader=public-python-ast@" + pythonReaderModuleVersion + ";tests=include;hidden=include;paths=stable;fallback=fixed-raw;embedding=structured-code;keyword=bm25-k1-1.2-b-0.75;hybrid=rrf-k-60;tie=score-desc-id-asc;invocation-dedup=disabled"
	default:
		return "workspace-representation/v3;unknown=" + string(representation)
	}
}
