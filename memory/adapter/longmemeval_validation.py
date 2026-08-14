#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent. All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
"""Validate maintained LongMemEval result and provenance artifacts."""

from __future__ import annotations

import gzip
import hashlib
import json
import math
import os
import re
import stat
from decimal import Decimal
from pathlib import Path
from typing import Any


class ResultEligibilityError(ValueError):
    """Raised when an input cannot be used in a maintained report."""

    def __init__(self, path: Path, blockers: list[str]) -> None:
        self.path = path
        self.blockers = sorted(set(blockers))
        super().__init__(
            f"ineligible maintained result {path}: "
            + "; ".join(self.blockers)
        )


_RUN_MANIFEST_SCHEMA_VERSION = 1
_RESULT_SCHEMA_VERSION = 1
_BUILD_PLAN_VERSION = 1
_MAINTAINED_RESULT_ORIGIN = "native_runner"
_BUILD_PROTOCOL = "turn-pair-fragment"
_AUTO_TEMPORAL_CONTEXT = "extractor_reference_date"
_MEM0_TEMPORAL_CONTEXT = "custom_prompt_reference_date"
_TEMPORAL_REFERENCE_SOURCE = "build_plan_session_observation_time"
_TEMPORAL_REFERENCE_FORMAT = "YYYY-MM-DD"
_TRACE_SELECTION_SCHEMA = "longmemeval.build_trace_selection/v1"
_TRACE_SCHEMA = "longmemeval.build_trace/v1"
_TRACE_PURPOSE = "best-effort-diagnostic"
_TRACE_COMPARABILITY = "backend-specific-not-cross-comparable"
_TRACE_MAX_FILE_BYTES = 64 << 20
_TRACE_MAX_DECODED_BYTES = 256 << 20
_TRACE_MAX_RECORDS = 250_000
_TRACE_MAX_LINE_BYTES = 4 << 20
_RESULT_ARTIFACT_MAX_FILE_BYTES = 64 << 20
_RESULT_ARTIFACT_MAX_TOTAL_BYTES = 512 << 20
_RESULT_ARTIFACT_MAX_FILES = 100_000
_IMMUTABLE_REVISION_PATTERN = re.compile(
    r"^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64}|sha256:[0-9a-fA-F]{64})$"
)
_FORBIDDEN_DIAGNOSTIC_MARKERS = (
    "recovered_from_logs",
    "recovered",
    "diagnostic",
    "diagnostic_only",
)
_COMPARISON_CONFIG_KEYS = (
    "question_types",
    "max_tasks",
    "retrieval_top_k",
    "build_max_tokens",
    "build_tokenizer",
    "build_tokenizer_model",
    "build_tokenizer_encoding",
    "build_stats",
    "replay_digest",
    "build_plan_digest",
    "max_retries",
    "answer_max_tokens",
    "judge_max_tokens",
    "transport_retry_enabled",
    "transport_retry_strategy",
    "temporal_reference_source",
    "temporal_reference_format",
)


def _case_ids(cases_payload: list[Any]) -> list[str]:
    return [str(case.get("question_id") or "") for case in cases_payload]


def _validate_maintained_publication(
    raw: dict[str, Any],
    path: Path,
) -> tuple[dict[str, Any], dict[str, Any]]:
    blockers = _diagnostic_marker_blockers(raw)
    publication = raw.get("publication")
    if not isinstance(publication, dict):
        blockers.append(
            "publication metadata is missing; historical input is diagnostic only"
        )
        raise ResultEligibilityError(path, blockers)

    if int(publication.get("schema_version") or 0) != _RESULT_SCHEMA_VERSION:
        blockers.append("unsupported or missing publication schema")
    if publication.get("classification") != "maintained":
        blockers.append("result classification is not maintained")
    if publication.get("origin") != _MAINTAINED_RESULT_ORIGIN:
        blockers.append("maintained publication origin must be native_runner")
    if publication.get("eligible") is not True:
        blockers.append("result publication is marked ineligible")
    blockers.extend(str(item) for item in publication.get("blockers") or [])

    run_manifest = publication.get("run_manifest") or {}
    if not isinstance(run_manifest, dict):
        blockers.append("published run manifest metadata is invalid")
        run_manifest = {}
    if int(run_manifest.get("schema_version") or 0) != _RUN_MANIFEST_SCHEMA_VERSION:
        blockers.append(
            f"published run manifest schema must be {_RUN_MANIFEST_SCHEMA_VERSION}"
        )
    compatibility = str(run_manifest.get("compatibility_digest") or "")
    if not compatibility.startswith("sha256:"):
        blockers.append("run compatibility digest is missing or invalid")
    comparison = str(run_manifest.get("comparison_digest") or "")
    if not comparison.startswith("sha256:"):
        blockers.append("run comparison digest is missing or invalid")

    cases_payload = raw.get("cases")
    if not isinstance(cases_payload, list):
        blockers.append("result does not contain top-level cases[]")
        cases_payload = []
    elif any(not isinstance(case, dict) for case in cases_payload):
        blockers.append("result cases[] contains a non-object record")
        cases_payload = [
            case if isinstance(case, dict) else {}
            for case in cases_payload
        ]
    case_ids = _case_ids(cases_payload)
    denominator = publication.get("fixed_denominator") or {}
    if not isinstance(denominator, dict):
        blockers.append("fixed denominator metadata is invalid")
        denominator = {}
    denominator_ids = [str(item) for item in denominator.get("case_ids") or []]
    if int(denominator.get("total_cases") or 0) != len(denominator_ids):
        blockers.append("fixed denominator total does not match case_ids")
    if case_ids != denominator_ids:
        blockers.append("cases[] does not match fixed denominator IDs/order")
    expected_digest = _go_json_digest(denominator_ids)
    if denominator.get("digest") != expected_digest:
        blockers.append("fixed denominator digest is invalid")

    statuses = {"succeeded", "failed", "judge_failed"}
    for case in cases_payload:
        case_id = str(case.get("question_id") or "")
        if str(case.get("status") or "") not in statuses:
            blockers.append(f"case {case_id} has a non-terminal status")

    blockers.extend(_validate_cost_schema(raw.get("cost")))
    blockers.extend(
        _validate_publication_artifacts(path.parent, publication.get("artifacts"))
    )
    blockers.extend(
        _validate_generated_artifact_contents(raw, path.parent, publication)
    )
    manifest, manifest_blockers = _validate_immutable_run_manifest(
        path.parent / "run_manifest.json",
        compatibility,
        comparison,
        denominator_ids,
        raw,
    )
    blockers.extend(manifest_blockers)
    scenario = str((raw.get("metadata") or {}).get("scenario") or "")
    artifacts = publication.get("artifacts") or {}
    if scenario in {"auto", "mem0_oss"} and "build_trace" not in artifacts:
        blockers.append("required build_trace artifact is missing")
    elif scenario in {"auto", "mem0_oss"}:
        blockers.extend(
            _validate_build_trace_selection(
                path.parent,
                raw,
                denominator_ids,
                artifacts.get("build_trace"),
                manifest,
            )
        )
    if blockers:
        raise ResultEligibilityError(path, blockers)
    return publication, manifest


def _validate_immutable_run_manifest(
    path: Path,
    compatibility: str,
    comparison: str,
    case_ids: list[str],
    result: dict[str, Any],
) -> tuple[dict[str, Any], list[str]]:
    try:
        manifest = _read_strict_run_manifest(path)
    except (OSError, ValueError):
        return {}, ["immutable run manifest is invalid"]
    blockers = _run_manifest_unknown_field_blockers(manifest)
    if int(manifest.get("schema_version") or 0) != _RUN_MANIFEST_SCHEMA_VERSION:
        blockers.append("unsupported or missing run manifest schema")
    if manifest.get("compatibility_digest") != compatibility:
        blockers.append("result/run-manifest compatibility digest mismatch")
    try:
        calculated = _run_compatibility_digest(manifest)
    except (TypeError, ValueError) as exc:
        blockers.append(f"run compatibility digest cannot be calculated: {exc}")
    else:
        if calculated != manifest.get("compatibility_digest"):
            blockers.append("run compatibility digest is invalid")
    if manifest.get("comparison_digest") != comparison:
        blockers.append("result/run-manifest comparison digest mismatch")
    try:
        calculated_comparison = _run_comparison_digest(manifest)
    except (TypeError, ValueError) as exc:
        blockers.append(f"run comparison digest cannot be calculated: {exc}")
    else:
        if calculated_comparison != manifest.get("comparison_digest"):
            blockers.append("run comparison digest is invalid")
    declared_blockers = [
        str(item) for item in manifest.get("official_blockers") or []
    ]
    derived_blockers = _derive_manifest_blockers(manifest)
    if declared_blockers != derived_blockers:
        blockers.append("run manifest official blockers do not match provenance")
    reproducible = not derived_blockers
    status = "eligible" if reproducible else "blocked"
    if manifest.get("reproducible") is not reproducible or (
        manifest.get("official_status") != status
    ):
        blockers.append("run manifest official eligibility does not match provenance")
    blockers.extend(declared_blockers)
    if [str(item) for item in manifest.get("case_ids") or []] != case_ids:
        blockers.append("run manifest case IDs/order mismatch")
    run = manifest.get("run") or {}
    if int(run.get("effective_top_k") or 0) != 20:
        blockers.append("effective retrieval top-k must be 20")
    if run.get("build_protocol") != _BUILD_PROTOCOL:
        blockers.append("unsupported build protocol")
    metadata = result.get("metadata") or {}
    if not isinstance(metadata, dict):
        blockers.append("result metadata is invalid")
        metadata = {}
    if int(metadata.get("run_manifest_version") or 0) != _RUN_MANIFEST_SCHEMA_VERSION:
        blockers.append("result metadata run manifest schema does not match")
    if metadata.get("run_compatibility_digest") != compatibility:
        blockers.append("result metadata compatibility digest does not match")
    if metadata.get("run_comparison_digest") != comparison:
        blockers.append("result metadata comparison digest does not match")
    if run.get("scenario") != metadata.get("scenario"):
        blockers.append("result scenario does not match run manifest")
    if str(run.get("backend") or "") != str(
        metadata.get("memory_backend") or ""
    ):
        blockers.append("result backend does not match run manifest")
    config = metadata.get("config") or {}
    if run.get("model_name") != config.get("model_name"):
        blockers.append("result model does not match run manifest")
    if run.get("embed_model_name") != config.get("embed_model_name"):
        blockers.append("result embedding model does not match run manifest")
    if str(run.get("backend_version") or "") != str(
        config.get("mem0_version") or ""
    ):
        blockers.append("result backend version does not match run manifest")
    if str(run.get("backend_revision") or "") != str(
        config.get("mem0_revision") or ""
    ):
        blockers.append("result backend revision does not match run manifest")
    if str((manifest.get("config") or {}).get("trace_content_mode") or "") != str(
        config.get("trace_content_mode") or ""
    ):
        blockers.append("result trace content mode does not match run manifest")
    result_top_k = int(config.get("retrieval_top_k") or 0)
    if result_top_k > 0 and int(run.get("effective_top_k") or 0) != result_top_k:
        blockers.append("result retrieval top-k does not match run manifest")
    blockers.extend(_validate_memory_build_identity(manifest, metadata))
    blockers.extend(_validate_input_artifacts(path.parent.parent, manifest))
    return manifest, blockers


