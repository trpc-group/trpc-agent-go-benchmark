#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Guarded service lifecycle for the Contextual Retrieval I2 Agentic A/B."""

from __future__ import annotations

from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Tuple

from contextual_retrieval.agentic import (
    AGENTIC_ANSWERS_SCHEMA,
    AGENTIC_LINEAGE_SCHEMA,
    AGENTIC_REPORT_SCHEMA,
    AGENTIC_SEARCH_K,
    FORMAL_CASES,
    FORMAL_REPEATS,
    FORMAL_SCHEDULE_SEED,
    _public_agent_config,
    run_agentic_ab,
    validate_agentic_checkpoint_evidence,
    validate_agentic_service_pair,
)
from contextual_retrieval.artifacts import (
    canonical_digest,
    load_artifact,
    text_digest,
    write_artifact,
)
from contextual_retrieval.context_cache import (
    CONTEXT_FINISH_REASON_POLICY,
    CONTEXT_PROMPT,
    CONTEXT_SUMMARY_SCHEMA,
)
from contextual_retrieval.controller import (
    INDEX_STATE_SCHEMA,
    SERVICE_CONFIG_SCHEMA,
    _build_service,
    _git_snapshot,
    _index_identity,
    _load_service_config_artifact,
    _port_is_available,
    _require_clean_snapshot,
    _request_json,
    _sanitize_service_config,
    _start_service,
    _stop_owned_process,
    _wait_for_health,
    validate_table_name,
)
from contextual_retrieval.dataset import CASE_SCHEMA, CHUNK_SCHEMA


AGENTIC_CONTROLLER_MANIFEST_SCHEMA = (
    "contextual-retrieval/agentic-controller-manifest/v1"
)
AGENTIC_CONTROLLER_REPORT_SCHEMA = (
    "contextual-retrieval/agentic-controller-report/v1"
)
AGENTIC_PROVENANCE_SCHEMA = "contextual-retrieval/agentic-provenance/v1"
EXPECTED_CONTEXT_CHUNKS = 13086
EXPECTED_CONTEXT_MODEL = "deepseek-v3.2"
EXPECTED_CONTEXT_PROMPT = "anthropic-contextual-retrieval-v1"
EXPECTED_EMBEDDING_MODEL = "bge-m3"


def _safe_error(error: BaseException) -> Dict[str, str]:
    """Describe a failure without persisting arbitrary exception text."""
    return {
        "type": type(error).__name__,
        "message": "agentic controller failed",
    }


def _capture_source_snapshots(
    benchmark_root: Path,
    framework_repository_root: Optional[str],
) -> Tuple[Dict[str, Any], Dict[str, Any]]:
    """Capture explicit framework provenance or use the module repository safely."""
    benchmark_snapshot = _git_snapshot(benchmark_root)
    if framework_repository_root is None:
        framework_snapshot = {
            **benchmark_snapshot,
            "scope": "benchmark_module_only",
        }
    else:
        framework_snapshot = {
            **_git_snapshot(Path(framework_repository_root).resolve()),
            "scope": "explicit_framework_repository",
        }
    return framework_snapshot, benchmark_snapshot


def _reuse_or_write_artifact(
    path: Path,
    schema: str,
    identity: Mapping[str, Any],
    *,
    creation_metadata: Optional[Mapping[str, Any]] = None,
) -> Dict[str, Any]:
    """Reuse an identical sealed identity artifact without changing its time."""
    stable = {"schema_version": schema, **dict(identity)}
    creation = dict(creation_metadata or {})
    if path.exists():
        existing = load_artifact(str(path), schema)
        expected_fields = set(stable) | set(creation) | {"artifact_digest"}
        if set(existing) != expected_fields:
            raise ValueError(
                f"existing {path.name} has incompatible fields"
            )
        mismatched = [
            field
            for field, expected in stable.items()
            if existing.get(field) != expected
        ]
        if mismatched:
            raise ValueError(
                f"existing {path.name} identity differs: "
                + ", ".join(mismatched)
            )
        return existing
    return write_artifact(
        str(path),
        {**stable, **creation},
    )


