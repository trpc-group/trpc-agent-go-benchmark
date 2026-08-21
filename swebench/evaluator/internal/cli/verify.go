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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
)

type harnessIdentity struct {
	Version     string `json:"version"`
	Revision    string `json:"revision,omitempty"`
	PackagePath string `json:"package_path,omitempty"`
}

type verifyManifest struct {
	RunID            string          `json:"run_id"`
	Target           string          `json:"target"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       time.Time       `json:"finished_at"`
	DurationMS       int64           `json:"duration_ms"`
	Command          commandResult   `json:"command"`
	Config           verifyConfig    `json:"config"`
	Harness          harnessIdentity `json:"harness"`
	Report           verifyReport    `json:"report,omitempty"`
	ReportError      string          `json:"report_error,omitempty"`
	PredictionsError string          `json:"predictions_error,omitempty"`
}

type verifyReport struct {
	HarnessRunID string `json:"harness_run_id"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
}

type verifyConfig struct {
	Dataset             string   `json:"dataset"`
	Split               string   `json:"split"`
	Instance            string   `json:"instance,omitempty"`
	InstanceIDs         []string `json:"instance_ids,omitempty"`
	Predictions         string   `json:"predictions"`
	PredictionsSnapshot string   `json:"predictions_snapshot,omitempty"`
	PredictionsSHA256   string   `json:"predictions_sha256,omitempty"`
	OutputDir           string   `json:"output_dir"`
	Workers             int      `json:"workers"`
	TimeoutSec          int      `json:"timeout_seconds"`
	CacheLevel          string   `json:"cache_level"`
	Clean               bool     `json:"clean"`
	Python              string   `json:"python"`
	DockerHost          string   `json:"docker_host"`
	HFHome              string   `json:"hf_home,omitempty"`
}

func runVerify(ctx context.Context, args []string) error {
	fs := newFlagSet("verify")
	runID := fs.String("run-id", "", "run id")
	target := fs.String("target", "baseline", "artifact target label")
	predictions := fs.String("predictions", "", "predictions JSON/JSONL path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>/local-harness-report/<target>")
	dataset := fs.String("dataset", defaultDatasetName, "SWE-Bench dataset name")
	split := fs.String("split", defaultSplit, "dataset split")
	instance := fs.String("instance", "", "optional single instance id")
	instancesFromPredictions := fs.Bool("instances-from-predictions", true, "restrict harness dataset to instance ids found in predictions")
	workers := fs.Int("harness-workers", 1, "SWE-Bench harness max workers")
	timeoutSec := fs.Int("instance-timeout-seconds", 1800, "official harness timeout per instance")
	cacheLevel := fs.String("cache-level", "instance", "SWE-Bench harness cache level")
	clean := fs.Bool("clean", false, "clean harness images/containers")
	python := fs.String("python", envOrDefault("PYTHON", "python"), "python executable")
	dockerHost := fs.String("docker-host", envOrDefault("DOCKER_HOST", defaultDockerHost), "Docker host")
	hfHome := fs.String("hf-home", os.Getenv("HF_HOME"), "HF_HOME cache path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"run-id":      *runID,
		"target":      *target,
		"predictions": *predictions,
	} {
		if err := required(fs, name, value); err != nil {
			return err
		}
	}
	if err := validateArtifactName("run id", *runID); err != nil {
		return err
	}
	if err := validateTargetLabel(*target); err != nil {
		return err
	}
	harnessRunID := *runID + "-" + *target
	if err := validateArtifactName("harness run id", harnessRunID); err != nil {
		return err
	}
	if strings.TrimSpace(*instance) != "" {
		if err := validateArtifactName("instance id", strings.TrimSpace(*instance)); err != nil {
			return err
		}
	}
	if *workers < 1 {
		return fmt.Errorf("harness-workers must be >= 1")
	}
	if *timeoutSec < 1 {
		return fmt.Errorf("instance-timeout-seconds must be >= 1")
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID, "local-harness-report", *target)
	}
	outputAbs := absPath(*output)
	if err := ensureDir(outputAbs); err != nil {
		return err
	}
	predictionsSource := harnessPredictionsArg(*predictions)
	predictionsArg, predictionsSHA256, err := snapshotHarnessPredictions(predictionsSource, outputAbs)
	if err != nil {
		return fmt.Errorf("snapshot harness predictions: %w", err)
	}

	instanceIDs, err := verifyInstanceIDs(predictionsArg, *instance, *instancesFromPredictions)
	if err != nil {
		return err
	}
	identity, err := detectHarnessIdentity(ctx, *python)
	if err != nil {
		return err
	}

	cmdArgs := buildHarnessArgs(
		*dataset,
		*split,
		predictionsArg,
		*workers,
		*timeoutSec,
		*cacheLevel,
		*clean,
		outputAbs,
		harnessRunID,
		instanceIDs,
	)
	env := verifyEnvironment(*dockerHost, *hfHome)

	start := time.Now()
	logPath := filepath.Join(outputAbs, "verify.log")
	result := runLogged(ctx, outputAbs, env, logPath, *python, cmdArgs...)
	finish := time.Now()
	report := verifyReport{HarnessRunID: harnessRunID}
	var reportErr error
	if result.ExitCode == 0 {
		report, reportErr = discoverHarnessReport(outputAbs, harnessRunID)
	}
	predictionsErr := validatePredictionsSnapshot(predictionsArg, predictionsSHA256)

	manifest := verifyManifest{
		RunID:            *runID,
		Target:           *target,
		StartedAt:        start.UTC(),
		FinishedAt:       finish.UTC(),
		DurationMS:       finish.Sub(start).Milliseconds(),
		Command:          result,
		Harness:          identity,
		Report:           report,
		ReportError:      errorString(reportErr),
		PredictionsError: errorString(predictionsErr),
		Config: verifyConfig{
			Dataset:             *dataset,
			Split:               *split,
			Instance:            *instance,
			InstanceIDs:         instanceIDs,
			Predictions:         predictionsSource,
			PredictionsSnapshot: predictionsSnapshotPath(predictionsArg),
			PredictionsSHA256:   predictionsSHA256,
			OutputDir:           outputAbs,
			Workers:             *workers,
			TimeoutSec:          *timeoutSec,
			CacheLevel:          *cacheLevel,
			Clean:               *clean,
			Python:              *python,
			DockerHost:          *dockerHost,
			HFHome:              *hfHome,
		},
	}
	if err := writeJSON(filepath.Join(outputAbs, "verifier_manifest.json"), manifest); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("swebench harness failed with exit code %d; see %s", result.ExitCode, logPath)
	}
	if reportErr != nil {
		return fmt.Errorf("bind harness report: %w", reportErr)
	}
	if predictionsErr != nil {
		return fmt.Errorf("bind harness predictions: %w", predictionsErr)
	}
	return nil
}