def _validate_memory_build_identity(
    manifest: dict[str, Any],
    metadata: dict[str, Any],
) -> list[str]:
    memory_build = metadata.get("memory_build")
    if not isinstance(memory_build, dict):
        return ["result memory-build metadata is missing"]
    run = manifest.get("run") or {}
    config = manifest.get("config") or {}
    blockers: list[str] = []
    if memory_build.get("protocol") != run.get("build_protocol"):
        blockers.append("result memory-build protocol does not match run manifest")
    if memory_build.get("temporal_context") != run.get("temporal_context"):
        blockers.append("result temporal context does not match run manifest")
    for key in (
        "temporal_reference_source",
        "temporal_reference_format",
    ):
        if memory_build.get(key) != config.get(key):
            blockers.append(f"result {key} does not match run manifest")
    if run.get("scenario") != "mem0_oss":
        return blockers
    if memory_build.get("custom_extraction_prompt") is not True:
        blockers.append(
            "result does not confirm the Mem0 OSS custom extraction prompt"
        )
    if memory_build.get("observation_prompt_verified") is not True:
        blockers.append(
            "result does not confirm the Mem0 OSS observation-prompt capability"
        )
    for result_key, manifest_key in (
        ("preflight_digest", "mem0_preflight_digest"),
        ("environment_lock_digest", "mem0_environment_lock_digest"),
    ):
        if memory_build.get(result_key) != config.get(manifest_key):
            blockers.append(
                f"result {result_key} does not match run manifest"
            )
    return blockers


def _read_strict_run_manifest(path: Path) -> dict[str, Any]:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > 16 << 20:
        raise ValueError("run manifest must be a bounded regular file")
    data = path.read_bytes()

    def reject_constant(value: str) -> None:
        raise ValueError(f"invalid JSON number {value}")

    value = json.loads(data.decode("utf-8"), parse_constant=reject_constant)
    if not isinstance(value, dict):
        raise ValueError("run manifest top-level value must be an object")
    return value


def _run_manifest_unknown_field_blockers(manifest: dict[str, Any]) -> list[str]:
    blockers: list[str] = []

    def check(value: Any, allowed: set[str], label: str) -> dict[str, Any]:
        if not isinstance(value, dict):
            blockers.append(f"run manifest {label} must be an object")
            return {}
        for key in value:
            if key not in allowed:
                blockers.append(f"run manifest {label} contains unknown field {key}")
        return value

    check(manifest, {
        "schema_version", "created_at", "compatibility_digest",
        "comparison_digest", "reproducible", "official_status",
        "official_blockers", "code", "artifacts", "case_ids", "run",
        "config", "unavailable",
    }, "root")
    code = check(manifest.get("code"), {
        "go_version", "benchmark", "trpc_agent_go_root_revision",
        "trpc_agent_go_modules",
    }, "code")
    check(code.get("benchmark"), {"revision", "dirty_state", "source"}, "benchmark")
    modules = code.get("trpc_agent_go_modules") or []
    if not isinstance(modules, list):
        blockers.append("run manifest trpc_agent_go_modules must be an array")
    else:
        module_fields = {
            "path", "requested_version", "effective_path", "effective_version",
            "revision", "checksum", "replaced", "local_replacement", "resolved",
        }
        for index, module in enumerate(modules):
            check(module, module_fields, f"module {index}")
    artifacts = check(manifest.get("artifacts"), {
        "dataset", "case_manifest", "canonical_replay", "build_plan",
    }, "artifacts")
    artifact_fields = {
        "configured", "available", "path", "digest", "unavailable_reason",
    }
    for name in ("dataset", "case_manifest", "canonical_replay", "build_plan"):
        check(artifacts.get(name), artifact_fields, f"artifact {name}")
    check(manifest.get("run"), {
        "scenario", "backend", "table", "auto_update_policy",
        "temporal_context", "model_name", "embed_model_name",
        "llm_endpoint_fingerprint", "embedding_endpoint_fingerprint",
        "tokenizer_name", "effective_top_k", "build_protocol",
        "case_manifest_schema_version", "case_manifest_method",
        "case_manifest_split",
        "backend_version", "backend_revision",
    }, "run")
    if not isinstance(manifest.get("config"), dict):
        blockers.append("run manifest config must be an object")
    if manifest.get("unavailable") is not None and not isinstance(
        manifest.get("unavailable"), dict
    ):
        blockers.append("run manifest unavailable must be an object")
    return blockers


def _derive_manifest_blockers(manifest: dict[str, Any]) -> list[str]:
    blockers: list[str] = []
    run = manifest.get("run") or {}
    if not isinstance(run, dict):
        return ["run identity is unavailable"]
    for key in (
        "scenario", "model_name", "embed_model_name", "tokenizer_name",
        "llm_endpoint_fingerprint", "embedding_endpoint_fingerprint",
        "build_protocol",
    ):
        if not str(run.get(key) or ""):
            blockers.append(f"{key} is unavailable")
    if run.get("build_protocol") not in {None, "", _BUILD_PROTOCOL}:
        blockers.append(f"unsupported build protocol {run.get('build_protocol')!r}")
    if run.get("scenario") == "auto" and not str(
        run.get("auto_update_policy") or ""
    ):
        blockers.append("auto_update_policy is unavailable")
    try:
        effective_top_k = int(run.get("effective_top_k") or 0)
    except (TypeError, ValueError):
        effective_top_k = 0
    if effective_top_k <= 0:
        blockers.append("effective_top_k is unavailable")
    if effective_top_k not in {0, 20}:
        blockers.append(f"effective_top_k is {effective_top_k}, want 20")
    try:
        case_manifest_schema = int(
            run.get("case_manifest_schema_version") or 0
        )
    except (TypeError, ValueError):
        case_manifest_schema = 0
    if case_manifest_schema != 1:
        blockers.append(
            f"case_manifest_schema_version is {case_manifest_schema}, want 1"
        )
    method = str(run.get("case_manifest_method") or "")
    split = str(run.get("case_manifest_split") or "")
    if not method:
        blockers.append("case_manifest_method is unavailable")
    if method == "full-category" and split:
        blockers.append("full-category case manifest must not declare a split")
    elif method == "stratified-sha256" and split not in {"dev", "holdout"}:
        blockers.append(
            "sampled case manifest must declare a dev or holdout split"
        )
    scenario = str(run.get("scenario") or "")
    if scenario not in {"auto", "mem0_oss"}:
        blockers.append("scenario is not eligible for maintained comparison")
    expected_temporal_context = {
        "auto": _AUTO_TEMPORAL_CONTEXT,
        "mem0_oss": _MEM0_TEMPORAL_CONTEXT,
    }.get(scenario)
    temporal_context = str(run.get("temporal_context") or "")
    if expected_temporal_context and temporal_context != expected_temporal_context:
        blockers.append(
            f"{scenario} temporal_context is {temporal_context!r}, "
            f"want {expected_temporal_context!r}"
        )

    code = manifest.get("code") or {}
    benchmark = code.get("benchmark") or {}
    if not str(benchmark.get("revision") or ""):
        blockers.append("benchmark Git revision is unavailable")
    dirty_state = str(benchmark.get("dirty_state") or "")
    if dirty_state == "dirty":
        blockers.append("benchmark worktree is dirty")
    elif dirty_state != "clean":
        blockers.append("benchmark worktree state is unavailable")
    if not str(code.get("trpc_agent_go_root_revision") or ""):
        blockers.append("trpc-agent-go root module revision is unavailable")
    modules = code.get("trpc_agent_go_modules") or []
    if not isinstance(modules, list) or not modules:
        blockers.append("trpc-agent-go module provenance is unavailable")
        modules = []
    for module in modules:
        if not isinstance(module, dict):
            blockers.append("trpc-agent-go module provenance is invalid")
            continue
        module_path = str(module.get("path") or "<unknown>")
        if module.get("local_replacement") is True:
            blockers.append(f"module {module_path} uses a local replacement")
        if module.get("resolved") is not True:
            blockers.append(f"module {module_path} is unresolved")
        if not str(module.get("revision") or ""):
            blockers.append(f"module {module_path} revision is unavailable")
    artifacts = manifest.get("artifacts") or {}
    for name in ("dataset", "case_manifest", "canonical_replay", "build_plan"):
        artifact = artifacts.get(name) or {}
        locator = str(artifact.get("path") or "")
        path_required = name in {"canonical_replay", "build_plan"}
        if (
            artifact.get("configured") is not True
            or artifact.get("available") is not True
            or not str(artifact.get("digest") or "").startswith("sha256:")
            or (
                path_required
                and (not locator or Path(locator).is_absolute())
            )
        ):
            blockers.append(f"{name} artifact is unavailable")
    if scenario == "mem0_oss":
        if not str(run.get("backend_version") or "").strip():
            blockers.append("Mem0 OSS version is unavailable")
        revision = str(run.get("backend_revision") or "").strip()
        if not revision:
            blockers.append("Mem0 OSS commit or image digest is unavailable")
        elif _IMMUTABLE_REVISION_PATTERN.fullmatch(revision) is None:
            blockers.append(
                "Mem0 OSS revision must be a full Git commit or image digest"
            )
    config = manifest.get("config") or {}
    if scenario == "mem0_oss":
        for key in (
            "mem0_preflight_digest",
            "mem0_environment_lock_digest",
        ):
            if not str(config.get(key) or "").startswith("sha256:"):
                blockers.append(f"{key} is unavailable")
        if config.get("mem0_observation_prompt_verified") is not True:
            blockers.append(
                "Mem0 OSS observation-prompt capability is not verified"
            )
        if str(config.get("mem0_runtime_llm_model") or "") != str(
            run.get("model_name") or ""
        ):
            blockers.append(
                "Mem0 OSS runtime LLM model does not match the benchmark model"
            )
        if str(config.get("mem0_runtime_embed_model") or "") != str(
            run.get("embed_model_name") or ""
        ):
            blockers.append(
                "Mem0 OSS runtime embedding model does not match the benchmark model"
            )
    if str(config.get("temporal_reference_source") or "") != (
        _TEMPORAL_REFERENCE_SOURCE
    ):
        blockers.append(
            "temporal_reference_source does not identify the immutable "
            "build-plan session observation time"
        )
    if str(config.get("temporal_reference_format") or "") != (
        _TEMPORAL_REFERENCE_FORMAT
    ):
        blockers.append("temporal_reference_format is not YYYY-MM-DD")
    if scenario == "auto" and config.get("auto_qa_only") is True:
        blockers.append(
            "auto QA-only reuse is not eligible for a maintained build comparison"
        )
    trace_mode = str(config.get("trace_content_mode") or "")
    if trace_mode == "full":
        blockers.append("full build-trace content is local diagnostic only")
    if trace_mode not in {"hash", "none"}:
        blockers.append("maintained build-trace content mode must be hash or none")
    return sorted(set(blockers))


