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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type verifyManifest struct {
	RunID          string        `json:"run_id"`
	Target         string        `json:"target"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     time.Time     `json:"finished_at"`
	DurationMS     int64         `json:"duration_ms"`
	Command        commandResult `json:"command"`
	Config         verifyConfig  `json:"config"`
	HarnessPatched bool          `json:"harness_patched"`
}

type verifyConfig struct {
	Dataset     string   `json:"dataset"`
	Split       string   `json:"split"`
	Instance    string   `json:"instance,omitempty"`
	InstanceIDs []string `json:"instance_ids,omitempty"`
	Predictions string   `json:"predictions"`
	OutputDir   string   `json:"output_dir"`
	Workers     int      `json:"workers"`
	CacheLevel  string   `json:"cache_level"`
	Clean       bool     `json:"clean"`
	Python      string   `json:"python"`
	DockerHost  string   `json:"docker_host"`
	HFHome      string   `json:"hf_home,omitempty"`
	CompatPatch bool     `json:"compat_patch"`
}

func runVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	runID := fs.String("run-id", "", "run id")
	target := fs.String("target", "baseline", "baseline or native")
	predictions := fs.String("predictions", "", "predictions JSON/JSONL path")
	output := fs.String("output", "", "output directory; defaults to ../results/runs/<run-id>/local-harness-report/<target>")
	dataset := fs.String("dataset", defaultDatasetName, "SWE-Bench dataset name")
	split := fs.String("split", defaultSplit, "dataset split")
	instance := fs.String("instance", "", "optional single instance id")
	instancesFromPredictions := fs.Bool("instances-from-predictions", true, "restrict harness dataset to instance ids found in predictions")
	workers := fs.Int("harness-workers", 1, "SWE-Bench harness max workers")
	cacheLevel := fs.String("cache-level", "instance", "SWE-Bench harness cache level")
	clean := fs.Bool("clean", false, "clean harness images/containers")
	python := fs.String("python", envOrDefault("PYTHON", "python"), "python executable")
	dockerHost := fs.String("docker-host", envOrDefault("DOCKER_HOST", defaultDockerHost), "Docker host")
	hfHome := fs.String("hf-home", os.Getenv("HF_HOME"), "HF_HOME cache path")
	compatPatch := fs.Bool("apply-harness-compat", true, "patch installed swebench harness for Docker API<1.41 and seccomp-limited containers; set false for clean-upstream comparison")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "run-id", *runID); err != nil {
		return err
	}
	if err := required(fs, "predictions", *predictions); err != nil {
		return err
	}
	if *workers < 1 {
		return fmt.Errorf("harness-workers must be >= 1")
	}
	if *output == "" {
		*output = filepath.Join("..", "results", "runs", *runID, "local-harness-report", *target)
	}
	outputAbs := absPath(*output)
	if err := ensureDir(*output); err != nil {
		return err
	}

	patched := false
	if *compatPatch {
		if err := applyHarnessCompat(ctx, *python); err != nil {
			return err
		}
		patched = true
	}

	instanceIDs, err := verifyInstanceIDs(*predictions, *instance, *instancesFromPredictions)
	if err != nil {
		return err
	}

	harnessRunID := *runID + "-" + *target
	cmdArgs := []string{
		"-m", "swebench.harness.run_evaluation",
		"-d", *dataset,
		"-s", *split,
		"-p", absPath(*predictions),
		"--max_workers", strconv.Itoa(*workers),
		"--cache_level", *cacheLevel,
		"--clean", strconv.FormatBool(*clean),
		"--report_dir", outputAbs,
		"-id", harnessRunID,
	}
	if len(instanceIDs) > 0 {
		cmdArgs = append(cmdArgs, "-i")
		cmdArgs = append(cmdArgs, instanceIDs...)
	}

	env := []string{"DOCKER_HOST=" + *dockerHost}
	if strings.TrimSpace(*hfHome) != "" {
		env = append(env, "HF_HOME="+*hfHome)
	}

	start := time.Now()
	logPath := filepath.Join(outputAbs, "verify.log")
	result := runLogged(ctx, outputAbs, env, logPath, *python, cmdArgs...)
	finish := time.Now()

	manifest := verifyManifest{
		RunID:          *runID,
		Target:         *target,
		StartedAt:      start.UTC(),
		FinishedAt:     finish.UTC(),
		DurationMS:     finish.Sub(start).Milliseconds(),
		Command:        result,
		HarnessPatched: patched,
		Config: verifyConfig{
			Dataset:     *dataset,
			Split:       *split,
			Instance:    *instance,
			InstanceIDs: instanceIDs,
			Predictions: absPath(*predictions),
			OutputDir:   outputAbs,
			Workers:     *workers,
			CacheLevel:  *cacheLevel,
			Clean:       *clean,
			Python:      *python,
			DockerHost:  *dockerHost,
			HFHome:      *hfHome,
			CompatPatch: *compatPatch,
		},
	}
	if err := writeJSON(filepath.Join(outputAbs, "verifier_manifest.json"), manifest); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("swebench harness failed with exit code %d; see %s", result.ExitCode, logPath)
	}
	return nil
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
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func applyHarnessCompat(ctx context.Context, python string) error {
	script := `
from pathlib import Path
import inspect
import swebench.harness.docker_build as db

path = Path(inspect.getsourcefile(db))
text = path.read_text()
backup = path.with_suffix(path.suffix + ".bak-swebench-compat")
if not backup.exists():
    backup.write_text(text)

old_create = '''        container = client.containers.create(
            image=test_spec.instance_image_key,
            name=test_spec.get_instance_container_name(run_id),
            user=DOCKER_USER,
            detach=True,
            command="tail -f /dev/null",
            platform=test_spec.platform,
            cap_add=cap_add,
        )
'''
new_create = '''        create_kwargs = dict(
            image=test_spec.instance_image_key,
            name=test_spec.get_instance_container_name(run_id),
            user=DOCKER_USER,
            detach=True,
            command="tail -f /dev/null",
            cap_add=cap_add,
            environment={
                "OPENBLAS_NUM_THREADS": "1",
                "OMP_NUM_THREADS": "1",
                "MKL_NUM_THREADS": "1",
                "NUMEXPR_NUM_THREADS": "1",
                "GIT_CONFIG_COUNT": "1",
                "GIT_CONFIG_KEY_0": "core.preloadindex",
                "GIT_CONFIG_VALUE_0": "false",
            },
        )
        try:
            api_version = tuple(int(part) for part in client.api._version.split(".")[:2])
        except Exception:
            api_version = (999, 999)
        if api_version >= (1, 41):
            create_kwargs["platform"] = test_spec.platform

        container = client.containers.create(**create_kwargs)
'''
if old_create in text:
    text = text.replace(old_create, new_create)
elif "create_kwargs = dict(" in text and "OPENBLAS_NUM_THREADS" in text:
    pass
else:
    raise SystemExit("unsupported swebench docker_build.py layout; compat patch not applied")
path.write_text(text)
print(path)
`
	res := runCapture(ctx, "", nil, python, "-c", script)
	if res.ExitCode != 0 {
		return fmt.Errorf("apply harness compat patch: %s %s", res.Error, strings.TrimSpace(res.Stdout+"\n"+res.Stderr))
	}
	return nil
}
