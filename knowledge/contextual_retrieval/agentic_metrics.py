#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Statistics and the pre-registered gate for the Agentic I2 experiment."""

from __future__ import annotations

import math
import random
from collections import defaultdict
from statistics import mean
from typing import Any, Dict, List, Mapping, Sequence


I2_METRICS = (
    "faithfulness",
    "answer_relevancy",
    "answer_correctness",
    "answer_similarity",
    "context_precision",
    "context_recall",
    "context_entity_recall",
)
I2_PRIMARY_METRIC = "answer_correctness"


def _finite_number(value: Any) -> bool:
    return (
        isinstance(value, (int, float))
        and not isinstance(value, bool)
        and math.isfinite(float(value))
    )


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


def average_repeats_by_case(
    records: Sequence[Mapping[str, Any]],
    repeats: int,
) -> List[Dict[str, Any]]:
    """Average repeats before inference so repeats are not pseudo-samples."""
    if repeats <= 0:
        raise ValueError("repeats must be positive")
    grouped: Dict[tuple[str, str], List[Mapping[str, Any]]] = defaultdict(list)
    for record in records:
        case_id = str(record.get("case_id") or "")
        lane = str(record.get("lane") or "")
        if not case_id or lane not in ("baseline", "contextual"):
            raise ValueError("scored record has an invalid case or lane")
        grouped[(case_id, lane)].append(record)

    averaged = []
    for (case_id, lane), group in sorted(grouped.items()):
        repeat_ids = {int(record["repeat"]) for record in group}
        if len(group) != repeats or repeat_ids != set(range(repeats)):
            raise ValueError(
                f"case {case_id} lane {lane} does not have {repeats} repeats"
            )
        question_types = {str(record["question_type"]) for record in group}
        if len(question_types) != 1:
            raise ValueError(f"case {case_id} question type changed by repeat")
        for record in group:
            metrics = record.get("metrics")
            if not isinstance(metrics, Mapping):
                raise ValueError(f"case {case_id} has missing Judge metrics")
            missing = [
                metric
                for metric in I2_METRICS
                if not _finite_number(metrics.get(metric))
            ]
            if missing:
                raise ValueError(
                    f"case {case_id} has non-finite Judge metrics: {missing}"
                )
        averaged.append(
            {
                "case_id": case_id,
                "lane": lane,
                "question_type": next(iter(question_types)),
                "metrics": {
                    metric: mean(
                        float(record["metrics"][metric]) for record in group
                    )
                    for metric in I2_METRICS
                },
            }
        )
    return averaged


def _paired_rows(
    averaged: Sequence[Mapping[str, Any]],
) -> List[Dict[str, Any]]:
    by_case: Dict[str, Dict[str, Mapping[str, Any]]] = defaultdict(dict)
    for record in averaged:
        by_case[str(record["case_id"])][str(record["lane"])] = record
    paired = []
    for case_id, lanes in sorted(by_case.items()):
        if set(lanes) != {"baseline", "contextual"}:
            raise ValueError(f"case {case_id} is not paired")
        baseline = lanes["baseline"]
        contextual = lanes["contextual"]
        if baseline["question_type"] != contextual["question_type"]:
            raise ValueError(f"case {case_id} question type differs by lane")
        paired.append(
            {
                "case_id": case_id,
                "question_type": baseline["question_type"],
                "baseline": baseline["metrics"],
                "contextual": contextual["metrics"],
            }
        )
    if not paired:
        raise ValueError("no paired I2 scores")
    return paired


def _metric_summary(
    rows: Sequence[Mapping[str, Any]],
    bootstrap_indices: Sequence[Sequence[int]],
) -> Dict[str, Any]:
    result: Dict[str, Any] = {}
    for metric in I2_METRICS:
        baseline = [float(row["baseline"][metric]) for row in rows]
        contextual = [float(row["contextual"][metric]) for row in rows]
        deltas = [b_value - a_value for a_value, b_value in zip(baseline, contextual)]
        bootstrap = [
            mean(deltas[index] for index in indices)
            for indices in bootstrap_indices
        ]
        result[metric] = {
            "baseline": mean(baseline),
            "contextual": mean(contextual),
            "delta": mean(deltas),
            "ci_95": [
                _percentile(bootstrap, 0.025),
                _percentile(bootstrap, 0.975),
            ],
        }
    return result


def stratified_paired_comparison(
    averaged: Sequence[Mapping[str, Any]],
    resamples: int = 10000,
    seed: int = 20260725,
) -> Dict[str, Any]:
    """Bootstrap paired case deltas, sampling within question type."""
    if resamples <= 0:
        raise ValueError("bootstrap resamples must be positive")
    paired = _paired_rows(averaged)
    groups: Dict[str, List[int]] = defaultdict(list)
    for index, row in enumerate(paired):
        groups[str(row["question_type"])].append(index)
    rng = random.Random(seed)
    stratified_indices = []
    for _ in range(resamples):
        draw = []
        for question_type in sorted(groups):
            indices = groups[question_type]
            draw.extend(rng.choice(indices) for _ in indices)
        stratified_indices.append(draw)

    by_type: Dict[str, Any] = {}
    for type_offset, question_type in enumerate(sorted(groups), start=1):
        indices = groups[question_type]
        rows = [paired[index] for index in indices]
        type_rng = random.Random(seed + type_offset)
        draws = [
            [type_rng.randrange(len(rows)) for _ in rows]
            for _ in range(resamples)
        ]
        by_type[question_type] = _metric_summary(rows, draws)
    return {
        "samples": len(paired),
        "bootstrap": {
            "method": "question_type_stratified_paired_percentile",
            "resamples": resamples,
            "seed": seed,
        },
        "overall": _metric_summary(paired, stratified_indices),
        "by_question_type": by_type,
    }


def evaluate_i2_gate(
    comparison: Mapping[str, Any] | None,
    evidence_complete: bool,
    baseline_agent_failures: int,
    contextual_agent_failures: int,
    executions_per_lane: int,
) -> Dict[str, Any]:
    """Apply the accepted I2 primary threshold and operational guardrails."""
    if executions_per_lane <= 0:
        raise ValueError("executions_per_lane must be positive")
    baseline_rate = baseline_agent_failures / executions_per_lane
    contextual_rate = contextual_agent_failures / executions_per_lane
    failure_delta = contextual_rate - baseline_rate
    if comparison is None:
        return {
            "decision": "insufficient",
            "primary_metric": I2_PRIMARY_METRIC,
            "checks": {"complete_judge_evidence": False},
            "agent_failure_rates": {
                "baseline": baseline_rate,
                "contextual": contextual_rate,
                "delta": failure_delta,
            },
        }
    overall = comparison["overall"]
    primary = overall[I2_PRIMARY_METRIC]
    context_precision = overall["context_precision"]
    checks = {
        "complete_judge_evidence": evidence_complete,
        "answer_correctness_delta_at_least_0_02": primary["delta"] >= 0.02,
        "answer_correctness_ci_lower_above_zero": primary["ci_95"][0] > 0,
        "context_precision_delta_at_least_minus_0_01": (
            context_precision["delta"] >= -0.01
        ),
        "agent_failure_delta_at_most_0_01": failure_delta <= 0.01,
    }
    if not evidence_complete:
        decision = "insufficient"
    else:
        decision = (
            "method_effective"
            if all(checks.values())
            else "method_not_effective"
        )
    return {
        "decision": decision,
        "primary_metric": I2_PRIMARY_METRIC,
        "checks": checks,
        "agent_failure_rates": {
            "baseline": baseline_rate,
            "contextual": contextual_rate,
            "delta": failure_delta,
        },
    }
