#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Deterministic retrieval metrics and paired significance analysis."""

from __future__ import annotations

import math
import random
from collections import defaultdict
from functools import lru_cache
from statistics import mean
from typing import Any, Dict, List, Mapping, Sequence


DEFAULT_CUTOFFS = (4, 10, 20)
PRIMARY_METRIC = "all_evidence_recall_at_10"


def _attainable_ideal_dcg(
    evidence_chunks: Mapping[str, set[str]],
    cutoff: int,
) -> float:
    """Return the best evidence-novelty DCG allowed by the gold mapping."""
    evidence_bits = {
        evidence_id: 1 << index
        for index, evidence_id in enumerate(evidence_chunks)
    }
    chunk_masks: Dict[str, int] = defaultdict(int)
    for evidence_id, chunk_ids in evidence_chunks.items():
        for chunk_id in chunk_ids:
            chunk_masks[chunk_id] |= evidence_bits[evidence_id]
    candidate_masks = tuple(sorted(set(chunk_masks.values())))
    all_evidence = (1 << len(evidence_chunks)) - 1

    @lru_cache(maxsize=None)
    def best(covered: int, rank: int) -> float:
        if covered == all_evidence or rank > cutoff:
            return 0.0
        candidates = []
        for mask in candidate_masks:
            newly_covered = mask & ~covered
            if not newly_covered:
                continue
            gain = newly_covered.bit_count() / math.log2(rank + 1)
            candidates.append(gain + best(covered | mask, rank + 1))
        return max(candidates, default=0.0)

    return best(0, 1)


def score_ranking(
    case: Mapping[str, Any],
    ranking: Sequence[Mapping[str, Any]],
    cutoffs: Sequence[int] = DEFAULT_CUTOFFS,
) -> Dict[str, Any]:
    """Score one ordered ranking against a frozen evidence mapping."""
    evidence = list(case.get("evidence") or [])
    if not evidence:
        raise ValueError(f"case {case.get('case_id')} has no gold evidence")
    evidence_chunks: Dict[str, set[str]] = {}
    evidence_parents: Dict[str, str] = {}
    for record in evidence:
        evidence_id = record.get("evidence_id")
        parent_id = record.get("parent_document_id")
        chunk_ids = record.get("chunk_ids")
        if (
            not isinstance(evidence_id, str)
            or not isinstance(parent_id, str)
            or not isinstance(chunk_ids, list)
            or not chunk_ids
        ):
            raise ValueError(
                f"case {case.get('case_id')} has incomplete evidence mapping"
            )
        evidence_chunks[evidence_id] = set(chunk_ids)
        evidence_parents[evidence_id] = parent_id

    gold_parent_ids = set(evidence_parents.values())
    gold_chunk_ids = set().union(*evidence_chunks.values())
    ranked_chunk_ids = [str(record.get("chunk_id") or "") for record in ranking]
    ranked_parent_ids = [
        str(record.get("parent_document_id") or "") for record in ranking
    ]
    metrics: Dict[str, float] = {}
    hits: Dict[str, Any] = {}
    for cutoff in cutoffs:
        if cutoff <= 0:
            raise ValueError("metric cutoffs must be positive")
        top_chunks = set(ranked_chunk_ids[:cutoff])
        top_parents = set(ranked_parent_ids[:cutoff])
        hit_evidence = [
            evidence_id
            for evidence_id, chunk_ids in evidence_chunks.items()
            if chunk_ids & top_chunks
        ]
        hit_parents = sorted(gold_parent_ids & top_parents)
        metrics[f"document_recall_at_{cutoff}"] = (
            len(hit_parents) / len(gold_parent_ids)
        )
        metrics[f"evidence_recall_at_{cutoff}"] = (
            len(hit_evidence) / len(evidence_chunks)
        )
        metrics[f"all_evidence_recall_at_{cutoff}"] = float(
            len(hit_evidence) == len(evidence_chunks)
        )
        hits[f"at_{cutoff}"] = {
            "document_ids": hit_parents,
            "evidence_ids": hit_evidence,
        }

    first_relevant_rank = next(
        (
            index
            for index, chunk_id in enumerate(ranked_chunk_ids, start=1)
            if chunk_id in gold_chunk_ids
        ),
        None,
    )
    metrics["mrr"] = (
        1.0 / first_relevant_rank if first_relevant_rank is not None else 0.0
    )

    covered: set[str] = set()
    dcg = 0.0
    ndcg_cutoff = max(cutoffs)
    for rank, chunk_id in enumerate(ranked_chunk_ids[:ndcg_cutoff], start=1):
        newly_covered = {
            evidence_id
            for evidence_id, chunk_ids in evidence_chunks.items()
            if evidence_id not in covered and chunk_id in chunk_ids
        }
        if newly_covered:
            dcg += len(newly_covered) / math.log2(rank + 1)
            covered.update(newly_covered)
    ideal_dcg = _attainable_ideal_dcg(evidence_chunks, ndcg_cutoff)
    ndcg = dcg / ideal_dcg if ideal_dcg else 0.0
    if ndcg > 1.0 + 1e-12:
        raise ValueError("evidence-novelty nDCG exceeds its attainable ideal")
    metrics[f"ndcg_at_{ndcg_cutoff}"] = min(ndcg, 1.0)
    return {
        "metrics": metrics,
        "hits": hits,
        "gold_document_ids": sorted(gold_parent_ids),
        "gold_evidence_ids": list(evidence_chunks),
        "gold_chunk_ids": sorted(gold_chunk_ids),
    }


