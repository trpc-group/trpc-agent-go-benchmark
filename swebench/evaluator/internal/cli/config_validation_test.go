//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"strings"
	"testing"
)

func TestValidateTargetLabel(t *testing.T) {
	for _, target := range []string{"baseline", "native", "mini-go", "tag", "rag-ast", "loop-warn"} {
		if err := validateTargetLabel(target); err != nil {
			t.Errorf("validateTargetLabel(%q) error = %v", target, err)
		}
	}
	for _, target := range []string{"", "A", " a ", ".", "..", "a/b", `a\b`, "-a", "a-", "a--b", strings.Repeat("a", 129)} {
		if err := validateTargetLabel(target); err == nil {
			t.Errorf("validateTargetLabel(%q) unexpectedly succeeded", target)
		}
	}
}

func TestValidateArtifactName(t *testing.T) {
	for _, value := range []string{"run-1", "case__id-1", "Run_1.2"} {
		if err := validateArtifactName("run id", value); err != nil {
			t.Errorf("validateArtifactName(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "..", " a ", "a/b", `a\b`, strings.Repeat("a", 129)} {
		if err := validateArtifactName("run id", value); err == nil {
			t.Errorf("validateArtifactName(%q) unexpectedly succeeded", value)
		}
	}
}
