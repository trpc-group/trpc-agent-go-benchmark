//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package offlinepreflight validates clean-room testbeds without invoking a
// model. It exercises the same Docker, Git sanitation, fixture, and cleanup
// path as the native runner.
package offlinepreflight

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
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

const (
	preflightSchema              = "swebench-offline-preflight-v1"
	offlineHTTPBinImageReference = "docker.io/kennethreitz/httpbin:latest"
)

type manifest struct {
	Schema                string                          `json:"schema"`
	StartedAt             time.Time                       `json:"started_at"`
	FinishedAt            time.Time                       `json:"finished_at"`
	Status                string                          `json:"status"`
	CaseCount             int                             `json:"case_count"`
	FailedCount           int                             `json:"failed_count"`
	CleanRoomPolicySHA256 string                          `json:"clean_room_policy_sha256"`
	OfflineAssets         *sweenv.OfflineAssetIdentity    `json:"offline_assets,omitempty"`
	ImageSetSHA256        string                          `json:"image_set_sha256"`
	DockerImages          map[string]sweenv.ImageIdentity `json:"docker_images"`
	Cases                 []caseResult                    `json:"cases"`
}

type caseResult struct {
	InstanceID string             `json:"instance_id"`
	BaseCommit string             `json:"base_commit"`
	DurationMS int64              `json:"duration_ms"`
	Status     string             `json:"status"`
	Checks     []checkResult      `json:"checks,omitempty"`
	Provenance *sweenv.Provenance `json:"environment_provenance,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type checkResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

// Run executes the model-free offline preflight CLI.
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
		"portable host asset bundle prepared by scripts/prepare-offline-assets.sh",
	)
	output := fs.String("output", "offline-preflight-manifest.json", "preflight JSON manifest")
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
	if *workers <= 0 || *commandTimeout <= 0 || *caseTimeout <= 0 {
		return fmt.Errorf("workers and timeouts must be positive")
	}

	cases, err := artifact.ReadCasesJSONL(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	selected, err := selectCases(cases, *caseListPath, *filterValue)
	if err != nil {
		return err
	}
	instanceIDs := make([]string, 0, len(selected))
	specs := make([]sweenv.CaseSpec, 0, len(selected))
	for _, selectedCase := range selected {
		instanceIDs = append(instanceIDs, selectedCase.InstanceID)
		specs = append(specs, sweenv.CaseSpec{
			InstanceID: selectedCase.InstanceID,
			Repo:       selectedCase.Repo,
			BaseCommit: selectedCase.BaseCommit,
		})
	}
	assets, err := sweenv.InspectOfflineAssets(*offlineAssetsDir, instanceIDs)
	if err != nil {
		return err
	}
	policySHA256, err := sweenv.CleanRoomPolicySHA256(nil)
	if err != nil {
		return err
	}
	environmentConfig, err := sweenv.LoadConfig(*environmentConfigPath)
	if err != nil {
		return err
	}
	factory := sweenv.DockerFactory{
		Config: environmentConfig, DockerHost: *dockerHost,
		CommandTimeout: *commandTimeout, CaseTimeout: *caseTimeout,
		ContainerNamePrefix: "swebench-preflight-",
		Labels:              map[string]string{"swebench.preflight": "offline"},
		CleanRoom:           true, EnableOfflineServices: true,
		OfflineAssetsDir: *offlineAssetsDir, OfflineAssets: assets,
	}
	images, err := factory.ResolveImages(context.Background(), specs)
	if err != nil {
		return err
	}
	imageSetSHA256, err := sweenv.ImageSetSHA256(images)
	if err != nil {
		return err
	}
	factory.ResolvedImages = images

	started := time.Now().UTC()
	results := run(context.Background(), factory, selected, *workers, *caseTimeout, images)
	doc := manifest{
		Schema: preflightSchema, StartedAt: started, FinishedAt: time.Now().UTC(),
		Status: "passed", CaseCount: len(results), CleanRoomPolicySHA256: policySHA256,
		ImageSetSHA256: imageSetSHA256, DockerImages: images, Cases: results,
	}
	if assets.SHA256 != "" {
		doc.OfflineAssets = &assets
	}
	var failures []error
	for _, result := range results {
		if result.Status != "Passed" {
			doc.FailedCount++
			failures = append(failures, fmt.Errorf("%s: %s", result.InstanceID, result.Error))
		}
	}
	if doc.FailedCount > 0 {
		doc.Status = "failed"
	}
	if err := artifact.WriteJSON(*output, doc); err != nil {
		return err
	}
	fmt.Printf("preflight cases=%d failed=%d manifest=%s\n", doc.CaseCount, doc.FailedCount, *output)
	if len(failures) > 0 {
		return fmt.Errorf("offline preflight failed for %d/%d cases: %w", doc.FailedCount, doc.CaseCount, errors.Join(failures...))
	}
	return nil
}

func run(
	ctx context.Context,
	factory sweenv.CaseFactory,
	cases []contract.Case,
	workers int,
	caseTimeout time.Duration,
	images map[string]sweenv.ImageIdentity,
) []caseResult {
	jobs := make(chan contract.Case)
	results := make(chan caseResult, len(cases))
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for selectedCase := range jobs {
				results <- runCase(ctx, factory, selectedCase, caseTimeout, images)
			}
		}()
	}
	go func() {
		for _, selectedCase := range cases {
			jobs <- selectedCase
		}
		close(jobs)
		waitGroup.Wait()
		close(results)
	}()
	byInstance := make(map[string]caseResult, len(cases))
	for result := range results {
		byInstance[result.InstanceID] = result
	}
	ordered := make([]caseResult, 0, len(cases))
	for _, selectedCase := range cases {
		ordered = append(ordered, byInstance[selectedCase.InstanceID])
	}
	return ordered
}

func runCase(
	ctx context.Context,
	factory sweenv.CaseFactory,
	selectedCase contract.Case,
	caseTimeout time.Duration,
	images map[string]sweenv.ImageIdentity,
) caseResult {
	started := time.Now()
	result := caseResult{
		InstanceID: selectedCase.InstanceID,
		BaseCommit: selectedCase.BaseCommit,
		Status:     "Failed",
	}
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	caseContext, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	environment, err := factory.StartCase(caseContext, sweenv.CaseSpec{
		InstanceID: selectedCase.InstanceID,
		Repo:       selectedCase.Repo,
		BaseCommit: selectedCase.BaseCommit,
	})
	if err != nil {
		result.Error = fmt.Sprintf("start environment: %v", err)
		return result
	}
	closeEnvironment := func() error {
		closeContext, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()
		return environment.Close(closeContext)
	}
	provider, ok := environment.(sweenv.ProvenanceProvider)
	if !ok {
		result.Checks = append(result.Checks, checkResult{
			Name: "environment-provenance", Passed: false,
			Error: "clean-room environment did not provide image provenance",
		})
		_ = closeEnvironment()
		result.Error = "clean-room environment did not provide image provenance"
		return result
	}
	provenance := provider.Provenance()
	if err := validateProvenance(selectedCase.InstanceID, provenance, images); err != nil {
		result.Checks = append(result.Checks, checkResult{
			Name: "environment-provenance", Passed: false,
			Error: err.Error(),
		})
		_ = closeEnvironment()
		result.Error = err.Error()
		return result
	}
	result.Provenance = &provenance
	result.Checks = append(result.Checks, checkResult{Name: "environment-provenance", Passed: true})
	for _, check := range canaries(selectedCase) {
		commandResult := environment.Execute(caseContext, check.command)
		outcome := checkResult{Name: check.name, Passed: commandResult.ReturnCode == 0}
		if !outcome.Passed {
			outcome.Error = strings.TrimSpace(commandResult.Output)
			if commandResult.ExceptionInfo != "" {
				outcome.Error = strings.TrimSpace(outcome.Error + ": " + commandResult.ExceptionInfo)
			}
		}
		result.Checks = append(result.Checks, outcome)
	}
	closeErr := closeEnvironment()
	for _, check := range result.Checks {
		if !check.Passed {
			result.Error = "one or more isolation canaries failed"
			return result
		}
	}
	if closeErr != nil {
		result.Error = fmt.Sprintf("close environment: %v", closeErr)
		return result
	}
	result.Status = "Passed"
	return result
}

func validateProvenance(
	instanceID string,
	provenance sweenv.Provenance,
	images map[string]sweenv.ImageIdentity,
) error {
	testbed, ok := images[sweenv.ImageForInstance(instanceID)]
	if !ok || provenance.Testbed != testbed {
		return fmt.Errorf("clean-room environment did not attest the resolved testbed image")
	}
	expected := map[string]sweenv.ImageIdentity{}
	if strings.HasPrefix(instanceID, "psf__requests-") {
		httpbin, ok := images[offlineHTTPBinImageReference]
		if !ok {
			return fmt.Errorf("resolved Docker images do not contain the offline httpbin image")
		}
		expected["httpbin"] = httpbin
	}
	switch instanceID {
	case "psf__requests-2317", "psf__requests-2931", "psf__requests-5414", "psf__requests-6028":
		expected["network-helper"] = testbed
	}
	if len(provenance.AuxiliaryImages) != len(expected) {
		return fmt.Errorf("clean-room environment attested unexpected auxiliary image roles")
	}
	for role, want := range expected {
		if actual, ok := provenance.AuxiliaryImages[role]; !ok || actual != want {
			return fmt.Errorf("clean-room environment attested unexpected %s image provenance", role)
		}
	}
	return nil
}

type canary struct {
	name    string
	command string
}

func canaries(selectedCase contract.Case) []canary {
	checks := []canary{
		{name: "git", command: gitCanary(selectedCase.BaseCommit)},
		{name: "dns-egress-gateway", command: networkCanary},
		{name: "model-privileges", command: privilegeCanary},
		{name: "package-index", command: `test "${PIP_NO_INDEX:-}" = 1`},
	}
	if strings.HasPrefix(selectedCase.InstanceID, "psf__requests-") {
		checks = append(checks, canary{name: "local-httpbin", command: httpbinCanary})
	}
	if selectedCase.InstanceID == "psf__requests-2317" || selectedCase.InstanceID == "psf__requests-2931" ||
		selectedCase.InstanceID == "psf__requests-5414" || selectedCase.InstanceID == "psf__requests-6028" {
		checks = append(checks, canary{name: "local-tarpit", command: tarpitCanary})
	}
	return checks
}

func gitCanary(expectedBase string) string {
	return `set -euo pipefail
cd /testbed
test "$(git rev-parse --verify HEAD)" = "` + expectedBase + `"
test -z "$(git status --porcelain)"
test "$(git for-each-ref --format='%(refname)' | wc -l)" -eq 1
test -z "$(git remote)"
test -z "$(git tag --list)"
test ! -f .git/objects/info/alternates
test ! -e .git/logs
git diff --quiet
git submodule foreach --quiet --recursive '
set -e
test -z "$(git status --porcelain)"
test "$(git for-each-ref --format="%(refname)" | wc -l)" -eq 1
test -z "$(git remote)"
test -z "$(git tag --list)"
test ! -f .git/objects/info/alternates
test ! -e .git/logs
git diff --quiet
'`
}

