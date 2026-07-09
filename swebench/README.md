# SWE-Bench Verified

SWE-Bench Verified evaluates software-engineering agents on real GitHub issues.
This benchmark provides a reproducible path to run the official 500-case test
split, verify patches with the official local harness, and compare
mini-SWE-agent against a Go-native `trpc-agent-go` implementation.

## Results

The committed baseline summary is
[`results/baseline-mini-swe-agent-m2.5.json`](results/baseline-mini-swe-agent-m2.5.json).

| Metric | Value |
| --- | ---: |
| Total cases | 500 |
| Resolved | 382 |
| Unresolved | 117 |
| Empty patch | 1 |
| Infra error | 0 |
| Incomplete | 0 |
| Resolved rate | 76.4% |

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
cd swebench/evaluator

go run . doctor \
  --run-id swebench-doctor \
  --output ../results/runs/doctor \
  --model-config ../config/models/glm-5.2.local.yaml
```

The command prints a concise `ok/fail` summary and writes the full details to
`../results/runs/doctor/doctor.json`.

### 4. Download dataset

```bash
go run . prepare-data --python python
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
go run . verify \
  --run-id <run-id> \
  --target baseline \
  --predictions <path-to-preds.json> \
  --output ../results/runs/<run-id>/local-harness-report/baseline \
  --harness-workers 1
```

If the environment uses an internal httpbin proxy for `psf/requests` cases,
keep the verifier URL host as `httpbin.org` and let Docker `extra_hosts` route
it to the local proxy. Add these flags to the `verify` command:

```bash
--httpbin-url http://httpbin.org --httpbin-ca-bundle <path-to-ca-bundle>
```

For subset predictions, `verify` restricts the official harness to the
prediction instance ids by default.

### 7. Normalize results

```bash
go run . import \
  --target baseline \
  --cases ../data/generated/cases.jsonl \
  --predictions <path-to-preds.json> \
  --raw-dir <path-to-raw-run-dir> \
  --harness-report <path-to-harness-report.json> \
  --output ../results/runs/<run-id>/imported
```

This converts predictions and verifier output into the common case-level result
format used by reports.
