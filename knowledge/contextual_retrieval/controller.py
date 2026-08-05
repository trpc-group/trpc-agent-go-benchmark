#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Safe server controllers for Contextual Retrieval experiments."""

from __future__ import annotations

import json
import os
import re
import signal
import socket
import subprocess
import sys
import time
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple
from urllib.request import Request, urlopen

from contextual_retrieval.artifacts import (
    canonical_digest,
    load_artifact,
    write_artifact,
)
from contextual_retrieval.context_cache import summarize_context_cache
from contextual_retrieval.dataset import (
    CASE_SCHEMA,
    CHUNK_SCHEMA,
    DEFAULT_QUESTION_TYPES,
)
from contextual_retrieval.runner import (
    RETRIEVAL_MANIFEST_SCHEMA,
    RETRIEVAL_REPORT_SCHEMA,
    run_retrieval_ab,
)


CONTROLLER_MANIFEST_SCHEMA = "contextual-retrieval/controller-manifest/v1"
CONTROLLER_REPORT_SCHEMA = "contextual-retrieval/controller-report/v1"
INDEX_STATE_SCHEMA = "contextual-retrieval/index-state/v2"
LOAD_RESULT_SCHEMA = "contextual-retrieval/load-result/v1"
SERVICE_CONFIG_SCHEMA = "contextual-retrieval/service-config/v1"
TABLE_PATTERN = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
ROOT_CONTROL_PLANE_PATHS = {
    "..dev/contextual-embedding-validation-requirements.md",
    "benchmark",
}
BENCHMARK_CONTROL_PLANE_PATHS = {
    "knowledge/contextual_retrieval/README.md",
    "knowledge/contextual_retrieval/__main__.py",
    "knowledge/contextual_retrieval/controller.py",
}
BENCHMARK_CONTROL_PLANE_PREFIXES = (
    "knowledge/contextual_retrieval/results/",
    "knowledge/tests/test_contextual_retrieval_",
)


def validate_table_name(value: str) -> str:
    """Allow only a plain PostgreSQL identifier as an experiment table."""
    if not TABLE_PATTERN.fullmatch(value):
        raise ValueError(
            "PG table must match [A-Za-z_][A-Za-z0-9_]*"
        )
    return value


def _git_snapshot(path: Path) -> Dict[str, Any]:
    def output(*args: str) -> str:
        return subprocess.check_output(
            ["git", "-C", str(path), *args],
            text=True,
            stderr=subprocess.DEVNULL,
        ).strip()

    status = output("status", "--porcelain=v1", "--untracked-files=all")
    status_lines = [line for line in status.splitlines() if line]
    untracked_dirty = any(line.startswith("?? ") for line in status_lines)
    tracked_dirty = any(not line.startswith("?? ") for line in status_lines)
    return {
        "commit": output("rev-parse", "HEAD"),
        "branch": output("branch", "--show-current"),
        "tracked_dirty": tracked_dirty,
        "untracked_dirty": untracked_dirty,
        "worktree_dirty": bool(status_lines),
    }


def _require_clean_snapshot(label: str, snapshot: Mapping[str, Any]) -> None:
    if not snapshot.get("commit"):
        raise ValueError(f"{label} commit is missing")
    if snapshot.get("worktree_dirty") is not False:
        raise ValueError(f"{label} checkout is dirty or has untracked files")


def _git_changed_paths(path: Path, older: str, newer: str) -> List[str]:
    if older == newer:
        return []
    output = subprocess.check_output(
        [
            "git",
            "-C",
            str(path),
            "diff",
            "--name-only",
            f"{older}..{newer}",
            "--",
        ],
        text=True,
        stderr=subprocess.DEVNULL,
    )
    return [line for line in output.splitlines() if line]


def classify_source_compatibility(
    root_changes: Sequence[str],
    benchmark_changes: Sequence[str],
) -> Dict[str, Any]:
    """Separate experiment control-plane changes from retrieval-path changes."""
    unsafe_root = sorted(
        path for path in root_changes if path not in ROOT_CONTROL_PLANE_PATHS
    )
    unsafe_benchmark = sorted(
        path
        for path in benchmark_changes
        if path not in BENCHMARK_CONTROL_PLANE_PATHS
        and not any(
            path.startswith(prefix)
            for prefix in BENCHMARK_CONTROL_PLANE_PREFIXES
        )
    )
    return {
        "compatible": not unsafe_root and not unsafe_benchmark,
        "root_changed_paths": sorted(root_changes),
        "benchmark_changed_paths": sorted(benchmark_changes),
        "retrieval_sensitive_root_changes": unsafe_root,
        "retrieval_sensitive_benchmark_changes": unsafe_benchmark,
    }


def _source_compatibility(
    repository_root: Path,
    benchmark_root: Path,
    smoke_manifest: Mapping[str, Any],
) -> Dict[str, Any]:
    current_root = _git_snapshot(repository_root)
    current_benchmark = _git_snapshot(benchmark_root)
    smoke_root = smoke_manifest.get("repository") or {}
    smoke_benchmark = smoke_manifest.get("benchmark_repository") or {}
    for label, snapshot in (
        ("current root", current_root),
        ("current benchmark", current_benchmark),
        ("smoke root", smoke_root),
        ("smoke benchmark", smoke_benchmark),
    ):
        if not snapshot.get("commit"):
            raise ValueError(f"{label} commit is missing")
        _require_clean_snapshot(label, snapshot)
    compatibility = classify_source_compatibility(
        _git_changed_paths(
            repository_root,
            str(smoke_root["commit"]),
            str(current_root["commit"]),
        ),
        _git_changed_paths(
            benchmark_root,
            str(smoke_benchmark["commit"]),
            str(current_benchmark["commit"]),
        ),
    )
    compatibility.update(
        {
            "smoke_repository": dict(smoke_root),
            "smoke_benchmark_repository": dict(smoke_benchmark),
            "current_repository": current_root,
            "current_benchmark_repository": current_benchmark,
        }
    )
    if not compatibility["compatible"]:
        raise ValueError(
            "retrieval-sensitive source changed after the promoted smoke"
        )
    return compatibility