def _validate_completed_controller_report(
    target: Path,
    report: Mapping[str, Any],
    *,
    mode: str,
    manifest: Mapping[str, Any],
    provenance: Mapping[str, Any],
) -> Dict[str, Any]:
    """Validate and return a previously completed controller result."""
    baseline_config = _load_service_config_artifact(
        target / "baseline.config.json"
    )
    contextual_config = _load_service_config_artifact(
        target / "contextual.config.json"
    )
    lineage = load_artifact(
        str(target / "verified-lineage.json"),
        AGENTIC_LINEAGE_SCHEMA,
    )
    agentic_report = load_artifact(
        str(target / "agentic.json"),
        AGENTIC_REPORT_SCHEMA,
    )
    answers = load_artifact(
        str(target / "agentic.answers.json"),
        AGENTIC_ANSWERS_SCHEMA,
    )
    checkpoint = validate_agentic_checkpoint_evidence(
        str(target / "agentic.checkpoint.jsonl"),
        answers,
        agentic_report,
    )
    expected = {
        "status": "valid",
        "phase": f"agentic_{mode}_answers_completed",
        "mode": mode,
        "manifest_digest": manifest["artifact_digest"],
        "provenance_digest": provenance["artifact_digest"],
        "verified_lineage_digest": lineage["artifact_digest"],
        "agentic_report_digest": agentic_report["artifact_digest"],
        "agentic_answers_digest": answers["artifact_digest"],
        "checkpoint_sha256": checkpoint["sha256"],
        "checkpoint_records": checkpoint["records"],
        "agentic_evidence_scope": agentic_report.get("evidence_scope"),
        "formal_answers_eligible": agentic_report.get(
            "formal_answers_eligible",
            False,
        ),
        "load_endpoint_called": False,
        "judge_initialized": False,
        "automatic_formal_promotion": False,
    }
    errors = [
        field
        for field, value in expected.items()
        if report.get(field) != value
    ]
    if lineage.get("controller_manifest_digest") != manifest.get(
        "artifact_digest"
    ):
        errors.append("lineage_controller_manifest_digest")
    if lineage.get("provenance_digest") != provenance.get("artifact_digest"):
        errors.append("lineage_provenance_digest")
    for field in ("repository", "benchmark_repository"):
        if lineage.get(field) != provenance.get(field):
            errors.append(f"lineage_{field}")
    if lineage.get("baseline_runtime_config_digest") != baseline_config.get(
        "artifact_digest"
    ):
        errors.append("baseline_runtime_config_digest")
    if lineage.get("contextual_runtime_config_digest") != contextual_config.get(
        "artifact_digest"
    ):
        errors.append("contextual_runtime_config_digest")
    if errors:
        raise ValueError(
            "existing valid controller report identity differs: "
            + ", ".join(errors)
        )
    return dict(report)


def _validate_context_summary(
    summary: Mapping[str, Any],
    chunks: Mapping[str, Any],
) -> None:
    errors: List[str] = []
    config = summary.get("config") or {}
    expected_count = chunks.get("chunks_count")
    if summary.get("status") != "valid":
        errors.append("status")
    if expected_count != EXPECTED_CONTEXT_CHUNKS:
        errors.append("chunk_manifest_count")
    if summary.get("chunk_manifest_digest") != chunks.get("artifact_digest"):
        errors.append("chunk_manifest_digest")
    if summary.get("expected_chunks") != EXPECTED_CONTEXT_CHUNKS:
        errors.append("expected_chunks")
    if summary.get("successful_chunks") != EXPECTED_CONTEXT_CHUNKS:
        errors.append("successful_chunks")
    if summary.get("error_chunks") != 0:
        errors.append("error_chunks")
    if summary.get("missing_chunks") != 0:
        errors.append("missing_chunks")
    if str(config.get("model") or "").strip().lower() != EXPECTED_CONTEXT_MODEL:
        errors.append("context_model")
    if config.get("prompt_id") != EXPECTED_CONTEXT_PROMPT:
        errors.append("context_prompt_id")
    if config.get("prompt_hash") != text_digest(CONTEXT_PROMPT):
        errors.append("context_prompt_hash")
    if config.get("temperature") != 0:
        errors.append("context_temperature")
    if config.get("reasoning") != "unspecified":
        errors.append("context_reasoning")
    if config.get("finish_reason_policy") != CONTEXT_FINISH_REASON_POLICY:
        errors.append("context_finish_reason_policy")
    if config.get("transport_max_retries") != 0:
        errors.append("context_transport_max_retries")
    if not isinstance(summary.get("cache_identity"), str) or not summary.get(
        "cache_identity"
    ):
        errors.append("cache_identity")
    if not isinstance(summary.get("context_set_digest"), str) or not summary.get(
        "context_set_digest"
    ):
        errors.append("context_set_digest")
    if errors:
        raise ValueError(
            "invalid Context summary fields: " + ", ".join(errors)
        )


