//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package contract

// HarnessIndex is the subset of official harness results used by importers.
type HarnessIndex struct {
	Resolved   map[string]bool
	Unresolved map[string]bool
	Errors     map[string]bool
	Completed  map[string]bool
}

// NewHarnessIndex returns an initialized harness result index.
func NewHarnessIndex() HarnessIndex {
	return HarnessIndex{
		Resolved:   map[string]bool{},
		Unresolved: map[string]bool{},
		Errors:     map[string]bool{},
		Completed:  map[string]bool{},
	}
}