def _port_is_available(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as handle:
        handle.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            handle.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True


def _request_json(
    url: str,
    method: str = "GET",
    payload: Optional[Mapping[str, Any]] = None,
    timeout: float = 30,
) -> Dict[str, Any]:
    data = None
    headers: Dict[str, str] = {}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = Request(url, data=data, headers=headers, method=method)
    try:
        with urlopen(request, timeout=timeout) as response:
            if response.status < 200 or response.status >= 300:
                raise RuntimeError(f"HTTP status {response.status} from {url}")
            body = response.read()
    except Exception as error:
        status = getattr(error, "code", None)
        if status is not None:
            raise RuntimeError(f"HTTP status {status} from {url}") from error
        raise RuntimeError(
            f"{type(error).__name__} while requesting {url}"
        ) from error
    try:
        result = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError(f"invalid JSON response from {url}") from error
    if not isinstance(result, dict):
        raise RuntimeError(f"non-object JSON response from {url}")
    return result


def _wait_for_health(
    process: subprocess.Popen,
    base_url: str,
    timeout: float,
) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(
                f"service exited before health check: {base_url}"
            )
        try:
            health = _request_json(base_url + "/health", timeout=2)
            if health.get("status") == "ok":
                return
        except RuntimeError:
            pass
        time.sleep(0.5)
    raise RuntimeError(f"service health check timed out: {base_url}")


def _stop_owned_process(process: subprocess.Popen) -> Dict[str, Any]:
    result = {"pid": process.pid, "owned": True, "stopped": False, "forced": False}
    if process.poll() is not None:
        result["stopped"] = True
        return result
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)
        result["forced"] = True
    except ProcessLookupError:
        pass
    result["stopped"] = process.poll() is not None
    return result


def _table_storage_bytes(table: str) -> Dict[str, Any]:
    """Best-effort table plus index size; never return connection errors."""
    try:
        import psycopg

        with psycopg.connect(
            host=os.environ.get("PGVECTOR_HOST", "127.0.0.1"),
            port=os.environ.get("PGVECTOR_PORT", "5432"),
            user=os.environ.get("PGVECTOR_USER", "root"),
            password=os.environ.get("PGVECTOR_PASSWORD", ""),
            dbname=os.environ.get("PGVECTOR_DATABASE", "rgb"),
            connect_timeout=10,
        ) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    "SELECT pg_total_relation_size(to_regclass(%s))",
                    (table,),
                )
                row = cursor.fetchone()
        value = row[0] if row else None
        return {
            "status": "available" if value is not None else "unavailable",
            "pg_total_relation_size_bytes": int(value) if value is not None else None,
        }
    except Exception as error:
        return {
            "status": "unavailable",
            "pg_total_relation_size_bytes": None,
            "error_type": type(error).__name__,
        }


def _index_identity(config: Mapping[str, Any]) -> Dict[str, Any]:
    fields = (
        "pg_table",
        "index_variant",
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
        "context_cache_identity",
        "context_set_digest",
    )
    return {field: config.get(field) for field in fields}


def decide_index_action(
    current_count: int,
    expected_count: int,
    state: Optional[Mapping[str, Any]],
    identity: Mapping[str, Any],
    resume_indexes: bool,
) -> str:
    """Return a non-destructive index action or reject ambiguous state."""
    if current_count < 0 or current_count > expected_count:
        raise ValueError("index document count is outside the expected range")
    if state is None:
        if current_count != 0:
            raise ValueError("non-empty index has no matching controller state")
        return "load_fresh"
    if state.get("identity") != dict(identity):
        raise ValueError("index state identity does not match the service")
    status = state.get("status")
    if status == "complete":
        if current_count != expected_count:
            raise ValueError("complete index state has an unexpected document count")
        return "reuse_complete"
    if status != "building":
        raise ValueError(f"unsupported index state status {status!r}")
    if current_count == expected_count:
        return "recover_complete"
    if current_count > 0 and not resume_indexes:
        raise ValueError(
            "partial index requires explicit --resume-indexes"
        )
    return "resume_load" if current_count > 0 else "load_fresh"


def _validate_lane_config(
    config: Mapping[str, Any],
    variant: str,
    table: str,
    chunks: Mapping[str, Any],
) -> None:
    errors = []
    if config.get("index_variant") != variant:
        errors.append("index_variant")
    if config.get("pg_table") != table:
        errors.append("pg_table")
    if config.get("vectorstore") != "pgvector":
        errors.append("vectorstore")
    if config.get("search_mode") != 1:
        errors.append("search_mode")
    if config.get("chunk_manifest_digest") != chunks.get("artifact_digest"):
        errors.append("chunk_manifest_digest")
    if config.get("manifest_chunks_count") != chunks.get("chunks_count"):
        errors.append("manifest_chunks_count")
    context_identity = config.get("context_cache_identity")
    context_set_digest = config.get("context_set_digest")
    if variant == "baseline" and context_identity not in (None, ""):
        errors.append("baseline_context_cache_identity")
    if variant == "baseline" and context_set_digest not in (None, ""):
        errors.append("baseline_context_set_digest")
    if variant == "contextual" and not context_identity:
        errors.append("contextual_context_cache_identity")
    if variant == "contextual" and not context_set_digest:
        errors.append("contextual_context_set_digest")
    count = config.get("index_document_count")
    if not isinstance(count, int):
        errors.append("index_document_count")
    if errors:
        raise ValueError(
            f"invalid {variant} service config fields: {', '.join(errors)}"
        )


