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

The runner defaults to `--workspace-preload=true`. A controlled preload
ablation may pass `--workspace-preload=false`; this still builds the identical
workspace index and keeps `code_search` available, but does not append
`workspace_context` to the initial prompt. The manifest records
`workspace_preload`, and each case records `workspace_index.preload_injected`.

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
  ./trpc-agent-go \
  ./trpc-agent-go/knowledge/document/reader/python
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

## V3 full-500 result

Run `tag-rag-bge-m3-full500-20260718-r1` evaluated all 500 SWE-Bench Verified
cases with the unchanged R2 framework, model, retrieval configuration, and
15-case-worker strategy. The submitted ID sets are identical to historical E1
and E2. The checked-in 500-case list has SHA-256
`a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9`.

The official local harness used four workers and calibrated verifier mode:

| Metric | Historical E1 | Historical E2 | V3 RAG full-500 |
|---|---:|---:|---:|
| Resolved | 383/500 | **399/500** | 394/500 |
| Resolve rate | 76.6% | **79.8%** | 78.8% |
| Completed by harness | 498 | 494 | 499 |
| Empty patches | 2 | 5 | 1 |
| Harness errors | 0 | 1 | 0 |

V3 is 11 cases and 2.2 percentage points above E1, but five cases and one
percentage point below E2. It is only three cases and 0.6 percentage points
above the historical two-run mean of 391/500 (78.2%). This does not establish a
material full-500 resolve-rate gain.

The single empty patch was `sphinx-doc__sphinx-10435`. Its generation hit an
endpoint timeout after about 71.5 minutes; the runner recorded one error while
the official harness correctly classified the submitted empty patch and
reported no evaluator errors.

### Full-pool transitions

Historical E1/E2 divide the pool into 364 F11 cases resolved in both runs, 82
F00 cases unresolved in both, and 54 F01 cases resolved in exactly one:

| Historical class | V3 resolved | V3 unresolved |
|---|---:|---:|
| F11 baseline-consensus cases | **345/364** | 19/364 |
| F00 rescues | **16/82** | 66/82 |
| F01 holds | **33/54** | 21/54 |

The 49 resolved cases from the historical instability pool plus 345 F11 holds
produce the 394 full-pool total. Retrieval adds difficult-case rescues, but
regressions in 19 baseline-consensus cases offset that gain. V3 therefore
should not become an unconditional default from this result.

### Does the 136-case signal survive?

Yes at the aggregate level. Projecting the full-500 run onto the exact original
136 IDs gives 49/136 (36.0%), seven cases above the predeclared 42-case gate:

| Exact 136-case run | Resolved | F00 rescued | F01 held |
|---|---:|---:|---:|
| V3 R1 | 53/136 | 20/82 | 33/54 |
| V3 R2 | 50/136 | 21/82 | 29/54 |
| Full-500 projection | 49/136 | 16/82 | 33/54 |

The three V3 observations average 50.7/136 (37.3%), versus the historical mean
of 27/136 (19.9%): +23.7 cases and +17.4 percentage points. The difficult-pool
aggregate signal has therefore repeated a third time.

Case identity remains unstable. The full-500 projection shares 36 resolved
cases with R1 (54.5% Jaccard) and 30 with R2 (43.5% Jaccard). It preserves
24/32 cases resolved in both R1 and R2, but only 6/12 persistent F00 rescues.
This supports a population-level retrieval effect, not deterministic rescue of
individual cases.

### Full-500 model cost

All columns use the same GLM-5.2 rate card: 8 billing units per million
uncached input tokens, 2 per million cached input tokens, and 28 per million
output tokens.

| Metric | Historical E1 | Historical E2 | V3 RAG full-500 |
|---|---:|---:|---:|
| Prompt tokens | 206,203,477 | 254,957,626 | 210,729,897 |
| Cached input tokens | 201,521,216 | 250,148,160 | 205,124,672 |
| Uncached input tokens | 4,682,261 | 4,809,466 | 5,605,225 |
| Completion tokens | 3,600,312 | 3,842,970 | 3,485,052 |
| Total tokens | 209,803,789 | 258,800,596 | 214,214,949 |
| LLM calls | 13,090 | 13,829 | 12,813 |
| Estimated model cost | 541.309256 | 646.375208 | 552.672600 |
| Tokens per resolved | 547,791 | 648,623 | **543,693** |
| Cost per resolved | 1.413340 | 1.619988 | **1.402722** |

