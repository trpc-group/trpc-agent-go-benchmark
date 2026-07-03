//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package dataset loads SWE-Bench-Verified instance metadata.
package dataset

import (
	"encoding/json"
	"fmt"
)

// Instance is the subset of SWE-Bench instance metadata needed by the
// benchmark runner. Raw preserves any upstream fields not modeled here.
type Instance struct {
	InstanceID       string          `json:"instance_id"`
	Repo             string          `json:"repo,omitempty"`
	BaseCommit       string          `json:"base_commit,omitempty"`
	ProblemStatement string          `json:"problem_statement,omitempty"`
	Patch            string          `json:"patch,omitempty"`
	TestPatch        string          `json:"test_patch,omitempty"`
	Version          string          `json:"version,omitempty"`
	FailToPass       StringList      `json:"FAIL_TO_PASS,omitempty"`
	PassToPass       StringList      `json:"PASS_TO_PASS,omitempty"`
	Raw              json.RawMessage `json:"-"`
}

// StringList accepts both a JSON string containing an encoded list and a
// normal JSON array. Hugging Face exports SWE-Bench test lists as strings.
type StringList []string

// UnmarshalJSON implements json.Unmarshaler.
func (s *StringList) UnmarshalJSON(data []byte) error {
	var items []string
	if err := json.Unmarshal(data, &items); err == nil {
		*s = items
		return nil
	}
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	if encoded == "" {
		*s = nil
		return nil
	}
	if err := json.Unmarshal([]byte(encoded), &items); err != nil {
		return fmt.Errorf("decode encoded string list: %w", err)
	}
	*s = items
	return nil
}
