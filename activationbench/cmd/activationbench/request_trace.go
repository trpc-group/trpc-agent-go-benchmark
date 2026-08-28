//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/runner"
)

// requestTraceWriter persists one JSON object per before-model callback. It is
// intentionally opt-in: serializing complete conversation messages is useful
// for diagnosing a provider run, but would add I/O and potentially sensitive
// prompt content to an ordinary benchmark invocation.
type requestTraceWriter struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
	err  error
}

func newRequestTraceWriter(path string) (*requestTraceWriter, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	return &requestTraceWriter{file: file, enc: encoder}, nil
}

func (w *requestTraceWriter) Observe(trace runner.ModelRequestTrace) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return
	}
	if err := w.enc.Encode(trace); err != nil {
		w.err = err
	}
}

func (w *requestTraceWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return w.err
	}
	file := w.file
	w.file = nil
	closeErr := file.Close()
	if w.err != nil {
		return w.err
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}
