//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExperimentManifestSourceChangesOnlyEmbeddingText(t *testing.T) {
	temporary := t.TempDir()
	parentPath := filepath.Join(temporary, "parents.json")
	chunkPath := filepath.Join(temporary, "chunks.json")
	cachePath := filepath.Join(temporary, "contexts.jsonl")
	content := strings.Repeat("a", benchmarkChunkSize-20) +
		" the release happened in July " + strings.Repeat("b", 100)
	parents := &parentManifest{
		SchemaVersion: parentManifestSchema,
		ParentsCount:  1,
		Parents: []parentRecord{
			{
				ParentDocumentID: "parent-1",
				CorpusIndex:      0,
				FileName:         "article.txt",
				Content:          content,
				ContentHash:      digestText(content),
				Metadata:         map[string]any{"title": "Article"},
			},
		},
	}
	if err := sealAndWriteJSON(parentPath, parents); err != nil {
		t.Fatalf("sealAndWriteJSON(parent) error = %v", err)
	}
	chunks, err := writeChunkManifest(parentPath, chunkPath)
	if err != nil {
		t.Fatalf("writeChunkManifest() error = %v", err)
	}
	if chunks.ChunksCount != 2 {
		t.Fatalf("chunks count = %d, want 2", chunks.ChunksCount)
	}
	if chunks.Chunks[1].ContentStart != benchmarkChunkSize-benchmarkChunkOverlap {
		t.Fatalf("second chunk start = %d", chunks.Chunks[1].ContentStart)
	}

	cacheRecords := []any{
		contextCacheHeader{
			RecordType:           "header",
			SchemaVersion:        contextCacheSchema,
			ChunkManifestDigest:  chunks.ArtifactDigest,
			ParentManifestDigest: chunks.ParentManifestDigest,
			CacheIdentity:        "cache-identity",
		},
	}
	for _, chunk := range chunks.Chunks {
		contextText := "This article identifies the July release."
		cacheRecords = append(cacheRecords, contextCacheAttempt{
			RecordType:        "attempt",
			ChunkID:           chunk.ChunkID,
			ParentDocumentID:  chunk.ParentDocumentID,
			ParentContentHash: chunk.ParentContentHash,
			ChunkContentHash:  chunk.ChunkContentHash,
			Status:            "success",
			Context:           contextText,
			ContextHash:       digestText(contextText),
			FinishReason:      "stop",
		})
	}
	writeJSONLines(t, cachePath, cacheRecords)

	baseline, err := newExperimentManifestSource(
		indexVariantBaseline,
		chunkPath,
		"",
	)
	if err != nil {
		t.Fatalf("new baseline source error = %v", err)
	}
	contextual, err := newExperimentManifestSource(
		indexVariantContextual,
		chunkPath,
		cachePath,
	)
	if err != nil {
		t.Fatalf("new contextual source error = %v", err)
	}
	if contextual.contextSetDigest == "" {
		t.Fatal("contextual context set digest is empty")
	}
	baselineDocuments, err := baseline.ReadDocuments(context.Background())
	if err != nil {
		t.Fatalf("baseline.ReadDocuments() error = %v", err)
	}
	contextualDocuments, err := contextual.ReadDocuments(context.Background())
	if err != nil {
		t.Fatalf("contextual.ReadDocuments() error = %v", err)
	}
	for index := range baselineDocuments {
		baselineDocument := baselineDocuments[index]
		contextualDocument := contextualDocuments[index]
		if baselineDocument.Content != contextualDocument.Content {
			t.Fatalf("document %d content differs", index)
		}
		if !reflect.DeepEqual(baselineDocument.Metadata, contextualDocument.Metadata) {
			t.Fatalf("document %d metadata differs", index)
		}
		if baselineDocument.EmbeddingText != chunks.Chunks[index].BaseEmbeddingText {
			t.Fatalf("document %d baseline embedding text differs", index)
		}
		wantPrefix := contextualEmbeddingPrefix +
			"This article identifies the July release." +
			contextualEmbeddingDelimiter
		if !strings.HasPrefix(contextualDocument.EmbeddingText, wantPrefix) {
			t.Fatalf("document %d contextual prefix differs", index)
		}
		if !strings.HasSuffix(
			contextualDocument.EmbeddingText,
			baselineDocument.EmbeddingText,
		) {
			t.Fatalf("document %d contextual base text differs", index)
		}
	}
}

