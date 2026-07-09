# SWE-Bench Evaluator

This Go module is the shared evaluation layer for SWE-Bench Verified runs.

It owns dataset manifest generation, official local harness invocation, result
import, status normalization, secret scrubbing, batch planning, and run
manifest assembly.

## File Layout

```text
evaluator/
  README.md
  go.mod
  main.go              # Thin CLI entrypoint.
  internal/cli/        # Command implementations and helpers.
```

Most readers only need the commands documented in the root SWE-Bench README.
The `internal/cli` package contains implementation details.

## Commands

| Command | Purpose |
| --- | --- |
| `doctor` | Check Python, Docker, SWE-Bench, mini-SWE-agent, dataset loading, and model access. |
| `prepare-data` | Download/load SWE-Bench Verified, validate the committed 500-case list, and write generated dataset metadata. |
| `run-mini` | Run mini-SWE-agent and write raw predictions/traces. |
| `verify` | Run the official local SWE-Bench harness. |
| `import` | Normalize predictions, traces, and verifier output into case-level results. |
| `run-config` | Build a run-level manifest from generated artifacts. |
| `plan-batches` | Build fixed case batches and mini-SWE-agent filters. |

## Local Compatibility Notes

Some SWE-Bench repositories are old projects with dependency stacks that live in
the official `testbed` conda environment. The mini-SWE-agent environment config
at `../config/environments/swebench-testbed.yaml` makes agent shell commands run
inside that environment instead of the container default Python.

`verify` uses the local harness compatibility patch by default. It avoids
passing `platform` when Docker API `<1.41` and injects conservative thread/Git
environment variables into harness containers. It does not change the dataset,
test lists, submitted patches, or SWE-Bench pass/fail logic. The verifier
manifest records `compat_patch: true`; use `--apply-harness-compat=false` only
for clean-upstream comparisons.

Current quick start status:

| Compatibility item | In quick start? | Notes |
| --- | --- | --- |
| mini-SWE-agent command environment | Yes | `run-mini` passes `../config/environments/swebench-testbed.yaml`, so agent shell commands run inside the official `testbed` conda environment. |
| Docker API / seccomp harness patch | Yes | Enabled by default in `verify`; disable with `--apply-harness-compat=false` only for clean-upstream comparison. |
| Case-specific calibrated verifier patches | No | Historical audits found examples such as astropy pytest/runtime fixes and a Django 2.2 SQLite `legacy_alter_table` shim. These are not implemented in this module's quick start path. |

Case-specific verifier changes, such as dependency pins, network mirrors,
SQLite shims, or log-parser changes, must be implemented as explicit evaluator
behavior and recorded in the run manifest before they are used for a reported
result.
