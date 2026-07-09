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
| `doctor` | Check Python, Docker, SWE-Bench, mini-SWE-agent, dataset loading, model access, and managed httpbin readiness. |
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

`verify` has three verifier modes:

| Mode | Purpose |
| --- | --- |
| `calibrated` | Default mode for reported local-harness results. It applies the generic Docker/container compatibility patch plus the calibrated patches listed below. |
| `compat` | Applies only the generic Docker/container compatibility patch. Use this when auditing the effect of case-specific calibration. |
| `upstream` | Leaves the installed SWE-Bench harness unchanged. Use this only for clean-upstream comparison. |

The generic compatibility patch avoids passing `platform` when Docker API
`<1.41` and injects conservative thread/Git environment variables into harness
containers. The calibrated mode additionally records each applied patch in
`verifier_manifest.json` and `run_config.json`.

Current calibrated verifier behavior:

| Compatibility item | Applied in `calibrated`? | Notes |
| --- | --- | --- |
| mini-SWE-agent command environment | Yes | `run-mini` passes `../config/environments/swebench-testbed.yaml`, so agent shell commands run inside the official `testbed` conda environment. |
| Docker API / seccomp harness patch | Yes | Enabled by default through `--verifier-mode=calibrated`. |
| astropy log parser calibration | Yes | Maps pytest names ending in `[unit0]` back to the corresponding `[]` form used by some historical expected-test names. |
| astropy 3.1 runtime pin | Yes | Installs `pytest==6.2.5` and `setuptools==59.8.0` for astropy 3.1 eval commands. |
| Django 2.2 SQLite shim | Yes | Injects `PRAGMA legacy_alter_table=ON` through `sitecustomize.py` for Django 2.2 eval commands. |
| managed requests httpbin | Yes | Starts a local managed httpbin backend and HTTP/HTTPS proxy, keeps the in-container host as `httpbin.org`, and mounts the evaluator-managed CA bundle into requests containers. |

These changes do not expose gold patches, hidden tests, or test lists to the
agent. They only affect local harness execution after a prediction has already
been produced.

### Managed httpbin

Public `httpbin.org` is not part of the calibrated verifier path. When
`verify` runs in `calibrated` mode, the evaluator manages this dependency
itself:

```text
results/runs/managed-httpbin/
  certs/
    ca.crt
    ca.key
    server.crt
    server.key
    ca-bundle.pem
```

The backend container is named `swebench-managed-httpbin` and listens on
`127.0.0.1:18081`. During `doctor` or `verify`, the evaluator process starts a
temporary local proxy on ports `80` and `443`. For `psf/requests-*` harness
containers, the calibrated Docker patch maps `httpbin.org` to Docker's host
gateway and mounts `ca-bundle.pem` at the requests CA path. This preserves the
old tests' expected host string while keeping all traffic local.

If ports `80` or `443` are already occupied by another service, `doctor` and
`verify` fail rather than falling back to public `httpbin.org`.
