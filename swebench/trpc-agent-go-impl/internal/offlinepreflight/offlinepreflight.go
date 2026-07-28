//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package offlinepreflight validates generation testbeds without invoking a
// model. It exercises the same Docker startup, Git sanitation, offline service,
// and cleanup paths as the TAG runner.
package offlinepreflight

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

const isolationCanary = `set -euo pipefail
cd /testbed
test -z "$(git status --porcelain)"
test "$(git for-each-ref --format='%(refname)' | wc -l)" -eq 1
test -z "$(git remote)"
test -z "$(git tag --list)"
git rev-parse --verify HEAD >/dev/null
git log -1 --oneline >/dev/null
git diff --quiet
python - <<'PY'
import socket

try:
    socket.getaddrinfo("example.com", 443)
except OSError:
    pass
else:
    raise SystemExit("public DNS unexpectedly resolved example.com")

try:
    connection = socket.create_connection(("1.1.1.1", 443), timeout=1)
except OSError:
    pass
else:
    connection.close()
    raise SystemExit("raw-IP public egress unexpectedly succeeded")
PY`

// Run executes the offline preflight CLI.
func Run(args []string) error {
	fs := flag.NewFlagSet("offline-preflight", flag.ContinueOnError)
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases.jsonl")
	caseListPath := fs.String("case-list", "", "optional newline-delimited exact instance ids")
	filterValue := fs.String("filter", "", "optional instance id regexp")
	environmentConfigPath := fs.String(
		"environment-config",
		"config/environments/swebench-testbed.yaml",
		"environment YAML path",
	)
	offlineAssetsDir := fs.String(
		"offline-assets-dir",
		"",
		"host directory prepared by scripts/prepare-offline-assets.sh",
	)
	workers := fs.Int("workers", 1, "parallel preflight testbeds")
	commandTimeout := fs.Duration("command-timeout", 15*time.Second, "timeout for each canary command")
	caseTimeout := fs.Duration("case-timeout", 10*time.Minute, "timeout for each preflight case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*caseListPath) != "" && strings.TrimSpace(*filterValue) != "" {
		return fmt.Errorf("-case-list and -filter are mutually exclusive")
	}
	if *workers <= 0 {
		return fmt.Errorf("-workers must be positive")
	}
	if *commandTimeout <= 0 {
		return fmt.Errorf("-command-timeout must be positive")
	}
	if *caseTimeout <= 0 {
		return fmt.Errorf("-case-timeout must be positive")
	}

	cases, err := artifact.ReadCasesJSONL(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	selected, err := selectInstanceIDs(cases, *caseListPath, *filterValue)
	if err != nil {
		return err
	}
	if err := sweenv.ValidateOfflineAssets(*offlineAssetsDir, selected); err != nil {
		return err
	}
	environmentConfig, err := sweenv.LoadConfig(*environmentConfigPath)
	if err != nil {
		return err
	}
	factory := sweenv.DockerFactory{
		Config:                environmentConfig,
		DockerHost:            *dockerHost,
		CommandTimeout:        *commandTimeout,
		CaseTimeout:           *caseTimeout,
		Labels:                map[string]string{"tag-swebench.preflight": "offline"},
		EnableOfflineServices: true,
		OfflineAssetsDir:      *offlineAssetsDir,
		SanitizeGitHistory:    true,
	}
	return run(context.Background(), factory, selected, *workers, *caseTimeout, os.Stdout)
}

type caseResult struct {
	instanceID string
	duration   time.Duration
	err        error
}

func run(
	ctx context.Context,
	factory sweenv.Factory,
	instanceIDs []string,
	workers int,
	caseTimeout time.Duration,
	output io.Writer,
) error {
	started := time.Now()
	jobs := make(chan string)
	results := make(chan caseResult, len(instanceIDs))
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for instanceID := range jobs {
				results <- runCase(ctx, factory, instanceID, caseTimeout)
			}
		}()
	}
	go func() {
		for _, instanceID := range instanceIDs {
			jobs <- instanceID
		}
		close(jobs)
		waitGroup.Wait()
		close(results)
	}()

	byInstance := make(map[string]caseResult, len(instanceIDs))
	for result := range results {
		byInstance[result.instanceID] = result
	}
	var failures []error
	for _, instanceID := range instanceIDs {
		result := byInstance[instanceID]
		status := "Passed"
		if result.err != nil {
			status = "Failed"
			failures = append(failures, fmt.Errorf("%s: %w", instanceID, result.err))
		}
		fmt.Fprintf(
			output,
			"instance=%s status=%s duration_ms=%d\n",
			instanceID,
			status,
			result.duration.Milliseconds(),
		)
	}
	fmt.Fprintf(
		output,
		"preflight cases=%d failed=%d duration_ms=%d\n",
		len(instanceIDs),
		len(failures),
		time.Since(started).Milliseconds(),
	)
	if len(failures) > 0 {
		return fmt.Errorf("offline preflight failed for %d/%d cases: %w", len(failures), len(instanceIDs), errors.Join(failures...))
	}
	return nil
}

