//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import "testing"

func TestValidateTarget(t *testing.T) {
	for _, target := range []string{targetBaseline, targetMiniGo} {
		if err := validateTarget(target); err != nil {
			t.Fatalf("validateTarget(%q) error = %v", target, err)
		}
	}
	if err := validateTarget("trpc-go"); err == nil {
		t.Fatal("validateTarget(trpc-go) succeeded before the framework lane exists")
	}
	if err := validateTarget("native"); err == nil {
		t.Fatal("validateTarget(native) succeeded after the result migration")
	}
}
