//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Command longmemeval-manifest creates and verifies reproducible case manifests.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
)

const defaultQuestionTypes = "multi-session,temporal-reasoning,knowledge-update," +
	"single-session-user,single-session-assistant,single-session-preference"

type commandOptions struct {
	action          string
	datasetPath     string
	outputPath      string
	method          string
	seed            string
	questionTypes   string
	totalSize       int
	quotas          string
	perType         int
	devOutput       string
	holdoutOutput   string
	devSize         int
	holdoutSize     int
	devQuotas       string
	holdoutQuotas   string
	manifestPath    string
	holdoutManifest string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	opts, err := parseCommandOptions(args)
	if err != nil {
		return err
	}
	if opts.datasetPath == "" {
		return errors.New("-dataset is required")
	}
	instances, err := dataset.LoadLongMemEval(opts.datasetPath)
	if err != nil {
		return fmt.Errorf("load dataset: %w", err)
	}
	switch opts.action {
	case "generate":
		return runGenerate(instances, opts, stdout)
	case "split":
		return runSplit(instances, opts, stdout)
	case "verify":
		return runVerify(instances, opts, stdout)
	default:
		return fmt.Errorf("unsupported -action %q; use generate, split, or verify", opts.action)
	}
}

func runGenerate(
	instances []*dataset.LongMemEvalInstance,
	opts commandOptions,
	stdout io.Writer,
) error {
	if opts.outputPath == "" {
		return errors.New("-output is required for generate")
	}
	if opts.perType < 0 {
		return errors.New("-per-type must not be negative")
	}
	if opts.devOutput != "" || opts.holdoutOutput != "" || opts.devSize != 0 ||
		opts.holdoutSize != 0 || opts.devQuotas != "" || opts.holdoutQuotas != "" {
		return errors.New("split output and allocation flags cannot be used with generate")
	}
	if opts.manifestPath != "" || opts.holdoutManifest != "" {
		return errors.New("verification flags cannot be used with generate")
	}
	questionTypes := parseQuestionTypes(opts.questionTypes)
	quotas, err := parseQuotas(opts.quotas)
	if err != nil {
		return fmt.Errorf("parse -quotas: %w", err)
	}
	if opts.perType > 0 {
		if opts.totalSize > 0 || len(quotas) > 0 {
			return errors.New("-per-type cannot be combined with -total-size or -quotas")
		}
		quotas = equalQuotas(questionTypes, opts.perType)
	}
	manifest, err := dataset.BuildLongMemEvalManifest(
		instances,
		dataset.LongMemEvalManifestSelection{
			Method:        dataset.LongMemEvalManifestMethod(opts.method),
			Seed:          opts.seed,
			QuestionTypes: questionTypes,
			TotalSize:     opts.totalSize,
			Quotas:        quotas,
		},
	)
	if err != nil {
		return fmt.Errorf("build LongMemEval manifest: %w", err)
	}
	if err := dataset.WriteLongMemEvalManifest(opts.outputPath, manifest); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d cases to %s (%s)\n", len(manifest.CaseIDs), opts.outputPath, manifest.ManifestDigest)
	return nil
}

