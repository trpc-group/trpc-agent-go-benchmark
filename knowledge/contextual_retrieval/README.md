# Controlled Contextual Retrieval A/B

This directory provides an opt-in benchmark for testing Contextual Retrieval in
tRPC-Agent-Go. It contains two complementary evaluation lanes:

- **I1 retrieval-only diagnostics** measure ranking for one frozen, original
  query per case.
- **I2 Agentic evaluation** measures the end-to-end result when an Agent may
  reformulate a question and search more than once.

I1 is diagnostic evidence, not a gate for I2. A negative result in either lane
applies only to that sealed experiment identity; it is not evidence that every
Contextual Retrieval design is ineffective.

The benchmark implementation does not change the public tRPC-Agent-Go API.

## Method boundary

Both lanes read the same sealed chunk manifest. The only A/B method variable is
the text embedded at index time:

```text
A / baseline:   BaseEmbeddingText
B / contextual: Context + delimiter + BaseEmbeddingText
```

For every retrieved document, both arms return the original
`Document.Content`, metadata, and stable chunk identity to the Agent; neither
arm exposes generated Context or reorders the backend result. The selected
chunks and their ranking may differ between arms, which is the intended method
effect. Both arms also share the same corpus, chunking, embedding model, vector
store, Agent model and prompt, vector-only search, top-k value, tool schema,
and tool-argument policy.

The contextual arm fails closed when Context is missing or empty, or when its
parent/chunk identity no longer matches the sealed manifests. This benchmark
does not test contextual BM25, hybrid retrieval, reranking, HyDE, parent
retrieval, or another Context prompt/model.

`query-guard/v1` is a common control, not an A/B variable. It accepts only a
JSON object with one non-empty string field named `query`, rejects aliases and
extra fields, permits one model-visible repair, and never silently rewrites an
invalid call. Validation or dispatcher errors remain in the trace and are not
treated as retrieved Context.

## Dataset and sealed artifacts

The reference panel is selected from MultiHop-RAG:

```text
parent documents:             609
chunks:                    13,086
cases:                         450
comparison/inference/temporal: 150 each
gold evidence mappings:      1,209
```

All 1,209 evidence entries are mapped by exact parent span to frozen chunks.
The preparation step freezes every exact location when the same evidence text
appears more than once in a parent document; it does not guess using semantic
similarity.

Generated raw data, caches, answers, Context, per-record scores, databases, and
logs are intentionally excluded from Git. Keep them in a private working
directory outside the checkout.

From the repository's `knowledge` directory, define only local paths:

```bash
export KNOWLEDGE_ROOT="$PWD"
export CR_WORKDIR="${CR_WORKDIR:?set CR_WORKDIR outside the checkout}"
export GO_SERVICE="$KNOWLEDGE_ROOT/knowledge_system/trpc_agent_go/trpc_knowledge"

mkdir -p "$CR_WORKDIR"/{raw,artifacts,cache,runs}
```

Service/model credentials and endpoint values must be supplied through the
runtime environment. Do not put them in scripts, command history, reports, or
artifacts.

## CLI overview

The package exposes eleven commands:

| Command | Purpose |
|---|---|
| `prepare-dataset` | Build stable parent/query manifests. |
| `map-evidence` | Freeze exact evidence-to-chunk mappings. |
| `probe-contexts` | Probe Context generation without changing the durable cache. |
| `generate-contexts` | Generate or resume the append-only Context cache. |
| `summarize-contexts` | Validate an existing Context cache and seal its summary. |
| `run` | Run a paired retrieval-only A/B against existing services. |
| `run-server-smoke` | Build/reuse isolated indexes and run guarded I1 smoke. |
| `run-server-formal` | Reuse promoted indexes for guarded I1 formal evaluation. |
| `run-agentic` | Freeze Agent answers against existing services; uncontrolled without controller lineage. |
| `run-agentic-server` | Run a guarded I2 smoke or formal answer-freezing phase. |
| `judge-agentic` | Judge immutable I2 answers without invoking the Agent. |

