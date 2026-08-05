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
	"strings"
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

func TestSnapshotHarnessPredictionsBindsExactRegularFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "preds.json")
	data := []byte(`{"case-a":{"instance_id":"case-a","model_patch":"patch"}}`)
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(dir, "verify")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, digest, err := snapshotHarnessPredictions(source, outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != filepath.Join(outputDir, "predictions.snapshot.json") {
		t.Fatalf("snapshot = %q", snapshot)
	}
	if digest != testFileSHA256(t, source) || digest != testFileSHA256(t, snapshot) {
		t.Fatalf("digest = %q, source/snapshot digests differ", digest)
	}
	if err := os.WriteFile(snapshot, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err = validatePredictionsSnapshot(snapshot, digest)
	if err == nil || !strings.Contains(err.Error(), "does not match pre-run digest") {
		t.Fatalf("validatePredictionsSnapshot() error = %v", err)
	}
}

func TestSnapshotHarnessPredictionsRejectsSymlinkSource(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "preds.json")
	if err := os.Symlink(realPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	_, _, err := snapshotHarnessPredictions(symlinkPath, filepath.Join(dir, "verify"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("snapshotHarnessPredictions() error = %v", err)
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

func TestRunVerifyRejectsOversizedDerivedHarnessRunIDBeforeCreatingOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	err := runVerify(context.Background(), []string{
		"--run-id", strings.Repeat("r", 100),
		"--target", strings.Repeat("t", 40),
		"--predictions", "gold",
		"--output", output,
		"--python", filepath.Join(dir, "missing-python"),
	})
	if err == nil || !strings.Contains(err.Error(), "harness run id") {
		t.Fatalf("runVerify() error = %v, want derived harness run-id validation", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output was created before derived run-id validation: %v", statErr)
	}
}

func TestRunVerifyBindsPredictionsAndEmptyPatchReport(t *testing.T) {
	dir := t.TempDir()
	predictionsPath := filepath.Join(dir, "preds.json")
	if err := writeJSON(predictionsPath, map[string]any{
		"case-a": map[string]any{
			"instance_id":        "case-a",
			"model_name_or_path": "model",
			"model_patch":        "",
		},
	}); err != nil {
		t.Fatal(err)
	}
	fakePython := filepath.Join(dir, "fake-python")
	script := `#!/bin/sh
if [ "$1" = "-c" ]; then
  printf '%s\n' '{"version":"test","revision":"rev","package_path":"/tmp/swebench.py"}'
  exit 0
fi
report_dir=""
run_id=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --report_dir) shift; report_dir="$1" ;;
    -id) shift; run_id="$1" ;;
  esac
  shift
done
printf '%s\n' '{"empty_patch_ids":["case-a"]}' > "$report_dir/model.$run_id.json"
`
	if err := os.WriteFile(fakePython, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(dir, "verify")
	if err := runVerify(context.Background(), []string{
		"--run-id", "run-1",
		"--target", "native",
		"--predictions", predictionsPath,
		"--output", outputDir,
		"--python", fakePython,
	}); err != nil {
		t.Fatalf("runVerify() error = %v", err)
	}
	var manifest verifyManifest
	if err := readJSONFile(filepath.Join(outputDir, "verifier_manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Report.Path != filepath.Join(outputDir, "model.run-1-native.json") || manifest.Report.SHA256 == "" {
		t.Fatalf("manifest.Report = %+v", manifest.Report)
	}
	if manifest.Config.Predictions != absPath(predictionsPath) ||
		manifest.Config.PredictionsSnapshot != filepath.Join(outputDir, "predictions.snapshot.json") ||
		manifest.Config.PredictionsSHA256 != testFileSHA256(t, predictionsPath) {
		t.Fatalf("manifest.Config predictions binding = %+v", manifest.Config)
	}
	if manifest.ReportError != "" || manifest.PredictionsError != "" {
		t.Fatalf("manifest errors = report:%q predictions:%q", manifest.ReportError, manifest.PredictionsError)
	}
}

func TestDiscoverHarnessReportBindsUniqueArtifact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-model.run-1-tag.json")
	if err := writeJSON(path, map[string]any{"resolved_ids": []string{"case-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "run-1-tag", "case-a")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "report.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := discoverHarnessReport(dir, "run-1-tag")
	if err != nil {
		t.Fatal(err)
	}
	if got.HarnessRunID != "run-1-tag" || got.Path != absPath(path) || got.SHA256 != testFileSHA256(t, path) {
		t.Fatalf("discoverHarnessReport() = %+v", got)
	}
}

func TestDiscoverHarnessReportBindsEmptyPatchOnlyArtifact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-model.run-1-tag.json")
	if err := writeJSON(path, map[string]any{"empty_patch_ids": []string{"case-a"}}); err != nil {
		t.Fatal(err)
	}
	got, err := discoverHarnessReport(dir, "run-1-tag")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != absPath(path) || got.SHA256 != testFileSHA256(t, path) {
		t.Fatalf("discoverHarnessReport() = %+v", got)
	}
}

func TestDiscoverHarnessReportRejectsAmbiguousOrUnsafeArtifact(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{name: "missing", setup: func(*testing.T, string) {}, want: "found 0"},
		{
			name: "ambiguous",
			setup: func(t *testing.T, dir string) {
				for _, model := range []string{"model-a", "model-b"} {
					if err := writeJSON(filepath.Join(dir, model+".run-1-tag.json"), map[string]any{"resolved_ids": []string{"case-a"}}); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "found 2",
		},
		{
			name: "symlink",
			setup: func(t *testing.T, dir string) {
				realPath := filepath.Join(dir, "real.json")
				if err := writeJSON(realPath, map[string]any{"resolved_ids": []string{"case-a"}}); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realPath, filepath.Join(dir, "model.run-1-tag.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a regular non-symlink file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			_, err := discoverHarnessReport(dir, "run-1-tag")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("discoverHarnessReport() error = %v, want %q", err, tt.want)
			}
		})
	}
}
