# tRPC-Agent-Go SWE-Bench Runner

This runner implements the mini-SWE-agent v2.1 model-facing protocol through
tRPC-Agent-Go's `llmagent` and `runner` lifecycles. It is distinct from
`mini-swe-agent-go-impl`, which owns an explicit Go control loop.

The framework-backed lane keeps the behavior that affects model quality and
cost:

- the upstream system/instance prompts and response format-error recovery;
- one callable `bash` tool, with same-response calls executed sequentially;
- XML-like, JSON, and text observation codecs with the same truncation boundary;
- a 250 LLM-call limit and non-streaming model requests;
- submission through a controlled framework stop, with no remaining action in
  the same response and no following model call;
- OpenAI SDK retry with nine retries after the initial request;
- one official SWE-Bench Docker testbed and one framework runner per case.

Framework events are retained as run artifacts. They are not converted to the
explicit-loop trajectory format.

## Run

From `swebench/`:

```bash
go run ./trpc-agent-go-impl \
  --run-id native-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/openai-compatible.yaml.example \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/native-smoke/raw/native \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1 \
  --observation-codec xml
```

`--observation-codec` accepts `xml`, `json`, or `text`. Runs resume by
skipping instance IDs already present in `preds.json` only after the complete
per-case bundle matches the run ID, source/binary, model/environment/cases,
codec, timeout, worker-count, and selected-case fingerprints. Pass
`--redo-existing` to rerun a matching bundle. In clean-room mode, that flag may
also rerun a complete retryable environment failure produced before a successful
`StartCase`, when the success-only verified-base and image-provenance
attestations cannot yet exist. The immutable run identity and all available
bundle fingerprints must still match; without the flag, or for any other
missing or mismatched attestation, resume remains fail-closed. Model
configuration supports arbitrary OpenAI-compatible HTTP headers through the
shared `modelconfig.HTTPHeaders` normalization.

Case bundles created before the worker-count and response-artifact fingerprints
were added are rejected during resume. Keep them as historical evidence and use
a new output directory (or explicitly rerun them) instead of mixing old and new
bundle schemas.

Each output directory contains:

```text
preds.json
native-runner-manifest.json
native-runner-progress.json
<instance_id>/<instance_id>.native.json
<instance_id>/<instance_id>.responses.json
```

The manifest reads the linked tRPC-Agent-Go module version from Go build
information. It does not hardcode a development revision.

## Optional clean-room generation

Clean-room mode is a benchmark protocol option, not a tRPC-Agent-Go framework
default. It is disabled unless `--clean-room=true` is supplied. When enabled,
the runner:

- resolves all selected testbed and fixture images before resume, starts them
  by immutable local image ID, and uses `--pull=never`;
- starts each model-facing testbed with `--network=none`;
- recursively removes Git remotes, tags, extra refs, reflogs, alternates,
  unreachable objects, untracked files, and non-allowlisted ignored files;
- verifies the repository `HEAD` is exactly the case `base_commit`, then
  recursively re-attests the complete Git state after all environment setup
  and before the model is constructed;
- provides isolated loopback HTTP/HTTPS fixtures for requests cases without
  publishing host ports; and
- installs the few required request-test dependencies only from a validated
  closed-world asset bundle.

Prepare the portable asset bundle in a separate, intentionally network-enabled
preparation phase. Before running the script, both requests testbed images
referenced by the script must already exist locally because image startup uses
`--pull=never`. The preparation host also needs the Go toolchain, GNU
`sha256sum`, and a `sort` implementation with `-z` support, while the compiler
testbed image must provide `gcc` with static-linking support. Runtime containers
never download these dependencies:

```bash
./scripts/prepare-offline-assets.sh results/offline-assets
```

Before model-backed generation, run the model-free smoke panel through the
same environment setup and cleanup path. This standalone preflight is an
operator gate: it exits nonzero on any failed case, and model-backed generation
must not start unless the complete panel passes.

```bash
go run ./trpc-agent-go-impl/cmd/offline-preflight \
  --cases data/generated/cases.jsonl \
  --case-list data/case-lists/offline-smoke.case_ids.txt \
  --environment-config config/environments/swebench-testbed.yaml \
  --offline-assets-dir results/offline-assets \
  --output results/runs/offline-preflight/manifest.json \
  --workers 1
```

Then add the two explicit flags to a Native run:

```text
--clean-room=true
--offline-assets-dir results/offline-assets
```

The runner manifest records the portable asset-tree identity, clean-room
policy hash, resolved image map, and image-set hash. Per-case artifacts record
the exact base commit and actual environment image provenance. These values are
validated again by resume, import, and `run-config`; host asset paths are not
part of the portable identity. Independently of the standalone operator gate,
the Native runner fails closed before model construction when it encounters a
missing local image, malformed or changed asset bundle, Git mismatch, or
isolation setup failure.

## Complete a filtered smoke run

Use the predictions as the selected case set when normalizing a filtered or
sliced run. The full prepared cases manifest is still passed to `run-config`,
so the final provenance document retains both the complete source panel and the
actual run selection without fabricating a subset manifest.

```bash
go run ./evaluator verify \
  --run-id native-smoke \
  --target tag \
  --predictions results/runs/native-smoke/raw/native/preds.json \
  --output results/runs/native-smoke/local-harness-report/tag \
  --harness-workers 1 \
  --instance-timeout-seconds 1800

go run ./evaluator import \
  --target tag \
  --predictions results/runs/native-smoke/raw/native/preds.json \
  --raw-dir results/runs/native-smoke/raw/native \
  --harness-report <verifier_manifest.report.path> \
  --output results/runs/native-smoke/imported

go run ./evaluator run-config \
  --run-id native-smoke \
  --target tag \
  --cases-manifest data/generated/cases.manifest.json \
  --runner-manifest results/runs/native-smoke/raw/native/native-runner-manifest.json \
  --verifier-manifest results/runs/native-smoke/local-harness-report/tag/verifier_manifest.json \
  --import-summary results/runs/native-smoke/imported/summary/tag.json \
  --harness-report <verifier_manifest.report.path> \
  --model-name <model-name>
```

Both commands must receive the exact report path recorded by `verify`.
`run-config` checks its SHA-256, harness run ID, output directory, filename, and
verify command provenance before it accepts resolved/unresolved/error outcomes.
`verify` also evaluates an atomic predictions snapshot; Native finalization
requires its SHA-256 to match both the runner predictions and the harness `-p`
input.

For an unfiltered full-panel run, pass `--cases data/generated/cases.jsonl` to
`import` to retain all canonical case metadata in the normalized rows.

## Validate

```bash
go test ./trpc-agent-go-impl/...
go test -race ./trpc-agent-go-impl/...
```
