# Memory Benchmark Report

This report follows the order in which the experiments were run: first
the long-term conversational memory evaluation on LoCoMo, together
with the update policy comparison added later under the same protocol,
then the cross-session user memory evaluation on LongMemEval. Each
part states its own setup, results, and analysis.

| Experiment | Benchmark | Subject | Scale | Model |
| --- | --- | --- | --- | --- |
| 1 | LoCoMo-10 | Internal scenarios, Python agent frameworks, external memory systems | 1,986 QA | `gpt-4o-mini` |
| 2 | LoCoMo-10 | Three Auto update policies | 1,986 QA | `gpt-4o-mini` |
| 3 | LongMemEval | Three update policies × assistant-episode extraction, plus Mem0 OSS | 50 cases | `glm52` |

**Result status**

- The LoCoMo part records the runs as they were executed. The
  repository keeps only the report text; datasets, logs, and traces
  are not published with it, so these numbers should be cited as
  historical run records. Re-running the documented setup should
  reproduce the same conclusions.
- The LongMemEval section is a development-stage evaluation on a
  50-case subset; its composition and full listing are given in
  Section 1 of that part and in Appendix D. It supports relative
  comparison between configurations and is not a final score on that
  benchmark.

---

## Evaluating Long-Term Conversational Memory on LoCoMo Benchmark

### 1. Introduction

This report evaluates the long-term conversational memory of
**trpc-agent-go** using the **LoCoMo** benchmark (Maharana et al.,
2024). It covers two versions:

- **trpc-agent-go (original)**: Baseline version (Auto extraction + pgvector)
- **trpc-agent-go (optimized)**: After multiple rounds of optimization
  including contextualized memory extraction, episodic memory
  classification, hybrid search, and multi-pass retrieval
  (see Section 2.3 for details)

Both versions are compared against four Python agent frameworks
(AutoGen, Agno, ADK, CrewAI) and ten external memory systems
(Mem0, Zep, etc.).

### 2. Experimental Setup

#### 2.1 Benchmark

| Item | Value |
| --- | --- |
| Dataset | LoCoMo-10 (10 conversations, 1,986 QA) |
| Categories | single-hop (282), multi-hop (321), temporal (96), open-domain (841), adversarial (446) |
| Model | GPT-4o-mini (inference + judge) |
| Embedding | text-embedding-3-small |

#### 2.2 Scenarios

| Scenario | Description |
| --- | --- |
| **Long-Context** | Full transcript as LLM context (upper bound) |
| **Original** | Auto extraction + pgvector baseline; background extractor writes memories and retrieves them at query time |
| **Optimized** | Optimized memory extraction strategy and multi-pass retrieval over extracted memories |

#### 2.3 Optimizations: Original → Optimized

The optimized version builds on the original baseline with a series
of targeted improvements across the memory extraction, storage, and
retrieval pipeline:

1. **Contextualized Memory Extraction** — The original extractor
   produces flat, unstructured memory strings. The optimized version
   uses a comprehensive extraction prompt that enforces **atomicity**
   (one fact per memory), **completeness** (all speakers, all
   details), and **specificity** (exact names, dates, quantities).
   This significantly improves information density and recall.

2. **Episodic Memory Classification** — Each extracted memory is
   classified as either a **Fact** (stable attributes, preferences,
   relationships) or an **Episode** (time-anchored events with
   `event_time`, `participants`, and `location` metadata). This
   structured schema enables temporal filtering and event-time
   ordering during retrieval, which is critical for multi-hop and
   temporal questions.

3. **Absolute Date Resolution** — Relative time expressions in
   conversations ("yesterday", "last month") are resolved to
   absolute ISO 8601 dates using the session's reference date
   before being stored. This prevents temporal drift and enables
   accurate date-based queries.

4. **Topic Tagging** — Each memory is tagged with descriptive
   topics (e.g., `["hiking", "Mt. Fuji", "travel"]`), and the
   extractor is instructed to reuse existing topic names rather
   than inventing synonyms. Topics improve retrieval relevance
   and enable future topic-based filtering.

5. **Hybrid Search (Vector + Keyword)** — The original uses
   pure vector similarity search. The optimized version adds
   **hybrid search** that combines vector cosine similarity with
   PostgreSQL full-text search (`tsvector/tsquery`), merged via
   **Reciprocal Rank Fusion (RRF)**. This improves recall for
   queries containing specific entity names, book titles, or
   exact-match terms that vector embeddings alone may not rank
   highly.

6. **Multi-Pass Retrieval** — Instead of a single search, the
   QA agent performs **2–3 search passes** with different query
   strategies (e.g., keyword-style query, entity-focused query,
   broad name query). Each pass uses different angles to maximize
   recall before the final answer.

7. **Kind Fallback** — When a kind-filtered search (e.g.,
   episodes only) returns too few results (< 3), the system
   automatically falls back to an unfiltered search and merges
   both result sets, prioritizing the requested kind. This
   prevents missed results when kind classification is uncertain.

8. **Content Deduplication** — Near-duplicate memories (> 80%
   word-level Jaccard similarity) are deduplicated, keeping only
   the highest-scored version. This reduces redundant context
   in the retrieval results.

### 3. Results

#### 3.1 Internal Scenario Comparison

**Table 1: Overall Metrics**

| Scenario | F1 | BLEU | LLM Score | Tokens/QA | Calls/QA | Latency | Total Time |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Long-Context | 0.469 | 0.426 | 0.526 | 18,776 | 1.0 | 2,607ms | 1h26m |
| Session Recall | **0.549** | **0.511** | **0.609** | 3,694 | 1.0 | 6,430ms | 3h33m |
| Optimized | **0.469** | **0.431** | **0.532** | 17,182 | 3.0 | 8,585ms | 4h44m |
| Original | 0.399 | 0.371 | 0.416 | 3,056 | 2.0 | 6,659ms | 3h40m |

> The optimized version's F1 improved from 0.399 to **0.469**
> (+17.5%), reaching **99.9%** of Long-Context F1 (up from 85.1%
> for original). Although the nominal Tokens/QA (17,182) is higher,
> **43.9% are served from prompt cache**, making the effective new
> token cost ~9,663/QA (see Section 4.5).
>
> As a supplemental retrieval path, Session Recall now pushes
> overall F1 to **0.549** while keeping Tokens/QA at **3,694**.
> Compared with Long-Context, it uses **80.3% fewer tokens** per QA;
> compared with the optimized version, it uses **78.5% fewer tokens**.

**Table 2: F1 by Category**

| Category | Count | Long-Context | Session Recall | Optimized | Original |
| --- | ---: | ---: | ---: | ---: | ---: |
| single-hop | 282 | 0.320 | 0.368 | **0.396** | 0.316 |
| multi-hop | 321 | 0.308 | **0.554** | 0.453 | 0.096 |
| temporal | 96 | 0.088 | 0.174 | **0.247** | 0.088 |
| open-domain | 841 | 0.518 | **0.618** | 0.441 | 0.358 |
| adversarial | 446 | 0.667 | 0.610 | 0.626 | **0.814** |

**Table 3: Weighted Average F1**

| Average | Long-Context | Session Recall | Optimized | Original |
| --- | ---: | ---: | ---: | ---: |
| 5-category weighted (÷1986) | 0.469 | **0.549** | 0.469 | 0.399 |
| 4-category weighted (÷1540, excl. adversarial) | 0.411 | **0.531** | 0.423 | 0.279 |

> The optimized version still achieves improvements across all four
> knowledge categories. Multi-hop improved from 0.096 to 0.453
> (+372%), the most significant gain. Temporal improved from
> 0.088 to 0.247 (+181%), the second largest gain. Adversarial
> decreased (0.814 → 0.626) as the original had an overly
> aggressive refusal tendency.
>
> As a supplement, Session Recall now changes the trade-off profile
> much more substantially. It is best on **multi-hop** and
> **open-domain**, improves **temporal** to 0.174, and raises
> 4-category weighted F1 to **0.531**. The optimized version remains
> stronger on **single-hop** and **temporal**, while Long-Context and
> the optimized version still retain a small edge on **adversarial**.

**Table 4: Per-Sample F1**

| Sample | #QA | Long-Context | Session Recall | Optimized | Original |
| --- | ---: | ---: | ---: | ---: | ---: |
| locomo10_1 | 199 | 0.455 | **0.530** | 0.432 | 0.331 |
| locomo10_2 | 105 | 0.496 | **0.636** | 0.422 | 0.302 |
| locomo10_3 | 193 | 0.527 | **0.644** | 0.521 | 0.432 |
| locomo10_4 | 260 | 0.466 | **0.482** | 0.447 | 0.378 |
| locomo10_5 | 242 | 0.433 | **0.542** | 0.436 | 0.451 |
| locomo10_6 | 158 | 0.511 | **0.553** | 0.505 | 0.455 |
| locomo10_7 | 190 | 0.461 | **0.530** | 0.487 | 0.407 |
| locomo10_8 | 239 | 0.453 | **0.563** | 0.492 | 0.404 |
| locomo10_9 | 196 | 0.450 | **0.508** | 0.464 | 0.383 |
| locomo10_10 | 204 | 0.471 | **0.562** | 0.478 | 0.407 |
| **Average** | **199** | 0.469 | **0.549** | 0.469 | 0.399 |

> The optimized version improves on all 10 samples vs original, and
> surpasses Long-Context on 6 samples.
>
> As a supplement, Session Recall now beats Long-Context on all 10
> samples and beats the optimized version on all 10 samples, with the
> largest gains on `locomo10_2`, `locomo10_3`, and `locomo10_5`.

#### 3.2 Retrieval Strategies vs Long-Context