def _validate_prebuilt_state(
    state: Mapping[str, Any],
    variant: str,
    chunks: Mapping[str, Any],
    context_summary: Mapping[str, Any],
    repository_snapshot: Mapping[str, Any],
    benchmark_repository_snapshot: Mapping[str, Any],
) -> str:
    errors: List[str] = []
    identity = state.get("identity") or {}
    expected_count = chunks.get("chunks_count")
    table = str(identity.get("pg_table") or "")
    try:
        validate_table_name(table)
    except ValueError:
        errors.append("pg_table")
    if state.get("status") != "complete":
        errors.append("status")
    if state.get("expected_count") != expected_count:
        errors.append("expected_count")
    if state.get("count_after") != expected_count:
        errors.append("count_after")
    if identity.get("index_variant") != variant:
        errors.append("index_variant")
    if identity.get("chunk_manifest_digest") != chunks.get("artifact_digest"):
        errors.append("chunk_manifest_digest")
    if identity.get("manifest_chunks_count") != expected_count:
        errors.append("manifest_chunks_count")
    if identity.get("search_mode") != 1:
        errors.append("search_mode")
    if identity.get("vectorstore") != "pgvector":
        errors.append("vectorstore")
    if str(identity.get("embedding_model") or "").strip().lower() != (
        EXPECTED_EMBEDDING_MODEL
    ):
        errors.append("embedding_model")
    if identity.get("embedding_dimensions") != 1024:
        errors.append("embedding_dimensions")
    cache_identity = identity.get("context_cache_identity")
    context_set_digest = identity.get("context_set_digest")
    if variant == "baseline" and cache_identity not in (None, ""):
        errors.append("baseline_context_cache_identity")
    if variant == "baseline" and context_set_digest not in (None, ""):
        errors.append("baseline_context_set_digest")
    if variant == "contextual" and cache_identity != context_summary.get(
        "cache_identity"
    ):
        errors.append("contextual_context_cache_identity")
    if variant == "contextual" and context_set_digest != context_summary.get(
        "context_set_digest"
    ):
        errors.append("contextual_context_set_digest")
    builder_source = state.get("builder_source") or {}
    for label in ("repository", "benchmark_repository"):
        builder = builder_source.get(label) or {}
        if builder.get("worktree_dirty") is not False:
            errors.append(f"builder_{label}_dirty")
        if not builder.get("commit"):
            errors.append(f"builder_{label}_commit")
    if errors:
        raise ValueError(
            f"invalid prebuilt {variant} index state fields: "
            + ", ".join(errors)
        )
    return table


def validate_agentic_prebuilt_inputs(
    context_summary: Mapping[str, Any],
    baseline_state: Mapping[str, Any],
    contextual_state: Mapping[str, Any],
    chunks: Mapping[str, Any],
    repository_snapshot: Mapping[str, Any],
    benchmark_repository_snapshot: Mapping[str, Any],
) -> Tuple[str, str]:
    """Validate immutable Context and index evidence before starting services."""
    _require_clean_snapshot("repository", repository_snapshot)
    _require_clean_snapshot(
        "benchmark repository",
        benchmark_repository_snapshot,
    )
    _validate_context_summary(context_summary, chunks)
    baseline_table = _validate_prebuilt_state(
        baseline_state,
        "baseline",
        chunks,
        context_summary,
        repository_snapshot,
        benchmark_repository_snapshot,
    )
    contextual_table = _validate_prebuilt_state(
        contextual_state,
        "contextual",
        chunks,
        context_summary,
        repository_snapshot,
        benchmark_repository_snapshot,
    )
    if baseline_table == contextual_table:
        raise ValueError("baseline and contextual index tables must differ")
    return baseline_table, contextual_table


def _validate_runtime_index(
    config: Mapping[str, Any],
    state: Mapping[str, Any],
    variant: str,
    chunks: Mapping[str, Any],
) -> None:
    errors: List[str] = []
    if _index_identity(config) != state.get("identity"):
        errors.append("identity")
    if config.get("index_variant") != variant:
        errors.append("index_variant")
    if config.get("index_document_count") != chunks.get("chunks_count"):
        errors.append("index_document_count")
    if errors:
        raise ValueError(
            f"runtime {variant} service does not match its prebuilt index: "
            + ", ".join(errors)
        )


