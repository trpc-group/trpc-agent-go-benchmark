# Memory Benchmarks

## Common Environment

Load model, embedding, and storage settings before running benchmarks:

```bash
cd benchmark/memory
cp .env.example .env
# Fill in local values. The .env file is ignored by git.
set -a
. ./.env
set +a
```

The current environment convention is:

- LongMemEval LLM: `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_NAME`.
- LoCoMo LLM: the standard OpenAI model environment, including
  `OPENAI_BASE_URL` and `OPENAI_API_KEY`.
- Embedding: `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `MODEL_NAME`.
- Vector storage: `PGVECTOR_DSN`.

Some Go builds compile sqlite-vec even when a run uses pgvector. If system
SQLite headers do not define `SQLITE_INNOCUOUS`, set:

```bash
export CGO_ENABLED=1
export CGO_CFLAGS='-DSQLITE_INNOCUOUS=0x000200000'
export GOCACHE=/tmp/go-build-cache
```

## Memory Evaluation Benchmark

This benchmark evaluates the long-term conversational memory capabilities of
trpc-agent-go using the LoCoMo dataset.

### Overview

Based on:

- [LoCoMo: Long-Context Conversational Memory](https://arxiv.org/abs/2402.17753)
- [Memory in the Age of AI Agents](https://arxiv.org/abs/2512.13564)

### Reports

| File | Description |
|------|-------------|
| [REPORT.md](results/REPORT.md) | Full evaluation report (English) |
| [REPORT.zh_CN.md](results/REPORT.zh_CN.md) | Full evaluation report (Chinese) |

### Key Results

**Configuration**: Model=gpt-4o-mini, 10 samples, 1,986 QA pairs.

**Overall Results (No History Injection)**:

| Scenario | Backend | F1 | LLM Score |
|----------|---------|----:|----------:|
| Long-Context | - | **0.472** | **0.523** |
| Auto | pgvector | 0.357 | 0.366 |
| Auto | MySQL | 0.347 | 0.362 |
| Agentic | pgvector | 0.294 | 0.287 |
| Agentic | MySQL | 0.286 | 0.285 |

**History Injection Effect (Auto pgvector)**:

| History | F1 | LLM Score | Adversarial F1 | Open-domain LLM |
|---------|----:|----------:|---------------:|----------------:|
| None | **0.357** | 0.366 | **0.771** | 0.355 |
| +300 turns | 0.296 | 0.414 | 0.514 | 0.539 |
| +700 turns | 0.288 | **0.464** | 0.418 | **0.685** |

**Key Insights**:
1. Auto extraction with pgvector achieves the best memory-based F1 (75.6%
   of long-context baseline).
2. History injection improves semantic quality (LLM Score +0.10~0.18) but
   hurts token-level precision (F1 -0.02~0.07) due to adversarial
   robustness degradation.
3. Structured memory extraction outperforms brute-force history injection
   for factual recall tasks.
4. pgvector > MySQL for retrieval quality; gap vanishes with history
   injection.

### SQLite vs SQLiteVec (Subset)

This is a set of subset runs to compare local SQLite keyword matching
(`sqlite`) vs sqlite-vec semantic search (`sqlitevec`).

**Subset run A (end-to-end QA)**:

- Model: gpt-4o-mini
- Scenario: auto
- Sample: locomo10_1 (199 QA, all categories)
- LLM Judge: enabled

| Backend | #QA | F1 | LLM Score | Prompt Tokens | Avg Prompt/QA | Avg Latency |
|---------|---:|---:|----------:|--------------:|--------------:|------------:|
| sqlite | 199 | 0.327 | 0.370 | 1,287,813 | 6,471 | 5,805ms |
| sqlitevec | 199 | 0.307 | 0.325 | 407,969 | 2,050 | 6,327ms |

Note: `Prompt Tokens` and `Avg Prompt/QA` count only QA agent calls.
They exclude embedding requests and LLM-as-Judge calls.

We also rerun the same configuration on `locomo10_6` (158 QA):

| Backend | #QA | F1 | Prompt Tokens | Avg Prompt/QA |
|---------|---:|---:|--------------:|--------------:|
| sqlite | 158 | 0.269 | 1,296,580 | 8,206 |
| sqlitevec | 158 | 0.274 | 362,903 | 2,297 |

**Subset run B (temporal token-cost micro-run)**:

- Model: gpt-4o-mini
- Scenario: auto
- Sample: locomo10_1
- Category filter: temporal (13 QA)
- LLM Judge: disabled

| Backend | F1 | Prompt Tokens | Avg Prompt/QA |
|---------|---:|--------------:|--------------:|
| sqlite | 0.116 | 80,184 | 6,168 |
| sqlitevec | 0.116 | 26,483 | 2,037 |

**Subset run C (top-k sweep + multi-search ablation)**:

To study whether "retrieving more memories" (higher top-k) or "searching more
times" (multiple `memory_search` calls) improves answer quality, we run a small
sweep on `locomo10_1` (LLM Judge disabled; F1/BLEU only).

| Backend | vector-topk | qa-search-passes | F1 | Prompt Tokens | Avg Prompt/QA |
|---------|------------:|-----------------:|---:|--------------:|--------------:|
| sqlite | - | 1 | 0.299 | 1,322,360 | 6,645 |
| sqlitevec | 5 | 1 | 0.320 | 346,253 | 1,740 |
| sqlitevec | 10 | 1 | 0.343 | 398,751 | 2,004 |
| sqlitevec | 20 | 1 | 0.329 | 621,790 | 3,125 |
| sqlitevec | 40 | 1 | 0.327 | 965,423 | 4,851 |
| sqlitevec | 10 | 2 | 0.342 | 659,981 | 3,316 |

Takeaway: top-k does not monotonically improve quality in this setup; higher
top-k increases tokens and can slightly reduce F1. See `results/REPORT.md` for
details.

### Evaluation Metrics

Aligned with LoCoMo paper and industry standards (Mem0, MemMachine):

| Metric     | Description                      |
| ---------- | -------------------------------- |
| F1 Score   | Token-level F1 (LoCoMo standard) |
| BLEU Score | N-gram overlap                   |
| LLM-score  | LLM-as-Judge evaluation          |

### QA Categories

| Category    | Description                                        |
| ----------- | -------------------------------------------------- |
| single-hop  | Single-hop questions from one conversation segment |
| multi-hop   | Multi-hop questions requiring multiple segments    |
| temporal    | Temporal reasoning questions                       |
| open-domain | Open-domain questions requiring world knowledge    |
| adversarial | Adversarial questions testing robustness           |

### Evaluation Scenarios

#### 1. Long-Context (Baseline)

Full conversation as context, evaluates model's native long-context ability.

```bash
go run . -scenario long_context
```

#### 2. Agentic (Memory Tools)

Agent uses memory tools to add and search memories. The agent processes each
conversation session separately and decides what to store.

```bash
go run . -scenario agentic
```

#### 3. Auto (Memory Extractor + Search)

Auto mode uses the built-in memory extractor to generate memories in the
background. The QA stage only performs memory search.

```bash
go run . -scenario auto
```

Memory backends apply to `agentic` and `auto` scenarios.
Auto mode uses the built-in extractor provided by the memory service.

#### 4. All Scenarios

Run all scenarios for comparison.

```bash
go run . -scenario all

