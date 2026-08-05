# SWE-Bench Runtime Results

`results/runs/` and `results/artifacts/` are ignored runtime locations. Keep
predictions, patches, traces, verifier logs, workspaces, and batch outputs there
or in external artifact storage.

Only small, sanitized, provenance-complete result summaries belong in git.
Canonical experiment reports are added separately from evaluator and runner
implementation changes.
