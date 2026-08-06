//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package embeddingconfig loads the optional workspace RAG embedding config.
package embeddingconfig

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/option"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	openaiembedder "trpc.group/trpc-go/trpc-agent-go/knowledge/embedder/openai"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

const (
	defaultBatchSize   = 64
	defaultConcurrency = 4
	defaultMaxResults  = 4
	defaultMaxChars    = 6000
)

// Config describes an OpenAI-compatible embedding endpoint and retrieval
// bounds. Secrets are kept in a local, ignored YAML file.
type Config struct {
	Embedding struct {
		Provider     string            `yaml:"provider"`
		APIBase      string            `yaml:"api_base"`
		APIKey       string            `yaml:"api_key"`
		Model        string            `yaml:"model"`
		Dimensions   int               `yaml:"dimensions"`
		Timeout      int               `yaml:"timeout"`
		MaxRetries   int               `yaml:"max_retries"`
		BatchSize    int               `yaml:"batch_size"`
		Concurrency  int               `yaml:"concurrency"`
		ExtraHeaders map[string]string `yaml:"extra_headers"`
	} `yaml:"embedding"`
	Retrieval struct {
		Mode       string `yaml:"mode"`
		MaxResults int    `yaml:"max_results"`
		MaxChars   int    `yaml:"max_chars"`
	} `yaml:"retrieval"`
	Cache struct {
		Enabled          bool   `yaml:"enabled"`
		Directory        string `yaml:"directory"`
		ModelFingerprint string `yaml:"model_fingerprint"`
	} `yaml:"cache"`
}

// Load reads and validates an embedding YAML config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse embedding config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("parse embedding config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.Embedding.Provider) == "" {
		c.Embedding.Provider = "openai"
	}
	if c.Embedding.Timeout <= 0 {
		c.Embedding.Timeout = 120
	}
	if c.Embedding.BatchSize <= 0 {
		c.Embedding.BatchSize = defaultBatchSize
	}
	if c.Embedding.Concurrency <= 0 {
		c.Embedding.Concurrency = defaultConcurrency
	}
	if strings.TrimSpace(c.Retrieval.Mode) == "" {
		c.Retrieval.Mode = "hybrid"
	}
	if c.Retrieval.MaxResults <= 0 {
		c.Retrieval.MaxResults = defaultMaxResults
	}
	if c.Retrieval.MaxChars <= 0 {
		c.Retrieval.MaxChars = defaultMaxChars
	}
}

// Validate checks fields required by the current OpenAI-compatible path.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("embedding config cannot be nil")
	}
	if !strings.EqualFold(strings.TrimSpace(c.Embedding.Provider), "openai") {
		return fmt.Errorf("unsupported embedding provider %q", c.Embedding.Provider)
	}
	for name, value := range map[string]string{
		"embedding.api_base": c.Embedding.APIBase,
		"embedding.api_key":  c.Embedding.APIKey,
		"embedding.model":    c.Embedding.Model,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if c.Embedding.Dimensions <= 0 {
		return fmt.Errorf("embedding.dimensions must be positive")
	}
	if c.Embedding.BatchSize <= 0 || c.Embedding.BatchSize > 2048 {
		return fmt.Errorf("embedding.batch_size must be between 1 and 2048")
	}
	if c.Embedding.Concurrency <= 0 {
		return fmt.Errorf("embedding.concurrency must be positive")
	}
	if c.Cache.Enabled {
		if strings.TrimSpace(c.Cache.Directory) == "" {
			return fmt.Errorf("cache.directory is required when cache is enabled")
		}
		if strings.TrimSpace(c.Cache.ModelFingerprint) == "" {
			return fmt.Errorf("cache.model_fingerprint is required when cache is enabled")
		}
	}
	if _, err := c.SearchMode(); err != nil {
		return err
	}
	return nil
}

// CacheIdentity returns the model identity used to isolate persistent entries.
func (c *Config) CacheIdentity() embeddingcache.Identity {
	if c == nil {
		return embeddingcache.Identity{}
	}
	return embeddingcache.Identity{
		Provider:         c.Embedding.Provider,
		Model:            c.Embedding.Model,
		ModelFingerprint: c.Cache.ModelFingerprint,
		Dimensions:       c.Embedding.Dimensions,
	}
}

