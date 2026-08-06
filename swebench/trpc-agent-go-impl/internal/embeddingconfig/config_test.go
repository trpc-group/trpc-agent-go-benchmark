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
	if cfg.Redacted()["endpoint_configured"] != true ||
		cfg.Redacted()["credentials_configured"] != true {
		t.Fatalf("redacted config = %#v", cfg.Redacted())
	}
	cache, ok := cfg.Redacted()["cache"].(map[string]any)
	if !ok || cache["enabled"] != false || cache["access"] != "readwrite" ||
		cache["directory_configured"] != false {
		t.Fatalf("redacted cache config = %#v", cache)
	}
}

func TestLoadValidatesPersistentCacheIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embedding.yaml")
	err := os.WriteFile(path, []byte(`
embedding:
  provider: openai
  api_base: http://embedding.example/v1
  api_key: secret
  model: bge-m3
  dimensions: 1024
cache:
  enabled: true
  directory: /tmp/embedding-cache
  model_fingerprint: bge-m3-weights-and-tokenizer-v1
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	identity := cfg.CacheIdentity()
	if identity.Provider != "openai" || identity.Model != "bge-m3" ||
		identity.ModelFingerprint != "bge-m3-weights-and-tokenizer-v1" ||
		identity.Dimensions != 1024 {
		t.Fatalf("cache identity = %+v", identity)
	}
}

func TestLoadRejectsIncompleteEnabledCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embedding.yaml")
	err := os.WriteFile(path, []byte(`
embedding:
  provider: openai
  api_base: http://embedding.example/v1
  api_key: secret
  model: bge-m3
  dimensions: 1024
cache:
  enabled: true
  directory: /tmp/embedding-cache
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted enabled cache without model fingerprint")
	}
}
