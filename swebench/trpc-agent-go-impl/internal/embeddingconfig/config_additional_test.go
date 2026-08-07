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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

func TestValidateRejectsInvalidConfiguration(t *testing.T) {
	if err := (*Config)(nil).Validate(); err == nil {
		t.Fatal("nil config validated successfully")
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "provider",
			mutate: func(config *Config) {
				config.Embedding.Provider = "unsupported"
			},
			want: "unsupported embedding provider",
		},
		{
			name: "missing endpoint",
			mutate: func(config *Config) {
				config.Embedding.APIBase = ""
			},
			want: "is required",
		},
		{
			name: "missing API key",
			mutate: func(config *Config) {
				config.Embedding.APIKey = ""
			},
			want: "is required",
		},
		{
			name: "missing model",
			mutate: func(config *Config) {
				config.Embedding.Model = ""
			},
			want: "is required",
		},
		{
			name: "dimensions",
			mutate: func(config *Config) {
				config.Embedding.Dimensions = 0
			},
			want: "dimensions must be positive",
		},
		{
			name: "batch size",
			mutate: func(config *Config) {
				config.Embedding.BatchSize = 2049
			},
			want: "batch_size",
		},
		{
			name: "concurrency",
			mutate: func(config *Config) {
				config.Embedding.Concurrency = 0
			},
			want: "concurrency must be positive",
		},
		{
			name: "cache directory",
			mutate: func(config *Config) {
				config.Cache.Enabled = true
				config.Cache.Directory = ""
				config.Cache.ModelFingerprint = "weights-v1"
			},
			want: "cache.directory",
		},
		{
			name: "cache fingerprint",
			mutate: func(config *Config) {
				config.Cache.Enabled = true
				config.Cache.Directory = t.TempDir()
				config.Cache.ModelFingerprint = ""
			},
			want: "cache.model_fingerprint",
		},
		{
			name: "retrieval mode",
			mutate: func(config *Config) {
				config.Retrieval.Mode = "invalid"
			},
			want: "unsupported retrieval.mode",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfigForTest()
			test.mutate(config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSearchModeAliases(t *testing.T) {
	tests := []struct {
		value string
		want  vectorstore.SearchMode
	}{
		{value: " HYBRID ", want: vectorstore.SearchModeHybrid},
		{value: "vector", want: vectorstore.SearchModeVector},
		{value: "dense", want: vectorstore.SearchModeVector},
		{value: "keyword", want: vectorstore.SearchModeKeyword},
		{value: "BM25", want: vectorstore.SearchModeKeyword},
	}
	for _, test := range tests {
		t.Run(strings.TrimSpace(test.value), func(t *testing.T) {
			config := validConfigForTest()
			config.Retrieval.Mode = test.value
			got, err := config.SearchMode()
			if err != nil || got != test.want {
				t.Fatalf("SearchMode() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestLoadReportsReadAndYAMLErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load() accepted a missing file")
	}
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("embedding: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "parse embedding config") {
		t.Fatalf("Load() error = %v, want YAML parse error", err)
	}
	unknown := filepath.Join(t.TempDir(), "unknown.yaml")
	if err := os.WriteFile(unknown, []byte("embedding:\n  unknown_field: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(unknown); err == nil || !strings.Contains(err.Error(), "field unknown_field") {
		t.Fatalf("Load() error = %v, want unknown-field rejection", err)
	}
	multiple := filepath.Join(t.TempDir(), "multiple.yaml")
	if err := os.WriteFile(multiple, []byte("embedding: {}\n---\nretrieval: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(multiple); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Load() error = %v, want trailing-document rejection", err)
	}
}

func TestNewEmbedderUsesPublicTAGInterface(t *testing.T) {
	config := validConfigForTest()
	config.Embedding.APIBase = "https://api.example.com/v1"
	config.Embedding.Model = "test-embedding"
	config.Embedding.Dimensions = 2
	config.Embedding.ExtraHeaders = map[string]string{
		"X-Test-Header": "test-value",
		"X-Ignored":     "",
		"":              "ignored",
	}
	configured := config.NewEmbedder()
	if configured.GetDimensions() != 2 {
		t.Fatalf("GetDimensions() = %d, want 2", configured.GetDimensions())
	}
}

func TestCacheIdentityOnNilConfig(t *testing.T) {
	if got := (*Config)(nil).CacheIdentity(); got.Provider != "" ||
		got.Model != "" || got.ModelFingerprint != "" ||
		got.BackendFingerprint != "" || got.Dimensions != 0 {
		t.Fatalf("nil CacheIdentity() = %+v", got)
	}
}

func TestCacheIdentityBindsBackendWithoutBindingCredential(t *testing.T) {
	config := validConfigForTest()
	config.Embedding.ExtraHeaders = map[string]string{
		"X-Tenant": "tenant-a",
		"X-Route":  "route-a",
	}
	initial := config.CacheIdentity().BackendFingerprint
	if len(initial) != 64 {
		t.Fatalf("backend fingerprint = %q, want SHA-256 hex", initial)
	}

	config.Embedding.APIKey = "rotated-credential"
	if got := config.CacheIdentity().BackendFingerprint; got != initial {
		t.Fatalf("credential rotation changed backend fingerprint: %q != %q", got, initial)
	}

	config.Embedding.APIBase = "https://other.example.com/v1"
	endpointChanged := config.CacheIdentity().BackendFingerprint
	if endpointChanged == initial {
		t.Fatal("endpoint change did not change backend fingerprint")
	}

	config.Embedding.APIBase = "https://api.example.com/v1"
	config.Embedding.ExtraHeaders["X-Tenant"] = "tenant-b"
	if got := config.CacheIdentity().BackendFingerprint; got == initial {
		t.Fatal("routing-header change did not change backend fingerprint")
	}
}

func TestRedactedOmitsEndpointCredentialsAndCachePath(t *testing.T) {
	config := validConfigForTest()
	config.Embedding.APIBase = "https://private.example.invalid/v1"
	config.Embedding.APIKey = "super-secret-key"
	config.Cache.Enabled = true
	config.Cache.Directory = "/private/cache/location"
	config.Cache.ModelFingerprint = "public-model-revision"

	data, err := json.Marshal(config.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{
		config.Embedding.APIBase,
		config.Embedding.APIKey,
		config.Cache.Directory,
		"api_base",
		"api_key",
		"\"directory\":",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("redacted metadata contains %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(serialized, "public-model-revision") {
		t.Fatalf("redacted metadata lost model identity: %s", serialized)
	}
}

func TestScrubSensitiveTextRemovesLocalEmbeddingConfiguration(t *testing.T) {
	config := validConfigForTest()
	config.Embedding.APIBase = "https://private.example.invalid/v1/"
	config.Embedding.APIKey = "super-secret-key"
	config.Embedding.ExtraHeaders = map[string]string{"X-Tenant": "private-tenant"}
	config.Cache.Directory = "/private/cache/location"
	input := "POST https://private.example.invalid/v1/embeddings with super-secret-key " +
		"for private-tenant using /private/cache/location failed"
	got := config.ScrubSensitiveText(input)
	for _, forbidden := range []string{
		config.Embedding.APIBase,
		strings.TrimSuffix(config.Embedding.APIBase, "/"),
		config.Embedding.APIKey,
		config.Embedding.ExtraHeaders["X-Tenant"],
		config.Cache.Directory,
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("scrubbed error contains %q: %s", forbidden, got)
		}
	}
	for _, marker := range []string{
		"<redacted-embedding-endpoint>",
		"<redacted-embedding-credential>",
		"<redacted-embedding-header-value>",
		"<redacted-embedding-cache-path>",
	} {
		if !strings.Contains(got, marker) {
			t.Fatalf("scrubbed error missing %q: %s", marker, got)
		}
	}
}

func TestScrubSensitiveTextPreservesOrdinaryShortLiterals(t *testing.T) {
	config := validConfigForTest()
	config.Cache.Directory = "cache"
	config.Embedding.ExtraHeaders = map[string]string{
		"X-Route": "1",
	}
	input := "cache lookup returned 1 result; django__django-11111 remains diagnostic"
	if got := config.ScrubSensitiveText(input); got != input {
		t.Fatalf("ScrubSensitiveText() = %q, want unchanged %q", got, input)
	}
	resolvedCache, err := filepath.Abs(config.Cache.Directory)
	if err != nil {
		t.Fatal(err)
	}
	got := config.ScrubSensitiveText("database at " + resolvedCache + " failed; cache lookup returned 1 result")
	if strings.Contains(got, resolvedCache) ||
		!strings.Contains(got, "<redacted-embedding-cache-path>") ||
		!strings.Contains(got, "cache lookup returned 1 result") {
		t.Fatalf("ScrubSensitiveText() relative-cache handling = %q", got)
	}
}

func validConfigForTest() *Config {
	var config Config
	config.Embedding.Provider = "openai"
	config.Embedding.APIBase = "https://api.example.com/v1"
	config.Embedding.APIKey = "test-key"
	config.Embedding.Model = "test-model"
	config.Embedding.Dimensions = 3
	config.Embedding.BatchSize = 64
	config.Embedding.Concurrency = 4
	config.Retrieval.Mode = "hybrid"
	config.Retrieval.MaxResults = 4
	config.Retrieval.MaxChars = 6000
	return &config
}
