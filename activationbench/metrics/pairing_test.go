//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package metrics

import (
	"strings"
	"testing"
)

func TestValidatePairedResultsAcceptsAnnotatedResults(t *testing.T) {
	static := AnnotateRepetition([]RunResult{{TaskID: "a"}, {TaskID: "b"}}, 3)
	dynamic := AnnotateRepetition([]RunResult{{TaskID: "a"}, {TaskID: "b"}}, 3)
	if err := ValidatePairedResults(static, dynamic); err != nil {
		t.Fatalf("ValidatePairedResults: %v", err)
	}
	if _, err := NewCheckedComparison("suite", static, dynamic); err != nil {
		t.Fatalf("NewCheckedComparison: %v", err)
	}
}

func TestValidatePairedResultsRejectsLengthOrderAndDuplicateMismatches(t *testing.T) {
	tests := []struct {
		name   string
		static []RunResult
		dyn    []RunResult
		want   string
	}{
		{
			name:   "length",
			static: []RunResult{{TaskID: "a"}},
			dyn:    []RunResult{},
			want:   "length mismatch",
		},
		{
			name:   "order",
			static: []RunResult{{TaskID: "a"}, {TaskID: "b"}},
			dyn:    []RunResult{{TaskID: "b"}, {TaskID: "a"}},
			want:   "pair key mismatch",
		},
		{
			name:   "duplicate",
			static: []RunResult{{TaskID: "a"}, {TaskID: "a"}},
			dyn:    []RunResult{{TaskID: "a"}, {TaskID: "a"}},
			want:   "duplicate static pair key",
		},
		{
			name:   "missing id",
			static: []RunResult{{}},
			dyn:    []RunResult{{}},
			want:   "missing PairID and TaskID",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePairedResults(test.static, test.dyn)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidatePairedResultsRejectsMismatchedTaskIDsWithSamePairID(t *testing.T) {
	static := []RunResult{{TaskID: "static-task", PairID: "pair-1"}}
	dynamic := []RunResult{{TaskID: "dynamic-task", PairID: "pair-1"}}
	err := ValidatePairedResults(static, dynamic)
	if err == nil || !strings.Contains(err.Error(), "task id mismatch") {
		t.Fatalf("error = %v, want task id mismatch", err)
	}
}

func TestValidatePairedResultsRejectsDuplicatePairIDs(t *testing.T) {
	static := []RunResult{
		{TaskID: "a", PairID: "pair-1"},
		{TaskID: "b", PairID: "pair-1"},
	}
	dynamic := []RunResult{
		{TaskID: "a", PairID: "pair-1"},
		{TaskID: "b", PairID: "pair-2"},
	}
	err := ValidatePairedResults(static, dynamic)
	if err == nil || !strings.Contains(err.Error(), "duplicate static pair key") {
		t.Fatalf("error = %v, want duplicate static pair key", err)
	}
}

func TestValidatePairedResultsRejectsReversedBenchmarkModes(t *testing.T) {
	static := []RunResult{{TaskID: "a", Mode: "dynamic-activation"}}
	dynamic := []RunResult{{TaskID: "a", Mode: "static-all"}}
	err := ValidatePairedResults(static, dynamic)
	if err == nil || !strings.Contains(err.Error(), "unexpected benchmark mode pair") {
		t.Fatalf("error = %v, want reversed-mode error", err)
	}
}

func TestValidatePairedResultsRejectsSameBenchmarkMode(t *testing.T) {
	static := []RunResult{{TaskID: "a", Mode: "static-all"}}
	dynamic := []RunResult{{TaskID: "a", Mode: "static-all"}}
	err := ValidatePairedResults(static, dynamic)
	if err == nil || !strings.Contains(err.Error(), "both paired results use mode") {
		t.Fatalf("error = %v, want same-mode error", err)
	}
}
