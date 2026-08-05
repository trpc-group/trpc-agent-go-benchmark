#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Durable, resumable LLM context generation for contextual retrieval."""

from __future__ import annotations

import concurrent.futures
import json
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, List, Mapping, Optional, Tuple

from contextual_retrieval.artifacts import (
    canonical_digest,
    load_artifact,
    public_endpoint_identity,
    text_digest,
    write_artifact,
)
from contextual_retrieval.dataset import CHUNK_SCHEMA, PARENT_SCHEMA


CONTEXT_CACHE_SCHEMA = "contextual-retrieval/context-cache/v1"
CONTEXT_SUMMARY_SCHEMA = "contextual-retrieval/context-cache-summary/v2"
CONTEXT_PROBE_SCHEMA = "contextual-retrieval/context-probe/v1"
CONTEXT_PROMPT_ID = "anthropic-contextual-retrieval-v1"
NORMAL_FINISH_REASONS = frozenset(("stop",))
CONTEXT_FINISH_REASON_POLICY = "normal-stop-only-v1"
CONTEXT_PROMPT = """<document>
{document}
</document>
<chunk>
{chunk}
</chunk>
Give a short, succinct context that situates this chunk within the overall
document for the purpose of improving retrieval. Include only information
needed to resolve the chunk's topic, entities, time, section, or references.
Do not summarize or rewrite the chunk. Answer with only the context."""


def context_config_from_env() -> Dict[str, Any]:
    """Load an explicit index-build model configuration from the environment."""
    model = os.environ.get("CONTEXT_MODEL_NAME", "").strip()
    api_key = os.environ.get("CONTEXT_API_KEY", "").strip()
    base_url = os.environ.get("CONTEXT_BASE_URL", "").strip()
    if not model:
        raise ValueError("CONTEXT_MODEL_NAME is required")
    if not api_key:
        raise ValueError("CONTEXT_API_KEY is required")
    if not base_url:
        raise ValueError("CONTEXT_BASE_URL is required")
    headers = {}
    if value := os.environ.get("CONTEXT_SMG_ROUTING_KEY"):
        headers["X-SMG-Routing-Key"] = value
    if value := os.environ.get("CONTEXT_SMG_AGENT_NAME"):
        headers["X-SMG-Agent-Name"] = value
    max_tokens = int(os.environ.get("CONTEXT_MAX_TOKENS", "4096"))
    timeout_seconds = float(
        os.environ.get("CONTEXT_TIMEOUT_SECONDS", "300")
    )
    retry_base_delay_seconds = float(
        os.environ.get("CONTEXT_RETRY_BASE_DELAY_SECONDS", "2")
    )
    retry_max_delay_seconds = float(
        os.environ.get("CONTEXT_RETRY_MAX_DELAY_SECONDS", "30")
    )
    if max_tokens <= 0:
        raise ValueError("CONTEXT_MAX_TOKENS must be positive")
    if timeout_seconds <= 0:
        raise ValueError("CONTEXT_TIMEOUT_SECONDS must be positive")
    if retry_base_delay_seconds < 0 or retry_max_delay_seconds < 0:
        raise ValueError("CONTEXT retry delays must be non-negative")
    if retry_max_delay_seconds < retry_base_delay_seconds:
        raise ValueError(
            "CONTEXT_RETRY_MAX_DELAY_SECONDS must be at least the base delay"
        )
    return {
        "model": model,
        "api_key": api_key,
        "base_url": base_url,
        "headers": headers,
        "max_tokens": max_tokens,
        "timeout_seconds": timeout_seconds,
        "retry_base_delay_seconds": retry_base_delay_seconds,
        "retry_max_delay_seconds": retry_max_delay_seconds,
    }


def _public_config(config: Mapping[str, Any]) -> Dict[str, Any]:
    return {
        "model": config["model"],
        "endpoint": public_endpoint_identity(str(config["base_url"])),
        "header_names": sorted((config.get("headers") or {}).keys()),
        "max_tokens": int(config["max_tokens"]),
        "timeout_seconds": float(config.get("timeout_seconds", 300)),
        "retry_base_delay_seconds": float(
            config.get("retry_base_delay_seconds", 2)
        ),
        "retry_max_delay_seconds": float(
            config.get("retry_max_delay_seconds", 30)
        ),
        "temperature": 0,
        "reasoning": "unspecified",
        "finish_reason_policy": CONTEXT_FINISH_REASON_POLICY,
        "transport_max_retries": 0,
        "prompt_id": CONTEXT_PROMPT_ID,
        "prompt_hash": text_digest(CONTEXT_PROMPT),
    }


