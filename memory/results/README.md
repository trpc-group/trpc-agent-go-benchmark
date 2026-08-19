# Memory Benchmark Results

## Evaluation Results

This directory stores memory benchmark evaluation results.

### Reports

| File | Description |
|------|-------------|
| [REPORT.md](REPORT.md) | Full evaluation report (English) |
| [REPORT.zh_CN.md](REPORT.zh_CN.md) | Full evaluation report (Chinese) |

### LoCoMo Update Policy Results

This comparison uses the full LoCoMo-10 dataset, 1,986 QA items,
`gpt-4o-mini`, `text-embedding-3-small`, top-k 30, and two
`memory_search` passes. The Optimized configuration is the default Merge
Similar policy.

| Policy configuration | F1 | BLEU | LLM Score | Active memories |
| --- | ---: | ---: | ---: | ---: |
| Optimized / Merge Similar | 0.4690 | 0.4310 | 0.5320 | unavailable |
| **Preserve History** | **0.4865** | **0.4473** | **0.5609** | 2,740 |
| Append Only | 0.4773 | 0.4397 | 0.5441 | 2,627 |

Assistant-episode experiments are excluded: LoCoMo maps its second human
speaker to the assistant role and is not an appropriate evaluation of
model-generated assistant results. The Optimized row is quoted from the
archived report baseline. Category metrics, cost, and runtime are in the
LoCoMo part of [REPORT.md](REPORT.md).

### Historical LoCoMo Benchmark Evaluation Summary

**Configuration**:
- Model: gpt-4o-mini
- Samples: 10 (full LoCoMo-10)
- Total Questions: 1,986

**Key Results (No History Injection)**:

| Scenario | Backend | F1 | LLM Score |
|----------|---------|----:|----------:|
| Long-Context | - | **0.472** | **0.523** |
| Auto | pgvector | 0.357 | 0.366 |
| Auto | MySQL | 0.347 | 0.362 |
| Agentic | pgvector | 0.294 | 0.287 |
| Agentic | MySQL | 0.286 | 0.285 |

**History Injection Impact (Auto pgvector)**:

| Variant | F1 | LLM Score | Adversarial F1 |
|---------|----:|----------:|---------------:|
| No history | **0.357** | 0.366 | **0.771** |
| +300 turns | 0.296 | 0.414 | 0.514 |
| +700 turns | 0.288 | **0.464** | 0.418 |

**Key Insights**:
1. Memory extraction (Auto) achieves 75.6% of the long-context gold
   standard.
2. History injection trades F1 precision for semantic quality (LLM Score).
3. Adversarial robustness degrades with history injection (model attempts
   to answer unanswerable questions).
4. Open-domain LLM Score improves dramatically with history (+92.9%).

### SQLite vs SQLiteVec (Subset Runs)

We also run focused subset experiments comparing local SQLite keyword
matching (`sqlite`) vs sqlite-vec semantic search (`sqlitevec`).

**End-to-end QA subset (Auto / locomo10_1 / 199 QA, LLM Judge enabled)**:

| Backend | F1 | LLM Score | Prompt Tokens | Avg Prompt/QA |
|---------|---:|----------:|--------------:|--------------:|
| sqlite | 0.327 | 0.370 | 1,287,813 | 6,471 |
| sqlitevec | 0.307 | 0.325 | 407,969 | 2,050 |

**End-to-end QA subset (Auto / locomo10_6 / 158 QA, LLM Judge enabled)**:

| Backend | F1 | LLM Score | Prompt Tokens | Avg Prompt/QA |
|---------|---:|----------:|--------------:|--------------:|
| sqlite | 0.269 | 0.289 | 1,296,580 | 8,206 |
| sqlitevec | 0.274 | 0.295 | 362,903 | 2,297 |

Note: token usage above counts QA agent model calls only; it excludes
embedding requests and LLM-as-Judge calls. See `REPORT.md` for full
configuration and breakdown.

**Top-k sweep (Auto / locomo10_1 / LLM Judge disabled)**:

To understand how `sqlitevec` quality changes with retrieval size, we run a
small sweep on `locomo10_1` (199 QA). In this run, `sqlitevec` achieves the
best quality at the default top-k=10; increasing top-k increases tokens but
does not improve F1.

| Backend | vector-topk | qa-search-passes | F1 | Prompt Tokens | Avg Prompt/QA |
|---------|------------:|-----------------:|---:|--------------:|--------------:|
| sqlite | - | 1 | 0.299 | 1,322,360 | 6,645 |
| sqlitevec | 5 | 1 | 0.320 | 346,253 | 1,740 |
| sqlitevec | 10 | 1 | 0.343 | 398,751 | 2,004 |
| sqlitevec | 20 | 1 | 0.329 | 621,790 | 3,125 |
| sqlitevec | 40 | 1 | 0.327 | 965,423 | 4,851 |
| sqlitevec | 10 | 2 | 0.342 | 659,981 | 3,316 |

### Directory Structure

Note: `data_*` and `log_*.log` are large, machine-generated artifacts and are
ignored by git (see `.gitignore`).