Use `python -m contextual_retrieval <command> --help` as the authoritative
option list.

## 1. Prepare parents, chunks, and cases

```bash
python -m contextual_retrieval prepare-dataset \
  --corpus "$CR_WORKDIR/raw/MultiHopRAG-corpus.json" \
  --questions "$CR_WORKDIR/raw/MultiHopRAG.json" \
  --parents-output "$CR_WORKDIR/artifacts/parents.json" \
  --queries-output "$CR_WORKDIR/artifacts/queries.json" \
  --preflight-output "$CR_WORKDIR/artifacts/dataset-preflight.json"

(cd "$GO_SERVICE" && go run . \
  --parent-manifest "$CR_WORKDIR/artifacts/parents.json" \
  --write-chunk-manifest "$CR_WORKDIR/artifacts/chunks.json")

python -m contextual_retrieval map-evidence \
  --queries "$CR_WORKDIR/artifacts/queries.json" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cases-output "$CR_WORKDIR/artifacts/cases.json" \
  --preflight-output "$CR_WORKDIR/artifacts/evidence-preflight.json"
```

Both preflight artifacts must report `status=valid` before continuing.

## 2. Generate and seal Context

Context generation uses a separate index-build model configuration. The
reference experiment used DeepSeek-V3.2 with temperature 0, no reasoning
parameter, and `max_tokens=4096`. Configure the following variables privately:

```text
CONTEXT_MODEL_NAME
CONTEXT_BASE_URL
CONTEXT_API_KEY
CONTEXT_MAX_TOKENS
CONTEXT_TIMEOUT_SECONDS
CONTEXT_RETRY_BASE_DELAY_SECONDS
CONTEXT_RETRY_MAX_DELAY_SECONDS
```

Optional gateway headers can be supplied by the environment variables described
in `context_cache.py`; artifacts record only public endpoint identity and header
names, never credential values.

Probe a stratified sample first:

```bash
python -m contextual_retrieval probe-contexts \
  --parents "$CR_WORKDIR/artifacts/parents.json" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --output "$CR_WORKDIR/artifacts/context-probe.json" \
  --count 20 \
  --attempts-per-item 3
```

After confirming `status=valid`, build the full cache:

```bash
python -m contextual_retrieval generate-contexts \
  --parents "$CR_WORKDIR/artifacts/parents.json" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cache "$CR_WORKDIR/cache/contexts.jsonl" \
  --summary-output "$CR_WORKDIR/artifacts/context-summary.json" \
  --workers 8 \
  --attempts-per-run 3 \
  --progress-interval 10

python -m contextual_retrieval summarize-contexts \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cache "$CR_WORKDIR/cache/contexts.jsonl" \
  --output "$CR_WORKDIR/artifacts/context-summary.json"
```

The cache is append-only JSONL. Each attempt is flushed and synced before the
next item, so the same command can resume after interruption. A changed model,
prompt, endpoint identity, max-token setting, or chunk manifest changes the
cache identity. Contextual indexing is allowed only when all 13,086 chunks are
successful and the summary is valid.

## 3. Build isolated indexes and run I1 smoke

Choose two new PostgreSQL identifiers. The controller refuses an unowned,
non-empty table and never drops or truncates one. Completed indexes are reused
only when their identity and row count match.

```bash
export BASELINE_TABLE="${BASELINE_TABLE:?set a new baseline table name}"
export CONTEXTUAL_TABLE="${CONTEXTUAL_TABLE:?set a new contextual table name}"
export INDEX_RUN="$CR_WORKDIR/runs/index-smoke"

python -m contextual_retrieval run-server-smoke \
  --go-service-dir "$GO_SERVICE" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cases "$CR_WORKDIR/artifacts/cases.json" \
  --context-cache "$CR_WORKDIR/cache/contexts.jsonl" \
  --output-dir "$INDEX_RUN" \
  --baseline-table "$BASELINE_TABLE" \
  --contextual-table "$CONTEXTUAL_TABLE" \
  --smoke-per-type 10 \
  --bootstrap-resamples 1000 \
  --bootstrap-seed 20260722
```

