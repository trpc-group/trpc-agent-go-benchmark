#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Frozen-answer Agentic A/B runner for the Contextual Retrieval I2 lane."""

from __future__ import annotations

import json
import math
import os
import random
import time
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

from contextual_retrieval.artifacts import (
    canonical_digest,
    file_digest,
    load_artifact,
    public_endpoint_identity,
    text_digest,
    write_artifact,
)
from contextual_retrieval.controller import _sanitize_service_config
from contextual_retrieval.dataset import (
    CASE_SCHEMA,
    CHUNK_SCHEMA,
    DEFAULT_QUESTION_TYPES,
)
from contextual_retrieval.runner import (
    _UrllibSession,
    _get_json,
    validate_service_pair,
)


AGENTIC_MANIFEST_SCHEMA = "contextual-retrieval/agentic-manifest/v1"
AGENTIC_ANSWERS_SCHEMA = "contextual-retrieval/agentic-answers/v1"
AGENTIC_CHECKPOINT_SCHEMA = "contextual-retrieval/agentic-checkpoint/v1"
AGENTIC_REPORT_SCHEMA = "contextual-retrieval/agentic-report/v1"
AGENTIC_LINEAGE_SCHEMA = "contextual-retrieval/agentic-lineage/v1"
AGENTIC_RUN_KIND = "agentic_contextual_ab"
AGENTIC_TRACE_CONTRACT = "contextual-retrieval/agent-trace/v3"
AGENTIC_SEARCH_K = 4
AGENTIC_SMOKE_MIN_SUCCESS_RATE = 0.9
AGENTIC_SMOKE_MAX_FAILURE_RATE_DELTA = 2 / 30
TOOL_ARGUMENT_POLICY = "query-guard/v1"
TOOL_ARGUMENT_MAX_REPAIRS = 1
TOOL_ARGUMENT_VALIDATION_ERROR = "tool_argument_validation_error"
TOOL_ARGUMENT_REPAIR_EXHAUSTED = "tool_argument_repair_exhausted"
KNOWLEDGE_SEARCH_TOOL_NAME = "knowledge_search"
TOOL_NOT_FOUND_RESPONSE = "executeToolCall: Error: tool not found"
FORMAL_REPEATS = 3
FORMAL_CASES = 450
FORMAL_SCHEDULE_SEED = 20260725


def _sealed_payload_is_valid(payload: Mapping[str, Any]) -> bool:
    expected = payload.get("artifact_digest")
    if not isinstance(expected, str) or not expected:
        return False
    unsigned = dict(payload)
    unsigned.pop("artifact_digest", None)
    return canonical_digest(unsigned) == expected


def validate_agentic_lineage(
    lineage: Mapping[str, Any],
    cases: Mapping[str, Any],
    chunks: Mapping[str, Any],
    baseline_config: Mapping[str, Any],
    contextual_config: Mapping[str, Any],
    *,
    mode: str,
    expected_cases: int,
    repeats: int,
) -> Dict[str, Any]:
    """Validate controller-owned, sealed evidence before granting eligibility."""
    public_baseline = _public_agent_config(baseline_config)
    public_contextual = _public_agent_config(contextual_config)
    errors: List[str] = []

    def require(label: str, actual: Any, expected: Any) -> None:
        if actual != expected:
            errors.append(label)

    require("schema_version", lineage.get("schema_version"), AGENTIC_LINEAGE_SCHEMA)
    if not _sealed_payload_is_valid(lineage):
        errors.append("artifact_digest")
    require("status", lineage.get("status"), "valid")
    require("mode", lineage.get("mode"), mode)
    require(
        "case_manifest_digest",
        lineage.get("case_manifest_digest"),
        cases.get("artifact_digest"),
    )
    require(
        "chunk_manifest_digest",
        lineage.get("chunk_manifest_digest"),
        chunks.get("artifact_digest"),
    )
    require("expected_cases", lineage.get("expected_cases"), expected_cases)
    require("repeats", lineage.get("repeats"), repeats)
    require("search_k", lineage.get("search_k"), AGENTIC_SEARCH_K)
    require(
        "tool_argument_policy",
        lineage.get("tool_argument_policy"),
        TOOL_ARGUMENT_POLICY,
    )
    require(
        "max_argument_repairs",
        lineage.get("max_argument_repairs"),
        TOOL_ARGUMENT_MAX_REPAIRS,
    )
    require(
        "silent_argument_rewrite",
        lineage.get("silent_argument_rewrite"),
        False,
    )
    require("provider_strict", lineage.get("provider_strict"), False)
    require(
        "baseline_config_identity",
        lineage.get("baseline_config_identity"),
        canonical_digest(public_baseline),
    )
    require(
        "contextual_config_identity",
        lineage.get("contextual_config_identity"),
        canonical_digest(public_contextual),
    )
    for field in (
        "controller_manifest_digest",
        "provenance_digest",
        "context_summary_digest",
        "baseline_index_state_digest",
        "contextual_index_state_digest",
        "baseline_runtime_config_digest",
        "contextual_runtime_config_digest",
        "context_cache_identity",
        "context_set_digest",
    ):
        if not isinstance(lineage.get(field), str) or not lineage.get(field):
            errors.append(field)
    for field in ("repository", "benchmark_repository"):
        snapshot = lineage.get(field)
        if (
            not isinstance(snapshot, Mapping)
            or not isinstance(snapshot.get("commit"), str)
            or not snapshot.get("commit")
            or snapshot.get("worktree_dirty") is not False
        ):
            errors.append(field)
    if lineage.get("load_endpoint_called") is not False:
        errors.append("load_endpoint_called")
    if lineage.get("judge_initialized") is not False:
        errors.append("judge_initialized")
    if errors:
        raise ValueError(
            "invalid Agentic controller lineage: " + ", ".join(errors)
        )
    return dict(lineage)


def manifest_has_verified_formal_lineage(
    manifest: Mapping[str, Any],
) -> bool:
    """Check the embedded sealed lineage used by the Judge eligibility gate."""
    lineage = manifest.get("verified_lineage")
    if not isinstance(lineage, Mapping) or not _sealed_payload_is_valid(lineage):
        return False
    required_digests = (
        "controller_manifest_digest",
        "provenance_digest",
        "context_summary_digest",
        "baseline_index_state_digest",
        "contextual_index_state_digest",
        "baseline_runtime_config_digest",
        "contextual_runtime_config_digest",
        "context_cache_identity",
        "context_set_digest",
    )
    clean_snapshots = all(
        isinstance(lineage.get(field), Mapping)
        and isinstance(lineage[field].get("commit"), str)
        and bool(lineage[field].get("commit"))
        and lineage[field].get("worktree_dirty") is False
        for field in ("repository", "benchmark_repository")
    )
    return (
        manifest.get("trace_contract") == AGENTIC_TRACE_CONTRACT
        and lineage.get("schema_version") == AGENTIC_LINEAGE_SCHEMA
        and lineage.get("status") == "valid"
        and lineage.get("mode") == "formal"
        and lineage.get("artifact_digest")
        == manifest.get("verified_lineage_digest")
        and lineage.get("case_manifest_digest")
        == manifest.get("case_manifest_digest")
        and lineage.get("chunk_manifest_digest")
        == manifest.get("chunk_manifest_digest")
        and lineage.get("expected_cases") == FORMAL_CASES
        and lineage.get("repeats") == FORMAL_REPEATS
        and lineage.get("search_k") == AGENTIC_SEARCH_K
        and lineage.get("tool_argument_policy") == TOOL_ARGUMENT_POLICY
        and lineage.get("max_argument_repairs") == TOOL_ARGUMENT_MAX_REPAIRS
        and lineage.get("silent_argument_rewrite") is False
        and lineage.get("provider_strict") is False
        and lineage.get("load_endpoint_called") is False
        and lineage.get("judge_initialized") is False
        and clean_snapshots
        and all(
            isinstance(lineage.get(field), str) and lineage.get(field)
            for field in required_digests
        )
    )


def evaluate_agentic_smoke_gate(
    failures: Mapping[str, int],
    executions_per_lane: int,
    *,
    chain_complete: bool,
    protocol_errors: int = 0,
    judge_errors: int = 0,
) -> Dict[str, Any]:
    """Apply the frozen operational smoke guard without gating on method effect."""
    if executions_per_lane <= 0:
        raise ValueError("smoke executions per lane must be positive")
    observed_failures: Dict[str, int] = {}
    for lane in ("baseline", "contextual"):
        value = failures.get(lane)
        if (
            isinstance(value, bool)
            or not isinstance(value, int)
            or value < 0
            or value > executions_per_lane
        ):
            raise ValueError(f"invalid smoke failure count for {lane}")
        observed_failures[lane] = value
    minimum_successes = math.ceil(
        AGENTIC_SMOKE_MIN_SUCCESS_RATE * executions_per_lane - 1e-12
    )
    maximum_failure_delta = math.floor(
        AGENTIC_SMOKE_MAX_FAILURE_RATE_DELTA * executions_per_lane + 1e-12
    )
    successes = {
        lane: executions_per_lane - observed_failures[lane]
        for lane in ("baseline", "contextual")
    }
    failure_delta = abs(
        observed_failures["contextual"] - observed_failures["baseline"]
    )
    checks = {
        "chain_complete": bool(chain_complete),
        "protocol_errors_zero": protocol_errors == 0,
        "judge_errors_zero": judge_errors == 0,
        "minimum_successes_met": all(
            successes[lane] >= minimum_successes
            for lane in ("baseline", "contextual")
        ),
        "failure_delta_within_limit": failure_delta <= maximum_failure_delta,
    }
    return {
        "operational_only": True,
        "method_gate_applied": False,
        "decision": "pass" if all(checks.values()) else "fail",
        "requirements": {
            "minimum_agent_success_rate": AGENTIC_SMOKE_MIN_SUCCESS_RATE,
            "minimum_successes_per_lane": minimum_successes,
            "maximum_failure_rate_delta": (
                AGENTIC_SMOKE_MAX_FAILURE_RATE_DELTA
            ),
            "maximum_failure_count_delta": maximum_failure_delta,
            "protocol_errors": 0,
            "judge_errors": 0,
        },
        "observed": {
            "executions_per_lane": executions_per_lane,
            "failures": observed_failures,
            "successes": successes,
            "success_rates": {
                lane: successes[lane] / executions_per_lane
                for lane in ("baseline", "contextual")
            },
            "failure_count_delta": failure_delta,
            "failure_rate_delta": failure_delta / executions_per_lane,
            "protocol_errors": protocol_errors,
            "judge_errors": judge_errors,
        },
        "checks": checks,
    }


