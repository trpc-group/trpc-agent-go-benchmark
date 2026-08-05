//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package data exposes frozen benchmark-panel metadata bundled with the CLI.
package data

import _ "embed"

var (
	//go:embed case-lists/verified-test-500.case_ids.txt
	verifiedTest500CaseIDs []byte

	//go:embed case-lists/verified-test-500.case_ids.sha256
	verifiedTest500CaseHash []byte
)

// VerifiedTest500CaseIDs returns a copy of the frozen Verified/test case list.
func VerifiedTest500CaseIDs() []byte {
	return append([]byte(nil), verifiedTest500CaseIDs...)
}

// VerifiedTest500CaseHash returns a copy of the frozen case-list hash.
func VerifiedTest500CaseHash() []byte {
	return append([]byte(nil), verifiedTest500CaseHash...)
}