Long-Context places the full transcript into a single LLM call.
It is effective for short single-session histories, but the two
retrieval-based strategies expose different production trade-offs:

| Dimension | Long-Context | Session Recall | Optimized |
| --- | --- | --- | --- |
| **Cross-session source** | None | Searches raw historical session events at query time | Searches extracted persistent memories |
| **Context window** | Bounded by model limit (128K for GPT-4o-mini) | Unbounded — injects only recalled events | Unbounded — injects only retrieved memories |
| **Scaling** | Cost grows linearly with transcript length | Cost stays near-constant (top-K retrieval) | Cost grows with tool-call steps and retrieved memory payload |
| **Overall F1** | 0.469 | **0.549** | 0.469 |
| **4-category weighted F1** | 0.411 | **0.531** | 0.423 |
| **Tokens/QA** | 18,776 | **3,694** | 17,182 |
| **Best strengths** | Adversarial robustness | Overall accuracy, open-domain, and multi-hop | Temporal and adversarial balance |

---

#### 3.3 SQLite vs SQLiteVec (Subset Run)

This subsection compares `sqlite` (keyword matching) and `sqlitevec`
(semantic vector search via sqlite-vec) on a few controlled subset runs.

**Subset run A: End-to-end QA (Auto / Full categories)**

This run keeps the same end-to-end pipeline and evaluation settings as the
main experiments, but limits to a single sample to control cost.

**Configuration**:

- Dataset: LoCoMo `locomo10.json`
- Sample: `locomo10_1` (199 QA, all categories)
- Scenario: `auto`
- Model: `gpt-4o-mini`
- LLM Judge: enabled
- Embedding model (SQLiteVec): `text-embedding-3-small`
- SQLiteVec retrieval top-k: 10 (default)

**End-to-end results: Overall Metrics and Token Usage (Auto / 199 QA)**

| Backend | #QA | F1 | BLEU | LLM Score | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | Avg Latency |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | 199 | 0.327 | 0.301 | 0.370 | 1,287,813 | 5,624 | 1,293,437 | 398 | 5,805ms |
| SQLiteVec | 199 | 0.307 | 0.285 | 0.325 | 407,969 | 5,556 | 413,525 | 396 | 6,327ms |

**Interpretation (locomo10_1)**:

- **SQLiteVec reduces prompt tokens by ~3.2x** (bounded top-k retrieval),
  but **F1/BLEU/LLM Score are slightly lower** on this sample at the
  default top-k=10 setting.
- Category-level behavior differs: `sqlitevec` improves `adversarial`
  (more correct refusals), but underperforms on other categories when the
  needed evidence is not retrieved within top-k.

We also rerun the same configuration on another representative sample.

- Sample: `locomo10_6` (158 QA, all categories)

**End-to-end results: Overall Metrics and Token Usage (Auto / 158 QA)**

| Backend | #QA | F1 | BLEU | LLM Score | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | Avg Latency |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | 158 | 0.269 | 0.243 | 0.289 | 1,296,580 | 5,103 | 1,301,683 | 340 | 6,359ms |
| SQLiteVec | 158 | 0.274 | 0.254 | 0.295 | 362,903 | 4,773 | 367,676 | 324 | 6,928ms |

**Overall takeaway (locomo10_1 + locomo10_6)**:

- SQLiteVec consistently reduces prompt tokens by ~3x-4x in our runs.
- Answer quality changes are sample-dependent at the default top-k=10;
  increasing top-k can improve recall but will also increase prompt tokens.

> Note: `Prompt Tokens`, `LLM Calls` count only the QA agent model calls.
> They exclude embedding requests and LLM-as-Judge calls. `Avg Latency`
> reflects end-to-end time averaged by #QA (including embeddings, judge,
> and auto extraction).

**Subset run B: Temporal-only token-cost micro-run**

**Configuration**:

- Dataset: LoCoMo `locomo10.json`
- Sample: `locomo10_1`
- Category filter: `temporal` (13 QA)
- Scenario: `auto`
- Model: `gpt-4o-mini`
- LLM Judge: disabled
- Embedding model (SQLiteVec): `text-embedding-3-small`

**Table 5: Overall Metrics and Token Usage (Auto / Temporal / 13 QA)**

| Backend | F1 | BLEU | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | Avg Latency |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | 0.116 | 0.082 | 80,184 | 352 | 80,536 | 26 | 12,352ms |
| SQLiteVec | 0.116 | 0.082 | 26,483 | 353 | 26,836 | 26 | 17,817ms |

**Subset run C: Vector top-k sweep + multi-search ablation (Auto / Full categories)**

**Table 6: Top-k and Multi-search Sweep (Auto / locomo10_1 / 199 QA)**

| Backend | vector-topk | qa-search-passes | F1 | BLEU | Prompt Tokens | Avg Prompt/QA | Avg Latency |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | - | 1 | 0.299 | 0.283 | 1,322,360 | 6,645 | 3,316ms |
| SQLiteVec | 5 | 1 | 0.320 | 0.296 | 346,253 | 1,740 | 4,182ms |
| SQLiteVec | 10 | 1 | 0.343 | 0.315 | 398,751 | 2,004 | 4,352ms |
| SQLiteVec | 20 | 1 | 0.329 | 0.308 | 621,790 | 3,125 | 4,180ms |
| SQLiteVec | 40 | 1 | 0.327 | 0.303 | 965,423 | 4,851 | 4,460ms |
| SQLiteVec | 10 | 2 | 0.342 | 0.312 | 659,981 | 3,316 | 5,198ms |

**Interpretation**:

- **Increasing top-k does not monotonically improve quality**: top-k=20/40
  increases prompt tokens but slightly lowers F1/BLEU. The QA agent can
  be sensitive to noise in retrieved memories.
- `qa-search-passes=2` improves some categories (e.g. multi-hop) but does
  not improve overall F1, and increases both tokens and latency.

---

### 4. Comparison with Python Agent Frameworks

We ran the same LoCoMo benchmark on four Python agent frameworks —
**AutoGen**, **Agno**, **ADK**, **CrewAI** — all using GPT-4o-mini,
the same 10 samples (1,986 QA), and LLM-as-Judge evaluation.

#### 4.1 Framework Configurations

| Framework | Memory Backend | Retrieval | Embedding |
| --- | --- | --- | --- |
| **trpc-agent-go** | pgvector | Vector similarity (top-K) + multi-pass | text-embedding-3-small |
| **AutoGen** | ChromaDB | Vector similarity (top-30) | text-embedding-3-small |
| **Agno** | SQLite | LLM fact extraction → system prompt | N/A |
| **ADK** | In-memory | Agent tool call (LoadMemoryTool) | Internal |
| **CrewAI** | Built-in vector | Auto-retrieve by Crew | Internal |

#### 4.2 Framework Memory Approaches

Below is a detailed breakdown of each framework's memory storage,
retrieval, and QA call flow. All benchmark implementations share
the same system prompt strategy (five-category QA answering rules)
and evaluation pipeline.

**trpc-agent-go (optimized) — Auto extraction + pgvector hybrid:**

- **Storage**: Conversation turns are processed by an LLM extractor
  into structured facts/episodes (content + metadata + event_time),
  stored in pgvector.
- **Stored message roles**: The extractor's
  `ExtractionContext.Messages` includes **both user and assistant
  messages** (excluding tool calls), so both sides of the conversation
  are available for LLM memory extraction.
- **Retrieval**: The agent issues a `memory_search` tool call that
  triggers pgvector hybrid search (vector similarity + keyword
  matching), returning up to 30 structured memory entries.
- **QA flow**: 3 LLM calls (Step 1 emits tool call for search #1 →
  Step 2 emits tool call for search #2 → Step 3 reads all results
  and answers).
- **Strengths**: Extracted memories are precise, high information
  density; hybrid search covers both semantic and keyword matches.
- **Token profile**: The tool-call pattern re-reads prior context
  at each step, resulting in ~17,182 prompt tokens/QA. However,
  **43.9% of prompt tokens are served from the provider's prompt
  cache** (OpenAI `cached_tokens`), so the effective *new* prompt
  cost is ~9,663 tokens/QA — comparable to single-call approaches
  when measured by billable cost (cached tokens are billed at 50%
  on most providers).
- **Issues**: Structured JSON format adds serialization overhead;
  multi-step latency is higher than single-call patterns.

**AutoGen — Raw turns in ChromaDB + single LLM call:**

- **Storage**: Raw conversation turns stored as
  `[SessionDate: ...] Speaker: text` in ChromaDB; embedding only,
  no LLM extraction.
- **Stored message roles**: No auto-storage — `ChromaDBVectorMemory.
  add()` is a purely manual API; the caller decides what to store.
  In our benchmark, we manually `add()` each turn without role
  distinction.
- **Retrieval**: Before `AssistantAgent.run()`, the
  `ChromaDBVectorMemory.update_context()` method queries ChromaDB
  with the question, retrieves top-30 results (score ≥ 0.3), and
  injects them as a `SystemMessage` into the model context.
- **QA flow**: **1 LLM call** — retrieval results are pre-injected
  before the call; no tool call needed.
- **Strengths**: Fewest calls (1/QA), highest token efficiency
  (1,943 tokens/QA).
- **Issues**: Adversarial F1 only 0.272 (lowest among all
  frameworks), severe adversarial robustness deficiency; relies on
  pure vector search with no keyword/BM25 supplement.

**CrewAI — ShortTermMemory + Crew two-step call:**

- **Storage**: Raw conversation turns stored in CrewAI's built-in
  `ShortTermMemory` (ChromaDB-based vector store); no LLM
  extraction.
- **Stored message roles**: The framework stores **task-level
  execution summaries** (task description + agent role + expected
  output + final result), not individual messages. In our benchmark,
  we bypass this and manually `stm.save()` each turn.