V3's absolute model cost is between the two baselines: +2.1% versus E1 and
-14.5% versus E2. Its cost per resolved is the lowest, but only 0.8% below E1
and 13.4% below E2. Claim the efficiency result; do not claim lower absolute
model spend than both baselines.

On the exact 136-case projection, V3 used 86,118,423 total tokens and 218.951704
billing units, or 1,757,519 tokens and 4.468402 billing units per resolved
case. The model-efficiency signal from R1/R2 therefore also survives in the
full run.

### Full-500 retrieval and runtime

| Metric | V3 RAG full-500 |
|---|---:|
| Indexed documents | 5,693,838 |
| Preloaded documents / characters | 2,000 / 2,389,034 |
| Follow-up `code_search` calls | 86 |
| Embedding requests / batched requests | 89,804 / 89,218 |
| Embedded inputs | 5,694,424 |
| Final embedding errors | 0 |
| Recovered HTTP 502 retries | 209 |
| Aggregate embedding duration | 996,008,745 ms |
| Aggregate case duration | 1,056,741,067 ms |
| Mean / median / maximum case duration | 35.2 / 38.5 / 79.4 minutes |
| Prediction wall time | 19h 48m |
| Four-worker harness wall time | 1h 28m 38s |

Embedding accounts for 94.3% of aggregate case duration. Every one of the 209
observed HTTP 502 retries recovered on the first retry; there were no final
embedding errors, fatal errors, or panics. Operational reliability is
acceptable, but repository-wide embedding makes the prediction wall time about
seven times the 2h48m-2h59m historical baseline range.

### Full-500 decision

V3 demonstrates a repeated aggregate improvement on the deliberately difficult
136-case pool and competitive model cost per resolved case. It does not
demonstrate a material full-500 resolve-rate gain, deterministic case-level
rescue, or acceptable retrieval latency for a default-on production path.

The next quality experiment should route retrieval selectively while measuring
F11 preservation. The first isolated performance change should remain a
persistent content-hash embedding cache; lexical prefiltering and larger
batches follow only after cache behavior is measured.

The machine-readable full-pool result is
[`v3-bge-m3-full500-r1.json`](./v3-bge-m3-full500-r1.json).

## V3 initial-preload ablation

The full-500 run regressed on 19 of the 364 cases resolved by both historical
TAG runs. A single rerun of those observed regressions would be confounded by
selection and regression to the mean, so the preload ablation uses a fixed
54-case panel:

- the 19 observed F11 regressions;
- 19 F11 cases that RAG held, matched without replacement within repository on
  problem length and historical E1/E2 mean LLM calls;
- all 16 F00 cases rescued by the full-500 RAG run.

Arm A retains the current Top-4 initial preload and `code_search`. Arm B
withholds only the initial preload while building the same hybrid index and
retaining the same `code_search` tool. Both arms run twice with one fixed
binary, model, case list, worker count, embedding configuration, and XML
observation codec. Generation order is A1, B1, B2, A2 to balance time drift;
the official four-worker harness runs only after all four generations.

The primary comparison is the paired no-preload minus preload resolution rate
on the 19 regressions, interpreted alongside the matched F11 controls, F00
rescue retention, and within-arm repeatability. Two repetitions can establish
direction and expose a large effect, but they are not a high-power significance
test.

The frozen panel, matching metadata, protocol, and predeclared estimands are in
[`v3-bge-m3-preload-ablation-54-plan.json`](./v3-bge-m3-preload-ablation-54-plan.json).

### Preload-ablation result

All four generations completed with 54 non-empty predictions, and all four
official local-harness evaluations completed without empty patches or evaluator
errors. The evaluator used the calibrated verifier, four workers, and
`clean=false`.

| Run | Initial preload | Resolved | Unresolved | Harness time |
|---|---:|---:|---:|---:|
| A1 | yes | 40/54 | 14 | 7m47s |
| B1 | no | 41/54 | 13 | 7m27s |
| B2 | no | 39/54 | 15 | 8m07s |
| A2 | yes | 38/54 | 16 | 7m25s |

The ablation boundary matters: both arms build the same hybrid index, retrieve
the same initial Top-4 candidates, and expose `code_search`. The B arm only
withholds those candidates from the first task prompt. It does not disable RAG,
index construction, or initial retrieval.

### Primary causal comparison

Pooling the two repetitions within each arm gives:

