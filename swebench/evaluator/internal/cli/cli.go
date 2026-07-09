//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package cli provides SWE-Bench Verified benchmark orchestration commands.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
)

// Run dispatches the SWE-Bench evaluator command line.
func Run(args []string) error {
	if len(args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx := context.Background()
	switch args[1] {
	case "doctor":
		return runDoctor(ctx, args[2:])
	case "prepare-data":
		return runPrepareData(ctx, args[2:])
	case "run-mini":
		return runMini(ctx, args[2:])
	case "verify":
		return runVerify(ctx, args[2:])
	case "import":
		return runImport(args[2:])
	case "run-config":
		return runRunConfig(args[2:])
	case "plan-batches":
		return runPlanBatches(args[2:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[1])
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  swebench doctor   [flags]
  swebench prepare-data [flags]
  swebench run-mini [flags]
  swebench verify   [flags]
  swebench import   [flags]
  swebench run-config [flags]
  swebench plan-batches [flags]

Commands:
  doctor    Probe local benchmark environment and model endpoint.
  prepare-data  Download/load SWE-Bench Verified and generate safe metadata.
  run-mini  Run mini-SWE-agent batch runner for a filter/slice.
  verify    Run SWE-Bench official local harness for predictions.
  import    Normalize mini predictions, trajectories, and harness report.
  run-config  Write a run-level manifest from generated artifacts.
  plan-batches  Create fixed case batches and mini-SWE-agent filters.

`)
}

func required(fs *flag.FlagSet, name, value string) error {
	if value != "" {
		return nil
	}
	return fmt.Errorf("missing required flag -%s", name)
}