const networkCanary = `python - <<'PY'
import socket

try:
    socket.getaddrinfo("example.com", 443)
except OSError:
    pass
else:
    raise SystemExit("public DNS unexpectedly resolved example.com")

for family, address in ((socket.AF_INET, ("1.1.1.1", 443)),
                        (socket.AF_INET6, ("2606:4700:4700::1111", 443, 0, 0))):
    connection = socket.socket(family, socket.SOCK_STREAM)
    connection.settimeout(1)
    try:
        connection.connect(address)
    except OSError:
        pass
    else:
        raise SystemExit("public raw-IP egress unexpectedly succeeded")
    finally:
        connection.close()

with open("/proc/net/route", encoding="ascii") as routes:
    for line in list(routes)[1:]:
        fields = line.split()
        if len(fields) >= 4 and fields[1] == "00000000" and int(fields[3], 16) & 1:
            raise SystemExit("IPv4 default route unexpectedly exists")

try:
    ipv6_routes = open("/proc/net/ipv6_route", encoding="ascii")
except FileNotFoundError:
    pass
else:
    with ipv6_routes:
        for line in ipv6_routes:
            fields = line.split()
            if (len(fields) >= 9 and fields[0] == "0" * 32 and fields[1] == "00"
                    and int(fields[8], 16) & 1):
                raise SystemExit("IPv6 default route unexpectedly exists")
PY`

