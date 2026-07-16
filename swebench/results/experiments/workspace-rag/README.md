# Workspace Code RAG experiment

This experiment evaluates a framework-level workspace retrieval capability in
`trpc-agent-go-impl`. It intentionally avoids repository-specific rules and
business heuristics.

## Variant

- Materialize the current `/testbed` workspace through an optional environment
  capability.
- Split Python source into bounded raw-code chunks with path metadata.
- Build an in-process BM25 index without an embedding API.
- Expose a compact, query-only `code_search` tool returning at most six results.
- Keep document URIs stable as `workspace://<instance_id>/<path>`.
- Record `workspace_index.documents`, `workspace_index.duration_ms`, and
  `code_search_calls` separately from model usage.

The variant is opt-in with `--code-search`; an invocation without the flag is
the unchanged baseline.

## First evaluation pool

The pool contains every case not resolved in both historical TAG runs:

- `tag-impl-full500-20260715-e1`: 19/136 resolved
- `tag-impl-full500-20260716-e2`: 35/136 resolved
- historical mean: 27/136 resolved

The checked-in list has 136 IDs and SHA-256
`90b071ba6ac1407df571ecc585efcb12b7cc2d14b6ed8e791ee99d0a80de2044`.

## Execution order

This variant must be tested against the matching framework checkout. Create a
Go workspace containing the benchmark and framework root, then run from
`swebench/`. For example, with sibling checkouts:

```bash
cd /data/validation
go work init ./trpc-agent-go-benchmark/swebench \
  ./trpc-agent-go
export GOWORK=/data/validation/go.work
cd /data/validation/trpc-agent-go-benchmark/swebench
```

Then:

```bash
go test ./internal/sweenv \
  ./trpc-agent-go-impl/internal/tagagent \
  ./trpc-agent-go-impl/internal/executor \
  ./trpc-agent-go-impl/internal/runner

go run ./trpc-agent-go-impl \
  --run-id tag-rag-smoke-1 \
  --cases data/generated/cases.jsonl \
  --filter '^astropy__astropy-13033$' \
  --model-config config/models/glm-5.2.local.yaml \
  --code-search
```

After one case passes runner and official evaluator checks, run 5-10 cases.
Then run the full historical pool:

```bash
CASE_FILTER="^($(paste -sd'|' data/case-lists/tag-impl-unstable-e1-e2-136.case_ids.txt))$"

go run ./trpc-agent-go-impl \
  --run-id tag-rag-unstable136-r1 \
  --cases data/generated/cases.jsonl \
  --filter "$CASE_FILTER" \
  --model-config config/models/glm-5.2.local.yaml \
  --code-search
```

Use the official local harness as the resolution source. Do not infer resolution
from submission status.

## Decision gate

- `<=27/136`: no signal; stop this configuration.
- `28-41/136`: weak/ambiguous; inspect retrieval use and cost before changing
  one major variable.
- `>=42/136`: at least 15 cases above the historical mean; repeat the same 136
  cases once.
- Run 500 cases only if the second 136-case run repeats a material gain without
  unacceptable total-token or wall-time regression.

Compare quality and cost separately:

- Quality: official resolved count, F00/F01 transitions, regressions from the
  historically one-run-resolved subset.
- Model cost: prompt, cached, uncached, completion, and total tokens; API calls;
  tokens per resolved case.
- Retrieval cost: index duration, indexed document count, code-search calls,
  and code-search result payload size where available.

Python AST indexing and Dense+BM25 hybrid retrieval are follow-up variants.
Dense retrieval requires an
OpenAI-compatible embeddings endpoint, model name, dimensions if non-default,
and price information so embedding cost can be reported independently.
