# SWE-Bench Evaluator

The evaluator is the agent-neutral orchestration CLI for this benchmark.

```text
go run ./evaluator <command> [flags]
```

| Command | Purpose |
| --- | --- |
| `doctor` | Probe Python, Docker, dataset, runner, and model access. |
| `prepare-data` | Generate a safe case manifest and validate the fixed panel. |
| `run-mini` | Invoke an installed mini-SWE-agent batch. |
| `verify` | Invoke the unmodified upstream official local harness. |
| `import` | Normalize predictions and harness outcomes. |
| `run-config` | Assemble a run-level provenance manifest. |
| `plan-batches` | Create deterministic case filters for sharded runs. |
| `summarize-shards` | Validate shard coverage and accepted case artifacts. |
| `merge-predictions` | Deterministically merge validated shard predictions. |

The evaluator writes complete manifests even when an external command fails.
Runtime outputs live under ignored `results/runs/` paths. The verifier records
the installed SWE-Bench package identity and never patches installed harness
source code.

## Artifact boundaries

- `prepare-data` projects only agent-visible fields and checks the default
  Verified/test panel against an embedded 500-case list and SHA-256.
- prediction readers accept map JSON, array JSON, or JSONL, but reject empty
  inputs, duplicate/empty IDs, and map-key mismatches.
- JSON, JSONL, patch, trace, and summary artifacts are replaced atomically.
- `verify` runs `swebench.harness.run_evaluation` from the installed upstream
  package. It does not patch Python source or start auxiliary services.
- `import` writes schema version 1 with one explicit target and result per row.
- `run-config` accepts exactly one of `--run-mini-manifest`,
  `--runner-manifest`, or `--shards-manifest`, then checks provenance across all
  inputs before writing the final manifest.

Target labels must be lowercase slugs. Run IDs and instance IDs are restricted
to path-safe artifact names because they are used below ignored runtime roots.

## Official harness invocation

```bash
go run ./evaluator verify \
  --run-id smoke-1 \
  --target baseline \
  --predictions results/runs/smoke-1/raw/mini/preds.json \
  --harness-workers 1 \
  --instance-timeout-seconds 1800
```

The exact command, timeout, worker count, package version, discoverable Git
revision, and package path are stored in `verifier_manifest.json`.

Run the unit tests from this module:

```bash
cd swebench
go test ./...
go test -race ./...
go vet ./...
```
