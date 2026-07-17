# SWE-Bench Results

This directory stores the committed result summaries and reports for SWE-Bench
Verified.

Large runtime artifacts are intentionally excluded from git. Keep full
predictions, patches, traces, local-harness logs, workspaces, and batch outputs
under ignored runtime directories such as `results/runs/` or external artifact
storage, and reference them from the report with hashes and paths.

Committed files:

- `REPORT.md`: English report.
- `REPORT.zh_CN.md`: Chinese report.
- `baseline-mini-swe-agent-m2.5.json`: structured baseline summary for the
  accepted MiniMax M2.5 + mini-SWE-agent full-500 run.
- `experiments/`: small structured summaries for additional runs that inform
  later experiments but are not the accepted evaluator-calibration baseline.
- `experiments/observation-codec/REPORT.zh_CN.md`: JSON/XML observation codec
  experiment and the preliminary recommendation for an opt-in per-tool TAG
  `json2xml` tool-result codec.
- `experiments/observation-codec/json-vs-xml-e1.json`: machine-readable run,
  verifier, usage, billing, reconciliation, and comparison data for that
  experiment.
- `experiments/workspace-rag/README.md`: framework-generic workspace retrieval
  variants, decision gates, and the 136-case batched hybrid RAG result.
- `experiments/workspace-rag/v3-bge-m3-unstable136-r1.json`: machine-readable
  official-harness, F00/F01 transition, model-cost, and retrieval-runtime data
  for the V3 result.
- `experiments/workspace-rag/v3-bge-m3-unstable136-repeat.json`:
  machine-readable R1/R2 repeatability, two-run efficiency, and promotion
  decision for the V3 result.