def _diagnostic_marker_blockers(raw: dict[str, Any]) -> list[str]:
    blockers: list[str] = []
    for label, value in (("result", raw), ("metadata", raw.get("metadata"))):
        if not isinstance(value, dict):
            continue
        for marker in _FORBIDDEN_DIAGNOSTIC_MARKERS:
            if marker in value:
                blockers.append(f"{label} contains {marker} diagnostic marker")
    return blockers


def _validate_input_artifacts(
    base: Path,
    manifest: dict[str, Any],
) -> list[str]:
    blockers: list[str] = []
    artifacts = manifest.get("artifacts") or {}
    resolved: dict[str, Path] = {}
    for name in ("canonical_replay", "build_plan"):
        artifact = artifacts.get(name) or {}
        try:
            path = _resolve_input_locator(base, str(artifact.get("path") or ""))
            actual = _digest_input_artifact(path)
        except (OSError, ValueError):
            blockers.append(f"{name} artifact cannot be rehashed")
            continue
        resolved[name] = path
        if actual != str(artifact.get("digest") or ""):
            blockers.append(f"{name} artifact digest does not match actual content")
    replay_root = resolved.get("canonical_replay")
    build_root = resolved.get("build_plan")
    try:
        replay_index = _read_bounded_json_object(replay_root / "index.json")
    except (AttributeError, OSError, ValueError):
        blockers.append("canonical replay index is invalid")
        replay_index = {}
    try:
        build_index = _read_bounded_json_object(build_root / "index.json")
    except (AttributeError, OSError, ValueError):
        blockers.append("build plan index is invalid")
        build_index = {}
    dataset_digest = str(
        ((artifacts.get("dataset") or {}).get("digest") or "")
    )
    manifest_digest = str(
        ((artifacts.get("case_manifest") or {}).get("digest") or "")
    )
    if dataset_digest != "sha256:" + str(replay_index.get("dataset_digest") or ""):
        blockers.append("canonical replay dataset digest does not match provenance")
    if manifest_digest != "sha256:" + str(replay_index.get("manifest_digest") or ""):
        blockers.append("canonical replay manifest digest does not match provenance")
    config = manifest.get("config") or {}
    replay_digest = str(replay_index.get("replay_digest") or "")
    if str(config.get("replay_digest") or "") != replay_digest:
        blockers.append("canonical replay logical digest does not match provenance")
    build_digest = str(build_index.get("build_plan_digest") or "")
    if str(config.get("build_plan_digest") or "") != build_digest:
        blockers.append("build plan logical digest does not match provenance")
    if str(build_index.get("protocol") or "") != _BUILD_PROTOCOL:
        blockers.append("build plan protocol is not turn-pair-fragment")
    if int(build_index.get("version") or 0) != _BUILD_PLAN_VERSION:
        blockers.append("unsupported build plan version")
    build_config = build_index.get("config") or {}
    if str(build_config.get("replay_digest") or "") != replay_digest:
        blockers.append("build plan replay digest does not match canonical replay")
    if replay_root is not None and build_root is not None:
        blockers.extend(
            _validate_turn_pair_plan(
                replay_root,
                replay_index,
                build_root,
                build_index,
            )
        )
    return blockers


def _validate_turn_pair_plan(
    replay_root: Path,
    replay_index: dict[str, Any],
    build_root: Path,
    build_index: dict[str, Any],
) -> list[str]:
    replay_entries = replay_index.get("cases") or []
    build_entries = build_index.get("cases") or []
    if not isinstance(replay_entries, list) or not isinstance(build_entries, list):
        return ["replay or build plan case index is invalid"]
    replay_ids = [
        str(entry.get("case_id") or "")
        for entry in replay_entries
        if isinstance(entry, dict)
    ]
    build_ids = [
        str(entry.get("case_id") or "")
        for entry in build_entries
        if isinstance(entry, dict)
    ]
    if replay_ids != build_ids or len(replay_ids) != len(replay_entries):
        return ["build plan cases do not match canonical replay"]
    try:
        max_tokens = int((build_index.get("config") or {}).get("max_tokens"))
    except (TypeError, ValueError):
        return ["build plan max token count is invalid"]
    if max_tokens <= 0:
        return ["build plan max token count is invalid"]
    blockers: list[str] = []
    aggregate_stats = _empty_build_stats()
    valid_cases = 0
    for replay_entry, build_entry in zip(replay_entries, build_entries):
        try:
            replay_case = _read_bounded_json_object(
                _resolve_input_locator(
                    replay_root,
                    str(replay_entry.get("file") or ""),
                )
            )
            build_case = _read_bounded_json_object(
                _resolve_input_locator(
                    build_root,
                    str(build_entry.get("file") or ""),
                )
            )
            if (
                str(build_case.get("replay_digest") or "")
                != str((build_index.get("config") or {}).get("replay_digest") or "")
                or str(build_case.get("config_digest") or "")
                != str(build_index.get("config_digest") or "")
            ):
                raise ValueError("build case source digest mismatch")
            case_stats = _validate_turn_pair_case(
                replay_case,
                build_case,
                max_tokens,
            )
            _add_build_stats(aggregate_stats, case_stats)
            valid_cases += 1
        except (OSError, TypeError, ValueError):
            blockers.append(
                f"case {replay_entry.get('case_id')} build plan is not a turn-pair projection"
            )
    if valid_cases == len(build_entries):
        try:
            _require_build_stats(build_index.get("stats"), aggregate_stats, "index")
        except ValueError:
            blockers.append("build plan aggregate statistics mismatch")
    return blockers


def _validate_turn_pair_case(
    replay_case: dict[str, Any],
    build_case: dict[str, Any],
    max_tokens: int,
) -> dict[str, int]:
    if (
        int(replay_case.get("version") or 0) != 2
        or int(build_case.get("version") or 0) != _BUILD_PLAN_VERSION
        or str(replay_case.get("case_id") or "")
        != str(build_case.get("case_id") or "")
    ):
        raise ValueError("case identity or version mismatch")
    replay_sessions = replay_case.get("sessions") or []
    build_sessions = build_case.get("sessions") or []
    if (
        not isinstance(replay_sessions, list)
        or not isinstance(build_sessions, list)
        or len(replay_sessions) != len(build_sessions)
    ):
        raise ValueError("session count mismatch")
    if max_tokens <= 0:
        raise ValueError("max tokens must be positive")
    actual_stats = _empty_build_stats()
    case_id = str(build_case.get("case_id") or "")
    actual_stats["case_count"] = 1
    seen_sessions: set[str] = set()
    seen_pairs: set[str] = set()
    seen_chunks: set[str] = set()
    for session_index, (replay_session, build_session) in enumerate(
        zip(replay_sessions, build_sessions)
    ):
        if not isinstance(replay_session, dict) or not isinstance(build_session, dict):
            raise ValueError("session is not an object")
        if (
            _non_negative_int(
                replay_session.get("session_index"), "replay session index"
            )
            != session_index
            or _non_negative_int(
                build_session.get("session_index"), "build session index"
            )
            != session_index
            or str(replay_session.get("session_id") or "")
            != str(build_session.get("session_id") or "")
            or not str(replay_session.get("observation_time") or "")
            or str(replay_session.get("observation_time") or "")
            != str(build_session.get("observation_time") or "")
        ):
            raise ValueError("session identity mismatch")
        session_id = str(build_session.get("session_id") or "")
        if session_id in seen_sessions:
            raise ValueError("duplicate build session identity")
        seen_sessions.add(session_id)
        expected_groups = _turn_pair_groups(replay_session.get("turns") or [])
        pairs = build_session.get("pairs") or []
        if not isinstance(pairs, list) or len(pairs) != len(expected_groups):
            raise ValueError("turn-pair count mismatch")
        actual_stats["session_count"] += 1
        session_tokens = 0
        session_chunked = False
        for pair, expected_turns in zip(pairs, expected_groups):
            expected_ids = [str(turn.get("turn_id") or "") for turn in expected_turns]
            if (
                not isinstance(pair, dict)
                or not str(pair.get("pair_id") or "")
                or [str(item) for item in pair.get("source_turn_ids") or []]
                != expected_ids
            ):
                raise ValueError("turn-pair boundary mismatch")
            pair_id = str(pair["pair_id"])
            if pair_id in seen_pairs:
                raise ValueError("duplicate turn-pair identity")
            seen_pairs.add(pair_id)
            pair_audit = _validate_turn_pair_chunks(
                pair,
                expected_turns,
                max_tokens,
                seen_chunks,
            )
            actual_stats["pair_count"] += 1
            actual_stats["turn_count"] += len(expected_turns)
            chunks = pair.get("chunks") or []
            actual_stats["chunk_count"] += len(chunks)
            if len(chunks) > 1:
                actual_stats["chunked_pair_count"] += 1
                session_chunked = True
            for key in (
                "original_tokens",
                "final_tokens",
                "original_bytes",
                "final_bytes",
            ):
                actual_stats[key] += _non_negative_int(pair.get(key), key)
            original_tokens = _non_negative_int(
                pair.get("original_tokens"), "original_tokens"
            )
            session_tokens += original_tokens
            actual_stats["split_turn_count"] += pair_audit["split_turn_count"]
            actual_stats["max_original_turn_tokens"] = max(
                actual_stats["max_original_turn_tokens"],
                pair_audit["max_original_turn_tokens"],
            )
            actual_stats["max_original_pair_tokens"] = max(
                actual_stats["max_original_pair_tokens"], original_tokens
            )
            actual_stats["max_chunk_tokens"] = max(
                actual_stats["max_chunk_tokens"], pair_audit["max_chunk_tokens"]
            )
        if session_chunked:
            actual_stats["chunked_session_count"] += 1
        actual_stats["max_session_tokens"] = max(
            actual_stats["max_session_tokens"], session_tokens
        )
    if actual_stats["chunked_pair_count"]:
        actual_stats["fragmented_case_ids"] = [case_id]
    _require_build_stats(build_case.get("stats"), actual_stats, "case")
    return actual_stats