def _ensure_index(
    base_url: str,
    config: Mapping[str, Any],
    state_path: Path,
    load_result_path: Path,
    load_timeout: float,
    resume_indexes: bool,
    *,
    repository_snapshot: Mapping[str, Any],
    benchmark_repository_snapshot: Mapping[str, Any],
) -> Dict[str, Any]:
    _require_clean_snapshot("root index builder", repository_snapshot)
    _require_clean_snapshot(
        "benchmark index builder",
        benchmark_repository_snapshot,
    )
    identity = _index_identity(config)
    expected_count = int(config["manifest_chunks_count"])
    current_count = int(config["index_document_count"])
    state = None
    builder_source = {
        "repository": dict(repository_snapshot),
        "benchmark_repository": dict(benchmark_repository_snapshot),
    }
    if state_path.exists():
        state = load_artifact(str(state_path), INDEX_STATE_SCHEMA)
        if state.get("builder_source") != builder_source:
            raise ValueError(
                "index state builder source does not match the clean checkout"
            )
    action = decide_index_action(
        current_count,
        expected_count,
        state,
        identity,
        resume_indexes,
    )
    storage_before = _table_storage_bytes(str(config["pg_table"]))
    started = time.monotonic()
    load_result = None
    if action in ("load_fresh", "resume_load"):
        write_artifact(
            str(state_path),
            {
                "schema_version": INDEX_STATE_SCHEMA,
                "status": "building",
                "identity": identity,
                "builder_source": builder_source,
                "expected_count": expected_count,
                "count_before": current_count,
                "action": action,
                "started_at": datetime.now(timezone.utc).isoformat(),
            },
        )
        response = _request_json(
            base_url + "/load",
            method="POST",
            payload={"file_paths": []},
            timeout=load_timeout,
        )
        if (
            response.get("success") is not True
            or response.get("count") != expected_count
        ):
            raise RuntimeError("load response did not confirm the expected count")
        load_result = write_artifact(
            str(load_result_path),
            {
                "schema_version": LOAD_RESULT_SCHEMA,
                "status": "success",
                "identity": identity,
                "count": response.get("count"),
                "elapsed_ms_reported": response.get("elapsed_ms"),
                "message": response.get("message"),
            },
        )
    final_config = _request_json(base_url + "/config", timeout=30)
    final_count = final_config.get("index_document_count")
    if final_count != expected_count:
        raise RuntimeError(
            f"index has {final_count!r} documents, expected {expected_count}"
        )
    if _index_identity(final_config) != identity:
        raise RuntimeError("service identity changed while building the index")
    storage_after = _table_storage_bytes(str(config["pg_table"]))
    existing_load_digest = state.get("load_result_digest") if state else None
    if load_result is None and load_result_path.exists():
        previous_load = load_artifact(str(load_result_path), LOAD_RESULT_SCHEMA)
        if previous_load.get("identity") == identity:
            existing_load_digest = previous_load.get("artifact_digest")
    return write_artifact(
        str(state_path),
        {
            "schema_version": INDEX_STATE_SCHEMA,
            "status": "complete",
            "identity": identity,
            "builder_source": builder_source,
            "expected_count": expected_count,
            "count_before": current_count,
            "count_after": final_count,
            "action": action,
            "load_result_digest": (
                load_result.get("artifact_digest")
                if load_result
                else existing_load_digest
            ),
            "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
            "storage_before": storage_before,
            "storage_after": storage_after,
            "completed_at": datetime.now(timezone.utc).isoformat(),
        },
    )


def _build_service(go_service_dir: Path, output_dir: Path) -> Tuple[Path, Path]:
    binary = output_dir / "trpc-knowledge-contextual-retrieval"
    log_path = output_dir / "build.log"
    with log_path.open("w", encoding="utf-8") as log_handle:
        result = subprocess.run(
            ["go", "build", "-o", str(binary), "."],
            cwd=str(go_service_dir),
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            check=False,
            env=os.environ.copy(),
        )
    if result.returncode != 0:
        raise RuntimeError(f"Go service build failed; inspect {log_path}")
    return binary, log_path


def _start_service(
    binary: Path,
    go_service_dir: Path,
    output_dir: Path,
    variant: str,
    table: str,
    port: int,
    chunks_path: str,
    context_cache_path: Optional[str],
) -> Tuple[subprocess.Popen, Any, Path]:
    log_path = output_dir / f"{variant}.service.log"
    log_handle = log_path.open("w", encoding="utf-8")
    command = [
        str(binary),
        "--port",
        str(port),
        "--vectorstore",
        "pgvector",
        "--search-mode",
        "1",
        "--pg-table",
        table,
        "--index-variant",
        variant,
        "--chunk-manifest",
        str(Path(chunks_path).resolve()),
    ]
    if variant == "contextual":
        if not context_cache_path:
            raise ValueError("context cache is required for contextual service")
        command.extend(
            ["--context-cache", str(Path(context_cache_path).resolve())]
        )
    try:
        process = subprocess.Popen(
            command,
            cwd=str(go_service_dir),
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            env=os.environ.copy(),
            start_new_session=True,
        )
    except Exception:
        log_handle.close()
        raise
    return process, log_handle, log_path


def _redacted_error(error: BaseException) -> Dict[str, str]:
    message = str(error)
    for name in (
        "OPENAI_API_KEY",
        "EMBEDDING_API_KEY",
        "PGVECTOR_PASSWORD",
        "CONTEXT_API_KEY",
        "EVAL_API_KEY",
    ):
        value = os.environ.get(name, "")
        if value:
            message = message.replace(value, "[REDACTED]")
    return {"type": type(error).__name__, "message": message[:4000]}


def _artifact_digest_if_exists(path: Path, schema: str) -> Optional[str]:
    if not path.exists():
        return None
    return str(load_artifact(str(path), schema)["artifact_digest"])


