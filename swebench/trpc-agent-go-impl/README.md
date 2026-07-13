# tRPC-Agent-Go SWE Agent

This directory contains the Go-native SWE-Bench agent. It preserves the key
mini-swe-agent v2.1 behavior while using `trpc-agent-go` for the model/tool loop:

- one `bash` tool and a linear 250-step limit;
- one official SWE-Bench Docker testbed per instance, rooted at `/testbed`;
- mini-compatible command observations and 10,000-character truncation;
- the `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` submission protocol;
- concurrent cases, incremental atomic predictions, and per-case trajectories;
- resumable runs, live LLM/tool progress, and endpoint-error classification.

Docker is only required when cases are executed. Unit and integration tests use
fake environments and a mock model.

## Run

From `swebench/`:

```bash
go run ./trpc-agent-go-impl \
  --run-id native-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/<model>.yaml \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/native-smoke/raw/native \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1
```

Useful runtime controls are `--command-timeout`, `--case-timeout`, and
`--docker-host`. Existing complete cases are skipped by default. Retryable
endpoint/environment failures and incomplete artifacts are attempted again;
`--redo-existing` deliberately reruns every selected case. The output directory
contains:

```text
preds.json
native-runner-manifest.json
native-runner-progress.json
<instance_id>/<instance_id>.traj.json
```

`preds.json` is directly consumable by the official SWE-Bench harness. The
shared evaluator's import, shard-summary, and run-config commands accept the
native artifacts without conversion.

`native-runner-progress.json` is updated while cases are active. It includes
`last_llm_at`, event/LLM/tool counts, and final error categories. The manifest's
`service_error_counts` contains only model endpoint errors, so agent outcomes
such as `LimitsExceeded`, empty patches, and unresolved patches do not look like
endpoint concurrency failures.

## Sharded 500-case run

The supervisor creates 10 fixed 50-case shards, runs them serially, and resumes
without deleting completed artifacts:

```bash
./trpc-agent-go-impl/run-sharded.sh \
  --run-prefix trpc-glm52-full500-$(date +%Y%m%d) \
  --model-config config/models/glm-5.2.local.yaml \
  --initial-workers 10 \
  --max-workers 15
```

After a stable shard, concurrency increases by one. Final transient endpoint
errors or ten minutes with no LLM result reduce concurrency by half and retry
only incomplete/retryable cases. At one worker the no-progress window becomes
30 minutes. Permanent endpoint/configuration errors pause the supervisor. On
completion it writes the shard summary and merged predictions under
`results/runs/<run-prefix>/`.

## Validate without Docker

```bash
go test ./trpc-agent-go-impl/...
go test -race ./trpc-agent-go-impl/...
```
