//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/chunking"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
)

const (
	parentManifestSchema = "contextual-retrieval/parents/v1"
	chunkManifestSchema  = "contextual-retrieval/chunks/v1"
	contextCacheSchema   = "contextual-retrieval/context-cache/v1"

	indexVariantLegacy     = "legacy"
	indexVariantBaseline   = "baseline"
	indexVariantContextual = "contextual"

	experimentSourceName = "MultiHop-RAG Contextual Retrieval"
	experimentSourceType = "contextual_manifest"
	experimentMetaPrefix = "contextual_retrieval_"

	contextualEmbeddingPrefix    = "Context:\n"
	contextualEmbeddingDelimiter = "\n\n--- Original chunk ---\n"
)

type parentManifest struct {
	SchemaVersion  string         `json:"schema_version"`
	ParentsCount   int            `json:"parents_count"`
	Parents        []parentRecord `json:"parents"`
	ArtifactDigest string         `json:"artifact_digest"`
}

type parentRecord struct {
	ParentDocumentID string         `json:"parent_document_id"`
	CorpusIndex      int            `json:"corpus_index"`
	FileName         string         `json:"file_name"`
	Content          string         `json:"content"`
	ContentHash      string         `json:"content_hash"`
	Metadata         map[string]any `json:"metadata"`
}

type chunkManifest struct {
	SchemaVersion        string        `json:"schema_version"`
	Dataset              string        `json:"dataset"`
	ParentManifestDigest string        `json:"parent_manifest_digest"`
	ChunkSize            int           `json:"chunk_size"`
	ChunkOverlap         int           `json:"chunk_overlap"`
	ParentsCount         int           `json:"parents_count"`
	ChunksCount          int           `json:"chunks_count"`
	Chunks               []chunkRecord `json:"chunks"`
	ArtifactDigest       string        `json:"artifact_digest"`
}

type chunkRecord struct {
	ParentDocumentID  string         `json:"parent_document_id"`
	ChunkID           string         `json:"chunk_id"`
	ChunkIndex        int            `json:"chunk_index"`
	ContentStart      int            `json:"content_start"`
	ContentEnd        int            `json:"content_end"`
	ParentContentHash string         `json:"parent_content_hash"`
	ChunkContentHash  string         `json:"chunk_content_hash"`
	Name              string         `json:"name"`
	Content           string         `json:"content"`
	BaseEmbeddingText string         `json:"base_embedding_text"`
	Metadata          map[string]any `json:"metadata"`
}

type contextCacheHeader struct {
	RecordType           string `json:"record_type"`
	SchemaVersion        string `json:"schema_version"`
	ChunkManifestDigest  string `json:"chunk_manifest_digest"`
	ParentManifestDigest string `json:"parent_manifest_digest"`
	CacheIdentity        string `json:"cache_identity"`
}

type contextCacheAttempt struct {
	RecordType        string `json:"record_type"`
	ChunkID           string `json:"chunk_id"`
	ParentDocumentID  string `json:"parent_document_id"`
	ParentContentHash string `json:"parent_content_hash"`
	ChunkContentHash  string `json:"chunk_content_hash"`
	Status            string `json:"status"`
	Context           string `json:"context"`
	ContextHash       string `json:"context_hash"`
	FinishReason      string `json:"finish_reason"`
}

type experimentManifestSource struct {
	variant              string
	manifest             *chunkManifest
	contexts             map[string]string
	contextCacheIdentity string
	contextSetDigest     string
}

func (s *experimentManifestSource) Name() string {
	return experimentSourceName
}

func (s *experimentManifestSource) Type() string {
	return experimentSourceType
}

func (s *experimentManifestSource) GetMetadata() map[string]any {
	return map[string]any{}
}

func (s *experimentManifestSource) ReadDocuments(_ context.Context) ([]*document.Document, error) {
	documents := make([]*document.Document, 0, len(s.manifest.Chunks))
	for _, record := range s.manifest.Chunks {
		embeddingText := record.BaseEmbeddingText
		if s.variant == indexVariantContextual {
			contextText, ok := s.contexts[record.ChunkID]
			if !ok || strings.TrimSpace(contextText) == "" {
				return nil, fmt.Errorf("context is missing for chunk %s", record.ChunkID)
			}
			embeddingText = contextualEmbeddingPrefix + strings.TrimSpace(contextText) +
				contextualEmbeddingDelimiter + record.BaseEmbeddingText
		}
		metadata := cloneMetadata(record.Metadata)
		documents = append(documents, &document.Document{
			ID:            record.ChunkID,
			Name:          record.Name,
			Content:       record.Content,
			EmbeddingText: embeddingText,
			Metadata:      metadata,
		})
	}
	return documents, nil
}

