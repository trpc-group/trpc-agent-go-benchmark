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
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

func TestWorkspaceVectorStoreKeywordWithoutEmbeddingIsDeterministic(t *testing.T) {
	store := newWorkspaceVectorStore()
	for _, doc := range []*document.Document{
		{ID: "b", Content: "def common_name(): pass"},
		{ID: "a", Content: "def common_name(): pass"},
		{ID: "c", Content: "def unrelated(): pass"},
	} {
		if err := store.Add(context.Background(), doc, nil); err != nil {
			t.Fatalf("Add(%s) error = %v", doc.ID, err)
		}
	}

	for attempt := 0; attempt < 5; attempt++ {
		result, err := store.Search(context.Background(), &vectorstore.SearchQuery{
			Query:      "common_name",
			Limit:      10,
			SearchMode: vectorstore.SearchModeKeyword,
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		if len(result.Results) != 2 || result.Results[0].Document.ID != "a" ||
			result.Results[1].Document.ID != "b" {
			t.Fatalf("keyword order = %#v, want a,b", result.Results)
		}
	}
}

func TestWorkspaceVectorStoreHybridFallbackAndRRF(t *testing.T) {
	store := newWorkspaceVectorStore()
	if err := store.Add(context.Background(), &document.Document{ID: "a", Content: "alpha"}, []float64{0, 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(context.Background(), &document.Document{ID: "b", Content: "beta"}, []float64{1, 0}); err != nil {
		t.Fatal(err)
	}

	keywordOnly, err := store.Search(context.Background(), &vectorstore.SearchQuery{
		Query: "alpha", Limit: 10, SearchMode: vectorstore.SearchModeHybrid,
	})
	if err != nil {
		t.Fatalf("hybrid keyword fallback error = %v", err)
	}
	if len(keywordOnly.Results) != 1 || keywordOnly.Results[0].Document.ID != "a" ||
		keywordOnly.Results[0].Score != 1 {
		t.Fatalf("hybrid keyword fallback = %#v", keywordOnly.Results)
	}

	fused, err := store.Search(context.Background(), &vectorstore.SearchQuery{
		Query: "alpha", Vector: []float64{1, 0}, Limit: 10, SearchMode: vectorstore.SearchModeHybrid,
	})
	if err != nil {
		t.Fatalf("hybrid RRF error = %v", err)
	}
	if len(fused.Results) != 2 || fused.Results[0].Document.ID != "a" ||
		fused.Results[1].Document.ID != "b" {
		t.Fatalf("hybrid RRF order = %#v, want a,b", fused.Results)
	}
}

func TestWorkspaceVectorStoreKeepsBM25InSync(t *testing.T) {
	ctx := context.Background()
	store := newWorkspaceVectorStore()
	if err := store.Add(ctx, &document.Document{ID: "doc", Content: "before"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, &document.Document{ID: "doc", Content: "after"}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := store.Search(ctx, &vectorstore.SearchQuery{Query: "before", SearchMode: vectorstore.SearchModeKeyword})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Results) != 0 {
		t.Fatalf("updated BM25 retained old terms: %#v", before.Results)
	}
	after, err := store.Search(ctx, &vectorstore.SearchQuery{Query: "after", SearchMode: vectorstore.SearchModeKeyword})
	if err != nil || len(after.Results) != 1 {
		t.Fatalf("updated BM25 result = %#v, err = %v", after, err)
	}
	if err := store.Delete(ctx, "doc"); err != nil {
		t.Fatal(err)
	}
	after, err = store.Search(ctx, &vectorstore.SearchQuery{Query: "after", SearchMode: vectorstore.SearchModeKeyword})
	if err != nil || len(after.Results) != 0 {
		t.Fatalf("deleted BM25 result = %#v, err = %v", after, err)
	}
}