def validate_reused_index(
    config: Mapping[str, Any],
    stored_config: Mapping[str, Any],
    state: Mapping[str, Any],
    variant: str,
    table: str,
    chunks: Mapping[str, Any],
) -> None:
    """Require an existing complete index without invoking the load endpoint."""
    _validate_lane_config(config, variant, table, chunks)
    errors = []
    expected_count = chunks.get("chunks_count")
    identity = _index_identity(config)
    if state.get("status") != "complete":
        errors.append("index_state_status")
    if state.get("identity") != identity:
        errors.append("index_state_identity")
    if state.get("expected_count") != expected_count:
        errors.append("index_state_expected_count")
    if state.get("count_after") != expected_count:
        errors.append("index_state_count_after")
    if _index_identity(stored_config) != identity:
        errors.append("smoke_config_identity")
    if stored_config.get("index_document_count") != expected_count:
        errors.append("smoke_config_document_count")
    if config.get("index_document_count") != expected_count:
        errors.append("runtime_config_document_count")
    builder_source = state.get("builder_source") or {}
    for label in ("repository", "benchmark_repository"):
        snapshot = builder_source.get(label) or {}
        if not snapshot.get("commit"):
            errors.append(f"builder_{label}_commit")
        if snapshot.get("worktree_dirty") is not False:
            errors.append(f"builder_{label}_dirty")
    if errors:
        raise ValueError(
            f"invalid reusable {variant} index fields: {', '.join(errors)}"
        )


def _load_promoted_smoke_lineage(
    smoke_dir: Path,
    chunks: Mapping[str, Any],
    cases: Mapping[str, Any],
    context_summary: Mapping[str, Any],
    repository_root: Path,
    benchmark_root: Path,
) -> Dict[str, Any]:
    controller_manifest = load_artifact(
        str(smoke_dir / "controller.manifest.json"),
        CONTROLLER_MANIFEST_SCHEMA,
    )
    controller_report = load_artifact(
        str(smoke_dir / "controller.report.json"),
        CONTROLLER_REPORT_SCHEMA,
    )
    smoke_manifest = load_artifact(
        str(smoke_dir / "smoke.manifest.json"),
        RETRIEVAL_MANIFEST_SCHEMA,
    )
    smoke_report = load_artifact(
        str(smoke_dir / "smoke.json"),
        RETRIEVAL_REPORT_SCHEMA,
    )
    baseline_config = load_artifact(
        str(smoke_dir / "baseline.config.json"),
        SERVICE_CONFIG_SCHEMA,
    )
    contextual_config = load_artifact(
        str(smoke_dir / "contextual.config.json"),
        SERVICE_CONFIG_SCHEMA,
    )
    baseline_state = load_artifact(
        str(smoke_dir / "baseline.index-state.json"),
        INDEX_STATE_SCHEMA,
    )
    contextual_state = load_artifact(
        str(smoke_dir / "contextual.index-state.json"),
        INDEX_STATE_SCHEMA,
    )

    errors = []

    def require(label: str, actual: Any, expected: Any) -> None:
        if actual != expected:
            errors.append(label)

    require("controller_status", controller_report.get("status"), "valid")
    require(
        "controller_phase",
        controller_report.get("phase"),
        "retrieval_smoke_completed",
    )
    require(
        "controller_manifest_digest",
        controller_report.get("manifest_digest"),
        controller_manifest.get("artifact_digest"),
    )
    require(
        "controller_smoke_digest",
        controller_report.get("smoke_report_digest"),
        smoke_report.get("artifact_digest"),
    )
    require(
        "controller_baseline_config_digest",
        controller_report.get("baseline_config_digest"),
        baseline_config.get("artifact_digest"),
    )
    require(
        "controller_contextual_config_digest",
        controller_report.get("contextual_config_digest"),
        contextual_config.get("artifact_digest"),
    )
    require(
        "controller_baseline_index_digest",
        controller_report.get("baseline_index_state_digest"),
        baseline_state.get("artifact_digest"),
    )
    require(
        "controller_contextual_index_digest",
        controller_report.get("contextual_index_state_digest"),
        contextual_state.get("artifact_digest"),
    )
    expected_builder_source = {
        "repository": dict(controller_manifest.get("repository") or {}),
        "benchmark_repository": dict(
            controller_manifest.get("benchmark_repository") or {}
        ),
    }
    require(
        "baseline_index_builder_source",
        baseline_state.get("builder_source"),
        expected_builder_source,
    )
    require(
        "contextual_index_builder_source",
        contextual_state.get("builder_source"),
        expected_builder_source,
    )
    require(
        "smoke_manifest_digest",
        smoke_report.get("manifest_digest"),
        smoke_manifest.get("artifact_digest"),
    )
    require("smoke_evidence_status", smoke_report.get("evidence_status"), "valid")
    require(
        "smoke_formal_ab_eligible",
        smoke_report.get("formal_ab_eligible"),
        False,
    )
    require("smoke_runtime_errors", smoke_report.get("runtime_errors"), 0)
    require(
        "smoke_failed_request_attempts",
        smoke_report.get("failed_request_attempts"),
        0,
    )
    require(
        "smoke_promotion",
        (smoke_report.get("smoke_promotion") or {}).get("decision"),
        "promote",
    )
    require(
        "smoke_formal_method_conclusion",
        (smoke_report.get("smoke_promotion") or {}).get(
            "formal_method_conclusion"
        ),
        False,
    )
    require(
        "controller_baseline_only",
        controller_manifest.get("baseline_only"),
        False,
    )
    require(
        "controller_agent_initialized",
        controller_manifest.get("agent_initialized"),
        False,
    )
    require(
        "controller_judge_initialized",
        controller_manifest.get("judge_initialized"),
        False,
    )
    require(
        "controller_chunk_manifest",
        controller_manifest.get("chunk_manifest_digest"),
        chunks.get("artifact_digest"),
    )
    require(
        "controller_case_manifest",
        controller_manifest.get("case_manifest_digest"),
        cases.get("artifact_digest"),
    )
    require(
        "smoke_chunk_manifest",
        smoke_manifest.get("chunk_manifest_digest"),
        chunks.get("artifact_digest"),
    )
    require(
        "smoke_case_manifest",
        smoke_manifest.get("case_manifest_digest"),
        cases.get("artifact_digest"),
    )
    require("context_summary_status", context_summary.get("status"), "valid")
    require(
        "context_cache_identity",
        controller_manifest.get("context_cache_identity"),
        context_summary.get("cache_identity"),
    )
    require(
        "context_set_digest",
        controller_manifest.get("context_set_digest"),
        context_summary.get("context_set_digest"),
    )
    require(
        "context_expected_chunks",
        context_summary.get("expected_chunks"),
        chunks.get("chunks_count"),
    )
    require(
        "context_successful_chunks",
        context_summary.get("successful_chunks"),
        chunks.get("chunks_count"),
    )
    require("context_error_chunks", context_summary.get("error_chunks"), 0)
    require("context_missing_chunks", context_summary.get("missing_chunks"), 0)
    if not context_summary.get("context_set_digest"):
        errors.append("context_set_digest_missing")

    baseline_lane = controller_manifest.get("baseline") or {}
    contextual_lane = controller_manifest.get("contextual") or {}
    baseline_table = validate_table_name(str(baseline_lane.get("table") or ""))
    contextual_table = validate_table_name(
        str(contextual_lane.get("table") or "")
    )
    if baseline_table == contextual_table:
        errors.append("shared_index_table")
    require("baseline_config_table", baseline_config.get("pg_table"), baseline_table)
    require(
        "contextual_config_table",
        contextual_config.get("pg_table"),
        contextual_table,
    )
    baseline_public = smoke_manifest.get("baseline_config") or {}
    contextual_public = smoke_manifest.get("contextual_config") or {}
    if {
        key: baseline_config.get(key) for key in baseline_public
    } != baseline_public:
        errors.append("smoke_baseline_public_config")
    if {
        key: contextual_config.get(key) for key in contextual_public
    } != contextual_public:
        errors.append("smoke_contextual_public_config")
    require("baseline_context_set_digest", baseline_config.get("context_set_digest"), None)
    require(
        "contextual_context_set_digest",
        contextual_config.get("context_set_digest"),
        context_summary.get("context_set_digest"),
    )
    try:
        validate_reused_index(
            baseline_config,
            baseline_config,
            baseline_state,
            "baseline",
            baseline_table,
            chunks,
        )
        validate_reused_index(
            contextual_config,
            contextual_config,
            contextual_state,
            "contextual",
            contextual_table,
            chunks,
        )
    except ValueError as error:
        errors.append(str(error))
    if errors:
        raise ValueError(
            "invalid promoted smoke lineage: " + "; ".join(errors)
        )

    source_compatibility = _source_compatibility(
        repository_root,
        benchmark_root,
        controller_manifest,
    )
    return {
        "controller_manifest": controller_manifest,
        "controller_report": controller_report,
        "smoke_manifest": smoke_manifest,
        "smoke_report": smoke_report,
        "baseline_config": baseline_config,
        "contextual_config": contextual_config,
        "baseline_state": baseline_state,
        "contextual_state": contextual_state,
        "baseline_table": baseline_table,
        "contextual_table": contextual_table,
        "baseline_port": int(baseline_lane.get("port")),
        "contextual_port": int(contextual_lane.get("port")),
        "source_compatibility": source_compatibility,
    }


