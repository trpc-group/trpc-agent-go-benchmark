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

const (
	verifierModeUpstream   = "upstream"
	verifierModeCompat     = "compat"
	verifierModeCalibrated = "calibrated"
)

type verifyManifest struct {
	RunID              string              `json:"run_id"`
	Target             string              `json:"target"`
	StartedAt          time.Time           `json:"started_at"`
	FinishedAt         time.Time           `json:"finished_at"`
	DurationMS         int64               `json:"duration_ms"`
	Command            commandResult       `json:"command"`
	Config             verifyConfig        `json:"config"`
	HarnessPatched     bool                `json:"harness_patched"`
	CalibrationPatches []string            `json:"calibration_patches,omitempty"`
	ManagedHTTPBin     *managedHTTPBinInfo `json:"managed_httpbin,omitempty"`
}

type verifyConfig struct {
	Dataset      string   `json:"dataset"`
	Split        string   `json:"split"`
	Instance     string   `json:"instance,omitempty"`
	InstanceIDs  []string `json:"instance_ids,omitempty"`
	Predictions  string   `json:"predictions"`
	OutputDir    string   `json:"output_dir"`
	Workers      int      `json:"workers"`
	CacheLevel   string   `json:"cache_level"`
	Clean        bool     `json:"clean"`
	Python       string   `json:"python"`
	Docker       string   `json:"docker"`
	DockerHost   string   `json:"docker_host"`
	HFHome       string   `json:"hf_home,omitempty"`
	VerifierMode string   `json:"verifier_mode"`
	CompatPatch  bool     `json:"compat_patch"`
}

func runVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	runID := fs.String("run-id", "", "run id")
	target := fs.String("target", targetBaseline, targetHelp)
	predictions := fs.String("predictions", "", "predictions JSON/JSONL path")
	output := fs.String("output", "", "output directory; defaults to results/runs/<run-id>/local-harness-report/<target>")
	dataset := fs.String("dataset", defaultDatasetName, "SWE-Bench dataset name")
	split := fs.String("split", defaultSplit, "dataset split")
	instance := fs.String("instance", "", "optional single instance id")
	instancesFromPredictions := fs.Bool("instances-from-predictions", true, "restrict harness dataset to instance ids found in predictions")
	workers := fs.Int("harness-workers", 1, "SWE-Bench harness max workers")
	cacheLevel := fs.String("cache-level", "instance", "SWE-Bench harness cache level")
	clean := fs.Bool("clean", false, "clean harness images/containers")
	python := fs.String("python", envOrDefault("PYTHON", "python"), "python executable")
	docker := fs.String("docker", envOrDefault("DOCKER", "docker"), "docker executable")
	dockerHost := fs.String("docker-host", envOrDefault("DOCKER_HOST", defaultDockerHost), "Docker host")
	hfHome := fs.String("hf-home", os.Getenv("HF_HOME"), "HF_HOME cache path")
	verifierMode := fs.String("verifier-mode", verifierModeCalibrated, "verifier mode: upstream, compat, or calibrated")
	compatPatch := fs.Bool("apply-harness-compat", true, "deprecated compatibility flag; set false for clean-upstream comparison")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "run-id", *runID); err != nil {
		return err
	}
	if err := required(fs, "predictions", *predictions); err != nil {
		return err
	}
	if err := validateTarget(*target); err != nil {
		return err
	}
	if *workers < 1 {
		return fmt.Errorf("harness-workers must be >= 1")
	}
	mode, err := resolveVerifierMode(*verifierMode, *compatPatch, flagWasSet(fs, "verifier-mode"), flagWasSet(fs, "apply-harness-compat"))
	if err != nil {
		return err
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID, "local-harness-report", *target)
	}
	outputAbs := absPath(*output)
	if err := ensureDir(*output); err != nil {
		return err
	}

	var managedHTTPBin *managedHTTPBin
	if mode == verifierModeCalibrated {
		managedHTTPBin, err = ensureManagedHTTPBin(ctx, *docker, *dockerHost)
		if err != nil {
			return err
		}
		defer managedHTTPBin.Close(context.Background())
	}

	patches, err := applyHarnessPatch(ctx, *python, mode)
	if err != nil {
		return err
	}
	patched := len(patches) > 0

	instanceIDs, err := verifyInstanceIDs(*predictions, *instance, *instancesFromPredictions)
	if err != nil {
		return err
	}

	harnessRunID := *runID + "-" + *target
	cmdArgs := []string{
		"-m", "swebench.harness.run_evaluation",
		"-d", *dataset,
		"-s", *split,
		"-p", harnessPredictionsArg(*predictions),
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

	env := verifyEnvironment(*dockerHost, *hfHome, managedHTTPBin)

	start := time.Now()
	logPath := filepath.Join(outputAbs, "verify.log")
	result := runLogged(ctx, outputAbs, env, logPath, *python, cmdArgs...)
	finish := time.Now()

	manifest := verifyManifest{
		RunID:              *runID,
		Target:             *target,
		StartedAt:          start.UTC(),
		FinishedAt:         finish.UTC(),
		DurationMS:         finish.Sub(start).Milliseconds(),
		Command:            result,
		HarnessPatched:     patched,
		CalibrationPatches: patches,
		ManagedHTTPBin:     managedHTTPBinInfoPtr(managedHTTPBin),
		Config: verifyConfig{
			Dataset:      *dataset,
			Split:        *split,
			Instance:     *instance,
			InstanceIDs:  instanceIDs,
			Predictions:  harnessPredictionsArg(*predictions),
			OutputDir:    outputAbs,
			Workers:      *workers,
			CacheLevel:   *cacheLevel,
			Clean:        *clean,
			Python:       *python,
			Docker:       *docker,
			DockerHost:   *dockerHost,
			HFHome:       *hfHome,
			VerifierMode: mode,
			CompatPatch:  mode != verifierModeUpstream,
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

func flagWasSet(fs *flag.FlagSet, name string) bool {
	wasSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			wasSet = true
		}
	})
	return wasSet
}

