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
