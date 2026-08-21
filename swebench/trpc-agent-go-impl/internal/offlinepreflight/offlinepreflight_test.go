//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package offlinepreflight

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

type preflightFactory struct {
	mu            sync.Mutex
	specs         []sweenv.CaseSpec
	delays        map[string]time.Duration
	failingCase   string
	noProvenance  bool
	emptyIdentity bool
	environments  map[string]*preflightEnvironment
}

func (f *preflightFactory) StartCase(_ context.Context, spec sweenv.CaseSpec) (sweenv.Environment, error) {
	if delay := f.delays[spec.InstanceID]; delay > 0 {
		time.Sleep(delay)
	}
	environment := &preflightEnvironment{fail: spec.InstanceID == f.failingCase}
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	if f.environments == nil {
		f.environments = map[string]*preflightEnvironment{}
	}
	f.environments[spec.InstanceID] = environment
	f.mu.Unlock()
	if f.noProvenance {
		return environment, nil
	}
	identity := sweenv.ImageIdentity{
		Reference: sweenv.ImageForInstance(spec.InstanceID),
		ID:        "sha256:" + strings.Repeat("1", 64),
	}
	if f.emptyIdentity {
		identity = sweenv.ImageIdentity{}
	}
	return provenancePreflightEnvironment{
		preflightEnvironment: environment,
		provenance:           sweenv.Provenance{Testbed: identity},
	}, nil
}

type preflightEnvironment struct {
	mu       sync.Mutex
	commands []string
	fail     bool
	closed   bool
}

func (e *preflightEnvironment) Execute(_ context.Context, command string) sweenv.CommandResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.commands = append(e.commands, command)
	if e.fail {
		e.fail = false
		return sweenv.CommandResult{ReturnCode: 1, Output: "injected canary failure"}
	}
	return sweenv.CommandResult{}
}

func (e *preflightEnvironment) Close(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return nil
}

type provenancePreflightEnvironment struct {
	*preflightEnvironment
	provenance sweenv.Provenance
}

func (e provenancePreflightEnvironment) Provenance() sweenv.Provenance { return e.provenance }

func TestSelectCasesPreservesManifestOrder(t *testing.T) {
	cases := []contract.Case{
		{InstanceID: "case-b", BaseCommit: strings.Repeat("b", 40)},
		{InstanceID: "case-a", BaseCommit: strings.Repeat("a", 40)},
		{InstanceID: "case-c", BaseCommit: strings.Repeat("c", 40)},
	}
	caseList := filepath.Join(t.TempDir(), "cases.txt")
	if err := os.WriteFile(caseList, []byte("case-c\n# ignored\ncase-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	selected, err := selectCases(cases, caseList, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []contract.Case{cases[1], cases[2]}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected = %#v, want %#v", selected, want)
	}

	filtered, err := selectCases(cases, "", `case-[bc]`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []contract.Case{cases[0], cases[2]}; !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %#v, want %#v", filtered, want)
	}
}

