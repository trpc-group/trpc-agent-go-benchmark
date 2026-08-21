//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingcache

import (
	"strings"
	"testing"
)

func TestIdentityValidate(t *testing.T) {
	tests := []struct {
		name     string
		identity Identity
		want     string
	}{
		{
			name:     "provider",
			identity: Identity{Model: "model", ModelFingerprint: "revision", BackendFingerprint: "backend", Dimensions: 3},
			want:     "provider",
		},
		{
			name: "model",
			identity: Identity{
				Provider: "openai", ModelFingerprint: "revision", BackendFingerprint: "backend", Dimensions: 3,
			},
			want: "model is required",
		},
		{
			name: "fingerprint",
			identity: Identity{
				Provider: "openai", Model: "model", BackendFingerprint: "backend", Dimensions: 3,
			},
			want: "fingerprint",
		},
		{
			name: "backend fingerprint",
			identity: Identity{
				Provider: "openai", Model: "model", ModelFingerprint: "revision", Dimensions: 3,
			},
			want: "backend fingerprint",
		},
		{
			name: "dimensions",
			identity: Identity{
				Provider: "openai", Model: "model", ModelFingerprint: "revision", BackendFingerprint: "backend",
			},
			want: "dimensions",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.identity.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
	if err := testIdentity().Validate(); err != nil {
		t.Fatalf("valid identity failed: %v", err)
	}
}
