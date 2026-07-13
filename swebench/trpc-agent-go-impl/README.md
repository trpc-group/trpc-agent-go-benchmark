# tRPC-Agent-Go SWE Agent

This is the landing directory for the future Go-native SWE agent implementation.

The implementation will use `trpc-agent-go` to read SWE-Bench instances, operate
on a checked-out repository workspace, generate unified diff patches, and emit
predictions compatible with the shared evaluator.

## Current CLI Skeleton

The current entrypoint only validates the shared SWE-Bench contracts and writes
empty-patch predictions. It is a wiring target for the native agent loop.

Run it from `swebench/`:

```bash
go run ./trpc-agent-go-impl \
  --run-id native-smoke-skeleton \
  --cases data/generated/cases.jsonl \
  --model-config config/models/glm-5.2.local.yaml \
  --output results/runs/native-smoke-skeleton \
  --filter '^(astropy__astropy-12907)$'
```

It writes:

```text
results/runs/<run-id>/preds.json
results/runs/<run-id>/native-runner-manifest.json
```

Future iterations should replace the skeleton case execution with the
tRPC-Agent-Go SWE agent loop while preserving the shared prediction and manifest
contracts.
