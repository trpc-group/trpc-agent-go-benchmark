# TAG SWE-Bench Implementation

This runner implements the mini-SWE-agent v2.1 model-facing protocol through
tRPC-Agent-Go (TAG) `llmagent` and `runner` lifecycles. It is distinct from
`mini-swe-agent-go-impl`, which owns an explicit Go control loop.

The TAG lane keeps the behavior that matters to model quality and cost:

- the upstream system/instance prompts and response FormatError recovery;
- one callable `bash` tool, with same-batch calls executed sequentially;
- XML, JSON, and text observation codecs with the same truncation boundary;
- a 250 LLM-call limit and non-streaming model requests;
- submission through `SkipSummarization`, with no following model call;
- OpenAI SDK retry with nine retries after the initial request;
- one official SWE-Bench Docker testbed and one TAG runner per case.

TAG-native events are retained as run artifacts. They are not converted to the
mini-go trajectory format.

## Run

From `swebench/`:

```bash
go run ./trpc-agent-go-impl \
  --run-id tag-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/glm-5.2.local.yaml \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/tag-smoke/raw/tag \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1 \
  --observation-codec xml
```

`--observation-codec` accepts `xml`, `json`, or `text`. Runs resume by skipping
instance IDs already present in `preds.json`; pass `--redo-existing` to rerun
them. `--billing-tag` and `--experiment-id` must be provided together.

Each output directory contains:

```text
preds.json
tag-runner-manifest.json
tag-runner-progress.json
<instance_id>/<instance_id>.tag.json
<instance_id>/<instance_id>.responses.json
```

Verify and import a result with the `tag` evaluator target:

```bash
go run ./evaluator verify \
  --run-id tag-smoke \
  --target tag \
  --predictions results/runs/tag-smoke/raw/tag/preds.json \
  --output results/runs/tag-smoke/local-harness-report/tag \
  --harness-workers 1

go run ./evaluator import \
  --target tag \
  --cases data/generated/cases.jsonl \
  --predictions results/runs/tag-smoke/raw/tag/preds.json \
  --raw-dir results/runs/tag-smoke/raw/tag \
  --harness-report results/runs/tag-smoke/local-harness-report/tag/report.json \
  --output results/runs/tag-smoke/imported
```

Use the actual report path emitted by `evaluator verify` when it differs from
the illustrative `report.json` path above.

## Validate

```bash
go test ./trpc-agent-go-impl/... ./internal/minicompat ./internal/sweenv
go test -race ./trpc-agent-go-impl/internal/executor
```
