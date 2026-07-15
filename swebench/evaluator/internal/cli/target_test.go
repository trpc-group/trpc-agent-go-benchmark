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
	for _, target := range []string{targetBaseline, targetMiniGo, targetTAG} {
		if err := validateTarget(target); err != nil {
			t.Fatalf("validateTarget(%q) error = %v", target, err)
		}
	}
	if err := validateTarget("trpc-go"); err == nil {
		t.Fatal("validateTarget(trpc-go) succeeded; TAG is the required target name")
	}
	if err := validateTarget("native"); err == nil {
		t.Fatal("validateTarget(native) succeeded after the result migration")
	}
}
