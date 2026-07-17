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

### V3: batched Dense+BM25 retrieval

- Embed repository chunks and the query with an OpenAI-compatible endpoint.
- Combine dense and BM25 rankings with reciprocal rank fusion.
- Batch repository chunks and make embedding concurrency configurable. The
  136-case experiment used batches of 64 with one embedding worker per case.
- Validate response count, vector dimensions, and response indices before
  attaching vectors to documents.
- Record embedding requests, batch requests, inputs, tokens, errors, and
  aggregate request duration for every case.

All variants are framework-generic and use the same opt-in `--code-search`
path. V3 additionally requires `--embedding-config`; an invocation without the
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

For V3:

```bash
go run ./trpc-agent-go-impl \
  --run-id tag-rag-bge-m3-smoke-1 \
  --cases data/generated/cases.jsonl \
  --filter '^astropy__astropy-13033$' \
  --model-config config/models/glm-5.2.local.yaml \
  --code-search \
  --embedding-config config/embeddings/workspace-rag.local.yaml
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
  --code-search \
  --embedding-config config/embeddings/workspace-rag.local.yaml
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

V3 uses a self-hosted `bge-m3` deployment returning 1,024-dimensional vectors.
Monetary embedding cost is out of scope for this run; request count, tokens,
index duration, and wall time remain operational metrics.

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

## V2 first four-case result

Run `tag-rag-preload4-20260716-r1` proactively injected four retrieved results
for every case. The official local harness resolved 1/4 with no verifier errors:

| Case | Historical E1/E2 | V1 | V2 | V1 cost | V2 cost |
|---|---|---:|---:|---:|---:|
| `astropy__astropy-13033` | U/R | U | U | 1.271488 | 0.307472 |
| `astropy__astropy-13236` | U/U | U | R | 1.206516 | 1.287252 |
| `django__django-10999` | U/U | U | U | 0.072224 | 0.146620 |
| `django__django-11532` | R/U | R | U | 0.348820 | 0.326008 |

V2 rescued one F00 case but regressed one V1-resolved F01 case, leaving the net
result unchanged at 1/4. Total model usage fell from 1,003,790 to 743,945 tokens
and estimated cost fell from 2.899048 to 2.067352 billing units (-28.7%). LLM
calls fell from 92 to 74. No case invoked the follow-up `code_search` tool.

The cost reduction is not yet stable evidence: it is dominated by
`astropy__astropy-13033` ending much earlier, while two other cases became more
expensive. Repeat the same four cases once before expanding the sample. Do not
run the 136-case pool from this result.

The repeat `tag-rag-preload4-20260716-r2` resolved 2/4:
`astropy__astropy-13236` and `django__django-11532`. It used 2,022,390 total
tokens, 135 LLM calls, and 5.331064 billing units. Per-case estimated costs were
1.318996, 0.748032, 0.257608, and 3.006428 respectively in the table's order.

Across the two V2 runs, `astropy__astropy-13236` is the only repeated F00 rescue
and resolved 2/2. This is a weak quality signal worth preserving. The cost
signal did not repeat: V2's mean cost was 3.699208, 27.6% above V1's 2.899048 on
the same four cases, with individual V2 runs ranging from 2.067352 to 5.331064.
Do not claim a cost reduction and do not expand to 136 cases. The next quality
screen should use a new cost-bounded F00 sample and keep the rate card enabled.

## V3 136-case result

Run `tag-rag-bge-m3-unstable136-20260717-r1` evaluated the entire historical
instability pool with batched `bge-m3` Dense+BM25 retrieval. The official local
harness, run with four workers, produced the following result:

| Metric | Historical E1 | Historical E2 | V3 RAG |
|---|---:|---:|---:|
| Resolved | 19/136 (14.0%) | 35/136 (25.7%) | **53/136 (39.0%)** |
| Change from V3 | +34 cases / +25.0 pp | +18 cases / +13.2 pp | - |
| Submitted | 136 | 136 | 136 |
| Completed by harness | 134 | 130 | 135 |
| Empty patches | 2 | 5 | 1 |
| Harness errors | 0 | 1 | 0 |

The V3 result is 26 cases and 19.1 percentage points above the historical
two-run mean of 27/136. It therefore clears the predeclared `>=42/136` repeat
gate by 11 cases. `sphinx-doc__sphinx-9367` reached the 250-call model limit,
produced no patch, and is the single empty-patch case.

### Case-level transitions

The pool contains 82 F00 cases unresolved in both historical runs and 54 F01
cases resolved in exactly one historical run:

| Transition | Count |
|---|---:|
| F00 rescued | **20/82** |
| F00 still unresolved | 62/82 |
| F01 held | **33/54** |
| F01 regressed | 21/54 |

The 53 resolved cases are exactly the 20 F00 rescues plus the 33 F01 holds.
The net gain is material, but 21 F01 regressions show that this single run
cannot establish stability by itself.

| Repository | Cases | E1 | E2 | V3 | F00 rescued | F01 held | F01 regressed |
|---|---:|---:|---:|---:|---:|---:|---:|
| astropy | 8 | 1 | 1 | 2 | 1 | 1 | 1 |
| django | 64 | 9 | 18 | 26 | 10 | 16 | 11 |
| matplotlib | 9 | 2 | 1 | 3 | 1 | 2 | 1 |
| mwaskom | 1 | 0 | 1 | 0 | 0 | 0 | 1 |
| psf | 3 | 0 | 0 | 0 | 0 | 0 | 0 |
| pydata | 3 | 0 | 1 | 1 | 0 | 1 | 0 |
| pylint-dev | 5 | 0 | 1 | 2 | 1 | 1 | 0 |
| pytest-dev | 3 | 1 | 0 | 1 | 1 | 0 | 1 |
| scikit-learn | 5 | 1 | 2 | 3 | 0 | 3 | 0 |
| sphinx-doc | 13 | 1 | 6 | 5 | 2 | 3 | 4 |
| sympy | 22 | 4 | 4 | 10 | 4 | 6 | 2 |

<details>
<summary>F00 rescues (20)</summary>

- `astropy__astropy-13236`
- `django__django-11728`
- `django__django-11885`
- `django__django-12308`
- `django__django-13590`
- `django__django-13820`
- `django__django-14155`
- `django__django-14792`
- `django__django-15629`
- `django__django-15916`
- `django__django-16454`
- `matplotlib__matplotlib-20676`
- `pylint-dev__pylint-4551`
- `pytest-dev__pytest-7324`
- `sphinx-doc__sphinx-10435`
- `sphinx-doc__sphinx-11510`
- `sympy__sympy-15017`
- `sympy__sympy-15875`
- `sympy__sympy-17318`
- `sympy__sympy-20916`

</details>

<details>
<summary>F01 regressions (21)</summary>

- `astropy__astropy-14182`
- `django__django-11532`
- `django__django-12325`
- `django__django-13401`
- `django__django-13406`
- `django__django-13807`
- `django__django-14376`
- `django__django-14404`
- `django__django-15563`
- `django__django-16263`
- `django__django-16502`
- `django__django-16631`
- `matplotlib__matplotlib-21568`
- `mwaskom__seaborn-3069`
- `pytest-dev__pytest-5840`
- `sphinx-doc__sphinx-7985`
- `sphinx-doc__sphinx-8548`
- `sphinx-doc__sphinx-9281`
- `sphinx-doc__sphinx-9367`
- `sympy__sympy-18763`
- `sympy__sympy-22080`

</details>

### Model cost

The cost comparison below re-aggregates the exact same 136 case files from E1
and E2. All three columns use the configured GLM-5.2 rate card of 8 billing
units per million uncached input tokens, 2 per million cached input tokens, and
28 per million output tokens.

| Metric | Historical E1 | Historical E2 | V3 RAG |
|---|---:|---:|---:|
| Prompt tokens | 86,755,137 | 123,528,628 | 98,440,497 |
| Cached input tokens | 85,195,456 | 121,957,312 | 96,597,632 |
| Uncached input tokens | 1,559,681 | 1,571,316 | 1,842,865 |
| Completion tokens | 1,413,942 | 1,594,946 | 1,410,121 |
| Total tokens | 88,169,079 | 125,123,574 | 99,850,618 |
| LLM calls | 4,593 | 5,077 | 4,691 |
| Estimated model cost | 222.458736 | 301.143640 | 247.421572 |
| Tokens per resolved | 4,640,478 | 3,574,959 | **1,883,974** |
| Cost per resolved | 11.708355 | 8.604104 | **4.668332** |

V3 used 20.2% fewer total tokens and 17.8% less estimated model cost than E2
while resolving 18 more cases. Against E1, V3 used 13.2% more total tokens and
11.2% more model cost while resolving 34 more cases. The efficiency result is
therefore positive even though absolute cost is not lower than both historical
runs: cost per resolved fell 45.7% versus E2 and 60.1% versus E1.

Embedding was self-hosted and has no monetary charge in this experiment. Its
operational cost is significant and is reported separately.

### Retrieval and runtime

The run indexed and proactively retrieved workspace context for every case:

| Metric | V3 RAG |
|---|---:|
| Indexed documents | 1,611,873 |
| Preloaded documents | 544 (4 per case) |
| Preloaded characters | 646,780 |
| Follow-up `code_search` calls | 24 |
| Embedding requests / batched requests | 25,409 / 25,249 |
| Embedded inputs | 1,612,033 |
| Final embedding errors | 0 |
| Recovered HTTP 502 retries in runner log | 22 |
| Aggregate embedding duration | 243,667,641 ms |
| Aggregate case duration | 270,089,490 ms |
| Mean / median case duration | 33.1 / 37.2 minutes |

Embedding accounts for 90.2% of aggregate case duration. These are accumulated
per-case durations and must not be interpreted as serial wall time because
cases ran concurrently. The first 21 predictions were produced with two case
workers; the remaining 115 resumed safely with 15 workers. End-to-end
prediction generation spanned about 6 hours 43 minutes, and the official
four-worker harness took about 23 minutes.

The bottleneck is repository-wide embedding, not the model loop. Batching kept
the service healthy at 15 concurrent cases, but retrieval latency is too high
for a production default. The next performance experiment should change only
one framework-level variable at a time: persistent content-hash embedding
cache, lexical candidate prefiltering before dense embedding, or a larger
embedding batch.

### R1 decision

This is a strong, verifiable resolve-rate signal and a positive
model-cost-per-resolved signal. It is not yet a stability result: the exact
same 136 cases and configuration must be repeated once because 21 F01 cases
regressed and the historical pool itself was selected for variance.

Do not expand directly to 500 cases. Promote V3 to a full-500 candidate only if
the repeat again reaches at least 42/136 without materially worsening F01
retention or model efficiency. Optimize embedding latency separately so a
speed change cannot be confused with a quality change.

The machine-readable result is
[`v3-bge-m3-unstable136-r1.json`](./v3-bge-m3-unstable136-r1.json).

## V3 exact repeat result

Run `tag-rag-bge-m3-unstable136-20260717-r2` repeated the same 136 cases with
the same model, prompts, retrieval configuration, framework binary, and
official harness. It used the established 15-case-worker strategy from the
start instead of R1's initial 21-case two-worker warm-up.

| Metric | Historical E1 | Historical E2 | V3 R1 | V3 R2 |
|---|---:|---:|---:|---:|
| Resolved | 19/136 | 35/136 | **53/136** | **50/136** |
| Resolve rate | 14.0% | 25.7% | **39.0%** | **36.8%** |
| Completed by harness | 134 | 130 | 135 | 134 |
| Empty patches | 2 | 5 | 1 | 2 |
| Harness errors | 0 | 1 | 0 | 0 |

R2 is three cases below R1 but eight cases above the predeclared 42-case repeat
gate. The two RAG runs average 51.5/136 (37.9%), versus the historical
two-run mean of 27/136 (19.9%): a repeated gain of 24.5 cases and 18.0
percentage points. The aggregate resolve-rate signal therefore reproduced.

R2's two empty patches were
`scikit-learn__scikit-learn-14894` and `sympy__sympy-17318`; both reached the
250-call model limit. The official harness itself reported no errors.

### Repeatability and transitions

Aggregate quality repeated, but individual case identity remained volatile:

| Repeat transition | Count |
|---|---:|
| Resolved in both R1 and R2 | 32 |
| Resolved only in R1 | 21 |
| Resolved only in R2 | 18 |
| Unresolved in both | 65 |
| Resolved union | 71 |
| R1/R2 resolved-set Jaccard | 45.1% |

Only 32/136 cases resolved in both runs. Of those, 12 were persistent F00
rescues and 20 were persistent F01 holds. R1 and R2 therefore support the
framework-level aggregate effect, not a claim that any individual rescue is
deterministic.

| Historical transition | V3 R1 | V3 R2 |
|---|---:|---:|
| F00 rescued | 20/82 | **21/82** |
| F00 still unresolved | 62/82 | 61/82 |
| F01 held | **33/54** | 29/54 |
| F01 regressed | 21/54 | 25/54 |

R2 rescued one more F00 case than R1 but held four fewer F01 cases. Its net
resolved count was therefore three lower.

| Repository | R1 | R2 | Both | R1 only | R2 only |
|---|---:|---:|---:|---:|---:|
| astropy | 2 | 2 | 1 | 1 | 1 |
| django | 26 | 27 | 18 | 8 | 9 |
| matplotlib | 3 | 4 | 2 | 1 | 2 |
| mwaskom | 0 | 0 | 0 | 0 | 0 |
| psf | 0 | 0 | 0 | 0 | 0 |
| pydata | 1 | 0 | 0 | 1 | 0 |
| pylint-dev | 2 | 1 | 0 | 2 | 1 |
| pytest-dev | 1 | 1 | 0 | 1 | 1 |
| scikit-learn | 3 | 2 | 2 | 1 | 0 |
| sphinx-doc | 5 | 7 | 4 | 1 | 3 |
| sympy | 10 | 6 | 5 | 5 | 1 |

### Repeat cost

| Metric | Historical E1 | Historical E2 | V3 R1 | V3 R2 |
|---|---:|---:|---:|---:|
| Total tokens | 88,169,079 | 125,123,574 | 99,850,618 | 116,340,055 |
| LLM calls | 4,593 | 5,077 | 4,691 | 5,085 |
| Estimated model cost | 222.458736 | 301.143640 | 247.421572 | 286.944040 |
| Tokens per resolved | 4,640,478 | 3,574,959 | **1,883,974** | **2,326,801** |
| Cost per resolved | 11.708355 | 8.604104 | **4.668332** | **5.738881** |

R2 was 16.5% higher in total tokens and 16.0% higher in estimated model cost
than R1, so R1's exact efficiency did not repeat. It remained materially more
efficient than E2: 7.0% fewer total tokens, 4.7% lower total model cost, 34.9%
fewer tokens per resolved, and 33.3% lower cost per resolved while resolving
15 more cases.

Across the two runs in each lane:

| Two-run aggregate | Historical E1+E2 | V3 R1+R2 | Change |
|---|---:|---:|---:|
| Resolved outcomes | 54 | **103** | +90.7% |
| Total tokens | 213,292,653 | 216,190,673 | +1.4% |
| Estimated model cost | 523.602376 | 534.365612 | +2.1% |
| Tokens per resolved outcome | 3,949,864 | **2,098,939** | -46.9% |
| Cost per resolved outcome | 9.696340 | **5.188016** | -46.5% |

This is the stable cost conclusion: nearly twice as many resolved outcomes for
approximately the same two-run model spend. Embedding remains self-hosted and
is excluded from monetary cost.

### Repeat runtime

| Metric | V3 R1 | V3 R2 |
|---|---:|---:|
| Prediction wall time | about 6h 43m | 6h 36m |
| Harness wall time | about 23m | 22m 24s |
| Indexed documents | 1,611,873 | 1,611,873 |
| Embedding requests | 25,409 | 25,412 |
| Recovered HTTP 502 retries | 22 | 47 |
| Final embedding errors | 0 | 0 |
| Aggregate embedding duration | 243,667,641 ms | 302,308,138 ms |
| Aggregate case duration | 270,089,490 ms | 332,754,039 ms |
| Follow-up `code_search` calls | 24 | 27 |

Embedding represented 90.9% of R2 aggregate case duration. R2's median
embedding duration was close to R1 (35.9 versus 35.7 minutes), but its tail
grew: maximum embedding duration increased from 75.5 to 104.9 minutes. The
service recovered every retry and prediction wall time remained comparable,
but embedding is still the dominant operational cost.

### Repeat decision

V3 passes the predeclared repeat gate and should be promoted to a full-500
candidate. The full-500 validation should retain the current configuration so
that a quality result is not confounded by a simultaneous retrieval-speed
change.

The evidence boundary is important:

- claim a repeated aggregate resolve-rate improvement and a repeated
  model-cost-per-resolved improvement;
- do not claim deterministic case-level rescue, because the resolved-set
  Jaccard is only 45.1%;
- do not claim lower absolute model cost than every baseline run;
- treat repository-wide embedding latency as the primary production blocker.

After the unchanged full-500 quality run, evaluate persistent content-hash
embedding cache first, then lexical candidate prefiltering or larger embedding
batches as isolated performance changes.

The repeat comparison is stored in
[`v3-bge-m3-unstable136-repeat.json`](./v3-bge-m3-unstable136-repeat.json).
