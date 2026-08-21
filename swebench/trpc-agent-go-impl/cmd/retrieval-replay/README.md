# Offline retrieval replay

`retrieval-replay` rebuilds the benchmark-local task-start workspace index and
re-executes the `code_search` calls that actually ran in a framework-Native
trajectory. It never invokes an LLM or embedding endpoint, and it does not read
SWE-Bench gold patches.

## Freeze a portable input bundle

The Native run directory is not sufficient: it records index identities and
retrieval traces, but intentionally does not retain the task workspace bytes.
Provide a separately frozen task-start checkout for every selected case:

```text
<corpus-root>/
  django__django-12345/
    ... exact task-start repository snapshot ...
  sympy__sympy-12345/
    ... exact task-start repository snapshot ...
```

The case-list file must contain the selected instance IDs in strictly sorted
order, one per line. The corpus root must contain exactly those top-level case
directories. Symlinks, special files, missing/extra cases, path traversal, and
an index hash mismatch are rejected. Only the same eligible non-empty `.py`
files used by the Native indexer enter the deterministic corpus archive.

```sh
go run ./trpc-agent-go-impl/cmd/retrieval-replay prepare \
  --run-dir ./native-run \
  --corpus-root ./frozen-task-start-corpora \
  --case-list ./sorted-cases.txt \
  --output-dir ./new-portable-bundle
```

`prepare` cross-checks every rebuilt corpus/index identity against the Native
`workspace_index`, writes content-addressed artifacts through a staging
directory, and runs a complete replay before atomically publishing the output.
The bundle contains neither candidate/gold patches nor model/endpoint data.

## Replay

```sh
go run ./trpc-agent-go-impl/cmd/retrieval-replay replay \
  --run-dir ./native-run \
  --bundle ./new-portable-bundle/replay-bundle.json \
  --output ./replay-report.json
```

The report compares exact success/no-hit/error outcome, error fingerprint, ranked document identity,
content/metadata hashes, and IEEE-754 score bits. Retrieval trace/raw-result
entries are the authoritative executed-call sequence. A `code_search` emitted
after a successful `submit` in the same parallel batch can be skipped by the
runner's stop signal; such calls are validated against the model response and
reported separately, but are not replayed as if they executed.

The public replay engine currently supports keyword/BM25 mode with
`invocation_dedup=false`. Historical private-tool runs that used invocation
deduplication are not claimed to be equivalent and fail closed. Vector and
hybrid runs also fail closed until a complete, portable, all-hit embedding
cache adapter is available; there is no online fallback.
