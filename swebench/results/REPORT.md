# SWE-Bench Verified Report

This report is a baseline-stage snapshot. The final comparison report will be
completed after the Go-native `trpc-agent-go` SWE agent finishes a full
SWE-Bench Verified run under the same evaluator.

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

## Internal GLM-5.2 Runs

Subsequent experiments, including mini-SWE-agent follow-up runs and the
Go-native `trpc-agent-go` implementation, use the internal GLM-5.2 endpoint
(`glm52`). This endpoint is not treated as the public leaderboard GLM-5.2
because it is internally deployed and may differ from the public model.

The mini-SWE-agent GLM-5.2 repeat-run summary is
[`experiments/mini-swe-agent-glm52-r3.json`](experiments/mini-swe-agent-glm52-r3.json).

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

## Native Run

The Go-native `trpc-agent-go` implementation has not produced a full 500-case
result yet. The final report will add native metrics, case-level comparison,
failure analysis, and reproduction notes after that run is complete.
