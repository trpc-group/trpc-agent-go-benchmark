//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHarnessPredictionsArgKeepsGoldLiteral(t *testing.T) {
	if got := harnessPredictionsArg("gold"); got != "gold" {
		t.Fatalf("harnessPredictionsArg(\"gold\") = %q, want gold", got)
	}
}

func TestVerifyEnvironment(t *testing.T) {
	want := []string{
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"HF_HOME=/tmp/hf",
	}
	if got := verifyEnvironment("unix:///var/run/docker.sock", "/tmp/hf"); !reflect.DeepEqual(got, want) {
		t.Fatalf("verifyEnvironment() = %#v, want %#v", got, want)
	}
}

func TestVerifyInstanceIDsUsesSortedPredictionIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	data := []byte(`{
  "case-b": {"model_name_or_path":"model","instance_id":"case-b","model_patch":"patch"},
  "case-a": {"model_name_or_path":"model","instance_id":"case-a","model_patch":"patch"}
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := verifyInstanceIDs(path, "", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"case-a", "case-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verifyInstanceIDs() = %#v, want %#v", got, want)
	}
}

func TestBuildHarnessArgsIncludesSubset(t *testing.T) {
	got := buildHarnessArgs(
		"dataset",
		"test",
		"/tmp/preds.json",
		4,
		1800,
		"instance",
		false,
		"/tmp/report",
		"run-agent",
		[]string{"case-a", "case-b"},
	)
	want := []string{
		"-m", "swebench.harness.run_evaluation",
		"-d", "dataset",
		"-s", "test",
		"-p", "/tmp/preds.json",
		"--max_workers", "4",
		"--timeout", "1800",
		"--cache_level", "instance",
		"--clean", "false",
		"--report_dir", "/tmp/report",
		"-id", "run-agent",
		"-i", "case-a", "case-b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildHarnessArgs() = %#v, want %#v", got, want)
	}
}

func TestRunVerifyRejectsUnsafeTargetBeforeCreatingOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	err := runVerify(context.Background(), []string{
		"--run-id", "run-1",
		"--target", "../escape",
		"--predictions", "gold",
		"--output", output,
		"--python", filepath.Join(dir, "missing-python"),
	})
	if err == nil {
		t.Fatal("runVerify() accepted an unsafe target")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output was created before validation: %v", statErr)
	}
}