def _output_paths(output_path: str) -> Dict[str, str]:
    target = Path(output_path)
    stem = target.with_suffix("") if target.suffix else target
    return {
        "report": str(target),
        "manifest": str(stem) + ".manifest.json",
        "answers": str(stem) + ".answers.json",
        "checkpoint": str(stem) + ".checkpoint.jsonl",
    }


def _append_checkpoint_record(path: str, payload: Mapping[str, Any]) -> None:
    """Append one independently sealed and durable checkpoint record."""
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    sealed = dict(payload)
    sealed.pop("artifact_digest", None)
    sealed["artifact_digest"] = canonical_digest(sealed)
    with target.open("a", encoding="utf-8") as handle:
        handle.write(
            json.dumps(
                sealed,
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
                allow_nan=False,
            )
            + "\n"
        )
        handle.flush()
        os.fsync(handle.fileno())


def _load_checkpoint_records(
    path: str,
    *,
    run_identity: str,
    manifest_digest: str,
    schedule_digest: str,
    expected_executions: int,
    schedule: Sequence[Mapping[str, Any]],
    repair_torn_tail: bool = False,
) -> Tuple[Dict[str, Dict[str, Any]], int]:
    """Load the latest durable execution state from an append-only checkpoint."""
    header_seen = False
    records = 0
    completed: Dict[str, Dict[str, Any]] = {}
    expected_ids = [str(item["execution_id"]) for item in schedule]
    expected_by_id = {
        str(item["execution_id"]): dict(item)
        for item in schedule
    }
    state_by_id: Dict[str, str] = {}
    torn_tail_offset: Optional[int] = None
    append_missing_newline = False
    file_size = os.path.getsize(path)
    offset = 0
    with open(path, "rb") as handle:
        for line_number, raw_line in enumerate(handle, start=1):
            line_offset = offset
            offset += len(raw_line)
            terminated = raw_line.endswith(b"\n")
            is_last = offset == file_size
            if not raw_line.strip():
                if is_last and not terminated:
                    torn_tail_offset = line_offset
                    break
                continue
            try:
                record = json.loads(raw_line.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError) as error:
                if is_last and not terminated:
                    torn_tail_offset = line_offset
                    break
                raise ValueError(
                    f"Agent checkpoint line {line_number} is invalid JSON"
                ) from error
            if not isinstance(record, Mapping) or not _sealed_payload_is_valid(record):
                if is_last and not terminated:
                    torn_tail_offset = line_offset
                    break
                raise ValueError(
                    f"Agent checkpoint line {line_number} has an invalid digest"
                )
            if record.get("schema_version") != AGENTIC_CHECKPOINT_SCHEMA:
                raise ValueError(
                    f"Agent checkpoint line {line_number} has an invalid schema"
                )
            records += 1
            record_type = record.get("record_type")
            if record_type == "header":
                if header_seen or line_number != 1:
                    raise ValueError("Agent checkpoint must have one header on line 1")
                header_seen = True
                if (
                    record.get("run_identity") != run_identity
                    or record.get("manifest_digest") != manifest_digest
                    or record.get("schedule_digest") != schedule_digest
                    or record.get("expected_executions") != expected_executions
                    or record.get("execution_ids") != expected_ids
                ):
                    raise ValueError("Agent checkpoint belongs to another run")
                continue
            if not header_seen:
                raise ValueError("Agent checkpoint execution precedes its header")
            if record_type != "execution":
                raise ValueError(
                    f"Agent checkpoint line {line_number} has an invalid record type"
                )
            if (
                record.get("run_identity") != run_identity
                or record.get("manifest_digest") != manifest_digest
                or record.get("schedule_digest") != schedule_digest
            ):
                raise ValueError(
                    f"Agent checkpoint line {line_number} belongs to another run"
                )
            execution = record.get("execution")
            if not isinstance(execution, Mapping):
                raise ValueError(
                    f"Agent checkpoint line {line_number} has no execution"
                )
            execution_id = str(execution.get("execution_id") or "")
            if record.get("execution_id") != execution_id:
                raise ValueError(
                    f"Agent checkpoint line {line_number} execution ID differs"
                )
            if execution_id not in expected_by_id:
                raise ValueError(
                    f"Agent checkpoint line {line_number} has an unknown execution"
                )
            expected = expected_by_id[execution_id]
            for field in (
                "execution_id",
                "repeat",
                "case_id",
                "lane",
                "lane_position",
            ):
                if execution.get(field) != expected.get(field):
                    raise ValueError(
                        "Agent checkpoint line "
                        f"{line_number} changes schedule field {field}"
                    )
            result = execution.get("result")
            if not isinstance(result, Mapping):
                raise ValueError(
                    f"Agent checkpoint line {line_number} has no execution result"
                )
            status = str(result.get("status") or "")
            if status not in (
                "started",
                "success",
                "protocol_error",
                "error",
                "indeterminate",
            ):
                raise ValueError(
                    f"Agent checkpoint line {line_number} has an invalid status"
                )
            previous = state_by_id.get(execution_id)
            if previous is None and status != "started":
                raise ValueError(
                    f"Agent checkpoint line {line_number} does not start with started"
                )
            if previous == "started" and status == "started":
                raise ValueError(
                    f"Agent checkpoint line {line_number} repeats started"
                )
            if previous not in (None, "started"):
                raise ValueError(
                    f"Agent checkpoint line {line_number} follows a terminal state"
                )
            state_by_id[execution_id] = status
            completed[execution_id] = dict(execution)
            if is_last and not terminated:
                append_missing_newline = True
    if repair_torn_tail and torn_tail_offset is not None:
        with open(path, "r+b") as handle:
            handle.truncate(torn_tail_offset)
            handle.flush()
            os.fsync(handle.fileno())
    elif repair_torn_tail and append_missing_newline:
        with open(path, "ab") as handle:
            handle.write(b"\n")
            handle.flush()
            os.fsync(handle.fileno())
    if not header_seen:
        raise ValueError("Agent checkpoint header is missing")
    return completed, records


def validate_agentic_checkpoint_evidence(
    path: str,
    answers: Mapping[str, Any],
    report: Mapping[str, Any],
) -> Dict[str, Any]:
    """Bind the append-only checkpoint entity to frozen answers and report."""
    target = Path(path)
    if not target.is_file():
        raise ValueError("Agentic checkpoint file is missing")
    digest = file_digest(str(target))
    with target.open("rb") as handle:
        record_count = sum(1 for line in handle if line.strip())
    if (
        answers.get("checkpoint_sha256") != digest
        or report.get("checkpoint_sha256") != digest
        or answers.get("checkpoint_records") != record_count
        or report.get("checkpoint_records") != record_count
        or report.get("answers_digest") != answers.get("artifact_digest")
    ):
        raise ValueError(
            "Agentic checkpoint, answers, and report are inconsistent"
        )
    return {
        "sha256": digest,
        "records": record_count,
    }


