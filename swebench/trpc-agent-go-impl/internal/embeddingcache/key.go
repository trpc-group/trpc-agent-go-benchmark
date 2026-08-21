//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package embeddingcache persists exact embedding results for repeated
// SWE-Bench workspace indexing.
package embeddingcache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

const schemaVersion = 2

// Key is the content-addressed identifier for one exact embedding input.
type Key [sha256.Size]byte

// Identity isolates cache entries produced by different embedding models.
type Identity struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	ModelFingerprint   string `json:"model_fingerprint"`
	BackendFingerprint string `json:"backend_fingerprint"`
	Dimensions         int    `json:"dimensions"`
}

// Validate checks the fields required for safe persistent reuse.
func (i Identity) Validate() error {
	if strings.TrimSpace(i.Provider) == "" {
		return fmt.Errorf("embedding cache provider is required")
	}
	if strings.TrimSpace(i.Model) == "" {
		return fmt.Errorf("embedding cache model is required")
	}
	if strings.TrimSpace(i.ModelFingerprint) == "" {
		return fmt.Errorf("embedding cache model fingerprint is required")
	}
	if strings.TrimSpace(i.BackendFingerprint) == "" {
		return fmt.Errorf("embedding cache backend fingerprint is required")
	}
	if i.Dimensions <= 0 {
		return fmt.Errorf("embedding cache dimensions must be positive")
	}
	return nil
}

func (i Identity) normalized() Identity {
	return Identity{
		Provider:           strings.ToLower(strings.TrimSpace(i.Provider)),
		Model:              strings.TrimSpace(i.Model),
		ModelFingerprint:   strings.TrimSpace(i.ModelFingerprint),
		BackendFingerprint: strings.ToLower(strings.TrimSpace(i.BackendFingerprint)),
		Dimensions:         i.Dimensions,
	}
}

func identityHash(identity Identity) [sha256.Size]byte {
	normalized := identity.normalized()
	digest := sha256.New()
	writeHashPart(digest, "trpc-agent-go-embedding-cache")
	writeHashPart(digest, fmt.Sprintf("schema-v%d", schemaVersion))
	writeHashPart(digest, normalized.Provider)
	writeHashPart(digest, normalized.Model)
	writeHashPart(digest, normalized.ModelFingerprint)
	writeHashPart(digest, normalized.BackendFingerprint)
	writeHashPart(digest, fmt.Sprintf("%d", normalized.Dimensions))
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func keyForText(identityDigest [sha256.Size]byte, text string) Key {
	digest := sha256.New()
	writeHashPart(digest, "embedding-input")
	_, _ = digest.Write(identityDigest[:])
	writeHashPart(digest, text)
	var key Key
	copy(key[:], digest.Sum(nil))
	return key
}

func writeHashPart(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func databaseFileName(identity Identity) string {
	digest := identityHash(identity)
	return fmt.Sprintf("embeddings-v%d-%s.sqlite", schemaVersion, hex.EncodeToString(digest[:]))
}
