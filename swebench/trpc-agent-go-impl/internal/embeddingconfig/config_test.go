//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingconfig

import (
	"os"
	"path/filepath"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

func TestLoadAppliesDefaultsAndRedactsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embedding.yaml")
	err := os.WriteFile(path, []byte(`
embedding:
  provider: openai
  api_base: http://embedding.example/v1
  api_key: secret
  model: bge-m3
  dimensions: 1024
retrieval:
  mode: hybrid
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Embedding.BatchSize != 64 || cfg.Embedding.Concurrency != 4 {
		t.Fatalf("embedding defaults = %+v", cfg.Embedding)
	}
	mode, err := cfg.SearchMode()
	if err != nil || mode != vectorstore.SearchModeHybrid {
		t.Fatalf("SearchMode() = %v, %v", mode, err)
	}
	if cfg.Redacted()["api_key"] != "***" {
		t.Fatalf("redacted config = %#v", cfg.Redacted())
	}
}