func TestContextCacheRejectsIncompleteCoverage(t *testing.T) {
	manifest := &chunkManifest{
		ArtifactDigest:       "chunks",
		ParentManifestDigest: "parents",
		Chunks: []chunkRecord{
			{
				ChunkID:           "chunk-1",
				ParentDocumentID:  "parent-1",
				ParentContentHash: "parent-hash",
				ChunkContentHash:  "chunk-hash",
			},
		},
	}
	path := filepath.Join(t.TempDir(), "contexts.jsonl")
	writeJSONLines(t, path, []any{
		contextCacheHeader{
			RecordType:           "header",
			SchemaVersion:        contextCacheSchema,
			ChunkManifestDigest:  "chunks",
			ParentManifestDigest: "parents",
			CacheIdentity:        "cache",
		},
	})
	if _, _, _, err := loadContextCache(path, manifest); err == nil ||
		!strings.Contains(err.Error(), "successful context is missing") {
		t.Fatalf("loadContextCache() error = %v, want missing-context error", err)
	}
}

func TestContextCacheBindsContextEntitiesAndCanonicalSetDigest(t *testing.T) {
	manifest := &chunkManifest{
		ArtifactDigest:       "chunks",
		ParentManifestDigest: "parents",
		Chunks: []chunkRecord{
			{
				ChunkID:           "chunk-1",
				ParentDocumentID:  "parent-1",
				ParentContentHash: "parent-hash-1",
				ChunkContentHash:  "chunk-hash-1",
			},
			{
				ChunkID:           "chunk-2",
				ParentDocumentID:  "parent-2",
				ParentContentHash: "parent-hash-2",
				ChunkContentHash:  "chunk-hash-2",
			},
		},
	}
	path := filepath.Join(t.TempDir(), "contexts.jsonl")
	records := []any{
		contextCacheHeader{
			RecordType:           "header",
			SchemaVersion:        contextCacheSchema,
			ChunkManifestDigest:  "chunks",
			ParentManifestDigest: "parents",
			CacheIdentity:        "cache",
		},
	}
	for index, chunk := range manifest.Chunks {
		contextText := []string{"Alpha context", "Beta context"}[index]
		records = append(records, contextCacheAttempt{
			RecordType:        "attempt",
			ChunkID:           chunk.ChunkID,
			ParentDocumentID:  chunk.ParentDocumentID,
			ParentContentHash: chunk.ParentContentHash,
			ChunkContentHash:  chunk.ChunkContentHash,
			Status:            "success",
			Context:           "  " + contextText + "  ",
			ContextHash:       digestText(contextText),
			FinishReason:      "stop",
		})
	}
	writeJSONLines(t, path, records)

	contexts, cacheIdentity, contextSetDigest, err := loadContextCache(path, manifest)
	if err != nil {
		t.Fatalf("loadContextCache() error = %v", err)
	}
	if cacheIdentity != "cache" {
		t.Fatalf("cache identity = %q, want cache", cacheIdentity)
	}
	if contexts["chunk-1"] != "Alpha context" || contexts["chunk-2"] != "Beta context" {
		t.Fatalf("trimmed contexts = %#v", contexts)
	}
	// This fixed vector is also asserted by the Python cache tests.
	const wantDigest = "106d98bbb5d3ede84e002004107e1a747dcb13c7a2a129dcf51107da1d2cbff1"
	if contextSetDigest != wantDigest {
		t.Fatalf("context set digest = %q, want %q", contextSetDigest, wantDigest)
	}
}

