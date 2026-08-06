#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Offline rescoring for frozen retrieval rankings."""

from __future__ import annotations

from typing import Any, Dict, Mapping, Optional

from contextual_retrieval import RETRIEVAL_EVIDENCE_SCOPE
from contextual_retrieval.artifacts import load_artifact, write_artifact
from contextual_retrieval.dataset import CASE_SCHEMA
from contextual_retrieval.metrics import (
    aggregate_scores,
    evaluate_gate,
    evaluate_smoke_promotion,
    paired_comparison,
    score_ranking,
)
from contextual_retrieval.runner import (
    RETRIEVAL_MANIFEST_SCHEMA,
    RETRIEVAL_SAMPLES_SCHEMA,
)


RETRIEVAL_RESCORE_SCHEMA = "contextual-retrieval/retrieval-rescore/v1"
RETRIEVAL_SCORER_REVISION = "evidence-novelty-ndcg-attainable-idcg/v1"


def _bootstrap_setting(
    manifest: Mapping[str, Any],
    name: str,
    override: Optional[int],
) -> int:
    value = override
    if value is None:
        bootstrap = manifest.get("bootstrap")
        if not isinstance(bootstrap, Mapping):
            raise ValueError("source manifest is missing bootstrap settings")
        value = bootstrap.get(name)
    minimum = 0 if name == "seed" else 1
    if (
        not isinstance(value, int)
        or isinstance(value, bool)
        or value < minimum
    ):
        qualifier = "non-negative" if minimum == 0 else "positive"
        raise ValueError(f"bootstrap {name} must be a {qualifier} integer")
    return value


def _changed_metrics(
    source: Mapping[str, Any],
    rescored: Mapping[str, Any],
) -> Dict[str, int]:
    if set(source) != set(rescored):
        raise ValueError("source and rescored metric sets do not match")
    changes: Dict[str, int] = {}
    for metric, value in rescored.items():
        source_value = source.get(metric)
        changed = (
            not isinstance(source_value, (int, float))
            or abs(float(source_value) - float(value)) > 1e-12
        )
        changes[metric] = int(changed)
    return changes


