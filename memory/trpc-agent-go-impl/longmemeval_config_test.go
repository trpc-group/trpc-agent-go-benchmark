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