def _validate_turn_pair_chunks(
    pair: dict[str, Any],
    source_turns: list[dict[str, Any]],
    max_tokens: int,
    seen_chunks: set[str],
) -> dict[str, int]:
    chunks = pair.get("chunks") or []
    if not isinstance(chunks, list) or not chunks:
        raise ValueError("turn-pair chunks are missing")
    turns_by_id = {str(turn.get("turn_id") or ""): turn for turn in source_turns}
    if len(turns_by_id) != len(source_turns):
        raise ValueError("duplicate source turn identity")
    reconstructed = {turn_id: bytearray() for turn_id in turns_by_id}
    token_offsets = {turn_id: 0 for turn_id in turns_by_id}
    turn_chunks = {turn_id: set() for turn_id in turns_by_id}
    final_tokens = 0
    final_bytes = 0
    max_chunk_tokens = 0
    for chunk_index, chunk in enumerate(chunks):
        if not isinstance(chunk, dict):
            raise ValueError("chunk is not an object")
        chunk_id = str(chunk.get("chunk_id") or "")
        if (
            not chunk_id
            or chunk_id in seen_chunks
            or _non_negative_int(chunk.get("index"), "chunk index") != chunk_index
        ):
            raise ValueError("chunk identity or order mismatch")
        seen_chunks.add(chunk_id)
        token_count = _non_negative_int(chunk.get("token_count"), "chunk tokens")
        byte_count = _non_negative_int(chunk.get("byte_count"), "chunk bytes")
        if token_count > max_tokens:
            raise ValueError("chunk exceeds the configured token limit")
        max_chunk_tokens = max(max_chunk_tokens, token_count)
        parts = chunk.get("turns") or []
        if not isinstance(parts, list):
            raise ValueError("chunk turns must be an array")
        actual_chunk_tokens = 0
        actual_chunk_bytes = 0
        for part in parts:
            if not isinstance(part, dict):
                raise ValueError("chunk turn part is not an object")
            turn_id = str(part.get("source_turn_id") or "")
            source = turns_by_id.get(turn_id)
            if source is None:
                raise ValueError("chunk references a turn outside its pair")
            content = part.get("content")
            if not isinstance(content, str):
                raise ValueError("chunk content must be text")
            encoded = content.encode("utf-8")
            start_byte = _non_negative_int(part.get("start_byte"), "start byte")
            end_byte = _non_negative_int(part.get("end_byte"), "end byte")
            start_token = _non_negative_int(part.get("start_token"), "start token")
            end_token = _non_negative_int(part.get("end_token"), "end token")
            if (
                _non_negative_int(
                    part.get("source_turn_index"), "source turn index"
                )
                != _non_negative_int(source.get("turn_index"), "replay turn index")
                or str(part.get("role") or "") != str(source.get("role") or "")
                or start_byte != len(reconstructed[turn_id])
                or end_byte != start_byte + len(encoded)
                or start_token != token_offsets[turn_id]
                or end_token < start_token
            ):
                raise ValueError("chunk turn offsets or identity are not contiguous")
            reconstructed[turn_id].extend(encoded)
            token_offsets[turn_id] = end_token
            turn_chunks[turn_id].add(chunk_id)
            actual_chunk_bytes += len(encoded)
            actual_chunk_tokens += end_token - start_token
        if actual_chunk_bytes != byte_count or actual_chunk_tokens != token_count:
            raise ValueError("chunk accounting mismatch")
        final_bytes += byte_count
        final_tokens += token_count
    source_bytes = sum(
        len(str(turn.get("content") or "").encode("utf-8"))
        for turn in source_turns
    )
    original_tokens = _non_negative_int(pair.get("original_tokens"), "pair tokens")
    original_bytes = _non_negative_int(pair.get("original_bytes"), "pair bytes")
    if (
        original_tokens != final_tokens
        or _non_negative_int(pair.get("final_tokens"), "pair final tokens")
        != final_tokens
        or original_bytes != source_bytes
        or _non_negative_int(pair.get("final_bytes"), "pair final bytes")
        != final_bytes
        or original_bytes != final_bytes
    ):
        raise ValueError("turn-pair is not lossless")
    for turn_id, source in turns_by_id.items():
        if bytes(reconstructed[turn_id]) != str(source.get("content") or "").encode("utf-8"):
            raise ValueError("source turn content is not losslessly reconstructed")
    return {
        "split_turn_count": sum(
            1 for chunk_ids in turn_chunks.values() if len(chunk_ids) > 1
        ),
        "max_original_turn_tokens": max(token_offsets.values(), default=0),
        "max_chunk_tokens": max_chunk_tokens,
    }


def _non_negative_int(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"{label} must be an integer")
    if value < 0:
        raise ValueError(f"{label} must be a non-negative integer")
    return value


def _empty_build_stats() -> dict[str, Any]:
    return {
        "case_count": 0,
        "session_count": 0,
        "turn_count": 0,
        "pair_count": 0,
        "chunk_count": 0,
        "chunked_session_count": 0,
        "chunked_pair_count": 0,
        "split_turn_count": 0,
        "original_tokens": 0,
        "final_tokens": 0,
        "original_bytes": 0,
        "final_bytes": 0,
        "max_original_turn_tokens": 0,
        "max_original_pair_tokens": 0,
        "max_session_tokens": 0,
        "max_chunk_tokens": 0,
        "fragmented_case_ids": [],
    }


def _require_build_stats(value: Any, expected: dict[str, Any], label: str) -> None:
    optional = {"fragmented_case_ids"}
    if (
        not isinstance(value, dict)
        or set(value) - optional != set(expected) - optional
        or not set(value).issubset(set(expected))
    ):
        raise ValueError(f"{label} build statistics are invalid")
    actual = {
        key: _non_negative_int(value.get(key), f"{label} {key}")
        for key in expected
        if key not in optional
    }
    case_ids = value.get("fragmented_case_ids") or []
    if (
        not isinstance(case_ids, list)
        or any(not isinstance(item, str) or not item for item in case_ids)
        or case_ids != sorted(set(case_ids))
    ):
        raise ValueError(f"{label} fragmented case IDs are invalid")
    actual["fragmented_case_ids"] = case_ids
    if actual != expected:
        raise ValueError(f"{label} build statistics mismatch")


def _add_build_stats(target: dict[str, Any], source: dict[str, Any]) -> None:
    maximums = {
        "max_original_turn_tokens",
        "max_original_pair_tokens",
        "max_session_tokens",
        "max_chunk_tokens",
    }
    for key in target:
        if key == "fragmented_case_ids":
            target[key] = sorted(set(target[key]) | set(source[key]))
            continue
        if key in maximums:
            target[key] = max(target[key], source[key])
        else:
            target[key] += source[key]


def _turn_pair_source_ids(turns: Any) -> list[list[str]]:
    return [
        [str(turn.get("turn_id") or "") for turn in group]
        for group in _turn_pair_groups(turns)
    ]


def _turn_pair_groups(turns: Any) -> list[list[dict[str, Any]]]:
    if not isinstance(turns, list):
        raise ValueError("turns must be an array")
    groups: list[list[dict[str, Any]]] = []
    index = 0
    while index < len(turns):
        turn = turns[index]
        if not isinstance(turn, dict):
            raise ValueError("turn must be an object")
        role = str(turn.get("role") or "")
        turn_id = str(turn.get("turn_id") or "")
        if role not in {"user", "assistant"} or not turn_id:
            raise ValueError("turn identity is invalid")
        group = [turn]
        index += 1
        if role == "user" and index < len(turns):
            following = turns[index]
            if not isinstance(following, dict):
                raise ValueError("turn must be an object")
            if str(following.get("role") or "") == "assistant":
                following_id = str(following.get("turn_id") or "")
                if not following_id:
                    raise ValueError("turn identity is invalid")
                group.append(following)
                index += 1
        groups.append(group)
    return groups


def _read_bounded_json_object(
    path: Path,
    max_bytes: int = 16 << 20,
) -> dict[str, Any]:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > max_bytes:
        raise ValueError("artifact index must be a bounded regular file")
    with path.open("rb") as stream:
        opened = os.fstat(stream.fileno())
        if not stat.S_ISREG(opened.st_mode) or not os.path.samestat(info, opened):
            raise ValueError("artifact index changed while opening")
        data = stream.read(max_bytes + 1)
    if len(data) > max_bytes:
        raise ValueError("artifact index exceeds its size limit")

    def reject_constant(value: str) -> None:
        raise ValueError(f"invalid JSON number {value}")

    value = json.loads(
        data.decode("utf-8"),
        parse_constant=reject_constant,
    )
    if not isinstance(value, dict):
        raise ValueError("artifact index must be a JSON object")
    return value


def _resolve_input_locator(base: Path, locator: str) -> Path:
    locator = locator.strip()
    if (
        not locator
        or "\\" in locator
        or locator.startswith("//")
        or (len(locator) >= 2 and locator[0].isalpha() and locator[1] == ":")
    ):
        raise ValueError("input artifact locator must be portable and relative")
    relative = Path(locator)
    if relative.is_absolute() or relative == Path("."):
        raise ValueError("input artifact locator must identify a child artifact")
    absolute_base = Path(os.path.abspath(base))
    candidate = Path(os.path.abspath(absolute_base / relative))
    try:
        candidate.relative_to(absolute_base)
    except ValueError as exc:
        raise ValueError("input artifact locator escapes its base") from exc
    current = absolute_base
    if stat.S_ISLNK(current.lstat().st_mode):
        raise ValueError("input artifact base must not be a symbolic link")
    for component in candidate.relative_to(absolute_base).parts:
        current /= component
        try:
            mode = current.lstat().st_mode
        except FileNotFoundError:
            break
        if stat.S_ISLNK(mode):
            raise ValueError("input artifact locator traverses a symbolic link")
    return candidate


