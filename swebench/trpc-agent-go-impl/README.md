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

Before `--redo-existing` removes selected predictions from the active resume
boundary, the runner preserves the exact prior `preds.json` under a
content-addressed `preds.pre-redo.*.json` path recorded in the manifest. On
`SIGINT` or `SIGTERM` it stops dispatching new cases, cancels active case
contexts so their Docker environments close, restores any not-yet-replaced
redo predictions, writes an `interrupted` manifest, and exits non-zero.

Case bundles created before the worker-count and response-artifact fingerprints
were added are rejected during resume. Keep them as historical evidence and use
a new output directory (or explicitly rerun them) instead of mixing old and new
bundle schemas.

The optional tool-loop warning setting is also part of the immutable run
identity. Resume rejects a case bundle whose recorded setting differs from the
current command, even when every other fingerprint matches.

## Optional benchmark-local workspace retrieval

Workspace retrieval is a benchmark protocol option, not a tRPC-Agent-Go
framework default. It is disabled unless `--code-search=true` is supplied, so
the default Native lane still exposes only `bash` and does not emit retrieval
fields. When enabled, the runner copies the task-start `/testbed` bytes into a
temporary host snapshot after environment setup and before the agent runs. The
index is therefore static for the case: later edits are visible to `bash`, but
not to `code_search`.

The model receives a `code_search` tool alongside `bash`. Its query-only result is
rendered with source paths and excerpts through the same observation boundary
as other tool output. The exact tool result, a content-free ranked-document
trace, index coverage and representation fingerprints are retained in the
per-case artifact. `code_search` calls are deliberately excluded from the
exact `bash` loop-warning detector.

Keyword retrieval needs no embedding service:

```bash
go run ./trpc-agent-go-impl \
  --run-id native-keyword-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/openai-compatible.yaml.example \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/native-keyword-smoke/raw/native \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1 \
  --code-search=true \
  --workspace-preload=false \
  --workspace-representation=current-fixed
```

`--workspace-representation` accepts:

- `current-fixed`: line-normalized fixed-size chunks used by the current
  baseline;
- `fixed-raw`: fixed-size chunks that preserve Python whitespace;
- `ast-code`: public Python-reader AST nodes embedded as source code; and
- `ast-structured`: stable JSON embedding text containing node identity,
  signature, comment and code, while returning source code to the model.

The two AST modes fall back per file to `fixed-raw` when Python parsing fails
or yields no usable nodes. The artifact records fallback reasons, file and
document-set hashes, coverage, duplicate rate, node counts, parser dependency
and Python runtime identity, and index duration.
The representation schema hash is part of resume identity, so a run cannot
silently mix index contracts.

AST construction aborts the index build when the case context expires. The
pinned public reader exposes only a synchronous API and its Python subprocess
cannot be killed through that API, so a parser already in flight may finish in
the background after the case has failed closed. Hard child-process
cancellation requires an upstream context-aware reader API; this adapter does
not copy parser internals to simulate one.

The pinned public OpenAI adapter sorts tool names before sending a request, so
the optional lane records provider order `bash,code_search`. The public generic
knowledge tool also has invocation deduplication disabled: a later search may
return a chunk seen by an earlier search. Both values are explicit in run,
case, index and replay identity. The frozen historical RAG lane used
`code_search,bash` plus invocation-scoped deduplication, so this public rebuild
does not claim trajectory equivalence with that implementation.

To use vector or hybrid retrieval, copy
`config/embeddings/workspace-rag.yaml.example` to the ignored private file
`config/embeddings/workspace-rag.local.yaml`, set the OpenAI-compatible
embedding endpoint and model identity, then add:

```text
--embedding-config config/embeddings/workspace-rag.local.yaml
```

Only a SHA-256 and redacted configuration summary enter the manifest; endpoint,
credential and cache paths do not. Persisted embedding and retrieval errors are
scrubbed using the same local configuration before case bundles are written.
The optional SQLite cache is exact-input, model-fingerprint, endpoint, and
routing-header scoped, and read-through/write-through. Endpoint and header
values contribute only through an opaque SHA-256 backend fingerprint; API keys
are excluded so credential rotation does not invalidate semantically identical
vectors. A preload search with no relevant documents injects empty context and
continues, while real retrieval/backend errors still fail closed. Embedding requests,
tokens, latency and cache hits/misses are reported separately
from agent-model usage. A configured `batch_size` is retained as experiment
metadata; the current public framework loader invokes the embedder per
document, with `concurrency` controlling those calls.