- **Retrieval**: Monkey-patched `ContextualMemory._fetch_stm_context`
  widens the search window to top-30 (default is only top-5);
  results formatted as `- [content]` list injected into agent
  context.
- **QA flow**: 2 LLM calls — Call 1 is Crew's internal
  formatting/planning step, Call 2 answers with memory context.
- **Strengths**: Simple storage (no LLM extraction cost), compact
  retrieval format.
- **Issues**: Insufficient vector retrieval recall; Crew's Call 1
  (planning step) is pure framework overhead contributing ~140
  completion tokens/QA with no F1 benefit; adversarial and temporal
  categories show 44.6% and 39.6% loss rates respectively.

**ADK — InMemoryMemoryService + LoadMemoryTool full load:**

- **Storage**: Conversation turns stored as `Event` objects in ADK's
  `InMemoryMemoryService` (pure in-memory, no persistence).
- **Stored message roles**: `add_session_to_memory()` stores **all**
  events with `content.parts` — **user, model, and tool events are
  all included** without filtering by author.
- **Retrieval**: The agent calls `LoadMemoryTool` which loads
  **all memories indiscriminately into context** — no selective
  retrieval whatsoever.
- **QA flow**: 2 LLM calls (Step 1 calls LoadMemoryTool → Step 2
  reads all memories and answers).
- **Strengths**: No memory loss.
- **Issues**: **Catastrophic token inflation** (49,224 tokens/QA,
  3.0x the optimized version); 9 QA exceeded 128K tokens causing
  context overflow; 10 QA returned empty predictions; single QA
  peak at 252,849 tokens.

**Agno — LLM fact extraction + SQLite full injection:**

- **Storage**: Each conversation turn is processed by
  `MemoryManager` which calls an LLM to extract facts/preferences,
  stored in SQLite (LLM extraction cost excluded from QA token
  counts).
- **Stored message roles**: `make_memories()` processes **only user
  messages** — assistant and tool messages are excluded.
  `create_or_update_memories()` also filters `m.role == 'user'`
  explicitly.
- **Retrieval**: With `add_memories_to_context=True`, **all**
  stored memories are injected into the system prompt under
  `<memories_from_previous_interactions>` — no vector search or
  similarity filtering.
- **QA flow**: 1 LLM call (memories already in system prompt).
- **Strengths**: LLM extraction preserves key facts.
- **Issues**: **Full injection inflates to 10,436 tokens/QA**;
  highest latency (14,127ms/QA, 7h47m total); the underlying
  DB interface's `limit`/`topics` filtering parameters are
  never used by `MemoryManager` — a design gap.

**Approach comparison summary:**

| Dimension | Session Recall | trpc-agent-go (optimized) | AutoGen | CrewAI | ADK | Agno |
| --- | --- | --- | --- | --- | --- | --- |
| Stored message roles | user + assistant raw session events | user + assistant extracted into structured memories | No auto-storage (manual API) | Task-level summary (input + output) | All events (user + model + tool) | User only (assistant excluded) |
| Benchmark turn mapping | Speaker[0]→user, [1]→assistant | Speaker[0]→user, [1]→assistant | Per-turn manual add() | Per-turn manual save() | Per-turn→Event, whole session write | Per-turn→create_user_memories() |
| Storage | Raw session events | LLM-extracted structured memories | Raw turns | Raw turns | Raw turns | LLM-extracted facts |
| Retrieval | Hybrid RRF over session events, preloaded once | Vector+keyword hybrid via tool calls | Vector top-30 | Vector top-30 | **Full load** | **Full injection** |
| LLM calls/QA | 1 (preload) | 3 (tool call) | **1** (pre-inject) | 2 (Crew internal) | 2 (tool call) | 1 (pre-inject) |
| Tokens/QA | 3,694 (3,567 effective†) | 17,182 (9,663 effective‡) | **1,943** | 2,839 | 49,224 | 10,436 |

> † Session Recall cache hit rate is 3.7%, giving an effective new
> token cost of ~3,567/QA.
>
> ‡ 43.9% of optimized prompt tokens are served from the
> provider's prompt cache — the effective *new* token cost is
> ~9,663/QA.
>
> Key insight: **retrieval strategy is the primary differentiator**.
> Full-load approaches (ADK/Agno) waste tokens with poor results;
> selective retrieval (Session Recall / optimized / AutoGen /
> CrewAI) performs significantly better. Within selective retrieval,
> Session Recall now delivers the strongest absolute quality while
> staying in the low-token tier, while the optimized version remains
> the more extraction-heavy, tool-driven alternative.

#### 4.3 Overall Results

**Table 7: Memory Scenario — Overall Metrics**

| Framework | F1 | BLEU | LLM Score | Tokens/QA | Calls/QA | Latency | Total Time |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| **trpc-agent-go (Session Recall)** | **0.549** | **0.511** | **0.609** | 3,694† | 1.0 | 6,430ms | 3h33m |
| trpc-agent-go (optimized) | 0.469 | 0.431 | 0.532 | 17,182‡ | 3.0 | 8,585ms | 4h44m |
| AutoGen | 0.457 | 0.414 | 0.540 | 1,943 | 1.0 | 3,816ms | 2h06m |
| CrewAI | 0.427 | 0.385 | 0.479 | 2,839 | 2.0 | 8,081ms | 4h27m |
| ADK | 0.362 | 0.309 | 0.476 | 49,224 | 2.0 | 5,578ms | 3h04m |
| trpc-agent-go (original) | 0.399 | 0.371 | 0.416 | 3,056 | 2.0 | 6,659ms | 3h40m |
| Agno | 0.332 | 0.289 | 0.494 | 10,436 | 1.0 | 14,127ms | 7h47m |

> † Session Recall cache hit rate is 3.7%; effective new token cost
> is ~3,567/QA.
>
> ‡ 43.9% of optimized prompt tokens hit the provider's prompt
> cache; effective new token cost is ~9,663/QA. See Section 4.5 for
> details.

> **LLM Score aggregation note.** All frameworks now use the same
> all-sample denominator (accuracy-style: `sum(llm_score) / total_qa`).
> Python frameworks originally reported precision-style scores
> (~0.93) that excluded non-scored QAs from the denominator; those
> values have been recalculated here for fair cross-framework
> comparison.

```
Memory F1 (10 samples, 1986 QA)

trpc-agent-go (Session Recall) |====================================================| 0.549
trpc-agent-go (optimized)      |============================================        | 0.469
AutoGen                        |=========================================           | 0.457
CrewAI                         |========================================            | 0.427
trpc-agent-go (original)       |=====================================               | 0.399
ADK                            |==================================                  | 0.362
Agno                           |===============================                     | 0.332
                               +----------------------------------------------------+
                               0.0      0.1      0.2      0.3      0.4      0.5
```

#### 4.4 Category-Level F1

**Table 8: F1 by Category**

| Category | Count | Session Recall | trpc-agent-go (optimized) | AutoGen | CrewAI | trpc-agent-go (original) | ADK | Agno |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| single-hop | 282 | 0.368 | **0.396** | 0.377 | 0.322 | 0.316 | 0.299 | 0.240 |
| multi-hop | 321 | **0.554** | 0.453 | 0.512 | 0.380 | 0.096 | 0.418 | 0.283 |
| temporal | 96 | 0.174 | **0.247** | 0.176 | 0.140 | 0.088 | 0.120 | 0.076 |
| open-domain | 841 | **0.618** | 0.441 | 0.594 | 0.501 | 0.358 | 0.494 | 0.292 |
| adversarial | 446 | 0.610 | 0.626 | 0.272 | 0.448 | **0.814** | 0.163 | 0.556 |

**Table 9: Weighted Average F1**

| Average | Session Recall | trpc-agent-go (optimized) | AutoGen | CrewAI | trpc-agent-go (original) | ADK | Agno |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5-category weighted (÷1986) | **0.549** | 0.469 | 0.457 | 0.427 | 0.399 | 0.362 | 0.332 |
| 4-category weighted (÷1540) | **0.531** | 0.423 | 0.511 | 0.420 | 0.279 | 0.420 | 0.267 |

> The optimized version still materially improves on the original
> memory baseline, especially on **single-hop** and **temporal**
> questions, while Session Recall should be read as a supplemental
> retrieval path on top of that internal evolution.
>
> 5-category weighted F1: **Session Recall ranks first at 0.549**,
> leading the optimized version (0.469) by 0.080 and AutoGen (0.457)
> by 0.092. 4-category weighted F1 also ranks **#1 at 0.531**,
> beating AutoGen's 0.511 by 0.020 while clearly leading all other
> trpc-agent-go variants and dedicated memory systems.

#### 4.5 Token Efficiency and Latency

**Table 10: Token Efficiency Comparison**

| Framework | F1 | Total Tokens | Tokens/QA | Cache Hit | Effective Tokens/QA† | F1/Billion Tokens |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AutoGen | 0.457 | 3,859,412 | 1,943 | n/a | 1,943 | 118.4 |
| trpc-agent-go (Session Recall) | **0.549** | 7,353,057 | 3,694 | 3.7% | 3,567 | 74.6 |
| CrewAI | 0.427 | 5,639,085 | 2,839 | n/a | 2,839 | 75.7 |
| trpc-agent-go (original) | 0.399 | 6,068,802 | 3,056 | n/a | 3,056 | 65.7 |
| trpc-agent-go (optimized) | 0.469 | 34,123,774 | 17,182 | **43.9%** | **9,663** | 13.7 |
| Agno | 0.332 | 20,725,728 | 10,436 | n/a | 10,436 | 16.0 |
| ADK | 0.362 | 97,759,453 | 49,224 | n/a | 49,224 | 3.7 |