func TestSelectCasesRejectsAmbiguousInputs(t *testing.T) {
	t.Run("duplicate manifest id", func(t *testing.T) {
		cases := []contract.Case{
			{InstanceID: "case-a", BaseCommit: strings.Repeat("a", 40)},
			{InstanceID: "case-a", BaseCommit: strings.Repeat("b", 40)},
		}
		if _, err := selectCases(cases, "", ""); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing listed id", func(t *testing.T) {
		caseList := filepath.Join(t.TempDir(), "cases.txt")
		if err := os.WriteFile(caseList, []byte("missing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := selectCases([]contract.Case{{
			InstanceID: "case-a", BaseCommit: strings.Repeat("a", 40),
		}}, caseList, ""); err == nil || !strings.Contains(err.Error(), "absent") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunPreservesSelectionOrderAndReportsCanaryFailure(t *testing.T) {
	cases := []contract.Case{
		{InstanceID: "case-a", Repo: "org/a", BaseCommit: strings.Repeat("a", 40)},
		{InstanceID: "case-b", Repo: "org/b", BaseCommit: strings.Repeat("b", 40)},
		{InstanceID: "case-c", Repo: "org/c", BaseCommit: strings.Repeat("c", 40)},
	}
	factory := &preflightFactory{
		delays:      map[string]time.Duration{"case-a": 20 * time.Millisecond},
		failingCase: "case-b",
	}
	results := run(context.Background(), factory, cases, 3, time.Second, preflightImages(cases...))
	if got := []string{results[0].InstanceID, results[1].InstanceID, results[2].InstanceID}; !reflect.DeepEqual(got, []string{"case-a", "case-b", "case-c"}) {
		t.Fatalf("result order = %#v", got)
	}
	if results[0].Status != "Passed" || results[1].Status != "Failed" || results[2].Status != "Passed" {
		t.Fatalf("results = %+v", results)
	}
	if !strings.Contains(results[1].Error, "canaries failed") || len(results[1].Checks) != 5 {
		t.Fatalf("failed result = %+v", results[1])
	}
	for index, selectedCase := range cases {
		if results[index].BaseCommit != selectedCase.BaseCommit || results[index].Provenance == nil {
			t.Fatalf("result %d = %+v", index, results[index])
		}
		if environment := factory.environments[selectedCase.InstanceID]; !environment.closed {
			t.Fatalf("environment %s was not closed", selectedCase.InstanceID)
		}
	}
}

func TestRunCaseFailsClosedWithoutImageProvenance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		factory *preflightFactory
	}{
		{name: "missing provider", factory: &preflightFactory{noProvenance: true}},
		{name: "empty identity", factory: &preflightFactory{emptyIdentity: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectedCase := contract.Case{
				InstanceID: "case-a", Repo: "org/repo", BaseCommit: strings.Repeat("a", 40),
			}
			result := runCase(
				context.Background(), tc.factory, selectedCase, time.Second, preflightImages(selectedCase),
			)
			if result.Status != "Failed" || len(result.Checks) != 1 || result.Checks[0].Passed ||
				!strings.Contains(result.Checks[0].Name, "provenance") {
				t.Fatalf("result = %+v", result)
			}
			environment := tc.factory.environments["case-a"]
			if !environment.closed || len(environment.commands) != 0 {
				t.Fatalf("environment = %+v", environment)
			}
		})
	}
}

func preflightImages(cases ...contract.Case) map[string]sweenv.ImageIdentity {
	images := make(map[string]sweenv.ImageIdentity, len(cases))
	for _, selectedCase := range cases {
		reference := sweenv.ImageForInstance(selectedCase.InstanceID)
		images[reference] = sweenv.ImageIdentity{
			Reference: reference,
			ID:        "sha256:" + strings.Repeat("1", 64),
		}
	}
	return images
}

func TestValidateProvenanceRequiresResolvedImagesAndExactRoles(t *testing.T) {
	instanceID := "psf__requests-2317"
	reference := sweenv.ImageForInstance(instanceID)
	testbed := sweenv.ImageIdentity{
		Reference: reference,
		ID:        "sha256:" + strings.Repeat("1", 64),
	}
	httpbin := sweenv.ImageIdentity{
		Reference: offlineHTTPBinImageReference,
		ID:        "sha256:" + strings.Repeat("2", 64),
	}
	images := map[string]sweenv.ImageIdentity{
		reference:                    testbed,
		offlineHTTPBinImageReference: httpbin,
	}
	valid := sweenv.Provenance{
		Testbed: testbed,
		AuxiliaryImages: map[string]sweenv.ImageIdentity{
			"httpbin":        httpbin,
			"network-helper": testbed,
		},
	}
	if err := validateProvenance(instanceID, valid, images); err != nil {
		t.Fatal(err)
	}

	wrong := valid
	wrong.AuxiliaryImages = map[string]sweenv.ImageIdentity{
		"httpbin":        httpbin,
		"network-helper": httpbin,
	}
	if err := validateProvenance(instanceID, wrong, images); err == nil ||
		!strings.Contains(err.Error(), "network-helper") {
		t.Fatalf("error = %v", err)
	}
	wrong = valid
	wrong.Testbed = httpbin
	if err := validateProvenance(instanceID, wrong, images); err == nil ||
		!strings.Contains(err.Error(), "resolved testbed") {
		t.Fatalf("error = %v", err)
	}
}

func TestNetworkCanaryChecksIPv4AndIPv6DefaultRoutes(t *testing.T) {
	for _, expected := range []string{"/proc/net/route", "/proc/net/ipv6_route", "IPv4 default route", "IPv6 default route"} {
		if !strings.Contains(networkCanary, expected) {
			t.Fatalf("network canary is missing %q", expected)
		}
	}
}
