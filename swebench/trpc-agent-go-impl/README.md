# TAG SWE-Bench Implementation

This runner implements the mini-SWE-agent v2.1 model-facing protocol through
tRPC-Agent-Go (TAG) `llmagent` and `runner` lifecycles. It is distinct from
`mini-swe-agent-go-impl`, which owns an explicit Go control loop.

The TAG lane keeps the behavior that matters to model quality and cost:

- the upstream system/instance prompts and response FormatError recovery;
- one callable `bash` tool, with same-batch calls executed sequentially;
- XML, JSON, and text observation codecs with the same truncation boundary;
- a 250 LLM-call limit and non-streaming model requests;
- submission through `SkipSummarization`, with no following model call;
- OpenAI SDK retry with nine retries after the initial request;
- one official SWE-Bench Docker testbed and one TAG runner per case.

TAG-native events are retained as run artifacts. They are not converted to the
mini-go trajectory format.

## Run

From `swebench/`:

```bash
go run ./trpc-agent-go-impl \
  --run-id tag-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/glm-5.2.local.yaml \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/tag-smoke/raw/tag \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1 \
  --observation-codec xml
```

`--observation-codec` accepts `xml`, `json`, or `text`. Runs resume by skipping
instance IDs already present in `preds.json`; pass `--redo-existing` to rerun
them. `--billing-tag` and `--experiment-id` must be provided together.
Workspace retrieval preloads context by default when `--code-search` is set.
Use `--workspace-preload=false` to keep the same index and `code_search` tool
while withholding retrieved context from the initial prompt for a controlled
ablation.

`--workspace-representation` selects the Python indexing representation:

- `current-fixed` (default): historical 1024-character text chunks with
  128-character overlap and per-line whitespace trimming;
- `fixed-raw`: the same chunk boundaries while preserving indentation;
- `ast-code`: Python AST node boundaries, embedding node code;
- `ast-structured`: Python AST node boundaries, embedding stable structural
  fields plus node code.

Both AST variants include test files, use repository-stable module/file paths,
and emit a whole-file fallback document for parse failures or files with no AST
nodes. Per-case `workspace_index` telemetry records eligible/indexed file
coverage, fallback reasons, node types, duplicate rate, and stable file/document
set hashes. The run manifest records the representation schema and its SHA-256.

When `cache.enabled` is set in the embedding config, the runner shares one
SQLite embedding cache across all case workers and later runs. Cache access is
always read-through/write-through: exact input hits bypass the embedding
endpoint, while misses are embedded and persisted. Entries are isolated by
provider, model, output dimensions, and the required `model_fingerprint`;
change that fingerprint whenever model weights, tokenizer, pooling,
normalization, or serving preprocessing changes. The cache decorates the
framework `Embedder`, so the same mechanism can also cover a future AST-based
reader without making AST part of the cache key. This version does not
automatically expire or delete cache databases.

## AST retrieval gate

Run retrieval replay before spending model tokens on an Agent A/B. It starts one
official testbed per case, snapshots it once, and builds every requested
representation sequentially from that exact snapshot. Gold patches must come
from an external, uncommitted JSON or JSONL file with `instance_id` and `patch`
fields; never add that file or derived gold content to benchmark data.

```bash
go run ./trpc-agent-go-impl/cmd/retrieval-replay \
  --run-id ast-rag-replay-smoke \
  --cases data/generated/cases.jsonl \
  --case-list data/case-lists/tag-rag-preload-ablation-54.case_ids.txt \
  --labels /data/private/swebench-verified-gold.jsonl \
  --environment-config config/environments/swebench-testbed.yaml \
  --embedding-config config/embeddings/workspace-rag.local.yaml \
  --source-revision "$(git rev-parse HEAD)" \
  --framework-revision "$(git -C /data/validation/trpc-agent-go rev-parse HEAD)" \
  --representations current-fixed,fixed-raw,ast-code,ast-structured \
  --max-results 6 \
  --case-workers 1
```

The checkpointed report contains target-file Recall@4/@6, target-file reciprocal
rank, base-hunk anchor Recall@4/@6, target-file character precision, and
representation/cache/index telemetry. Search traces contain paths, scores,
sizes, and hashes, but not retrieved source text, gold target paths, or gold
patch content. The exact case-list, labels, configs, source revision, framework
revision, and binary SHA-256 are recorded.

The 54-case preload-ablation panel is a high-sensitivity diagnostic set, not an
unbiased resolve-rate estimate. Replay also measures only retrieval from the
problem statement; it does not reproduce later model-written `code_search`
queries. Use it as a gate: require full file coverage and a material paired
Recall@6 or hunk-anchor gain, then run the winning AST representation against
`fixed-raw` in a repeated Agent A/B. Only expand to the 136-case or full-500
panel after that Agent result preserves or improves official-harness resolve
rate.

The Python AST reader is a separate Go module. Until the benchmark pins a
published reader revision, add it to the same Go workspace as the benchmark
and framework root:

```bash
go work use \
  /data/validation/trpc-agent-go/knowledge/document/reader/python
```

Each output directory contains:

```text
preds.json
tag-runner-manifest.json
tag-runner-progress.json
<instance_id>/<instance_id>.tag.json
<instance_id>/<instance_id>.responses.json
```

Verify and import a result with the `tag` evaluator target:

```bash
go run ./evaluator verify \
  --run-id tag-smoke \
  --target tag \
  --predictions results/runs/tag-smoke/raw/tag/preds.json \
  --output results/runs/tag-smoke/local-harness-report/tag \
  --harness-workers 1

go run ./evaluator import \
  --target tag \
  --cases data/generated/cases.jsonl \
  --predictions results/runs/tag-smoke/raw/tag/preds.json \
  --raw-dir results/runs/tag-smoke/raw/tag \
  --harness-report results/runs/tag-smoke/local-harness-report/tag/report.json \
  --output results/runs/tag-smoke/imported
```

Use the actual report path emitted by `evaluator verify` when it differs from
the illustrative `report.json` path above.

## Validate

```bash
go test ./trpc-agent-go-impl/... ./internal/minicompat ./internal/sweenv
go test -race ./trpc-agent-go-impl/internal/executor
```