| Fixed subgroup | Preload A | No-preload B | B minus A |
|---|---:|---:|---:|
| 19 observed F11 regressions | 26/38 (68.4%) | 26/38 (68.4%) | **0/38 (0.0pp)** |
| 19 matched F11 holds | 35/38 (92.1%) | 36/38 (94.7%) | +1/38 (+2.6pp) |
| 16 observed F00 rescues | 17/32 (53.1%) | 18/32 (56.3%) | +1/32 (+3.1pp) |
| Entire 54-case panel | 78/108 (72.2%) | 80/108 (74.1%) | +2/108 (+1.9pp) |

The predeclared difference-in-differences is therefore
`0.0pp - 2.6pp = -2.6pp`. Disabling initial prompt injection does not improve
the selected 19 regressions relative to their matched controls.

Twelve of the 19 target cases resolve in both no-preload repetitions, but that
number is not evidence of preload harm by itself: the cases were selected
because one earlier RAG run failed them, so reruns are expected to regress
toward their historically successful outcome. The two-arm comparison removes
that misleading recovery signal.

### The 19 target cases

`1` means resolved by the official harness. Delta is the number of B successes
minus the number of A successes across the two repetitions.

| Instance | A1 | A2 | B1 | B2 | Delta | Read |
|---|---:|---:|---:|---:|---:|---|
| `django__django-10973` | 0 | 0 | 0 | 0 | 0 | stable failure |
| `django__django-11239` | 1 | 1 | 0 | 0 | -2 | strict reverse of hypothesis |
| `django__django-13346` | 1 | 0 | 0 | 1 | 0 | unstable, arm tie |
| `django__django-13810` | 1 | 1 | 1 | 1 | 0 | stable success |
| `django__django-14311` | 1 | 0 | 1 | 1 | +1 | directional B signal |
| `django__django-14559` | 1 | 1 | 1 | 1 | 0 | stable success |
| `django__django-15268` | 1 | 1 | 1 | 1 | 0 | stable success |
| `django__django-15987` | 0 | 0 | 0 | 0 | 0 | stable failure |
| `django__django-16938` | 0 | 1 | 1 | 1 | +1 | directional B signal |
| `matplotlib__matplotlib-25287` | 1 | 1 | 1 | 1 | 0 | stable success |
| `psf__requests-6028` | 1 | 1 | 1 | 1 | 0 | stable success |
| `pydata__xarray-4687` | 1 | 0 | 0 | 0 | -1 | directional A signal |
| `pydata__xarray-7229` | 0 | 0 | 1 | 0 | +1 | single-run B recovery |
| `scikit-learn__scikit-learn-12973` | 1 | 1 | 0 | 0 | -2 | strict reverse of hypothesis |
| `sphinx-doc__sphinx-10673` | 1 | 1 | 1 | 1 | 0 | stable success |
| `sphinx-doc__sphinx-9258` | 1 | 1 | 1 | 1 | 0 | stable success |
| `sympy__sympy-12489` | 0 | 0 | 1 | 1 | +2 | only strict hypothesized case |
| `sympy__sympy-13877` | 1 | 1 | 1 | 1 | 0 | stable success |
| `sympy__sympy-15809` | 1 | 1 | 1 | 1 | 0 | stable success |

Only `sympy__sympy-12489` shows the strict hypothesized pattern
`A1=A2=0, B1=B2=1`. Two cases show the strict reverse pattern. At the looser
directional level, four cases favor B, three favor A, and twelve tie. This is
not a coherent case-level signature of misleading initial preload.

### Repeatability and rescue identity

| Arm | Both resolved | First only | Second only | Neither | Agreement | Resolved-set Jaccard |
|---|---:|---:|---:|---:|---:|---:|
| Preload A | 33 | 7 | 5 | 9 | 77.8% | 73.3% |
| No-preload B | 36 | 5 | 3 | 10 | 85.2% | 81.8% |

No-preload is descriptively more repeatable on this panel, but two repetitions
and one time-ordered block are insufficient to attribute that difference to
the arm. The original 16 F00 rescues are especially unstable: only 5/16 are
resolved in both A runs and 6/16 in both B runs, with Jaccard values of 41.7%
and 50.0%. The earlier full-run rescue identities should not be treated as
deterministic.

### Cost and retrieval behavior