// NewEmbedder constructs a configured OpenAI-compatible embedder.
func (c *Config) NewEmbedder() *openaiembedder.Embedder {
	requestOptions := []option.RequestOption{
		option.WithRequestTimeout(time.Duration(c.Embedding.Timeout) * time.Second),
	}
	headerNames := make([]string, 0, len(c.Embedding.ExtraHeaders))
	for name, value := range c.Embedding.ExtraHeaders {
		if strings.TrimSpace(name) != "" && strings.TrimSpace(value) != "" {
			headerNames = append(headerNames, name)
		}
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		requestOptions = append(requestOptions, option.WithHeader(name, c.Embedding.ExtraHeaders[name]))
	}
	return openaiembedder.New(
		openaiembedder.WithAPIKey(c.Embedding.APIKey),
		openaiembedder.WithBaseURL(c.Embedding.APIBase),
		openaiembedder.WithModel(c.Embedding.Model),
		openaiembedder.WithDimensions(c.Embedding.Dimensions),
		openaiembedder.WithMaxRetries(c.Embedding.MaxRetries),
		openaiembedder.WithRequestOptions(requestOptions...),
	)
}

// SearchMode maps the config value to the framework retrieval mode.
func (c *Config) SearchMode() (vectorstore.SearchMode, error) {
	switch strings.ToLower(strings.TrimSpace(c.Retrieval.Mode)) {
	case "hybrid":
		return vectorstore.SearchModeHybrid, nil
	case "vector", "dense":
		return vectorstore.SearchModeVector, nil
	case "keyword", "bm25":
		return vectorstore.SearchModeKeyword, nil
	default:
		return 0, fmt.Errorf("unsupported retrieval.mode %q", c.Retrieval.Mode)
	}
}

// Redacted returns manifest-safe configuration metadata.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"provider":               c.Embedding.Provider,
		"endpoint_configured":    strings.TrimSpace(c.Embedding.APIBase) != "",
		"credentials_configured": strings.TrimSpace(c.Embedding.APIKey) != "",
		"model":                  c.Embedding.Model,
		"dimensions":             c.Embedding.Dimensions,
		"batch_size":             c.Embedding.BatchSize,
		"concurrency":            c.Embedding.Concurrency,
		"retrieval_mode":         c.Retrieval.Mode,
		"max_results":            c.Retrieval.MaxResults,
		"max_chars":              c.Retrieval.MaxChars,
		"cache": map[string]any{
			"enabled":              c.Cache.Enabled,
			"directory_configured": strings.TrimSpace(c.Cache.Directory) != "",
			"model_fingerprint":    c.Cache.ModelFingerprint,
			"access":               "readwrite",
		},
	}
}

// ScrubSensitiveText removes endpoint, credential, header-value, and local
// cache-path material before an error is persisted in a portable artifact.
// The returned string remains diagnostic without disclosing local config.
func (c *Config) ScrubSensitiveText(value string) string {
	if c == nil || value == "" {
		return value
	}
	type replacement struct {
		value string
		label string
	}
	replacements := []replacement{
		{value: c.Embedding.APIBase, label: "<redacted-embedding-endpoint>"},
		{value: strings.TrimSuffix(c.Embedding.APIBase, "/"), label: "<redacted-embedding-endpoint>"},
		{value: c.Embedding.APIKey, label: "<redacted-embedding-credential>"},
		{value: c.Cache.Directory, label: "<redacted-embedding-cache-path>"},
	}
	for _, headerValue := range c.Embedding.ExtraHeaders {
		replacements = append(replacements, replacement{
			value: headerValue,
			label: "<redacted-embedding-header-value>",
		})
	}
	sort.SliceStable(replacements, func(i, j int) bool {
		return len(replacements[i].value) > len(replacements[j].value)
	})
	seen := map[string]struct{}{}
	for _, item := range replacements {
		if item.value == "" {
			continue
		}
		if _, ok := seen[item.value]; ok {
			continue
		}
		seen[item.value] = struct{}{}
		value = strings.ReplaceAll(value, item.value, item.label)
	}
	return value
}