def _digest_input_artifact(path: Path) -> str:
    mode = path.lstat().st_mode
    if stat.S_ISLNK(mode):
        raise ValueError("symbolic-link inputs are not supported")
    if stat.S_ISREG(mode):
        return "sha256:" + _sha256_file_hex(path)
    if not stat.S_ISDIR(mode):
        raise ValueError("input artifact must be a regular file or directory")
    entries: list[tuple[Path, int]] = []

    def collect(directory: Path) -> None:
        for item in sorted(directory.iterdir(), key=lambda value: value.name):
            item_mode = item.lstat().st_mode
            entries.append((item, item_mode))
            if stat.S_ISDIR(item_mode):
                collect(item)

    collect(path)
    digest = hashlib.sha256()
    for item, item_mode in sorted(
        entries,
        key=lambda value: value[0].relative_to(path).as_posix(),
    ):
        relative = item.relative_to(path).as_posix()
        if stat.S_ISLNK(item_mode):
            raise ValueError("input artifact contains a symbolic link")
        if stat.S_ISDIR(item_mode):
            digest.update(f"D\0{relative}\n".encode("utf-8"))
            continue
        if not stat.S_ISREG(item_mode):
            raise ValueError("input artifact contains a non-regular file")
        digest.update(
            f"F\0{relative}\0{_sha256_file_hex(item)}\n".encode("utf-8")
        )
    return "sha256:" + digest.hexdigest()


def _sha256_file_hex(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _methodology_identity(manifest: dict[str, Any]) -> dict[str, Any]:
    artifacts = manifest.get("artifacts") or {}
    run = manifest.get("run") or {}
    return {
        "dataset": str((artifacts.get("dataset") or {}).get("digest") or ""),
        "case_manifest": str(
            (artifacts.get("case_manifest") or {}).get("digest") or ""
        ),
        "canonical_replay": str(
            (artifacts.get("canonical_replay") or {}).get("digest") or ""
        ),
        "build_plan": str(
            (artifacts.get("build_plan") or {}).get("digest") or ""
        ),
        "model_name": str(run.get("model_name") or ""),
        "embed_model_name": str(run.get("embed_model_name") or ""),
        "llm_endpoint_fingerprint": str(
            run.get("llm_endpoint_fingerprint") or ""
        ),
        "embedding_endpoint_fingerprint": str(
            run.get("embedding_endpoint_fingerprint") or ""
        ),
        "tokenizer_name": str(run.get("tokenizer_name") or ""),
        "effective_top_k": int(run.get("effective_top_k") or 0),
        "build_protocol": str(run.get("build_protocol") or ""),
    }


def _run_compatibility_digest(manifest: dict[str, Any]) -> str:
    run = manifest.get("run") or {}
    compatible_run: dict[str, Any] = {"scenario": str(run.get("scenario") or "")}
    for key in (
        "backend", "table", "auto_update_policy", "temporal_context",
    ):
        if run.get(key):
            compatible_run[key] = run[key]
    compatible_run["model_name"] = str(run.get("model_name") or "")
    compatible_run["embed_model_name"] = str(
        run.get("embed_model_name") or ""
    )
    compatible_run["llm_endpoint_fingerprint"] = str(
        run.get("llm_endpoint_fingerprint") or ""
    )
    compatible_run["embedding_endpoint_fingerprint"] = str(
        run.get("embedding_endpoint_fingerprint") or ""
    )
    if run.get("tokenizer_name"):
        compatible_run["tokenizer_name"] = run["tokenizer_name"]
    compatible_run["effective_top_k"] = int(run.get("effective_top_k") or 0)
    compatible_run["build_protocol"] = str(run.get("build_protocol") or "")
    compatible_run["case_manifest_schema_version"] = int(
        run.get("case_manifest_schema_version") or 0
    )
    if run.get("case_manifest_method"):
        compatible_run["case_manifest_method"] = run["case_manifest_method"]
    if run.get("case_manifest_split"):
        compatible_run["case_manifest_split"] = run["case_manifest_split"]
    for key in ("backend_version", "backend_revision"):
        if run.get(key):
            compatible_run[key] = run[key]
    payload: dict[str, Any] = {
        "reproducible": manifest.get("reproducible") is True,
        "official_status": str(manifest.get("official_status") or ""),
    }
    official_blockers = [
        str(item) for item in manifest.get("official_blockers") or []
    ]
    if official_blockers:
        payload["official_blockers"] = official_blockers
    payload.update({
        "code": _go_code_provenance(manifest.get("code")),
        "artifacts": _compatible_artifacts(manifest.get("artifacts")),
        "case_ids": [str(item) for item in manifest.get("case_ids") or []],
        "run": compatible_run,
        "config": _sort_json_maps(manifest.get("config")),
    })
    return _go_json_digest(payload)


def _run_comparison_digest(manifest: dict[str, Any]) -> str:
    run = manifest.get("run") or {}
    comparison_run: dict[str, Any] = {
        "model_name": str(run.get("model_name") or ""),
        "embed_model_name": str(run.get("embed_model_name") or ""),
        "llm_endpoint_fingerprint": str(
            run.get("llm_endpoint_fingerprint") or ""
        ),
        "embedding_endpoint_fingerprint": str(
            run.get("embedding_endpoint_fingerprint") or ""
        ),
    }
    if run.get("tokenizer_name"):
        comparison_run["tokenizer_name"] = run["tokenizer_name"]
    comparison_run["effective_top_k"] = int(run.get("effective_top_k") or 0)
    comparison_run["build_protocol"] = str(run.get("build_protocol") or "")
    config = manifest.get("config")
    if config is not None and not isinstance(config, dict):
        raise TypeError("run manifest config must be an object")
    selected_config = {
        key: (config or {}).get(key)
        for key in _COMPARISON_CONFIG_KEYS
    }
    comparison_code = _go_code_provenance(manifest.get("code"))
    benchmark = comparison_code.get("benchmark") or {}
    benchmark.pop("source", None)
    payload = {
        "code": comparison_code,
        "artifacts": _compatible_artifacts(manifest.get("artifacts")),
        "case_ids": [str(item) for item in manifest.get("case_ids") or []],
        "run": comparison_run,
        "config": _sort_json_maps(selected_config),
    }
    return _go_json_digest(payload)


def _compatible_artifacts(raw: Any) -> dict[str, Any]:
    artifacts = raw or {}
    if not isinstance(artifacts, dict):
        raise TypeError("run manifest artifacts must be an object")

    def compatible_artifact(name: str) -> dict[str, Any]:
        artifact = artifacts.get(name) or {}
        if not isinstance(artifact, dict):
            raise TypeError(f"run manifest artifact {name} must be an object")
        value: dict[str, Any] = {
            "configured": bool(artifact.get("configured")),
            "available": bool(artifact.get("available")),
        }
        digest = str(artifact.get("digest") or "")
        if digest:
            value["digest"] = digest
        return value

    return {
        "dataset": compatible_artifact("dataset"),
        "case_manifest": compatible_artifact("case_manifest"),
        "canonical_replay": compatible_artifact("canonical_replay"),
        "build_plan": compatible_artifact("build_plan"),
    }


def _go_code_provenance(raw: Any) -> dict[str, Any]:
    code = raw or {}
    if not isinstance(code, dict):
        raise TypeError("run manifest code must be an object")
    value: dict[str, Any] = {}
    if code.get("go_version"):
        value["go_version"] = code["go_version"]
    benchmark = code.get("benchmark") or {}
    if not isinstance(benchmark, dict):
        raise TypeError("run manifest benchmark provenance must be an object")
    benchmark_value: dict[str, Any] = {}
    if benchmark.get("revision"):
        benchmark_value["revision"] = benchmark["revision"]
    benchmark_value["dirty_state"] = str(benchmark.get("dirty_state") or "")
    if benchmark.get("source"):
        benchmark_value["source"] = benchmark["source"]
    value["benchmark"] = benchmark_value
    if code.get("trpc_agent_go_root_revision"):
        value["trpc_agent_go_root_revision"] = code[
            "trpc_agent_go_root_revision"
        ]
    modules = code.get("trpc_agent_go_modules") or []
    if not isinstance(modules, list):
        raise TypeError("trpc_agent_go_modules must be an array")
    if modules:
        module_values: list[dict[str, Any]] = []
        for module in modules:
            if not isinstance(module, dict):
                raise TypeError("trpc_agent_go_modules entries must be objects")
            item: dict[str, Any] = {"path": str(module.get("path") or "")}
            if module.get("requested_version"):
                item["requested_version"] = module["requested_version"]
            item["effective_path"] = str(module.get("effective_path") or "")
            for key in ("effective_version", "revision", "checksum"):
                if module.get(key):
                    item[key] = module[key]
            if module.get("replaced") is True:
                item["replaced"] = True
            if module.get("local_replacement") is True:
                item["local_replacement"] = True
            item["resolved"] = bool(module.get("resolved"))
            module_values.append(item)
        value["trpc_agent_go_modules"] = module_values
    return value


def _go_json_digest(payload: Any) -> str:
    encoded = _go_json_encode(payload)
    encoded = (
        encoded.replace("&", r"\u0026")
        .replace("<", r"\u003c")
        .replace(">", r"\u003e")
        .replace("\u2028", r"\u2028")
        .replace("\u2029", r"\u2029")
    )
    return "sha256:" + hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def _go_json_encode(value: Any) -> str:
    if value is None:
        return "null"
    if value is True:
        return "true"
    if value is False:
        return "false"
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False, allow_nan=False)
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        return _go_json_float(value)
    if isinstance(value, list):
        return "[" + ",".join(_go_json_encode(item) for item in value) + "]"
    if isinstance(value, dict):
        parts: list[str] = []
        for key, item in value.items():
            if not isinstance(key, str):
                raise TypeError("Go JSON object keys must be strings")
            parts.append(_go_json_encode(key) + ":" + _go_json_encode(item))
        return "{" + ",".join(parts) + "}"
    raise TypeError(f"unsupported Go JSON value: {type(value).__name__}")


