# tRPC-Agent-Go SWE Agent

This directory contains the Go-native SWE-Bench agent. It preserves the key
mini-swe-agent v2.1 behavior while using `trpc-agent-go` for the model/tool loop:

- one `bash` tool and a linear 250-step limit;
- one official SWE-Bench Docker testbed per instance, rooted at `/testbed`;
- mini-compatible command observations and 10,000-character truncation;
- the `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` submission protocol;
- concurrent cases, incremental atomic predictions, and per-case trajectories.

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
`--docker-host`. The output directory contains:

```text
preds.json
native-runner-manifest.json
<instance_id>/<instance_id>.traj.json
```

`preds.json` is directly consumable by the official SWE-Bench harness. The
shared evaluator's import, shard-summary, and run-config commands accept the
native artifacts without conversion.

## Validate without Docker

```bash
go test ./trpc-agent-go-impl/...
```
