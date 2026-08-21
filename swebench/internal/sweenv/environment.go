//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package sweenv defines the execution boundary for SWE-Bench testbeds.
package sweenv

import (
	"context"
	"errors"
)

// CommandResult is the stable command result passed between an environment
// and a model-facing observation codec.
type CommandResult struct {
	Output        string `json:"output"`
	ReturnCode    int    `json:"returncode"`
	ExceptionInfo string `json:"exception_info,omitempty"`
	TimedOut      bool   `json:"timed_out,omitempty"`
}

// Environment executes commands inside one isolated SWE-Bench testbed.
type Environment interface {
	Execute(ctx context.Context, command string) CommandResult
	Close(ctx context.Context) error
}

// WorkspaceSnapshotter is implemented by environments that can materialize
// the immutable task-start workspace into an empty host directory. Callers
// must treat the snapshot as read-only: it is a retrieval input, not a second
// execution workspace.
type WorkspaceSnapshotter interface {
	SnapshotWorkspace(ctx context.Context, destination string) error
}

// CaseSpec is the minimum case identity required to enforce a clean-room
// boundary before an agent sees a testbed.
type CaseSpec struct {
	InstanceID string
	Repo       string
	BaseCommit string
}

// ImageIdentity binds a Docker reference used by a testbed to the immutable
// local image ID that was inspected before the container started.
type ImageIdentity struct {
	Reference string `json:"reference"`
	ID        string `json:"id"`
}

// Provenance records container inputs that are relevant to clean-room runs.
// AuxiliaryImages is keyed by the fixture role, for example "httpbin".
type Provenance struct {
	Testbed         ImageIdentity            `json:"testbed"`
	AuxiliaryImages map[string]ImageIdentity `json:"auxiliary_images,omitempty"`
}

// ProvenanceProvider is implemented by environments that can attest the
// immutable images used to construct a case testbed.
type ProvenanceProvider interface {
	Provenance() Provenance
}

// Factory starts one isolated environment per SWE-Bench instance.
type Factory interface {
	Start(ctx context.Context, instanceID string) (Environment, error)
}

// CaseFactory starts an environment using the complete immutable case
// identity. Native runners use this interface so clean-room validation cannot
// lose the expected base commit at the environment boundary.
type CaseFactory interface {
	StartCase(ctx context.Context, spec CaseSpec) (Environment, error)
}

type retryableStartError struct {
	err error
}

func (e *retryableStartError) Error() string { return e.err.Error() }
func (e *retryableStartError) Unwrap() error { return e.err }

// MarkStartErrorRetryable marks a transient CaseFactory startup error without
// changing the CaseFactory interface or losing the original error chain.
func MarkStartErrorRetryable(err error) error {
	if err == nil || IsStartErrorRetryable(err) {
		return err
	}
	return &retryableStartError{err: err}
}

// IsStartErrorRetryable reports whether a CaseFactory explicitly classified a
// startup error as transient. Unmarked startup errors fail closed.
func IsStartErrorRetryable(err error) bool {
	var retryable *retryableStartError
	return errors.As(err, &retryable)
}