def _go_json_float(value: float) -> str:
    if not math.isfinite(value):
        raise ValueError("Go JSON does not support non-finite floats")
    if value == 0:
        return "0"
    decimal = Decimal(repr(value)).normalize()
    absolute = abs(value)
    if 1e-6 <= absolute < 1e21:
        return format(decimal, "f")
    coefficient, exponent = format(decimal, "e").split("e", 1)
    exponent_value = int(exponent)
    exponent_text = (
        f"+{exponent_value}" if exponent_value >= 0 else str(exponent_value)
    )
    return coefficient + "e" + exponent_text


def _sort_json_maps(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: _sort_json_maps(value[key]) for key in sorted(value)}
    if isinstance(value, list):
        return [_sort_json_maps(item) for item in value]
    return value


def _validate_cost_schema(raw: Any) -> list[str]:
    if not isinstance(raw, dict):
        return ["phase-level cost artifact is missing"]
    blockers: list[str] = []
    if raw.get("partial"):
        blockers.append(
            "phase-level cost artifact is partial: "
            + str(raw.get("partial_reason") or "unspecified")
        )
    paths = (
        ("llm", "total"),
        ("llm", "memory_build"),
        ("llm", "qa"),
        ("llm", "judge"),
        ("embedding", "total"),
        ("embedding", "memory_build"),
        ("embedding", "qa_retrieval"),
    )
    for modality, phase in paths:
        bucket = ((raw.get(modality) or {}).get(phase))
        label = f"{modality}.{phase}"
        if not isinstance(bucket, dict):
            blockers.append(f"cost bucket {label} is missing")
            continue
        if "tokens_known" not in bucket or not isinstance(
            bucket.get("tokens_known"), bool
        ):
            blockers.append(f"cost bucket {label} lacks explicit tokens_known")
        values = (
            "calls",
            "requests",
            "cache_hits",
            "prompt_tokens",
            "completion_tokens",
            "total_tokens",
            "cached_tokens",
        )
        for key in values:
            try:
                value = int(bucket.get(key) or 0)
            except (TypeError, ValueError):
                blockers.append(f"cost bucket {label} has invalid {key}")
                continue
            if value < 0:
                blockers.append(f"cost bucket {label} contains negative {key}")
    return blockers


def _validate_publication_artifacts(root: Path, raw: Any) -> list[str]:
    if not isinstance(raw, dict):
        return ["publication artifacts are missing"]
    blockers: list[str] = []
    required = ("aggregate", "bad_cases", "bad_cases_en", "bad_cases_zh_cn")
    for name in required:
        artifact = raw.get(name)
        if not isinstance(artifact, dict):
            blockers.append(f"required result artifact is missing: {name}")
            continue
        error = _validate_artifact(root, artifact)
        if error:
            blockers.append(f"invalid {name} artifact: {error}")
    build_trace = raw.get("build_trace")
    if build_trace is not None:
        if not isinstance(build_trace, dict):
            blockers.append("invalid build_trace artifact metadata")
        else:
            error = _validate_artifact(root, build_trace)
            if error:
                blockers.append(f"invalid build_trace artifact: {error}")
    return blockers


def _validate_generated_artifact_contents(
    raw: dict[str, Any],
    root: Path,
    publication: dict[str, Any],
) -> list[str]:
    blockers: list[str] = []
    artifacts = publication.get("artifacts") or {}
    aggregate, aggregate_error = _read_json_artifact(
        root,
        artifacts.get("aggregate"),
    )
    bad_cases, bad_cases_error = _read_json_artifact(
        root,
        artifacts.get("bad_cases"),
    )
    if aggregate_error:
        blockers.append("aggregate artifact content is invalid: " + aggregate_error)
    if bad_cases_error:
        blockers.append("bad_cases artifact content is invalid: " + bad_cases_error)
    denominator = publication.get("fixed_denominator") or {}
    metadata = raw.get("metadata") or {}
    compatibility = str(
        (publication.get("run_manifest") or {}).get("compatibility_digest")
        or ""
    )
    comparison = str(
        (publication.get("run_manifest") or {}).get("comparison_digest")
        or ""
    )

    if aggregate is not None:
        expected_header = {
            "schema_version": _RESULT_SCHEMA_VERSION,
            "classification": "maintained",
            "scenario": str(metadata.get("scenario") or ""),
            "run_compatibility_digest": compatibility,
            "comparison_digest": comparison,
            "fixed_denominator": denominator,
            "summary": raw.get("summary"),
            "by_type": raw.get("by_type"),
        }
        if metadata.get("memory_backend"):
            expected_header["backend"] = metadata["memory_backend"]
        for key, expected in expected_header.items():
            if aggregate.get(key) != expected:
                blockers.append(
                    f"aggregate artifact does not match result field {key}"
                )
        blockers.extend(
            _compare_case_summaries(
                "aggregate",
                raw.get("cases") or [],
                aggregate.get("cases"),
            )
        )

    if bad_cases is not None:
        for key, expected in (
            ("schema_version", _RESULT_SCHEMA_VERSION),
            ("classification", "maintained"),
            ("scenario", str(metadata.get("scenario") or "")),
            ("run_compatibility_digest", compatibility),
            ("comparison_digest", comparison),
            ("fixed_denominator", denominator),
        ):
            if bad_cases.get(key) != expected:
                blockers.append(
                    f"bad_cases artifact does not match result field {key}"
                )
        if metadata.get("memory_backend") and bad_cases.get("backend") != metadata.get(
            "memory_backend"
        ):
            blockers.append("bad_cases artifact does not match result field backend")
        expected_bad_cases = [
            case
            for case in raw.get("cases") or []
            if not (
                case.get("status") == "succeeded"
                and case.get("correct") is True
            )
        ]
        blockers.extend(
            _compare_case_summaries(
                "bad_cases",
                expected_bad_cases,
                bad_cases.get("cases"),
            )
        )
    return blockers


def _read_json_artifact(
    root: Path,
    raw: Any,
) -> tuple[dict[str, Any] | None, str]:
    if not isinstance(raw, dict) or _validate_artifact(root, raw):
        return None, ""
    path = root / str(raw.get("path") or "")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None, "artifact is unreadable or is not strict JSON"
    if not isinstance(value, dict):
        return None, "top-level value is not an object"
    return value, ""


def _compare_case_summaries(
    artifact_name: str,
    expected_cases: list[dict[str, Any]],
    actual: Any,
) -> list[str]:
    if not isinstance(actual, list):
        return [f"{artifact_name} artifact does not contain cases[]"]
    expected = [_case_summary(case) for case in expected_cases]
    observed = [_case_summary(case) for case in actual]
    if observed != expected:
        return [f"{artifact_name} artifact case records do not match results"]
    return []


def _case_summary(case: dict[str, Any]) -> dict[str, Any]:
    return {
        "question_id": str(case.get("question_id") or ""),
        "question_type": str(case.get("question_type") or ""),
        "status": str(case.get("status") or ""),
        "correct": bool(case.get("correct")),
        "failure_stage": str(
            case.get("failure_stage") or _default_failure_stage(case)
        ),
        "build_observability": str(
            case.get("build_observability") or "unknown"
        ),
    }


def _default_failure_stage(case: dict[str, Any]) -> str:
    status = str(case.get("status") or "")
    if status == "judge_failed":
        return "judge_error"
    if status == "failed":
        return "evaluation_error"
    if status == "succeeded":
        return "success" if case.get("correct") is True else "answer_generation_miss"
    return "missing"


def _validate_artifact(root: Path, artifact: dict[str, Any]) -> str:
    rel_path = str(artifact.get("path") or "")
    expected_digest = str(artifact.get("sha256") or "")
    try:
        path = _resolve_input_locator(root, rel_path)
        actual_digest = _digest_path(path)
    except (OSError, ValueError):
        return "path is unsafe or artifact is not a regular file/directory"
    if actual_digest != expected_digest:
        return "digest mismatch"
    return ""


def _digest_path(path: Path) -> str:
    mode = path.lstat().st_mode
    if stat.S_ISLNK(mode):
        raise ValueError("symbolic links are not valid result artifacts")
    if stat.S_ISREG(mode):
        digest = hashlib.sha256()
        _hash_bounded_result_file(
            path,
            digest,
            _RESULT_ARTIFACT_MAX_FILE_BYTES,
        )
        return "sha256:" + digest.hexdigest()
    if not stat.S_ISDIR(mode):
        raise ValueError("result artifact must be a regular file or directory")
    files: list[Path] = []

    def collect(directory: Path) -> None:
        for item in sorted(directory.iterdir(), key=lambda value: value.name):
            item_mode = item.lstat().st_mode
            if stat.S_ISLNK(item_mode):
                raise ValueError("result artifact contains a symbolic link")
            if stat.S_ISDIR(item_mode):
                collect(item)
                continue
            if not stat.S_ISREG(item_mode):
                raise ValueError("result artifact contains a non-regular file")
            files.append(item)
            if len(files) > _RESULT_ARTIFACT_MAX_FILES:
                raise ValueError("result artifact exceeds the file-count limit")

    collect(path)
    digest = hashlib.sha256()
    files.sort(key=lambda item: item.relative_to(path).as_posix())
    total_bytes = 0
    for item in files:
        digest.update(item.relative_to(path).as_posix().encode("utf-8"))
        digest.update(b"\0")
        remaining = _RESULT_ARTIFACT_MAX_TOTAL_BYTES - total_bytes
        if remaining <= 0:
            raise ValueError("result artifact exceeds the total-size limit")
        total_bytes += _hash_bounded_result_file(
            item,
            digest,
            min(_RESULT_ARTIFACT_MAX_FILE_BYTES, remaining),
        )
    return "sha256:" + digest.hexdigest()


def _hash_bounded_result_file(
    path: Path,
    digest: Any,
    limit: int,
) -> int:
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_size > limit:
        raise ValueError("result artifact file is not regular or exceeds its limit")
    total = 0
    with path.open("rb") as stream:
        opened = os.fstat(stream.fileno())
        if not stat.S_ISREG(opened.st_mode) or not os.path.samestat(before, opened):
            raise ValueError("result artifact file changed while opening")
        while True:
            chunk = stream.read(min(1024 * 1024, limit - total + 1))
            if not chunk:
                break
            total += len(chunk)
            if total > limit:
                raise ValueError("result artifact file exceeds its limit")
            digest.update(chunk)
    return total