Use `--resume-indexes` only for partial indexes owned by the same controller
state. `--baseline-only` may prepare the baseline index while Context is still
being generated. The controller starts and stops only services it owns.

The I1 smoke promotion decision controls only expansion to I1 formal. A
`stop` decision does not block I2 when both index states are complete and their
identities are valid.

When I1 smoke promotes, run the retrieval-only formal lane:

```bash
python -m contextual_retrieval run-server-formal \
  --go-service-dir "$GO_SERVICE" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cases "$CR_WORKDIR/artifacts/cases.json" \
  --context-cache "$CR_WORKDIR/cache/contexts.jsonl" \
  --smoke-dir "$INDEX_RUN" \
  --output-dir "$CR_WORKDIR/runs/i1-formal" \
  --conformance-smoke-per-type 10 \
  --conformance-bootstrap-resamples 1000 \
  --bootstrap-resamples 10000 \
  --bootstrap-seed 20260722
```

`run-server-formal` first repeats the conformance smoke and never calls `/load`
for the frozen complete indexes.

## 4. Freeze I2 Agent answers

The guarded controller validates the sealed Context summary, both complete index
states, the current runtime configuration, and source provenance before issuing
Agent requests. The formal schedule is fixed at:

```text
450 cases x 2 arms x 3 logical repeats = 2,700 executions
schedule seed: 20260725
Agent:         DeepSeek-V3.2
Embedding:     BGE-M3
search:        vector-only, top-4
session:       fresh per execution
Agent reruns:  none
```

The Agent environment must identify DeepSeek-V3.2 and BGE-M3 and provide their
private service credentials. The controller verifies the effective runtime
configuration; command-line intent alone is not evidence.

Run a 30-case operational smoke first:

```bash
python -m contextual_retrieval run-agentic-server \
  --mode smoke \
  --go-service-dir "$GO_SERVICE" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cases "$CR_WORKDIR/artifacts/cases.json" \
  --context-cache "$CR_WORKDIR/cache/contexts.jsonl" \
  --context-summary "$CR_WORKDIR/artifacts/context-summary.json" \
  --baseline-index-state "$INDEX_RUN/baseline.index-state.json" \
  --contextual-index-state "$INDEX_RUN/contextual.index-state.json" \
  --output-dir "$CR_WORKDIR/runs/i2-smoke" \
  --smoke-per-type 10 \
  --schedule-seed 20260725
```

After reviewing the smoke evidence, use a new output directory for formal I2:

```bash
python -m contextual_retrieval run-agentic-server \
  --mode formal \
  --go-service-dir "$GO_SERVICE" \
  --chunks "$CR_WORKDIR/artifacts/chunks.json" \
  --cases "$CR_WORKDIR/artifacts/cases.json" \
  --context-cache "$CR_WORKDIR/cache/contexts.jsonl" \
  --context-summary "$CR_WORKDIR/artifacts/context-summary.json" \
  --baseline-index-state "$INDEX_RUN/baseline.index-state.json" \
  --contextual-index-state "$INDEX_RUN/contextual.index-state.json" \
  --output-dir "$CR_WORKDIR/runs/i2-formal" \
  --schedule-seed 20260725
```

Every execution causes one Agent request. A normal Agent failure is retained in
the fixed denominator and is not resampled. An append-only
`agentic.checkpoint.jsonl` is synced before immutable
`agentic.answers.json` and `agentic.json` are sealed. Trace v3 validates tool
calls, effective search mode and top-k, search identities, returned chunk
content/order, and Context alignment.

Standalone benchmark checkouts record the benchmark/module repository by
default. `--framework-repository-root` is optional and should be supplied only
when reproducing a historical dual-checkout lineage that explicitly tracked a
separate framework repository.

## 5. Judge immutable I2 answers