const privilegeCanary = `python - <<'PY'
with open("/proc/self/status", encoding="ascii") as status:
    values = dict(line.split(":", 1) for line in status if ":" in line)
effective = int(values["CapEff"].strip(), 16)
if effective & (1 << 12):
    raise SystemExit("model container unexpectedly has CAP_NET_ADMIN")
PY`

const httpbinCanary = `set -euo pipefail
test "$(getent ahostsv4 httpbin.org | awk 'NR == 1 {print $1}')" = 127.0.0.1
curl -fsS --connect-timeout 3 http://httpbin.org/get | grep -q 'httpbin.org/get'
curl -fsS --cacert /tmp/swebench-httpbin-ca.pem --connect-timeout 3 https://httpbin.org/get | grep -q 'httpbin.org/get'`

const tarpitCanary = `python - <<'PY'
import socket
import time

started = time.monotonic()
connection = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
connection.settimeout(0.25)
try:
    connection.connect(("10.255.255.1", 80))
except socket.timeout:
    if time.monotonic() - started < 0.20:
        raise SystemExit("tarpit timed out too quickly")
except OSError as error:
    raise SystemExit(f"tarpit failed immediately: {error}")
else:
    raise SystemExit("tarpit unexpectedly connected")
finally:
    connection.close()
PY`

func selectCases(cases []contract.Case, caseListPath, filterValue string) ([]contract.Case, error) {
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
	selected := make([]contract.Case, 0, len(cases))
	for _, selectedCase := range cases {
		instanceID := strings.TrimSpace(selectedCase.InstanceID)
		if instanceID == "" || selectedCase.BaseCommit == "" {
			return nil, fmt.Errorf("case manifest contains an incomplete case identity")
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
		selected = append(selected, selectedCase)
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

func loadCaseList(file string) (map[string]struct{}, error) {
	if strings.TrimSpace(file) == "" {
		return nil, nil
	}
	handle, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("open case list: %w", err)
	}
	defer handle.Close()
	listed := map[string]struct{}{}
	scanner := bufio.NewScanner(handle)
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
		return nil, fmt.Errorf("case list %s is empty", file)
	}
	return listed, nil
}
