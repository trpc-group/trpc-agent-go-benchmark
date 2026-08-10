# SWE-Bench Results

This directory separates ignored runtime artifacts from small, sanitized,
provenance-complete experiment summaries.

- `runs/` and `artifacts/` are ignored runtime locations. Predictions, patches,
  traces, verifier logs, workspaces, and batch outputs belong there or in
  external artifact storage.
- `experiments/` contains machine-readable summaries that can be audited back
  to frozen source commits and content hashes without publishing raw model or
  server data.

The frozen source refs recorded in each bundle resolve against the public
[historical fork](https://github.com/hr-chang/trpc-agent-go-benchmark); SHA-256
values pin the exact source files used for each summary.

The public runner in this branch is a maintainable rebuild. Historical results
are evidence for the evaluated methods and frozen implementations; they are not
automatically attributed to the rebuilt runtime where the recorded protocol
differs.

## Headline results

| Question | Baseline | Candidate | Official-harness RR | Total tokens | Model-side cost |
| --- | --- | --- | --- | --- | --- |
| Tool-result observation codec | JSON | XML-like | 77.6% → 79.2% (+1.6pp) | 251.05M → 215.99M (-14.0%) | 626.55 → 553.22 (-11.7%) |
| Preloaded workspace-context representation | fixed-raw | AST-structured | 79.2% → 80.6% (+1.4pp) | 238.43M → 253.43M (+6.3%) | 598.48 → 638.58 (+6.7%) |
| Exact repeated-tool warning | warning off | warning on | 74.13% → 73.67% (-0.47pp) | 308.51M → 283.44M (-8.1%) | 843.18 → 773.30 (-8.3%) |

Cost is reported in reproducible billing units using the frozen rate card, not
as monetary spend. The machine-readable summaries also provide prompt,
completion, cached and uncached tokens; LLM/tool calls; errors and long tails;
run-level sample SD/range where repetitions exist; paired agreement; and
fixed-cache sensitivity at 0%, 90%, 95%, 98%, and 100% prompt cache hit.

## Result bundles

- [JSON vs XML-like observations](experiments/observation-codec/json-vs-xml-like.json)
- [fixed-raw vs AST-structured workspace representation](experiments/workspace-representation/fixed-raw-vs-ast-structured.json)
- [clean-room Native loop-warning on vs off](experiments/cleanroom-loop-warning/native-warning-on-vs-off.json)
- [current-revision Native full-panel baseline](experiments/native-baselines/current-revision-full-panel.json)

## What the evidence supports

1. XML-like tool-result observations are a validated configurable benchmark
   capability for this Coding Agent protocol. The result does not justify a
   model-independent serialization default.
2. The full-panel workspace-RAG realization favored AST-structured preloaded
   context by seven cases. Both arms injected preload for every case and made
   relatively few explicit `code_search` calls, so this primarily validates a
   context-representation direction rather than retrieval enablement itself and
   supports adopting AST-structured context in workspace RAG.
3. Across three runs per setting, warning-on reduced total tokens by 8.1% and
   model-side cost by 8.3% while mean RR remained nearly unchanged. Offline
   replay further confirms that substantial exact-repeat tails exist in
   warning-off trajectories.

## Reporting conventions

- RR always uses the fixed 500-case denominator and official SWE-bench harness
  `resolved` membership.
- Repeated runs of the same panel are summarized at run level; 1,500 case-runs
  are never treated as independent observations.
- Quality, tokens, cost, latency, errors, and trajectory behavior remain
  separate metrics.
- Raw trajectories, responses, patches, internal endpoints, absolute server
  paths, recovery controllers, and credentials are not committed.

Chinese: [README.zh_CN.md](README.zh_CN.md)
