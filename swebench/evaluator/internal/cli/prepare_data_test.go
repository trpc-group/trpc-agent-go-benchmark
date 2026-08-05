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
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

func TestValidateExpectedCaseIDsAndHash(t *testing.T) {
	cases := []contract.Case{
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

func TestLoadExpectedCaseFilesUsesEmbeddedVerifiedPanel(t *testing.T) {
	ids, hash, err := loadExpectedCaseFiles(defaultDatasetName, defaultSplit, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ids.Source, "embedded:") || len(nonEmptyLines(string(ids.Data))) != 500 {
		t.Fatalf("embedded ids = source %q, lines %d", ids.Source, len(nonEmptyLines(string(ids.Data))))
	}
	if !strings.HasPrefix(hash.Source, "embedded:") || len(strings.TrimSpace(string(hash.Data))) != 64 {
		t.Fatalf("embedded hash = source %q, value %q", hash.Source, hash.Data)
	}
}

func TestValidateExpectedCaseIDsRejectsUnsortedAndDuplicateLists(t *testing.T) {
	cases := []contract.Case{{InstanceID: "a"}, {InstanceID: "b"}}
	for name, data := range map[string]string{
		"unsorted":  "b\na\n",
		"duplicate": "a\na\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateExpectedCaseIDsData(name, []byte(data), cases); err == nil {
				t.Fatalf("validateExpectedCaseIDsData() accepted %s list", name)
			}
		})
	}
}
