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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type batchPlan struct {
	GeneratedAt time.Time       `json:"generated_at"`
	CasesPath   string          `json:"cases_path"`
	OutputDir   string          `json:"output_dir"`
	RunPrefix   string          `json:"run_prefix"`
	BatchSize   int             `json:"batch_size"`
	CaseCount   int             `json:"case_count"`
	BatchCount  int             `json:"batch_count"`
	Batches     []batchPlanItem `json:"batches"`
}

type batchPlanItem struct {
	Index       int      `json:"index"`
	Name        string   `json:"name"`
	RunID       string   `json:"run_id"`
	Size        int      `json:"size"`
	InstanceIDs []string `json:"instance_ids"`
	Filter      string   `json:"filter"`
	JSONPath    string   `json:"json_path"`
	FilterPath  string   `json:"filter_path"`
}

func runPlanBatches(args []string) error {
	fs := flag.NewFlagSet("plan-batches", flag.ExitOnError)
	casesPath := fs.String("cases", "", "canonical cases.jsonl path")
	outputDir := fs.String("output-dir", "../data/batches", "output directory for plan and batch files")
	runPrefix := fs.String("run-prefix", "baseline-full", "run id prefix")
	batchSize := fs.Int("batch-size", 20, "cases per batch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "cases", *casesPath); err != nil {
		return err
	}
	if *batchSize < 1 {
		return fmt.Errorf("batch-size must be >= 1")
	}
	if err := ensureDir(*outputDir); err != nil {
		return err
	}

	cases, err := readCases(*casesPath, nil)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("empty cases list")
	}

	plan := batchPlan{
		GeneratedAt: time.Now().UTC(),
		CasesPath:   absPath(*casesPath),
		OutputDir:   absPath(*outputDir),
		RunPrefix:   *runPrefix,
		BatchSize:   *batchSize,
		CaseCount:   len(cases),
	}
	for start, index := 0, 0; start < len(cases); start, index = start+*batchSize, index+1 {
		end := start + *batchSize
		if end > len(cases) {
			end = len(cases)
		}
		ids := make([]string, 0, end-start)
		for _, c := range cases[start:end] {
			ids = append(ids, c.InstanceID)
		}
		name := fmt.Sprintf("batch-%03d", index)
		filter := instanceFilter(ids)
		item := batchPlanItem{
			Index:       index,
			Name:        name,
			RunID:       fmt.Sprintf("%s-%03d", strings.TrimRight(*runPrefix, "-"), index),
			Size:        len(ids),
			InstanceIDs: ids,
			Filter:      filter,
			JSONPath:    absPath(filepath.Join(*outputDir, name+".json")),
			FilterPath:  absPath(filepath.Join(*outputDir, name+".filter")),
		}
		if err := writeJSON(filepath.Join(*outputDir, name+".json"), item); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*outputDir, name+".filter"), []byte(filter+"\n"), 0o644); err != nil {
			return err
		}
		plan.Batches = append(plan.Batches, item)
	}
	plan.BatchCount = len(plan.Batches)
	if err := writeJSON(filepath.Join(*outputDir, "plan.json"), plan); err != nil {
		return err
	}
	fmt.Printf("wrote %d batches for %d cases\nplan=%s\n", plan.BatchCount, plan.CaseCount, filepath.Join(*outputDir, "plan.json"))
	return nil
}

func instanceFilter(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, regexp.QuoteMeta(id))
	}
	return "^(" + strings.Join(parts, "|") + ")$"
}