def _public_agent_config(config: Mapping[str, Any]) -> Dict[str, Any]:
    sanitized = _sanitize_service_config(config)
    fields = (
        "model_name",
        "llm_endpoint",
        "llm_header_names",
        "embedding_model",
        "embedding_endpoint",
        "embedding_dimensions",
        "embedding_header_names",
        "vectorstore",
        "search_mode",
        "agent_search_mode_enforced",
        "agent_search_mode_effective",
        "tool_argument_policy",
        "max_argument_repairs",
        "silent_argument_rewrite",
        "provider_strict",
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
    return {field: sanitized.get(field) for field in fields}


def _effective_vector_mode(config: Mapping[str, Any]) -> bool:
    value = config.get("agent_search_mode_effective")
    if isinstance(value, str):
        return value.strip().lower() in ("1", "vector")
    return value == 1


def validate_agentic_service_pair(
    baseline: Mapping[str, Any],
    contextual: Mapping[str, Any],
    chunks: Mapping[str, Any],
    expected_agent_model: str = "deepseek-v3.2",
) -> None:
    """Validate both index identity and effective /answer retrieval behavior."""
    baseline = _sanitize_service_config(baseline)
    contextual = _sanitize_service_config(contextual)
    validate_service_pair(baseline, contextual, chunks)
    errors: List[str] = []
    for lane, config in (("baseline", baseline), ("contextual", contextual)):
        if str(config.get("model_name") or "").strip().lower() != (
            expected_agent_model.strip().lower()
        ):
            errors.append(
                f"{lane} Agent model must be {expected_agent_model}"
            )
        if config.get("agent_search_mode_enforced") is not True:
            errors.append(
                f"{lane} service does not enforce search mode in /answer"
            )
        if not _effective_vector_mode(config):
            errors.append(f"{lane} /answer search mode is not vector")
        if str(config.get("embedding_model") or "").strip().lower() != "bge-m3":
            errors.append(f"{lane} embedding model must be bge-m3")
        if config.get("embedding_dimensions") != 1024:
            errors.append(f"{lane} embedding dimensions must be 1024")
        if config.get("tool_argument_policy") != TOOL_ARGUMENT_POLICY:
            errors.append(
                f"{lane} tool argument policy must be {TOOL_ARGUMENT_POLICY}"
            )
        if config.get("max_argument_repairs") != TOOL_ARGUMENT_MAX_REPAIRS:
            errors.append(
                f"{lane} max argument repairs must be "
                f"{TOOL_ARGUMENT_MAX_REPAIRS}"
            )
        if config.get("silent_argument_rewrite") is not False:
            errors.append(f"{lane} silently rewrites tool arguments")
        if config.get("provider_strict") is not False:
            errors.append(f"{lane} unexpectedly enables provider strict mode")
    if baseline.get("context_set_digest") not in (None, ""):
        errors.append("baseline service unexpectedly has a Context set")
    if not contextual.get("context_set_digest"):
        errors.append("contextual service Context set digest is missing")
    for field in (
        "model_name",
        "llm_endpoint",
        "llm_header_names",
        "tool_argument_policy",
        "max_argument_repairs",
        "silent_argument_rewrite",
        "provider_strict",
    ):
        if baseline.get(field) != contextual.get(field):
            errors.append(f"Agent service field {field} differs between A and B")
    if not baseline.get("llm_endpoint") or not contextual.get("llm_endpoint"):
        errors.append("Agent endpoint identity is missing")
    if errors:
        raise ValueError("invalid Agentic A/B services: " + "; ".join(errors))


def _select_cases(
    cases: Sequence[Mapping[str, Any]],
    smoke_per_type: Optional[int],
) -> List[Mapping[str, Any]]:
    if smoke_per_type is None:
        return list(cases)
    if smoke_per_type <= 0:
        raise ValueError("smoke_per_type must be positive")
    selected: List[Mapping[str, Any]] = []
    counts: Dict[str, int] = defaultdict(int)
    for case in cases:
        question_type = str(case.get("question_type") or "")
        if (
            question_type in DEFAULT_QUESTION_TYPES
            and counts[question_type] < smoke_per_type
        ):
            selected.append(case)
            counts[question_type] += 1
    incomplete = {
        question_type: counts[question_type]
        for question_type in DEFAULT_QUESTION_TYPES
        if counts[question_type] != smoke_per_type
    }
    if incomplete:
        raise ValueError(f"smoke selection is incomplete: {incomplete}")
    return selected


def build_agentic_schedule(
    cases: Sequence[Mapping[str, Any]],
    repeats: int,
    seed: int,
) -> List[Dict[str, Any]]:
    """Pair A/B per case with balanced order and deterministic shuffling."""
    if repeats <= 0:
        raise ValueError("repeats must be positive")
    canonical = list(cases)
    schedule: List[Dict[str, Any]] = []
    for repeat in range(repeats):
        ordered = list(enumerate(canonical))
        random.Random(seed + repeat).shuffle(ordered)
        for schedule_position, (case_position, case) in enumerate(ordered):
            baseline_first = (case_position + repeat) % 2 == 0
            lanes = (
                ("baseline", "contextual")
                if baseline_first
                else ("contextual", "baseline")
            )
            pair_id = f"r{repeat}:{case['case_id']}"
            for lane_position, lane in enumerate(lanes):
                schedule.append(
                    {
                        "execution_id": f"{pair_id}:{lane}",
                        "pair_id": pair_id,
                        "repeat": repeat,
                        "case_id": case["case_id"],
                        "case_position": case_position,
                        "schedule_position": schedule_position,
                        "lane_position": lane_position,
                        "lane": lane,
                    }
                )
    return schedule


def _tool_query(arguments: Any) -> Tuple[Optional[str], Optional[str]]:
    if not isinstance(arguments, str):
        return None, "tool arguments are not a string"
    try:
        parsed = json.loads(arguments)
    except json.JSONDecodeError as error:
        return None, f"invalid JSON tool arguments: {error}"
    if not isinstance(parsed, Mapping):
        return None, "tool arguments are not an object"
    unexpected = sorted(str(field) for field in parsed if field != "query")
    if "query" not in parsed:
        suffix = (
            f"; unexpected fields: {', '.join(unexpected)}"
            if unexpected
            else ""
        )
        return None, "tool arguments contain no query" + suffix
    if unexpected:
        return None, f"unexpected tool argument fields: {', '.join(unexpected)}"
    value = parsed.get("query")
    if not isinstance(value, str) or not value.strip():
        return None, "tool query is not a non-empty string"
    return value, None


def _tool_argument_validation_feedback(
    content: Any,
) -> Tuple[Optional[Dict[str, Any]], List[str]]:
    """Parse model-visible query-guard feedback without treating it as evidence."""
    if not isinstance(content, str):
        return None, []
    try:
        payload = json.loads(content)
    except json.JSONDecodeError:
        return None, []
    if not isinstance(payload, Mapping) or payload.get("type") != (
        TOOL_ARGUMENT_VALIDATION_ERROR
    ):
        return None, []

    errors: List[str] = []
    if payload.get("policy") != TOOL_ARGUMENT_POLICY:
        errors.append("tool argument validation policy is invalid")
    if payload.get("retryable") is not True:
        errors.append("tool argument validation response is not retryable")
    if payload.get("remaining_repairs") != TOOL_ARGUMENT_MAX_REPAIRS:
        errors.append("tool argument validation repair budget is invalid")
    if not isinstance(payload.get("message"), str) or not payload.get(
        "message"
    ).strip():
        errors.append("tool argument validation message is missing")

    normalized: Dict[str, Any] = {
        "type": TOOL_ARGUMENT_VALIDATION_ERROR,
        "policy": payload.get("policy"),
        "retryable": payload.get("retryable"),
        "remaining_repairs": payload.get("remaining_repairs"),
    }
    allowed = payload.get("allowed")
    if allowed != ["query"]:
        errors.append("tool argument validation allowed fields are invalid")
        allowed = []
    normalized["allowed"] = list(allowed)
    for field in ("missing", "unexpected", "invalid"):
        value = payload.get(field) or []
        if not isinstance(value, list) or not all(
            isinstance(item, str) and item for item in value
        ):
            errors.append(f"tool argument validation {field} is invalid")
            value = []
        normalized[field] = list(value)
    if not any(
        normalized[field] for field in ("missing", "unexpected", "invalid")
    ):
        errors.append("tool argument validation response has no diagnostics")
    return normalized, errors


def _is_tool_not_found_response(content: Any) -> bool:
    """Recognize the local dispatcher error without treating it as evidence."""
    return (
        isinstance(content, str)
        and content.strip() == TOOL_NOT_FOUND_RESPONSE
    )


def _chunk_indexes(
    chunks: Mapping[str, Any],
) -> Tuple[Dict[str, Mapping[str, Any]], Dict[str, List[Mapping[str, Any]]]]:
    by_id = {str(chunk["chunk_id"]): chunk for chunk in chunks["chunks"]}
    by_hash: Dict[str, List[Mapping[str, Any]]] = defaultdict(list)
    for chunk in chunks["chunks"]:
        by_hash[str(chunk["chunk_content_hash"])].append(chunk)
    return by_id, by_hash


def _normalize_tool_document(
    raw: Any,
    rank: int,
    chunks_by_id: Mapping[str, Mapping[str, Any]],
    chunks_by_hash: Mapping[str, Sequence[Mapping[str, Any]]],
) -> Tuple[Dict[str, Any], List[str]]:
    if not isinstance(raw, Mapping):
        return {"rank": rank}, ["tool document is not an object"]
    text = raw.get("text")
    if not isinstance(text, str):
        return {"rank": rank}, ["tool document text is missing"]
    metadata = raw.get("metadata") or {}
    if not isinstance(metadata, Mapping):
        metadata = {}
    raw_chunk_id = metadata.get("contextual_retrieval_chunk_id")
    raw_parent_id = metadata.get("contextual_retrieval_parent_document_id")
    content_hash = text_digest(text)
    errors: List[str] = []
    expected: Optional[Mapping[str, Any]] = None
    identity_source: Optional[str] = None
    if isinstance(raw_chunk_id, str) and raw_chunk_id in chunks_by_id:
        expected = chunks_by_id[raw_chunk_id]
        identity_source = "metadata"
    elif isinstance(raw_chunk_id, str):
        errors.append("tool document has unknown chunk ID")
    else:
        matches = list(chunks_by_hash.get(content_hash) or [])
        if len(matches) == 1:
            expected = matches[0]
            identity_source = "content_hash"
        else:
            errors.append("tool document cannot be mapped to a unique chunk")
    chunk_id = expected["chunk_id"] if expected is not None else None
    parent_id = (
        expected["parent_document_id"] if expected is not None else None
    )
    if expected is not None:
        if content_hash != expected["chunk_content_hash"]:
            errors.append("tool document content hash does not match chunk")
        if raw_parent_id is not None and raw_parent_id != parent_id:
            errors.append("tool document parent ID does not match chunk")
    score = raw.get("score")
    if isinstance(score, bool) or not isinstance(score, (int, float)):
        score = None
    elif not math.isfinite(float(score)):
        score = None
    return (
        {
            "rank": rank,
            "chunk_id": chunk_id,
            "parent_document_id": parent_id,
            "score": float(score) if score is not None else None,
            "chunk_content_hash": content_hash,
            "text": text,
            "metadata": dict(metadata),
            "identity_source": identity_source,
        },
        errors,
    )


def _parse_tool_response(
    content: Any,
    chunks_by_id: Mapping[str, Mapping[str, Any]],
    chunks_by_hash: Mapping[str, Sequence[Mapping[str, Any]]],
) -> Tuple[List[Dict[str, Any]], List[str], List[str]]:
    contract_errors: List[str] = []
    if not isinstance(content, str):
        return [], [], ["tool response content is not a string"]
    try:
        payload = json.loads(content)
    except json.JSONDecodeError as error:
        return [], [], [f"tool response is not JSON: {error}"]
    if not isinstance(payload, Mapping) or not isinstance(
        payload.get("documents"), list
    ):
        return [], [], ["tool response has no document list"]
    documents = []
    for rank, raw in enumerate(payload["documents"], start=1):
        document, errors = _normalize_tool_document(
            raw,
            rank,
            chunks_by_id,
            chunks_by_hash,
        )
        documents.append(document)
        contract_errors.extend(f"rank {rank}: {error}" for error in errors)
    return documents, contract_errors, []


def _normalize_recorded_searches(
    raw_searches: Any,
    chunks_by_id: Mapping[str, Mapping[str, Any]],
) -> Tuple[List[Dict[str, Any]], List[str]]:
    """Normalize the server-side record of effective knowledge.Search calls."""
    if raw_searches is None:
        return [], []
    if not isinstance(raw_searches, list):
        return [], ["Agent trace searches field is not a list"]
    searches = []
    errors: List[str] = []
    for search_index, raw in enumerate(raw_searches, start=1):
        if not isinstance(raw, Mapping):
            errors.append(f"search {search_index} is not an object")
            continue
        query = raw.get("query")
        request = raw.get("request")
        results = raw.get("results") or []
        search_errors = []
        if not isinstance(query, str) or not query.strip():
            search_errors.append("query is missing")
        if not isinstance(request, Mapping):
            search_errors.append("effective request is missing")
            request = {}
        if request.get("max_results") != AGENTIC_SEARCH_K:
            search_errors.append(
                f"effective max_results is not {AGENTIC_SEARCH_K}"
            )
        if request.get("search_mode") != 1:
            search_errors.append("effective search_mode is not vector")
        if not isinstance(results, list):
            search_errors.append("results is not a list")
            results = []
        documents = []
        for fallback_rank, result in enumerate(results, start=1):
            if not isinstance(result, Mapping):
                search_errors.append(f"result {fallback_rank} is not an object")
                continue
            metadata = result.get("metadata") or {}
            if not isinstance(metadata, Mapping):
                metadata = {}
            chunk_id = metadata.get("contextual_retrieval_chunk_id")
            if not isinstance(chunk_id, str) or chunk_id not in chunks_by_id:
                document_id = result.get("document_id")
                chunk_id = (
                    document_id
                    if isinstance(document_id, str)
                    and document_id in chunks_by_id
                    else None
                )
            raw_parent_id = metadata.get(
                "contextual_retrieval_parent_document_id"
            )
            expected = chunks_by_id.get(str(chunk_id)) if chunk_id else None
            content_hash = result.get("content_sha256")
            parent_id = (
                expected["parent_document_id"]
                if expected is not None
                else raw_parent_id
            )
            if expected is None:
                search_errors.append(
                    f"result {fallback_rank} has unknown chunk identity"
                )
            else:
                if (
                    raw_parent_id is not None
                    and raw_parent_id != expected["parent_document_id"]
                ):
                    search_errors.append(
                        f"result {fallback_rank} has wrong parent identity"
                    )
                if content_hash != expected["chunk_content_hash"]:
                    search_errors.append(
                        f"result {fallback_rank} has wrong content hash"
                    )
            score = result.get("score")
            if (
                isinstance(score, bool)
                or not isinstance(score, (int, float))
                or not math.isfinite(float(score))
            ):
                score = None
            documents.append(
                {
                    "rank": result.get("rank", fallback_rank),
                    "chunk_id": chunk_id,
                    "parent_document_id": parent_id,
                    "score": float(score) if score is not None else None,
                    "chunk_content_hash": content_hash,
                    "document_id": result.get("document_id"),
                    "metadata": dict(metadata),
                    "identity_source": (
                        "metadata"
                        if metadata.get("contextual_retrieval_chunk_id")
                        == chunk_id
                        else "document_id"
                        if chunk_id is not None
                        else None
                    ),
                }
            )
        if raw.get("error"):
            search_errors.append(f"server search error: {raw['error']}")
        errors.extend(
            f"search {search_index}: {error}" for error in search_errors
        )
        searches.append(
            {
                "search_index": search_index,
                "query": query if isinstance(query, str) else None,
                "request": dict(request),
                "documents": documents,
                "server_error": raw.get("error") or None,
                "validation_errors": search_errors,
            }
        )
    return searches, errors


def _document_identity(document: Mapping[str, Any]) -> Tuple[Any, ...]:
    """Return the hard-gated identity projection for one retrieved chunk."""
    return (
        document.get("rank"),
        document.get("chunk_id"),
        document.get("parent_document_id"),
        document.get("chunk_content_hash"),
    )


def _metadata_difference_keys(
    recorded: Mapping[str, Any],
    tool: Mapping[str, Any],
) -> List[str]:
    recorded_metadata = recorded.get("metadata") or {}
    tool_metadata = tool.get("metadata") or {}
    if not isinstance(recorded_metadata, Mapping):
        recorded_metadata = {}
    if not isinstance(tool_metadata, Mapping):
        tool_metadata = {}
    return sorted(
        str(key)
        for key in set(recorded_metadata) | set(tool_metadata)
        if recorded_metadata.get(key) != tool_metadata.get(key)
    )


def _score_delta(
    recorded: Mapping[str, Any],
    tool: Mapping[str, Any],
) -> Tuple[bool, Optional[float]]:
    recorded_score = recorded.get("score")
    tool_score = tool.get("score")
    if recorded_score is None and tool_score is None:
        return False, None
    if recorded_score is None or tool_score is None:
        return True, None
    delta = abs(float(recorded_score) - float(tool_score))
    return delta != 0, delta


def _evidence_summary(
    case: Mapping[str, Any],
    searches: Sequence[Mapping[str, Any]],
) -> Dict[str, Any]:
    evidence = list(case.get("evidence") or [])
    evidence_chunks = {
        str(record["evidence_id"]): set(record.get("chunk_ids") or [])
        for record in evidence
    }
    gold_parents = {
        str(record["parent_document_id"])
        for record in evidence
        if record.get("parent_document_id")
    }
    cumulative_chunks: set[str] = set()
    cumulative_parents: set[str] = set()
    per_search = []
    for search in searches:
        documents = search.get("documents") or []
        chunks = {
            str(document["chunk_id"])
            for document in documents
            if document.get("chunk_id")
        }
        parents = {
            str(document["parent_document_id"])
            for document in documents
            if document.get("parent_document_id")
        }
        cumulative_chunks.update(chunks)
        cumulative_parents.update(parents)
        hit_evidence = sorted(
            evidence_id
            for evidence_id, chunk_ids in evidence_chunks.items()
            if chunk_ids & chunks
        )
        cumulative_evidence = sorted(
            evidence_id
            for evidence_id, chunk_ids in evidence_chunks.items()
            if chunk_ids & cumulative_chunks
        )
        per_search.append(
            {
                "tool_call_id": search.get("tool_call_id"),
                "evidence_ids": hit_evidence,
                "cumulative_evidence_ids": cumulative_evidence,
                "cumulative_evidence_recall": (
                    len(cumulative_evidence) / len(evidence_chunks)
                    if evidence_chunks
                    else None
                ),
            }
        )
    hit_evidence = sorted(
        evidence_id
        for evidence_id, chunk_ids in evidence_chunks.items()
        if chunk_ids & cumulative_chunks
    )
    hit_parents = sorted(gold_parents & cumulative_parents)
    return {
        "gold_evidence_ids": sorted(evidence_chunks),
        "gold_document_ids": sorted(gold_parents),
        "hit_evidence_ids": hit_evidence,
        "hit_document_ids": hit_parents,
        "cumulative_evidence_recall": (
            len(hit_evidence) / len(evidence_chunks) if evidence_chunks else None
        ),
        "cumulative_document_recall": (
            len(hit_parents) / len(gold_parents) if gold_parents else None
        ),
        "all_evidence_recalled": bool(evidence_chunks)
        and len(hit_evidence) == len(evidence_chunks),
        "per_search": per_search,
    }


def _normalize_answer_payload(
    case: Mapping[str, Any],
    payload: Any,
    chunks_by_id: Mapping[str, Mapping[str, Any]],
    chunks_by_hash: Mapping[str, Sequence[Mapping[str, Any]]],
) -> Dict[str, Any]:
    if not isinstance(payload, Mapping):
        raise ValueError("answer response is not an object")
    answer = payload.get("answer")
    documents = payload.get("documents")
    trace = payload.get("trace")
    if not isinstance(answer, str) or not answer.strip():
        raise ValueError("answer response has an empty answer")
    if not isinstance(documents, list):
        raise ValueError("answer response has no document list")
    if not isinstance(trace, Mapping):
        raise ValueError("answer response has no Agent trace")
    contexts = []
    excluded_tool_error_contexts = 0
    for index, document in enumerate(documents):
        if not isinstance(document, Mapping) or not isinstance(
            document.get("text"), str
        ):
            raise ValueError(f"answer document {index} has no text")
        text = document["text"]
        if _is_tool_not_found_response(text):
            excluded_tool_error_contexts += 1
            continue
        contexts.append(text)

    calls = trace.get("tool_calls") or []
    responses = trace.get("tool_responses") or []
    if not isinstance(calls, list) or not isinstance(responses, list):
        raise ValueError("Agent trace tool fields must be lists")
    response_by_id: Dict[str, Mapping[str, Any]] = {}
    for response in responses:
        if isinstance(response, Mapping) and isinstance(
            response.get("tool_id"), str
        ):
            response_by_id[str(response["tool_id"])] = response

    searches, trace_errors = _normalize_recorded_searches(
        trace.get("searches"),
        chunks_by_id,
    )
    tool_runtime_errors: List[str] = []
    trace_diagnostics: Dict[str, Any] = {
        "contract_version": AGENTIC_TRACE_CONTRACT,
        "document_pairs_compared": 0,
        "metadata_mismatch_documents": 0,
        "metadata_difference_keys": [],
        "score_mismatch_documents": 0,
        "score_max_abs_delta": None,
        "identity_fallback_documents": 0,
        "excluded_tool_error_contexts": excluded_tool_error_contexts,
        "comparison_skipped_reasons": [],
    }
    metadata_difference_keys: set[str] = set()
    tool_searches = []
    tool_name_errors: List[Dict[str, Any]] = []
    for call_position, call in enumerate(calls):
        if not isinstance(call, Mapping):
            trace_errors.append("tool call is not an object")
            continue
        name = str(call.get("name") or "")
        call_id = str(call.get("id") or "")
        response = response_by_id.get(call_id)
        if name != KNOWLEDGE_SEARCH_TOOL_NAME:
            if response is not None and _is_tool_not_found_response(
                response.get("content")
            ):
                tool_name_errors.append(
                    {
                        "tool_call_id": call_id or None,
                        "call_position": call_position,
                        "recovered": False,
                    }
                )
                continue
            if "search" not in name.lower():
                continue
        query, query_error = _tool_query(call.get("arguments"))
        parsed_documents: List[Dict[str, Any]] = []
        response_contract_errors: List[str] = []
        response_runtime_errors: List[str] = []
        argument_error: Optional[Dict[str, Any]] = None
        if response is None:
            response_runtime_errors.append("tool response is missing")
        else:
            feedback, feedback_errors = _tool_argument_validation_feedback(
                response.get("content")
            )
            if feedback is not None:
                response_contract_errors.extend(feedback_errors)
                argument_error = {
                    **feedback,
                    "tool_call_id": call_id or None,
                    "argument_error": query_error,
                }
                if query_error is None:
                    response_contract_errors.append(
                        "query guard rejected valid tool arguments"
                    )
            else:
                (
                    parsed_documents,
                    response_contract_errors,
                    response_runtime_errors,
                ) = _parse_tool_response(
                    response.get("content"), chunks_by_id, chunks_by_hash
                )
        if query_error and argument_error is None:
            response_contract_errors.insert(
                0,
                "invalid tool arguments were not handled by "
                f"{TOOL_ARGUMENT_POLICY}: {query_error}",
            )
        trace_errors.extend(
            f"tool response {call_id or '<missing-id>'}: {error}"
            for error in response_contract_errors
        )
        tool_runtime_errors.extend(
            f"tool response {call_id or '<missing-id>'}: {error}"
            for error in response_runtime_errors
        )
        trace_diagnostics["identity_fallback_documents"] += sum(
            document.get("identity_source") == "content_hash"
            for document in parsed_documents
        )
        tool_searches.append(
            {
                "tool_call_id": call_id or None,
                "tool_name": name,
                "call_position": call_position,
                "query": query,
                "arguments": call.get("arguments"),
                "documents": parsed_documents,
                "argument_error": argument_error,
                "validation_errors": [
                    *response_contract_errors,
                    *response_runtime_errors,
                ],
                "contract_errors": response_contract_errors,
                "runtime_errors": response_runtime_errors,
            }
        )

    tool_argument_errors: List[Dict[str, Any]] = []
    for index, search in enumerate(tool_searches):
        argument_error = search.get("argument_error")
        if not isinstance(argument_error, Mapping):
            continue
        recovered = any(
            later.get("argument_error") is None and later.get("query")
            for later in tool_searches[index + 1 :]
        )
        normalized_error = {**argument_error, "recovered": recovered}
        search["argument_error"] = normalized_error
        tool_argument_errors.append(normalized_error)
    if len(tool_argument_errors) > TOOL_ARGUMENT_MAX_REPAIRS:
        trace_errors.append("tool argument validation feedback exceeds budget")

    valid_search_positions = [
        int(search["call_position"])
        for search in tool_searches
        if search.get("tool_name") == KNOWLEDGE_SEARCH_TOOL_NAME
        and search.get("argument_error") is None
        and not search.get("runtime_errors")
        and search.get("query")
        and search.get("documents")
    ]
    for error in tool_name_errors:
        error["recovered"] = any(
            position > int(error["call_position"])
            for position in valid_search_positions
        )

    effective_tool_searches = [
        search
        for search in tool_searches
        if search.get("argument_error") is None
        and not search.get("runtime_errors")
    ]
    aligned_tool_searches: Optional[List[Dict[str, Any]]]
    if len(searches) == len(effective_tool_searches):
        aligned_tool_searches = effective_tool_searches
        if tool_argument_errors:
            trace_diagnostics["comparison_skipped_reasons"].append(
                "tool_argument_validation_calls_excluded"
            )
        if tool_name_errors:
            trace_diagnostics["comparison_skipped_reasons"].append(
                "tool_name_error_calls_excluded"
            )
        if any(search.get("runtime_errors") for search in tool_searches):
            trace_diagnostics["comparison_skipped_reasons"].append(
                "runtime_failed_tool_calls_excluded"
            )
    else:
        aligned_tool_searches = None
        trace_errors.append(
            "tool-call search count does not match effective search count"
        )
        trace_diagnostics["comparison_skipped_reasons"].append(
            "search_count_mismatch"
        )
    if aligned_tool_searches is not None:
        for index, search in enumerate(searches):
            tool_search = aligned_tool_searches[index]
            search["tool_call_id"] = tool_search.get("tool_call_id")
            search["tool_call"] = tool_search
            if (
                tool_search.get("query")
                and search.get("query") != tool_search.get("query")
            ):
                trace_errors.append(
                    f"search {index + 1} query differs from tool arguments"
                )
            recorded_documents = list(search.get("documents") or [])
            tool_documents = list(tool_search.get("documents") or [])
            comparison_blockers = [
                *tool_search.get("contract_errors", []),
                *tool_search.get("runtime_errors", []),
            ]
            if comparison_blockers:
                trace_diagnostics["comparison_skipped_reasons"].append(
                    f"search_{index + 1}_invalid_tool_response"
                )
                continue
            recorded_identities = [
                _document_identity(document) for document in recorded_documents
            ]
            tool_identities = [
                _document_identity(document) for document in tool_documents
            ]
            if recorded_identities != tool_identities:
                trace_errors.append(
                    f"search {index + 1} tool response document identity/order "
                    "differs from server record"
                )
            for recorded, tool_document in zip(
                recorded_documents, tool_documents
            ):
                trace_diagnostics["document_pairs_compared"] += 1
                difference_keys = _metadata_difference_keys(
                    recorded, tool_document
                )
                if difference_keys:
                    trace_diagnostics["metadata_mismatch_documents"] += 1
                    metadata_difference_keys.update(difference_keys)
                score_differs, score_delta = _score_delta(
                    recorded, tool_document
                )
                if score_differs:
                    trace_diagnostics["score_mismatch_documents"] += 1
                if score_delta is not None:
                    current_max = trace_diagnostics["score_max_abs_delta"]
                    trace_diagnostics["score_max_abs_delta"] = (
                        score_delta
                        if current_max is None
                        else max(float(current_max), score_delta)
                    )
    trace_diagnostics["metadata_difference_keys"] = sorted(
        metadata_difference_keys
    )
    tool_contexts = [
        document["text"]
        for search in tool_searches
        for document in search.get("documents") or []
        if isinstance(document.get("text"), str) and document.get("text")
    ]
    tool_contexts_comparable = aligned_tool_searches is not None and not any(
        search.get("contract_errors") or search.get("runtime_errors")
        for search in effective_tool_searches
    )
    if tool_contexts_comparable and contexts != tool_contexts:
        trace_errors.append(
            "top-level contexts differ from parsed tool-response documents"
        )
    elif not tool_contexts_comparable:
        trace_diagnostics["comparison_skipped_reasons"].append(
            "top_level_contexts_not_comparable"
        )
    trace_diagnostics["comparison_skipped_reasons"] = list(
        dict.fromkeys(trace_diagnostics["comparison_skipped_reasons"])
    )
    protocol_violations = []
    if not searches:
        protocol_violations.append("no_search_tool_call")
    unrecovered_tool_argument_errors = sum(
        not bool(error.get("recovered")) for error in tool_argument_errors
    )
    unrecovered_tool_name_errors = sum(
        not bool(error.get("recovered")) for error in tool_name_errors
    )
    agent_errors = []
    if unrecovered_tool_argument_errors:
        agent_errors.append("tool argument repair was not completed")
    if unrecovered_tool_name_errors:
        agent_errors.append("tool name repair was not completed")
    failure_categories = []
    if trace_errors:
        failure_categories.append("trace_contract_error")
    if tool_runtime_errors:
        failure_categories.append("tool_runtime_error")
    if protocol_violations:
        failure_categories.append("no_search_tool_call")
    if unrecovered_tool_argument_errors:
        failure_categories.append("tool_argument_error")
    if unrecovered_tool_name_errors:
        failure_categories.append("tool_name_error")
    return {
        "answer": answer,
        "contexts": contexts,
        "trace": dict(trace),
        "tool_call_count": len(calls),
        "search_call_count": len(searches),
        "search_queries": [search.get("query") for search in searches],
        "searches": searches,
        "trace_validation_errors": trace_errors,
        "tool_runtime_errors": tool_runtime_errors,
        "agent_errors": agent_errors,
        "protocol_violations": protocol_violations,
        "tool_argument_errors": tool_argument_errors,
        "tool_argument_error_attempts": len(tool_argument_errors),
        "recovered_tool_argument_errors": sum(
            bool(error.get("recovered")) for error in tool_argument_errors
        ),
        "unrecovered_tool_argument_errors": unrecovered_tool_argument_errors,
        "tool_name_errors": tool_name_errors,
        "tool_name_error_attempts": len(tool_name_errors),
        "recovered_tool_name_errors": sum(
            bool(error.get("recovered")) for error in tool_name_errors
        ),
        "unrecovered_tool_name_errors": unrecovered_tool_name_errors,
        "failure_categories": failure_categories,
        "trace_diagnostics": trace_diagnostics,
        "evidence": _evidence_summary(case, searches),
    }


def _service_error_type(response: Any) -> Optional[str]:
    if response is None:
        return None
    payload: Any = None
    try:
        payload = response.json()
    except Exception:
        reader = getattr(response, "read", None)
        if callable(reader):
            try:
                body = reader()
                if isinstance(body, bytes):
                    body = body.decode("utf-8")
                payload = json.loads(body) if isinstance(body, str) else None
            except Exception:
                payload = None
    if not isinstance(payload, Mapping):
        return None
    error_type = payload.get("error_type")
    if error_type in (TOOL_ARGUMENT_REPAIR_EXHAUSTED, "agent_execution_error"):
        return str(error_type)
    return None


def _answer_once(
    session: Any,
    service_url: str,
    case: Mapping[str, Any],
    timeout: float,
    chunks_by_id: Mapping[str, Mapping[str, Any]],
    chunks_by_hash: Mapping[str, Sequence[Mapping[str, Any]]],
    request_started_at: str,
) -> Dict[str, Any]:
    started = time.monotonic()
    service_error_type: Optional[str] = None
    try:
        response = session.post(
            service_url.rstrip("/") + "/answer",
            json={"question": case["question"], "k": AGENTIC_SEARCH_K},
            timeout=timeout,
        )
        status_code = getattr(
            response,
            "status_code",
            getattr(response, "status", None),
        )
        if isinstance(status_code, int) and status_code >= 400:
            service_error_type = _service_error_type(response)
        response.raise_for_status()
        try:
            normalized = _normalize_answer_payload(
                case,
                response.json(),
                chunks_by_id,
                chunks_by_hash,
            )
        except ValueError as error:
            response_error_type = (
                "empty_answer"
                if str(error) == "answer response has an empty answer"
                else "response_validation_error"
            )
            result = _empty_result(
                "error",
                response_error_type,
                elapsed_ms=round((time.monotonic() - started) * 1000, 3),
            )
            result["response_error_type"] = response_error_type
            result["agent_errors"] = [response_error_type]
            result["failure_categories"] = [response_error_type]
            result["request_attempt"] = {
                "attempt": 1,
                "started_at": request_started_at,
                "completed_at": datetime.now(timezone.utc).isoformat(),
                "status": "success",
                "http_status": (
                    int(status_code) if isinstance(status_code, int) else None
                ),
                "error_type": None,
            }
            return result
        protocol_errors = list(normalized["trace_validation_errors"])
        agent_errors = [
            *normalized["tool_runtime_errors"],
            *normalized["protocol_violations"],
            *normalized["agent_errors"],
        ]
        completed_at = datetime.now(timezone.utc).isoformat()
        status_code = getattr(response, "status_code", None)
        status = (
            "protocol_error"
            if protocol_errors
            else "error"
            if agent_errors
            else "success"
        )
        errors = [*protocol_errors, *agent_errors]
        return {
            "status": status,
            "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
            "error": "; ".join(errors)[:4000] if errors else None,
            "request_attempt": {
                "attempt": 1,
                "started_at": request_started_at,
                "completed_at": completed_at,
                "status": "success",
                "http_status": (
                    int(status_code) if isinstance(status_code, int) else None
                ),
                "error_type": None,
            },
            **normalized,
        }
    except Exception as error:
        error_response = getattr(error, "response", None)
        if service_error_type is None:
            error_source = error_response if error_response is not None else error
            service_error_type = _service_error_type(error_source)
        status_code = getattr(error_response, "status_code", None)
        if status_code is None:
            status_code = getattr(error, "code", None)
        result = _empty_result(
            "error",
            service_error_type or "request_error",
            elapsed_ms=round((time.monotonic() - started) * 1000, 3),
        )
        if service_error_type == TOOL_ARGUMENT_REPAIR_EXHAUSTED:
            result["agent_errors"] = [TOOL_ARGUMENT_REPAIR_EXHAUSTED]
            result["tool_argument_error_attempts"] = (
                TOOL_ARGUMENT_MAX_REPAIRS + 1
            )
            result["unrecovered_tool_argument_errors"] = 1
            result["failure_categories"] = ["tool_argument_error"]
        elif service_error_type == "agent_execution_error":
            result["agent_errors"] = ["agent_execution_error"]
            result["failure_categories"] = ["agent_execution_error"]
        else:
            result["failure_categories"] = ["request_error"]
        result["request_attempt"] = {
            "attempt": 1,
            "started_at": request_started_at,
            "completed_at": datetime.now(timezone.utc).isoformat(),
            "status": "error",
            "http_status": (
                int(status_code) if isinstance(status_code, int) else None
            ),
            "error_type": service_error_type or "request_error",
        }
        return result


def _empty_result(
    status: str,
    error: str,
    elapsed_ms: float = 0,
) -> Dict[str, Any]:
    return {
        "status": status,
        "elapsed_ms": elapsed_ms,
        "error": error,
        "answer": "",
        "contexts": [],
        "trace": None,
        "tool_call_count": 0,
        "search_call_count": 0,
        "search_queries": [],
        "searches": [],
        "trace_validation_errors": [],
        "tool_runtime_errors": [],
        "agent_errors": [],
        "protocol_violations": [],
        "tool_argument_errors": [],
        "tool_argument_error_attempts": 0,
        "recovered_tool_argument_errors": 0,
        "unrecovered_tool_argument_errors": 0,
        "tool_name_errors": [],
        "tool_name_error_attempts": 0,
        "recovered_tool_name_errors": 0,
        "unrecovered_tool_name_errors": 0,
        "response_error_type": None,
        "failure_categories": [],
        "trace_diagnostics": {
            "contract_version": AGENTIC_TRACE_CONTRACT,
            "document_pairs_compared": 0,
            "metadata_mismatch_documents": 0,
            "metadata_difference_keys": [],
            "score_mismatch_documents": 0,
            "score_max_abs_delta": None,
            "identity_fallback_documents": 0,
            "excluded_tool_error_contexts": 0,
            "comparison_skipped_reasons": [],
        },
        "evidence": None,
        "request_attempt": None,
    }


def _runtime_summary(
    executions: Sequence[Mapping[str, Any]],
    lane: str,
) -> Dict[str, Any]:
    selected = [record for record in executions if record["lane"] == lane]
    latencies = sorted(float(record["result"]["elapsed_ms"]) for record in selected)
    p95_index = max(0, math.ceil(len(latencies) * 0.95) - 1)
    diagnostics = [
        record["result"].get("trace_diagnostics") or {}
        for record in selected
    ]
    score_deltas = [
        float(diagnostic["score_max_abs_delta"])
        for diagnostic in diagnostics
        if diagnostic.get("score_max_abs_delta") is not None
    ]
    return {
        "executions": len(selected),
        "successes": sum(
            record["result"]["status"] == "success"
            for record in selected
        ),
        "failures": sum(
            record["result"]["status"] != "success"
            for record in selected
        ),
        "average_ms": sum(latencies) / len(latencies) if latencies else None,
        "p95_ms": latencies[p95_index] if latencies else None,
        "total_tool_calls": sum(
            int(record["result"]["tool_call_count"])
            for record in selected
        ),
        "total_search_calls": sum(
            int(record["result"]["search_call_count"])
            for record in selected
        ),
        "protocol_violations": sum(
            len(record["result"]["protocol_violations"])
            for record in selected
        ),
        "trace_validation_errors": sum(
            len(record["result"]["trace_validation_errors"])
            for record in selected
        ),
        "tool_runtime_errors": sum(
            len(record["result"].get("tool_runtime_errors") or [])
            for record in selected
        ),
        "tool_argument_error_attempts": sum(
            int(record["result"].get("tool_argument_error_attempts") or 0)
            for record in selected
        ),
        "recovered_tool_argument_errors": sum(
            int(record["result"].get("recovered_tool_argument_errors") or 0)
            for record in selected
        ),
        "unrecovered_tool_argument_errors": sum(
            int(record["result"].get("unrecovered_tool_argument_errors") or 0)
            for record in selected
        ),
        "tool_name_error_attempts": sum(
            int(record["result"].get("tool_name_error_attempts") or 0)
            for record in selected
        ),
        "recovered_tool_name_errors": sum(
            int(record["result"].get("recovered_tool_name_errors") or 0)
            for record in selected
        ),
        "unrecovered_tool_name_errors": sum(
            int(record["result"].get("unrecovered_tool_name_errors") or 0)
            for record in selected
        ),
        "failure_categories": {
            category: sum(
                category in (record["result"].get("failure_categories") or [])
                for record in selected
            )
            for category in (
                "trace_contract_error",
                "tool_runtime_error",
                "tool_argument_error",
                "tool_name_error",
                "no_search_tool_call",
                "request_error",
                "empty_answer",
                "response_validation_error",
                "agent_execution_error",
            )
        },
        "trace_diagnostics": {
            "contract_version": AGENTIC_TRACE_CONTRACT,
            "document_pairs_compared": sum(
                int(diagnostic.get("document_pairs_compared") or 0)
                for diagnostic in diagnostics
            ),
            "metadata_mismatch_documents": sum(
                int(diagnostic.get("metadata_mismatch_documents") or 0)
                for diagnostic in diagnostics
            ),
            "score_mismatch_documents": sum(
                int(diagnostic.get("score_mismatch_documents") or 0)
                for diagnostic in diagnostics
            ),
            "score_max_abs_delta": max(score_deltas) if score_deltas else None,
            "identity_fallback_documents": sum(
                int(diagnostic.get("identity_fallback_documents") or 0)
                for diagnostic in diagnostics
            ),
            "excluded_tool_error_contexts": sum(
                int(diagnostic.get("excluded_tool_error_contexts") or 0)
                for diagnostic in diagnostics
            ),
        },
    }


def run_agentic_ab(
    cases_path: str,
    chunks_path: str,
    baseline_url: str,
    contextual_url: str,
    output_path: str,
    repeats: int = FORMAL_REPEATS,
    timeout: float = 1800,
    schedule_seed: int = FORMAL_SCHEDULE_SEED,
    smoke_per_type: Optional[int] = None,
    expected_agent_model: str = "deepseek-v3.2",
    http_session: Any = None,
    verified_lineage: Optional[Mapping[str, Any]] = None,
) -> Dict[str, Any]:
    """Call each Agent execution exactly once and freeze the resulting answers."""
    if timeout <= 0:
        raise ValueError("timeout must be positive")
    if repeats <= 0:
        raise ValueError("repeats must be positive")
    cases_artifact = load_artifact(cases_path, CASE_SCHEMA)
    chunks_artifact = load_artifact(chunks_path, CHUNK_SCHEMA)
    if cases_artifact.get("chunk_manifest_digest") != chunks_artifact.get(
        "artifact_digest"
    ):
        raise ValueError("case and chunk manifests do not match")
    cases = _select_cases(cases_artifact["cases"], smoke_per_type)
    if not cases:
        raise ValueError("no cases selected")
    if smoke_per_type is not None and repeats != 1:
        raise ValueError("the operational smoke must use exactly one repeat")
    if smoke_per_type is None and (
        len(cases) != FORMAL_CASES or repeats != FORMAL_REPEATS
    ):
        raise ValueError("formal I2 requires exactly 450 cases and 3 repeats")
    if smoke_per_type is None and schedule_seed != FORMAL_SCHEDULE_SEED:
        raise ValueError(
            "formal I2 requires schedule seed 20260725"
        )

    session = http_session
    if session is None:
        try:
            import requests

            session = requests.Session()
        except ModuleNotFoundError:
            session = _UrllibSession()
    baseline_config = _sanitize_service_config(
        _get_json(
            session,
            baseline_url.rstrip("/") + "/config",
            min(timeout, 30),
        )
    )
    contextual_config = _sanitize_service_config(
        _get_json(
            session,
            contextual_url.rstrip("/") + "/config",
            min(timeout, 30),
        )
    )
    validate_agentic_service_pair(
        baseline_config,
        contextual_config,
        chunks_artifact,
        expected_agent_model=expected_agent_model,
    )
    public_baseline = _public_agent_config(baseline_config)
    public_contextual = _public_agent_config(contextual_config)
    mode = "smoke" if smoke_per_type is not None else "formal"
    controlled_lineage = None
    if verified_lineage is not None:
        controlled_lineage = validate_agentic_lineage(
            verified_lineage,
            cases_artifact,
            chunks_artifact,
            baseline_config,
            contextual_config,
            mode=mode,
            expected_cases=len(cases),
            repeats=repeats,
        )
    schedule = build_agentic_schedule(cases, repeats, schedule_seed)
    schedule_digest = canonical_digest(schedule)
    identity_payload = {
        "run_kind": AGENTIC_RUN_KIND,
        "trace_contract": AGENTIC_TRACE_CONTRACT,
        "case_manifest_digest": cases_artifact["artifact_digest"],
        "chunk_manifest_digest": chunks_artifact["artifact_digest"],
        "case_ids": [case["case_id"] for case in cases],
        "repeats": repeats,
        "search_k": AGENTIC_SEARCH_K,
        "timeout_seconds": timeout,
        "schedule_seed": schedule_seed,
        "schedule_digest": schedule_digest,
        "baseline_url": public_endpoint_identity(baseline_url),
        "contextual_url": public_endpoint_identity(contextual_url),
        "baseline_config": public_baseline,
        "contextual_config": public_contextual,
        "selection": {"smoke_per_type": smoke_per_type},
        "verified_lineage_digest": (
            controlled_lineage.get("artifact_digest")
            if controlled_lineage is not None
            else None
        ),
    }
    run_identity = canonical_digest(identity_payload)
    paths = _output_paths(output_path)
    manifest_payload = {
        "schema_version": AGENTIC_MANIFEST_SCHEMA,
        **identity_payload,
        "run_identity": run_identity,
        "evidence_scope": (
            "agentic_operational_smoke"
            if controlled_lineage is not None and smoke_per_type is not None
            else "agentic_effectiveness"
            if controlled_lineage is not None
            else "agentic_uncontrolled_smoke"
            if smoke_per_type is not None
            else "agentic_uncontrolled"
        ),
        "verified_lineage": controlled_lineage,
        "expected_cases": len(cases),
        "expected_executions": len(schedule),
        "agent_request_attempts": 1,
        "agent_failure_policy": "fixed_denominator_zero_score_at_judge",
        "checkpoint_schema": AGENTIC_CHECKPOINT_SCHEMA,
        "checkpoint_format": "append-only-jsonl",
        "checkpoint_file": Path(paths["checkpoint"]).name,
        "expected_checkpoint_records": 1 + 2 * len(schedule),
        "created_at": datetime.now(timezone.utc).isoformat(),
    }
    if os.path.exists(paths["manifest"]):
        manifest = load_artifact(paths["manifest"], AGENTIC_MANIFEST_SCHEMA)
        if manifest.get("run_identity") != run_identity:
            raise ValueError("existing Agentic manifest belongs to another run")
        for field in (
            "schedule_digest",
            "expected_executions",
            "checkpoint_schema",
            "checkpoint_format",
            "checkpoint_file",
            "expected_checkpoint_records",
        ):
            if manifest.get(field) != manifest_payload.get(field):
                raise ValueError(
                    f"existing Agentic manifest has incompatible {field}"
                )
    else:
        manifest = write_artifact(paths["manifest"], manifest_payload)

    expected_ids = [str(item["execution_id"]) for item in schedule]
    if len(set(expected_ids)) != len(expected_ids):
        raise ValueError("Agentic schedule contains duplicate execution IDs")
    completed: Dict[str, Dict[str, Any]] = {}
    checkpoint_records = 0
    answers: Optional[Dict[str, Any]] = None
    if os.path.exists(paths["answers"]):
        answers = load_artifact(paths["answers"], AGENTIC_ANSWERS_SCHEMA)
        if answers.get("run_identity") != run_identity:
            raise ValueError("existing frozen answers belong to another run")
        if answers.get("manifest_digest") != manifest["artifact_digest"]:
            raise ValueError("existing frozen answers reference another manifest")
        if answers.get("schedule_digest") != schedule_digest:
            raise ValueError("existing frozen answers reference another schedule")
        if answers.get("expected_executions") != len(schedule):
            raise ValueError("existing frozen answers have an invalid expected count")
        if answers.get("completed_executions") != len(schedule):
            raise ValueError("existing frozen answers are incomplete")
        if answers.get("execution_ids") != expected_ids:
            raise ValueError("existing frozen answers have invalid execution IDs")
        if not os.path.exists(paths["checkpoint"]):
            raise ValueError("frozen answers checkpoint is missing")
        completed, checkpoint_records = _load_checkpoint_records(
            paths["checkpoint"],
            run_identity=run_identity,
            manifest_digest=manifest["artifact_digest"],
            schedule_digest=schedule_digest,
            expected_executions=len(schedule),
            schedule=schedule,
        )
        if answers.get("checkpoint_sha256") != file_digest(paths["checkpoint"]):
            raise ValueError("frozen answers checkpoint digest does not match")
        if answers.get("checkpoint_records") != checkpoint_records:
            raise ValueError("frozen answers checkpoint record count does not match")
        answer_executions = answers.get("executions")
        if not isinstance(answer_executions, list):
            raise ValueError("existing frozen answers have no execution list")
        answer_ids = [
            str(record.get("execution_id") or "")
            for record in answer_executions
            if isinstance(record, Mapping)
        ]
        if answer_ids != expected_ids or len(answer_executions) != len(schedule):
            raise ValueError("existing frozen answers are not in schedule order")
        for record in answer_executions:
            if not isinstance(record, Mapping):
                raise ValueError("existing frozen answers contain an invalid execution")
            result = record.get("result")
            if not isinstance(result, Mapping) or result.get("status") == "started":
                raise ValueError(
                    "existing frozen answers contain unfinished executions"
                )
        if set(completed) != set(expected_ids):
            raise ValueError("frozen answers checkpoint is incomplete")
        checkpoint_executions = [
            completed[execution_id]
            for execution_id in expected_ids
        ]
        if canonical_digest(answer_executions) != canonical_digest(
            checkpoint_executions
        ):
            raise ValueError("frozen answers differ from their checkpoint")
        completed = {
            str(record["execution_id"]): record
            for record in answer_executions
        }
    elif os.path.exists(paths["checkpoint"]):
        completed, checkpoint_records = _load_checkpoint_records(
            paths["checkpoint"],
            run_identity=run_identity,
            manifest_digest=manifest["artifact_digest"],
            schedule_digest=schedule_digest,
            expected_executions=len(schedule),
            schedule=schedule,
            repair_torn_tail=True,
        )
    else:
        _append_checkpoint_record(
            paths["checkpoint"],
            {
                "schema_version": AGENTIC_CHECKPOINT_SCHEMA,
                "record_type": "header",
                "run_identity": run_identity,
                "manifest_digest": manifest["artifact_digest"],
                "schedule_digest": schedule_digest,
                "expected_executions": len(schedule),
                "execution_ids": expected_ids,
                "created_at": datetime.now(timezone.utc).isoformat(),
            },
        )
        checkpoint_records = 1

    cases_by_id = {str(case["case_id"]): case for case in cases}
    chunks_by_id, chunks_by_hash = _chunk_indexes(chunks_artifact)

    def append_execution_checkpoint(execution: Mapping[str, Any]) -> None:
        nonlocal checkpoint_records
        execution_id = str(execution.get("execution_id") or "")
        _append_checkpoint_record(
            paths["checkpoint"],
            {
                "schema_version": AGENTIC_CHECKPOINT_SCHEMA,
                "record_type": "execution",
                "run_identity": run_identity,
                "manifest_digest": manifest["artifact_digest"],
                "schedule_digest": schedule_digest,
                "execution_id": execution_id,
                "execution": execution,
            },
        )
        checkpoint_records += 1

    for execution_id, record in list(completed.items()):
        if record.get("result", {}).get("status") == "started":
            previous_attempt = record.get("result", {}).get("request_attempt")
            attempt = (
                dict(previous_attempt)
                if isinstance(previous_attempt, Mapping)
                else {
                    "attempt": 1,
                    "started_at": None,
                }
            )
            attempt.update(
                {
                    "completed_at": None,
                    "status": "indeterminate",
                    "http_status": None,
                    "error_type": "UnknownCompletion",
                    "recovered_at": datetime.now(timezone.utc).isoformat(),
                }
            )
            recovered = _empty_result(
                "indeterminate",
                "previous Agent request had unknown completion state; "
                "it was not sampled again",
            )
            recovered["request_attempt"] = attempt
            terminal_record = dict(record)
            terminal_record["result"] = recovered
            append_execution_checkpoint(terminal_record)
            completed[execution_id] = terminal_record

    if answers is None:
        for scheduled in schedule:
            execution_id = str(scheduled["execution_id"])
            if execution_id in completed:
                continue
            case = cases_by_id[str(scheduled["case_id"])]
            lane = str(scheduled["lane"])
            service_url = baseline_url if lane == "baseline" else contextual_url
            request_started_at = datetime.now(timezone.utc).isoformat()
            started_result = _empty_result(
                "started",
                "Agent request completion has not been observed",
            )
            started_result["request_attempt"] = {
                "attempt": 1,
                "started_at": request_started_at,
                "completed_at": None,
                "status": "started",
                "http_status": None,
                "error_type": None,
            }
            started_record = {
                **scheduled,
                "dataset_index": case["dataset_index"],
                "question": case["question"],
                "ground_truth": case["answer"],
                "question_type": case["question_type"],
                "result": started_result,
            }
            append_execution_checkpoint(started_record)
            completed[execution_id] = started_record
            terminal_record = dict(started_record)
            terminal_record["result"] = _answer_once(
                session,
                service_url,
                case,
                timeout,
                chunks_by_id,
                chunks_by_hash,
                request_started_at,
            )
            append_execution_checkpoint(terminal_record)
            completed[execution_id] = terminal_record

        executions = [completed[execution_id] for execution_id in expected_ids]
        if any(
            record.get("result", {}).get("status") == "started"
            for record in executions
        ):
            raise ValueError("Agentic checkpoint still has unfinished executions")
        checkpoint_sha256 = file_digest(paths["checkpoint"])
        answers = write_artifact(
            paths["answers"],
            {
                "schema_version": AGENTIC_ANSWERS_SCHEMA,
                "run_identity": run_identity,
                "manifest_digest": manifest["artifact_digest"],
                "schedule_digest": schedule_digest,
                "checkpoint_sha256": checkpoint_sha256,
                "checkpoint_records": checkpoint_records,
                "expected_executions": len(schedule),
                "completed_executions": len(executions),
                "execution_ids": expected_ids,
                "executions": executions,
            },
        )
    else:
        executions = [completed[execution_id] for execution_id in expected_ids]
        checkpoint_sha256 = str(answers["checkpoint_sha256"])

    runtime = {
        lane: _runtime_summary(executions, lane)
        for lane in ("baseline", "contextual")
    }
    execution_complete = len(executions) == len(schedule)
    agent_failures = {
        lane: int(runtime[lane]["failures"])
        for lane in ("baseline", "contextual")
    }
    trace_protocol_errors = sum(
        int(runtime[lane]["trace_validation_errors"])
        for lane in ("baseline", "contextual")
    )
    protocol_valid = all(
        runtime[lane]["trace_validation_errors"] == 0
        for lane in ("baseline", "contextual")
    )
    smoke = (
        evaluate_agentic_smoke_gate(
            agent_failures,
            len(executions) // 2,
            chain_complete=execution_complete,
            protocol_errors=trace_protocol_errors,
        )
        if smoke_per_type is not None
        else None
    )
    formal_answers_eligible = (
        controlled_lineage is not None
        and smoke_per_type is None
        and len(cases) == FORMAL_CASES
        and repeats == FORMAL_REPEATS
        and execution_complete
        and protocol_valid
    )
    report_payload = {
        "schema_version": AGENTIC_REPORT_SCHEMA,
        "run_kind": AGENTIC_RUN_KIND,
        "trace_contract": AGENTIC_TRACE_CONTRACT,
        "evidence_scope": manifest["evidence_scope"],
        "evidence_status": (
            "valid"
            if execution_complete
            and protocol_valid
            and (smoke is None or smoke["decision"] == "pass")
            else "insufficient"
        ),
        "run_identity": run_identity,
        "manifest_digest": manifest["artifact_digest"],
        "answers_digest": answers["artifact_digest"],
        "checkpoint_schema": AGENTIC_CHECKPOINT_SCHEMA,
        "checkpoint_file": Path(paths["checkpoint"]).name,
        "checkpoint_sha256": checkpoint_sha256,
        "checkpoint_records": checkpoint_records,
        "expected_cases": len(cases),
        "repeats": repeats,
        "expected_executions": len(schedule),
        "completed_executions": len(executions),
        "formal_answers_eligible": formal_answers_eligible,
        "protocol_valid": protocol_valid,
        "protocol_errors": trace_protocol_errors,
        "runtime": runtime,
        "paired_completed_cases": sum(
            all(
                completed.get(f"r{repeat}:{case['case_id']}:{lane}", {})
                .get("result", {})
                .get("status")
                != "started"
                for lane in ("baseline", "contextual")
            )
            for repeat in range(repeats)
            for case in cases
        ),
        "smoke": smoke,
    }
    if os.path.exists(paths["report"]):
        report = load_artifact(paths["report"], AGENTIC_REPORT_SCHEMA)
        expected_fields = set(report_payload) | {
            "completed_at",
            "artifact_digest",
        }
        if set(report) != expected_fields or any(
            report.get(field) != expected
            for field, expected in report_payload.items()
        ):
            raise ValueError("existing Agentic report differs from frozen answers")
    else:
        report = write_artifact(
            paths["report"],
            {
                **report_payload,
                "completed_at": datetime.now(timezone.utc).isoformat(),
            },
        )
    return report