The reference Judge profile is exact, not inferred from defaults:

```text
Judge model:          GLM-5.2
temperature:          0
reasoning parameter:  omitted
max_tokens:           65536
EVAL_MAX_WORKERS:     8
batch size:           25
record attempts:      5
bootstrap resamples:  10000
bootstrap seed:       20260725
```

Configure the Judge and BGE-M3 embedding clients privately using the `EVAL_*`
and `EMBEDDING_*` variables read by `agentic_judge.py`. Do not persist their
values.

```bash
export EVAL_MAX_WORKERS=8

python -m contextual_retrieval judge-agentic \
  --answers "$CR_WORKDIR/runs/i2-formal/agentic.answers.json" \
  --agentic-manifest "$CR_WORKDIR/runs/i2-formal/agentic.manifest.json" \
  --agentic-report "$CR_WORKDIR/runs/i2-formal/agentic.json" \
  --agentic-checkpoint "$CR_WORKDIR/runs/i2-formal/agentic.checkpoint.jsonl" \
  --cases "$CR_WORKDIR/artifacts/cases.json" \
  --controller-report "$CR_WORKDIR/runs/i2-formal/controller.report.json" \
  --verified-lineage "$CR_WORKDIR/runs/i2-formal/verified-lineage.json" \
  --output "$CR_WORKDIR/runs/i2-formal/judge.json" \
  --batch-size 25 \
  --record-attempts 5 \
  --bootstrap-resamples 10000 \
  --bootstrap-seed 20260725
```

The output path above produces `judge.manifest.json`, `judge.scores.json`, and
the aggregate `judge.json`. The Judge consumes only frozen answers and never
calls or reruns the Agent. The Agent and Judge source revisions may differ, but
the answers, Agent manifest/report/checkpoint, controller report, verified
lineage, cases, Context summary, and index identities must remain frozen.

Agent failures are explicitly scored as zero under the registered fixed
denominator and are reported separately from protocol and Judge errors. Judge
records with missing or non-finite metrics are retried individually. If any
metric is still unavailable after five attempts, the record retains
`judge_error` with `metrics=null`; it is neither zero-filled nor counted as
recovered. A partial Judge checkpoint is fail-stop and requires a new output
path.

Older experimental artifacts that zero-filled unavailable metrics do not meet
the fail-closed policy implemented here. See the I2 aggregate report for the
disclosed historical fallback audit and complete-case sensitivity analysis.

## Artifact and recovery contract

Controlled runs bind each stage to canonical digests rather than directory
names alone:

| Stage | Durable artifacts | Recovery rule |
|---|---|---|
| Context | cache JSONL and `context-summary.json` | Append attempts; index only after complete validated coverage. |
| Index/controller | `controller.manifest.json`, `controller.report.json`, `provenance.json`, runtime config, and `*.index-state.json` | Reuse only an owned state with matching identity and row count. |
| Agent | `verified-lineage.json`, `agentic.manifest.json`, `agentic.checkpoint.jsonl`, `agentic.answers.json`, and `agentic.json` | Resume the append-only checkpoint; seal answers only after the full schedule. |
| Judge | `judge.manifest.json`, `judge.scores.json`, and `judge.json` | Reuse only a complete compatible score artifact; partial scores are fail-stop. |

Existing files are never silently overwritten when their run identity or
source digests differ. Use a new output directory or output path for a new
hypothesis, changed configuration, or failed partial Judge run.

## Evidence and result reports

- [I1 retrieval-only smoke](results/i1-smoke-20260722.md)
- [I1 retrieval-only formal result](results/i1-formal-20260722.md)
- [I2 Agentic engineering evaluation](results/i2-agentic-20260801.md)

The reference I2 report covers one frozen MultiHop-RAG panel and one dense-only
configuration. Its three repeats share the same cases, Context cache, and
indexes; they are not independent datasets or independent index builds. Use the
suite to validate candidate behavior on the target business corpus rather than
assuming the recorded direction will transfer.