func newExperimentManifestSource(
	variant string,
	manifestPath string,
	contextCachePath string,
) (*experimentManifestSource, error) {
	if variant != indexVariantBaseline && variant != indexVariantContextual {
		return nil, fmt.Errorf("unsupported experiment index variant %q", variant)
	}
	manifest, err := loadChunkManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	src := &experimentManifestSource{
		variant:  variant,
		manifest: manifest,
		contexts: make(map[string]string),
	}
	if variant == indexVariantContextual {
		contexts, identity, contextSetDigest, err := loadContextCache(
			contextCachePath,
			manifest,
		)
		if err != nil {
			return nil, err
		}
		src.contexts = contexts
		src.contextCacheIdentity = identity
		src.contextSetDigest = contextSetDigest
	}
	return src, nil
}

func writeChunkManifest(parentPath string, outputPath string) (*chunkManifest, error) {
	parents, err := loadParentManifest(parentPath)
	if err != nil {
		return nil, err
	}
	strategy := chunking.NewFixedSizeChunking(
		chunking.WithChunkSize(benchmarkChunkSize),
		chunking.WithOverlap(benchmarkChunkOverlap),
	)
	records := make([]chunkRecord, 0)
	for _, parent := range parents.Parents {
		metadata := cloneMetadata(parent.Metadata)
		metadata[source.MetaSource] = experimentSourceType
		metadata[source.MetaFileName] = parent.FileName
		metadata[source.MetaURI] = "multihop-rag://" + parent.ParentDocumentID
		metadata[experimentMetaPrefix+"parent_document_id"] = parent.ParentDocumentID
		metadata[experimentMetaPrefix+"parent_content_hash"] = parent.ContentHash
		chunks, err := strategy.Chunk(&document.Document{
			ID:       parent.ParentDocumentID,
			Name:     strings.TrimSuffix(parent.FileName, filepath.Ext(parent.FileName)),
			Content:  parent.Content,
			Metadata: metadata,
		})
		if err != nil {
			return nil, fmt.Errorf("chunk parent %s: %w", parent.ParentDocumentID, err)
		}
		baseContentLength := 0
		for _, chunk := range chunks {
			chunkSize, ok := integerMetadata(chunk.Metadata[source.MetaChunkSize])
			if !ok || chunkSize <= 0 {
				return nil, fmt.Errorf("parent %s produced invalid base chunk size", parent.ParentDocumentID)
			}
			baseContentLength += chunkSize
		}
		for _, chunk := range chunks {
			chunkIndex, ok := integerMetadata(chunk.Metadata[source.MetaChunkIndex])
			if !ok || chunkIndex <= 0 {
				return nil, fmt.Errorf("parent %s produced invalid chunk index", parent.ParentDocumentID)
			}
			contentHash := digestText(chunk.Content)
			chunkID := stableIdentifier(
				"mhrag-chunk",
				parent.ParentDocumentID,
				fmt.Sprintf("%d", chunkIndex),
				contentHash,
			)
			baseStart := (chunkIndex - 1) * benchmarkChunkSize
			contentStart := baseStart
			if chunkIndex > 1 {
				contentStart -= benchmarkChunkOverlap
				if contentStart < 0 {
					contentStart = 0
				}
			}
			contentEnd := chunkIndex * benchmarkChunkSize
			if contentEnd > baseContentLength {
				contentEnd = baseContentLength
			}
			chunk.Metadata[experimentMetaPrefix+"chunk_id"] = chunkID
			chunk.Metadata[experimentMetaPrefix+"chunk_content_hash"] = contentHash
			records = append(records, chunkRecord{
				ParentDocumentID:  parent.ParentDocumentID,
				ChunkID:           chunkID,
				ChunkIndex:        chunkIndex,
				ContentStart:      contentStart,
				ContentEnd:        contentEnd,
				ParentContentHash: parent.ContentHash,
				ChunkContentHash:  contentHash,
				Name:              chunk.Name,
				Content:           chunk.Content,
				BaseEmbeddingText: baseEmbeddingText(parent.FileName, chunkIndex, chunk.Content),
				Metadata:          cloneMetadata(chunk.Metadata),
			})
		}
	}
	manifest := &chunkManifest{
		SchemaVersion:        chunkManifestSchema,
		Dataset:              "multihop-rag",
		ParentManifestDigest: parents.ArtifactDigest,
		ChunkSize:            benchmarkChunkSize,
		ChunkOverlap:         benchmarkChunkOverlap,
		ParentsCount:         len(parents.Parents),
		ChunksCount:          len(records),
		Chunks:               records,
	}
	if err := sealAndWriteJSON(outputPath, manifest); err != nil {
		return nil, fmt.Errorf("write chunk manifest: %w", err)
	}
	return manifest, nil
}

