# SWE-Bench Verified

SWE-Bench Verified evaluates software-engineering agents on real GitHub issues.
This benchmark provides a reproducible path to run the official 500-case test
split, verify patches with the official local harness, and compare
mini-SWE-agent against a Go-native `trpc-agent-go` implementation.

## Results

MiniMax M2.5 is used to validate the evaluator and baseline chain against a
public SWE-Bench reference point. Subsequent mini-SWE-agent and Go-native
experiments use GLM-5.2-Internal.

| Model | Agent | Resolved Rate |
| --- | --- | ---: |
| MiniMax M2.5 | mini-SWE-agent 2.1.0 | 76.00% |
| GLM-5.2-Internal | mini-SWE-agent 2.1.0 | 76.40%, 76.60%, 78.80% |

Structured summaries are stored in
[`results/baseline-mini-swe-agent-m2.5.json`](results/baseline-mini-swe-agent-m2.5.json)
and
[`results/experiments/mini-swe-agent-glm52-r3.json`](results/experiments/mini-swe-agent-glm52-r3.json).

The full comparison report will be published in
[`results/REPORT.md`](results/REPORT.md) and
[`results/REPORT.zh_CN.md`](results/REPORT.zh_CN.md) after the native
`trpc-agent-go` run is complete.

## Repository Layout

```text
swebench/
  config/                  # Shared local config templates.
    models/                # Model endpoint templates and ignored local config.
    environments/          # mini-SWE-agent runtime environment config.
  data/                    # Fixed case lists and generated dataset metadata.
  evaluator/               # Shared dataset, verifier, importer, and report CLI.
  mini-swe-agent-impl/     # mini-SWE-agent baseline adapter.
  trpc-agent-go-impl/      # Go-native SWE agent implementation.
  results/                 # Reports and small structured summaries.
```

## Dataset

| Item | Value |
| --- | --- |
| Dataset | `princeton-nlp/SWE-bench_Verified` |
| Split | `test` |
| Cases | 500 |
| Case list | `data/case-lists/verified-test-500.case_ids.txt` |
| Case list SHA256 | `a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9` |

`data/` only contains lightweight metadata. It must not contain gold patches,
test patches, hidden test lists, cloned repositories, Docker image caches, or
raw dataset dumps.

## Quick Start

Run the commands from the benchmark repository root.

### 1. Prepare the evaluator environment

Use a Linux machine with Docker, Go 1.21+, and Python 3.11+.

```bash
python3.11 -m venv swebench/results/runs/.venv
source swebench/results/runs/.venv/bin/activate

pip install -U pip

mkdir -p swebench/results/runs/repos
git clone https://github.com/SWE-bench/SWE-bench.git swebench/results/runs/repos/SWE-bench
pip install -e swebench/results/runs/repos/SWE-bench
```

Keep this virtual environment activated for the remaining commands in this
quick start.

For the mini-SWE-agent baseline runner, see
[`mini-swe-agent-impl/README.md`](mini-swe-agent-impl/README.md).

### 2. Configure model access

```bash
cp swebench/config/models/glm-5.2.yaml.example swebench/config/models/glm-5.2.local.yaml
```

Fill in the endpoint, API key, and required gateway headers in the local YAML.
Local model config is ignored by git.

### 3. Check evaluator and model access

```bash
cd swebench

go run ./evaluator doctor \
  --run-id swebench-doctor \
  --output results/runs/doctor \
  --model-config config/models/glm-5.2.local.yaml
```

The command prints a concise `ok/fail` summary and writes the full details to
`results/runs/doctor/doctor.json`.

For a healthy evaluator setup, `doctor` should report `ok` for Python,
SWE-Bench, Docker, dataset loading, managed httpbin, and model smoke checks.
The mini-SWE-agent check is also expected to be `ok` after installing the
baseline runner from `mini-swe-agent-impl/README.md`.

### 4. Download dataset

```bash
go run ./evaluator prepare-data --python python
```

This downloads SWE-Bench Verified if needed, checks it against the committed
500-case list, and writes generated metadata files under `data/generated/`.

### 5. Choose an implementation and produce predictions

Choose an implementation, run it, and keep its SWE-Bench predictions file:

- mini-SWE-agent baseline:
  [`mini-swe-agent-impl/README.md`](mini-swe-agent-impl/README.md)
- Go-native `trpc-agent-go` agent:
  [`trpc-agent-go-impl/README.md`](trpc-agent-go-impl/README.md)

The next steps assume `<path-to-preds.json>` points to that file.

### 6. Verify predictions

After an implementation produces SWE-Bench predictions, verify them with the
official local harness:

```bash
go run ./evaluator verify \
  --run-id <run-id> \
  --target <baseline-or-native> \
  --predictions <path-to-preds.json> \
  --output results/runs/<run-id>/local-harness-report/<baseline-or-native> \
  --harness-workers 1
```

For subset predictions, `verify` restricts the official harness to the
prediction instance ids by default.

### 7. Normalize results

```bash
go run ./evaluator import \
  --target <baseline-or-native> \
  --cases data/generated/cases.jsonl \
  --predictions <path-to-preds.json> \
  --raw-dir <path-to-raw-run-dir> \
  --harness-report <path-to-harness-report.json> \
  --output results/runs/<run-id>/imported
```

This converts predictions and verifier output into the common case-level result
format used by reports.

### 8. Write run config

For a single runner manifest:

```bash
go run ./evaluator run-config \
  --run-id <run-id> \
  --target <baseline-or-native> \
  --cases-manifest data/generated/cases.manifest.json \
  --runner-manifest <path-to-runner-manifest.json> \
  --verifier-manifest results/runs/<run-id>/local-harness-report/<baseline-or-native>/verifier_manifest.json \
  --import-summary results/runs/<run-id>/imported/summary/<baseline-or-native>.json \
  --harness-report <path-to-harness-report.json> \
  --model-name <model-name> \
  --output results/runs/<run-id>/run_config.json
```

For a sharded mini-SWE-agent full run, use `--shards-manifest` instead of
`--runner-manifest`.