def _load_or_write_controller_manifest(
    path: Path,
    payload: Dict[str, Any],
) -> Dict[str, Any]:
    if path.exists():
        existing = load_artifact(str(path), CONTROLLER_MANIFEST_SCHEMA)
        if existing.get("controller_identity") != payload.get(
            "controller_identity"
        ):
            raise ValueError(
                "existing formal controller manifest belongs to another run"
            )
        return existing
    return write_artifact(str(path), payload)


def run_server_smoke(
    go_service_dir: str,
    chunks_path: str,
    cases_path: str,
    context_cache_path: str,
    output_dir: str,
    baseline_table: str,
    contextual_table: str,
    baseline_port: int = 8765,
    contextual_port: int = 8766,
    smoke_per_type: int = 10,
    bootstrap_resamples: int = 1000,
    bootstrap_seed: int = 20260722,
    service_start_timeout: float = 120,
    load_timeout: float = 7200,
    resume_indexes: bool = False,
    baseline_only: bool = False,
) -> Dict[str, Any]:
    """Build, index, run a paired smoke, and clean up only owned services."""
    baseline_table = validate_table_name(baseline_table)
    contextual_table = validate_table_name(contextual_table)
    if baseline_table == contextual_table:
        raise ValueError("baseline and contextual tables must differ")
    if not baseline_only and baseline_port == contextual_port:
        raise ValueError("baseline and contextual ports must differ")
    if smoke_per_type <= 0 or bootstrap_resamples <= 0:
        raise ValueError("smoke and bootstrap sizes must be positive")
    ports = (baseline_port,) if baseline_only else (baseline_port, contextual_port)
    for port in ports:
        if not _port_is_available(port):
            raise ValueError(f"port {port} is already in use")

    go_dir = Path(go_service_dir).resolve()
    target = Path(output_dir).resolve()
    target.mkdir(parents=True, exist_ok=True)
    chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
    cases = load_artifact(cases_path, CASE_SCHEMA)
    if cases.get("chunk_manifest_digest") != chunks.get("artifact_digest"):
        raise ValueError("case and chunk manifests do not match")
    question_type_counts = Counter(
        str(case.get("question_type")) for case in cases.get("cases") or []
    )
    insufficient_types = {
        question_type: question_type_counts.get(question_type, 0)
        for question_type in DEFAULT_QUESTION_TYPES
        if question_type_counts.get(question_type, 0) < smoke_per_type
    }
    if insufficient_types:
        raise ValueError(
            "dataset cannot satisfy the stratified smoke selection: "
            f"{insufficient_types}"
        )
    context_summary = summarize_context_cache(context_cache_path, chunks_path)
    if not baseline_only and context_summary.get("status") != "valid":
        raise ValueError("context cache summary is not valid")

    benchmark_root = Path(__file__).resolve().parents[2]
    repository_root = benchmark_root.parent
    repository_snapshot = _git_snapshot(repository_root)
    benchmark_repository_snapshot = _git_snapshot(benchmark_root)
    _require_clean_snapshot("root index builder", repository_snapshot)
    _require_clean_snapshot(
        "benchmark index builder",
        benchmark_repository_snapshot,
    )
    manifest_name = (
        "controller.baseline.manifest.json"
        if baseline_only
        else "controller.manifest.json"
    )
    manifest = write_artifact(
        str(target / manifest_name),
        {
            "schema_version": CONTROLLER_MANIFEST_SCHEMA,
            "run_kind": "contextual_retrieval_server_smoke",
            "evidence_scope": "retrieval_smoke",
            "created_at": datetime.now(timezone.utc).isoformat(),
            "repository": repository_snapshot,
            "benchmark_repository": benchmark_repository_snapshot,
            "chunk_manifest_digest": chunks["artifact_digest"],
            "case_manifest_digest": cases["artifact_digest"],
            "context_cache_identity": context_summary["cache_identity"],
            "context_set_digest": context_summary.get("context_set_digest"),
            "expected_chunks": chunks["chunks_count"],
            "expected_cases": smoke_per_type * 3,
            "baseline": {"table": baseline_table, "port": baseline_port},
            "contextual": {
                "table": contextual_table,
                "port": contextual_port,
            },
            "smoke_per_type": smoke_per_type,
            "bootstrap_resamples": bootstrap_resamples,
            "bootstrap_seed": bootstrap_seed,
            "resume_indexes": resume_indexes,
            "baseline_only": baseline_only,
            "judge_initialized": False,
            "agent_initialized": False,
            "invocation": [str(item) for item in sys.argv],
        },
    )

    owned: List[Tuple[subprocess.Popen, Any]] = []
    service_logs: Dict[str, str] = {}
    cleanup: List[Dict[str, Any]] = []
    baseline_config: Optional[Dict[str, Any]] = None
    contextual_config: Optional[Dict[str, Any]] = None
    baseline_state: Optional[Dict[str, Any]] = None
    contextual_state: Optional[Dict[str, Any]] = None
    smoke_report: Optional[Dict[str, Any]] = None
    failure: Optional[BaseException] = None
    build_log: Optional[Path] = None
    try:
        binary, build_log = _build_service(go_dir, target)
        lanes = [("baseline", baseline_table, baseline_port, None)]
        if not baseline_only:
            lanes.append(
                (
                    "contextual",
                    contextual_table,
                    contextual_port,
                    context_cache_path,
                )
            )
        for variant, table, port, cache in lanes:
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
            service_logs[variant] = str(log_path)
            _wait_for_health(
                process,
                f"http://127.0.0.1:{port}",
                service_start_timeout,
            )
        baseline_url = f"http://127.0.0.1:{baseline_port}"
        baseline_config = _request_json(baseline_url + "/config", timeout=30)
        _validate_lane_config(
            baseline_config,
            "baseline",
            baseline_table,
            chunks,
        )
        if baseline_config.get("context_set_digest") is not None:
            raise ValueError("baseline service unexpectedly has a Context set")
        baseline_state = _ensure_index(
            baseline_url,
            baseline_config,
            target / "baseline.index-state.json",
            target / "baseline.load-result.json",
            load_timeout,
            resume_indexes,
            repository_snapshot=repository_snapshot,
            benchmark_repository_snapshot=benchmark_repository_snapshot,
        )
        baseline_config = _request_json(baseline_url + "/config", timeout=30)
        write_artifact(
            str(target / "baseline.config.json"),
            {
                "schema_version": SERVICE_CONFIG_SCHEMA,
                **baseline_config,
            },
        )
        if not baseline_only:
            contextual_url = f"http://127.0.0.1:{contextual_port}"
            contextual_config = _request_json(
                contextual_url + "/config",
                timeout=30,
            )
            _validate_lane_config(
                contextual_config,
                "contextual",
                contextual_table,
                chunks,
            )
            if contextual_config.get("context_set_digest") != context_summary.get(
                "context_set_digest"
            ):
                raise ValueError(
                    "contextual runtime Context set does not match the summary"
                )
            contextual_state = _ensure_index(
                contextual_url,
                contextual_config,
                target / "contextual.index-state.json",
                target / "contextual.load-result.json",
                load_timeout,
                resume_indexes,
                repository_snapshot=repository_snapshot,
                benchmark_repository_snapshot=benchmark_repository_snapshot,
            )
            contextual_config = _request_json(
                contextual_url + "/config",
                timeout=30,
            )
            write_artifact(
                str(target / "contextual.config.json"),
                {
                    "schema_version": SERVICE_CONFIG_SCHEMA,
                    **contextual_config,
                },
            )
            smoke_report = run_retrieval_ab(
                cases_path,
                chunks_path,
                baseline_url,
                contextual_url,
                str(target / "smoke.json"),
                bootstrap_resamples=bootstrap_resamples,
                bootstrap_seed=bootstrap_seed,
                smoke_per_type=smoke_per_type,
            )
    except BaseException as error:
        failure = error
    finally:
        for process, log_handle in reversed(owned):
            cleanup.append(_stop_owned_process(process))
            log_handle.close()

    report_name = (
        "controller.baseline.report.json"
        if baseline_only
        else "controller.report.json"
    )
    report = write_artifact(
        str(target / report_name),
        {
            "schema_version": CONTROLLER_REPORT_SCHEMA,
            "status": "valid" if failure is None else "insufficient",
            "phase": (
                "baseline_index_prepared"
                if baseline_only
                else "retrieval_smoke_completed"
            ),
            "manifest_digest": manifest["artifact_digest"],
            "baseline_config_digest": _artifact_digest_if_exists(
                target / "baseline.config.json",
                SERVICE_CONFIG_SCHEMA,
            ),
            "contextual_config_digest": _artifact_digest_if_exists(
                target / "contextual.config.json",
                SERVICE_CONFIG_SCHEMA,
            ),
            "baseline_index_state_digest": (
                baseline_state.get("artifact_digest") if baseline_state else None
            ),
            "contextual_index_state_digest": (
                contextual_state.get("artifact_digest") if contextual_state else None
            ),
            "smoke_report_digest": (
                smoke_report.get("artifact_digest") if smoke_report else None
            ),
            "smoke_promotion": (
                smoke_report.get("smoke_promotion") if smoke_report else None
            ),
            "build_log": str(build_log) if build_log else None,
            "service_logs": service_logs,
            "cleanup": cleanup,
            "error": _redacted_error(failure) if failure else None,
            "completed_at": datetime.now(timezone.utc).isoformat(),
        },
    )
    if failure is not None:
        raise RuntimeError(
            f"server smoke failed; inspect {target / report_name}"
        ) from failure
    return report