func loadParentManifest(path string) (*parentManifest, error) {
	var manifest parentManifest
	if err := loadSealedJSON(path, parentManifestSchema, &manifest); err != nil {
		return nil, fmt.Errorf("load parent manifest: %w", err)
	}
	if manifest.ParentsCount != len(manifest.Parents) || manifest.ParentsCount == 0 {
		return nil, fmt.Errorf("parent manifest count does not match records")
	}
	seen := make(map[string]struct{}, len(manifest.Parents))
	for index, parent := range manifest.Parents {
		if parent.ParentDocumentID == "" || parent.FileName == "" || parent.Content == "" {
			return nil, fmt.Errorf("parent[%d] has incomplete identity/content", index)
		}
		if parent.ContentHash == "" {
			return nil, fmt.Errorf("parent[%d] content hash is missing", index)
		}
		if digestText(parent.Content) != parent.ContentHash {
			return nil, fmt.Errorf("parent[%d] content hash does not match", index)
		}
		if _, ok := seen[parent.ParentDocumentID]; ok {
			return nil, fmt.Errorf("duplicate parent ID %s", parent.ParentDocumentID)
		}
		seen[parent.ParentDocumentID] = struct{}{}
	}
	return &manifest, nil
}

func loadChunkManifest(path string) (*chunkManifest, error) {
	if path == "" {
		return nil, fmt.Errorf("chunk manifest path is required")
	}
	var manifest chunkManifest
	if err := loadSealedJSON(path, chunkManifestSchema, &manifest); err != nil {
		return nil, fmt.Errorf("load chunk manifest: %w", err)
	}
	if manifest.ChunkSize != benchmarkChunkSize || manifest.ChunkOverlap != benchmarkChunkOverlap {
		return nil, fmt.Errorf(
			"chunk parameters are %d/%d, expected %d/%d",
			manifest.ChunkSize,
			manifest.ChunkOverlap,
			benchmarkChunkSize,
			benchmarkChunkOverlap,
		)
	}
	if strings.TrimSpace(manifest.ParentManifestDigest) == "" {
		return nil, fmt.Errorf("chunk manifest parent digest is missing")
	}
	if manifest.ChunksCount != len(manifest.Chunks) || manifest.ChunksCount == 0 {
		return nil, fmt.Errorf("chunk manifest count does not match records")
	}
	seen := make(map[string]struct{}, len(manifest.Chunks))
	for index, chunk := range manifest.Chunks {
		if chunk.ChunkID == "" || chunk.ParentDocumentID == "" || chunk.Content == "" {
			return nil, fmt.Errorf("chunk[%d] has incomplete identity/content", index)
		}
		if chunk.ParentContentHash == "" {
			return nil, fmt.Errorf("chunk[%d] parent content hash is missing", index)
		}
		if chunk.ChunkContentHash == "" {
			return nil, fmt.Errorf("chunk[%d] chunk content hash is missing", index)
		}
		if digestText(chunk.Content) != chunk.ChunkContentHash {
			return nil, fmt.Errorf("chunk[%d] content hash does not match", index)
		}
		if chunk.ContentStart < 0 || chunk.ContentEnd <= chunk.ContentStart {
			return nil, fmt.Errorf("chunk[%d] has invalid content interval", index)
		}
		if chunk.BaseEmbeddingText != baseEmbeddingText(
			metadataString(chunk.Metadata, source.MetaFileName),
			chunk.ChunkIndex,
			chunk.Content,
		) {
			return nil, fmt.Errorf("chunk[%d] base embedding text does not match", index)
		}
		if metadataString(chunk.Metadata, experimentMetaPrefix+"chunk_id") != chunk.ChunkID {
			return nil, fmt.Errorf("chunk[%d] metadata ID does not match", index)
		}
		if metadataString(
			chunk.Metadata,
			experimentMetaPrefix+"parent_document_id",
		) != chunk.ParentDocumentID {
			return nil, fmt.Errorf("chunk[%d] metadata parent ID does not match", index)
		}
		if metadataString(
			chunk.Metadata,
			experimentMetaPrefix+"parent_content_hash",
		) != chunk.ParentContentHash {
			return nil, fmt.Errorf("chunk[%d] metadata parent content hash does not match", index)
		}
		if metadataString(
			chunk.Metadata,
			experimentMetaPrefix+"chunk_content_hash",
		) != chunk.ChunkContentHash {
			return nil, fmt.Errorf("chunk[%d] metadata chunk content hash does not match", index)
		}
		if _, ok := seen[chunk.ChunkID]; ok {
			return nil, fmt.Errorf("duplicate chunk ID %s", chunk.ChunkID)
		}
		seen[chunk.ChunkID] = struct{}{}
	}
	return &manifest, nil
}