> † **Effective Tokens/QA** = prompt tokens minus cached prompt
> tokens, plus completion tokens. Cached tokens hit the provider's
> automatic prompt cache (e.g. OpenAI `cached_tokens`) and are
> typically billed at **50% of the standard prompt rate**. The
> Python frameworks do not report `cached_tokens` in their SDKs,
> so their effective cost may also be lower than shown; the `n/a`
> entries indicate data not available rather than zero caching.
>
> By raw token count, AutoGen still achieves the best efficiency
> (118.4 F1/billion tokens). The optimized version remains a
> meaningful improvement over the original memory baseline despite
> its higher nominal token cost. **Session Recall is the strongest
> accuracy/efficiency compromise inside trpc-agent-go**: it reaches
> 0.549 F1 with 3,694 tokens/QA, far below Long-Context and the
> optimized version while substantially outperforming them in
> accuracy. The optimized version remains far more expensive in
> nominal tokens because of the multi-step tool-call pattern where
> each step re-reads prior context; prompt caching mitigates that
> cost, but Session Recall is still much leaner in the current
> setup. ADK remains the least efficient — 49,224 tokens/QA for only
> 0.362 F1.

```
Total Evaluation Time (memory scenario, 1986 QA)

AutoGen            |====                                   | 2h06m
ADK                |======                                 | 3h04m
Session Recall     |=======                                | 3h33m
trpc (original)    |========                               | 3h40m
CrewAI             |==========                             | 4h27m
trpc (optimized)   |==========                             | 4h44m
Agno               |===============================        | 7h47m
                   +----------------------------------------+
                   0h       2h       4h       6h       8h
```

**Why the optimized version is slower (4h44m vs 3h40m):**

The optimized version consumes 5.6x more tokens/QA (17,182 vs 3,056)
and takes 1.29x longer per QA (8,585ms vs 6,659ms). The root cause
is the three-step agentic workflow:

1. **Step 1 — Tool call #1** (~1,650 prompt tokens): The LLM reads
   the system instruction + question, then emits the first
   `memory_search` tool call. This incurs one LLM round-trip plus a
   pgvector hybrid search (vector + keyword) with embedding generation.

2. **Step 2 — Tool call #2** (~5,900 prompt tokens): The LLM
   re-reads all prior context (system prompt + question + first tool
   call + first tool results), then emits a second `memory_search`
   tool call to refine the search.

3. **Step 3 — Final answer** (~10,000 prompt tokens): The LLM
   re-reads the entire conversation (all prior context + second tool
   call + second tool results) and generates the final answer.

The key overhead is **cumulative context re-reading**: each step
re-processes everything from all prior steps. Step 3 alone accounts
for ~10,000 prompt tokens. In contrast, the original version uses a
2-call agentic pattern with far fewer/shorter memory entries (~3,056
tokens total for both steps), because its memories are stored as
raw conversation turns rather than extracted structured
facts/episodes.

**Prompt cache mitigates the cost:** Despite re-reading prior
context at each step, the multi-turn pattern is highly
cache-friendly — Steps 2 and 3 share a long common prefix with
their predecessors. In practice, **43.9% of all prompt tokens
(14.93M out of 34.01M) are served from the provider's automatic
prompt cache**, reducing the effective new prompt volume to
~19.08M tokens. At the standard 50% cache pricing, the actual
billable prompt cost is equivalent to ~26.54M tokens rather than
34.01M — a **~22% reduction** from the nominal figure.

Despite the higher token cost, the optimized version achieves a
significantly better F1/cost trade-off: **+17.5% F1** (0.399→0.469)
for **5.6x nominal token cost** (significantly less after cache
discounts), making it worthwhile for production use where answer
quality matters more than token budget.

#### 4.6 ADK Failure Analysis

ADK (Google Agent Development Kit) uses an in-memory backend with
agent tool calls (`LoadMemoryTool`) for memory retrieval. In this
evaluation, ADK encountered context overflow issues on some samples:

**Table 11: ADK Context Overflow Details**

| Sample | #QA | Empty Predictions | QA with >128K Tokens | Max Tokens |
| --- | ---: | ---: | ---: | ---: |
| conv-26 | 199 | 0 | 0 | 43,887 |
| conv-30 | 105 | 0 | 0 | 59,458 |
| conv-41 | 193 | 4 | 4 | 252,849 |
| conv-42 | 260 | 1 | 1 | 180,603 |
| conv-43 | 242 | 2 | 2 | 162,249 |
| conv-44 | 158 | 1 | 0 | 123,063 |
| conv-47 | 190 | 0 | 0 | 114,912 |
| conv-48 | 239 | 1 | 0 | 105,680 |
| conv-49 | 196 | 0 | 1 | 166,597 |
| conv-50 | 204 | 1 | 1 | 219,026 |
| **Total** | **1,986** | **10** | **9** | **252,849** |

- **10 QA (0.5%) returned empty predictions**, concentrated in
  samples with longer conversation histories
- **53 QA exceeded 100K tokens**, with the single highest reaching
  **252,849 tokens** — approaching GPT-4o-mini's 128K context
  window limit
- ADK's `LoadMemoryTool` loads **all memories** into context
  without selective retrieval, causing severe token waste on
  longer conversations
- Average 49,224 tokens/QA (highest among all frameworks) for
  only 0.362 F1

#### 4.7 Per-Sample F1

**Table 12: Per-Sample F1 Comparison**

| Sample | #QA | Session Recall | trpc-agent-go (optimized) | AutoGen | CrewAI | trpc-agent-go (original) | ADK | Agno |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| conv-26 | 199 | **0.530** | 0.432 | 0.384 | 0.355 | 0.331 | 0.337 | 0.296 |
| conv-30 | 105 | **0.636** | 0.422 | 0.451 | 0.439 | 0.302 | 0.379 | 0.334 |
| conv-41 | 193 | **0.644** | 0.521 | 0.513 | 0.440 | 0.432 | 0.335 | 0.387 |
| conv-42 | 260 | **0.482** | 0.447 | 0.439 | 0.408 | 0.378 | 0.343 | 0.338 |
| conv-43 | 242 | **0.542** | 0.436 | 0.486 | 0.413 | 0.451 | 0.355 | 0.341 |
| conv-44 | 158 | **0.553** | 0.505 | 0.491 | 0.509 | 0.455 | 0.384 | 0.289 |
| conv-47 | 190 | **0.530** | 0.487 | 0.496 | 0.405 | 0.407 | 0.374 | 0.321 |
| conv-48 | 239 | **0.563** | 0.492 | 0.463 | 0.432 | 0.404 | 0.392 | 0.328 |
| conv-49 | 196 | **0.508** | 0.464 | 0.418 | 0.407 | 0.383 | 0.371 | 0.302 |
| conv-50 | 204 | **0.562** | 0.478 | 0.475 | 0.487 | 0.407 | 0.363 | 0.374 |
| **Average** | **199** | **0.549** | 0.469 | 0.457 | 0.427 | 0.399 | 0.362 | 0.332 |

> Session Recall beats AutoGen on all 10 samples.

---

### 5. Comparison with External Memory Systems

Source: Mem0 Table 1 (Chhikara et al., 2025, arXiv:2504.19413).
All systems use GPT-4o-mini. Adversarial category excluded for
cross-system comparability (Mem0 paper does not include it).

> **About "LoCoMo (paper baseline)" in the table.** LoCoMo is
> both the dataset used in this report and a memory system
> proposed in the LoCoMo paper (Maharana et al., 2024). That
> system extracts events and summaries from conversations via
> LLM and retrieves them at query time using BM25 + semantic
> search. The Mem0 paper reproduced this approach on the same
> dataset and reported the F1 scores shown here. The table entry
> "LoCoMo (paper baseline)" thus refers to the memory system's
> performance, not the dataset itself.

**Table 13: F1 by Category (Excluding Adversarial)**

| Method | Single-Hop | Multi-Hop | Open-Domain | Temporal | 4-cat Weighted | Source |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| AutoGen | 0.377 | 0.512 | 0.594 | 0.176 | 0.511 | This work |
| **trpc-agent-go (Session Recall)** | 0.368 | **0.554** | **0.618** | 0.174 | **0.531** | This work |
| trpc-agent (optimized) | **0.396** | 0.453 | 0.441 | 0.247 | 0.423 | This work |
| Mem0g | 0.381 | 0.243 | 0.493 | **0.516** | 0.422 | Mem0 paper |
| Mem0 | 0.387 | 0.286 | 0.477 | 0.489 | 0.421 | Mem0 paper |
| CrewAI | 0.322 | 0.380 | 0.501 | 0.140 | 0.420 | This work |
| trpc-agent (LC) | 0.320 | 0.308 | 0.518 | 0.088 | 0.411 | This work |
| ADK | 0.299 | 0.418 | 0.494 | 0.120 | 0.420 | This work |
| Zep | 0.357 | 0.194 | 0.496 | 0.420 | 0.403 | Mem0 paper |
| LangMem | 0.355 | 0.260 | 0.409 | 0.308 | 0.362 | Mem0 paper |
| A-Mem | 0.270 | 0.121 | 0.447 | 0.459 | 0.347 | Mem0 paper |
| OpenAI Memory | 0.343 | 0.201 | 0.393 | 0.140 | 0.328 | Mem0 paper |
| MemGPT | 0.267 | 0.092 | 0.410 | 0.255 | 0.308 | Mem0 paper |
| LoCoMo (paper baseline) | 0.250 | 0.120 | 0.404 | 0.184 | 0.303 | Mem0 paper |
| trpc-agent (original) | 0.316 | 0.096 | 0.358 | 0.088 | 0.279 | This work |
| Agno | 0.240 | 0.283 | 0.292 | 0.076 | 0.267 | This work |
| ReadAgent | 0.092 | 0.053 | 0.097 | 0.126 | 0.089 | Mem0 paper |
| MemoryBank | 0.050 | 0.056 | 0.066 | 0.097 | 0.063 | Mem0 paper |