```
results/
+-- .gitignore                           # Ignore data/log/pdf/tmp artifacts.
+-- README.md                            # This file.
+-- REPORT.md                            # English evaluation report.
+-- REPORT.zh_CN.md                      # Chinese evaluation report.
+-- tools/
|   +-- extract_paper_locomo_tables.py   # Extract external baselines.
+-- tmp/                                 # Paper text dumps (ignored).
+-- data_*/                              # Evaluation outputs (ignored).
+-- log_*.log                            # Run logs (ignored).
+-- *.pdf                                # Papers (ignored).
```

### External Baselines (From Papers)

To extract LoCoMo baseline tables reported by external papers and generate
Markdown snippets for `REPORT.md` and `REPORT.zh_CN.md`:

- Prepare paper text dumps under `tmp/`:
  - `tmp/2402.17753v1.txt` (LoCoMo paper).
  - `tmp/2504.19413.txt` (Mem0 paper).
- Run:
  - `python3 tools/extract_paper_locomo_tables.py --format md`

The script parses the tables and converts percentage-point metrics to the
0-1 range for consistent reporting.

### Result Format

Each `results.json` contains:

```json
{
  "metadata": {
    "framework": "trpc-agent-go",
    "model": "gpt-4o-mini",
    "scenario": "agentic",
    "memory_backend": "pgvector"
  },
  "summary": {
    "total_samples": 10,
    "total_questions": 1986,
    "overall_f1": 0.294,
    "overall_bleu": 0.279,
    "overall_llm_score": 0.287
  },
  "by_category": {
    "single-hop": {"count": 282, "f1": 0.146},
    "multi-hop": {"count": 321, "f1": 0.178},
    "temporal": {"count": 96, "f1": 0.091},
    "open-domain": {"count": 841, "f1": 0.126},
    "adversarial": {"count": 446, "f1": 0.830}
  }
}
```

## LongMemEval

LongMemEval is the cross-session user-memory benchmark. The harness replays
each case through public `Runner.Run` and answers from memory only. The
comparison below covers the Auto `Merge Similar`, `Preserve History`, and
`Append Only` update policies plus Mem0 OSS.

The evaluation uses a fixed 50-case subset of `longmemeval_s_cleaned.json`,
which holds 500 cases, sampled at 10% from each of the six question types.
This is a historical fixed subset. Appendix D of the reports identifies its
cases for reference, but the list predates the current versioned,
digest-bound manifest contract and cannot be replayed directly by the current
harness. It also defines no seeded blind split, so the results support
relative comparison between configurations and are neither final scores on
the benchmark nor a blind holdout baseline.

Comparable runs share the dataset, case list, replay, build plan, model
configuration, tokenizer, turn-pair protocol, and retrieval limit:

- The harness builds memory once per user/assistant pair through public
  `Runner.Run`. An over-limit pair is split without content loss, but each
  fragment is a separate Runner call and extraction boundary, and the affected
  case IDs are recorded in provenance. One Runner is reused per case; pairs
  from one source session share its original session ID, while other source
  sessions use isolated histories under the same user-level memory.
- Builder input is validated and timestamped, and `top-k=20` applies to every
  memory backend.
- Auto receives the session date through its extractor reference-date
  capability; Mem0 receives the same date as extraction custom instructions
  through the official `POST /memories` `prompt` field. The date comes from
  the build-plan session observation time and is not inserted into message
  content.
- Cases that produce no answer stay in the fixed denominator of 50 and score
  as incorrect.

### LongMemEval Policy and Assistant Matrix

All three Auto update policies with assistant-episode extraction disabled and
enabled, plus native Mem0 OSS. Every row uses the same 50 cases, `glm52`,
`text-embedding-ada-002`, the same ordered turn-pair fragments, and
`top-k=20`. The assistant-enabled rows are historical references because their
extraction implementation predates the merged version.

| Policy / backend | Assistant extraction | QA succeeded | Correct | Accuracy | F1 | BLEU | ROUGE-L | Complete-build memories |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | Disabled | 48/50 | 14 | 0.2800 | 0.0961 | 0.0635 | 0.0885 | 2,955 / 50 cases |
| Merge Similar | Enabled | 45/50 | 29 | 0.5800 | 0.1626 | 0.1083 | 0.1518 | 6,011 / 50 cases |
| Preserve History | Disabled | 50/50 | 41 | 0.8200 | 0.1683 | 0.1079 | 0.1614 | 15,353 / 50 cases |
| Preserve History | Enabled | 48/50 | **45** | **0.9000** | 0.1876 | 0.1204 | 0.1779 | 17,696 / 49 cases |
| Append Only | Disabled | 50/50 | 41 | 0.8200 | 0.1730 | 0.1112 | 0.1679 | 16,280 / 50 cases |
| Append Only | Enabled | 47/50 | 44 | 0.8800 | **0.1970** | **0.1295** | **0.1865** | 18,728 / 49 cases |
| Mem0 OSS | Native | 50/50 | 42 | 0.8400 | 0.1681 | 0.1067 | 0.1588 | 28,041 / 50 cases |

Accuracy always uses the fixed denominator of 50. Memory totals include only
the last successful persistence snapshot for each completely built case;
partial rows are excluded. The assistant-enabled rows use an earlier two-stage
implementation and do not reproduce the current head; two of them have one
case each whose memory build did not complete. Per-question-type results,
memory footprint, cost, and validity limits are in the LongMemEval part of
[REPORT.md](REPORT.md).