def aggregate_scores(
    samples: Sequence[Mapping[str, Any]],
) -> Dict[str, Any]:
    """Aggregate metric dictionaries overall and by question type."""
    if not samples:
        raise ValueError("cannot aggregate an empty sample set")
    metric_names = sorted(samples[0]["metrics"])
    for sample in samples:
        if sorted(sample["metrics"]) != metric_names:
            raise ValueError("samples do not share the same metrics")

    def aggregate(group: Sequence[Mapping[str, Any]]) -> Dict[str, Any]:
        return {
            "samples": len(group),
            "metrics": {
                metric: mean(
                    float(sample["metrics"][metric]) for sample in group
                )
                for metric in metric_names
            },
        }

    grouped: Dict[str, List[Mapping[str, Any]]] = defaultdict(list)
    for sample in samples:
        grouped[str(sample["question_type"])].append(sample)
    return {
        "overall": aggregate(samples),
        "by_question_type": {
            question_type: aggregate(group)
            for question_type, group in sorted(grouped.items())
        },
    }


def _percentile(values: Sequence[float], percentile: float) -> float:
    ordered = sorted(values)
    if not ordered:
        raise ValueError("cannot compute a percentile of an empty list")
    position = (len(ordered) - 1) * percentile
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    fraction = position - lower
    return ordered[lower] * (1 - fraction) + ordered[upper] * fraction


def paired_comparison(
    baseline: Sequence[Mapping[str, Any]],
    contextual: Sequence[Mapping[str, Any]],
    resamples: int = 10000,
    seed: int = 20260722,
) -> Dict[str, Any]:
    """Compute paired mean deltas and percentile bootstrap intervals."""
    if len(baseline) != len(contextual) or not baseline:
        raise ValueError("paired samples must be non-empty and equal in length")
    baseline_by_id = {sample["case_id"]: sample for sample in baseline}
    contextual_by_id = {sample["case_id"]: sample for sample in contextual}
    if list(baseline_by_id) != list(contextual_by_id):
        raise ValueError("baseline and contextual samples are not aligned")
    if resamples <= 0:
        raise ValueError("bootstrap resamples must be positive")
    case_ids = list(baseline_by_id)
    result = _paired_metric_group(
        baseline,
        contextual,
        resamples=resamples,
        seed=seed,
    )

    by_type: Dict[str, Any] = {}
    question_types = sorted(
        {str(sample["question_type"]) for sample in baseline}
    )
    for question_type in question_types:
        baseline_group = [
            sample
            for sample in baseline
            if sample["question_type"] == question_type
        ]
        contextual_group = [
            sample
            for sample in contextual
            if sample["question_type"] == question_type
        ]
        by_type[question_type] = _paired_metric_group(
            baseline_group,
            contextual_group,
            resamples=resamples,
            seed=seed + len(by_type) + 1,
        )
    return {
        "samples": len(case_ids),
        "bootstrap": {
            "method": "paired_percentile",
            "resamples": resamples,
            "seed": seed,
        },
        "overall": result,
        "by_question_type": by_type,
    }


