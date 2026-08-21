//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingcache

import "testing"

func TestKeyIsolatesIdentityAndExactInput(t *testing.T) {
	base := Identity{
		Provider:           " OpenAI ",
		Model:              "bge-m3",
		ModelFingerprint:   "weights-v1",
		BackendFingerprint: " ABC123 ",
		Dimensions:         3,
	}
	normalized := Identity{
		Provider:           "openai",
		Model:              "bge-m3",
		ModelFingerprint:   "weights-v1",
		BackendFingerprint: "abc123",
		Dimensions:         3,
	}
	if identityHash(base) != identityHash(normalized) {
		t.Fatal("normalized identities produced different hashes")
	}

	key := keyForText(identityHash(base), "same text")
	if key == keyForText(identityHash(base), "same text\n") {
		t.Fatal("different exact inputs produced the same key")
	}
	variants := []Identity{
		{Provider: "other", Model: "bge-m3", ModelFingerprint: "weights-v1", BackendFingerprint: "abc123", Dimensions: 3},
		{Provider: "openai", Model: "bge-m3-v2", ModelFingerprint: "weights-v1", BackendFingerprint: "abc123", Dimensions: 3},
		{Provider: "openai", Model: "bge-m3", ModelFingerprint: "weights-v2", BackendFingerprint: "abc123", Dimensions: 3},
		{Provider: "openai", Model: "bge-m3", ModelFingerprint: "weights-v1", BackendFingerprint: "def456", Dimensions: 3},
		{Provider: "openai", Model: "bge-m3", ModelFingerprint: "weights-v1", BackendFingerprint: "abc123", Dimensions: 4},
	}
	for _, variant := range variants {
		if key == keyForText(identityHash(variant), "same text") {
			t.Fatalf("identity variant did not isolate key: %+v", variant)
		}
	}
}
