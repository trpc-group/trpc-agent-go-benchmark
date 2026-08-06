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
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

const (
	workspaceBM25K1 = 1.2
	workspaceBM25B  = 0.75
	workspaceRRFK   = 60.0
)

var workspaceBM25MetadataKeys = []string{
	"trpc_ast_name",
	"trpc_ast_full_name",
	"trpc_ast_package",
	"trpc_ast_file_path",
	"trpc_ast_signature",
	"trpc_ast_comment",
}

var _ vectorstore.VectorStore = (*workspaceVectorStore)(nil)

// workspaceVectorStore is deliberately local to the benchmark. The public
// framework in-memory store treats keyword search as filter-only search and
// rejects documents without embeddings, neither of which implements the
// frozen code_search contract. This store keeps the framework interface while
// making keyword and hybrid ranking deterministic and testable.
type workspaceVectorStore struct {
	mu         sync.RWMutex
	documents  map[string]*document.Document
	embeddings map[string][]float64
	bm25       *workspaceBM25Index
}

func newWorkspaceVectorStore() *workspaceVectorStore {
	return &workspaceVectorStore{
		documents:  make(map[string]*document.Document),
		embeddings: make(map[string][]float64),
		bm25:       newWorkspaceBM25Index(),
	}
}

func (s *workspaceVectorStore) Add(_ context.Context, doc *document.Document, embedding []float64) error {
	if doc == nil {
		return errors.New("document cannot be nil")
	}
	if strings.TrimSpace(doc.ID) == "" {
		return errors.New("document ID cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[doc.ID] = doc.Clone()
	s.embeddings[doc.ID] = slices.Clone(embedding)
	s.bm25.upsert(doc.ID, workspaceSearchText(doc))
	return nil
}

func (s *workspaceVectorStore) Get(_ context.Context, id string) (*document.Document, []float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.documents[id]
	if !ok {
		return nil, nil, fmt.Errorf("document not found: %s", id)
	}
	return doc.Clone(), slices.Clone(s.embeddings[id]), nil
}

func (s *workspaceVectorStore) Update(_ context.Context, doc *document.Document, embedding []float64) error {
	if doc == nil {
		return errors.New("document cannot be nil")
	}
	if strings.TrimSpace(doc.ID) == "" {
		return errors.New("document ID cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.documents[doc.ID]; !ok {
		return fmt.Errorf("document not found: %s", doc.ID)
	}
	s.documents[doc.ID] = doc.Clone()
	s.embeddings[doc.ID] = slices.Clone(embedding)
	s.bm25.upsert(doc.ID, workspaceSearchText(doc))
	return nil
}

func (s *workspaceVectorStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.documents[id]; !ok {
		return fmt.Errorf("document not found: %s", id)
	}
	delete(s.documents, id)
	delete(s.embeddings, id)
	s.bm25.delete(id)
	return nil
}

func (s *workspaceVectorStore) Search(ctx context.Context, query *vectorstore.SearchQuery) (*vectorstore.SearchResult, error) {
	if query == nil {
		return nil, errors.New("query cannot be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if query.Filter != nil && query.Filter.FilterCondition != nil {
		return nil, errors.New("workspace vector store does not support universal search filters")
	}

	var ranked []workspaceRankedDocument
	switch query.SearchMode {
	case vectorstore.SearchModeKeyword:
		ranked = s.keywordRank(query.Query, query.Filter)
	case vectorstore.SearchModeVector:
		if len(query.Vector) == 0 {
			return nil, errors.New("query vector cannot be empty for vector search")
		}
		ranked = s.vectorRank(query.Vector, query.Filter)
	case vectorstore.SearchModeHybrid:
		ranked = reciprocalRankFusion(
			s.keywordRank(query.Query, query.Filter),
			s.vectorRank(query.Vector, query.Filter),
		)
	case vectorstore.SearchModeFilter:
		for id, doc := range s.documents {
			if workspaceMatchesFilter(id, doc, query.Filter) {
				ranked = append(ranked, workspaceRankedDocument{id: id, score: 1})
			}
		}
		sortWorkspaceRanks(ranked)
	default:
		return nil, fmt.Errorf("unsupported workspace search mode %d", query.SearchMode)
	}

	limit := query.Limit
	if limit <= 0 || limit > len(ranked) {
		limit = len(ranked)
	}
	results := make([]*vectorstore.ScoredDocument, 0, limit)
	for _, item := range ranked {
		if item.score < query.MinScore {
			continue
		}
		results = append(results, &vectorstore.ScoredDocument{
			Document: s.documents[item.id].Clone(),
			Score:    item.score,
		})
		if len(results) == limit {
			break
		}
	}
	return &vectorstore.SearchResult{Results: results}, nil
}

type workspaceRankedDocument struct {
	id    string
	score float64
}

type workspaceBM25Document struct {
	termFrequency map[string]int
	length        int
}

type workspaceBM25Index struct {
	documents           map[string]workspaceBM25Document
	documentFrequency   map[string]int
	totalDocumentLength int
}

func newWorkspaceBM25Index() *workspaceBM25Index {
	return &workspaceBM25Index{
		documents:         make(map[string]workspaceBM25Document),
		documentFrequency: make(map[string]int),
	}
}

func (i *workspaceBM25Index) upsert(id, text string) {
	i.delete(id)
	tokens := workspaceTokens(text)
	doc := workspaceBM25Document{termFrequency: make(map[string]int), length: len(tokens)}
	for _, token := range tokens {
		doc.termFrequency[token]++
	}
	for term := range doc.termFrequency {
		i.documentFrequency[term]++
	}
	i.documents[id] = doc
	i.totalDocumentLength += doc.length
}

func (i *workspaceBM25Index) delete(id string) {
	doc, ok := i.documents[id]
	if !ok {
		return
	}
	for term := range doc.termFrequency {
		i.documentFrequency[term]--
		if i.documentFrequency[term] <= 0 {
			delete(i.documentFrequency, term)
		}
	}
	i.totalDocumentLength -= doc.length
	delete(i.documents, id)
}

func (i *workspaceBM25Index) scores(query string) map[string]float64 {
	queryTerms := uniqueWorkspaceTokens(workspaceTokens(query))
	result := make(map[string]float64)
	if len(queryTerms) == 0 || len(i.documents) == 0 {
		return result
	}
	documentCount := float64(len(i.documents))
	averageLength := float64(i.totalDocumentLength) / documentCount
	if averageLength == 0 {
		averageLength = 1
	}
	for id, doc := range i.documents {
		score := 0.0
		for _, term := range queryTerms {
			termFrequency := float64(doc.termFrequency[term])
			if termFrequency == 0 {
				continue
			}
			documentFrequency := float64(i.documentFrequency[term])
			idf := math.Log(1 + (documentCount-documentFrequency+0.5)/(documentFrequency+0.5))
			denominator := termFrequency + workspaceBM25K1*(1-workspaceBM25B+
				workspaceBM25B*float64(doc.length)/averageLength)
			score += idf * termFrequency * (workspaceBM25K1 + 1) / denominator
		}
		if score > 0 {
			result[id] = score
		}
	}
	return result
}

func (s *workspaceVectorStore) keywordRank(query string, filter *vectorstore.SearchFilter) []workspaceRankedDocument {
	scores := s.bm25.scores(query)
	ranked := make([]workspaceRankedDocument, 0, len(scores))
	for id, score := range scores {
		if workspaceMatchesFilter(id, s.documents[id], filter) {
			ranked = append(ranked, workspaceRankedDocument{id: id, score: score})
		}
	}
	sortWorkspaceRanks(ranked)
	return ranked
}

func (s *workspaceVectorStore) vectorRank(vector []float64, filter *vectorstore.SearchFilter) []workspaceRankedDocument {
	if len(vector) == 0 {
		return nil
	}
	ranked := make([]workspaceRankedDocument, 0, len(s.documents))
	for id, doc := range s.documents {
		if !workspaceMatchesFilter(id, doc, filter) {
			continue
		}
		embedding := s.embeddings[id]
		if len(embedding) != len(vector) || len(embedding) == 0 {
			continue
		}
		score := workspaceCosineSimilarity(vector, embedding)
		ranked = append(ranked, workspaceRankedDocument{id: id, score: score})
	}
	sortWorkspaceRanks(ranked)
	return ranked
}

func reciprocalRankFusion(keyword, vector []workspaceRankedDocument) []workspaceRankedDocument {
	scores := make(map[string]float64, len(keyword)+len(vector))
	nonEmpty := 0
	if len(keyword) > 0 {
		nonEmpty++
	}
	for rank, item := range keyword {
		scores[item.id] += 1 / (workspaceRRFK + float64(rank+1))
	}
	if len(vector) > 0 {
		nonEmpty++
	}
	for rank, item := range vector {
		scores[item.id] += 1 / (workspaceRRFK + float64(rank+1))
	}
	if nonEmpty == 0 {
		return nil
	}
	maximum := float64(nonEmpty) / (workspaceRRFK + 1)
	ranked := make([]workspaceRankedDocument, 0, len(scores))
	for id, score := range scores {
		ranked = append(ranked, workspaceRankedDocument{id: id, score: score / maximum})
	}
	sortWorkspaceRanks(ranked)
	return ranked
}

func sortWorkspaceRanks(ranked []workspaceRankedDocument) {
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
}

func workspaceSearchText(doc *document.Document) string {
	if doc == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(doc.Content)
	for _, key := range workspaceBM25MetadataKeys {
		value, ok := doc.Metadata[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok || text == "" {
			continue
		}
		b.WriteByte('\n')
		b.WriteString(text)
	}
	return b.String()
}

func workspaceTokens(value string) []string {
	var tokens []string
	flush := func(raw []rune) {
		if len(raw) == 0 {
			return
		}
		tokens = append(tokens, strings.ToLower(string(raw)))
		for _, part := range strings.FieldsFunc(string(raw), func(r rune) bool {
			return r == '_' || r == '.' || r == '/' || r == '-' || r == ':'
		}) {
			tokens = append(tokens, splitWorkspaceCamelToken(part)...)
		}
	}
	var current []rune
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '/' || r == '-' || r == ':' {
			current = append(current, r)
			continue
		}
		flush(current)
		current = current[:0]
	}
	flush(current)
	return compactWorkspaceTokens(tokens)
}

func splitWorkspaceCamelToken(token string) []string {
	if token == "" {
		return nil
	}
	runes := []rune(token)
	start := 0
	var result []string
	for index := 1; index < len(runes); index++ {
		boundary := unicode.IsUpper(runes[index]) && unicode.IsLower(runes[index-1])
		acronymBoundary := index+1 < len(runes) && unicode.IsUpper(runes[index]) &&
			unicode.IsUpper(runes[index-1]) && unicode.IsLower(runes[index+1])
		if boundary || acronymBoundary {
			result = append(result, strings.ToLower(string(runes[start:index])))
			start = index
		}
	}
	return append(result, strings.ToLower(string(runes[start:])))
}

func compactWorkspaceTokens(tokens []string) []string {
	result := tokens[:0]
	for _, token := range tokens {
		if token != "" {
			result = append(result, token)
		}
	}
	return result
}

func uniqueWorkspaceTokens(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

func workspaceCosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index, value := range left {
		dot += value * right[index]
		leftNorm += value * value
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func workspaceMatchesFilter(id string, doc *document.Document, filter *vectorstore.SearchFilter) bool {
	if filter == nil {
		return true
	}
	if len(filter.IDs) > 0 && !slices.Contains(filter.IDs, id) {
		return false
	}
	for key, expected := range filter.Metadata {
		if doc == nil || !reflect.DeepEqual(doc.Metadata[key], expected) {
			return false
		}
	}
	return true
}

func (s *workspaceVectorStore) DeleteByFilter(_ context.Context, options ...vectorstore.DeleteOption) error {
	config := vectorstore.ApplyDeleteOptions(options...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if config.DeleteAll {
		if len(config.DocumentIDs) > 0 || len(config.Filter) > 0 {
			return errors.New("delete all cannot be combined with filters")
		}
		clear(s.documents)
		clear(s.embeddings)
		s.bm25 = newWorkspaceBM25Index()
		return nil
	}
	if len(config.DocumentIDs) == 0 && len(config.Filter) == 0 {
		return errors.New("delete by filter requires a filter")
	}
	filter := &vectorstore.SearchFilter{IDs: config.DocumentIDs, Metadata: config.Filter}
	for id, doc := range s.documents {
		if workspaceMatchesFilter(id, doc, filter) {
			delete(s.documents, id)
			delete(s.embeddings, id)
			s.bm25.delete(id)
		}
	}
	return nil
}

func (s *workspaceVectorStore) UpdateByFilter(
	context.Context,
	...vectorstore.UpdateByFilterOption,
) (int64, error) {
	return 0, errors.New("workspace vector store does not support UpdateByFilter")
}

func (s *workspaceVectorStore) Count(_ context.Context, options ...vectorstore.CountOption) (int, error) {
	config := vectorstore.ApplyCountOptions(options...)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(config.Filter) == 0 {
		return len(s.documents), nil
	}
	count := 0
	filter := &vectorstore.SearchFilter{Metadata: config.Filter}
	for id, doc := range s.documents {
		if workspaceMatchesFilter(id, doc, filter) {
			count++
		}
	}
	return count, nil
}

func (s *workspaceVectorStore) GetMetadata(
	_ context.Context,
	options ...vectorstore.GetMetadataOption,
) (map[string]vectorstore.DocumentMetadata, error) {
	config, err := vectorstore.ApplyGetMetadataOptions(options...)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.documents))
	filter := &vectorstore.SearchFilter{IDs: config.IDs, Metadata: config.Filter}
	for id, doc := range s.documents {
		if workspaceMatchesFilter(id, doc, filter) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	start := config.Offset
	if start < 0 {
		start = 0
	}
	if start >= len(ids) {
		return map[string]vectorstore.DocumentMetadata{}, nil
	}
	end := len(ids)
	if config.Limit > 0 && start+config.Limit < end {
		end = start + config.Limit
	}
	result := make(map[string]vectorstore.DocumentMetadata, end-start)
	for _, id := range ids[start:end] {
		result[id] = vectorstore.DocumentMetadata{Metadata: cloneWorkspaceMetadata(s.documents[id].Metadata)}
	}
	return result, nil
}

func cloneWorkspaceMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func (s *workspaceVectorStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.documents)
	clear(s.embeddings)
	s.bm25 = newWorkspaceBM25Index()
	return nil
}
