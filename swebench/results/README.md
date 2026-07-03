# SWE-Bench-Verified Results

This directory stores run artifacts and reports for the SWE-Bench-Verified
benchmark.

Expected layout:

```text
results/
|-- README.md
|-- REPORT.md
|-- REPORT.zh_CN.md
|-- runs/
|   |-- mini/<run-id>/
|   |-- native/<run-id>/
|   |-- verifier/<run-id>/
|   `-- report/<run-id>/
`-- tools/
```

Full workspaces are not stored by default. Keep patches, traces, verifier
reports, and per-case metadata; preserve workspaces only for selected
debug cases.

Verifier runs use the official SWE-Bench local harness as the primary
resolved-status source. Keep `report.json`, `cases.jsonl`, `summary.json`, and
`logs/` for each verifier run. `sb-cli` reports may be stored as optional
cross-check artifacts when the hosted API is stable.
