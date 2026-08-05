//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package contract

// Case is the safe SWE-Bench case manifest visible to runners.
type Case struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo,omitempty"`
	BaseCommit       string `json:"base_commit,omitempty"`
	ProblemStatement string `json:"problem_statement,omitempty"`
	HintsText        string `json:"hints_text,omitempty"`
}