func resolveVerifierMode(mode string, compatPatch bool, modeSet bool, compatPatchSet bool) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = verifierModeCalibrated
	}
	switch mode {
	case verifierModeUpstream, verifierModeCompat, verifierModeCalibrated:
	default:
		return "", fmt.Errorf("verifier-mode must be one of %s, %s, %s", verifierModeUpstream, verifierModeCompat, verifierModeCalibrated)
	}
	if compatPatchSet && !compatPatch {
		if modeSet && mode != verifierModeUpstream {
			return "", fmt.Errorf("--apply-harness-compat=false conflicts with --verifier-mode=%s", mode)
		}
		return verifierModeUpstream, nil
	}
	return mode, nil
}

func verifyEnvironment(dockerHost, hfHome string, managedHTTPBin *managedHTTPBin) []string {
	env := []string{
		"DOCKER_HOST=" + dockerHost,
		"SWEBENCH_HTTPBIN_URL=",
		"SWEBENCH_HTTPBIN_CA_BUNDLE=",
	}
	if managedHTTPBin != nil {
		env[1] = "SWEBENCH_HTTPBIN_URL=http://" + managedHTTPBin.Info.PublicHost
		env[2] = "SWEBENCH_HTTPBIN_CA_BUNDLE=" + managedHTTPBin.Info.CABundle
	}
	if strings.TrimSpace(hfHome) != "" {
		env = append(env, "HF_HOME="+hfHome)
	}
	return env
}

