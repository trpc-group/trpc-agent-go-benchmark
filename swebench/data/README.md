# SWE-Bench Verified Data

This directory stores lightweight, safe dataset indexes for the SWE-Bench
Verified evaluation.

Committed files should only contain agent-visible or aggregate metadata, such as
the 500-case instance id list and its hash. Do not commit official gold patches,
test patches, FAIL_TO_PASS, PASS_TO_PASS, cloned repositories, image caches, or
raw dataset dumps.

Committed fixed inputs:

- `case-lists/verified-test-500.case_ids.txt`: canonical 500-case instance id list.
- `case-lists/verified-test-500.case_ids.sha256`: SHA256 of the sorted instance id
  list, using the same hash rule as `prepare-data`.
- `case-lists/offline-smoke.case_ids.txt`: small model-free clean-room preflight
  panel covering ordinary testbeds and both offline requests fixture/dependency
  profiles. It is an operational isolation gate, not an evaluation result set
  or a replacement denominator for the canonical 500-case panel.

Generated files from `evaluator prepare-data`:

- `generated/cases.jsonl`
- `generated/cases.sha256`
- `generated/cases.manifest.json`

The current dataset is `princeton-nlp/SWE-bench_Verified`, `test` split.
For that default pair, the case list and checksum are embedded in the evaluator
binary. Missing checkout-relative files therefore cannot silently disable the
fixed-panel check. Explicit `--expected-case-ids` and `--expected-case-hash`
paths remain available for reviewed alternate panels.
