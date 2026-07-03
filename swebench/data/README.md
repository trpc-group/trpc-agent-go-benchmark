# SWE-Bench-Verified Data

Place the SWE-Bench-Verified instance list here as:

```text
swebench_verified_cases.jsonl
```

To export from Hugging Face:

```bash
python3 -m pip install datasets
python3 export_verified_jsonl.py \
  --limit 3 \
  --output swebench_verified_cases.jsonl
```

Omit `--limit` for the full 500-case verified split.

Each line should contain one SWE-Bench instance JSON object. The loader
accepts the common fields used by SWE-Bench:

- `instance_id`
- `repo`
- `base_commit`
- `problem_statement`
- `patch`
- `test_patch`
- `version`
- `FAIL_TO_PASS`
- `PASS_TO_PASS`

The full 500-case list hash must be recorded in each official run's
`run_config.json`.
