# Workspace Code RAG experiment

This experiment evaluates a framework-level workspace retrieval capability in
`trpc-agent-go-impl`. It intentionally avoids repository-specific rules and
business heuristics.

## Variants

### V1: tool-only retrieval

- Materialize the current `/testbed` workspace through an optional environment
  capability.
- Split Python source into bounded raw-code chunks with path metadata.
- Build an in-process BM25 index without an embedding API.
- Expose a compact, query-only `code_search` tool returning at most six results.
- Keep document URIs stable as `workspace://<instance_id>/<path>`.
- Record `workspace_index.documents`, `workspace_index.duration_ms`, and
  `code_search_calls` separately from model usage.

### V2: proactive retrieval plus tool

- Before the first model request, retrieve against the problem statement and
  inject at most four results and 6,000 characters as `workspace_context`.
- Keep `code_search` available for focused follow-up retrieval.
- Record `workspace_index.preloaded_documents` and
  `workspace_index.preloaded_chars` in addition to the V1 metrics.

Both variants are framework-generic and use the same opt-in `--code-search`
path. V2 replaces V1 in the current implementation; an invocation without the
flag remains the unchanged baseline.

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

After one case passes runner and official evaluator checks, run a small
stratified sample. Do not run the full historical pool until that sample shows
a material quality or cost signal. The full-pool command is:

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

When the model config contains a `pricing` rate card, the TAG runner writes a
`cost_estimate` to its manifest. The current GLM-5.2 deployment's historical
billing records fit the following per-million-token rates with rounding-only
residuals: uncached input 8, cached input 2, and output 28. The currency must be
declared by the deployment config rather than inferred by the framework.

Python AST indexing and Dense+BM25 hybrid retrieval are follow-up variants.
Dense retrieval requires an
OpenAI-compatible embeddings endpoint, model name, dimensions if non-default,
and price information so embedding cost can be reported independently.

## V1 observed result

Run `tag-rag-smoke8-20260716-r1` evaluated eight cases with the official local
harness:

| Case | Historical E1/E2 | V1 | Search calls | Total tokens |
|---|---|---:|---:|---:|
| `astropy__astropy-13033` | U/R | U | 0 | 466,725 |
| `astropy__astropy-13236` | U/U | U | 1 | 406,779 |
| `astropy__astropy-14182` | R/U | R | 0 | 390,865 |
| `django__django-10999` | U/U | U | 0 | 19,574 |
| `django__django-11265` | U/R | R | 0 | 1,721,197 |
| `django__django-11532` | R/U | R | 0 | 110,712 |
| `matplotlib__matplotlib-20676` | U/U | U | 0 | 2,646,106 |
| `psf__requests-2317` | U/U | U | 0 | 62,759 |

V1 resolved 3/8, versus a historical per-run expectation of 2/8 for this
sample. It rescued 0/4 F00 cases and used `code_search` in only 1/8 cases. The
run consumed 5,824,717 total tokens. This is noise-level quality evidence and a
negative cost signal, so V1 must not be expanded to 136 cases.

V2 first re-runs four representative cases from the same sample:

- F00: `astropy__astropy-13236`, `django__django-10999`
- F01: `astropy__astropy-13033`, `django__django-11532`

The purpose is to verify that retrieval is actually consumed on every case and
to reject V2 cheaply if it neither rescues an F00 case nor materially reduces
model usage.
