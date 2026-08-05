# SWE-Bench Verified

This directory provides the shared contracts and evaluator tooling used to run
software-engineering agents on the official 500-case SWE-Bench Verified test
split.

The core layer is deliberately agent-neutral. It prepares a safe case manifest,
runs external prediction producers, invokes the unmodified upstream SWE-Bench
local harness, and normalizes the resulting artifacts. Agent implementations
can be added without changing the evaluator contract.

The optional Mini-Go lane is a source-aligned Go port of mini-SWE-agent v2.1.0.
It reuses tRPC-Agent-Go's public model and tool types, but intentionally keeps
mini-SWE-agent's explicit control loop. It is therefore a reference lane, not
the native `llmagent` runner.

## Scope

The committed core includes:

- the canonical 500-instance case list and checksum;
- a safe dataset projection that excludes gold patches and test metadata;
- prediction, runner-manifest, verifier-manifest, and result contracts;
- an adapter for the external mini-SWE-agent reference implementation;
- shared Docker-environment and XML-like/JSON/text observation codecs;
- a golden-tested, source-aligned Mini-Go reference runner;
- the unmodified upstream local harness invocation;
- batch planning, resumable shard inspection, and deterministic prediction
  merging.

Runtime workspaces, model credentials, raw datasets, predictions, traces,
patches, and harness logs are intentionally excluded from git.

## Layout

```text
swebench/
  config/                 # Secret-safe model and environment templates.
  data/                   # Canonical case list and generated safe metadata.
  evaluator/              # Shared preparation, verification, and import CLI.
  internal/               # Artifact, contract, environment, and codec packages.
  mini-swe-agent-impl/    # External reference-runner instructions.
  mini-swe-agent-go-impl/ # Source-aligned Mini-Go reference runner.
  results/                # Ignored runtime outputs and future summaries.
```

## Dataset Contract

| Item | Value |
| --- | --- |
| Dataset | `princeton-nlp/SWE-bench_Verified` |
| Split | `test` |
| Cases | 500 |
| Case list | `data/case-lists/verified-test-500.case_ids.txt` |
| Case-list SHA-256 | `a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9` |

Generated runner inputs contain only `instance_id`, `repo`, `base_commit`,
`problem_statement`, and optionally `hints_text`. They never contain `patch`,
`test_patch`, `FAIL_TO_PASS`, or `PASS_TO_PASS`.

## Quick Start

Run commands from the repository root. A Linux host with Docker, Go 1.21+, and
Python 3.11+ is recommended.

### 1. Install the official harness

Create an isolated Python environment and install a reviewed SWE-Bench
revision. Record the installed package version and Git revision with every run.

```bash
python3.11 -m venv swebench/results/runs/.venv
source swebench/results/runs/.venv/bin/activate
pip install -U pip

mkdir -p swebench/results/runs/repos
git clone https://github.com/SWE-bench/SWE-bench.git \
  swebench/results/runs/repos/SWE-bench
pip install -e swebench/results/runs/repos/SWE-bench
```

The evaluator invokes this installed upstream package without modifying its
Python source.

### 2. Configure an OpenAI-compatible model

```bash
cp swebench/config/models/openai-compatible.yaml.example \
  swebench/config/models/openai-compatible.local.yaml
```

Fill in the local endpoint, model, credential, and optional headers. Files
matching `*.local.yaml` are ignored by git.

### 3. Check the environment

```bash
cd swebench

go run ./evaluator doctor \
  --run-id swebench-doctor \
  --output results/runs/doctor \
  --model-config config/models/openai-compatible.local.yaml
```

### 4. Prepare the safe case manifest

```bash
go run ./evaluator prepare-data --python python
```

For the canonical dataset and split, preparation fails closed unless the
generated instance IDs match the committed list and checksum.

### 5. Produce and verify predictions

Choose either the
[`external mini-SWE-agent runner`](mini-swe-agent-impl/README.md), the
[`source-aligned Mini-Go runner`](mini-swe-agent-go-impl/README.md), or any
other prediction producer that follows the shared contract.

```bash
go run ./evaluator verify \
  --run-id <run-id> \
  --target <target-label> \
  --predictions <path-to-preds.json> \
  --output results/runs/<run-id>/local-harness-report/<target-label> \
  --harness-workers 1 \
  --instance-timeout-seconds 1800
```

By default the harness is restricted to instance IDs present in the prediction
file. The verifier manifest records the installed SWE-Bench version, discoverable
Git revision, package path, exact command, and runtime configuration.

`<target-label>` is an agent-neutral lowercase slug such as `baseline`,
`mini-go`, or `tag`. Prediction readers accept SWE-Bench map JSON, array JSON,
or JSONL, and fail on empty input, duplicate IDs, unsafe IDs, or disagreement
between a map key and its nested `instance_id`.

### 6. Normalize artifacts

```bash
go run ./evaluator import \
  --target <target-label> \
  --cases data/generated/cases.jsonl \
  --predictions <path-to-preds.json> \
  --harness-report <path-to-harness-report.json> \
  --output results/runs/<run-id>/imported
```

Use `run-config` after generation, verification, and import have produced their
manifests. See [`evaluator/README.md`](evaluator/README.md) for the complete CLI.

The importer writes a target-neutral versioned row for every canonical case:

```json
{
  "schema_version": 1,
  "instance_id": "example__repo-123",
  "target": "tag",
  "result": {
    "main_status": "resolved",
    "patch_stats": {},
    "usage": {}
  }
}
```

`run-config` accepts exactly one runner provenance source and rejects mismatched
run IDs, targets, datasets, case counts, summary counts, or prediction paths.

## Validation

```bash
cd swebench
go test ./...
go test -race ./...
go vet ./...
```

Official-harness smoke tests require Linux, Docker, the installed Python
package, and a prediction for at least one instance. They are not part of the
unit-test suite.
