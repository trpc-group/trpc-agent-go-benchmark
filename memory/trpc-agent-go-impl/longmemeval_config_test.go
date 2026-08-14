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
	"flag"
	"testing"
)

func TestLongMemEvalHasNoBuildGranularityFlag(t *testing.T) {
	if flag.CommandLine.Lookup("lme-build-granularity") != nil {
		t.Fatal("LongMemEval build granularity must not be configurable")
	}
}

func TestValidateLMEMemoryBackends(t *testing.T) {
	tests := []struct {
		name          string
		datasetFormat string
		scenarios     string
		backends      []string
		wantErr       bool
	}{
		{
			name:          "auto uses pgvector",
			datasetFormat: lmeDatasetFormat,
			scenarios:     "auto",
			backends:      []string{"pgvector"},
		},
		{
			name:          "combined auto uses pgvector",
			datasetFormat: lmeDatasetFormat,
			scenarios:     "replay, auto",
			backends:      []string{"pgvector"},
		},
		{
			name:          "auto rejects another backend",
			datasetFormat: lmeDatasetFormat,
			scenarios:     "auto",
			backends:      []string{"mysql"},
			wantErr:       true,
		},
		{
			name:          "auto rejects multiple backends",
			datasetFormat: lmeDatasetFormat,
			scenarios:     "auto",
			backends:      []string{"pgvector", "mysql"},
			wantErr:       true,
		},
		{
			name:          "replay ignores memory backend",
			datasetFormat: lmeDatasetFormat,
			scenarios:     "replay",
			backends:      []string{"inmemory"},
		},
		{
			name:          "locomo keeps backend selection",
			datasetFormat: "locomo",
			scenarios:     "auto",
			backends:      []string{"mysql"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLMEMemoryBackends(
				test.datasetFormat,
				test.scenarios,
				test.backends,
			)
			if test.wantErr && err == nil {
				t.Fatal("validateLMEMemoryBackends() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateLMEMemoryBackends() error = %v", err)
			}
		})
	}
}
