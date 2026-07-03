//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package report generates SWE-Bench comparison reports.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
)

// RunRequest configures report generation.
type RunRequest struct {
	RunID          string
	MiniRunDir     string
	NativeRunDir   string
	MiniVerifier   string
	NativeVerifier string
	OutputDir      string
}

// Run generates comparison artifacts.
func Run(req RunRequest) error {
	runID := req.RunID
	if runID == "" {
		runID = "comparison-" + time.Now().UTC().Format("20060102T150405Z")
	}
	outDir, err := result.EnsureDir(result.RunDir(req.OutputDir, runID))
	if err != nil {
		return err
	}
	miniCases, err := readCases(firstNonEmpty(req.MiniVerifier, req.MiniRunDir))
	if err != nil {
		return fmt.Errorf("read mini cases: %w", err)
	}
	nativeCases, err := readCases(firstNonEmpty(req.NativeVerifier, req.NativeRunDir))
	if err != nil {
		return fmt.Errorf("read native cases: %w", err)
	}
	comp := buildComparison(runID, miniCases, nativeCases)
	if err := result.WriteJSON(filepath.Join(outDir, "comparison.json"), comp); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "comparison.md"), []byte(renderMarkdown(comp)), 0644)
}

func readCases(dir string) ([]result.CaseResult, error) {
	if dir == "" {
		return nil, fmt.Errorf("case directory is required")
	}
	return result.ReadCasesJSONL(filepath.Join(dir, "cases.jsonl"))
}

func buildComparison(runID string, miniCases, nativeCases []result.CaseResult) result.Comparison {
	mini := result.Summarize(runID, string(result.RunnerMini), miniCases)
	native := result.Summarize(runID, string(result.RunnerNative), nativeCases)
	ids := unionIDs(miniCases, nativeCases)
	return result.Comparison{
		RunID:  runID,
		Mini:   mini,
		Native: native,
		Delta: result.DeltaSummary{
			Resolved:     native.Resolved - mini.Resolved,
			ResolvedRate: native.ResolvedRate - mini.ResolvedRate,
			TotalTokens:  native.TotalTokens - mini.TotalTokens,
			DurationMs:   native.DurationMs - mini.DurationMs,
		},
		CaseIDs: ids,
	}
}

func renderMarkdown(comp result.Comparison) string {
	var b strings.Builder
	b.WriteString("# SWE-Bench-Verified Comparison\n\n")
	b.WriteString("Resolved statuses should come from the official SWE-Bench verifier. ")
	b.WriteString("The current primary verifier is the official local harness; `sb-cli` reports are optional cross-checks while the hosted API is unstable. ")
	b.WriteString("Dry-run or imported reports are not final benchmark claims.\n\n")
	b.WriteString("| Metric | mini-SWE-agent | Native | Delta |\n")
	b.WriteString("| --- | ---: | ---: | ---: |\n")
	b.WriteString(fmt.Sprintf("| Total cases | %d | %d | - |\n", comp.Mini.Total, comp.Native.Total))
	b.WriteString(fmt.Sprintf("| Resolved | %d | %d | %+d |\n", comp.Mini.Resolved, comp.Native.Resolved, comp.Delta.Resolved))
	b.WriteString(fmt.Sprintf("| Resolved rate | %.2f%% | %.2f%% | %+.2fpp |\n",
		comp.Mini.ResolvedRate*100,
		comp.Native.ResolvedRate*100,
		comp.Delta.ResolvedRate*100,
	))
	b.WriteString(fmt.Sprintf("| Total tokens | %d | %d | %+d |\n", comp.Mini.TotalTokens, comp.Native.TotalTokens, comp.Delta.TotalTokens))
	b.WriteString(fmt.Sprintf("| Duration ms | %d | %d | %+d |\n", comp.Mini.DurationMs, comp.Native.DurationMs, comp.Delta.DurationMs))
	b.WriteString("\n")
	return b.String()
}

func unionIDs(a, b []result.CaseResult) []string {
	seen := map[string]bool{}
	for _, c := range a {
		seen[c.InstanceID] = true
	}
	for _, c := range b {
		seen[c.InstanceID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
