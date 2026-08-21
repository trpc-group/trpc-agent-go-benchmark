//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
	frameworktool "trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	defaultEngineName    = "trpc-agent-go-workspace-search"
	defaultEngineVersion = "retrieval-replay-v1"
	engineConfigKind     = "trpc-agent-go-swebench-retrieval-engine-config"
)

type engineConfig struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
}

// OfflineEngine is the benchmark-local concrete replay engine. Keyword mode
// rebuilds the same WorkspaceIndex implementation used by the Native runner.
// Dense modes fail closed until their complete portable embedding cache is
// wired into the same interface; they never fall back to an endpoint.
type OfflineEngine struct{}

// NewOfflineEngine returns the concrete engine used by the CLI.
func NewOfflineEngine() *OfflineEngine { return &OfflineEngine{} }

// DefaultEngineDescriptor is a stable semantic identity over the replay
// contract and every workspace representation schema used by the indexer.
func DefaultEngineDescriptor() EngineDescriptor {
	hasher := sha256.New()
	for _, value := range []string{
		"trpc-agent-go-swebench-retrieval-engine-v1",
		"build=tagagent.NewWorkspaceIndexFromDirectory",
		"search=WorkspaceIndex.Tool.Call;keyword=bm25;dedup=false;outcome=status-errorhash-ranked-docs",
		string(tagagent.WorkspaceRepresentationCurrentFixed) + "=" +
			tagagent.WorkspaceRepresentationSchema(tagagent.WorkspaceRepresentationCurrentFixed),
		string(tagagent.WorkspaceRepresentationFixedRaw) + "=" +
			tagagent.WorkspaceRepresentationSchema(tagagent.WorkspaceRepresentationFixedRaw),
		string(tagagent.WorkspaceRepresentationASTCode) + "=" +
			tagagent.WorkspaceRepresentationSchema(tagagent.WorkspaceRepresentationASTCode),
		string(tagagent.WorkspaceRepresentationASTStructured) + "=" +
			tagagent.WorkspaceRepresentationSchema(tagagent.WorkspaceRepresentationASTStructured),
	} {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return EngineDescriptor{
		Name: defaultEngineName, Version: defaultEngineVersion,
		ImplementationSHA256: hex.EncodeToString(hasher.Sum(nil)), OfflineOnly: true,
	}
}

func defaultEngineConfigJSON() ([]byte, error) {
	return json.MarshalIndent(engineConfig{SchemaVersion: 1, Kind: engineConfigKind}, "", "  ")
}

// Descriptor implements Engine.
func (*OfflineEngine) Descriptor() EngineDescriptor { return DefaultEngineDescriptor() }

// Build implements Engine using only verified portable artifacts.
func (*OfflineEngine) Build(ctx context.Context, input BuildInput) (Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configData, err := input.EngineConfig.ReadAll(1 << 20)
	if err != nil {
		return nil, fmt.Errorf("read engine config: %w", err)
	}
	var config engineConfig
	if err := decodeStrictJSON(configData, &config); err != nil {
		return nil, fmt.Errorf("decode engine config: %w", err)
	}
	if config.SchemaVersion != 1 || config.Kind != engineConfigKind {
		return nil, fmt.Errorf("unsupported engine config schema=%d kind=%q", config.SchemaVersion, config.Kind)
	}
	if input.Provenance.RetrievalMode != "keyword" {
		return nil, fmt.Errorf(
			"offline %s replay is unavailable: a complete portable all-hit embedding cache adapter is not linked",
			input.Provenance.RetrievalMode,
		)
	}
	if input.EmbeddingCache != nil || input.Provenance.Embedding != nil {
		return nil, errors.New("keyword replay must not receive embedding artifacts")
	}
	directory, err := os.MkdirTemp("", "tag-retrieval-replay-corpus-*")
	if err != nil {
		return nil, fmt.Errorf("create corpus workspace: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(directory)
		}
	}()
	if err := materializeCorpus(input.Corpus, input.Provenance.CorpusFormat, directory); err != nil {
		return nil, fmt.Errorf("materialize frozen corpus: %w", err)
	}
	representation, err := tagagent.ParseWorkspaceRepresentation(input.Provenance.WorkspaceRepresentation)
	if err != nil {
		return nil, err
	}
	if tagagent.WorkspaceRepresentationSchema(representation) != input.Provenance.WorkspaceRepresentationSchema ||
		tagagent.WorkspaceRepresentationSHA256(representation) != input.Provenance.WorkspaceRepresentationSHA256 {
		return nil, errors.New("runtime workspace representation identity does not match frozen provenance")
	}
	workspace, stats, err := tagagent.NewWorkspaceIndexFromDirectory(
		ctx,
		directory,
		input.Repository,
		&tagagent.WorkspaceSearchConfig{
			SearchMode: vectorstore.SearchModeKeyword, MaxResults: input.Provenance.MaxResults,
			Representation: representation, RepositoryName: input.Repository,
			BatchSize: 1, DocConcurrency: 1,
		},
	)
	if err != nil {
		return nil, err
	}
	identity := indexIdentityFromStats(input.Provenance, stats)
	if err := validateIndexIdentity(identity, CaseSpec{Retrieval: input.Provenance}); err != nil {
		_ = workspace.Close()
		return nil, fmt.Errorf("rebuilt corpus provenance: %w", err)
	}
	searchTool, ok := workspace.Tool().(frameworktool.CallableTool)
	if !ok {
		_ = workspace.Close()
		return nil, errors.New("rebuilt workspace code_search tool is not callable")
	}
	keep = true
	return &offlineIndex{
		workspace: workspace, searchTool: searchTool,
		identity: identity, directory: directory,
	}, nil
}

type offlineIndex struct {
	workspace  *tagagent.WorkspaceIndex
	searchTool frameworktool.CallableTool
	identity   IndexIdentity
	directory  string
}

func (i *offlineIndex) Identity() IndexIdentity { return i.identity }

func (i *offlineIndex) Search(ctx context.Context, request SearchRequest) ([]Document, error) {
	canonical, err := canonicalJSONObject(request.Arguments)
	if err != nil {
		return nil, err
	}
	if digestBytes(canonical) != request.ArgumentsSHA256 {
		return nil, errors.New("search arguments do not match arguments_sha256")
	}
	var arguments struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(canonical, &arguments); err != nil || arguments.Query != request.Query {
		return nil, errors.New("search arguments do not encode the recorded query")
	}
	result, err := i.searchTool.Call(ctx, canonical)
	if err != nil {
		return nil, err
	}
	response, ok := result.(*knowledgetool.KnowledgeSearchResponse)
	if !ok || response == nil {
		return nil, fmt.Errorf(
			"code_search returned %T, want *tool.KnowledgeSearchResponse",
			result,
		)
	}
	return replayDocuments(response), nil
}

func (i *offlineIndex) Close() error {
	if i == nil {
		return nil
	}
	var closeErr error
	if i.workspace != nil {
		closeErr = i.workspace.Close()
	}
	removeErr := os.RemoveAll(i.directory)
	i.workspace = nil
	i.searchTool = nil
	i.directory = ""
	return errors.Join(closeErr, removeErr)
}

func indexIdentityFromStats(
	provenance RetrievalProvenance,
	stats tagagent.WorkspaceIndexStats,
) IndexIdentity {
	return IndexIdentity{
		CorpusSHA256:                  provenance.CorpusSHA256,
		EligibleFileSetSHA256:         stats.EligibleFileSetSHA256,
		EligibleContentSHA256:         stats.EligibleContentSHA256,
		DocumentSetSHA256:             stats.DocumentSetSHA256,
		WorkspaceRepresentation:       stats.Representation,
		WorkspaceRepresentationSchema: stats.RepresentationSchema,
		WorkspaceRepresentationSHA256: stats.RepresentationSHA256,
		ParserDependency:              stats.ParserDependency,
		ParserRuntimeSHA256:           stats.ParserRuntimeSHA256,
		RetrievalMode:                 stats.RetrievalMode, MaxResults: provenance.MaxResults,
		InvocationDedup: provenance.InvocationDedup,
	}
}

func replayDocuments(result *knowledgetool.KnowledgeSearchResponse) []Document {
	if result == nil {
		return []Document{}
	}
	documents := make([]Document, 0, len(result.Documents))
	for _, item := range result.Documents {
		if item == nil {
			continue
		}
		path := replayMetadataMapString(item.Metadata, "trpc_ast_file_path", "trpc_agent_go_file_path")
		lines := replayMetadataMapLines(item.Metadata)
		symbol := replayMetadataMapString(item.Metadata, "trpc_ast_full_name")
		if strings.EqualFold(replayMetadataMapString(item.Metadata, "trpc_ast_type"), "file") || symbol == path {
			symbol = ""
		}
		documents = append(documents, Document{
			ID: item.ID, SourcePath: path,
			ContentSHA256:  digestBytes([]byte(item.Text)),
			MetadataSHA256: RetrievalMetadataSHA256(path, lines, symbol),
			Score:          item.Score,
		})
	}
	return documents
}

func replayMetadataMapString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := metadata[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func replayMetadataMapLines(metadata map[string]any) string {
	start, startOK := replayMetadataMapLine(metadata, "trpc_ast_line_start")
	end, endOK := replayMetadataMapLine(metadata, "trpc_ast_line_end")
	if !startOK || !endOK || start > end {
		return ""
	}
	if start == end {
		return strconv.FormatInt(start, 10)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func replayMetadataMapLine(metadata map[string]any, key string) (int64, bool) {
	value, ok := metadata[key]
	if !ok {
		return 0, false
	}
	line, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	return line, err == nil && line > 0
}
