//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//
//

package dataset

import (
	"testing"
)

func TestLongMemEvalTurnID(t *testing.T) {
	if got := LongMemEvalTurnID("answer_s1", 0, true); got != "answer_s1_1" {
		t.Fatalf("LongMemEvalTurnID(answer) = %q", got)
	}
	if got := LongMemEvalTurnID("answer_s1", 2, false); got != "noans_s1_3" {
		t.Fatalf("LongMemEvalTurnID(no answer) = %q", got)
	}
}