```
4-Category Weighted F1 (excluding adversarial, 1540 QA)

Session Recall      |============================================| 0.531
AutoGen             |==========================================  | 0.511
trpc-agent (optimized) |==================================       | 0.423
Mem0g               |==================================        | 0.422
Mem0                |==================================        | 0.421
CrewAI              |=================================         | 0.420
ADK                 |=================================         | 0.420
trpc-agent (LC)     |=================================         | 0.411
Zep                 |================================          | 0.403
LangMem             |=============================             | 0.362
A-Mem               |===========================               | 0.347
OpenAI Memory       |==========================                | 0.328
MemGPT              |========================                  | 0.308
LoCoMo (baseline)   |========================                  | 0.303
trpc-agent (original) |======================                  | 0.279
Agno                |====================                      | 0.267
                    +------------------------------------------+
                    0.0      0.1      0.2      0.3      0.4   0.5
```

> **5-category weighted F1** (for frameworks with adversarial data):
>
> | Method | 5-cat Weighted F1 |
> | --- | ---: |
> | **trpc-agent-go (Session Recall)** | **0.549** |
> | trpc-agent (optimized) | 0.469 |
> | AutoGen | 0.457 |
> | CrewAI | 0.427 |
> | trpc-agent (original) | 0.399 |
> | ADK | 0.362 |
> | Agno | 0.332 |

**Key takeaways:**

1. **trpc-agent-go (Session Recall)** reaches a 4-category weighted F1 of
   **0.531**, ranking **#1 overall** and surpassing AutoGen
   (0.511) by 0.020. It clearly surpasses Mem0g (0.422), Mem0
   (0.421), Zep (0.403), LangMem (0.362), A-Mem (0.347), and other
   dedicated memory systems.
2. **Open-domain and multi-hop are now standout strengths.** Session
   Recall ranks **#1 in multi-hop** (0.554) and **#1 in open-domain**
   (0.618), ahead of AutoGen on both categories.
3. **The optimized version remains a complementary strategy.** It is still the
   strongest trpc-agent-go variant on **temporal** (0.247) and offers
   better adversarial robustness (0.626), but its overall 4-category
   weighted F1 (0.423) is well below Session Recall.
4. **Token efficiency improved dramatically.** Session Recall cuts
   nominal Tokens/QA from 17,182 (optimized) and 18,776
   (Long-Context) down to **3,694**, while also improving F1.
5. Compared with the original baseline, the optimized version first
   moved trpc-agent-go from 0.279 to 0.423 in 4-category weighted F1,
   and Session Recall then pushed that further to 0.531.

---

### 6. Update Policy Results

#### 6.1 Evaluation Protocol

This section compares the archived Optimized result, which uses the
default Merge Similar policy, with Preserve History and Append Only.
The two new runs change only the Auto update policy. Assistant-episode
experiments are excluded: LoCoMo maps a second human speaker to the
assistant role and is not an appropriate evaluation of model-generated
assistant results.

| Item | Value |
| --- | --- |
| Dataset | Official LoCoMo-10; 10 conversations; 1,986 QA |
| Dataset SHA-256 | `79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4` |
| Scenario and backend | Auto + pgvector |
| Answer and judge model | `gpt-4o-mini` |
| Embedding model | `text-embedding-3-small` |
| Retrieval | top-k 30; two `memory_search` passes |
| QA context | No QA-history injection; 128,000-token maximum context |
| Metrics | F1, BLEU, LLM Score, category metrics, tokens, calls, and latency |

#### 6.2 Overall Results

| Policy configuration | F1 | Delta | BLEU | Delta | LLM Score | Delta | Active memories |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Optimized / Merge Similar | 0.4690 | - | 0.4310 | - | 0.5320 | - | unavailable |
| **Preserve History** | **0.4865** | **+1.75pp** | **0.4473** | **+1.63pp** | **0.5609** | **+2.89pp** | 2,740 |
| Append Only | 0.4773 | +0.83pp | 0.4397 | +0.87pp | 0.5441 | +1.21pp | 2,627 |

Preserve History is the strongest measured configuration on all three
overall metrics. Append Only also improves on the Optimized baseline
while retaining 113 fewer active memories than Preserve History.

#### 6.3 Category Metrics

Each cell is `F1 / BLEU / LLM Score`.

| Policy configuration | Single-hop | Multi-hop | Temporal | Open-domain | Adversarial |
| --- | --- | --- | --- | --- | --- |
| Optimized / Merge Similar | 0.396/0.325/0.395 | 0.453/0.415/0.519 | 0.247/0.192/0.364 | 0.441/0.398/0.552 | 0.626/0.626/0.626 |
| Preserve History | 0.386/0.319/0.387 | 0.530/0.484/0.603 | 0.242/0.196/0.415 | 0.479/0.432/0.607 | 0.586/0.585/0.585 |
| Append Only | 0.381/0.312/0.353 | 0.498/0.456/0.579 | 0.209/0.161/0.348 | 0.464/0.420/0.585 | 0.605/0.605/0.605 |

The policy gains are concentrated in multi-hop and open-domain
questions. The Optimized baseline remains stronger on single-hop and
adversarial F1.

#### 6.4 Answerable-Category Weighted F1

This view excludes adversarial questions and uses the fixed 1,540
answerable QA items as its denominator.

| Policy configuration | Weighted F1 | Delta vs Optimized |
| --- | ---: | ---: |
| Optimized / Merge Similar | 0.4230 | - |
| **Preserve History** | **0.4579** | **+3.49pp** |
| Append Only | 0.4402 | +1.72pp |

#### 6.5 Cost and Runtime

| Policy configuration | Prompt tokens | Completion tokens | Total tokens | Delta | Cached tokens | LLM calls | Average latency | Runtime |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Optimized / Merge Similar | 34,007,814 | 115,960 | 34,123,774 | - | unavailable | 5,981 | 8.59s | 4h44m |
| Preserve History | 35,097,721 | 118,823 | 35,216,544 | +3.20% | 15,295,232 | 5,983 | 10.83s | 5.98h |
| Append Only | 34,558,815 | 118,906 | 34,677,721 | +1.62% | 15,111,168 | 5,977 | 10.83s | 5.97h |

#### 6.6 Result Integrity

- Preserve History and Append Only each contain 10 conversations and
  the fixed 1,986 QA items, with structured result artifacts and
  independent pgvector tables.
- One transient embedding 504 was retried successfully; no case or
  score was replaced.
- The Optimized numbers are retained from the existing report
  baseline. Its original structured result artifact is not present in
  this repository, so exact deltas are report-derived and must not be
  described as independently reproduced from a committed artifact.

---
### 7. Conclusion

#### Key Findings

1. **trpc-agent-go Session Recall is now the strongest overall
   configuration.** It ranks **#1 in 5-category weighted F1** at
   **0.549** and **#1 in 4-category weighted F1** at **0.531**,
   beating AutoGen on both metrics. Compared with Long-Context and
   the optimized version, it improves overall F1 while using far
   fewer tokens.

2. **Different retrieval strategies now show clear trade-offs.**
   Session Recall is best on **open-domain** and **multi-hop**,
   making it the best default choice for cross-session QA. The
   optimized version remains stronger on **temporal** and adversarial
   robustness, while Long-Context still serves as a useful upper
   bound for short single-session histories.

3. **trpc-agent-go now surpasses dedicated memory systems by a wide
   margin.** Session Recall's 4-category weighted F1 of 0.531 is well
   above Mem0g (0.422), Mem0 (0.421), Zep (0.403), LangMem (0.362),
   A-Mem (0.347), OpenAI Memory (0.328), MemGPT (0.308), and other
   purpose-built memory systems.

4. **Limitations of other Python frameworks.**

   - **ADK**: Highest token consumption (49,224 tokens/QA) — **2.9x**
     that of the optimized version — yet only achieves 0.362 F1. Its
     `LoadMemoryTool` loads all memories indiscriminately into
     context, causing severe token waste and context overflow (9 QA
     exceeded 128K tokens) in longer conversations, lacking any
     selective retrieval capability
   - **Agno**: Lowest F1 (0.332), highest latency (14,127ms/QA,
     7h47m total), with token consumption of 10,436/QA. Like ADK,
     Agno employs a full-loading architecture — injecting all user
     memories into the system prompt under a
     `<memories_from_previous_interactions>` tag with no vector
     search or similarity retrieval. Although the underlying DB
     interface exposes `limit`, `topics`, and other filtering
     parameters, the `MemoryManager` never utilizes them at runtime
   - **CrewAI**: Memory loss in its short-term memory
     backend — particularly severe in adversarial (44.6%) and
     temporal (39.6%) categories
   - **AutoGen**: While achieving 0.511 in 4-category weighted F1,
     this is largely driven by a single outstanding category
     (open-domain at 0.594); its adversarial score of 0.272 is the
     lowest among all frameworks, revealing a critical adversarial
     robustness deficiency

5. **Memory is essential for production agents.** Long-Context is
   effective for short single-session scenarios, but cannot persist
   knowledge across sessions or scale beyond the model's context
   window. Session Recall delivers a stronger quality/cost balance,
   while the optimized version provides a second memory strategy built on
   extracted persistent memories.

6. **Temporal reasoning remains the next optimization target.** The
   optimized version reaches 0.247 in temporal, but Session Recall is
   still at 0.174. Time-aware retrieval, temporal query rewriting,
   and richer reranking remain the main next steps.

#### Production Recommendations