func runSplit(
	instances []*dataset.LongMemEvalInstance,
	opts commandOptions,
	stdout io.Writer,
) error {
	if dataset.LongMemEvalManifestMethod(opts.method) != dataset.LongMemEvalManifestMethodStratifiedSHA256 {
		return errors.New("split action requires -method stratified-sha256")
	}
	if opts.outputPath != "" || opts.totalSize != 0 || opts.quotas != "" || opts.perType != 0 {
		return errors.New("generate output and allocation flags cannot be used with split")
	}
	if opts.manifestPath != "" || opts.holdoutManifest != "" {
		return errors.New("verification flags cannot be used with split")
	}
	if opts.devOutput == "" || opts.holdoutOutput == "" {
		return errors.New("-dev-output and -holdout-output are required for split")
	}
	sameOutput, err := sameManifestPath(opts.devOutput, opts.holdoutOutput)
	if err != nil {
		return err
	}
	if sameOutput {
		return errors.New("-dev-output and -holdout-output must be different files")
	}
	devQuotas, err := parseQuotas(opts.devQuotas)
	if err != nil {
		return fmt.Errorf("parse -dev-quotas: %w", err)
	}
	holdoutQuotas, err := parseQuotas(opts.holdoutQuotas)
	if err != nil {
		return fmt.Errorf("parse -holdout-quotas: %w", err)
	}
	dev, holdout, err := dataset.BuildLongMemEvalManifestSplit(
		instances,
		dataset.LongMemEvalManifestSplitSelection{
			Seed:          opts.seed,
			QuestionTypes: parseQuestionTypes(opts.questionTypes),
			Dev: dataset.LongMemEvalManifestAllocation{
				TotalSize: opts.devSize,
				Quotas:    devQuotas,
			},
			Holdout: dataset.LongMemEvalManifestAllocation{
				TotalSize: opts.holdoutSize,
				Quotas:    holdoutQuotas,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("build LongMemEval manifest split: %w", err)
	}
	if err := dataset.WriteLongMemEvalManifest(opts.devOutput, dev); err != nil {
		return err
	}
	if err := dataset.WriteLongMemEvalManifest(opts.holdoutOutput, holdout); err != nil {
		return err
	}
	fmt.Fprintf(
		stdout,
		"wrote dev=%d cases to %s and holdout=%d cases to %s\n",
		len(dev.CaseIDs),
		opts.devOutput,
		len(holdout.CaseIDs),
		opts.holdoutOutput,
	)
	return nil
}

func runVerify(
	instances []*dataset.LongMemEvalInstance,
	opts commandOptions,
	stdout io.Writer,
) error {
	if opts.manifestPath == "" {
		return errors.New("-manifest is required for verify")
	}
	if opts.outputPath != "" || opts.seed != "" || opts.totalSize != 0 || opts.quotas != "" ||
		opts.perType != 0 || opts.devOutput != "" || opts.holdoutOutput != "" || opts.devSize != 0 ||
		opts.holdoutSize != 0 || opts.devQuotas != "" || opts.holdoutQuotas != "" {
		return errors.New("generation and split flags cannot be used with verify")
	}
	manifest, err := dataset.LoadLongMemEvalManifest(opts.manifestPath)
	if err != nil {
		return err
	}
	if opts.holdoutManifest == "" {
		if err := dataset.VerifyLongMemEvalManifest(instances, manifest); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "verified %s (%d cases)\n", opts.manifestPath, len(manifest.CaseIDs))
		return nil
	}
	holdout, err := dataset.LoadLongMemEvalManifest(opts.holdoutManifest)
	if err != nil {
		return err
	}
	if err := dataset.VerifyLongMemEvalManifestSplit(instances, manifest, holdout); err != nil {
		return err
	}
	fmt.Fprintf(
		stdout,
		"verified dev=%s (%d cases) and holdout=%s (%d cases)\n",
		opts.manifestPath,
		len(manifest.CaseIDs),
		opts.holdoutManifest,
		len(holdout.CaseIDs),
	)
	return nil
}

func parseCommandOptions(args []string) (commandOptions, error) {
	var opts commandOptions
	flags := flag.NewFlagSet("longmemeval-manifest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.action, "action", "generate", "Action: generate, split, or verify")
	flags.StringVar(&opts.datasetPath, "dataset", "", "LongMemEval dataset JSON path")
	flags.StringVar(&opts.outputPath, "output", "", "Generated manifest JSON path")
	flags.StringVar(
		&opts.method,
		"method",
		string(dataset.LongMemEvalManifestMethodStratifiedSHA256),
		"Selection method: stratified-sha256 or full-category",
	)
	flags.StringVar(&opts.seed, "seed", "", "Explicit seed for SHA-256 selection")
	flags.StringVar(&opts.questionTypes, "types", defaultQuestionTypes, "Comma-separated question types")
	flags.IntVar(&opts.totalSize, "total-size", 0, "Total selected cases, allocated by largest remainder")
	flags.StringVar(&opts.quotas, "quotas", "", "Per-type quotas as type=count,type=count")
	flags.IntVar(&opts.perType, "per-type", 0, "Equal per-type quota")
	flags.StringVar(&opts.devOutput, "dev-output", "", "Development manifest output path")
	flags.StringVar(&opts.holdoutOutput, "holdout-output", "", "Holdout manifest output path")
	flags.IntVar(&opts.devSize, "dev-size", 0, "Development total size")
	flags.IntVar(&opts.holdoutSize, "holdout-size", 0, "Holdout total size")
	flags.StringVar(&opts.devQuotas, "dev-quotas", "", "Development per-type quotas")
	flags.StringVar(&opts.holdoutQuotas, "holdout-quotas", "", "Holdout per-type quotas")
	flags.StringVar(&opts.manifestPath, "manifest", "", "Manifest to verify, or development manifest in pair verification")
	flags.StringVar(&opts.holdoutManifest, "holdout-manifest", "", "Holdout manifest for pair verification")
	if err := flags.Parse(args); err != nil {
		return commandOptions{}, fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return opts, nil
}

func sameManifestPath(first string, second string) (bool, error) {
	firstPath, err := filepath.Abs(filepath.Clean(first))
	if err != nil {
		return false, fmt.Errorf("resolve development manifest path: %w", err)
	}
	secondPath, err := filepath.Abs(filepath.Clean(second))
	if err != nil {
		return false, fmt.Errorf("resolve holdout manifest path: %w", err)
	}
	if firstPath == secondPath {
		return true, nil
	}
	firstInfo, firstErr := os.Stat(firstPath)
	secondInfo, secondErr := os.Stat(secondPath)
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo), nil
}

func parseQuestionTypes(raw string) []string {
	var questionTypes []string
	for _, part := range strings.Split(raw, ",") {
		questionType := strings.TrimSpace(part)
		if questionType != "" {
			questionTypes = append(questionTypes, questionType)
		}
	}
	return questionTypes
}

func parseQuotas(raw string) (map[string]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	quotas := make(map[string]int)
	for _, part := range strings.Split(raw, ",") {
		fields := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(fields) != 2 || strings.TrimSpace(fields[0]) == "" {
			return nil, fmt.Errorf("invalid quota %q; want question-type=count", part)
		}
		questionType := strings.TrimSpace(fields[0])
		if _, ok := quotas[questionType]; ok {
			return nil, fmt.Errorf("duplicate quota for %q", questionType)
		}
		quota, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("parse quota for %q: %w", questionType, err)
		}
		if quota < 0 {
			return nil, fmt.Errorf("quota for %q must not be negative", questionType)
		}
		quotas[questionType] = quota
	}
	return quotas, nil
}

func equalQuotas(questionTypes []string, perType int) map[string]int {
	quotas := make(map[string]int, len(questionTypes))
	for _, questionType := range questionTypes {
		quotas[questionType] = perType
	}
	return quotas
}