# Run all scenarios on both backends.
go run . -scenario all -memory-backend inmemory,pgvector
```

#### 5. Comma-Separated Scenarios

Run specific combinations of scenarios.

```bash
# Run agentic and auto only.
go run . -scenario agentic,auto -memory-backend pgvector,mysql
```

### Command-Line Options

| Option              | Default                | Description                            |
| ------------------- | ---------------------- | -------------------------------------- |
| `-model`            | gpt-4o-mini            | Model name                             |
| `-eval-model`       | same as model          | Evaluation model for LLM judge         |
| `-dataset`          | ../data                | Dataset directory                      |
| `-data-file`        | locomo10.json          | Dataset file name                      |
| `-output`           | ../results             | Output directory                       |
| `-scenario`         | long_context           | Evaluation scenario (comma-separated)  |
| `-memory-backend`   | inmemory               | Memory backend (comma-separated)       |
| `-pgvector-dsn`     | (env)                  | PostgreSQL DSN for pgvector            |
| `-mysql-dsn`        | (env)                  | MySQL DSN for mysql backend            |
| `-embed-model`      | text-embedding-3-small | Embedding model for vector backends    |
| `-vector-topk`      | 10                     | Top-k results for vector backends      |
| `-qa-history-turns` | 0                      | Inject N conversation turns as context |
| `-qa-search-passes` | 1                      | memory_search calls per QA             |
| `-sample-id`        |                        | Filter by sample ID                    |
| `-max-tasks`        | 0                      | Maximum tasks (0=all)                  |
| `-llm-judge`        | false                  | Enable LLM-as-Judge                    |
| `-verbose`          | false                  | Verbose output                         |
| `-resume`           | false                  | Resume from checkpoint                 |

### Environment Variables

| Variable                    | Description                               |
| --------------------------- | ----------------------------------------- |
| `MODEL_NAME`                | Default model name                        |
| `EVAL_MODEL_NAME`           | Evaluation model name                     |
| `OPENAI_API_KEY`            | OpenAI API key                            |
| `PGVECTOR_DSN`              | PostgreSQL DSN for pgvector backend       |
| `MYSQL_DSN`                 | MySQL DSN for mysql backend               |
| `SQLITE_DSN`                | SQLite DSN for sqlite backend (optional)  |
| `SQLITEVEC_DSN`             | SQLite DSN for sqlitevec backend (optional) |
| `EMBED_MODEL_NAME`          | Embedding model for vector backends       |
| `OPENAI_EMBEDDING_API_KEY`  | API key for embedding model (optional)    |
| `OPENAI_EMBEDDING_BASE_URL` | Base URL for embedding API (optional)     |

### Dataset Setup

1. Download the LoCoMo dataset:

```bash
git clone https://github.com/snap-research/locomo.git
cp locomo/data/locomo10/*.json ../data/
```

2. Or use the sample data for testing:

```bash
# Sample data should be in ../data/locomo_sample.json.
```

### Running the Benchmark

```bash
cd benchmark/memory/trpc-agent-go-impl

# Install dependencies.
go mod tidy

# Run with default settings (long_context + inmemory).
go run .

# Run with LLM judge enabled.
go run . -llm-judge -model gpt-4o

# Run agentic evaluation with pgvector backend.
export PGVECTOR_DSN="postgres://user:password@localhost:5432/memory_eval\
?sslmode=disable"
go run . -scenario agentic -memory-backend pgvector

# Run auto evaluation with sqlite backend.
go run . -scenario auto -memory-backend sqlite

# Run auto evaluation with sqlitevec backend (requires embeddings).
go run . -scenario auto -memory-backend sqlitevec

# Run auto evaluation with sqlite backend.
go run main.go -scenario auto -memory-backend sqlite

# Run auto evaluation with sqlitevec backend (requires embeddings).
go run main.go -scenario auto -memory-backend sqlitevec

# Run all scenarios.
go run . -scenario all -output ../results/full_eval

# Run with history injection (300 turns).
go run . \
  -scenario agentic,auto \
  -memory-backend pgvector,mysql \
  -qa-history-turns 300 \
  -llm-judge \
  -output ../results/history300
```

### Output Format

Results are saved in JSON format:

```json
{
  "metadata": {
    "framework": "trpc-agent-go",
    "model": "gpt-4o-mini",
    "scenario": "agentic",
    "memory_backend": "pgvector"
  },
  "summary": {
    "total_questions": 200,
    "overall_f1": 0.412,
    "overall_bleu": 0.156
  },
  "by_category": {
    "single-hop": { "count": 60, "f1": 0.523, "bleu": 0.182 },
    "multi-hop": { "count": 50, "f1": 0.384, "bleu": 0.145 }
  }
}
```

### Comparison with Baselines

| System             | F1   | LLM-score |
| ------------------ | ---- | --------- |
| GPT-4 (4K context) | 32.1 | -         |
| GPT-3.5-16K        | 37.8 | -         |
| Mem0               | -    | 0.80      |
| MemMachine         | 91.2 | 0.91      |

### Memory Backend Comparison

| Backend  | Pros                                | Cons                                         |
| -------- | ----------------------------------- | -------------------------------------------- |
| inmemory | Fast, no setup required             | No vector similarity, keyword-based matching |
| pgvector | Vector similarity search, scalable  | Requires PostgreSQL setup                    |
| mysql    | App-layer BM25-style keyword search | Requires MySQL setup                         |

#### Expected Results

- **pgvector** should outperform **inmemory** for semantic retrieval tasks.
- For exact-match questions, both backends may perform similarly.
- pgvector is recommended for production and realistic evaluation.
- With history injection, backend differences diminish.

## LongMemEval Memory Benchmark

### Overview

LongMemEval uses a three-stage benchmark:

1. Build or reuse a fixed manifest and Runner replay artifact.
2. Derive one immutable `turn-pair-fragment` build plan from that replay.
3. Run each memory implementation from the same build plan and answer with
   memory-only QA.

The QA agent may see the current question, question date, and retrieved
memories. It must not see the raw LongMemEval session transcript.

### Preparation

Before starting a run:

1. Download the cleaned dataset to
   `benchmark/memory/data/longmemeval-cleaned/longmemeval_s_cleaned.json` as
   described in [`data/README.md`](data/README.md).
2. Load the LLM, embedding, and storage variables described in
   [Common Environment](#common-environment). LLM and embedding endpoints are
   configured separately.
3. Start pgvector for `auto`. Use a new
   `-table-suffix` when the embedding model or experiment changes so a run does
   not read an incompatible table.
4. Choose one fixed manifest and one replay root for every implementation in
   the comparison. This keeps case IDs, order, and replay input identical.
5. Use Python 3.11 or newer to render the maintained report.
6. For Mem0 OSS, use the digest-pinned environment in
   [`mem0-oss`](mem0-oss/README.md). The benchmark does not accept an arbitrary
   local Mem0 checkout as a reproducible baseline. Run its fail-closed
   preflight before starting the benchmark. The locked image includes
   `mem0ai[nlp]` and the checksum-pinned `en_core_web_sm` model so native V3
   lemmatized BM25 and entity-link signals are active instead of silently
   degrading to semantic-only retrieval.

Runner replay stores sanitized user/assistant turns. The build plan preserves
their order and content, groups them into chronological user/assistant pairs,
and uses the configured tokenizer to split only a pair that exceeds the
provider context limit. Splitting never drops or truncates content, but each
fragment is a separate Runner call and extraction boundary, so it is not
semantically identical to one atomic pair. Future results record affected case
IDs in `fragmented_case_ids`. Sessions that begin with an assistant message
preserve that message as a singleton pair; the benchmark does not invent an
empty user turn. The protocol is fixed and has no command-line mode switch.

Manifest, replay, build-plan, run-manifest, result, and trace schemas are
versioned independently from `v1`. Changing dataset size, case quotas, or the
number of question types does not require a schema change. A version is bumped
only when an artifact's structure or semantics changes, and readers reject
unsupported versions instead of guessing compatibility.

Auto and Mem0 OSS consume the same immutable build-plan artifacts and confirm
each fragment before advancing. One Runner is reused for the complete case. Every
source session keeps its original session ID, all pairs in that source session
run sequentially through the same Runner session, and a different source
session starts isolated session history while retaining the same user-level
memory. QA starts only after memory build has completed.

Session dates are not prepended to message content. Instead, replay invokes the
public `Runner.Run` path for each pair, validates the stored messages, and
assigns deterministic event timestamps before the memory builder runs. Auto
receives the original session date through the public extractor reference-date
capability. Mem0 receives the same UTC date through the official
`POST /memories` `prompt` field. The locked Mem0 OSS implementation renders
this field as extraction custom instructions while retaining its native base
extraction protocol. The instruction identifies the immutable build-plan
session observation date as authoritative and prevents the server wall clock
from changing relative-date interpretation. Missing or malformed observation
time fails ingestion before any request or watermark update. Maintained result
validation rejects the former metadata-only Mem0 temporal context.

All compared memory backends receive the same explicit `memory_search` limit
of 20. The common QA service passes that limit to each backend and caps the
returned entries defensively before they are exposed to the answer model.
Mem0 additionally retains its native `0.1` semantic candidate threshold before
hybrid scoring. Its maintained preflight uses `explain=true` to prove that both
BM25 and entity boosts contribute to a live search. Explain data is diagnostic
only and is not exposed to the QA model.

Every result records corpus-wide build statistics using that shared plan:
sessions, turns, pairs, chunks, sessions and pairs affected by chunking, split
turns, original/final bytes and tokens, and maximum session, turn, pair, and
chunk token counts. The maintained report renders these fields for every
backend so input transformations remain auditable.

Failure-stage diagnostics are intentionally heuristic. Gold recall is measured
at the answer-session ID level, not by matching exact gold evidence spans. Auto
can expose extraction operations, while Mem0 OSS can expose only before/after
memory snapshots through its public API. Reports label this method as
`heuristic_session_provenance`; it must not be interpreted as definitive proof
that a particular fact was extracted or reconciled correctly.

The benchmark module pins all trpc-agent-go modules to the same reachable
upstream commit. That commit contains the update-policy, assistant-episode,
and Mem0 ingestion APIs exercised here. Local filesystem and contributor-fork
replacements are not accepted for maintained runs.

### Evaluation Scenarios

| Scenario | Role | Memory and QA path |
| --- | --- | --- |
| `auto` | Primary | Runner replay -> native auto memory worker -> standard `memory_search` |
| `mem0_oss` | Primary | Runner replay -> `session.Ingestor` -> Mem0 OSS -> standard `memory_search` |
| `replay` | Preparation | Writes the shared sanitized Runner replay and deterministic build-plan artifacts |
| `long_context` | Reference | Answers from the full context without a memory store |

Auto runs can select one extractor update policy while keeping the build plan,
retrieval limit, QA prompt, and judge unchanged:

- `merge_similar` preserves the existing Auto behavior and remains the default.
- `preserve_history` updates only safe, non-conflicting enrichments of the same fact or
  event; corrections, state changes, different events, and uncertain matches
  are added as new memories.
- `append_only` exposes only `memory_add` to background extraction and skips exact
  duplicates.

Policy comparisons must use separate pgvector tables and output directories.
The selected policy is stored in result metadata and the immutable run
manifest, so a checkpoint cannot be resumed under a different policy.

### Command-Line Options

The following options are relevant to the Go LongMemEval harness:

| Option | Default | Description |
| --- | --- | --- |
| `-dataset-format` | `locomo` | Must be `longmemeval` for this benchmark |
| `-dataset` | `../data` | Dataset directory, or a full `.json`/`.jsonl` path |
| `-data-file` | `longmemeval_s_cleaned.json` for LongMemEval | File used when `-dataset` is a directory |
| `-model` | `LLM_NAME`, `MODEL_NAME`, or `gpt-4o-mini` | Model used for extraction, QA, and judge |
| `-scenario` | `long_context` | `long_context`, `replay`, `auto`, or `mem0_oss`; comma-separated values are accepted |
| `-output` | `../results` | Output root; scenarios are written below `<output>/longmemeval` |
| `-memory-backend` | `inmemory` | Auto currently requires `pgvector` |
| `-pgvector-dsn` | `PGVECTOR_DSN` | PostgreSQL DSN for pgvector |
| `-embed-model` | `EMBED_MODEL_NAME`, `MODEL_NAME`, or `text-embedding-3-small` | Embedding model for vector memory |
| `-vector-topk` | `30` | Maximum memories returned by non-LongMemEval vector retrieval |
| `-table-suffix` | empty | Suffix appended to benchmark database table names |
| `-max-tasks` | `0` | Without a manifest, limits diagnostic cases. With a manifest, it must be `0` or exactly the manifest size; formal runs cannot truncate a manifest |
| `-resume` | `false` | Continue from the scenario checkpoint; requires a clean benchmark worktree |
| `-lme-question-types` | empty | Comma-separated LongMemEval question types; empty means all manifest cases |
| `-lme-manifest` | empty | Fixed case-ID manifest path |
| `-lme-replay-root` | `<output>/longmemeval/replay` | Shared Runner replay artifact root |
| `-lme-build-max-tokens` | `7500` | Maximum embedding-model tokens in one build fragment, leaving headroom below the common 8192-token limit; an oversized pair creates multiple extraction boundaries |
| `-lme-build-tokenizer-model` | `-embed-model` | Tokenizer model recorded in the immutable build plan |
| `-lme-build-tokenizer-encoding` | `cl100k_base` for known OpenAI embedding models | Explicit tiktoken encoding for compatible model aliases |
| `-lme-auto-qa-only` | `false` | Skip Auto memory build and rerun QA against an existing pgvector table |
| `-lme-auto-memory-table` | derived from table suffix | Explicit Auto pgvector table used by QA-only runs |
| `-lme-auto-update-policy` | `merge_similar` | Auto extractor policy: `merge_similar`, `preserve_history`, or `append_only` |
| `-lme-conversation-extraction` | `disabled` | Conversation extraction mode: `disabled` or `assistant-episode` |
| `-lme-max-retries` | `3` | Transport retry count |
| `-lme-answer-max-tokens` | `500` | Maximum answer tokens |
| `-lme-judge-max-tokens` | `10240` | Maximum judge tokens; the parsed final answer must still be exact `yes` or `no` |
| `-lme-extraction-wait` | `10m` | Timeout while waiting for Auto memory extraction |
| `-lme-trace-content` | `hash` | Build-trace content mode: `full`, `hash`, or `none` |
| `-lme-trace-gzip` | `false` | Compress build-trace attempt files with gzip |
| `-lme-embedding-cache` | `true` | Enable the persistent LongMemEval embedding cache |
| `-lme-embedding-cache-dir` | `<output>/longmemeval/.cache` | Override the embedding cache directory |
| `-mem0-host` | `MEM0_HOST` | Mem0 OSS service URL |
| `-mem0-api-key` | `MEM0_API_KEY` | Mem0 OSS API key; may be empty with `AUTH_DISABLED=true` |
| `-mem0-version` | `MEM0_VERSION` | Optional expected Mem0 version; the verified preflight value is authoritative |
| `-mem0-revision` | `MEM0_REVISION` | Optional expected Mem0 source commit; the verified preflight value is authoritative |
| `-mem0-preflight` | `MEM0_PREFLIGHT` | Sanitized successful `preflight.json`; required for maintained Mem0 runs |
| `-mem0-ingest-timeout` | `-lme-extraction-wait` | Timeout for synchronous Mem0 ingestion |
| `-mem0-proxy-usage-log` | empty | Split proxy JSONL log used for Mem0 internal usage accounting |
| `-mem0-proxy-run-id` | `MEM0_PROXY_RUN_ID` | Unique ID shared with the split proxy; required when usage logging is enabled |

The Go LongMemEval harness uses `-model` for both generation and judging;
`-eval-model` applies to the LoCoMo path. Agno has its own `--eval-model`
option.

### Environment Variables

| Variable | Used by | Description |
| --- | --- | --- |
| `LLM_NAME` | Go LongMemEval, Agno, split proxy | Primary LLM name |
| `LLM_BASE_URL` | Go LongMemEval, Agno, split proxy | OpenAI-compatible LLM endpoint |
| `LLM_API_KEY` | Go LongMemEval, Agno, split proxy | LLM API key |
| `EVAL_MODEL_NAME` | Agno | Optional separate Agno judge model |
| `EMBED_MODEL_NAME` | Go | Preferred embedding model override |
| `MODEL_NAME` | Go, Agno | Fallback model name; also the fallback Go embedding model |
| `OPENAI_EMBEDDING_BASE_URL` | Go | Preferred embedding endpoint override |
| `OPENAI_EMBEDDING_API_KEY` | Go | Preferred embedding API key override |
| `OPENAI_BASE_URL` | Go, split proxy | LoCoMo LLM endpoint and embedding fallback endpoint |
| `OPENAI_API_KEY` | Go, Agno, split proxy | LoCoMo LLM key, split-proxy embedding key, and Go/Agno fallback key |
| `PGVECTOR_DSN` | Go | pgvector connection string for Auto |
| `MEM0_HOST` | Go | Local Mem0 OSS service URL, for example `http://localhost:8888` |
| `MEM0_API_KEY` | Go | Mem0 OSS key; optional when local authentication is disabled |
| `MEM0_PREFLIGHT` | Go | Path to the sanitized successful Mem0 OSS preflight JSON |

### Running the Benchmark

The commands below use one 70-case single-session-user manifest and one shared
output root. Run them in order. Add `-resume` to a Go scenario command to
continue its existing checkpoint without overwriting completed cases. Resume
is rejected when either the stored or current benchmark worktree is dirty, so
commit or otherwise fix the exact implementation before starting a resumable
run.

#### 1. Prepare Go dependencies

```bash
cd benchmark/memory/trpc-agent-go-impl
go mod download
```

#### 2. Generate the fixed manifest

Use `full-category` when the run covers an entire LongMemEval category. It
selects every matching case and does not depend on source dataset order.

```bash
go run ./cmd/longmemeval-manifest \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -method full-category \
  -types single-session-user \
  -output ../results/lme_ssu_v2/full_70/longmemeval/manifests/single_session_user_70.json
```

New partial manifests use seeded SHA-256 ranking within each question type.
The seed is mandatory, selection is independent of dataset order, and a total
size is distributed in proportion to available cases with the largest
remainder method and lexical tie-breaking. A single generated subset is useful
for local diagnostics; use the split command below for a maintained dev or
holdout result:

```bash
go run ./cmd/longmemeval-manifest \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -method stratified-sha256 \
  -seed lme-dev-2026-07 \
  -total-size 50 \
  -output ../results/lme/manifests/dev_50.json
```

Use `-quotas` instead of `-total-size` to set every question-type quota
explicitly:

```bash
go run ./cmd/longmemeval-manifest \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -method stratified-sha256 \
  -seed lme-dev-2026-07 \
  -types multi-session,temporal-reasoning \
  -quotas multi-session=20,temporal-reasoning=10 \
  -output ../results/lme/manifests/dev_30.json
```

Generate a disjoint development/holdout pair in one operation. Development is
selected first by seeded rank; holdout is selected from the remaining cases.
Each side may use a total size or complete per-type quotas.

```bash
go run ./cmd/longmemeval-manifest \
  -action split \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -seed lme-split-2026-07 \
  -dev-size 50 \
  -holdout-size 50 \
  -dev-output ../results/lme/manifests/dev_50.json \
  -holdout-output ../results/lme/manifests/holdout_50.json
```

Verify one manifest, or verify a pair to additionally reject overlap and an
incorrect holdout offset:

```bash
go run ./cmd/longmemeval-manifest \
  -action verify \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -manifest ../results/lme/manifests/dev_50.json \
  -holdout-manifest ../results/lme/manifests/holdout_50.json
```

Manifests record the ordered `case_ids`, selection method, seed, resolved
quotas, case types, semantic digest of the declared question-type candidate
pool, and self-excluding manifest digest. Case-ID-only manifests are rejected
because they cannot establish dataset or selection provenance. Sampled
manifests without an explicit `dev` or `holdout` split must not be described as
blind holdouts. Smoke tests must use a separate 2-case manifest with its own digest;
`-max-tasks 2` cannot be used to truncate a maintained 50- or 70-case
manifest.

#### 3. Generate the shared Runner replay

```bash
go run . \
  -dataset-format longmemeval \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -scenario replay \
  -lme-manifest ../results/lme_ssu_v2/full_70/longmemeval/manifests/single_session_user_70.json \
  -lme-replay-root ../results/lme_ssu_v2/full_70/longmemeval/replay \
  -output ../results/lme_ssu_v2/full_70
```

#### 4. Run trpc-agent-go Auto

```bash
go run . \
  -dataset-format longmemeval \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -scenario auto \
  -lme-manifest ../results/lme_ssu_v2/full_70/longmemeval/manifests/single_session_user_70.json \
  -lme-replay-root ../results/lme_ssu_v2/full_70/longmemeval/replay \
  -memory-backend pgvector \
  -table-suffix _lme_ssu_v2_full \
  -output ../results/lme_ssu_v2/full_70
```

To rerun only QA against the existing Auto memory table:

```bash
go run . \
  -dataset-format longmemeval \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -scenario auto \
  -lme-auto-qa-only \
  -lme-auto-memory-table memory_eval_auto_lme_ssu_v2_full \
  -lme-manifest ../results/lme_ssu_v2/full_70/longmemeval/manifests/single_session_user_70.json \
  -lme-replay-root ../results/lme_ssu_v2/full_70/longmemeval/replay \
  -memory-backend pgvector \
  -output ../results/lme_ssu_v2/full_70_rejudge
```

#### 5. Run Mem0 OSS

In a separate terminal, start one authenticated split proxy for this benchmark
run. The run ID scopes usage records so concurrent or previous runs cannot be
charged to this result. Binding outside loopback is required for the Docker
container and therefore must be explicitly acknowledged:

```bash
cd benchmark/memory

export MEM0_PROXY_RUN_ID="lme-$(date -u +%Y%m%dT%H%M%SZ)-$$"
export MEM0_PROXY_API_KEY="$(
  set -a
  . mem0-oss/.env
  printf '%s' "$MEM0_PROXY_API_KEY"
)"

.venv-lme/bin/python adapter/openai_split_proxy.py \
  --host 0.0.0.0 \
  --allow-non-loopback \
  --port 18080 \
  --run-id "$MEM0_PROXY_RUN_ID" \
  --usage-log "results/lme_ssu_v2/full_70/mem0_proxy_usage_${MEM0_PROXY_RUN_ID}.jsonl"
```

Build and start the locked Mem0 environment, then run its mandatory preflight:

```bash
cd mem0-oss
cp .env.example .env
# Set POSTGRES_PASSWORD and confirm LLM_NAME and MODEL_NAME in .env.
docker compose --env-file .env -f compose.yaml up --build --detach --wait

set -a
. ./.env
set +a
python3 preflight.py --output preflight.json
```

The full setup, lock provenance, and update procedure are documented in
[`mem0-oss/README.md`](mem0-oss/README.md). Do not continue when preflight
fails. From `benchmark/memory/trpc-agent-go-impl`, run:

```bash
MEM0_HOST=http://localhost:8888 go run . \
  -dataset-format longmemeval \
  -dataset ../data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  -scenario mem0_oss \
  -lme-manifest ../results/lme_ssu_v2/full_70/longmemeval/manifests/single_session_user_70.json \
  -lme-replay-root ../results/lme_ssu_v2/full_70/longmemeval/replay \
  -mem0-preflight ../mem0-oss/preflight.json \
  -mem0-proxy-usage-log \
    "../results/lme_ssu_v2/full_70/mem0_proxy_usage_${MEM0_PROXY_RUN_ID}.jsonl" \
  -mem0-proxy-run-id "$MEM0_PROXY_RUN_ID" \
  -output ../results/lme_ssu_v2/full_70
```

Use a fresh run ID and usage-log path for every run, and do not share one proxy
process between concurrent benchmark runs. Omit `-mem0-proxy-usage-log` if the
proxy is not recording usage. In that
case, missing Mem0 internal token usage remains explicitly unknown rather than
being inferred.

#### 6. Render reports

```bash
cd benchmark/memory
python3 adapter/longmemeval_report.py \
  --root results/lme_ssu_v2/full_70/longmemeval
```

The main reports compare Auto Merge Similar, Preserve History, and Append Only
policies with Mem0 OSS. They retain the fixed 50-case development snapshot;
assistant-enabled rows are historical references because assistant extraction
changed after those runs. See [`results/REPORT.md`](results/REPORT.md) and
[`results/REPORT.zh_CN.md`](results/REPORT.zh_CN.md).

## Verification

Run the Go checks with workspace discovery disabled so a parent
`trpc-agent-go` checkout cannot satisfy missing or stale module dependencies:

```bash
cd benchmark/memory/trpc-agent-go-impl
GOWORK=off GOFLAGS='-mod=readonly' go mod verify
GOWORK=off GOFLAGS='-mod=readonly' CGO_ENABLED=1 \
  CGO_CFLAGS='-DSQLITE_INNOCUOUS=0x000200000' go test ./...

cd ../../..
python3 -m py_compile \
  benchmark/memory/adapter/longmemeval_report.py \
  benchmark/memory/adapter/openai_split_proxy.py
```