func loadContextCache(
	path string,
	manifest *chunkManifest,
) (map[string]string, string, string, error) {
	if manifest == nil || strings.TrimSpace(manifest.ArtifactDigest) == "" ||
		strings.TrimSpace(manifest.ParentManifestDigest) == "" {
		return nil, "", "", fmt.Errorf("context cache requires sealed chunk manifest lineage")
	}
	if path == "" {
		return nil, "", "", fmt.Errorf("context cache path is required for contextual indexing")
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, "", "", fmt.Errorf("open context cache: %w", err)
	}
	defer handle.Close()

	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	lineNumber := 0
	var header contextCacheHeader
	latest := make(map[string]contextCacheAttempt)
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return nil, "", "", fmt.Errorf("context cache line %d: %w", lineNumber, err)
		}
		switch envelope.RecordType {
		case "header":
			if header.RecordType != "" || lineNumber != 1 {
				return nil, "", "", fmt.Errorf("context cache must contain one header on line 1")
			}
			if err := json.Unmarshal(line, &header); err != nil {
				return nil, "", "", fmt.Errorf("decode context cache header: %w", err)
			}
		case "attempt":
			if header.RecordType == "" {
				return nil, "", "", fmt.Errorf("context cache attempt precedes header")
			}
			var attempt contextCacheAttempt
			if err := json.Unmarshal(line, &attempt); err != nil {
				return nil, "", "", fmt.Errorf("decode context cache line %d: %w", lineNumber, err)
			}
			if attempt.ChunkID == "" || attempt.ParentDocumentID == "" ||
				attempt.ParentContentHash == "" || attempt.ChunkContentHash == "" {
				return nil, "", "", fmt.Errorf(
					"context cache line %d has incomplete lineage identity",
					lineNumber,
				)
			}
			latest[attempt.ChunkID] = attempt
		default:
			return nil, "", "", fmt.Errorf("context cache line %d has unsupported record type %q", lineNumber, envelope.RecordType)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", "", fmt.Errorf("read context cache: %w", err)
	}
	if header.SchemaVersion != contextCacheSchema {
		return nil, "", "", fmt.Errorf("unsupported context cache schema %q", header.SchemaVersion)
	}
	if header.ChunkManifestDigest == "" {
		return nil, "", "", fmt.Errorf("context cache chunk manifest digest is missing")
	}
	if header.ParentManifestDigest == "" {
		return nil, "", "", fmt.Errorf("context cache parent manifest digest is missing")
	}
	if header.ChunkManifestDigest != manifest.ArtifactDigest {
		return nil, "", "", fmt.Errorf("context cache and chunk manifest digests do not match")
	}
	if header.ParentManifestDigest != manifest.ParentManifestDigest {
		return nil, "", "", fmt.Errorf("context cache and parent manifest digests do not match")
	}
	if header.CacheIdentity == "" {
		return nil, "", "", fmt.Errorf("context cache identity is missing")
	}
	expectedChunks := make(map[string]struct{}, len(manifest.Chunks))
	for _, chunk := range manifest.Chunks {
		expectedChunks[chunk.ChunkID] = struct{}{}
	}
	for chunkID := range latest {
		if _, ok := expectedChunks[chunkID]; !ok {
			return nil, "", "", fmt.Errorf("context cache contains unknown chunk %s", chunkID)
		}
	}
	contexts := make(map[string]string, len(manifest.Chunks))
	contextSet := make([]map[string]string, 0, len(manifest.Chunks))
	for _, chunk := range manifest.Chunks {
		attempt, ok := latest[chunk.ChunkID]
		if !ok || attempt.Status != "success" || strings.TrimSpace(attempt.Context) == "" {
			return nil, "", "", fmt.Errorf("successful context is missing for chunk %s", chunk.ChunkID)
		}
		if attempt.ParentDocumentID != chunk.ParentDocumentID {
			return nil, "", "", fmt.Errorf("context parent ID does not match chunk %s", chunk.ChunkID)
		}
		if attempt.ParentContentHash != chunk.ParentContentHash {
			return nil, "", "", fmt.Errorf("context parent content hash does not match chunk %s", chunk.ChunkID)
		}
		if attempt.ChunkContentHash != chunk.ChunkContentHash {
			return nil, "", "", fmt.Errorf("context chunk content hash does not match chunk %s", chunk.ChunkID)
		}
		if !strings.EqualFold(strings.TrimSpace(attempt.FinishReason), "stop") {
			return nil, "", "", fmt.Errorf("context finish reason is not a normal stop for chunk %s", chunk.ChunkID)
		}
		contextText := strings.TrimSpace(attempt.Context)
		contextHash := digestText(contextText)
		if attempt.ContextHash != contextHash {
			return nil, "", "", fmt.Errorf("context hash does not match text for chunk %s", chunk.ChunkID)
		}
		contexts[chunk.ChunkID] = contextText
		contextSet = append(contextSet, map[string]string{
			"chunk_id":     chunk.ChunkID,
			"context_hash": contextHash,
		})
	}
	canonicalContextSet, err := canonicalJSON(contextSet)
	if err != nil {
		return nil, "", "", fmt.Errorf("canonicalize context set: %w", err)
	}
	return contexts, header.CacheIdentity, digestText(string(canonicalContextSet)), nil
}

