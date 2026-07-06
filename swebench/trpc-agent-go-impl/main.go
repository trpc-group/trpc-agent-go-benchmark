//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package main provides SWE-Bench Verified benchmark orchestration commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "doctor":
		err = runDoctor(ctx, os.Args[2:])
	case "prepare-data":
		err = runPrepareData(ctx, os.Args[2:])
	case "run-mini":
		err = runMini(ctx, os.Args[2:])
	case "verify":
		err = runVerify(ctx, os.Args[2:])
	case "import":
		err = runImport(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatalf("%v", err)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  swebench doctor   [flags]
  swebench prepare-data [flags]
  swebench run-mini [flags]
  swebench verify   [flags]
  swebench import   [flags]

Commands:
  doctor    Probe local benchmark environment and model endpoint.
  prepare-data  Generate safe SWE-Bench case manifest and case-list hash.
  run-mini  Run mini-SWE-agent batch runner for a filter/slice.
  verify    Run SWE-Bench official local harness for predictions.
  import    Normalize mini predictions, trajectories, and harness report.

`)
}

func required(fs *flag.FlagSet, name, value string) error {
	if value != "" {
		return nil
	}
	return fmt.Errorf("missing required flag -%s", name)
}
