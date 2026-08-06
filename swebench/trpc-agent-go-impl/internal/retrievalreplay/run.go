//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrReplayMismatch reports a successfully executed replay whose ranked
// outputs differ from the recorded fingerprints.
var ErrReplayMismatch = errors.New("offline retrieval replay result mismatch")

// Run dispatches the prepare and replay subcommands. Replay always uses the
// concrete benchmark-local offline engine; it never summarizes stored traces.
func Run(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: retrieval-replay <prepare|replay> [flags]")
	}
	commandArgs := append([]string{args[0] + " " + args[1]}, args[2:]...)
	switch args[1] {
	case "prepare":
		return runPrepare(commandArgs)
	case "replay":
		return runReplay(commandArgs, NewOfflineEngine())
	default:
		return fmt.Errorf("unknown retrieval-replay subcommand %q", args[1])
	}
}

// RunWithEngine executes the replay subcommand using an injected offline
// engine. It is primarily an integration seam for alternative frozen engines.
func RunWithEngine(args []string, engine Engine) error {
	if len(args) > 1 && args[1] == "replay" {
		args = append([]string{args[0] + " replay"}, args[2:]...)
	}
	return runReplay(args, engine)
}

func runReplay(args []string, engine Engine) error {
	if len(args) == 0 {
		return errors.New("retrieval-replay argv is empty")
	}
	flags := flag.NewFlagSet(filepath.Base(args[0]), flag.ContinueOnError)
	runDir := flags.String("run-dir", "", "framework-Native run output directory")
	bundlePath := flags.String("bundle", "", "portable retrieval replay bundle JSON")
	outputPath := flags.String("output", "", "deterministic replay report JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "--run-dir", value: *runDir},
		{name: "--bundle", value: *bundlePath},
		{name: "--output", value: *outputPath},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required", required.name)
		}
	}
	if err := rejectOutputInInputs(*outputPath, *bundlePath, *runDir); err != nil {
		return err
	}
	loaded, err := Load(*bundlePath, *runDir)
	if err != nil {
		return err
	}
	report, err := Replay(context.Background(), loaded, engine)
	if err != nil {
		return err
	}
	if err := writeReportAtomic(*outputPath, report); err != nil {
		return err
	}
	if report.Status == "mismatch" {
		return ErrReplayMismatch
	}
	return nil
}

func rejectOutputInInputs(output, bundle, runDir string) error {
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	absBundle, err := filepath.Abs(bundle)
	if err != nil {
		return fmt.Errorf("resolve bundle path: %w", err)
	}
	absRun, err := filepath.Abs(runDir)
	if err != nil {
		return fmt.Errorf("resolve run directory: %w", err)
	}
	absOutput = filepath.Clean(absOutput)
	absBundle = filepath.Clean(absBundle)
	absRun = filepath.Clean(absRun)
	if absOutput == absBundle {
		return errors.New("output path cannot overwrite the replay bundle")
	}
	for _, input := range []struct {
		label string
		root  string
	}{
		{label: "bundle directory", root: filepath.Dir(absBundle)},
		{label: "Native run directory", root: absRun},
	} {
		rel, err := filepath.Rel(input.root, absOutput)
		if err != nil {
			return fmt.Errorf("compare output with %s: %w", input.label, err)
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return fmt.Errorf("output path cannot be inside the immutable %s", input.label)
		}
	}
	return nil
}

func writeReportAtomic(output string, report *Report) error {
	if report == nil {
		return errors.New("report is required")
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal replay report: %w", err)
	}
	payload = append(payload, '\n')
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve report output: %w", err)
	}
	parent := filepath.Dir(absOutput)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect report directory: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return errors.New("report directory must be a real directory")
	}
	temporary, err := os.CreateTemp(parent, ".retrieval-replay-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary report: %w", err)
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod temporary report: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		return fmt.Errorf("write temporary report: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary report: %w", err)
	}
	if info, err := os.Lstat(absOutput); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("report output cannot replace a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect report output: %w", err)
	}
	if err := os.Rename(temporaryName, absOutput); err != nil {
		return fmt.Errorf("publish replay report: %w", err)
	}
	keep = true
	return nil
}
