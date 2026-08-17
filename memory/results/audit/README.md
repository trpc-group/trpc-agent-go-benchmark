# High-similarity memory audit

Aggregates behind the memory structure audit in the LongMemEval part of
[REPORT.md](../REPORT.md) and [REPORT.zh_CN.md](../REPORT.zh_CN.md).

The audit covers the same six runs and the same memory population as the
memory footprint table of that part: the three Auto update policies with
assistant-episode extraction disabled and enabled. Each run's population
matches the reported inventory exactly, including the case-local rebuild
tables that replace one case in two runs and the incomplete builds that the
report excludes. `longmemeval_memory_audit.py` fails if an audited
population does not equal the reported one.

## Files

- `high_similarity_summary.csv`: pair counts per run and similarity band.
- `high_similarity_summary.json`: the same aggregates plus the class
  definitions and per-run totals.
- `provenance.json`: source tables, case exclusions, run manifest version
  and comparison digests, and the SHA-256 digest of every consumed snapshot
  file.

## Classes

Every same-case memory pair with cosine similarity of at least 0.90 is
assigned one deterministic class: exact normalized duplicate, strict lexical
near-duplicate, directional containment, number or negation mismatch under
high lexical overlap, or vector-similar only. The classes are text
predicates. They are not semantic judgements, and the audit does not link a
pair to any update operation or to answer correctness.

## Reproduce

The snapshot holds dataset-derived memory text and is therefore not part of
the repository. Export it from the tables listed in `provenance.json` with
the two queries recorded there, one pair of files per source label:

```text
<label>_memories.csv.gz          memory_id,user_id,memory_content
<label>_similarity_ge090.csv.gz  user_id,memory_id_a,memory_id_b,cosine
```

Then regenerate the aggregates:

```bash
python3 memory/adapter/longmemeval_memory_audit.py \
  --snapshot-dir <snapshot> \
  --output-dir memory/results/audit \
  --results-dir <longmemeval results tree>
```

`--results-dir` is optional and only adds each run's manifest version and
digests to the provenance. The script uses the Python standard library only,
reads the snapshot read-only, and sends no benchmark text anywhere. Comparing
the digests in `provenance.json` shows whether a regenerated snapshot matches
the one behind the published aggregates.