func TestContextCacheRejectsInvalidBoundContext(t *testing.T) {
	manifest := &chunkManifest{
		ArtifactDigest:       "chunks",
		ParentManifestDigest: "parents",
		Chunks: []chunkRecord{
			{
				ChunkID:           "chunk-1",
				ParentDocumentID:  "parent-1",
				ParentContentHash: "parent-hash",
				ChunkContentHash:  "chunk-hash",
			},
		},
	}
	base := contextCacheAttempt{
		RecordType:        "attempt",
		ChunkID:           "chunk-1",
		ParentDocumentID:  "parent-1",
		ParentContentHash: "parent-hash",
		ChunkContentHash:  "chunk-hash",
		Status:            "success",
		Context:           "Bound context",
		ContextHash:       digestText("Bound context"),
		FinishReason:      "stop",
	}
	tests := []struct {
		name          string
		mutateHeader  func(*contextCacheHeader)
		mutateAttempt func(*contextCacheAttempt)
		want          string
	}{
		{
			name: "missing chunk manifest digest",
			mutateHeader: func(header *contextCacheHeader) {
				header.ChunkManifestDigest = ""
			},
			want: "chunk manifest digest is missing",
		},
		{
			name: "wrong chunk manifest digest",
			mutateHeader: func(header *contextCacheHeader) {
				header.ChunkManifestDigest = "wrong"
			},
			want: "chunk manifest digests do not match",
		},
		{
			name: "missing parent manifest digest",
			mutateHeader: func(header *contextCacheHeader) {
				header.ParentManifestDigest = ""
			},
			want: "parent manifest digest is missing",
		},
		{
			name: "wrong parent manifest digest",
			mutateHeader: func(header *contextCacheHeader) {
				header.ParentManifestDigest = "wrong"
			},
			want: "parent manifest digests do not match",
		},
		{
			name: "missing chunk ID",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ChunkID = ""
			},
			want: "incomplete lineage identity",
		},
		{
			name: "missing parent ID",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ParentDocumentID = ""
			},
			want: "incomplete lineage identity",
		},
		{
			name: "missing parent content hash",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ParentContentHash = ""
			},
			want: "incomplete lineage identity",
		},
		{
			name: "missing chunk content hash",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ChunkContentHash = ""
			},
			want: "incomplete lineage identity",
		},
		{
			name: "context hash",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ContextHash = "wrong"
			},
			want: "context hash",
		},
		{
			name: "parent identity",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ParentDocumentID = "wrong"
			},
			want: "context parent ID",
		},
		{
			name: "parent content hash",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ParentContentHash = "wrong"
			},
			want: "context parent content hash",
		},
		{
			name: "chunk content hash",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ChunkContentHash = "wrong"
			},
			want: "context chunk content hash",
		},
		{
			name: "truncated finish",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.FinishReason = "length"
			},
			want: "finish reason",
		},
		{
			name: "unknown chunk",
			mutateAttempt: func(attempt *contextCacheAttempt) {
				attempt.ChunkID = "unknown"
			},
			want: "unknown chunk",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "contexts.jsonl")
			header := contextCacheHeader{
				RecordType:           "header",
				SchemaVersion:        contextCacheSchema,
				ChunkManifestDigest:  "chunks",
				ParentManifestDigest: "parents",
				CacheIdentity:        "cache",
			}
			attempt := base
			if test.mutateHeader != nil {
				test.mutateHeader(&header)
			}
			if test.mutateAttempt != nil {
				test.mutateAttempt(&attempt)
			}
			writeJSONLines(t, path, []any{
				header,
				attempt,
			})
			if _, _, _, err := loadContextCache(path, manifest); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadContextCache() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSealedManifestRejectsTampering(t *testing.T) {
	tests := []struct {
		name   string
		path   func(string, string) string
		loader func(string) error
	}{
		{
			name: "parent",
			path: func(parentPath, _ string) string {
				return parentPath
			},
			loader: func(path string) error {
				_, err := loadParentManifest(path)
				return err
			},
		},
		{
			name: "chunk",
			path: func(_, chunkPath string) string {
				return chunkPath
			},
			loader: func(path string) error {
				_, err := loadChunkManifest(path)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parentPath, chunkPath, _, _ := writeValidManifestPair(t)
			path := test.path(parentPath, chunkPath)
			tamperSealedJSON(t, path)
			if err := test.loader(path); err == nil ||
				!strings.Contains(err.Error(), "artifact digest does not match content") {
				t.Fatalf("loader error = %v, want artifact digest mismatch", err)
			}
		})
	}
}

