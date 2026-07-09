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
	"os"
	"path/filepath"
	"testing"
)

func TestValidateExpectedCaseIDsAndHash(t *testing.T) {
	cases := []caseManifest{
		{InstanceID: "b"},
		{InstanceID: "a"},
	}
	dir := t.TempDir()
	idsPath := filepath.Join(dir, "ids.txt")
	hashPath := filepath.Join(dir, "ids.sha256")
	if err := os.WriteFile(idsPath, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hashPath, []byte(caseListHash(cases)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateExpectedCaseIDs(idsPath, cases); err != nil {
		t.Fatalf("validateExpectedCaseIDs() error = %v", err)
	}
	if err := validateExpectedCaseHash(hashPath, caseListHash(cases)); err != nil {
		t.Fatalf("validateExpectedCaseHash() error = %v", err)
	}
}