| Use Case | Recommended Approach |
| --- | --- |
| Short single-session (< 50K tokens) | Long-context (no memory needed) |
| Cross-session QA / best accuracy | Session Recall |
| Long-running agents (weeks/months) | Optimized |
| History exceeding context window | Session Recall or optimized |

---

## LongMemEval

After the LoCoMo evaluation we moved to LongMemEval, which puts more
weight on building and retrieving user memory across sessions. It
covers two dimensions LoCoMo cannot evaluate: how an update policy
behaves on long-span input, and what assistant-episode extraction
contributes.

This section reports a development-stage evaluation on a 50-case
subset. The numbers support relative comparison between configurations
and are not a final score on the benchmark.

### 1. Dataset and Case Selection

LongMemEval (Wu et al., 2024) evaluates long-term memory over
multi-session user/assistant chat histories, with 500 questions in six
question types. This section uses the cleaned build of
**LongMemEval-S**, `longmemeval_s_cleaned.json`: each question ships
with a haystack of past sessions, 23,867 sessions across the full 500
cases, 38 to 62 sessions per case with a median of 48. Every type can
contain **abstention questions**, whose question IDs carry the `_abs`
suffix; the correct behavior there is to recognize that the history
holds no supporting evidence and decline to answer. The full set
contains 30 of them.

| Question type | Full set | Abstention | Cases here |
| --- | ---: | ---: | ---: |
| knowledge-update | 78 | 6 | 8 |
| multi-session | 133 | 12 | 13 |
| single-session-assistant | 56 | 0 | 6 |
| single-session-preference | 30 | 0 | 3 |
| single-session-user | 70 | 6 | 7 |
| temporal-reasoning | 133 | 6 | 13 |
| Total | 500 | 30 | 50 |

One configuration takes about 30 hours end to end, which ruled out
running all 500 cases within the time and token budget of this round.
The subset is therefore **stratified by question type, taking 10% of
each type**: 8, 13, 6, 3, 7, and 13 after rounding, 50 cases in total,
with the same type proportions as the full set. Abstention questions
receive no separate quota and enter through the stratified draw, which
yields three of them, in knowledge-update, multi-session, and
single-session-user.

The 50 selected cases are pinned by case ID, and every experiment in
this section uses the same cases in the same order. The list records
no random seed and cannot be re-derived from dataset order, so
reproduction should use the list in **Appendix D** directly.

### 2. Experimental Setup

| Item | Value |
| --- | --- |
| Dataset | LongMemEval-S cleaned (`longmemeval_s_cleaned.json`) |
| Cases | 50 cases sampled proportionally per question type, covering all six types |
| Distribution | knowledge-update 8; multi-session 13; assistant 6; preference 3; user 7; temporal 13 |
| Input | 2,353 sessions; 24,370 turns; 12,280 user/assistant pairs |
| Build protocol | Ordered turn-pair fragments; all scenarios share the same replay input |
| Build chunk limit | 6,000 `cl100k_base` tokens; one over-limit pair was split without content loss across separate extraction boundaries |
| Answer, extraction, and judge model | `glm52` |
| Embedding model | `text-embedding-ada-002` |
| Retrieval | Standard `memory_search`, fixed `top-k=20` |
| Benchmark revision | `c8c305c4c50594e3d083e06a5248cfeb81b15823` |
| trpc-agent-go revision | `1b3adb2f4bb8` |
| Primary metric | Fixed-denominator LLM-judge Accuracy |

All scenarios consume the same replay input and case order. Memory is
normally built once per user/assistant pair; an over-limit pair is
split without content loss, but each fragment is a separate Runner
call and extraction boundary, and the affected case IDs are recorded
in provenance. Pairs from one source session retain their session
identity; different source sessions remain isolated under the same
case-level user memory. Auto receives the source observation date
through the extractor reference-date API. Mem0 OSS 2.0.11
(`3b9aed866ae70d29043388ed0ae5cc4e1844f3e8`) receives the same date
through the supported extraction `prompt` field. QA uses a fresh
session and can see only the current question, its date, and results
returned by `memory_search`. Gold answers and evidence are available
only to evaluation and diagnostics.

The three Auto runs differ only in update policy and use independent
pgvector tables. Mem0 retains its native extraction and reconciliation
behavior. Cases that do not complete remain in the fixed denominator
of 50 and score as incorrect.

### 3. Update Policy Results

| Scenario | Policy | Successful | Failed | Correct | Accuracy | F1 | BLEU | ROUGE-L | Runtime |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Auto | Merge Similar | 48/50 | 2 | 14 | 0.2800 | 0.0961 | 0.0635 | 0.0885 | 28h34m |
| Auto | Preserve History | 50/50 | 0 | 41 | 0.8200 | 0.1683 | 0.1079 | 0.1614 | 29h51m |
| Auto | Append Only | 50/50 | 0 | 41 | 0.8200 | **0.1730** | **0.1112** | **0.1679** | 28h07m |
| Mem0 OSS | native | 50/50 | 0 | 42 | **0.8400** | 0.1681 | 0.1067 | 0.1588 | 30h15m |

Runtime is the total build and QA time for the 50 cases of a scenario.
The Failed column counts cases that produced no answer; they stay in
the fixed denominator of 50 and score as incorrect. The two Merge
Similar failures come from QA exceeding eight tool iterations.

#### Accuracy by Question Type

| Question type | Count | Merge Similar | Preserve History | Append Only | Mem0 OSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| knowledge-update | 8 | 0.5000 | 0.8750 | **1.0000** | 0.8750 |
| multi-session | 13 | 0.2308 | **0.8462** | 0.7692 | 0.7692 |
| single-session-assistant | 6 | 0.0000 | 0.0000 | 0.1667 | **0.5000** |
| single-session-preference | 3 | 0.0000 | **1.0000** | **1.0000** | **1.0000** |
| single-session-user | 7 | 0.2857 | **1.0000** | 0.8571 | **1.0000** |
| temporal-reasoning | 13 | 0.3846 | **1.0000** | **1.0000** | 0.9231 |

The table shows that the largest shortfall of the framework against
Mem0 OSS is on single-session-assistant.

### 4. Assistant-Episode Extraction Ablation

This section compares the three Auto update policies with
assistant-episode extraction disabled and enabled on the same 50
cases, six runs in total. The enabled rows use the conditional
two-stage extractor as it was then: ordinary user-memory extraction
runs first, and a bounded assistant-result extraction request is
issued only for a strong structured-result candidate. That
implementation predates the merged version, and eligibility,
grounding, and request construction changed afterwards, so the enabled
rows are historical references and do not represent the current
implementation. Mem0 uses its native extraction behavior and is listed
as an external reference row.

All seven rows use the same 50 cases and order, `glm52`,
`text-embedding-ada-002`, the same ordered turn-pair fragments, the
same 6,000-token chunk limit, and `top-k=20`.

| Policy / backend | Assistant extraction | QA succeeded | Failed | Correct | Accuracy | F1 | BLEU | ROUGE-L |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | Disabled | 48/50 | 2 | 14 | 0.2800 | 0.0961 | 0.0635 | 0.0885 |
| Merge Similar | Enabled | 45/50 | 5 | 29 | 0.5800 | 0.1626 | 0.1083 | 0.1518 |
| Preserve History | Disabled | 50/50 | 0 | 41 | 0.8200 | 0.1683 | 0.1079 | 0.1614 |
| Preserve History | Enabled | 48/50 | 2 | **45** | **0.9000** | 0.1876 | 0.1204 | 0.1779 |
| Append Only | Disabled | 50/50 | 0 | 41 | 0.8200 | 0.1730 | 0.1112 | 0.1679 |
| Append Only | Enabled | 47/50 | 3 | 44 | 0.8800 | **0.1970** | **0.1295** | **0.1865** |
| Mem0 OSS (reference) | Native | 50/50 | 0 | 42 | 0.8400 | 0.1681 | 0.1067 | 0.1588 |

Failed QA remains in the fixed 50-case denominator and scores as
incorrect. The assistant-enabled runs are not replacements selected by
score; each row is one complete experimental configuration.

#### Accuracy by Question Type (Assistant Extraction Enabled)

| Question type | Count | Merge Similar | Preserve History | Append Only | Mem0 OSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| knowledge-update | 8 | 0.7500 | **1.0000** | **1.0000** | 0.8750 |
| multi-session | 13 | 0.3077 | 0.7692 | **0.8462** | 0.7692 |
| single-session-assistant | 6 | **0.8333** | **0.8333** | **0.8333** | 0.5000 |
| single-session-preference | 3 | **1.0000** | 0.6667 | 0.6667 | **1.0000** |
| single-session-user | 7 | **1.0000** | **1.0000** | 0.8571 | **1.0000** |
| temporal-reasoning | 13 | 0.3077 | **1.0000** | 0.9231 | 0.9231 |

Compared with the same question types under disabled extraction in
Section 3, single-session-assistant is where the gain concentrates:
the three policies move from 0.0000, 0.0000 and 0.1667 to 0.8333,
above the 0.5000 of Mem0. Merge Similar improves on the widest front,
with single-session-user rising from 0.2857 to 1.0000 and
knowledge-update from 0.5000 to 0.7500. Enabling extraction also
produces a few regressions, each corresponding to a single case:
single-session-preference falls from 1.0000 to 0.6667 for Preserve
History and Append Only, multi-session falls from 0.8462 to 0.7692 for
Preserve History, and temporal-reasoning falls from 1.0000 to 0.9231
for Append Only and from 0.3846 to 0.3077 for Merge Similar. Assistant
extraction does affect the other question types, but the effect is
limited. In practice we recommend enabling it only when the assistant
returns information worth persisting, such as important facts, and
keeping it disabled by default.

