//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Command offline-httpbin-certs creates fixture-only TLS material for the
// closed-world SWE-Bench offline asset bundle.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

func main() {
	output := flag.String("output", "", "directory for the generated fixture certificates")
	flag.Parse()
	if flag.NArg() != 0 || strings.TrimSpace(*output) == "" {
		fmt.Fprintln(os.Stderr, "usage: offline-httpbin-certs -output DIRECTORY")
		os.Exit(2)
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output directory: %v\n", err)
		os.Exit(1)
	}
	if _, err := sweenv.GenerateOfflineHTTPBinCerts(*output); err != nil {
		fmt.Fprintf(os.Stderr, "generate offline httpbin certificates: %v\n", err)
		os.Exit(1)
	}
}
