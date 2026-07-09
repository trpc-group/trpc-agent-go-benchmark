# mini-SWE-agent Baseline Adapter

This directory contains the commands for the mini-SWE-agent baseline. Model
config is prepared from the benchmark root README.

Run commands from the benchmark repository root unless noted otherwise.

## Install mini-SWE-agent

```bash
source swebench/results/runs/.venv/bin/activate

mkdir -p swebench/results/runs/repos
git clone https://github.com/SWE-agent/mini-swe-agent.git swebench/results/runs/repos/mini-swe-agent
git -C swebench/results/runs/repos/mini-swe-agent checkout v2.1.0
pip install -e swebench/results/runs/repos/mini-swe-agent
```

Record the actual commit in `run_config.json`.

## Smoke Run

```bash
cd swebench/evaluator

go run . run-mini \
  --run-id mini-smoke-astropy-12907 \
  --subset verified \
  --split test \
  --filter '^astropy__astropy-12907$' \
  --agent-workers 1 \
  --output ../results/runs/mini-smoke-astropy-12907/raw/mini \
  --model-config ../config/models/glm-5.2.local.yaml \
  --environment-config ../config/environments/swebench-testbed.yaml \
  --redo-existing

go run . verify \
  --run-id mini-smoke-astropy-12907 \
  --target baseline \
  --predictions ../results/runs/mini-smoke-astropy-12907/raw/mini/preds.json \
  --output ../results/runs/mini-smoke-astropy-12907/local-harness-report/baseline \
  --harness-workers 1
```

`run-mini` writes `preds.json` and trajectory files under the raw output
directory. `verify` writes a local-harness report with one submitted instance.

## Full 500-Case Run

After the smoke run and verifier succeed, run the full SWE-Bench Verified split:

```bash
cd swebench/evaluator

go run . run-mini \
  --run-id baseline-mini-glm-5.2-full \
  --subset verified \
  --split test \
  --agent-workers 1 \
  --output ../results/runs/baseline-mini-glm-5.2-full/raw/mini \
  --model-config ../config/models/glm-5.2.local.yaml \
  --environment-config ../config/environments/swebench-testbed.yaml \
  --redo-existing

go run . verify \
  --run-id baseline-mini-glm-5.2-full \
  --target baseline \
  --predictions ../results/runs/baseline-mini-glm-5.2-full/raw/mini/preds.json \
  --output ../results/runs/baseline-mini-glm-5.2-full/local-harness-report/baseline \
  --harness-workers 8
```

Then import and package the run:

```bash
go run . import \
  --target baseline \
  --cases ../data/generated/cases.jsonl \
  --predictions ../results/runs/baseline-mini-glm-5.2-full/raw/mini/preds.json \
  --raw-dir ../results/runs/baseline-mini-glm-5.2-full/raw/mini \
  --harness-report <path-to-harness-report.json> \
  --output ../results/runs/baseline-mini-glm-5.2-full/imported
```

The full run is expected to be long-running. Keep `results/runs/` as the runtime
artifact directory; it is ignored by git.