### 5. Memory Footprint and Cost

| Scenario | Complete builds | Final entries | Entries/case | Median | Range |
| --- | ---: | ---: | ---: | ---: | ---: |
| Auto Merge Similar | 50/50 | 2,955 | 59.10 | 58 | 35-87 |
| Auto Preserve History | 50/50 | 15,353 | 307.06 | 305.5 | 257-381 |
| Auto Append Only | 50/50 | 16,280 | 325.60 | 326 | 264-396 |
| Mem0 OSS | 50/50 | 28,041 | 560.82 | 564.5 | 465-602 |

The inventory is taken from the stored memory entries at the end of
each completed build. The general snapshot reader requests up to
10,000 entries and the Mem0 OSS adapter observes up to 1,000; both are
well above the observed maximum of 602, so these counts are not
truncated. They count active entries after ingestion, not extraction
operations or database rows shared across cases.

Memory footprint with assistant-episode extraction enabled:

| Policy / backend | Assistant extraction | Active memories | Complete builds | Entries per complete build |
| --- | --- | ---: | ---: | ---: |
| Merge Similar | Disabled | 2,955 | 50/50 | 59.10 |
| Merge Similar | Enabled | 6,011 | 50/50 | 120.22 |
| Preserve History | Disabled | 15,353 | 50/50 | 307.06 |
| Preserve History | Enabled | 17,696 | 49/50 | 361.14 |
| Append Only | Disabled | 16,280 | 50/50 | 325.60 |
| Append Only | Enabled | 18,728 | 49/50 | 382.20 |
| Mem0 OSS | Native | 28,041 | 50/50 | 560.82 |

The inventory counts fully built cases only. With assistant extraction
enabled, Preserve History and Append Only each have one case that did
not finish building, so those two rows are short by one case.

| Policy / backend | Assistant extraction | Build LLM calls | Build LLM tokens | QA+judge calls | QA+judge tokens | Build embedding requests | Remote embedding calls | Cache hits |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | Disabled | 12,281 | 96,999,123 | 240 | 2,048,381 | 37,907 | 26,074 | 11,833 |
| Merge Similar | Enabled | 15,647 | 83,758,408 | 201 | 1,619,884 | 40,254 | 28,737 | 11,517 |
| Preserve History | Disabled | 12,336 | 95,086,050 | 163 | 434,852 | 28,028 | 27,581 | 447 |
| Preserve History | Enabled | 15,602 | 86,783,156 | 153 | 399,363 | 30,579 | 30,122 | 457 |
| Append Only | Disabled | 12,411 | 84,701,403 | 168 | 467,266 | 28,860 | 28,401 | 459 |
| Append Only | Enabled | 15,496 | 76,326,984 | 153 | 406,198 | 31,044 | 30,583 | 461 |
| Mem0 OSS | Native | 12,359 | 113,205,317* | 171 | 410,912 | 23,044 | 23,044 | 0 |

Cost accounting includes requests that failed or incomplete builds had
already issued, which differs from the memory inventory above, where
each row counts only the last successful snapshot. With assistant
extraction enabled, build LLM calls rise from 12,281-12,411 to
15,496-15,647, an increase of 3,085-3,366 calls, roughly as if a
quarter of the 12,280 pairs triggered a second extraction.

`*` Mem0 proxy accounting captured calls and observed token fields,
but the provider record marks build LLM and embedding token totals as
`tokens_known=false`; they must not be interpreted as complete
provider usage. Shared caches and concurrent execution also make
remote-call and wall-clock figures unsuitable as standalone backend
speed rankings. This table covers the same case population as Section
3.

### 6. Retrieval Attribution and Memory Structure Analysis

#### 6.1 Gold Session Recall and Failure Attribution

Gold session recall measures whether QA retrieval covered the answer
sessions annotated in the dataset. It is the fraction of the answer
sessions of a case that were hit, so it ranges from 0 to 1, and the
mean is computed over cases that produced an answer: 48 for Merge
Similar disabled and 45 enabled, 48 for Preserve History enabled, 47
for Append Only enabled, and 50 for the remaining configurations. The
"other cases" column merges partial recall, zero recall, and cases
that produced no answer.

| Configuration | Assistant extraction | Mean gold session recall | Fully recalled | Correct | Other cases | Correct | memory_search calls |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | Disabled | 0.6024 | 18 | 6 | 32 | 8 | 195 |
| Merge Similar | Enabled | 0.9148 | 38 | 26 | 12 | 3 | 116 |
| Preserve History | Disabled | 0.9700 | 48 | 41 | 2 | 0 | 88 |
| Preserve History | Enabled | 0.9896 | 47 | 44 | 3 | 1 | 75 |
| Append Only | Disabled | 0.9700 | 48 | 40 | 2 | 1 | 93 |
| Append Only | Enabled | 1.0000 | 47 | 44 | 3 | 0 | 76 |
| Mem0 OSS | Native | 0.9500 | 46 | 40 | 4 | 2 | 98 |

Retrieval is a necessary condition: across the seven runs only 1 of
the 11 zero-recall cases was answered correctly.

Merge Similar loses information at both layers. With extraction
disabled its mean recall is only 0.6024, and even on the 18 fully
recalled cases it answers just 6 correctly (0.3333), against 41/48
(0.8542) for Preserve History. The merged entries often still point at
the right session but no longer carry the detail the answer needs. It
also issues the most retrieval calls, 195 against 75 to 116 for the
other six runs, which shows the model kept rewriting its query without
reaching the evidence.

Enabling assistant extraction raises the mean recall of Merge Similar
to 0.9148 and its accuracy on fully recalled cases to 26/38 (0.6842),
still below the 44/47 (0.9362) of Preserve History and Append Only
with extraction enabled.

The metric counts answer sessions rather than evidence spans, so
"fully recalled but wrong" can mean either that the memory lost the
detail or that the evidence inside the session was not selected.

#### 6.2 One-off High-Similarity Memory Structure Analysis

This subsection records a one-off offline analysis of the same memory
population as Section 5: the same six runs and the same handling of the
case-local rebuilds and the
incomplete builds, so the populations are 2,955, 15,353, and 16,280
entries with assistant extraction disabled and 6,011, 17,696, and
18,728 with it enabled. Every memory pair within a case whose cosine
similarity is at least 0.90 is assigned one deterministic text class:
identical normalized text, strict lexical near-duplicate,
one-directional containment, high overlap with disagreeing numbers or
negations, or vector-similar only. The first three are grouped as
duplicate-like. The predicates call no model and make no semantic
equivalence judgement.

The source snapshot and analysis tooling were not retained. These figures are
included as a single exploratory observation, not as a maintained or
reproducible benchmark artifact, and no reproduction workflow is provided.

| Configuration | Assistant extraction | Memories | Pairs ≥0.90 | Per 1k memories | Duplicate-like | Number/negation mismatch | Vector-similar only |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | Disabled | 2,955 | 279 | 94.4 | 18 (6.5%) | 16 (5.7%) | 245 (87.8%) |
| Preserve History | Disabled | 15,353 | 8,472 | 551.8 | 1,422 (16.8%) | 733 (8.7%) | 6,317 (74.6%) |
| Append Only | Disabled | 16,280 | 8,742 | 537.0 | 407 (4.7%) | 1,022 (11.7%) | 7,313 (83.7%) |
| Merge Similar | Enabled | 6,011 | 1,232 | 205.0 | 58 (4.7%) | 66 (5.4%) | 1,108 (89.9%) |
| Preserve History | Enabled | 17,696 | 8,952 | 505.9 | 1,473 (16.5%) | 482 (5.4%) | 6,997 (78.2%) |
| Append Only | Enabled | 18,728 | 7,795 | 416.2 | 451 (5.8%) | 314 (4.0%) | 7,030 (90.2%) |

Pair counts grow roughly quadratically with the number of entries, so
absolute totals are not comparable across rows; the per-1k column
normalizes them by memory count.

The class composition of the cosine ≥ 0.95 band is as follows.

| Configuration | Assistant extraction | Pairs ≥0.95 | Exact | Near-duplicate | Containment | Number/negation mismatch | Vector-similar only |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | Disabled | 11 | 0 | 4 | 1 | 2 | 4 |
| Preserve History | Disabled | 1,885 | 5 | 496 | 433 | 474 | 477 |
| Append Only | Disabled | 861 | 1 | 96 | 12 | 445 | 307 |
| Merge Similar | Enabled | 119 | 0 | 17 | 3 | 32 | 67 |
| Preserve History | Enabled | 1,563 | 3 | 514 | 434 | 247 | 365 |
| Append Only | Enabled | 441 | 1 | 137 | 16 | 77 | 210 |

- After normalization Merge Similar has the lowest high-similarity
  pair density at 94.4 per 1k entries, while Preserve History and
  Append Only are close at 551.8 and 537.0. In the cosine ≥ 0.98 band
  Merge Similar has no pairs at all, Preserve History has 567 pairs
  spread over all 50 cases, and Append Only has 39 pairs over 14
  cases.
- Duplicate-like pairs concentrate in Preserve History: 16.8% over the
  whole population and 49.5% in the cosine ≥ 0.95 band, against 12.7%
  for Append Only in the same band.
- High vector similarity does not imply textual agreement: in the
  cosine ≥ 0.95 band 445 of 861 Append Only pairs (51.7%) and 474 of
  1,885 Preserve History pairs (25.1%) disagree on a number or a
  negation.

This subsection describes text structure between memories only. The
classes are structural signals rather than semantic labels, and the
analysis does not link a pair class to an update operation or to answer
correctness at case level, so it cannot attribute a wrong answer to
the merging behavior of a policy. As in Section 4, the three
assistant-enabled rows remain historical references.

### 7. Findings