def run_server_formal(
    go_service_dir: str,
    chunks_path: str,
    cases_path: str,
    context_cache_path: str,
    smoke_dir: str,
    output_dir: str,
    baseline_port: Optional[int] = None,
    contextual_port: Optional[int] = None,
    conformance_smoke_per_type: int = 10,
    conformance_bootstrap_resamples: int = 1000,
    bootstrap_resamples: int = 10000,
    bootstrap_seed: int = 20260722,
    service_start_timeout: float = 120,
    request_timeout: float = 120,
    request_attempts: int = 3,
) -> Dict[str, Any]:
    """Reuse promoted smoke indexes and run the guarded formal 450-case A/B."""
    if conformance_smoke_per_type <= 0:
        raise ValueError("conformance smoke size must be positive")
    if conformance_bootstrap_resamples <= 0 or bootstrap_resamples <= 0:
        raise ValueError("bootstrap sizes must be positive")
    if service_start_timeout <= 0 or request_timeout <= 0:
        raise ValueError("timeouts must be positive")
    if request_attempts <= 0:
        raise ValueError("request attempts must be positive")

    go_dir = Path(go_service_dir).resolve()
    smoke_root = Path(smoke_dir).resolve()
    target = Path(output_dir).resolve()
    if smoke_root == target:
        raise ValueError("formal output directory must differ from smoke directory")
    target.mkdir(parents=True, exist_ok=True)

    owned: List[Tuple[subprocess.Popen, Any]] = []
    service_logs: Dict[str, str] = {}
    cleanup: List[Dict[str, Any]] = []
    manifest: Optional[Dict[str, Any]] = None
    lineage: Optional[Dict[str, Any]] = None
    conformance_report: Optional[Dict[str, Any]] = None
    formal_report: Optional[Dict[str, Any]] = None
    failure: Optional[BaseException] = None
    build_log: Optional[Path] = None
    runtime_baseline_config: Optional[Dict[str, Any]] = None
    runtime_contextual_config: Optional[Dict[str, Any]] = None
    try:
        chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
        cases = load_artifact(cases_path, CASE_SCHEMA)
        if cases.get("chunk_manifest_digest") != chunks.get(
            "artifact_digest"
        ):
            raise ValueError("case and chunk manifests do not match")
        context_summary = summarize_context_cache(
            context_cache_path,
            chunks_path,
        )
        if context_summary.get("status") != "valid":
            raise ValueError("context cache summary is not valid")

        benchmark_root = Path(__file__).resolve().parents[2]
        repository_root = benchmark_root.parent
        lineage = _load_promoted_smoke_lineage(
            smoke_root,
            chunks,
            cases,
            context_summary,
            repository_root,
            benchmark_root,
        )
        selected_baseline_port = (
            baseline_port
            if baseline_port is not None
            else lineage["baseline_port"]
        )
        selected_contextual_port = (
            contextual_port
            if contextual_port is not None
            else lineage["contextual_port"]
        )
        if selected_baseline_port == selected_contextual_port:
            raise ValueError("baseline and contextual ports must differ")

        controller_identity_payload = {
            "run_kind": "contextual_retrieval_server_formal",
            "smoke_controller_manifest_digest": lineage[
                "controller_manifest"
            ]["artifact_digest"],
            "smoke_controller_report_digest": lineage["controller_report"][
                "artifact_digest"
            ],
            "promoted_smoke_report_digest": lineage["smoke_report"][
                "artifact_digest"
            ],
            "baseline_index_state_digest": lineage["baseline_state"][
                "artifact_digest"
            ],
            "contextual_index_state_digest": lineage["contextual_state"][
                "artifact_digest"
            ],
            "chunk_manifest_digest": chunks["artifact_digest"],
            "case_manifest_digest": cases["artifact_digest"],
            "context_cache_identity": context_summary["cache_identity"],
            "context_set_digest": context_summary.get("context_set_digest"),
            "repository": lineage["source_compatibility"][
                "current_repository"
            ],
            "benchmark_repository": lineage["source_compatibility"][
                "current_benchmark_repository"
            ],
            "baseline": {
                "table": lineage["baseline_table"],
                "port": selected_baseline_port,
            },
            "contextual": {
                "table": lineage["contextual_table"],
                "port": selected_contextual_port,
            },
            "conformance_smoke_per_type": conformance_smoke_per_type,
            "conformance_bootstrap_resamples": (
                conformance_bootstrap_resamples
            ),
            "bootstrap_resamples": bootstrap_resamples,
            "bootstrap_seed": bootstrap_seed,
            "request_timeout": request_timeout,
            "request_attempts": request_attempts,
        }
        controller_identity = canonical_digest(controller_identity_payload)
        manifest = _load_or_write_controller_manifest(
            target / "controller.formal.manifest.json",
            {
                "schema_version": CONTROLLER_MANIFEST_SCHEMA,
                "run_kind": "contextual_retrieval_server_formal",
                "evidence_scope": "retrieval_effectiveness",
                "controller_identity": controller_identity,
                "created_at": datetime.now(timezone.utc).isoformat(),
                **controller_identity_payload,
                "source_compatibility": lineage["source_compatibility"],
                "expected_chunks": chunks["chunks_count"],
                "expected_cases": cases["cases_count"],
                "load_endpoint_allowed": False,
                "judge_initialized": False,
                "agent_initialized": False,
                "invocation": [str(item) for item in sys.argv],
            },
        )

        for port in (selected_baseline_port, selected_contextual_port):
            if not _port_is_available(port):
                raise ValueError(f"port {port} is already in use")

        binary, build_log = _build_service(go_dir, target)
        for variant, table, port, cache in (
            (
                "baseline",
                lineage["baseline_table"],
                selected_baseline_port,
                None,
            ),
            (
                "contextual",
                lineage["contextual_table"],
                selected_contextual_port,
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
            service_logs[variant] = str(log_path)
            _wait_for_health(
                process,
                f"http://127.0.0.1:{port}",
                service_start_timeout,
            )

        baseline_url = f"http://127.0.0.1:{selected_baseline_port}"
        contextual_url = f"http://127.0.0.1:{selected_contextual_port}"
        runtime_baseline_config = _request_json(
            baseline_url + "/config",
            timeout=30,
        )
        runtime_contextual_config = _request_json(
            contextual_url + "/config",
            timeout=30,
        )
        validate_reused_index(
            runtime_baseline_config,
            lineage["baseline_config"],
            lineage["baseline_state"],
            "baseline",
            lineage["baseline_table"],
            chunks,
        )
        validate_reused_index(
            runtime_contextual_config,
            lineage["contextual_config"],
            lineage["contextual_state"],
            "contextual",
            lineage["contextual_table"],
            chunks,
        )
        write_artifact(
            str(target / "baseline.formal.config.json"),
            {
                "schema_version": SERVICE_CONFIG_SCHEMA,
                **runtime_baseline_config,
            },
        )
        write_artifact(
            str(target / "contextual.formal.config.json"),
            {
                "schema_version": SERVICE_CONFIG_SCHEMA,
                **runtime_contextual_config,
            },
        )

        conformance_report = run_retrieval_ab(
            cases_path,
            chunks_path,
            baseline_url,
            contextual_url,
            str(target / "conformance-smoke.json"),
            timeout=request_timeout,
            request_attempts=request_attempts,
            bootstrap_resamples=conformance_bootstrap_resamples,
            bootstrap_seed=bootstrap_seed,
            smoke_per_type=conformance_smoke_per_type,
        )
        promotion = conformance_report.get("smoke_promotion") or {}
        if promotion.get("decision") == "promote":
            formal_report = run_retrieval_ab(
                cases_path,
                chunks_path,
                baseline_url,
                contextual_url,
                str(target / "formal.json"),
                timeout=request_timeout,
                request_attempts=request_attempts,
                bootstrap_resamples=bootstrap_resamples,
                bootstrap_seed=bootstrap_seed,
            )
    except BaseException as error:
        failure = error
    finally:
        for process, log_handle in reversed(owned):
            cleanup.append(_stop_owned_process(process))
            log_handle.close()

    phase = "formal_controller_failed"
    if failure is None and formal_report is not None:
        phase = "retrieval_formal_completed"
    elif failure is None and conformance_report is not None:
        phase = "conformance_smoke_completed"
    report = write_artifact(
        str(target / "controller.formal.report.json"),
        {
            "schema_version": CONTROLLER_REPORT_SCHEMA,
            "status": "valid" if failure is None else "insufficient",
            "phase": phase,
            "manifest_digest": (
                manifest.get("artifact_digest") if manifest else None
            ),
            "promoted_smoke_report_digest": (
                lineage["smoke_report"].get("artifact_digest")
                if lineage
                else None
            ),
            "baseline_index_state_digest": (
                lineage["baseline_state"].get("artifact_digest")
                if lineage
                else None
            ),
            "contextual_index_state_digest": (
                lineage["contextual_state"].get("artifact_digest")
                if lineage
                else None
            ),
            "baseline_formal_config_digest": _artifact_digest_if_exists(
                target / "baseline.formal.config.json",
                SERVICE_CONFIG_SCHEMA,
            ),
            "contextual_formal_config_digest": _artifact_digest_if_exists(
                target / "contextual.formal.config.json",
                SERVICE_CONFIG_SCHEMA,
            ),
            "conformance_report_digest": (
                conformance_report.get("artifact_digest")
                if conformance_report
                else None
            ),
            "conformance_promotion": (
                conformance_report.get("smoke_promotion")
                if conformance_report
                else None
            ),
            "formal_report_digest": (
                formal_report.get("artifact_digest")
                if formal_report
                else None
            ),
            "formal_evidence_status": (
                formal_report.get("evidence_status")
                if formal_report
                else None
            ),
            "formal_ab_eligible": (
                formal_report.get("formal_ab_eligible")
                if formal_report
                else False
            ),
            "formal_gate": (
                formal_report.get("gate") if formal_report else None
            ),
            "load_endpoint_called": False,
            "build_log": str(build_log) if build_log else None,
            "service_logs": service_logs,
            "cleanup": cleanup,
            "error": _redacted_error(failure) if failure else None,
            "completed_at": datetime.now(timezone.utc).isoformat(),
        },
    )
    if failure is not None:
        raise RuntimeError(
            "server formal run failed; inspect "
            f"{target / 'controller.formal.report.json'}"
        ) from failure
    return report
