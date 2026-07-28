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

func TestSelectInstanceIDs(t *testing.T) {
	cases := []contract.Case{
		{InstanceID: "django__django-1"},
		{InstanceID: "psf__requests-2"},
		{InstanceID: "sympy__sympy-3"},
	}
	caseList := filepath.Join(t.TempDir(), "cases.txt")
	if err := os.WriteFile(caseList, []byte("sympy__sympy-3\ndjango__django-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := selectInstanceIDs(cases, caseList, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"django__django-1", "sympy__sympy-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected = %#v, want %#v", got, want)
	}

	got, err = selectInstanceIDs(cases, "", `^(psf|sympy)__`)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"psf__requests-2", "sympy__sympy-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered = %#v, want %#v", got, want)
	}
}

func TestSelectInstanceIDsRejectsUnknownListedCase(t *testing.T) {
	caseList := filepath.Join(t.TempDir(), "cases.txt")
	if err := os.WriteFile(caseList, []byte("missing__case-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := selectInstanceIDs([]contract.Case{{InstanceID: "known__case-1"}}, caseList, "")
	if err == nil || !strings.Contains(err.Error(), "absent from cases manifest") {
		t.Fatalf("selectInstanceIDs error = %v", err)
	}
}

func TestRunStartsChecksAndClosesEveryCase(t *testing.T) {
	factory := &fakeFactory{}
	var output strings.Builder
	if err := run(
		context.Background(),
		factory,
		[]string{"one", "two"},
		2,
		time.Second,
		&output,
	); err != nil {
		t.Fatal(err)
	}
	if got := factory.startedInstances(); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("started = %#v", got)
	}
	if strings.Count(output.String(), "status=Passed") != 2 ||
		!strings.Contains(output.String(), "preflight cases=2 failed=0") {
		t.Fatalf("output = %q", output.String())
	}
}

type fakeFactory struct {
	mu      sync.Mutex
	started []string
}

func (f *fakeFactory) Start(_ context.Context, instanceID string) (sweenv.Environment, error) {
	f.mu.Lock()
	f.started = append(f.started, instanceID)
	f.mu.Unlock()
	return &fakeEnvironment{}, nil
}

func (f *fakeFactory) startedInstances() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	started := append([]string(nil), f.started...)
	if started[0] > started[1] {
		started[0], started[1] = started[1], started[0]
	}
	return started
}

type fakeEnvironment struct{}

func (*fakeEnvironment) Execute(_ context.Context, _ string) sweenv.CommandResult {
	return sweenv.CommandResult{}
}

func (*fakeEnvironment) Close(_ context.Context) error {
	return nil
}