func managedHTTPBinInfoPtr(managed *managedHTTPBin) *managedHTTPBinInfo {
	if managed == nil {
		return nil
	}
	info := managed.Info
	return &info
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
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func applyHarnessPatch(ctx context.Context, python string, mode string) ([]string, error) {
	if mode == verifierModeUpstream {
		return nil, nil
	}
	script := `
from pathlib import Path
import inspect
import swebench.harness.docker_build as db

mode = __MODE__
patches = []

def backup(path, text):
    bak = path.with_suffix(path.suffix + ".bak-swebench-compat")
    if not bak.exists():
        bak.write_text(text)

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
new_create = '''        environment = {
            "OPENBLAS_NUM_THREADS": "1",
            "OMP_NUM_THREADS": "1",
            "MKL_NUM_THREADS": "1",
            "NUMEXPR_NUM_THREADS": "1",
            "GIT_CONFIG_COUNT": "1",
            "GIT_CONFIG_KEY_0": "core.preloadindex",
            "GIT_CONFIG_VALUE_0": "false",
        }
        volumes = {}
        extra_hosts = {}
        use_httpbin = test_spec.instance_id.startswith("psf__requests-")
        if use_httpbin and os.environ.get("SWEBENCH_HTTPBIN_URL"):
            environment["HTTPBIN_URL"] = os.environ["SWEBENCH_HTTPBIN_URL"]
            extra_hosts["host.docker.internal"] = "host-gateway"
            extra_hosts["httpbin.org"] = "host-gateway"
        if use_httpbin and os.environ.get("SWEBENCH_HTTPBIN_CA_BUNDLE"):
            ca_bundle = os.environ["SWEBENCH_HTTPBIN_CA_BUNDLE"]
            ca_target = "/testbed/requests/cacert.pem"
            volumes[ca_bundle] = {"bind": ca_target, "mode": "ro"}
            environment["REQUESTS_CA_BUNDLE"] = ca_target
            environment["SSL_CERT_FILE"] = ca_target
            environment["CURL_CA_BUNDLE"] = ca_target

        create_kwargs = dict(
            image=test_spec.instance_image_key,
            name=test_spec.get_instance_container_name(run_id),
            user=DOCKER_USER,
            detach=True,
            command="tail -f /dev/null",
            cap_add=cap_add,
            environment=environment,
            volumes=volumes or None,
            extra_hosts=extra_hosts or None,
        )
        try:
            api_version = tuple(int(part) for part in client.api._version.split(".")[:2])
        except Exception:
            api_version = (999, 999)
        if api_version >= (1, 41):
            create_kwargs["platform"] = test_spec.platform

        container = client.containers.create(**create_kwargs)
'''

docker_path = Path(inspect.getsourcefile(db))
docker_text = docker_path.read_text()
backup(docker_path, docker_text)
if "import os" not in docker_text:
    if "from __future__ import annotations\n\n" in docker_text:
        docker_text = docker_text.replace("from __future__ import annotations\n\n", "from __future__ import annotations\n\nimport os\n", 1)
    else:
        docker_text = "import os\n" + docker_text
if old_create in docker_text:
    docker_text = docker_text.replace(old_create, new_create)
    patches.append("docker_container_env")
elif "create_kwargs = dict(" in docker_text and "OPENBLAS_NUM_THREADS" in docker_text:
    start = docker_text.index("        create_kwargs = dict(")
    marker = "        container = client.containers.create(**create_kwargs)\n"
    end = docker_text.index(marker, start) + len(marker)
    docker_text = docker_text[:start] + new_create + docker_text[end:]
    patches.append("docker_container_env")
else:
    raise SystemExit("unsupported swebench docker_build.py layout; compat patch not applied")
docker_path.write_text(docker_text)

if mode == "calibrated":
    import swebench.harness.log_parsers.python as py_parsers
    parser_path = Path(inspect.getsourcefile(py_parsers))
    parser_text = parser_path.read_text()
    backup(parser_path, parser_text)
    old_parser = "parse_log_astropy = parse_log_pytest_v2\n"
    new_parser = '''def parse_log_astropy(log: str, test_spec: TestSpec) -> dict[str, str]:
    test_status_map = parse_log_pytest_v2(log, test_spec)
    for test_name, status in list(test_status_map.items()):
        if test_name.endswith("[unit0]"):
            test_status_map.setdefault(f"{test_name[:-7]}[]", status)
    return test_status_map
'''
    if old_parser in parser_text:
        parser_text = parser_text.replace(old_parser, new_parser)
        patches.append("astropy_log_parser_unit0")
    elif "def parse_log_astropy" in parser_text and "unit0" in parser_text:
        patches.append("astropy_log_parser_unit0")
    else:
        raise SystemExit("unsupported swebench python log parser layout; calibrated patch not applied")
    parser_path.write_text(parser_text)

    import swebench.harness.test_spec.python as py_spec
    spec_path = Path(inspect.getsourcefile(py_spec))
    spec_text = spec_path.read_text()
    backup(spec_path, spec_text)
    anchor = '''    if "install" in specs:
        eval_commands.append(specs["install"])
'''
    injection = '''    if "install" in specs:
        eval_commands.append(specs["install"])
    pip_trusted_hosts = "--trusted-host pypi.org --trusted-host files.pythonhosted.org"
    if instance["repo"] == "psf/requests":
        eval_commands.append(f"python -m pip install {pip_trusted_hosts} pytest-httpbin trustme")
    if instance["repo"] == "astropy/astropy" and instance["version"] == "3.1":
        eval_commands.append(f"python -m pip install {pip_trusted_hosts} pytest==6.2.5 setuptools==59.8.0")
    if instance["repo"] == "django/django" and instance["version"] == "2.2":
        shim = (
            "import sqlite3 as _sqlite3\\n"
            "import sqlite3.dbapi2 as _dbapi2\\n"
            "__orig_connect = _dbapi2.connect\\n"
            "def _connect(*args, **kwargs):\\n"
            "    conn = __orig_connect(*args, **kwargs)\\n"
            "    try:\\n"
            "        conn.execute('PRAGMA legacy_alter_table=ON')\\n"
            "    except Exception:\\n"
            "        pass\\n"
            "    return conn\\n"
            "_sqlite3.connect = _connect\\n"
            "_dbapi2.connect = _connect\\n"
        )
        eval_commands.append(
            "python - <<'PY'\\n"
            "from pathlib import Path\\n"
            "import site\\n"
            "paths = site.getsitepackages()\\n"
            "target = Path(paths[0]) / 'sitecustomize.py'\\n"
            f"target.write_text({shim!r})\\n"
            "print(target)\\n"
            "PY"
        )
'''
    if "pytest-httpbin trustme" in spec_text and "legacy_alter_table" in spec_text and "pytest==6.2.5" in spec_text and "files.pythonhosted.org" in spec_text:
        patches.extend(["requests_httpbin_runtime_deps", "astropy31_pytest_setuptools_pin", "django22_sqlite_legacy_alter_table"])
    elif anchor in spec_text:
        spec_text = spec_text.replace(anchor, injection, 1)
        patches.extend(["requests_httpbin_runtime_deps", "astropy31_pytest_setuptools_pin", "django22_sqlite_legacy_alter_table"])
    else:
        raise SystemExit("unsupported swebench python test_spec layout; calibrated patch not applied")
    spec_path.write_text(spec_text)

print("\\n".join(dict.fromkeys(patches)))
`
	script = strings.ReplaceAll(script, "__MODE__", strconv.Quote(mode))
	res := runCapture(ctx, "", nil, python, "-c", script)
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("apply harness %s patch: %s %s", mode, res.Error, strings.TrimSpace(res.Stdout+"\n"+res.Stderr))
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	patches := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			patches = append(patches, line)
		}
	}
	return patches, nil
}
