#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Strict retrieval-only A/B runner for Contextual Embedding."""

from __future__ import annotations

import json as json_module
import math
import os
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple
from urllib.request import Request, urlopen

from contextual_retrieval import (
    RETRIEVAL_EVIDENCE_SCOPE,
    RETRIEVAL_RUN_KIND,
)
from contextual_retrieval.artifacts import (
    canonical_digest,
    load_artifact,
    public_endpoint_identity,
    text_digest,
    write_artifact,
)
from contextual_retrieval.dataset import (
    CASE_SCHEMA,
    CHUNK_SCHEMA,
    DEFAULT_QUESTION_TYPES,
)
from contextual_retrieval.metrics import (
    aggregate_scores,
    evaluate_gate,
    evaluate_smoke_promotion,
    paired_comparison,
    score_ranking,
)


RETRIEVAL_MANIFEST_SCHEMA = "contextual-retrieval/run-manifest/v1"
RETRIEVAL_SAMPLES_SCHEMA = "contextual-retrieval/run-samples/v1"
RETRIEVAL_REPORT_SCHEMA = "contextual-retrieval/run-report/v1"
EXPERIMENT_META_PREFIX = "contextual_retrieval_"
FORMAL_SEARCH_K = 20
PUBLIC_RUNTIME_CONFIG_FIELDS = (
    "embedding_model",
    "embedding_endpoint",
    "embedding_dimensions",
    "embedding_header_names",
    "vectorstore",
    "search_mode",
    "use_rrf",
    "hybrid_vector_weight",
    "hybrid_text_weight",
    "chunk_size",
    "chunk_overlap",
    "pg_table",
    "framework_module",
    "index_variant",
    "chunk_manifest_digest",
    "parent_manifest_digest",
    "manifest_chunks_count",
    "context_cache_identity",
    "context_set_digest",
    "index_document_count",
)
IGNORED_RUNTIME_CONFIG_FIELDS = frozenset(
    (
        "model_name",
        "pg_connection",
        "llm_endpoint",
        "agent_search_mode_enforced",
        "agent_search_mode_effective",
        "tool_argument_policy",
        "max_argument_repairs",
        "silent_argument_rewrite",
        "provider_strict",
        "llm_header_names",
        "index_document_count_error",
    )
)


class _UrllibResponse:
    def __init__(self, status: int, body: bytes):
        self.status = status
        self.body = body

    def raise_for_status(self) -> None:
        if self.status < 200 or self.status >= 300:
            raise RuntimeError(f"HTTP status {self.status}")

    def json(self) -> Any:
        return json_module.loads(self.body.decode("utf-8"))