def run_agentic_server_ab(
    go_service_dir: str,
    chunks_path: str,
    cases_path: str,
    context_cache_path: str,
    context_summary_path: str,
    baseline_index_state_path: str,
    contextual_index_state_path: str,
    output_dir: str,
    *,
    mode: str,
    baseline_port: int = 8765,
    contextual_port: int = 8766,
    service_start_timeout: float = 120,
    request_timeout: float = 1800,
    schedule_seed: int = FORMAL_SCHEDULE_SEED,
    smoke_per_type: int = 10,
    framework_repository_root: Optional[str] = None,
) -> Dict[str, Any]:
    """Run one explicitly selected smoke or formal A/B without loading indexes."""
    if mode not in ("smoke", "formal"):
        raise ValueError("mode must be smoke or formal")
    if baseline_port == contextual_port:
        raise ValueError("baseline and contextual ports must differ")
    if service_start_timeout <= 0 or request_timeout <= 0:
        raise ValueError("timeouts must be positive")
    if smoke_per_type <= 0 or smoke_per_type > 150:
        raise ValueError("smoke_per_type must be between 1 and 150")
    target = Path(output_dir).resolve()
    target.mkdir(parents=True, exist_ok=True)
    report_path = target / "controller.report.json"
    preserved_final_report: Optional[Dict[str, Any]] = None
    if report_path.exists():
        previous_report = load_artifact(
            str(report_path),
            AGENTIC_CONTROLLER_REPORT_SCHEMA,
        )
        if previous_report.get("status") == "valid":
            preserved_final_report = previous_report
    go_dir = Path(go_service_dir).resolve()
    owned: List[Tuple[Any, Any]] = []
    cleanup: List[Dict[str, Any]] = []
    service_logs: Dict[str, str] = {}
    manifest: Optional[Dict[str, Any]] = None
    provenance: Optional[Dict[str, Any]] = None
    lineage: Optional[Dict[str, Any]] = None
    agentic_report: Optional[Dict[str, Any]] = None
    failure: Optional[BaseException] = None
    build_log: Optional[Path] = None
    try:
        chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
        cases = load_artifact(cases_path, CASE_SCHEMA)
        summary = load_artifact(context_summary_path, CONTEXT_SUMMARY_SCHEMA)
        baseline_state = load_artifact(
            baseline_index_state_path,
            INDEX_STATE_SCHEMA,
        )
        contextual_state = load_artifact(
            contextual_index_state_path,
            INDEX_STATE_SCHEMA,
        )
        if cases.get("chunk_manifest_digest") != chunks.get(
            "artifact_digest"
        ):
            raise ValueError("case and chunk manifests do not match")
        case_records = list(cases.get("cases") or [])
        case_ids = [str(case.get("case_id") or "") for case in case_records]
        if (
            cases.get("cases_count") != FORMAL_CASES
            or len(case_records) != FORMAL_CASES
            or len(set(case_ids)) != FORMAL_CASES
            or any(not case_id for case_id in case_ids)
        ):
            raise ValueError("I2 requires 450 unique non-empty case IDs")
        question_type_counts = Counter(
            str(case.get("question_type") or "") for case in case_records
        )
        expected_type_counts = {
            "comparison_query": 150,
            "inference_query": 150,
            "temporal_query": 150,
        }
        if dict(question_type_counts) != expected_type_counts:
            raise ValueError(
                "I2 requires exactly 150 comparison, inference, and temporal cases"
            )
        expected_cases = (
            3 * smoke_per_type if mode == "smoke" else FORMAL_CASES
        )
        repeats = 1 if mode == "smoke" else FORMAL_REPEATS
        if mode == "formal" and cases.get("cases_count") != FORMAL_CASES:
            raise ValueError("formal I2 requires the complete 450-case manifest")
        if mode == "formal" and schedule_seed != FORMAL_SCHEDULE_SEED:
            raise ValueError("formal I2 requires schedule seed 20260725")
        benchmark_root = Path(__file__).resolve().parents[2]
        repository_snapshot, benchmark_snapshot = _capture_source_snapshots(
            benchmark_root,
            framework_repository_root,
        )
        baseline_table, contextual_table = validate_agentic_prebuilt_inputs(
            summary,
            baseline_state,
            contextual_state,
            chunks,
            repository_snapshot,
            benchmark_snapshot,
        )

        provenance = _reuse_or_write_artifact(
            target / "provenance.json",
            AGENTIC_PROVENANCE_SCHEMA,
            {
                "repository": repository_snapshot,
                "benchmark_repository": benchmark_snapshot,
                "chunk_manifest_digest": chunks["artifact_digest"],
                "case_manifest_digest": cases["artifact_digest"],
                "context_summary_digest": summary["artifact_digest"],
                "context_set_digest": summary["context_set_digest"],
                "baseline_index_state_digest": baseline_state[
                    "artifact_digest"
                ],
                "contextual_index_state_digest": contextual_state[
                    "artifact_digest"
                ],
            },
            creation_metadata={
                "captured_at": datetime.now(timezone.utc).isoformat(),
            },
        )
        manifest = _reuse_or_write_artifact(
            target / "controller.manifest.json",
            AGENTIC_CONTROLLER_MANIFEST_SCHEMA,
            {
                "run_kind": "contextual_retrieval_agentic_server_ab",
                "mode": mode,
                "expected_cases": expected_cases,
                "repeats": repeats,
                "expected_executions": expected_cases * repeats * 2,
                "search_k": AGENTIC_SEARCH_K,
                "agent_model": EXPECTED_CONTEXT_MODEL,
                "embedding_model": EXPECTED_EMBEDDING_MODEL,
                "context_model": EXPECTED_CONTEXT_MODEL,
                "context_prompt_id": EXPECTED_CONTEXT_PROMPT,
                "baseline": {"table": baseline_table, "port": baseline_port},
                "contextual": {
                    "table": contextual_table,
                    "port": contextual_port,
                },
                "chunk_manifest_digest": chunks["artifact_digest"],
                "case_manifest_digest": cases["artifact_digest"],
                "context_summary_digest": summary["artifact_digest"],
                "context_set_digest": summary["context_set_digest"],
                "baseline_index_state_digest": baseline_state[
                    "artifact_digest"
                ],
                "contextual_index_state_digest": contextual_state[
                    "artifact_digest"
                ],
                "provenance_digest": provenance["artifact_digest"],
                "load_endpoint_allowed": False,
                "automatic_formal_promotion": False,
                "judge_initialized": False,
                "request_attempts": 1,
                "request_timeout": request_timeout,
                "service_start_timeout": service_start_timeout,
                "schedule_seed": schedule_seed,
            },
            creation_metadata={
                "created_at": datetime.now(timezone.utc).isoformat(),
                "interface": "python_api",
            },
        )
        if preserved_final_report is not None:
            return _validate_completed_controller_report(
                target,
                preserved_final_report,
                mode=mode,
                manifest=manifest,
                provenance=provenance,
            )
        for port in (baseline_port, contextual_port):
            if not _port_is_available(port):
                raise ValueError(f"port {port} is already in use")

        binary, build_log = _build_service(go_dir, target)
        for variant, table, port, cache in (
            ("baseline", baseline_table, baseline_port, None),
            (
                "contextual",
                contextual_table,
                contextual_port,
                context_cache_path,
            ),
        ):
            process, log_handle, log_path = _start_service(
                binary,
                go_dir,
                target,
                variant,
                table,
                port,
                chunks_path,
                cache,
            )
            owned.append((process, log_handle))
            del log_path
            service_logs[variant] = "captured"
            _wait_for_health(
                process,
                f"http://127.0.0.1:{port}",
                service_start_timeout,
            )

        baseline_url = f"http://127.0.0.1:{baseline_port}"
        contextual_url = f"http://127.0.0.1:{contextual_port}"
        baseline_config = _sanitize_service_config(
            _request_json(baseline_url + "/config", timeout=30)
        )
        contextual_config = _sanitize_service_config(
            _request_json(
                contextual_url + "/config",
                timeout=30,
            )
        )
        _validate_runtime_index(
            baseline_config,
            baseline_state,
            "baseline",
            chunks,
        )
        _validate_runtime_index(
            contextual_config,
            contextual_state,
            "contextual",
            chunks,
        )
        validate_agentic_service_pair(
            baseline_config,
            contextual_config,
            chunks,
            expected_agent_model=EXPECTED_CONTEXT_MODEL,
        )
        if contextual_config.get("context_cache_identity") != summary.get(
            "cache_identity"
        ):
            raise ValueError(
                "runtime contextual service does not use the verified cache"
            )
        if contextual_config.get("context_set_digest") != summary.get(
            "context_set_digest"
        ):
            raise ValueError(
                "runtime contextual service does not use the verified Context set"
            )
        baseline_config_artifact = _reuse_or_write_artifact(
            target / "baseline.config.json",
            SERVICE_CONFIG_SCHEMA,
            baseline_config,
        )
        contextual_config_artifact = _reuse_or_write_artifact(
            target / "contextual.config.json",
            SERVICE_CONFIG_SCHEMA,
            contextual_config,
        )
        lineage = _reuse_or_write_artifact(
            target / "verified-lineage.json",
            AGENTIC_LINEAGE_SCHEMA,
            {
                "status": "valid",
                "mode": mode,
                "controller_manifest_digest": manifest["artifact_digest"],
                "provenance_digest": provenance["artifact_digest"],
                "repository": repository_snapshot,
                "benchmark_repository": benchmark_snapshot,
                "case_manifest_digest": cases["artifact_digest"],
                "chunk_manifest_digest": chunks["artifact_digest"],
                "context_summary_digest": summary["artifact_digest"],
                "context_cache_identity": summary["cache_identity"],
                "context_set_digest": summary["context_set_digest"],
                "baseline_index_state_digest": baseline_state[
                    "artifact_digest"
                ],
                "contextual_index_state_digest": contextual_state[
                    "artifact_digest"
                ],
                "baseline_runtime_config_digest": baseline_config_artifact[
                    "artifact_digest"
                ],
                "contextual_runtime_config_digest": contextual_config_artifact[
                    "artifact_digest"
                ],
                "baseline_config_identity": canonical_digest(
                    _public_agent_config(baseline_config)
                ),
                "contextual_config_identity": canonical_digest(
                    _public_agent_config(contextual_config)
                ),
                "expected_cases": expected_cases,
                "repeats": repeats,
                "search_k": AGENTIC_SEARCH_K,
                "tool_argument_policy": baseline_config[
                    "tool_argument_policy"
                ],
                "max_argument_repairs": baseline_config[
                    "max_argument_repairs"
                ],
                "silent_argument_rewrite": baseline_config[
                    "silent_argument_rewrite"
                ],
                "provider_strict": baseline_config["provider_strict"],
                "load_endpoint_called": False,
                "judge_initialized": False,
            },
            creation_metadata={
                "verified_at": datetime.now(timezone.utc).isoformat(),
            },
        )
        agentic_report = run_agentic_ab(
            cases_path,
            chunks_path,
            baseline_url,
            contextual_url,
            str(target / "agentic.json"),
            repeats=repeats,
            timeout=request_timeout,
            schedule_seed=schedule_seed,
            smoke_per_type=smoke_per_type if mode == "smoke" else None,
            expected_agent_model=EXPECTED_CONTEXT_MODEL,
            verified_lineage=lineage,
        )
    except BaseException as error:
        failure = error
    finally:
        for process, log_handle in reversed(owned):
            cleanup.append(_stop_owned_process(process))
            log_handle.close()

    if failure is not None and preserved_final_report is not None:
        raise RuntimeError(
            "Agentic controller failed; preserved controller report"
        ) from failure

    report = write_artifact(
        str(report_path),
        {
            "schema_version": AGENTIC_CONTROLLER_REPORT_SCHEMA,
            "status": "valid" if failure is None else "insufficient",
            "phase": (
                f"agentic_{mode}_answers_completed"
                if failure is None
                else "agentic_controller_failed"
            ),
            "mode": mode,
            "manifest_digest": (
                manifest.get("artifact_digest") if manifest else None
            ),
            "provenance_digest": (
                provenance.get("artifact_digest") if provenance else None
            ),
            "verified_lineage_digest": (
                lineage.get("artifact_digest") if lineage else None
            ),
            "agentic_report_digest": (
                agentic_report.get("artifact_digest")
                if agentic_report
                else None
            ),
            "agentic_answers_digest": (
                agentic_report.get("answers_digest")
                if agentic_report
                else None
            ),
            "checkpoint_sha256": (
                agentic_report.get("checkpoint_sha256")
                if agentic_report
                else None
            ),
            "checkpoint_records": (
                agentic_report.get("checkpoint_records")
                if agentic_report
                else None
            ),
            "agentic_evidence_scope": (
                agentic_report.get("evidence_scope")
                if agentic_report
                else None
            ),
            "formal_answers_eligible": (
                agentic_report.get("formal_answers_eligible", False)
                if agentic_report
                else False
            ),
            "load_endpoint_called": False,
            "judge_initialized": False,
            "automatic_formal_promotion": False,
            "build_log": "captured" if build_log else None,
            "service_logs": service_logs,
            "cleanup": cleanup,
            "error": _safe_error(failure) if failure else None,
            "completed_at": datetime.now(timezone.utc).isoformat(),
        },
    )
    if failure is not None:
        raise RuntimeError("Agentic controller failed; inspect report") from failure
    return report