func loadSealedJSON(path string, expectedSchema string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	digest, ok := payload["artifact_digest"].(string)
	if !ok || digest == "" {
		return fmt.Errorf("artifact digest is missing")
	}
	if schema, _ := payload["schema_version"].(string); schema != expectedSchema {
		return fmt.Errorf("unsupported schema %q, expected %q", schema, expectedSchema)
	}
	delete(payload, "artifact_digest")
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return err
	}
	if digestText(string(canonical)) != digest {
		return fmt.Errorf("artifact digest does not match content")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return err
	}
	return nil
}

func sealAndWriteJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	delete(payload, "artifact_digest")
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return err
	}
	payload["artifact_digest"] = digestText(string(canonical))
	final, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	final = append(final, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".contextual-manifest-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(final); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return json.Unmarshal(final, value)
}

func canonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := appendCanonicalJSON(&buffer, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func appendCanonicalJSON(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			encodedKey, err := jsonString(key)
			if err != nil {
				return err
			}
			buffer.Write(encodedKey)
			buffer.WriteByte(':')
			if err := appendCanonicalJSON(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
		return nil
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := appendCanonicalJSON(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
		return nil
	case string:
		encoded, err := jsonString(typed)
		if err != nil {
			return err
		}
		buffer.Write(encoded)
		return nil
	case json.Number:
		buffer.WriteString(typed.String())
		return nil
	case float64, bool, nil:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		buffer.Write(encoded)
		return nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		var normalized any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&normalized); err != nil {
			return err
		}
		return appendCanonicalJSON(buffer, normalized)
	}
}

func jsonString(value string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func stableIdentifier(prefix string, parts ...string) string {
	return prefix + "-" + digestText(strings.Join(parts, "\x00"))
}

func baseEmbeddingText(fileName string, chunkIndex int, content string) string {
	return fmt.Sprintf("file: %s | chunk: %d\n%s", fileName, chunkIndex, content)
}

func cloneMetadata(metadata map[string]any) map[string]any {
	clone := make(map[string]any, len(metadata))
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func integerMetadata(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		return int(typed), typed == float64(int(typed))
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}
