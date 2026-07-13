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
	"log"
	"os"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/runner"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := runner.Run(os.Args); err != nil {
		log.Fatalf("%v", err)
	}
}