class _UrllibSession:
    """Small requests-compatible fallback for minimal benchmark hosts."""

    def get(self, url: str, timeout: float) -> _UrllibResponse:
        request = Request(url, method="GET")
        with urlopen(request, timeout=timeout) as response:
            return _UrllibResponse(response.status, response.read())

    def post(
        self,
        url: str,
        json: Mapping[str, Any],
        timeout: float,
    ) -> _UrllibResponse:
        request = Request(
            url,
            data=json_module.dumps(json).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urlopen(request, timeout=timeout) as response:
            return _UrllibResponse(response.status, response.read())


def _output_paths(output_path: str) -> Dict[str, str]:
    target = Path(output_path)
    stem = target.with_suffix("") if target.suffix else target
    return {
        "report": str(target),
        "manifest": str(stem) + ".manifest.json",
        "samples": str(stem) + ".samples.json",
    }


def _get_json(
    session: Any,
    url: str,
    timeout: float,
) -> Dict[str, Any]:
    try:
        endpoint = public_endpoint_identity(url)
    except ValueError:
        endpoint = "invalid_endpoint"
    try:
        response = session.get(url, timeout=timeout)
        response.raise_for_status()
        payload = response.json()
        if not isinstance(payload, dict):
            raise ValueError("response was not a JSON object")
        return payload
    except Exception as error:
        raise RuntimeError(
            f"{type(error).__name__} while requesting {endpoint}"
        ) from None


def _public_runtime_config(config: Mapping[str, Any]) -> Dict[str, Any]:
    unknown_fields = sorted(
        set(config)
        - set(PUBLIC_RUNTIME_CONFIG_FIELDS)
        - IGNORED_RUNTIME_CONFIG_FIELDS
    )
    if unknown_fields:
        raise ValueError(
            "service config contains unsupported fields: "
            + ", ".join(unknown_fields)
        )
    public = {
        field: config.get(field) for field in PUBLIC_RUNTIME_CONFIG_FIELDS
    }
    public["embedding_endpoint"] = public_endpoint_identity(
        str(config.get("embedding_endpoint") or "")
    )
    return public


def validate_service_pair(
    baseline: Mapping[str, Any],
    contextual: Mapping[str, Any],
    chunks: Mapping[str, Any],
) -> None:
    """Reject any A/B service pair with a confounded effective configuration."""
    errors: List[str] = []
    if baseline.get("index_variant") != "baseline":
        errors.append("baseline service index_variant must be baseline")
    if contextual.get("index_variant") != "contextual":
        errors.append("contextual service index_variant must be contextual")
    if baseline.get("search_mode") != 1 or contextual.get("search_mode") != 1:
        errors.append("both services must use vector search_mode=1")
    if (
        baseline.get("vectorstore") != "pgvector"
        or contextual.get("vectorstore") != "pgvector"
    ):
        errors.append("both services must use pgvector")
    if baseline.get("pg_table") == contextual.get("pg_table"):
        errors.append(
            "baseline and contextual services must use different PG tables"
        )
    same_fields = (
        "embedding_model",
        "embedding_endpoint",
        "embedding_dimensions",
        "embedding_header_names",
        "vectorstore",
        "search_mode",
        "use_rrf",
        "hybrid_vector_weight",
        "hybrid_text_weight",
        "chunk_size",
        "chunk_overlap",
        "framework_module",
        "chunk_manifest_digest",
        "parent_manifest_digest",
        "manifest_chunks_count",
    )
    for field in same_fields:
        if baseline.get(field) != contextual.get(field):
            errors.append(f"service field {field} differs between A and B")
    expected_digest = chunks.get("artifact_digest")
    if baseline.get("chunk_manifest_digest") != expected_digest:
        errors.append("services do not use the requested chunk manifest")
    expected_count = chunks.get("chunks_count")
    for lane_name, config in (("baseline", baseline), ("contextual", contextual)):
        if config.get("manifest_chunks_count") != expected_count:
            errors.append(f"{lane_name} manifest chunk count is incorrect")
        if config.get("index_document_count") != expected_count:
            errors.append(f"{lane_name} index document count is incorrect")
    if baseline.get("context_cache_identity") not in (None, ""):
        errors.append("baseline service unexpectedly has a context cache")
    if not contextual.get("context_cache_identity"):
        errors.append("contextual service context cache identity is missing")
    if baseline.get("context_set_digest") not in (None, ""):
        errors.append("baseline service unexpectedly has a Context set")
    if not contextual.get("context_set_digest"):
        errors.append("contextual service Context set digest is missing")
    if errors:
        raise ValueError("invalid retrieval A/B services: " + "; ".join(errors))


def _search(
    session: Any,
    service_url: str,
    query: str,
    timeout: float,
    max_attempts: int,
) -> Tuple[Optional[Dict[str, Any]], List[Dict[str, Any]]]:
    attempts = []
    for attempt in range(1, max_attempts + 1):
        started = time.monotonic()
        try:
            response = session.post(
                service_url.rstrip("/") + "/search",
                json={"query": query, "k": FORMAL_SEARCH_K},
                timeout=timeout,
            )
            response.raise_for_status()
            payload = response.json()
            if not isinstance(payload, dict) or not isinstance(
                payload.get("documents"),
                list,
            ):
                raise ValueError("search response has no document list")
            attempts.append(
                {
                    "attempt": attempt,
                    "status": "success",
                    "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
                    "error": None,
                }
            )
            return payload, attempts
        except (OSError, RuntimeError, ValueError) as error:
            attempts.append(
                {
                    "attempt": attempt,
                    "status": "error",
                    "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
                    "error": f"{type(error).__name__}: request failed",
                }
            )
    return None, attempts


def _normalize_ranking(
    payload: Mapping[str, Any],
    chunks_by_id: Mapping[str, Mapping[str, Any]],
) -> List[Dict[str, Any]]:
    ranking = []
    seen: set[str] = set()
    for rank, raw in enumerate(payload["documents"], start=1):
        if not isinstance(raw, dict):
            raise ValueError(f"search document at rank {rank} is not an object")
        metadata = raw.get("metadata") or {}
        if not isinstance(metadata, dict):
            raise ValueError(f"search document at rank {rank} has invalid metadata")
        chunk_id = metadata.get(EXPERIMENT_META_PREFIX + "chunk_id")
        parent_id = metadata.get(
            EXPERIMENT_META_PREFIX + "parent_document_id"
        )
        if not isinstance(chunk_id, str) or chunk_id not in chunks_by_id:
            raise ValueError(f"search document at rank {rank} has unknown chunk ID")
        expected = chunks_by_id[chunk_id]
        if parent_id != expected["parent_document_id"]:
            raise ValueError(f"search document at rank {rank} has wrong parent ID")
        if chunk_id in seen:
            raise ValueError(f"search ranking contains duplicate chunk {chunk_id}")
        seen.add(chunk_id)
        text = raw.get("text")
        if (
            not isinstance(text, str)
            or text_digest(text) != expected["chunk_content_hash"]
        ):
            raise ValueError(f"search document at rank {rank} content does not match")
        score = raw.get("score")
        if not isinstance(score, (int, float)) or not math.isfinite(float(score)):
            raise ValueError(f"search document at rank {rank} has invalid score")
        ranking.append(
            {
                "rank": rank,
                "chunk_id": chunk_id,
                "parent_document_id": parent_id,
                "score": float(score),
                "chunk_content_hash": expected["chunk_content_hash"],
            }
        )
    if len(ranking) != FORMAL_SEARCH_K:
        raise ValueError(
            f"search returned {len(ranking)} documents, expected {FORMAL_SEARCH_K}"
        )
    return ranking


def _lane_result(
    case: Mapping[str, Any],
    payload: Optional[Mapping[str, Any]],
    attempts: Sequence[Mapping[str, Any]],
    chunks_by_id: Mapping[str, Mapping[str, Any]],
) -> Dict[str, Any]:
    if payload is None:
        return {
            "status": "error",
            "attempts": list(attempts),
            "ranking": None,
            "metrics": None,
            "hits": None,
        }
    try:
        ranking = _normalize_ranking(payload, chunks_by_id)
        score = score_ranking(case, ranking)
        return {
            "status": "success",
            "attempts": list(attempts),
            "ranking": ranking,
            **score,
        }
    except ValueError as error:
        return {
            "status": "error",
            "attempts": [
                *attempts,
                {
                    "attempt": len(attempts) + 1,
                    "status": "validation_error",
                    "elapsed_ms": 0,
                    "error": str(error),
                },
            ],
            "ranking": None,
            "metrics": None,
            "hits": None,
        }


def _metric_view(
    samples: Sequence[Mapping[str, Any]],
    lane: str,
) -> List[Dict[str, Any]]:
    return [
        {
            "case_id": sample["case_id"],
            "question_type": sample["question_type"],
            "metrics": sample[lane]["metrics"],
        }
        for sample in samples
        if sample["baseline"]["status"] == "success"
        and sample["contextual"]["status"] == "success"
    ]


def _runtime_stats(
    samples: Sequence[Mapping[str, Any]],
    lane: str,
) -> Dict[str, Any]:
    latencies = [
        sum(
            float(attempt.get("elapsed_ms") or 0)
            for attempt in sample[lane]["attempts"]
        )
        for sample in samples
    ]
    ordered = sorted(latencies)
    p95_index = max(0, math.ceil(len(ordered) * 0.95) - 1)
    return {
        "queries": len(samples),
        "average_ms": sum(latencies) / len(latencies) if latencies else None,
        "p95_ms": ordered[p95_index] if ordered else None,
        "retried_queries": sum(
            len(sample[lane]["attempts"]) > 1 for sample in samples
        ),
        "failed_attempts": sum(
            attempt.get("status") != "success"
            for sample in samples
            for attempt in sample[lane]["attempts"]
        ),
    }


def run_retrieval_ab(
    cases_path: str,
    chunks_path: str,
    baseline_url: str,
    contextual_url: str,
    output_path: str,
    timeout: float = 120,
    request_attempts: int = 3,
    bootstrap_resamples: int = 10000,
    bootstrap_seed: int = 20260722,
    limit: Optional[int] = None,
    smoke_per_type: Optional[int] = None,
    http_session: Any = None,
) -> Dict[str, Any]:
    """Run paired retrieval requests and write resumable evidence artifacts."""
    cases_artifact = load_artifact(cases_path, CASE_SCHEMA)
    chunks_artifact = load_artifact(chunks_path, CHUNK_SCHEMA)
    if cases_artifact.get("chunk_manifest_digest") != chunks_artifact.get(
        "artifact_digest"
    ):
        raise ValueError("case and chunk manifests do not match")
    cases = list(cases_artifact["cases"])
    if limit is not None and smoke_per_type is not None:
        raise ValueError("limit and smoke_per_type cannot be used together")
    if limit is not None:
        if limit <= 0:
            raise ValueError("limit must be positive")
        cases = cases[:limit]
    if smoke_per_type is not None:
        if smoke_per_type <= 0:
            raise ValueError("smoke_per_type must be positive")
        selected = []
        counts: Dict[str, int] = {}
        for case in cases:
            question_type = str(case["question_type"])
            if question_type not in DEFAULT_QUESTION_TYPES:
                continue
            if counts.get(question_type, 0) < smoke_per_type:
                selected.append(case)
                counts[question_type] = counts.get(question_type, 0) + 1
        incomplete_types = {
            question_type: counts.get(question_type, 0)
            for question_type in DEFAULT_QUESTION_TYPES
            if counts.get(question_type, 0) != smoke_per_type
        }
        if incomplete_types:
            raise ValueError(
                "smoke selection is incomplete by question type: "
                f"{incomplete_types}"
            )
        cases = selected
    if not cases:
        raise ValueError("no cases selected")
    if timeout <= 0 or request_attempts <= 0:
        raise ValueError("timeout and request_attempts must be positive")

    session = http_session
    if session is None:
        try:
            import requests

            session = requests.Session()
        except ModuleNotFoundError:
            session = _UrllibSession()
    baseline_config = _get_json(
        session,
        baseline_url.rstrip("/") + "/config",
        min(timeout, 30),
    )
    contextual_config = _get_json(
        session,
        contextual_url.rstrip("/") + "/config",
        min(timeout, 30),
    )
    validate_service_pair(baseline_config, contextual_config, chunks_artifact)
    public_baseline = _public_runtime_config(baseline_config)
    public_contextual = _public_runtime_config(contextual_config)
    run_identity_payload = {
        "run_kind": RETRIEVAL_RUN_KIND,
        "case_manifest_digest": cases_artifact["artifact_digest"],
        "chunk_manifest_digest": chunks_artifact["artifact_digest"],
        "case_ids": [case["case_id"] for case in cases],
        "baseline_url": public_endpoint_identity(baseline_url),
        "contextual_url": public_endpoint_identity(contextual_url),
        "baseline_config": public_baseline,
        "contextual_config": public_contextual,
        "search_k": FORMAL_SEARCH_K,
        "request_attempts": request_attempts,
        "timeout_seconds": timeout,
        "request_order": "alternating_baseline_first_on_even_cases",
        "bootstrap_resamples": bootstrap_resamples,
        "bootstrap_seed": bootstrap_seed,
        "selection": {
            "limit": limit,
            "smoke_per_type": smoke_per_type,
        },
    }
    run_identity = canonical_digest(run_identity_payload)
    paths = _output_paths(output_path)
    manifest_payload = {
        "schema_version": RETRIEVAL_MANIFEST_SCHEMA,
        "run_kind": RETRIEVAL_RUN_KIND,
        "evidence_scope": (
            RETRIEVAL_EVIDENCE_SCOPE
            if limit is None and smoke_per_type is None
            else "retrieval_smoke"
        ),
        "run_identity": run_identity,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "case_manifest_digest": cases_artifact["artifact_digest"],
        "chunk_manifest_digest": chunks_artifact["artifact_digest"],
        "expected_cases": len(cases),
        "search_k": FORMAL_SEARCH_K,
        "request_attempts": request_attempts,
        "timeout_seconds": timeout,
        "request_order": "alternating_baseline_first_on_even_cases",
        "baseline_url": public_endpoint_identity(baseline_url),
        "contextual_url": public_endpoint_identity(contextual_url),
        "baseline_config": public_baseline,
        "contextual_config": public_contextual,
        "bootstrap": {
            "method": "paired_percentile",
            "resamples": bootstrap_resamples,
            "seed": bootstrap_seed,
        },
        "selection": {
            "limit": limit,
            "smoke_per_type": smoke_per_type,
        },
    }
    if os.path.exists(paths["manifest"]):
        manifest = load_artifact(
            paths["manifest"],
            RETRIEVAL_MANIFEST_SCHEMA,
        )
        if manifest.get("run_identity") != run_identity:
            raise ValueError("existing retrieval manifest belongs to another run")
    else:
        manifest = write_artifact(paths["manifest"], manifest_payload)

    completed: Dict[str, Dict[str, Any]] = {}
    if os.path.exists(paths["samples"]):
        checkpoint = load_artifact(paths["samples"], RETRIEVAL_SAMPLES_SCHEMA)
        if checkpoint.get("run_identity") != run_identity:
            raise ValueError("existing retrieval checkpoint belongs to another run")
        for sample in checkpoint.get("samples") or []:
            completed[sample["case_id"]] = sample

    chunks_by_id = {
        chunk["chunk_id"]: chunk for chunk in chunks_artifact["chunks"]
    }
    for case_position, case in enumerate(cases):
        case_id = case["case_id"]
        if case_id in completed:
            continue
        if case_position % 2 == 0:
            baseline_payload, baseline_attempts = _search(
                session,
                baseline_url,
                case["question"],
                timeout,
                request_attempts,
            )
            contextual_payload, contextual_attempts = _search(
                session,
                contextual_url,
                case["question"],
                timeout,
                request_attempts,
            )
            request_order = ["baseline", "contextual"]
        else:
            contextual_payload, contextual_attempts = _search(
                session,
                contextual_url,
                case["question"],
                timeout,
                request_attempts,
            )
            baseline_payload, baseline_attempts = _search(
                session,
                baseline_url,
                case["question"],
                timeout,
                request_attempts,
            )
            request_order = ["contextual", "baseline"]
        completed[case_id] = {
            "case_id": case_id,
            "dataset_index": case["dataset_index"],
            "question": case["question"],
            "question_type": case["question_type"],
            "request_order": request_order,
            "baseline": _lane_result(
                case,
                baseline_payload,
                baseline_attempts,
                chunks_by_id,
            ),
            "contextual": _lane_result(
                case,
                contextual_payload,
                contextual_attempts,
                chunks_by_id,
            ),
        }
        ordered = [
            completed[selected["case_id"]]
            for selected in cases
            if selected["case_id"] in completed
        ]
        write_artifact(
            paths["samples"],
            {
                "schema_version": RETRIEVAL_SAMPLES_SCHEMA,
                "run_identity": run_identity,
                "manifest_digest": manifest["artifact_digest"],
                "expected_cases": len(cases),
                "completed_cases": len(ordered),
                "samples": ordered,
            },
        )

    samples = [completed[case["case_id"]] for case in cases]
    errors = [
        {
            "case_id": sample["case_id"],
            "baseline_status": sample["baseline"]["status"],
            "contextual_status": sample["contextual"]["status"],
        }
        for sample in samples
        if sample["baseline"]["status"] != "success"
        or sample["contextual"]["status"] != "success"
    ]
    runtime = {
        "baseline": _runtime_stats(samples, "baseline"),
        "contextual": _runtime_stats(samples, "contextual"),
    }
    failed_attempts = sum(
        lane["failed_attempts"] for lane in runtime.values()
    )
    baseline_metrics = _metric_view(samples, "baseline")
    contextual_metrics = _metric_view(samples, "contextual")
    comparison = None
    aggregates = None
    if baseline_metrics:
        aggregates = {
            "baseline": aggregate_scores(baseline_metrics),
            "contextual": aggregate_scores(contextual_metrics),
        }
        comparison = paired_comparison(
            baseline_metrics,
            contextual_metrics,
            resamples=bootstrap_resamples,
            seed=bootstrap_seed,
        )
    formal_complete = (
        limit is None
        and smoke_per_type is None
        and len(samples) == cases_artifact["cases_count"]
        and not errors
        and failed_attempts == 0
    )
    is_smoke = limit is not None or smoke_per_type is not None
    is_promotion_smoke = smoke_per_type is not None
    smoke_complete = (
        is_promotion_smoke
        and len(samples) == len(cases)
        and len(baseline_metrics) == len(cases)
        and not errors
    )
    smoke_promotion = None
    if is_promotion_smoke:
        smoke_promotion = (
            evaluate_smoke_promotion(
                comparison,
                evidence_complete=smoke_complete,
                runtime_errors=len(errors),
                failed_attempts=failed_attempts,
            )
            if comparison is not None
            else {
                "decision": "insufficient",
                "checks": {"paired_evidence_complete": False},
                "primary_metric": "all_evidence_recall_at_10",
                "scope": "scale_up_only",
                "formal_method_conclusion": False,
            }
        )
    elif is_smoke:
        smoke_promotion = {
            "decision": "insufficient",
            "checks": {"official_stratified_smoke_selection": False},
            "primary_metric": "all_evidence_recall_at_10",
            "scope": "scale_up_only",
            "formal_method_conclusion": False,
        }
    evidence_valid = formal_complete or (
        is_promotion_smoke and smoke_complete and failed_attempts == 0
    )
    gate = (
        evaluate_gate(
            comparison,
            evidence_complete=formal_complete,
            runtime_errors=len(errors),
        )
        if comparison is not None
        else {
            "decision": "insufficient",
            "checks": {"evidence_and_runtime_complete": False},
        }
    )
    if not formal_complete:
        gate["decision"] = "insufficient"
    report = write_artifact(
        paths["report"],
        {
            "schema_version": RETRIEVAL_REPORT_SCHEMA,
            "run_kind": RETRIEVAL_RUN_KIND,
            "evidence_scope": manifest["evidence_scope"],
            "evidence_status": "valid" if evidence_valid else "insufficient",
            "formal_ab_eligible": formal_complete,
            "run_identity": run_identity,
            "manifest_digest": manifest["artifact_digest"],
            "samples_digest": load_artifact(
                paths["samples"],
                RETRIEVAL_SAMPLES_SCHEMA,
            )["artifact_digest"],
            "expected_cases": len(cases),
            "completed_cases": len(samples),
            "paired_valid_cases": len(baseline_metrics),
            "runtime_errors": len(errors),
            "failed_request_attempts": failed_attempts,
            "error_cases": errors,
            "runtime": runtime,
            "aggregates": aggregates,
            "comparison": comparison,
            "smoke_promotion": smoke_promotion,
            "gate": gate,
            "completed_at": datetime.now(timezone.utc).isoformat(),
        },
    )
    return report