func TestManifestRejectsResealedInvalidLineage(t *testing.T) {
	t.Run("parent content hash missing", func(t *testing.T) {
		parentPath, _, parents, _ := writeValidManifestPair(t)
		parents.Parents[0].ContentHash = ""
		if err := sealAndWriteJSON(parentPath, parents); err != nil {
			t.Fatalf("sealAndWriteJSON() error = %v", err)
		}
		if _, err := loadParentManifest(parentPath); err == nil ||
			!strings.Contains(err.Error(), "content hash is missing") {
			t.Fatalf("loadParentManifest() error = %v, want missing hash", err)
		}
	})

	t.Run("parent content hash mismatch", func(t *testing.T) {
		parentPath, _, parents, _ := writeValidManifestPair(t)
		parents.Parents[0].Content += " changed"
		if err := sealAndWriteJSON(parentPath, parents); err != nil {
			t.Fatalf("sealAndWriteJSON() error = %v", err)
		}
		if _, err := loadParentManifest(parentPath); err == nil ||
			!strings.Contains(err.Error(), "content hash does not match") {
			t.Fatalf("loadParentManifest() error = %v, want hash mismatch", err)
		}
	})

	chunkTests := []struct {
		name   string
		mutate func(*chunkManifest)
		want   string
	}{
		{
			name: "parent manifest digest missing",
			mutate: func(manifest *chunkManifest) {
				manifest.ParentManifestDigest = ""
			},
			want: "parent digest is missing",
		},
		{
			name: "parent content hash missing",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].ParentContentHash = ""
			},
			want: "parent content hash is missing",
		},
		{
			name: "chunk content hash missing",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].ChunkContentHash = ""
			},
			want: "chunk content hash is missing",
		},
		{
			name: "chunk content hash mismatch",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].Content += " changed"
			},
			want: "content hash does not match",
		},
		{
			name: "metadata chunk ID mismatch",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].Metadata[experimentMetaPrefix+"chunk_id"] = "wrong"
			},
			want: "metadata ID does not match",
		},
		{
			name: "metadata parent ID mismatch",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].Metadata[experimentMetaPrefix+"parent_document_id"] = "wrong"
			},
			want: "metadata parent ID does not match",
		},
		{
			name: "metadata parent hash mismatch",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].Metadata[experimentMetaPrefix+"parent_content_hash"] = "wrong"
			},
			want: "metadata parent content hash does not match",
		},
		{
			name: "metadata chunk hash mismatch",
			mutate: func(manifest *chunkManifest) {
				manifest.Chunks[0].Metadata[experimentMetaPrefix+"chunk_content_hash"] = "wrong"
			},
			want: "metadata chunk content hash does not match",
		},
	}
	for _, test := range chunkTests {
		t.Run(test.name, func(t *testing.T) {
			_, chunkPath, _, chunks := writeValidManifestPair(t)
			test.mutate(chunks)
			if err := sealAndWriteJSON(chunkPath, chunks); err != nil {
				t.Fatalf("sealAndWriteJSON() error = %v", err)
			}
			if _, err := loadChunkManifest(chunkPath); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadChunkManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func writeValidManifestPair(
	t *testing.T,
) (string, string, *parentManifest, *chunkManifest) {
	t.Helper()
	temporary := t.TempDir()
	parentPath := filepath.Join(temporary, "parents.json")
	chunkPath := filepath.Join(temporary, "chunks.json")
	content := "A complete parent document for lineage validation."
	parents := &parentManifest{
		SchemaVersion: parentManifestSchema,
		ParentsCount:  1,
		Parents: []parentRecord{
			{
				ParentDocumentID: "parent-1",
				CorpusIndex:      0,
				FileName:         "article.txt",
				Content:          content,
				ContentHash:      digestText(content),
				Metadata:         map[string]any{"title": "Article"},
			},
		},
	}
	if err := sealAndWriteJSON(parentPath, parents); err != nil {
		t.Fatalf("sealAndWriteJSON(parent) error = %v", err)
	}
	chunks, err := writeChunkManifest(parentPath, chunkPath)
	if err != nil {
		t.Fatalf("writeChunkManifest() error = %v", err)
	}
	return parentPath, chunkPath, parents, chunks
}

func tamperSealedJSON(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	payload["tampered"] = true
	tampered, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	tampered = append(tampered, '\n')
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}

func writeJSONLines(t *testing.T, path string, records []any) {
	t.Helper()
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	encoder := json.NewEncoder(handle)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			handle.Close()
			t.Fatalf("Encode() error = %v", err)
		}
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