func runCase(
	ctx context.Context,
	factory sweenv.Factory,
	instanceID string,
	caseTimeout time.Duration,
) caseResult {
	started := time.Now()
	result := caseResult{instanceID: instanceID}
	caseContext, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	environment, err := factory.Start(caseContext, instanceID)
	if err != nil {
		result.duration = time.Since(started)
		result.err = fmt.Errorf("start environment: %w", err)
		return result
	}
	canary := environment.Execute(caseContext, isolationCanary)
	if canary.ReturnCode != 0 {
		result.err = fmt.Errorf(
			"isolation canary returned %d: %s%s",
			canary.ReturnCode,
			strings.TrimSpace(canary.Output),
			formatException(canary.ExceptionInfo),
		)
	}
	closeContext, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	closeErr := environment.Close(closeContext)
	closeCancel()
	if closeErr != nil {
		result.err = errors.Join(result.err, fmt.Errorf("close environment: %w", closeErr))
	}
	result.duration = time.Since(started)
	return result
}

func formatException(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return ": " + value
}

func selectInstanceIDs(
	cases []contract.Case,
	caseListPath string,
	filterValue string,
) ([]string, error) {
	var filter *regexp.Regexp
	var err error
	if strings.TrimSpace(filterValue) != "" {
		filter, err = regexp.Compile(filterValue)
		if err != nil {
			return nil, fmt.Errorf("compile filter: %w", err)
		}
	}
	listed, err := loadCaseList(caseListPath)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(cases))
	selected := make([]string, 0, len(cases))
	for _, selectedCase := range cases {
		instanceID := strings.TrimSpace(selectedCase.InstanceID)
		if instanceID == "" {
			return nil, fmt.Errorf("case manifest contains an empty instance_id")
		}
		if _, ok := seen[instanceID]; ok {
			return nil, fmt.Errorf("case manifest contains duplicate instance_id %q", instanceID)
		}
		seen[instanceID] = struct{}{}
		if filter != nil && !filter.MatchString(instanceID) {
			continue
		}
		if listed != nil {
			if _, ok := listed[instanceID]; !ok {
				continue
			}
		}
		selected = append(selected, instanceID)
	}
	if listed != nil {
		var missing []string
		for instanceID := range listed {
			if _, ok := seen[instanceID]; !ok {
				missing = append(missing, instanceID)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			return nil, fmt.Errorf("case list contains ids absent from cases manifest: %s", strings.Join(missing, ", "))
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no cases selected")
	}
	return selected, nil
}

func loadCaseList(path string) (map[string]struct{}, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open case list: %w", err)
	}
	defer file.Close()
	listed := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		instanceID := strings.TrimSpace(scanner.Text())
		if instanceID == "" || strings.HasPrefix(instanceID, "#") {
			continue
		}
		if _, ok := listed[instanceID]; ok {
			return nil, fmt.Errorf("case list contains duplicate instance_id %q", instanceID)
		}
		listed[instanceID] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read case list: %w", err)
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("case list %s is empty", path)
	}
	return listed, nil
}