This implementation was rebuilt against the public framework API from the
frozen experiment contract. Historical result bundles remain historical
evidence: this tree does not claim bit-for-bit equivalence to those binaries,
and it does not reuse their results as validation of the rebuilt code.

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

### Offline retrieval replay

The Native run directory intentionally does not retain task workspace bytes,
so replay first freezes a content-addressed portable bundle from separately
preserved task-start checkouts. The corpus root must contain exactly one real
directory per selected instance; the case-list file contains those instance
IDs in strictly sorted order. Symlinks, special files, path traversal and
missing or extra cases fail closed:

```bash
go run ./trpc-agent-go-impl/cmd/retrieval-replay prepare \
  --run-dir ./native-run \
  --corpus-root ./frozen-task-start-corpora \
  --case-list ./sorted-cases.txt \
  --output-dir ./new-portable-bundle

go run ./trpc-agent-go-impl/cmd/retrieval-replay replay \
  --run-dir ./native-run \
  --bundle ./new-portable-bundle/replay-bundle.json \
  --output ./replay-report.json
```

`prepare` cross-checks the rebuilt corpus and index identity against the Native
artifacts, runs a complete self-replay, then atomically publishes the bundle.
`replay` verifies every input digest, reconstructs the recorded index using the
concrete benchmark-local engine and compares each executed call's outcome,
error fingerprint, ranked document identity and exact score bits. Neither step
calls an LLM or embedding endpoint or consumes a SWE-Bench gold patch. The
report output must be outside both the immutable bundle and Native run
directories.

The public offline engine currently supports keyword/BM25 retrieval with
invocation deduplication disabled. Vector, hybrid and historical private-tool
identities fail closed until their complete portable dependencies are
available; stored trace summaries are never substituted for an actual replay.

## Optional exact tool-loop warning

The warning is a benchmark protocol option, not a tRPC-Agent-Go framework
default. It is disabled unless `--tool-loop-warning=true` is supplied. When
enabled, the runner compares consecutive, complete assistant tool-call batches
after all calls in each batch have produced their final model-visible results.
A batch is an exact repeat only when its ordered tool names, canonical JSON
arguments, and result observations match. JSON object key order and
insignificant argument whitespace do not change the comparison; tool order or
any name, argument value, or visible result change does.

On a match, the runner appends one fixed `<tool_loop_detected>` instruction to
the history immediately before the next real model request. The warning does
not create an extra model request, tool call, retry, or hidden recovery turn.
After a match the detector resets, so a three-batch `T,T,T` sequence warns only
before the model request following the second batch.

The setting and observations remain auditable across the artifact pipeline:

- each case `info` records `tool_loop_warning`; its result records
  `tool_loop_warning_count`, nullable `first_tool_loop_warning_llm_call`, and
  the ordered `tool_loop_warning_llm_calls` list;
- progress records the per-case count, while the runner manifest records the
  setting plus total warning-event and warning-case counts, including valid
  cases loaded through resume; and
- the Native `agent_protocol` gains the `+tool-loop-warning-v1` suffix (after
  `+clean-room-v1` when both options are enabled).

The recorded call number is the real model call that received the warning.
When no warning occurred, the count is zero, the first-call field is `null`,
and the call list is empty.

For a warning-off run, apply the exact same detector offline without modifying
any source artifact. Current Native output uses the per-case identity layout:

```bash
go run ./trpc-agent-go-impl/cmd/tool-loop-shadow-replay \
  --run-dir results/runs/<run-id>/raw/native \
  --output results/runs/<run-id>/tool-loop-shadow-replay.json
```

The frozen V12 clean-room runs use the legacy TAG layout instead:

```bash
go run ./trpc-agent-go-impl/cmd/tool-loop-shadow-replay \
  --run-dir results/runs/<run-id>/raw/tag \
  --output results/runs/<run-id>/tool-loop-shadow-replay.json
```

That adapter accepts only the fixed root `tag-runner-manifest.json` plus an
exact 500-case set of per-case `.tag.json` and `.responses.json` artifacts. It
does not mix current and legacy layouts. Because the legacy manifest did not
record a `clean_room` field, its report emits `run_identity.clean_room: null`;
this means "not recorded in this artifact schema", not `false`.

The deterministic report fingerprints every admitted trajectory and detached
response artifact. It records would-warn cases and events, first/all LLM-call
positions, and the terminal outcome plus remaining call-count summary after
the first warning point. Input paths are validated fail-closed and the report
is replaced atomically.

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
