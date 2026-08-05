# mini-SWE-agent Reference Adapter

This directory documents the external mini-SWE-agent reference runner. The
package itself is installed into an ignored Python environment; no vendored
runtime or credentials are committed here.

## Install

```bash
source swebench/results/runs/.venv/bin/activate

mkdir -p swebench/results/runs/repos
git clone https://github.com/SWE-agent/mini-swe-agent.git \
  swebench/results/runs/repos/mini-swe-agent
git -C swebench/results/runs/repos/mini-swe-agent checkout v2.1.0
pip install -e swebench/results/runs/repos/mini-swe-agent
pip install fastapi orjson
```

Record the actual commit and installed version in the run provenance.

## One-case smoke

```bash
cd swebench

RUN_ID=baseline-mini-smoke
MODEL_CONFIG=config/models/openai-compatible.local.yaml

go run ./evaluator run-mini \
  --run-id "$RUN_ID" \
  --subset verified \
  --split test \
  --filter '^astropy__astropy-12907$' \
  --agent-workers 1 \
  --timeout 30m \
  --output "results/runs/$RUN_ID/raw/mini" \
  --model-config "$MODEL_CONFIG" \
  --environment-config config/environments/swebench-testbed.yaml
```

Verify the generated prediction with the upstream harness:

```bash
go run ./evaluator verify \
  --run-id "$RUN_ID" \
  --target baseline \
  --predictions "results/runs/$RUN_ID/raw/mini/preds.json" \
  --output "results/runs/$RUN_ID/local-harness-report/baseline" \
  --harness-workers 1
```

For longer runs, use `plan-batches`, invoke `run-mini` once per fixed filter,
then use `summarize-shards` and `merge-predictions`. Empty patches and
limit-exceeded trajectories are valid outcomes; missing/corrupt artifacts and
duplicate accepted cases are not.
