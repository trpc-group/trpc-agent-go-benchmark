# SWE-Bench Verified Report

This report summarizes the baseline, repeated mini-SWE-agent experiments, and
full SWE-Bench Verified TAG runner results under the same calibrated evaluator.

## Baseline

The current accepted baseline is mini-SWE-agent 2.1.0 with MiniMax M2.5 on the
SWE-Bench Verified 500-case test split. This run is used to validate that the
baseline and evaluator chain are sane against a public SWE-Bench reference
point.

| Metric | Value |
| --- | ---: |
| Total cases | 500 |
| Submitted | 500 |
| Completed | 495 |
| Resolved | 380 |
| Unresolved | 115 |
| Empty patch | 1 |
| Error | 4 |
| Resolved rate | 76.00% |

The canonical summary is
[`baseline-mini-swe-agent-m2.5.json`](baseline-mini-swe-agent-m2.5.json).

## GLM-5.2-Internal Runs

The mini-SWE-agent GLM-5.2-Internal repeat-run summary is
[`experiments/mini-swe-agent-glm-5.2-internal-r3.json`](experiments/mini-swe-agent-glm-5.2-internal-r3.json).

| Run | Resolved | Unresolved | Empty patch | Error | Resolved rate |
| --- | ---: | ---: | ---: | ---: | ---: |
| r2 | 383 | 116 | 0 | 1 | 76.60% |
| r3 | 382 | 118 | 0 | 0 | 76.40% |
| r4 | 394 | 106 | 0 | 0 | 78.80% |

## Verifier

The result uses the calibrated official local SWE-Bench harness. The calibrated
mode keeps the official harness as the scoring path while applying local
compatibility fixes needed for the frozen evaluator environment, including
managed `httpbin.org` handling for `psf/requests` cases and compatibility fixes
for known old dependency stacks.

Single-case reruns are retained as review evidence but do not override the
full-500 aggregate used above.

## TAG Runner

`trpc-agent-go-impl` uses the TAG `llmagent`, runner, session, and tool callback
lifecycles to carry mini-SWE-agent-compatible behavior. Both runs used the same
GLM-5.2 model configuration, 500-case list, XML observations, 15 agent workers,
and calibrated official local harness. XML matches the mini-go XML lane and is
not treated as a TAG feature or experiment variable.

| Run | Source revision | Resolved | Unresolved | Empty patch | Error | Resolved rate |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| TAG e1 | `61b6e46` | 383 | 115 | 2 | 0 | 76.60% |
| TAG e2 | `c5504d8` | 399 | 95 | 5 | 1 | 79.80% |
| mini-go XML reference | `02e0796` | 396 | 103 | 0 | 1 | 79.20% |

Four of the five empty patches in e2 reached the 250-call LLM limit; the other
was a valid empty submission. The only e2 verifier error was the official test
for `sympy__sympy-19040` reaching the 1,800-second timeout. The mini-go XML
verifier error was the same case. These errors remain on the unresolved side
and are not removed from the 500-case denominator.

### Quality and Cost

Token and cost figures use the actual billing records aggregated by
`X-SMG-Agent-Name`. Both TAG runs used
`BenchSWE-tag-llmagent-runner-v1`.

| Run | LLM calls | Tool calls | Input tokens | Output tokens | Total tokens | Cached tokens | Cost | Cost / resolved |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| TAG e1 | 13,090 | 14,048 | 206,203,477 | 3,600,312 | 209,803,789 | 201,521,216 | 541.3093 | 1.4133 |
| TAG e2 | 13,829 | 14,707 | 254,957,626 | 3,842,970 | 258,800,596 | 250,148,160 | 646.3752 | 1.6200 |
| mini-go XML reference | 13,364 | 14,211 | 212,437,059 | 3,550,905 | 215,987,964 | 207,616,384 | 553.2235 | 1.3970 |

The two TAG runs cost 1,187.6845 in total, averaging 593.8423 per run. Their
mean resolved rate is 78.20%, and their aggregate cost per resolved case is
1.5188. Compared with e1, e2 improved resolved rate by 3.2 percentage points
while increasing cost by 19.4% and cost per resolved case by 14.6%.

The 16-case difference between the TAG runs shows substantial single-run
sampling variance. e2 reached the mini-go XML quality range, while the two-run
TAG average is 1.0 percentage point below mini-go XML and costs 7.3% more on
average. The current evidence does not show a stable framework-driven quality
regression, nor does it establish that TAG reduces cost. Future comparisons
should report resolved rate, total cost, and cost per resolved case together.

Full artifacts are retained in ignored runtime directories:

- `results/runs/tag-impl-full500-20260715-e1/`
- `results/runs/tag-impl-full500-20260716-e2/`