func predictionsSnapshotPath(predictionsArg string) string {
	if predictionsArg == "gold" {
		return ""
	}
	return predictionsArg
}

func snapshotHarnessPredictions(source, outputDir string) (string, string, error) {
	if strings.TrimSpace(source) == "gold" {
		return "gold", "", nil
	}
	data, err := readRegularArtifact(source)
	if err != nil {
		return "", "", err
	}
	extension := strings.ToLower(filepath.Ext(source))
	if extension != ".jsonl" {
		extension = ".json"
	}
	snapshotPath := filepath.Join(absPath(outputDir), "predictions.snapshot"+extension)
	if sameArtifactPath(source, snapshotPath) {
		return "", "", fmt.Errorf("predictions source and snapshot resolve to the same path %s", snapshotPath)
	}
	if err := artifact.WriteFileAtomic(snapshotPath, data, 0o644); err != nil {
		return "", "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := validatePredictionsSnapshot(snapshotPath, digest); err != nil {
		return "", "", err
	}
	return snapshotPath, digest, nil
}

func validatePredictionsSnapshot(path, expectedSHA256 string) error {
	if path == "gold" && expectedSHA256 == "" {
		return nil
	}
	if !isSHA256Hex(expectedSHA256) {
		return fmt.Errorf("predictions sha256 %q is not a SHA-256 digest", expectedSHA256)
	}
	data, err := readRegularArtifact(path)
	if err != nil {
		return err
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != expectedSHA256 {
		return fmt.Errorf(
			"predictions snapshot SHA-256 %q does not match pre-run digest %q",
			actual,
			expectedSHA256,
		)
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func discoverHarnessReport(outputDir, harnessRunID string) (verifyReport, error) {
	if err := validateArtifactName("harness run id", harnessRunID); err != nil {
		return verifyReport{}, err
	}
	outputAbs := absPath(outputDir)
	entries, err := os.ReadDir(outputAbs)
	if err != nil {
		return verifyReport{}, fmt.Errorf("read report directory: %w", err)
	}
	suffix := "." + harnessRunID + ".json"
	candidates := make([]string, 0, 1)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		path := filepath.Join(outputAbs, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return verifyReport{}, fmt.Errorf("inspect harness report candidate %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return verifyReport{}, fmt.Errorf("harness report candidate %s is not a regular non-symlink file", path)
		}
		candidates = append(candidates, path)
	}
	sort.Strings(candidates)
	if len(candidates) != 1 {
		return verifyReport{}, fmt.Errorf(
			"expected exactly one *%s harness report in %s, found %d: %v",
			suffix,
			outputAbs,
			len(candidates),
			candidates,
		)
	}
	data, err := os.ReadFile(candidates[0])
	if err != nil {
		return verifyReport{}, fmt.Errorf("read harness report %s: %w", candidates[0], err)
	}
	if _, err := readHarnessReport(candidates[0]); err != nil {
		return verifyReport{}, fmt.Errorf("validate harness report %s: %w", candidates[0], err)
	}
	return verifyReport{
		HarnessRunID: harnessRunID,
		Path:         candidates[0],
		SHA256:       fmt.Sprintf("%x", sha256.Sum256(data)),
	}, nil
}

func buildHarnessArgs(
	dataset, split, predictions string,
	workers, timeoutSec int,
	cacheLevel string,
	clean bool,
	output, runID string,
	instanceIDs []string,
) []string {
	args := []string{
		"-m", "swebench.harness.run_evaluation",
		"-d", dataset,
		"-s", split,
		"-p", predictions,
		"--max_workers", strconv.Itoa(workers),
		"--timeout", strconv.Itoa(timeoutSec),
		"--cache_level", cacheLevel,
		"--clean", strconv.FormatBool(clean),
		"--report_dir", output,
		"-id", runID,
	}
	if len(instanceIDs) > 0 {
		args = append(args, "-i")
		args = append(args, instanceIDs...)
	}
	return args
}

func detectHarnessIdentity(ctx context.Context, python string) (harnessIdentity, error) {
	const script = `
import importlib.metadata as metadata
import json
from pathlib import Path
import subprocess
import swebench

package_path = Path(swebench.__file__).resolve()
revision = ""
for candidate in [package_path.parent, *package_path.parents]:
    if not (candidate / ".git").exists():
        continue
    result = subprocess.run(
        ["git", "-C", str(candidate), "rev-parse", "HEAD"],
        capture_output=True,
        text=True,
    )
    if result.returncode == 0:
        revision = result.stdout.strip()
    break

print(json.dumps({
    "version": metadata.version("swebench"),
    "revision": revision,
    "package_path": str(package_path),
}))
`
	res := runCapture(ctx, "", nil, python, "-c", script)
	if res.ExitCode != 0 {
		return harnessIdentity{}, fmt.Errorf(
			"identify swebench harness: %s",
			strings.TrimSpace(res.Error+"\n"+res.Stderr+"\n"+res.Stdout),
		)
	}
	var identity harnessIdentity
	if err := json.Unmarshal([]byte(res.Stdout), &identity); err != nil {
		return harnessIdentity{}, fmt.Errorf("parse swebench harness identity: %w", err)
	}
	if strings.TrimSpace(identity.Version) == "" {
		return harnessIdentity{}, fmt.Errorf("identify swebench harness: empty version")
	}
	return identity, nil
}

func verifyEnvironment(dockerHost, hfHome string) []string {
	env := []string{"DOCKER_HOST=" + dockerHost}
	if strings.TrimSpace(hfHome) != "" {
		env = append(env, "HF_HOME="+hfHome)
	}
	return env
}

func harnessPredictionsArg(predictions string) string {
	if strings.TrimSpace(predictions) == "gold" {
		return "gold"
	}
	return absPath(predictions)
}

func verifyInstanceIDs(predictionsPath, instance string, instancesFromPredictions bool) ([]string, error) {
	if strings.TrimSpace(instance) != "" {
		return []string{strings.TrimSpace(instance)}, nil
	}
	if !instancesFromPredictions || strings.TrimSpace(predictionsPath) == "gold" {
		return nil, nil
	}
	preds, err := readPredictions(predictionsPath)
	if err != nil {
		return nil, fmt.Errorf("read predictions for instance_ids: %w", err)
	}
	ids := make([]string, 0, len(preds))
	for id := range preds {
		if err := validateArtifactName("instance id", id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