def rescore_retrieval_samples(
    cases_path: str,
    source_manifest_path: str,
    source_samples_path: str,
    output_path: str,
    bootstrap_resamples: Optional[int] = None,
    bootstrap_seed: Optional[int] = None,
    allow_case_manifest_change: bool = False,
) -> Dict[str, Any]:
    """Rescore frozen A/B rankings without contacting retrieval services."""
    cases_artifact = load_artifact(cases_path, CASE_SCHEMA)
    manifest = load_artifact(
        source_manifest_path,
        RETRIEVAL_MANIFEST_SCHEMA,
    )
    samples_artifact = load_artifact(
        source_samples_path,
        RETRIEVAL_SAMPLES_SCHEMA,
    )
    source_case_digest = manifest.get("case_manifest_digest")
    rescored_case_digest = cases_artifact["artifact_digest"]
    case_manifest_changed = source_case_digest != rescored_case_digest
    if case_manifest_changed and not allow_case_manifest_change:
        raise ValueError(
            "case manifest differs from the source run; pass "
            "allow_case_manifest_change only for an audited mapping correction"
        )
    if samples_artifact.get("manifest_digest") != manifest["artifact_digest"]:
        raise ValueError("source samples do not belong to the source manifest")
    if samples_artifact.get("run_identity") != manifest.get("run_identity"):
        raise ValueError("source samples and manifest have different run identities")
    bootstrap = manifest.get("bootstrap")
    if (
        not isinstance(bootstrap, Mapping)
        or bootstrap.get("method") != "paired_percentile"
    ):
        raise ValueError("source manifest has an unsupported bootstrap method")

    samples = list(samples_artifact.get("samples") or [])
    expected_cases = samples_artifact.get("expected_cases")
    if (
        not isinstance(expected_cases, int)
        or expected_cases <= 0
        or samples_artifact.get("completed_cases") != len(samples)
        or expected_cases != len(samples)
        or manifest.get("expected_cases") != expected_cases
    ):
        raise ValueError("source retrieval samples are incomplete")
    cases_by_id = {
        str(case["case_id"]): case for case in cases_artifact["cases"]
    }
    if len(cases_by_id) != cases_artifact.get("cases_count"):
        raise ValueError("case artifact contains duplicate case IDs")

    baseline = []
    contextual = []
    change_counts: Dict[str, Dict[str, int]] = {
        "baseline": {},
        "contextual": {},
    }
    failed_attempts = 0
    seen_case_ids = set()
    for sample in samples:
        case_id = str(sample.get("case_id") or "")
        if not case_id or case_id in seen_case_ids or case_id not in cases_by_id:
            raise ValueError("source samples contain invalid or duplicate case IDs")
        seen_case_ids.add(case_id)
        case = cases_by_id[case_id]
        question_type = str(sample.get("question_type") or "")
        if question_type != str(case.get("question_type") or ""):
            raise ValueError(f"case {case_id} has a question-type mismatch")
        for lane, destination in (
            ("baseline", baseline),
            ("contextual", contextual),
        ):
            source_lane = sample.get(lane)
            if (
                not isinstance(source_lane, Mapping)
                or source_lane.get("status") != "success"
                or not isinstance(source_lane.get("ranking"), list)
                or not isinstance(source_lane.get("metrics"), Mapping)
                or not isinstance(source_lane.get("attempts"), list)
            ):
                raise ValueError(
                    f"case {case_id} lane {lane} is not a successful frozen ranking"
                )
            failed_attempts += sum(
                not isinstance(attempt, Mapping)
                or attempt.get("status") != "success"
                for attempt in source_lane["attempts"]
            )
            rescored = score_ranking(case, source_lane["ranking"])
            destination.append(
                {
                    "case_id": case_id,
                    "question_type": question_type,
                    "metrics": rescored["metrics"],
                }
            )
            for metric, changed in _changed_metrics(
                source_lane["metrics"],
                rescored["metrics"],
            ).items():
                change_counts[lane][metric] = (
                    change_counts[lane].get(metric, 0) + changed
                )

    resamples = _bootstrap_setting(
        manifest,
        "resamples",
        bootstrap_resamples,
    )
    seed = _bootstrap_setting(manifest, "seed", bootstrap_seed)
    comparison = paired_comparison(
        baseline,
        contextual,
        resamples=resamples,
        seed=seed,
    )
    aggregates = {
        "baseline": aggregate_scores(baseline),
        "contextual": aggregate_scores(contextual),
    }
    is_formal = (
        manifest.get("evidence_scope") == RETRIEVAL_EVIDENCE_SCOPE
        and expected_cases == cases_artifact.get("cases_count")
        and failed_attempts == 0
    )
    selection = manifest.get("selection")
    is_promotion_smoke = (
        isinstance(selection, Mapping)
        and selection.get("smoke_per_type") is not None
    )
    smoke_promotion = (
        evaluate_smoke_promotion(
            comparison,
            evidence_complete=True,
            runtime_errors=0,
            failed_attempts=failed_attempts,
        )
        if is_promotion_smoke
        else None
    )
    gate = evaluate_gate(
        comparison,
        evidence_complete=is_formal,
        runtime_errors=0,
    )
    if not is_formal:
        gate["decision"] = "insufficient"

    return write_artifact(
        output_path,
        {
            "schema_version": RETRIEVAL_RESCORE_SCHEMA,
            "scorer_revision": RETRIEVAL_SCORER_REVISION,
            "source_manifest_digest": manifest["artifact_digest"],
            "source_samples_digest": samples_artifact["artifact_digest"],
            "source_run_identity": manifest["run_identity"],
            "source_case_manifest_digest": source_case_digest,
            "rescored_case_manifest_digest": rescored_case_digest,
            "case_manifest_changed": case_manifest_changed,
            "evidence_scope": manifest.get("evidence_scope"),
            "expected_cases": expected_cases,
            "paired_valid_cases": len(baseline),
            "failed_request_attempts": failed_attempts,
            "evidence_status": (
                "valid"
                if is_formal or (is_promotion_smoke and failed_attempts == 0)
                else "insufficient"
            ),
            "formal_ab_eligible": is_formal,
            "bootstrap": {
                "method": "paired_percentile",
                "resamples": resamples,
                "seed": seed,
            },
            "changed_samples_by_metric": change_counts,
            "aggregates": aggregates,
            "comparison": comparison,
            "smoke_promotion": smoke_promotion,
            "gate": gate,
        },
    )
