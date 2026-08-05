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
  package. It does not patch Python source or start auxiliary services. On a
  successful command it also binds the unique top-level official report to the
  verifier manifest by harness run ID, path, and SHA-256. File-backed
  predictions are atomically snapshotted before execution; the manifest binds
  the source path, snapshot path, exact-byte SHA-256, and harness `-p` argument.
- `import` writes schema version 1 with one explicit target and result per row;
  filtered or sliced runs can omit `--cases` so prediction IDs define the rows.
- `run-config` accepts exactly one of `--run-mini-manifest`,
  `--runner-manifest`, or `--shards-manifest`, preserves the full prepared panel
  under `dataset`, records the actual prediction-backed run under `selection`,
  and cross-checks that selection against runner, verifier, and imported
  artifacts before writing the final manifest. When a harness report is
  supplied, it also checks the verifier output directory, report filename,
  verify command run ID, and report digest before accepting per-case outcomes.
  Native finalization additionally requires the current runner predictions to
  match the verify-time snapshot digest. Legacy Mini manifests may omit this
  newer attestation.
- `summarize-shards` accepts external mini-SWE-agent, Mini-Go, and Native runner
  manifests while applying the same fixed-plan coverage checks. For clean-room
  Native shards, it also requires one consistent policy and offline asset-tree
  identity, safely merges resolved image provenance, recomputes the image-set
  hash, and validates every accepted case artifact's base-commit and
  environment-image provenance.

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
revision, package path, and official report identity are stored in
`verifier_manifest.json`. Use its `report.path` as `--harness-report` for both
`import` and `run-config`. Native finalization rejects legacy verifier manifests
that lack the verify-time report attestation; legacy Mini manifests remain
readable and receive a finalization-time digest in `run_config.json`.

This binding detects accidental cross-run splicing and report mutation after
verification. It is provenance validation, not a cryptographic signature over
an attacker-modified verifier manifest.

Run the unit tests from this module:

```bash
cd swebench
go test ./...
go test -race ./...
go vet ./...
```
