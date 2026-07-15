# SWE-Bench Verified Report

This report is a baseline-stage snapshot. The final comparison report will be
completed after the Go-native `trpc-agent-go` SWE agent finishes a full
SWE-Bench Verified run under the same evaluator.

## Baseline

The current accepted baseline is mini-SWE-agent 2.1.0 with MiniMax M2.5 on the
SWE-Bench Verified 500-case test split.

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