def _validate_build_trace_selection(
    root: Path,
    result: dict[str, Any],
    case_ids: list[str],
    artifact: Any,
    manifest: dict[str, Any],
) -> list[str]:
    if not isinstance(artifact, dict):
        return ["build_trace artifact metadata is invalid"]
    blockers: list[str] = []
    metadata = result.get("metadata") or {}
    config = metadata.get("config") or {}
    mode = str(config.get("trace_content_mode") or "")
    if artifact.get("purpose") != _TRACE_PURPOSE:
        blockers.append("build trace purpose is not best-effort diagnostic")
    if artifact.get("comparability") != _TRACE_COMPARABILITY:
        blockers.append("build trace does not prohibit cross-backend comparison")
    if mode not in {"hash", "none"} or artifact.get("content_mode") != mode:
        blockers.append("build trace content mode is incompatible with the result")
    if int(artifact.get("selected_cases") or 0) != len(case_ids):
        blockers.append("build trace selected case count is incompatible")
    locator = str(artifact.get("path") or "")
    relative = Path(locator)
    if (
        relative.as_posix() != locator
        or len(relative.parts) != 2
        or relative.parts[0] != "build_trace"
        or not _is_trace_selection_name(relative.parts[1])
    ):
        return blockers + ["build trace path is not a maintained selection"]
    try:
        trace_root = _resolve_input_locator(root, locator)
        root_mode = trace_root.lstat().st_mode
    except (OSError, ValueError):
        return blockers + ["build trace selection directory is unavailable"]
    if not stat.S_ISDIR(root_mode) or stat.S_ISLNK(root_mode):
        return blockers + ["build trace selection is not a regular directory"]
    index_path = trace_root / "manifest.json"
    try:
        index_info = index_path.lstat()
        if not stat.S_ISREG(index_info.st_mode) or index_info.st_size > 1 << 20:
            raise ValueError("invalid selection index")
        index_data = index_path.read_bytes()
        index = json.loads(index_data.decode("utf-8"))
        if not isinstance(index, dict):
            raise ValueError("selection index must be an object")
    except (OSError, ValueError):
        return blockers + ["build trace selection manifest is invalid"]
    expected_index_fields = {
        "schema_version", "purpose", "comparability", "scenario", "backend",
        "content_mode", "cases",
    }
    if set(index) - expected_index_fields:
        blockers.append("build trace selection manifest has unknown fields")
    if (
        index.get("schema_version") != _TRACE_SELECTION_SCHEMA
        or index.get("purpose") != _TRACE_PURPOSE
        or index.get("comparability") != _TRACE_COMPARABILITY
    ):
        blockers.append("build trace selection declaration is invalid")
    if (
        index.get("scenario") != metadata.get("scenario")
        or str(index.get("backend") or "")
        != str(metadata.get("memory_backend") or "")
        or index.get("content_mode") != mode
    ):
        blockers.append("build trace selection identity is incompatible")
    cases = index.get("cases")
    if not isinstance(cases, list) or len(cases) != len(case_ids):
        return blockers + ["build trace selection does not match the denominator"]
    try:
        canonical_index = _canonical_trace_selection(index)
    except (TypeError, ValueError):
        return blockers + ["build trace selection manifest is not canonical"]
    if index_data != canonical_index:
        blockers.append("build trace selection manifest is not canonical")
    expected_name = "maintained-" + hashlib.sha256(canonical_index).hexdigest()[:16]
    if trace_root.name != expected_name:
        blockers.append("build trace selection identity digest is invalid")

    expected_files = {"manifest.json"}
    case_results = {
        str(case.get("question_id") or ""): case
        for case in result.get("cases") or []
        if isinstance(case, dict)
    }
    try:
        expected_sources = _load_expected_trace_sources(
            root.parent,
            manifest,
            case_ids,
        )
    except (OSError, TypeError, ValueError):
        expected_sources = {}
        blockers.append("build trace sources cannot be derived from the build plan")
    for index_number, entry in enumerate(cases):
        case_id = case_ids[index_number]
        if not isinstance(entry, dict) or set(entry) != {"case_id", "file", "sha256"}:
            blockers.append(f"build trace selection entry is invalid for {case_id}")
            continue
        if entry.get("case_id") != case_id:
            blockers.append("build trace selection case order differs from denominator")
            continue
        file_name = str(entry.get("file") or "")
        base_name = _trace_file_name(case_id) + ".jsonl"
        if file_name not in {base_name, base_name + ".gz"} or ".attempt-" in file_name:
            blockers.append(f"build trace file name is not canonical for {case_id}")
            continue
        if file_name in expected_files:
            blockers.append("build trace selection contains duplicate files")
            continue
        expected_files.add(file_name)
        trace_path = trace_root / file_name
        try:
            trace_info = trace_path.lstat()
            if (
                not stat.S_ISREG(trace_info.st_mode)
                or trace_info.st_size > _TRACE_MAX_FILE_BYTES
            ):
                raise ValueError("trace must be a bounded regular file")
            if "sha256:" + _sha256_file_hex(trace_path) != entry.get("sha256"):
                raise ValueError("trace digest mismatch")
            trace_summary = _read_selected_trace(trace_path, case_id, mode)
        except (OSError, ValueError):
            blockers.append(f"selected build trace is invalid for {case_id}")
            continue
        case = case_results.get(case_id) or {}
        expected_stage = str(case.get("failure_stage") or _default_failure_stage(case))
        outcome = trace_summary["outcome"]
        try:
            _validate_trace_sources(
                trace_summary["sources"],
                expected_sources[case_id],
                str(outcome.get("failure_stage") or ""),
            )
        except (KeyError, TypeError, ValueError):
            blockers.append(
                f"selected build trace differs from the build plan for {case_id}"
            )
        if outcome.get("correct") is not case.get("correct"):
            blockers.append(f"selected build trace disagrees with correct for {case_id}")
        if str(outcome.get("failure_stage") or "") != expected_stage:
            blockers.append(f"selected build trace disagrees with failure stage for {case_id}")
        if str(outcome.get("build_observability") or "") != str(
            case.get("build_observability") or "unknown"
        ):
            blockers.append(
                f"selected build trace disagrees with build observability for {case_id}"
            )
        if not _equal_optional_metric(
            outcome.get("gold_session_recall"),
            case.get("gold_session_recall"),
        ):
            blockers.append(f"selected build trace disagrees with recall for {case_id}")
    try:
        observed_files = set()
        for item in trace_root.iterdir():
            if not stat.S_ISREG(item.lstat().st_mode):
                blockers.append("build trace selection contains a non-regular artifact")
                continue
            observed_files.add(item.name)
        if observed_files != expected_files:
            blockers.append("build trace selection contains unexpected artifacts")
    except OSError:
        blockers.append("build trace selection directory is unreadable")
    return sorted(set(blockers))


def _load_expected_trace_sources(
    base: Path,
    manifest: dict[str, Any],
    case_ids: list[str],
) -> dict[str, list[dict[str, Any]]]:
    artifact = ((manifest.get("artifacts") or {}).get("build_plan") or {})
    build_root = _resolve_input_locator(base, str(artifact.get("path") or ""))
    build_index = _read_bounded_json_object(build_root / "index.json")
    entries = build_index.get("cases") or []
    if not isinstance(entries, list) or [
        str(entry.get("case_id") or "")
        for entry in entries
        if isinstance(entry, dict)
    ] != case_ids:
        raise ValueError("build plan case order differs from the denominator")
    expected: dict[str, list[dict[str, Any]]] = {}
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError("build plan entry is not an object")
        case_id = str(entry.get("case_id") or "")
        case_plan = _read_bounded_json_object(
            _resolve_input_locator(build_root, str(entry.get("file") or ""))
        )
        sources: list[dict[str, Any]] = []
        for session_plan in case_plan.get("sessions") or []:
            if not isinstance(session_plan, dict):
                raise ValueError("build session is not an object")
            session_id = str(session_plan.get("session_id") or "")
            observation_time = str(session_plan.get("observation_time") or "")
            for pair in session_plan.get("pairs") or []:
                if not isinstance(pair, dict):
                    raise ValueError("build pair is not an object")
                for chunk in pair.get("chunks") or []:
                    if not isinstance(chunk, dict):
                        raise ValueError("build chunk is not an object")
                    chunk_id = str(chunk.get("chunk_id") or "")
                    turn_ids = [
                        str(part.get("source_turn_id") or "")
                        for part in chunk.get("turns") or []
                        if isinstance(part, dict)
                    ]
                    identity = {
                        "case_id": case_id,
                        "session_id": session_id,
                        "turn_ids": turn_ids,
                        "chunk_id": chunk_id,
                    }
                    source_id = hashlib.sha256(
                        json.dumps(
                            identity,
                            ensure_ascii=False,
                            separators=(",", ":"),
                        ).encode("utf-8")
                    ).hexdigest()
                    sources.append({
                        "source_id": source_id,
                        "session_id": session_id,
                        "runner_session_id": session_id,
                        "turn_ids": turn_ids,
                        "chunk_id": chunk_id,
                        "observation_time": observation_time,
                    })
        expected[case_id] = sources
    return expected


def _validate_trace_sources(
    actual: list[dict[str, Any]],
    expected: list[dict[str, Any]],
    failure_stage: str,
) -> None:
    if len(actual) > len(expected) or actual != expected[:len(actual)]:
        raise ValueError("trace source sequence differs from the build plan")
    if len(actual) == len(expected):
        return
    if failure_stage not in {"build_error", "persistence_error"}:
        raise ValueError("trace source sequence is incomplete")


def _is_trace_selection_name(name: str) -> bool:
    prefix = "maintained-"
    suffix = name[len(prefix):] if name.startswith(prefix) else ""
    return len(suffix) == 16 and all(char in "0123456789abcdef" for char in suffix)


def _canonical_trace_selection(index: dict[str, Any]) -> bytes:
    value: dict[str, Any] = {
        "schema_version": index.get("schema_version"),
        "purpose": index.get("purpose"),
        "comparability": index.get("comparability"),
        "scenario": index.get("scenario"),
    }
    if index.get("backend"):
        value["backend"] = index["backend"]
    value["content_mode"] = index.get("content_mode")
    entries: list[dict[str, Any]] = []
    for entry in index.get("cases") or []:
        if not isinstance(entry, dict):
            raise TypeError("trace selection case must be an object")
        entries.append({
            "case_id": entry.get("case_id"),
            "file": entry.get("file"),
            "sha256": entry.get("sha256"),
        })
    value["cases"] = entries
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        indent=2,
        separators=(",", ": "),
    )
    encoded = (
        encoded.replace("&", r"\u0026")
        .replace("<", r"\u003c")
        .replace(">", r"\u003e")
        .replace("\u2028", r"\u2028")
        .replace("\u2029", r"\u2029")
    )
    return (encoded + "\n").encode("utf-8")


