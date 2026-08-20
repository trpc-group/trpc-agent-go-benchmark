//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

const (
	lmeEmbeddingCacheMagic     = "LMECACHE1"
	lmeEmbeddingCacheMaxUint32 = uint64(1<<32 - 1)
)

type lmeEmbeddingCache struct {
	inner      embedder.Embedder
	modelName  string
	dimensions int
	dir        string
	mu         sync.Mutex
	hits       atomic.Int64
	misses     atomic.Int64
}

type lmeEmbeddingCacheStats struct {
	Path     string `json:"path"`
	Model    string `json:"model"`
	Hits     int64  `json:"hits"`
	Misses   int64  `json:"misses"`
	Requests int64  `json:"requests"`
	Entries  int64  `json:"entries"`
}

type lmeEmbeddingCachePayload struct {
	Usage     map[string]any `json:"usage,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

var lmeEmbeddingCacheRegistry = struct {
	sync.Mutex
	caches []*lmeEmbeddingCache
	byPath map[string]*lmeEmbeddingCache
}{}

func newLMEEmbeddingCache(
	inner embedder.Embedder,
	modelName string,
	path string,
) (embedder.Embedder, error) {
	if inner == nil {
		return nil, fmt.Errorf("inner embedder is nil")
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create embedding cache dir: %w", err)
	}
	lmeEmbeddingCacheRegistry.Lock()
	if cache := lmeEmbeddingCacheRegistry.byPath[path]; cache != nil {
		lmeEmbeddingCacheRegistry.Unlock()
		return cache, nil
	}
	cache := &lmeEmbeddingCache{
		inner:      inner,
		modelName:  modelName,
		dimensions: inner.GetDimensions(),
		dir:        path,
	}
	if lmeEmbeddingCacheRegistry.byPath == nil {
		lmeEmbeddingCacheRegistry.byPath = make(map[string]*lmeEmbeddingCache)
	}
	lmeEmbeddingCacheRegistry.byPath[path] = cache
	lmeEmbeddingCacheRegistry.caches = append(
		lmeEmbeddingCacheRegistry.caches,
		cache,
	)
	lmeEmbeddingCacheRegistry.Unlock()
	return cache, nil
}

func closeLMEEmbeddingCaches() {
	lmeEmbeddingCacheRegistry.Lock()
	lmeEmbeddingCacheRegistry.caches = nil
	lmeEmbeddingCacheRegistry.byPath = nil
	lmeEmbeddingCacheRegistry.Unlock()
}

func collectLMEEmbeddingCacheStats() []lmeEmbeddingCacheStats {
	lmeEmbeddingCacheRegistry.Lock()
	caches := append([]*lmeEmbeddingCache(nil), lmeEmbeddingCacheRegistry.caches...)
	lmeEmbeddingCacheRegistry.Unlock()
	stats := make([]lmeEmbeddingCacheStats, 0, len(caches))
	for _, cache := range caches {
		if cache == nil {
			continue
		}
		stats = append(stats, cache.stats())
	}
	return stats
}

func writeLMEEmbeddingCacheStats(rootDir string) error {
	stats := collectLMEEmbeddingCacheStats()
	if len(stats) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal LongMemEval embedding cache stats: %w", err)
	}
	data = append(data, '\n')
	if err := writeLMEAtomicFile(
		filepath.Join(rootDir, "embedding_cache_stats.json"),
		data,
		0644,
	); err != nil {
		return fmt.Errorf("write LongMemEval embedding cache stats: %w", err)
	}
	return nil
}

func (c *lmeEmbeddingCache) GetEmbedding(
	ctx context.Context,
	text string,
) ([]float64, error) {
	embedding, _, err := c.lookupOrCreate(ctx, text)
	return embedding, err
}

func (c *lmeEmbeddingCache) GetEmbeddingWithUsage(
	ctx context.Context,
	text string,
) ([]float64, map[string]any, error) {
	return c.lookupOrCreate(ctx, text)
}

func (c *lmeEmbeddingCache) GetDimensions() int {
	return c.inner.GetDimensions()
}

func (c *lmeEmbeddingCache) lookupOrCreate(
	ctx context.Context,
	text string,
) ([]float64, map[string]any, error) {
	key := c.cacheKey(text)
	c.mu.Lock()
	defer c.mu.Unlock()

	embedding, usage, ok, err := c.lookup(key)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		c.hits.Add(1)
		recordLMEEmbeddingUsage(ctx, usage, false)
		return embedding, usage, nil
	}
	c.misses.Add(1)
	embedding, usage, err = c.inner.GetEmbeddingWithUsage(ctx, text)
	if err != nil {
		return nil, nil, err
	}
	recordLMEEmbeddingUsage(ctx, usage, true)
	if err := c.store(key, embedding, usage); err != nil {
		return nil, nil, err
	}
	return embedding, usage, nil
}

func (c *lmeEmbeddingCache) lookup(
	key string,
) ([]float64, map[string]any, bool, error) {
	data, err := os.ReadFile(c.cacheFile(key))
	if os.IsNotExist(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("read embedding cache: %w", err)
	}
	embedding, usage, err := decodeEmbeddingCacheFile(data)
	if err != nil {
		return nil, nil, false, err
	}
	return embedding, usage, true, nil
}

func (c *lmeEmbeddingCache) store(
	key string,
	embedding []float64,
	usage map[string]any,
) error {
	path := c.cacheFile(key)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create embedding cache shard: %w", err)
	}
	data, err := encodeEmbeddingCacheFile(embedding, usage)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write embedding cache temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit embedding cache: %w", err)
	}
	return nil
}

func (c *lmeEmbeddingCache) cacheKey(text string) string {
	textHashBytes := sha256.Sum256([]byte(text))
	raw := fmt.Sprintf(
		"%s\n%d\n%s",
		c.modelName,
		c.dimensions,
		hex.EncodeToString(textHashBytes[:]),
	)
	keyBytes := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(keyBytes[:])
}

func (c *lmeEmbeddingCache) cacheFile(key string) string {
	shard := key
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(c.dir, shard, key+".bin")
}

func (c *lmeEmbeddingCache) stats() lmeEmbeddingCacheStats {
	hits := c.hits.Load()
	misses := c.misses.Load()
	return lmeEmbeddingCacheStats{
		Path:     c.dir,
		Model:    c.modelName,
		Hits:     hits,
		Misses:   misses,
		Requests: hits + misses,
		Entries:  countEmbeddingCacheFiles(c.dir),
	}
}

func countEmbeddingCacheFiles(root string) int64 {
	var count int64
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".bin") {
			count++
		}
		return nil
	})
	return count
}

func encodeEmbeddingCacheFile(
	embedding []float64,
	usage map[string]any,
) ([]byte, error) {
	payload := lmeEmbeddingCachePayload{
		Usage:     usage,
		CreatedAt: time.Now().UTC(),
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode embedding cache payload: %w", err)
	}
	if uint64(len(payloadJSON)) > lmeEmbeddingCacheMaxUint32 {
		return nil, fmt.Errorf("embedding cache payload is too large")
	}
	if uint64(len(embedding)) > lmeEmbeddingCacheMaxUint32 {
		return nil, fmt.Errorf("embedding cache vector is too large")
	}
	payloadLen := uint32(len(payloadJSON)) //nolint:gosec // Length is bounded above.
	embeddingLen := uint32(len(embedding)) //nolint:gosec // Length is bounded above.
	var b bytes.Buffer
	b.WriteString(lmeEmbeddingCacheMagic)
	b.WriteByte('\n')
	if err := binary.Write(&b, binary.LittleEndian, payloadLen); err != nil {
		return nil, err
	}
	if err := binary.Write(&b, binary.LittleEndian, embeddingLen); err != nil {
		return nil, err
	}
	b.Write(payloadJSON)
	for _, value := range embedding {
		if err := binary.Write(&b, binary.LittleEndian, math.Float64bits(value)); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func decodeEmbeddingCacheFile(data []byte) ([]float64, map[string]any, error) {
	prefix := []byte(lmeEmbeddingCacheMagic + "\n")
	if !bytes.HasPrefix(data, prefix) {
		return nil, nil, fmt.Errorf("invalid embedding cache file")
	}
	r := bytes.NewReader(data[len(prefix):])
	var payloadLen uint32
	if err := binary.Read(r, binary.LittleEndian, &payloadLen); err != nil {
		return nil, nil, fmt.Errorf("read embedding cache payload length: %w", err)
	}
	var embeddingLen uint32
	if err := binary.Read(r, binary.LittleEndian, &embeddingLen); err != nil {
		return nil, nil, fmt.Errorf("read embedding cache vector length: %w", err)
	}
	wantRemaining := uint64(payloadLen) + uint64(embeddingLen)*8
	remaining := uint64(r.Len()) //nolint:gosec // bytes.Reader.Len is non-negative.
	if wantRemaining != remaining {
		return nil, nil, fmt.Errorf("invalid embedding cache file length")
	}
	payloadJSON := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payloadJSON); err != nil {
		return nil, nil, fmt.Errorf("read embedding cache payload: %w", err)
	}
	var payload lmeEmbeddingCachePayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return nil, nil, fmt.Errorf("decode embedding cache payload: %w", err)
	}
	embedding := make([]float64, embeddingLen)
	for i := range embedding {
		var bits uint64
		if err := binary.Read(r, binary.LittleEndian, &bits); err != nil {
			return nil, nil, fmt.Errorf("read embedding cache vector: %w", err)
		}
		embedding[i] = math.Float64frombits(bits)
	}
	return embedding, payload.Usage, nil
}