def _read_cache(path: str) -> Tuple[Optional[Dict[str, Any]], List[Dict[str, Any]]]:
    target = Path(path)
    if not target.exists():
        return None, []
    header: Optional[Dict[str, Any]] = None
    attempts: List[Dict[str, Any]] = []
    with target.open("r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, start=1):
            if not line.strip():
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError as error:
                raise ValueError(
                    f"context cache line {line_number} is invalid JSON"
                ) from error
            if not isinstance(record, dict):
                raise ValueError(
                    f"context cache line {line_number} must be an object"
                )
            record_type = record.get("record_type")
            if record_type == "header":
                if header is not None or line_number != 1:
                    raise ValueError("context cache must have one header on line 1")
                header = record
            elif record_type == "attempt":
                if header is None:
                    raise ValueError("context cache attempt precedes header")
                attempts.append(record)
            else:
                raise ValueError(
                    f"context cache line {line_number} has invalid record_type"
                )
    if header is None:
        raise ValueError("context cache header is missing")
    return header, attempts


def _append_record(handle: Any, record: Mapping[str, Any]) -> None:
    handle.write(
        json.dumps(
            record,
            ensure_ascii=False,
            separators=(",", ":"),
            sort_keys=True,
            allow_nan=False,
        )
        + "\n"
    )
    handle.flush()
    os.fsync(handle.fileno())


def _new_header(
    chunks: Mapping[str, Any],
    public_config: Mapping[str, Any],
) -> Dict[str, Any]:
    identity_payload = {
        "schema_version": CONTEXT_CACHE_SCHEMA,
        "chunk_manifest_digest": chunks["artifact_digest"],
        "parent_manifest_digest": chunks["parent_manifest_digest"],
        "config": dict(public_config),
    }
    return {
        "record_type": "header",
        **identity_payload,
        "cache_identity": canonical_digest(identity_payload),
        "created_at": datetime.now(timezone.utc).isoformat(),
    }


def _latest_attempts(
    attempts: Iterable[Mapping[str, Any]],
) -> Tuple[Dict[str, Dict[str, Any]], Dict[str, int]]:
    latest: Dict[str, Dict[str, Any]] = {}
    ordinals: Dict[str, int] = {}
    for raw in attempts:
        chunk_id = raw.get("chunk_id")
        attempt = raw.get("attempt")
        if not isinstance(chunk_id, str) or not chunk_id:
            raise ValueError("context cache attempt has invalid chunk_id")
        if not isinstance(attempt, int) or attempt <= 0:
            raise ValueError(f"context cache attempt for {chunk_id} has invalid ordinal")
        latest[chunk_id] = dict(raw)
        ordinals[chunk_id] = max(ordinals.get(chunk_id, 0), attempt)
    return latest, ordinals


def _redacted_error(
    error: BaseException,
    config: Mapping[str, Any],
) -> str:
    try:
        endpoint = public_endpoint_identity(str(config.get("base_url") or ""))
    except ValueError:
        endpoint = "unavailable"
    return (
        f"{type(error).__name__}: context generation failed; "
        f"endpoint={endpoint}"
    )


def _generate_one(
    client: Any,
    config: Mapping[str, Any],
    parent: Mapping[str, Any],
    chunk: Mapping[str, Any],
    attempt: int,
    retry_delay_seconds: float = 0,
) -> Dict[str, Any]:
    if retry_delay_seconds > 0:
        time.sleep(retry_delay_seconds)
    started = time.monotonic()
    prompt = CONTEXT_PROMPT.format(
        document=parent["content"],
        chunk=chunk["content"],
    )
    input_characters = {
        "parent_characters": len(parent["content"]),
        "chunk_characters": len(chunk["content"]),
        "prompt_characters": len(prompt),
    }
    base = {
        "record_type": "attempt",
        "chunk_id": chunk["chunk_id"],
        "parent_document_id": chunk["parent_document_id"],
        "parent_content_hash": chunk["parent_content_hash"],
        "chunk_content_hash": chunk["chunk_content_hash"],
        "attempt": attempt,
        "retry_delay_seconds": retry_delay_seconds,
        "input_characters": input_characters,
    }
    finish_reason: Optional[str] = None
    usage_record: Optional[Dict[str, Any]] = None
    try:
        response = client.chat.completions.create(
            model=config["model"],
            messages=[
                {
                    "role": "user",
                    "content": prompt,
                }
            ],
            temperature=0,
            max_tokens=int(config["max_tokens"]),
        )
        choice = response.choices[0]
        raw_finish_reason = getattr(choice, "finish_reason", None)
        if raw_finish_reason is not None:
            finish_reason = str(raw_finish_reason).strip()
        usage = getattr(response, "usage", None)
        prompt_details = getattr(usage, "prompt_tokens_details", None)
        cached_prompt_tokens = getattr(prompt_details, "cached_tokens", None)
        if cached_prompt_tokens is None:
            cached_prompt_tokens = getattr(
                usage,
                "cache_read_input_tokens",
                None,
            )
        usage_record = {
            "prompt_tokens": getattr(
                usage,
                "prompt_tokens",
                getattr(usage, "input_tokens", None),
            ),
            "completion_tokens": getattr(
                usage,
                "completion_tokens",
                getattr(usage, "output_tokens", None),
            ),
            "total_tokens": getattr(usage, "total_tokens", None),
            "cached_prompt_tokens": cached_prompt_tokens,
        }
        normalized_finish_reason = (finish_reason or "").lower()
        if normalized_finish_reason not in NORMAL_FINISH_REASONS:
            raise ValueError(
                "context model did not finish with a normal stop: "
                f"finish_reason={finish_reason!r}"
            )
        content = choice.message.content
        if not isinstance(content, str) or not content.strip():
            raise ValueError("context model returned empty text")
        return {
            **base,
            "status": "success",
            "completed_at": datetime.now(timezone.utc).isoformat(),
            "context": content.strip(),
            "context_hash": text_digest(content.strip()),
            "finish_reason": finish_reason,
            "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
            "usage": usage_record,
            "error": None,
        }
    except Exception as error:
        return {
            **base,
            "status": "error",
            "completed_at": datetime.now(timezone.utc).isoformat(),
            "context": None,
            "context_hash": None,
            "finish_reason": finish_reason,
            "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
            "usage": usage_record,
            "error": _redacted_error(error, config),
        }


def _new_openai_client(config: Mapping[str, Any]) -> Any:
    from openai import OpenAI

    return OpenAI(
        api_key=config["api_key"],
        base_url=config["base_url"],
        default_headers=dict(config.get("headers") or {}),
        timeout=float(config.get("timeout_seconds", 300)),
        max_retries=0,
    )


def _retry_delay(config: Mapping[str, Any], run_attempt: int) -> float:
    if run_attempt <= 1:
        return 0
    base = float(config.get("retry_base_delay_seconds", 2))
    maximum = float(config.get("retry_max_delay_seconds", 30))
    return min(maximum, base * (2 ** (run_attempt - 2)))


def _usage_totals(records: Iterable[Mapping[str, Any]]) -> Dict[str, int]:
    return {
        field: sum(
            int((record.get("usage") or {}).get(field) or 0)
            for record in records
        )
        for field in (
            "prompt_tokens",
            "completion_tokens",
            "total_tokens",
            "cached_prompt_tokens",
        )
    }


def _input_character_totals(
    records: Iterable[Mapping[str, Any]],
) -> Dict[str, int]:
    return {
        field: sum(
            int((record.get("input_characters") or {}).get(field) or 0)
            for record in records
        )
        for field in (
            "parent_characters",
            "chunk_characters",
            "prompt_characters",
        )
    }


def _finish_reason_counts(
    records: Iterable[Mapping[str, Any]],
) -> Dict[str, int]:
    counts: Dict[str, int] = {}
    for record in records:
        value = record.get("finish_reason")
        key = str(value).strip() if value is not None else "missing"
        if not key:
            key = "missing"
        counts[key] = counts.get(key, 0) + 1
    return dict(sorted(counts.items()))


def _emit_progress(
    progress_stream: Any,
    event: str,
    expected_chunks: int,
    completed_chunks: int,
    successful_chunks: int,
    final_error_chunks: int,
    attempt_errors: int,
    attempt_records: int,
    started: float,
    usage_totals: Mapping[str, int],
) -> None:
    elapsed = max(0.0, time.monotonic() - started)
    session_completed = successful_chunks + final_error_chunks
    remaining = max(0, expected_chunks - completed_chunks)
    rate = session_completed / elapsed if elapsed > 0 else 0
    eta_seconds = remaining / rate if rate > 0 else None
    print(
        json.dumps(
            {
                "event": event,
                "expected_chunks": expected_chunks,
                "completed_chunks": completed_chunks,
                "successful_chunks_this_run": successful_chunks,
                "final_error_chunks_this_run": final_error_chunks,
                "attempt_errors_this_run": attempt_errors,
                "attempt_records_this_run": attempt_records,
                "elapsed_seconds": round(elapsed, 3),
                "chunks_per_second": round(rate, 4),
                "eta_seconds": (
                    round(eta_seconds, 3) if eta_seconds is not None else None
                ),
                "usage_totals_this_run": dict(usage_totals),
            },
            ensure_ascii=False,
            sort_keys=True,
        ),
        file=progress_stream,
        flush=True,
    )


def generate_context_cache(
    parents_path: str,
    chunks_path: str,
    cache_path: str,
    config: Mapping[str, Any],
    workers: int = 8,
    attempts_per_run: int = 3,
    progress_interval_seconds: float = 10,
    progress_stream: Any = None,
) -> Dict[str, Any]:
    """Generate every missing context, appending each attempt durably."""
    if workers <= 0:
        raise ValueError("workers must be positive")
    if attempts_per_run <= 0:
        raise ValueError("attempts_per_run must be positive")
    if progress_interval_seconds <= 0:
        raise ValueError("progress_interval_seconds must be positive")
    parents = load_artifact(parents_path, PARENT_SCHEMA)
    chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
    if chunks.get("parent_manifest_digest") != parents.get("artifact_digest"):
        raise ValueError("parent and chunk manifests do not match")

    parent_by_id = {
        record["parent_document_id"]: record for record in parents["parents"]
    }
    chunk_by_id = {record["chunk_id"]: record for record in chunks["chunks"]}
    if len(chunk_by_id) != chunks["chunks_count"]:
        raise ValueError("chunk manifest contains duplicate chunk IDs")
    for chunk in chunks["chunks"]:
        parent = parent_by_id.get(chunk["parent_document_id"])
        if parent is None or parent["content_hash"] != chunk["parent_content_hash"]:
            raise ValueError(f"chunk {chunk['chunk_id']} has no matching parent")

    public_config = _public_config(config)
    expected_header = _new_header(chunks, public_config)
    header, previous_attempts = _read_cache(cache_path)
    target = Path(cache_path)
    target.parent.mkdir(parents=True, exist_ok=True)
    if header is None:
        with target.open("x", encoding="utf-8") as handle:
            _append_record(handle, expected_header)
        header = expected_header
    else:
        for field in (
            "schema_version",
            "chunk_manifest_digest",
            "parent_manifest_digest",
            "cache_identity",
            "config",
        ):
            if header.get(field) != expected_header.get(field):
                raise ValueError(
                    f"context cache header field {field} does not match this run"
                )

    latest, ordinals = _latest_attempts(previous_attempts)
    unknown_chunks = sorted(set(latest) - set(chunk_by_id))
    if unknown_chunks:
        raise ValueError(
            f"context cache contains {len(unknown_chunks)} unknown chunk IDs"
        )
    pending = [
        chunk
        for chunk in chunks["chunks"]
        if latest.get(chunk["chunk_id"], {}).get("status") != "success"
    ]
    if not pending:
        return summarize_context_cache(cache_path, chunks_path)

    client = _new_openai_client(config)
    stream = progress_stream if progress_stream is not None else sys.stderr
    started = time.monotonic()
    last_progress_at = started
    completed_before_run = len(chunks["chunks"]) - len(pending)
    successful_this_run = 0
    final_errors_this_run = 0
    attempt_errors_this_run = 0
    new_attempts: List[Dict[str, Any]] = []
    _emit_progress(
        stream,
        "context_generation_started",
        len(chunks["chunks"]),
        completed_before_run,
        successful_this_run,
        final_errors_this_run,
        attempt_errors_this_run,
        len(new_attempts),
        started,
        _usage_totals(new_attempts),
    )
    with target.open("a", encoding="utf-8") as handle:
        with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as executor:
            futures: Dict[concurrent.futures.Future, Tuple[Dict[str, Any], int]] = {}
            pending_iterator = iter(pending)

            def submit(chunk: Dict[str, Any], run_attempt: int) -> None:
                ordinal = ordinals.get(chunk["chunk_id"], 0) + 1
                future = executor.submit(
                    _generate_one,
                    client,
                    config,
                    parent_by_id[chunk["parent_document_id"]],
                    chunk,
                    ordinal,
                    _retry_delay(config, run_attempt),
                )
                futures[future] = (chunk, run_attempt)

            for _ in range(min(workers, len(pending))):
                submit(next(pending_iterator), 1)
            while futures:
                done, _ = concurrent.futures.wait(
                    futures,
                    return_when=concurrent.futures.FIRST_COMPLETED,
                )
                for future in done:
                    chunk, run_attempt = futures.pop(future)
                    record = future.result()
                    _append_record(handle, record)
                    new_attempts.append(record)
                    ordinals[chunk["chunk_id"]] = record["attempt"]
                    if (
                        record["status"] != "success"
                        and run_attempt < attempts_per_run
                    ):
                        attempt_errors_this_run += 1
                        submit(chunk, run_attempt + 1)
                    else:
                        if record["status"] == "success":
                            successful_this_run += 1
                        else:
                            attempt_errors_this_run += 1
                            final_errors_this_run += 1
                        next_chunk = next(pending_iterator, None)
                        if next_chunk is not None:
                            submit(next_chunk, 1)
                    now = time.monotonic()
                    if now - last_progress_at >= progress_interval_seconds:
                        _emit_progress(
                            stream,
                            "context_generation_progress",
                            len(chunks["chunks"]),
                            completed_before_run
                            + successful_this_run
                            + final_errors_this_run,
                            successful_this_run,
                            final_errors_this_run,
                            attempt_errors_this_run,
                            len(new_attempts),
                            started,
                            _usage_totals(new_attempts),
                        )
                        last_progress_at = now
    _emit_progress(
        stream,
        "context_generation_finished",
        len(chunks["chunks"]),
        completed_before_run + successful_this_run + final_errors_this_run,
        successful_this_run,
        final_errors_this_run,
        attempt_errors_this_run,
        len(new_attempts),
        started,
        _usage_totals(new_attempts),
    )
    return summarize_context_cache(cache_path, chunks_path)


def select_probe_chunks(
    parents: Mapping[str, Any],
    chunks: Mapping[str, Any],
    count: int = 20,
) -> List[Dict[str, Any]]:
    """Select deterministic, structurally diverse chunks for a model probe."""
    if count <= 0:
        raise ValueError("probe count must be positive")
    all_chunks = list(chunks.get("chunks") or [])
    if count > len(all_chunks):
        raise ValueError(
            f"probe count {count} exceeds chunk count {len(all_chunks)}"
        )
    parent_by_id = {
        parent["parent_document_id"]: parent
        for parent in parents.get("parents") or []
    }
    chunks_by_parent: Dict[str, List[Dict[str, Any]]] = {}
    for chunk in all_chunks:
        parent_id = chunk.get("parent_document_id")
        parent = parent_by_id.get(parent_id)
        if parent is None or parent.get("content_hash") != chunk.get(
            "parent_content_hash"
        ):
            raise ValueError(f"chunk {chunk.get('chunk_id')} has no matching parent")
        chunks_by_parent.setdefault(str(parent_id), []).append(dict(chunk))
    for parent_chunks in chunks_by_parent.values():
        parent_chunks.sort(
            key=lambda item: (int(item.get("chunk_index", 0)), item["chunk_id"])
        )

    selected: List[Dict[str, Any]] = []
    selected_ids: set[str] = set()

    def add(chunk: Mapping[str, Any], reason: str) -> None:
        chunk_id = str(chunk["chunk_id"])
        if len(selected) >= count or chunk_id in selected_ids:
            return
        selected_ids.add(chunk_id)
        selected.append({"chunk": dict(chunk), "selection_reason": reason})

    parents_by_longest = sorted(
        parent_by_id.values(),
        key=lambda item: (-len(item["content"]), item["parent_document_id"]),
    )
    parents_by_shortest = sorted(
        parent_by_id.values(),
        key=lambda item: (len(item["content"]), item["parent_document_id"]),
    )
    for label, ranked_parents in (
        ("long_parent", parents_by_longest[:3]),
        ("short_parent", parents_by_shortest[:3]),
    ):
        for parent_rank, parent in enumerate(ranked_parents, start=1):
            parent_chunks = chunks_by_parent[parent["parent_document_id"]]
            positions = (
                ("first", 0),
                ("middle", len(parent_chunks) // 2),
                ("last", len(parent_chunks) - 1),
            )
            for position_name, position in positions:
                add(
                    parent_chunks[position],
                    f"{label}_{parent_rank}_{position_name}",
                )

    for chunk in sorted(
        all_chunks,
        key=lambda item: (len(item["content"]), item["chunk_id"]),
    ):
        add(chunk, "short_chunk")
        if len(selected) >= count:
            break

    ordered_chunks = sorted(all_chunks, key=lambda item: item["chunk_id"])
    spread_slots = max(count, 2)
    for slot in range(spread_slots):
        position = round(slot * (len(ordered_chunks) - 1) / (spread_slots - 1))
        add(ordered_chunks[position], f"deterministic_spread_{slot + 1}")
        if len(selected) >= count:
            break
    for chunk in ordered_chunks:
        add(chunk, "deterministic_fill")
        if len(selected) >= count:
            break
    return selected


def probe_context_generation(
    parents_path: str,
    chunks_path: str,
    output_path: str,
    config: Mapping[str, Any],
    count: int = 20,
    attempts_per_item: int = 3,
    client: Any = None,
) -> Dict[str, Any]:
    """Probe the Context model without writing to the formal context cache."""
    if attempts_per_item <= 0:
        raise ValueError("attempts_per_item must be positive")
    parents = load_artifact(parents_path, PARENT_SCHEMA)
    chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
    if chunks.get("parent_manifest_digest") != parents.get("artifact_digest"):
        raise ValueError("parent and chunk manifests do not match")
    parent_by_id = {
        parent["parent_document_id"]: parent for parent in parents["parents"]
    }
    selections = select_probe_chunks(parents, chunks, count=count)
    model_client = client if client is not None else _new_openai_client(config)
    attempts: List[Dict[str, Any]] = []
    results: List[Dict[str, Any]] = []
    started = time.monotonic()
    for selection in selections:
        chunk = selection["chunk"]
        item_attempts: List[Dict[str, Any]] = []
        for run_attempt in range(1, attempts_per_item + 1):
            record = _generate_one(
                model_client,
                config,
                parent_by_id[chunk["parent_document_id"]],
                chunk,
                run_attempt,
                _retry_delay(config, run_attempt),
            )
            attempts.append(record)
            item_attempts.append(record)
            if record["status"] == "success":
                break
        latest = item_attempts[-1]
        results.append(
            {
                "chunk_id": chunk["chunk_id"],
                "parent_document_id": chunk["parent_document_id"],
                "selection_reason": selection["selection_reason"],
                "parent_characters": len(
                    parent_by_id[chunk["parent_document_id"]]["content"]
                ),
                "chunk_characters": len(chunk["content"]),
                "parent_content_hash": chunk["parent_content_hash"],
                "chunk_content_hash": chunk["chunk_content_hash"],
                "status": latest["status"],
                "attempts": len(item_attempts),
                "context": latest.get("context"),
                "context_hash": latest.get("context_hash"),
                "finish_reason": latest.get("finish_reason"),
                "usage": latest.get("usage"),
                "input_characters": latest.get("input_characters"),
                "error": latest.get("error"),
            }
        )
    error_results = [result for result in results if result["status"] != "success"]
    return write_artifact(
        output_path,
        {
            "schema_version": CONTEXT_PROBE_SCHEMA,
            "stage": "context_model_probe",
            "status": "valid" if not error_results else "insufficient",
            "created_at": datetime.now(timezone.utc).isoformat(),
            "parent_manifest_digest": parents["artifact_digest"],
            "chunk_manifest_digest": chunks["artifact_digest"],
            "config": _public_config(config),
            "selection": {
                "method": "deterministic_structural_diversity_v1",
                "requested_count": count,
                "selected_count": len(results),
            },
            "attempts_per_item": attempts_per_item,
            "attempt_records": len(attempts),
            "failed_attempts": sum(
                attempt["status"] != "success" for attempt in attempts
            ),
            "error_items": len(error_results),
            "elapsed_ms": round((time.monotonic() - started) * 1000, 3),
            "usage_totals": _usage_totals(attempts),
            "attempt_log": attempts,
            "results": results,
        },
    )


def summarize_context_cache(
    cache_path: str,
    chunks_path: str,
    output_path: Optional[str] = None,
) -> Dict[str, Any]:
    """Validate a cache and produce a secret-free completeness summary."""
    chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
    header, attempts = _read_cache(cache_path)
    if header is None:
        raise ValueError("context cache does not exist")
    if header.get("chunk_manifest_digest") != chunks.get("artifact_digest"):
        raise ValueError("context cache and chunk manifest do not match")
    if header.get("parent_manifest_digest") != chunks.get(
        "parent_manifest_digest"
    ):
        raise ValueError("context cache and parent manifest do not match")
    if header.get("schema_version") != CONTEXT_CACHE_SCHEMA:
        raise ValueError("context cache schema is unsupported")
    if not isinstance(header.get("cache_identity"), str) or not header.get(
        "cache_identity"
    ):
        raise ValueError("context cache identity is missing")
    latest, _ = _latest_attempts(attempts)
    chunk_records = list(chunks.get("chunks") or [])
    expected_by_id = {
        str(record.get("chunk_id") or ""): record for record in chunk_records
    }
    if (
        len(expected_by_id) != len(chunk_records)
        or "" in expected_by_id
        or len(chunk_records) != chunks.get("chunks_count")
    ):
        raise ValueError("chunk manifest contains invalid or duplicate chunk IDs")
    unknown_chunks = sorted(set(latest) - set(expected_by_id))
    if unknown_chunks:
        raise ValueError(
            "context cache contains unknown chunk IDs: "
            + ", ".join(unknown_chunks[:10])
        )

    successful: set[str] = set()
    errors: List[str] = []
    missing: List[str] = []
    latest_successes: List[Dict[str, Any]] = []
    context_set: List[Dict[str, str]] = []
    for chunk in chunk_records:
        chunk_id = str(chunk["chunk_id"])
        record = latest.get(chunk_id)
        if record is None:
            missing.append(chunk_id)
            continue
        status = record.get("status")
        if status == "error":
            errors.append(chunk_id)
            continue
        if status != "success":
            raise ValueError(
                f"context cache chunk {chunk_id} has invalid status {status!r}"
            )
        context = record.get("context")
        if not isinstance(context, str) or not context.strip():
            raise ValueError(
                f"successful context is empty for chunk {chunk_id}"
            )
        for field in (
            "parent_document_id",
            "parent_content_hash",
            "chunk_content_hash",
        ):
            if record.get(field) != chunk.get(field):
                raise ValueError(
                    f"context {field} does not match chunk {chunk_id}"
                )
        finish_reason = str(record.get("finish_reason") or "").strip().lower()
        if finish_reason not in NORMAL_FINISH_REASONS:
            raise ValueError(
                f"context finish_reason is not a normal stop for chunk {chunk_id}"
            )
        expected_context_hash = text_digest(context.strip())
        if record.get("context_hash") != expected_context_hash:
            raise ValueError(
                f"context hash does not match text for chunk {chunk_id}"
            )
        successful.add(chunk_id)
        latest_successes.append(record)
        context_set.append(
            {
                "chunk_id": chunk_id,
                "context_hash": expected_context_hash,
            }
        )

    complete = len(successful) == len(chunk_records)
    usage_totals = _usage_totals(attempts)
    payload = {
        "schema_version": CONTEXT_SUMMARY_SCHEMA,
        "status": "valid" if complete else "insufficient",
        "cache_identity": header.get("cache_identity"),
        "context_set_digest": (
            canonical_digest(context_set) if complete else None
        ),
        "chunk_manifest_digest": chunks["artifact_digest"],
        "expected_chunks": len(chunk_records),
        "successful_chunks": len(successful),
        "error_chunks": len(errors),
        "missing_chunks": len(missing),
        "attempt_records": len(attempts),
        "successful_elapsed_ms": round(
            sum(float(record.get("elapsed_ms") or 0) for record in latest_successes),
            3,
        ),
        "usage_totals": usage_totals,
        "latest_success_usage_totals": _usage_totals(latest_successes),
        "input_character_totals": _input_character_totals(attempts),
        "latest_success_input_character_totals": _input_character_totals(
            latest_successes
        ),
        "finish_reason_counts": _finish_reason_counts(attempts),
        "latest_success_finish_reason_counts": _finish_reason_counts(
            latest_successes
        ),
        "error_chunk_ids": errors,
        "missing_chunk_ids": missing,
        "config": header.get("config"),
    }
    if output_path:
        return write_artifact(output_path, payload)
    return payload
