# Memory Benchmark Data

This directory stores datasets used by the `memory/` benchmark suite.

## Supported Datasets

### LoCoMo Dataset

This directory contains the LoCoMo benchmark dataset for evaluating long-term
conversational memory.

#### Download

Download the LoCoMo dataset from the official repository:

```bash
# Clone the LoCoMo repository.
git clone https://github.com/snap-research/locomo.git

# Copy the dataset files.
cp locomo/data/locomo10/*.json ./
```

#### Dataset Format

The LoCoMo dataset contains long-term conversational data with the following
structure:

```json
{
  "sample_id": "1",
  "speakers": ["Alice", "Bob"],
  "conversation": [
    {
      "session_id": "1",
      "session_date": "2023-01-15",
      "turns": [
        {"speaker": "Alice", "text": "..."},
        {"speaker": "Bob", "text": "..."}
      ],
      "observation": "Key observations from this session...",
      "summary": "Summary of this session..."
    }
  ],
  "qa": [
    {
      "question_id": "1_1",
      "question": "What did Alice mention about...?",
      "answer": "...",
      "category": "single-hop",
      "evidence": ["1"]
    }
  ],
  "event_summary": {
    "Alice": "Summary of events for Alice...",
    "Bob": "Summary of events for Bob..."
  }
}
```

#### QA Categories

- `single-hop`: Single-hop questions answerable from one conversation segment.
- `multi-hop`: Multi-hop questions requiring multiple conversation segments.
- `temporal`: Temporal reasoning questions involving time relationships.
- `open-domain`: Open-domain questions requiring world knowledge.
- `adversarial`: Adversarial questions designed to test robustness.

### LongMemEval

Use LongMemEval to evaluate long-term memory recall over realistic multi-session user/assistant dialogues.
The cleaned `longmemeval_s_cleaned.json` dataset contains all supported question types. A full-category
manifest can select all 70 single-session-user cases, while partial development and holdout comparisons
should use seeded, stratified manifests across the intended question types.

Expected layout:

```text
data/
└── longmemeval-cleaned/
    └── longmemeval_s_cleaned.json
```

#### Download

```bash
cd benchmark/memory
mkdir -p data/longmemeval-cleaned
curl -L \
  -o data/longmemeval-cleaned/longmemeval_s_cleaned.json \
  https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json
```

#### Case Manifests

Generate LongMemEval case manifests with
`memory/trpc-agent-go-impl/cmd/longmemeval-manifest`. Full-category manifests
contain every case in each requested category. New partial manifests use an
explicit seed and stable SHA-256 ranking within each question type, so source
dataset order cannot change the selected cases. The generator supports either
explicit per-type quotas or proportional total-size allocation, as well as
disjoint development/holdout pairs.

Generated manifests record the ordered `case_ids`, selection metadata, and
SHA-256 digests needed for verification. Case-ID-only manifests are rejected
because they do not identify the dataset or selection procedure.

## References

- [LongMemEval Paper](https://arxiv.org/abs/2410.10813)
- [LongMemEval Cleaned Dataset](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned)
- [LoCoMo: Long-Context Conversational Memory Benchmark](https://arxiv.org/abs/2402.17753)