| Pooled arm metric | Preload A | No-preload B | B versus A |
|---|---:|---:|---:|
| Resolved outcomes | 78/108 | 80/108 | +2 |
| Total model tokens | 58,649,286 | 61,641,366 | +5.1% |
| Estimated model cost | 149.403076 | 155.757900 | +4.3% |
| Tokens per resolved outcome | 751,914 | 770,517 | +2.5% |
| Cost per resolved outcome | 1.915424 | 1.946974 | +1.6% |
| Follow-up `code_search` calls | 22 | 69 | **3.14x** |
| Embedded inputs | 1,220,012 | 1,220,059 | +47 |
| Aggregate embedding duration | 191,466,370 ms | 192,483,460 ms | +0.5% |

Both arms indexed exactly 609,941 documents per repetition and selected 216
initial candidates with 259,042 characters. The preload arm injected those
candidates in all 108 case-runs; the no-preload arm injected none. The latter
then made 47 more dynamic `code_search` calls and exactly 47 more embedding
inputs, showing that the agent partly compensates for missing initial context.

Embedding still consumed 90.8% of aggregate case time in A and 92.7% in B.
Withholding prompt injection therefore cannot provide the desired indexing
speedup. All 100 observed embedding retry log entries across the four runs
recovered; there were no final embedding errors, fatal errors, or panics.

### Preload-ablation decision

This experiment rejects the operational hypothesis that unconditional initial
preload is the main cause of the 19 observed full-run regressions. It does not
justify making no-preload the default: target-group quality is unchanged,
matched-control-adjusted quality is slightly worse, and no-preload uses more
tokens, model cost, and follow-up search while remaining subject to the same
indexing latency.

The result is compatible with regression to the mean plus ordinary agent
stochasticity, with isolated case-specific preload effects in both directions.
The next quality experiment should test relevance-gated or selective initial
injection rather than globally removing preload. The first performance change
should remain persistent content-hash embeddings, because that is the work
shared by both arms.

The complete provenance, per-run metrics, primary estimands, and all 54
case-level outcomes are in
[`v3-bge-m3-preload-ablation-54-result.json`](./v3-bge-m3-preload-ablation-54-result.json).

## AST representation retrieval gate

The four-arm offline replay completed all 54 selected cases without a case,
embedding, cache, fatal, or panic error. Each case used one official testbed
snapshot and indexed the four representations serially in the declared order.
The query was always the original problem statement, BGE-M3 hybrid retrieval
returned at most six chunks, and the persistent embedding cache remained in
read-write mode.

### Localization quality

`fixed-raw` is the primary control because it preserves indentation while
changing neither chunk size nor overlap. `current-fixed` remains a historical
line-trimmed reference rather than the causal AST control.

| Arm | Target-file R@4 | Target-file R@6 | MRR | Hunk R@4 | Hunk R@6 | Target char P@6 |
|---|---:|---:|---:|---:|---:|---:|
| `current-fixed` | 0.5309 | 0.5463 | 0.4241 | 0.2000 | 0.2213 | 0.2323 |
| `fixed-raw` | 0.4537 | 0.5185 | 0.4015 | 0.1601 | 0.1973 | 0.2332 |
| `ast-code` | 0.4383 | 0.5586 | 0.3759 | 0.2022 | 0.2428 | 0.2109 |
| `ast-structured` | 0.4762 | **0.5873** | 0.4173 | **0.2482** | **0.2706** | **0.2577** |

The paired deltas below are absolute mean changes across the same 54 cases;
`W/T/L` counts cases where the left arm improved, tied, or regressed.

| Metric | AST boundary: `ast-code - fixed-raw` | Structured text: `ast-structured - ast-code` | Overall: `ast-structured - fixed-raw` |
|---|---:|---:|---:|
| Target-file R@4 | -0.0154 (8/37/9) | +0.0379 (8/42/4) | +0.0225 (11/34/9) |
| Target-file R@6 | +0.0401 (8/39/7) | +0.0287 (5/47/2) | **+0.0688 (10/38/6)** |
| MRR | -0.0256 (14/26/14) | +0.0414 (12/33/9) | +0.0157 (17/25/12) |
| Hunk R@4 | +0.0421 (15/31/8) | +0.0459 (6/46/2) | **+0.0881 (17/28/9)** |
| Hunk R@6 | +0.0455 (15/33/6) | +0.0278 (5/48/1) | **+0.0733 (17/30/7)** |
| Target char P@6 | -0.0223 (17/16/21) | +0.0469 (26/19/9) | +0.0245 (22/13/19) |