def _read_selected_trace(path: Path, case_id: str, mode: str) -> dict[str, Any]:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > _TRACE_MAX_FILE_BYTES:
        raise ValueError("trace must be a bounded regular file")
    records = 0
    decoded_bytes = 0
    sequence = 0
    outcome: dict[str, Any] | None = None
    sources: dict[str, tuple[str, dict[str, Any]]] = {}
    source_order: list[dict[str, Any]] = []
    gold_joined = False
    with path.open("rb") as raw_stream:
        stream: Any = raw_stream
        if path.name.endswith(".gz"):
            stream = gzip.GzipFile(fileobj=raw_stream, mode="rb")
        try:
            while True:
                line = stream.readline(_TRACE_MAX_LINE_BYTES + 1)
                if not line:
                    break
                if len(line) > _TRACE_MAX_LINE_BYTES:
                    raise ValueError("trace record exceeds the line limit")
                decoded_bytes += len(line)
                if decoded_bytes > _TRACE_MAX_DECODED_BYTES:
                    raise ValueError("trace exceeds the decoded size limit")
                records += 1
                if records > _TRACE_MAX_RECORDS:
                    raise ValueError("trace exceeds the record limit")
                if outcome is not None:
                    raise ValueError("outcome is not the terminal trace record")
                record = json.loads(line.decode("utf-8"))
                if not isinstance(record, dict):
                    raise ValueError("trace record must be an object")
                sequence += 1
                _validate_trace_record(record, case_id, mode, sequence)
                event = str(record.get("event") or "")
                source = record.get("source") or {}
                source_id = str(source.get("source_id") or "")
                if event == "extraction":
                    if source_id in sources:
                        raise ValueError("duplicate trace extraction")
                    snapshot = dict(source)
                    sources[source_id] = ("extracted", snapshot)
                    source_order.append(snapshot)
                elif event == "persistence":
                    state = sources.get(source_id)
                    if state is None or state[0] != "extracted" or state[1] != source:
                        raise ValueError("out-of-order trace persistence")
                    sources[source_id] = ("persisted", state[1])
                elif event in {"retrieval", "gold_join", "outcome"}:
                    if any(state[0] != "persisted" for state in sources.values()):
                        raise ValueError("trace build lifecycle is incomplete")
                if event == "gold_join":
                    if gold_joined or record["gold"].get("joined_after_qa") is not True:
                        raise ValueError("invalid trace gold join")
                    gold_joined = True
                if event == "outcome":
                    outcome = record["outcome"]
                    if not gold_joined:
                        _validate_outcome_without_gold(outcome)
        finally:
            if stream is not raw_stream:
                stream.close()
    if outcome is None:
        raise ValueError("trace has no terminal outcome")
    if not sources:
        try:
            _validate_outcome_without_gold(outcome)
        except ValueError as exc:
            raise ValueError("trace has no build evidence") from exc
    return {"outcome": outcome, "sources": source_order}


def _validate_outcome_without_gold(outcome: dict[str, Any]) -> None:
    allowed_stages = {
        "build_error",
        "persistence_error",
        "answer_generation_miss",
        "unknown",
    }
    if (
        outcome.get("correct") is not False
        or outcome.get("gold_session_recall") is not None
        or not str(outcome.get("error") or "").strip()
        or str(outcome.get("failure_stage") or "") not in allowed_stages
    ):
        raise ValueError("trace outcome precedes gold join without a terminal error")


def _validate_trace_record(
    record: dict[str, Any],
    case_id: str,
    mode: str,
    sequence: int,
) -> None:
    allowed = {
        "schema_version", "sequence", "recorded_at", "case_id", "content_mode",
        "event", "source", "extraction", "persistence", "retrieval", "gold",
        "outcome",
    }
    if set(record) - allowed:
        raise ValueError("trace record contains unknown fields")
    if (
        record.get("schema_version") != _TRACE_SCHEMA
        or record.get("sequence") != sequence
        or not str(record.get("recorded_at") or "")
        or record.get("case_id") != case_id
        or record.get("content_mode") != mode
    ):
        raise ValueError("trace record identity is invalid")
    event = str(record.get("event") or "")
    payload_names = ("extraction", "persistence", "retrieval", "gold", "outcome")
    present = [name for name in payload_names if record.get(name) is not None]
    expected_payload = "gold" if event == "gold_join" else event
    if present != [expected_payload]:
        raise ValueError("trace event payload is invalid")
    if event in {"extraction", "persistence"}:
        _validate_trace_source(record.get("source"))
    elif record.get("source") is not None:
        raise ValueError("non-build trace event contains a source")
    if event == "extraction":
        _validate_trace_extraction(record["extraction"], mode)
    elif event == "persistence":
        _validate_trace_persistence(record["persistence"], mode)
    elif event == "retrieval":
        _validate_trace_retrieval(record["retrieval"], mode)
    elif event == "gold_join":
        _strict_trace_object(record["gold"], {"answer_session_ids", "joined_after_qa"})
    elif event == "outcome":
        outcome = _strict_trace_object(
            record["outcome"],
            {
                "failure_stage",
                "build_observability",
                "gold_session_recall",
                "correct",
                "error",
            },
        )
        if (
            not str(outcome.get("failure_stage") or "")
            or str(outcome.get("build_observability") or "")
            not in {"operations", "snapshot_diff", "unknown"}
            or not isinstance(outcome.get("correct"), bool)
        ):
            raise ValueError("trace outcome is incomplete")
    else:
        raise ValueError("unsupported trace event")


def _validate_trace_source(value: Any) -> None:
    source = _strict_trace_object(value, {
        "source_id", "session_id", "runner_session_id", "turn_ids", "chunk_id",
        "observation_time",
    })
    for key in ("source_id", "session_id", "chunk_id"):
        if not str(source.get(key) or ""):
            raise ValueError("trace source identity is incomplete")


def _validate_trace_extraction(value: Any, mode: str) -> None:
    extraction = _strict_trace_object(value, {
        "input", "operations", "operation_count", "effective_operations",
        "unavailable_reason", "error",
    })
    _validate_trace_text(extraction.get("input"), mode)
    operations = extraction.get("operations") or []
    if not isinstance(operations, list) or extraction.get("operation_count") != len(operations):
        raise ValueError("trace extraction operation count is invalid")
    if extraction.get("effective_operations") != "unavailable" or not str(
        extraction.get("unavailable_reason") or ""
    ):
        raise ValueError("trace overstates backend operation visibility")
    for operation in operations:
        item = _strict_trace_object(operation, {
            "type", "memory_id", "memory", "topics", "kind", "event_time",
            "participants", "location",
        })
        _validate_trace_text(item.get("memory"), mode)
        values = list(item.get("topics") or []) + list(item.get("participants") or [])
        if item.get("location"):
            values.append(item["location"])
        _validate_trace_metadata(values, mode)


def _validate_trace_persistence(value: Any, mode: str) -> None:
    persistence = _strict_trace_object(value, {
        "acknowledged", "before", "after", "diff", "actual_operations",
        "unavailable_reason", "error",
    })
    if persistence.get("actual_operations") != "unavailable" or not str(
        persistence.get("unavailable_reason") or ""
    ):
        raise ValueError("trace overstates backend persistence visibility")
    acknowledged = persistence.get("acknowledged") is True
    if acknowledged == bool(persistence.get("error")):
        raise ValueError("trace persistence acknowledgement is invalid")
    for name in ("before", "after"):
        for ref in persistence.get(name) or []:
            _validate_trace_memory_ref(ref, mode)
    diff = _strict_trace_object(
        persistence.get("diff"),
        {"added", "updated", "deleted", "unchanged"},
    )
    for name in ("added", "updated", "deleted"):
        for ref in diff.get(name) or []:
            _validate_trace_memory_ref(ref, mode)


def _validate_trace_retrieval(value: Any, mode: str) -> None:
    retrieval = _strict_trace_object(value, {"step", "query", "hits", "parse_error"})
    if not isinstance(retrieval.get("step"), int) or retrieval["step"] < 0:
        raise ValueError("trace retrieval step is invalid")
    _validate_trace_text(retrieval.get("query"), mode)
    for rank, hit in enumerate(retrieval.get("hits") or [], 1):
        item = _strict_trace_object(hit, {"memory_id", "score", "rank"})
        if item.get("rank") != rank or not isinstance(item.get("score"), (int, float)):
            raise ValueError("trace retrieval ranking is invalid")
        if not math.isfinite(float(item["score"])):
            raise ValueError("trace retrieval score is invalid")


def _validate_trace_memory_ref(value: Any, mode: str) -> None:
    ref = _strict_trace_object(value, {"id", "fingerprint", "memory"})
    _validate_trace_text(ref.get("memory"), mode)


def _validate_trace_text(value: Any, mode: str) -> None:
    text = _strict_trace_object(value, {"value", "sha256", "bytes"})
    byte_count = text.get("bytes")
    digest = str(text.get("sha256") or "")
    if not isinstance(byte_count, int) or byte_count < 0:
        raise ValueError("trace text length is invalid")
    if digest and (len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest)):
        raise ValueError("trace text digest is invalid")
    if mode == "hash" and (text.get("value") or not digest):
        raise ValueError("hash trace exposes content or omits a digest")
    if mode == "none" and text.get("value"):
        raise ValueError("none trace exposes content")


def _validate_trace_metadata(values: list[Any], mode: str) -> None:
    if mode == "none" and values:
        raise ValueError("none trace exposes operation metadata")
    if mode == "hash":
        for value in values:
            digest = str(value)
            if not digest.startswith("sha256:") or len(digest) != 71 or any(
                char not in "0123456789abcdef" for char in digest[7:]
            ):
                raise ValueError("hash trace exposes operation metadata")


def _strict_trace_object(value: Any, allowed: set[str]) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) - allowed:
        raise ValueError("trace payload is not a strict object")
    return value


def _trace_file_name(case_id: str) -> str:
    name = "".join(
        char if char.isalpha() or char.isdigit() or char in "-_" else "_"
        for char in case_id
    ).strip("_")
    if name:
        return name
    return "case-" + hashlib.sha256(case_id.encode("utf-8")).hexdigest()[:12]


def _equal_optional_metric(first: Any, second: Any) -> bool:
    if first is None or second is None:
        return first is None and second is None
    try:
        return math.isclose(float(first), float(second), rel_tol=1e-9, abs_tol=1e-9)
    except (TypeError, ValueError):
        return False
