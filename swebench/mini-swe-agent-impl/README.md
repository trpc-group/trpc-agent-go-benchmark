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
pip install fastapi orjson
```

Record the actual commit in `run_config.json`.

## Smoke Run

```bash
cd swebench

RUN_ID=baseline-mini-glm50-smoke
MODEL_CONFIG=config/models/glm-5.0.local.yaml

go run ./evaluator run-mini \
  --run-id "$RUN_ID" \
  --subset verified \
  --split test \
  --filter '^astropy__astropy-12907$' \
  --agent-workers 1 \
  --output "results/runs/$RUN_ID/raw/mini" \
  --model-config "$MODEL_CONFIG" \
  --environment-config config/environments/swebench-testbed.yaml \
  --redo-existing

go run ./evaluator verify \
  --run-id "$RUN_ID" \
  --target baseline \
  --predictions "results/runs/$RUN_ID/raw/mini/preds.json" \
  --output "results/runs/$RUN_ID/local-harness-report/baseline" \
  --harness-workers 1

HARNESS_REPORT=$(find "results/runs/$RUN_ID/local-harness-report/baseline" -name '*.json' ! -name verifier_manifest.json | head -n 1)

go run ./evaluator import \
  --target baseline \
  --cases data/generated/cases.jsonl \
  --predictions "results/runs/$RUN_ID/raw/mini/preds.json" \
  --raw-dir "results/runs/$RUN_ID/raw/mini" \
  --harness-report "$HARNESS_REPORT" \
  --output "results/runs/$RUN_ID/imported"

go run ./evaluator run-config \
  --run-id "$RUN_ID" \
  --target baseline \
  --cases-manifest data/generated/cases.manifest.json \
  --run-mini-manifest "results/runs/$RUN_ID/raw/mini/run-mini-manifest.json" \
  --verifier-manifest "results/runs/$RUN_ID/local-harness-report/baseline/verifier_manifest.json" \
  --import-summary "results/runs/$RUN_ID/imported/summary/baseline.json" \
  --harness-report "$HARNESS_REPORT" \
  --doctor results/runs/doctor/doctor.json \
  --model-name glm-5.0 \
  --mini-model-name openai/glm-5.0 \
  --reasoning-effort high \
  --timeout 120 \
  --output "results/runs/$RUN_ID/run_config.json"
```

The smoke run should write `preds.json`, a trajectory, a verifier report,
normalized imported results, and `run_config.json`.

## Full 500-Case Run

After the smoke run succeeds, run the full SWE-Bench Verified split. Sharding is
recommended because it makes endpoint failures recoverable at batch granularity.

```bash
cd swebench

RUN_PREFIX=baseline-mini-glm50-full
PLAN_DIR=data/generated/batches/$RUN_PREFIX
MODEL_CONFIG=config/models/glm-5.0.local.yaml

go run ./evaluator plan-batches \
  --cases data/generated/cases.jsonl \
  --output-dir "$PLAN_DIR" \
  --run-prefix "$RUN_PREFIX" \
  --batch-size 20
```

Run every planned batch:

```bash
for filter_file in "$PLAN_DIR"/batch-*.filter; do
  batch_name=$(basename "$filter_file" .filter)
  batch_index=${batch_name#batch-}
  run_id="$RUN_PREFIX-$batch_index"

  go run ./evaluator run-mini \
    --run-id "$run_id" \
    --subset verified \
    --split test \
    --filter "$(cat "$filter_file")" \
    --agent-workers 15 \
    --output "results/runs/$run_id/raw/mini" \
    --model-config "$MODEL_CONFIG" \
    --environment-config config/environments/swebench-testbed.yaml \
    --redo-existing
done
```

Summarize shards and merge predictions:

```bash
go run ./evaluator summarize-shards \
  --plan "$PLAN_DIR/plan.json" \
  --runs-root results/runs \
  --output "results/runs/$RUN_PREFIX/shards.json"

go run ./evaluator merge-predictions \
  --shards "results/runs/$RUN_PREFIX/shards.json" \
  --cases data/generated/cases.jsonl \
  --output "results/runs/$RUN_PREFIX/preds.json"
```

Verify, import, and package the full run:

```bash
go run ./evaluator verify \
  --run-id "$RUN_PREFIX" \
  --target baseline \
  --predictions "results/runs/$RUN_PREFIX/preds.json" \
  --output "results/runs/$RUN_PREFIX/local-harness-report/baseline" \
  --harness-workers 8

HARNESS_REPORT=$(find "results/runs/$RUN_PREFIX/local-harness-report/baseline" -name '*.json' ! -name verifier_manifest.json | head -n 1)

go run ./evaluator import \
  --target baseline \
  --cases data/generated/cases.jsonl \
  --predictions "results/runs/$RUN_PREFIX/preds.json" \
  --shards-manifest "results/runs/$RUN_PREFIX/shards.json" \
  --harness-report "$HARNESS_REPORT" \
  --output "results/runs/$RUN_PREFIX/imported"

go run ./evaluator run-config \
  --run-id "$RUN_PREFIX" \
  --target baseline \
  --cases-manifest data/generated/cases.manifest.json \
  --shards-manifest "results/runs/$RUN_PREFIX/shards.json" \
  --verifier-manifest "results/runs/$RUN_PREFIX/local-harness-report/baseline/verifier_manifest.json" \
  --import-summary "results/runs/$RUN_PREFIX/imported/summary/baseline.json" \
  --harness-report "$HARNESS_REPORT" \
  --doctor results/runs/doctor/doctor.json \
  --model-name glm-5.0 \
  --mini-model-name openai/glm-5.0 \
  --reasoning-effort high \
  --timeout 120 \
  --output "results/runs/$RUN_PREFIX/run_config.json"
```

The full run is expected to be long-running. Keep `results/runs/` as the runtime
artifact directory; it is ignored by git.
