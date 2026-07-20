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
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go/knowledge"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/source"
)

func evaluateRetrieval(
	result *knowledge.SearchResult,
	targets patchTargets,
) (RetrievalMetrics, []RetrievedDocument) {
	metrics := RetrievalMetrics{
		TargetFiles: len(targets.TargetFiles),
		HunkAnchors: len(targets.Anchors),
	}
	if result == nil {
		return metrics, nil
	}
	retrieved := make([]RetrievedDocument, 0, len(result.Documents))
	documents := make([]*document.Document, 0, len(result.Documents))
	targetSet := make(map[string]struct{}, len(targets.TargetFiles))
	for _, path := range targets.TargetFiles {
		targetSet[normalizeResultPath(path)] = struct{}{}
	}
	for rank, item := range result.Documents {
		if item == nil || item.Document == nil {
			continue
		}
		doc := item.Document
		documents = append(documents, doc)
		contentHash := sha256.Sum256([]byte(doc.Content))
		retrieved = append(retrieved, RetrievedDocument{
			Rank:          rank + 1,
			Score:         item.Score,
			FilePath:      resultDocumentPath(doc),
			ASTType:       resultMetadataString(doc, "trpc_ast_type"),
			ContentChars:  utf8.RuneCountInString(doc.Content),
			ContentSHA256: hex.EncodeToString(contentHash[:]),
		})
	}
	metrics.TargetFileRecallAt4 = targetFileRecall(documents, targetSet, 4)
	metrics.TargetFileRecallAt6 = targetFileRecall(documents, targetSet, 6)
	metrics.TargetFileReciprocalRank = targetFileReciprocalRank(documents, targetSet)
	metrics.HunkAnchorRecallAt4 = hunkAnchorRecall(documents, targets.Anchors, 4)
	metrics.HunkAnchorRecallAt6 = hunkAnchorRecall(documents, targets.Anchors, 6)
	metrics.TargetFileCharPrecisionAt6 = targetFileCharPrecision(documents, targetSet, 6)
	return metrics, retrieved
}

func targetFileRecall(
	documents []*document.Document,
	targets map[string]struct{},
	limit int,
) float64 {
	if len(targets) == 0 {
		return 0
	}
	hits := make(map[string]struct{})
	for _, doc := range firstDocuments(documents, limit) {
		path := resultDocumentPath(doc)
		if _, ok := targets[path]; ok {
			hits[path] = struct{}{}
		}
	}
	return float64(len(hits)) / float64(len(targets))
}

func targetFileReciprocalRank(
	documents []*document.Document,
	targets map[string]struct{},
) float64 {
	for index, doc := range documents {
		if _, ok := targets[resultDocumentPath(doc)]; ok {
			return 1 / float64(index+1)
		}
	}
	return 0
}

func hunkAnchorRecall(
	documents []*document.Document,
	anchors []patchAnchor,
	limit int,
) float64 {
	if len(anchors) == 0 {
		return 0
	}
	matched := 0
	for _, anchor := range anchors {
		for _, doc := range firstDocuments(documents, limit) {
			if resultDocumentPath(doc) != normalizeResultPath(anchor.File) {
				continue
			}
			if strings.Contains(normalizeAnchorText(doc.Content), anchor.Text) {
				matched++
				break
			}
		}
	}
	return float64(matched) / float64(len(anchors))
}

func targetFileCharPrecision(
	documents []*document.Document,
	targets map[string]struct{},
	limit int,
) float64 {
	var targetChars, totalChars int
	for _, doc := range firstDocuments(documents, limit) {
		chars := utf8.RuneCountInString(doc.Content)
		totalChars += chars
		if _, ok := targets[resultDocumentPath(doc)]; ok {
			targetChars += chars
		}
	}
	if totalChars == 0 {
		return 0
	}
	return float64(targetChars) / float64(totalChars)
}

func firstDocuments(documents []*document.Document, limit int) []*document.Document {
	if limit <= 0 || len(documents) <= limit {
		return documents
	}
	return documents[:limit]
}

func resultDocumentPath(doc *document.Document) string {
	if doc == nil {
		return ""
	}
	for _, key := range []string{source.MetaFilePath, "trpc_ast_file_path"} {
		if value := resultMetadataString(doc, key); value != "" {
			return normalizeResultPath(value)
		}
	}
	return ""
}

func resultMetadataString(doc *document.Document, key string) string {
	if doc == nil || doc.Metadata == nil {
		return ""
	}
	value, _ := doc.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func normalizeResultPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	return strings.TrimPrefix(path, "./")
}