def _paired_metric_group(
    baseline: Sequence[Mapping[str, Any]],
    contextual: Sequence[Mapping[str, Any]],
    resamples: int,
    seed: int,
) -> Dict[str, Any]:
    metric_names = sorted(baseline[0]["metrics"])
    case_ids = [str(sample["case_id"]) for sample in baseline]
    baseline_by_id = {str(sample["case_id"]): sample for sample in baseline}
    contextual_by_id = {
        str(sample["case_id"]): sample for sample in contextual
    }
    rng = random.Random(seed)
    result: Dict[str, Any] = {}
    for metric in metric_names:
        baseline_values = [
            float(baseline_by_id[case_id]["metrics"][metric])
            for case_id in case_ids
        ]
        contextual_values = [
            float(contextual_by_id[case_id]["metrics"][metric])
            for case_id in case_ids
        ]
        deltas = [
            contextual_value - baseline_value
            for baseline_value, contextual_value in zip(
                baseline_values,
                contextual_values,
            )
        ]
        bootstrap = []
        for _ in range(resamples):
            indices = [rng.randrange(len(deltas)) for _ in deltas]
            bootstrap.append(mean(deltas[index] for index in indices))
        result[metric] = {
            "baseline": mean(baseline_values),
            "contextual": mean(contextual_values),
            "delta": mean(deltas),
            "ci_95": [
                _percentile(bootstrap, 0.025),
                _percentile(bootstrap, 0.975),
            ],
        }

    return result


def evaluate_gate(
    comparison: Mapping[str, Any],
    evidence_complete: bool,
    runtime_errors: int,
) -> Dict[str, Any]:
    """Apply the pre-registered I1 acceptance gate."""
    overall = comparison["overall"]
    by_type = comparison["by_question_type"]
    checks = {
        "evidence_and_runtime_complete": evidence_complete
        and runtime_errors == 0,
        "all_evidence_recall_at_10_delta": (
            overall[PRIMARY_METRIC]["delta"] >= 0.05
        ),
        "all_evidence_recall_at_10_ci": (
            overall[PRIMARY_METRIC]["ci_95"][0] > 0
        ),
        "evidence_recall_at_10_delta": (
            overall["evidence_recall_at_10"]["delta"] >= 0.03
        ),
        "evidence_recall_at_10_ci": (
            overall["evidence_recall_at_10"]["ci_95"][0] > 0
        ),
        "document_recall_at_4_non_regression": (
            overall["document_recall_at_4"]["delta"] >= -0.01
        ),
        "evidence_recall_at_4_non_regression": (
            overall["evidence_recall_at_4"]["delta"] >= -0.01
        ),
        "question_type_consistency": (
            sum(
                lane[PRIMARY_METRIC]["delta"] > 0
                for lane in by_type.values()
            )
            >= 2
            and all(
                lane[PRIMARY_METRIC]["delta"] >= -0.02
                for lane in by_type.values()
            )
        ),
    }
    passed = all(checks.values())
    return {
        "decision": "pass" if passed else "fail",
        "checks": checks,
        "primary_metric": PRIMARY_METRIC,
    }


def evaluate_smoke_promotion(
    comparison: Mapping[str, Any],
    evidence_complete: bool,
    runtime_errors: int,
    failed_attempts: int,
) -> Dict[str, Any]:
    """Decide whether a complete smoke has enough signal to justify scale-up."""
    overall = comparison["overall"]
    by_type = comparison["by_question_type"]
    operational_checks = {
        "paired_evidence_complete": evidence_complete
        and runtime_errors == 0,
        "zero_failed_request_attempts": failed_attempts == 0,
    }
    signal_checks = {
        "directional_recall_at_10_signal": max(
            overall[PRIMARY_METRIC]["delta"],
            overall["evidence_recall_at_10"]["delta"],
        )
        >= 0.01,
        "document_recall_at_4_not_materially_worse": (
            overall["document_recall_at_4"]["delta"] >= -0.05
        ),
        "evidence_recall_at_4_not_materially_worse": (
            overall["evidence_recall_at_4"]["delta"] >= -0.05
        ),
        "no_question_type_primary_collapse": all(
            lane[PRIMARY_METRIC]["delta"] >= -0.10
            for lane in by_type.values()
        ),
    }
    checks = {**operational_checks, **signal_checks}
    if not all(operational_checks.values()):
        decision = "insufficient"
    elif all(signal_checks.values()):
        decision = "promote"
    else:
        decision = "stop"
    return {
        "decision": decision,
        "checks": checks,
        "primary_metric": PRIMARY_METRIC,
        "scope": "scale_up_only",
        "formal_method_conclusion": False,
    }