AST boundaries alone are mixed: they improve R@6 and both hunk metrics but
reduce R@4, MRR, and character precision. Adding stable AST fields over the
same boundaries improves all six aggregate metrics. The complete
`ast-structured` arm also beats `fixed-raw` on all six aggregates, with the
clearest gains in target-file R@6 and hunk localization. The effect is not
universal—six cases regress on target-file R@6 and seven regress on hunk R@6—
but it is material enough to pass this retrieval-only gate.

### Coverage, representation, and operations

Every arm indexed all 74,293 eligible file instances across the panel. There
were zero eligible/indexed hash mismatches within an arm and zero eligible or
indexed file-set hash mismatches across arms. Each arm used exactly one stable
representation schema and representation hash across all 54 cases. The two AST
arms have identical boundaries, counts, fallbacks, node distributions, and
duplicate statistics; their document-set hashes differ by design because their
embedded text differs.

| Arm | Documents | Indexed / eligible files | Fallback documents | Weighted duplicates | Mean index time |
|---|---:|---:|---:|---:|---:|
| `current-fixed` | 609,941 | 74,293 / 74,293 | 0 | 2,295 (0.3763%) | 221.694 s |
| `fixed-raw` | 728,047 | 74,293 / 74,293 | 0 | 2,452 (0.3368%) | 238.160 s |
| `ast-code` | 1,483,836 | 74,293 / 74,293 | 1,832 (0.1235%) | 83,778 (5.6460%) | 334.140 s |
| `ast-structured` | 1,483,836 | 74,293 / 74,293 | 1,832 (0.1235%) | 83,778 (5.6460%) | 242.976 s |

AST node totals are 797,851 methods, 280,019 functions, 266,551 classes,
137,553 variables, 30 interfaces, and 1,832 whole-file fallbacks. The fallback
reasons are 1,799 files with no supported nodes and 33 parse errors. Full file
coverage means these fallbacks did not drop source files. The AST document
count is 2.04x `fixed-raw`, and its weighted duplicate rate is materially
higher; both remain adoption risks for Agent context and cost.

| Arm | Embed requests / inputs | Embed errors | Embed duration | Cache hits / misses / writes | Cache hit rate |
|---|---:|---:|---:|---:|---:|
| `current-fixed` | 6,711 / 294,534 | 0 | 11,683,165 ms | 315,395 / 294,600 / 294,534 | 51.70% |
| `fixed-raw` | 7,808 / 350,705 | 0 | 12,512,590 ms | 377,341 / 350,760 / 350,705 | 51.83% |
| `ast-code` | 12,123 / 379,864 | 0 | 14,623,055 ms | 1,103,871 / 380,019 / 379,864 | 74.39% |
| `ast-structured` | 11,609 / 244,122 | 0 | 9,744,733 ms | 1,238,780 / 245,110 / 244,122 | 83.48% |

The run took 56,275,265 ms (15.632 hours). All 16 logged first-attempt 502
embedding retries recovered; the final cache contained 1,333,142 rows and
6,251,446,272 bytes. Because the arms ran in a fixed order against a warming
read-write cache, these timing and cache differences are operational
observations, not a clean causal speed comparison.

### Decision and next gate

`ast-structured` is the only AST candidate advanced. The next experiment is a
repeated, order-balanced Agent A/B with `fixed-raw` as control,
`ast-structured` as candidate, all non-representation settings fixed, and
patches scored by the official local harness. Resolve rate and paired case
outcomes are primary; tokens, model cost, retrieval/tool paths, latency, and
errors remain separate secondary evidence. The panel is not expanded to 136 or
500 cases unless that smaller Agent experiment shows a resolve-rate signal.

This remains a 54-case outcome-selected diagnostic panel, not a population
sample. Problem-statement replay cannot reproduce later model-written
`code_search` queries, and patch localization is only a proxy for an executable
fix. The result therefore selects an Agent candidate; it does not establish an
end-to-end resolve-rate gain.

The predeclared design is in
[`v4-bge-m3-ast-retrieval-plan.json`](./v4-bge-m3-ast-retrieval-plan.json).
Complete provenance, hashes, paired estimands, operations, transitions, and all
54 case-level metric rows are in
[`v4-bge-m3-ast-retrieval-54-result.json`](./v4-bge-m3-ast-retrieval-54-result.json).
The repeated, order-balanced Agent A/B that follows this gate is pre-registered
in
[`v5-bge-m3-ast-agent-ab-54-plan.json`](./v5-bge-m3-ast-agent-ab-54-plan.json).