1. **Update policy dominates this sample.** Preserve History raises
   Accuracy from 28% to 82%. Every Merge Similar success is also
   correct under Preserve History; Preserve History adds 27 correct
   cases without a regression unique to Merge Similar. Its completed
   cases retain about 5.2 times as many final entries per case as
   Merge Similar, showing how strongly Merge Similar reconciliation
   reduces the stored evidence set.
2. **Preserve History and Append Only tie on Accuracy but preserve
   different evidence.** They agree on 39 correct cases and each has
   two unique successes. Preserve History is stronger on multi-session
   and single-session-user questions; Append Only is stronger on
   knowledge-update and assistant questions and has the highest
   lexical-overlap metrics.
3. **Mem0 and the two new Auto policies are complementary.** Mem0 and
   Preserve History agree on 38 correct cases; Mem0 has four unique
   successes and Preserve History has three. Mem0 is strongest on
   assistant-memory questions, while Preserve History leads
   multi-session and temporal Accuracy.
4. **Assistant-episode extraction helps every policy, with diminishing
   returns.** Enabling it raises Accuracy by 30 points for Merge
   Similar, 8 points for Preserve History, and 6 points for Append
   Only: the weaker the baseline, the more assistant-side evidence
   adds. Preserve History with assistant extraction has the highest
   Accuracy (0.9000), and Append Only with assistant extraction has
   the strongest lexical-overlap metrics (F1 0.1970, BLEU 0.1295,
   ROUGE-L 0.1865). These gains come from the extraction
   implementation as it was then and need to be re-attributed with a
   rerun on the current implementation.
5. **Those gains come with a larger memory footprint and more
   failures.** Enabling assistant extraction adds 3,056, 2,343, and
   2,448 active memories respectively, while QA failures move from
   2/0/0 to 5/2/3, and two of the runs each leave one case unbuilt.
   Mem0 remains a native external baseline and is not configured with
   the Auto-only assistant option.

### 8. Validity Limits

- These 50 cases are a 10% sample drawn proportionally per question
  type, not the official LongMemEval dev/holdout split. The list pins
  the exact sample but defines no seeded blind split, so the
  conclusions apply to relative comparison between configurations and
  are neither final scores on the benchmark nor a blind holdout
  baseline.
- The sample is small: one case is worth 2 accuracy points, so
  differences within 2 points between policies should not be read as a
  stable gap.
- Two of the assistant-enabled runs in Section 4 each have one case
  that did not finish building, which slightly understates their
  memory footprint, so the six runs there should be used as
  development diagnostics.
- The three assistant-enabled runs use the two-stage implementation as
  it was then. It predates the merged version, and the extraction
  conditions and request construction changed afterwards, so those
  three rows are historical references rather than results of the
  current implementation.
- The recall metric in 6.1 counts answer sessions rather than evidence
  spans, so "same session" cannot be equated with "answerable".
- The classes in 6.2 are deterministic text predicates rather than
  semantic labels. This was a one-off analysis without retained source
  artifacts or a reproduction workflow. It does not link a pair class to
  an update operation or to answer correctness at case level, so it
  supports no causal attribution for an individual wrong answer.

---

## Appendix

### A. LoCoMo Experimental Environment

| Component | Version/Config |
| --- | --- |
| Framework | trpc-agent-go |
| Model | gpt-4o-mini |
| Embedding | text-embedding-3-small |
| PostgreSQL | 15+ with pgvector extension |
| Dataset | LoCoMo-10 (10 samples, 1,986 QA) |

### B. LoCoMo Full Category Breakdown (F1 / BLEU / LLM)

| Scenario | single-hop | multi-hop | temporal | open-domain | adversarial |
| --- | --- | --- | --- | --- | --- |
| Long-Context | 0.320/0.251/0.320 | 0.308/0.273/0.260 | 0.088/0.068/0.165 | 0.518/0.457/0.662 | 0.667/0.667/0.668 |
| Session Recall | 0.368/0.304/0.445 | 0.554/0.512/0.563 | 0.174/0.138/0.311 | 0.618/0.570/0.715 | 0.610/0.610/0.608 |
| Optimized | 0.396/0.325/0.395 | 0.453/0.415/0.519 | 0.247/0.192/0.364 | 0.441/0.398/0.552 | 0.626/0.626/0.626 |
| Original | 0.316/0.250/0.270 | 0.096/0.088/0.060 | 0.088/0.068/0.115 | 0.358/0.319/0.425 | 0.814/0.814/0.814 |

### C. LoCoMo Token Usage — Full Breakdown

| Scenario | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | Calls/QA |
| --- | ---: | ---: | ---: | ---: | ---: |
| Long-Context | 37,272,167 | 16,104 | 37,288,271 | 1,986 | 1.0 |
| Session Recall | 7,336,165 | 16,892 | 7,353,057 | 1,986 | 1.0 |
| Optimized | 34,007,814 | 115,960 | 34,123,774 | 5,981 | 3.0 |
| Original | 6,011,025 | 57,777 | 6,068,802 | 3,999 | 2.0 |
| AutoGen | 3,842,576 | 16,836 | 3,859,412 | 1,986 | 1.0 |
| CrewAI | 5,360,840 | 278,245 | 5,639,085 | 3,972 | 2.0 |
| Agno | 20,694,534 | 31,194 | 20,725,728 | 1,986 | 1.0 |
| ADK | 97,691,620 | 67,833 | 97,759,453 | 4,028 | 2.0 |

### D. The LongMemEval 50-Case List

Every LongMemEval experiment uses the 50 cases below. They were
selected by stratifying on question type and drawing 10% of each type
from the 500 cases of LongMemEval-S, which gives 8, 13, 6, 3, 7, and
13 after rounding and preserves the type proportions of the full set
(see Section 1 of the LongMemEval part). The list is pinned by case ID
and records no random seed, so reproduction should use this table
rather than resampling. Case IDs match `longmemeval_s_cleaned.json`;
the three cases carrying the `_abs` suffix are abstention questions,
where declining to answer is the correct behavior.

| # | Case ID | Question type |
| ---: | --- | --- |
| 1 | `4dfccbf8` | temporal-reasoning |
| 2 | `gpt4_ec93e27f` | temporal-reasoning |
| 3 | `a1eacc2a` | knowledge-update |
| 4 | `gpt4_e072b769` | temporal-reasoning |
| 5 | `3ba21379` | knowledge-update |
| 6 | `gpt4_70e84552` | temporal-reasoning |
| 7 | `f4f1d8a4_abs` | single-session-user |
| 8 | `545bd2b5` | single-session-user |
| 9 | `59524333` | knowledge-update |
| 10 | `0977f2af` | knowledge-update |
| 11 | `60159905` | multi-session |
| 12 | `gpt4_f2262a51` | multi-session |
| 13 | `195a1a1b` | single-session-preference |
| 14 | `gpt4_fa19884c` | temporal-reasoning |
| 15 | `58ef2f1c` | single-session-user |
| 16 | `a346bb18` | multi-session |
| 17 | `ef66a6e5` | multi-session |
| 18 | `7527f7e2` | single-session-user |
| 19 | `3fdac837` | multi-session |
| 20 | `bbf86515` | temporal-reasoning |
| 21 | `f0853d11` | temporal-reasoning |
| 22 | `603deb26` | knowledge-update |
| 23 | `129d1232` | multi-session |
| 24 | `1d4e3b97` | single-session-preference |
| 25 | `gpt4_4cd9eba1` | temporal-reasoning |
| 26 | `faba32e5` | single-session-user |
| 27 | `gpt4_1e4a8aeb` | temporal-reasoning |
| 28 | `2698e78f_abs` | knowledge-update |
| 29 | `gpt4_fa19884d` | temporal-reasoning |
| 30 | `2788b940` | multi-session |
| 31 | `3e321797` | single-session-assistant |
| 32 | `0100672e` | multi-session |
| 33 | `b3c15d39` | multi-session |
| 34 | `d7c942c3` | knowledge-update |
| 35 | `4f54b7c9` | multi-session |
| 36 | `08f4fc43` | temporal-reasoning |
| 37 | `0e5e2d1a` | single-session-assistant |
| 38 | `b01defab` | knowledge-update |
| 39 | `51b23612` | single-session-assistant |
| 40 | `6456829e` | multi-session |
| 41 | `gpt4_2d58bcd6` | temporal-reasoning |
| 42 | `25e5aa4f` | single-session-user |
| 43 | `6456829e_abs` | multi-session |
| 44 | `778164c6` | single-session-assistant |
| 45 | `d851d5ba` | multi-session |
| 46 | `06f04340` | single-session-preference |
| 47 | `bcbe585f` | temporal-reasoning |
| 48 | `c960da58` | single-session-user |
| 49 | `e3fc4d6e` | single-session-assistant |
| 50 | `4baee567` | single-session-assistant |

By question type: knowledge-update 8; multi-session 13;
single-session-assistant 6; single-session-preference 3;
single-session-user 7; temporal-reasoning 13, for a total of 50.

---

## References

1. Maharana, A., Lee, D., Tulyakov, S., Bansal, M., Barbieri, F., and Fang, Y. "Evaluating Very Long-Term Conversational Memory of LLM Agents." arXiv:2402.17753, 2024.
2. Chhikara, P., Khant, D., Aryan, S., Singh, T., and Yadav, D. "Mem0: Building Production-Ready AI Agents with Scalable Long-Term Memory." arXiv:2504.19413, 2025.
3. Hu, C., et al. "Memory in the Age of AI Agents." arXiv:2512.13564, 2024.
4. Wu, D., Wang, H., Yu, W., Zhang, Y., Chang, K.-W., and Yu, D. "LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory." arXiv:2410.10813, 2024.
