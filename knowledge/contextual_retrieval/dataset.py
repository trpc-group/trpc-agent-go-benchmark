#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Evidence-aware MultiHop-RAG preparation for retrieval-only evaluation."""

from __future__ import annotations

import html
import json
import os
import re
import unicodedata
from collections import defaultdict
from typing import Any, Dict, Iterable, List, Sequence, Tuple

from contextual_retrieval.artifacts import (
    file_digest,
    load_artifact,
    text_digest,
    write_artifact,
)


PARENT_SCHEMA = "contextual-retrieval/parents/v1"
QUERY_SCHEMA = "contextual-retrieval/queries/v1"
CHUNK_SCHEMA = "contextual-retrieval/chunks/v1"
CASE_SCHEMA = "contextual-retrieval/cases/v1"
PREFLIGHT_SCHEMA = "contextual-retrieval/preflight/v1"

DEFAULT_QUESTION_TYPES = (
    "comparison_query",
    "inference_query",
    "temporal_query",
)


def _load_json_list(path: str, label: str) -> List[Dict[str, Any]]:
    with open(path, "r", encoding="utf-8") as handle:
        payload = json.load(handle)
    if not isinstance(payload, list):
        raise ValueError(f"{label} must be a JSON list: {path}")
    result: List[Dict[str, Any]] = []
    for index, record in enumerate(payload):
        if not isinstance(record, dict):
            raise ValueError(f"{label}[{index}] must be an object")
        result.append(record)
    return result


def _stable_id(prefix: str, *parts: Any) -> str:
    payload = "\x00".join(str(part) for part in parts)
    return f"{prefix}-{text_digest(payload)}"


def _required_text(record: Dict[str, Any], field: str, label: str) -> str:
    value = record.get(field)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{label}.{field} must be a non-empty string")
    return value


def _safe_title(value: str) -> str:
    return re.sub(r"[^\w]", "_", value[:80])


def _parent_content(article: Dict[str, Any]) -> str:
    title = str(article.get("title") or "")
    source = str(article.get("source") or "")
    published_at = str(article.get("published_at") or "")
    body = str(article.get("body") or "")
    return (
        f"Title: {title}\n"
        f"Source: {source}\n"
        f"Published: {published_at}\n\n"
        f"{body}"
    )


def _document_metadata(record: Dict[str, Any]) -> Dict[str, Any]:
    return {
        field: record.get(field)
        for field in (
            "title",
            "source",
            "url",
            "author",
            "category",
            "published_at",
        )
    }


def _normalized_text(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", html.unescape(value))
    normalized = normalized.replace("\u2018", "'").replace("\u2019", "'")
    normalized = normalized.replace("\u201c", '"').replace("\u201d", '"')
    normalized = normalized.replace("\u00a0", " ")
    return " ".join(normalized.split()).casefold()


def prepare_dataset(
    corpus_path: str,
    questions_path: str,
    parents_output: str,
    queries_output: str,
    preflight_output: str,
    per_type_limit: int = 150,
    question_types: Sequence[str] = DEFAULT_QUESTION_TYPES,
) -> Tuple[Dict[str, Any], Dict[str, Any], Dict[str, Any]]:
    """Prepare stable parent and query manifests from canonical raw files."""
    if per_type_limit <= 0:
        raise ValueError("per_type_limit must be positive")
    corpus = _load_json_list(corpus_path, "corpus")
    questions = _load_json_list(questions_path, "questions")

    parents: List[Dict[str, Any]] = []
    parent_by_url: Dict[str, Dict[str, Any]] = {}
    duplicate_urls: List[str] = []
    for index, article in enumerate(corpus):
        url = _required_text(article, "url", f"corpus[{index}]").strip()
        if url in parent_by_url:
            duplicate_urls.append(url)
            continue
        title = str(article.get("title") or "")
        content = _parent_content(article)
        parent_id = _stable_id("mhrag-parent", url)
        parent = {
            "parent_document_id": parent_id,
            "corpus_index": index,
            "file_name": f"mhrag_{index:06d}_{_safe_title(title)}.txt",
            "content": content,
            "content_hash": text_digest(content),
            "metadata": _document_metadata(article),
        }
        parents.append(parent)
        parent_by_url[url] = parent

    grouped: Dict[str, List[Tuple[int, Dict[str, Any]]]] = defaultdict(list)
    for dataset_index, record in enumerate(questions):
        question_type = str(record.get("question_type") or "")
        if question_type in question_types:
            grouped[question_type].append((dataset_index, record))

    cases: List[Dict[str, Any]] = []
    unmapped_parents: List[Dict[str, Any]] = []
    empty_evidence_cases: List[str] = []
    selected_by_type: Dict[str, int] = {}
    for question_type in sorted(question_types):
        selected = grouped.get(question_type, [])[:per_type_limit]
        selected_by_type[question_type] = len(selected)
        for dataset_index, record in selected:
            question = _required_text(
                record,
                "query",
                f"questions[{dataset_index}]",
            )
            answer = _required_text(
                record,
                "answer",
                f"questions[{dataset_index}]",
            )
            case_id = _stable_id(
                "mhrag-query",
                dataset_index,
                question_type,
                question,
                answer,
            )
            raw_evidence = record.get("evidence_list")
            if not isinstance(raw_evidence, list) or not raw_evidence:
                empty_evidence_cases.append(case_id)
                raw_evidence = []
            evidence_records: List[Dict[str, Any]] = []
            for evidence_index, evidence in enumerate(raw_evidence):
                if not isinstance(evidence, dict):
                    raise ValueError(
                        f"questions[{dataset_index}].evidence_list"
                        f"[{evidence_index}] must be an object"
                    )
                fact = _required_text(
                    evidence,
                    "fact",
                    f"questions[{dataset_index}].evidence_list"
                    f"[{evidence_index}]",
                )
                url = _required_text(
                    evidence,
                    "url",
                    f"questions[{dataset_index}].evidence_list"
                    f"[{evidence_index}]",
                ).strip()
                parent = parent_by_url.get(url)
                parent_id = (
                    parent.get("parent_document_id") if parent else None
                )
                evidence_id = _stable_id(
                    "mhrag-evidence",
                    case_id,
                    evidence_index,
                    url,
                    fact,
                )
                if not parent_id:
                    unmapped_parents.append(
                        {
                            "case_id": case_id,
                            "evidence_id": evidence_id,
                            "url": url,
                        }
                    )
                evidence_records.append(
                    {
                        "evidence_id": evidence_id,
                        "evidence_index": evidence_index,
                        "fact": fact,
                        "parent_document_id": parent_id,
                        "metadata": _document_metadata(evidence),
                    }
                )
            cases.append(
                {
                    "case_id": case_id,
                    "dataset_index": dataset_index,
                    "question": question,
                    "answer": answer,
                    "question_type": question_type,
                    "evidence": evidence_records,
                }
            )

    expected_cases = per_type_limit * len(question_types)
    status = "valid"
    reasons: List[str] = []
    if duplicate_urls:
        status = "insufficient"
        reasons.append(f"duplicate_corpus_urls:{len(duplicate_urls)}")
    if len(cases) != expected_cases:
        status = "insufficient"
        reasons.append(f"selected_case_count:{len(cases)}/{expected_cases}")
    if empty_evidence_cases:
        status = "insufficient"
        reasons.append(f"empty_evidence_cases:{len(empty_evidence_cases)}")
    if unmapped_parents:
        status = "insufficient"
        reasons.append(f"unmapped_evidence_parents:{len(unmapped_parents)}")

    source_files = {
        "corpus": {
            "name": os.path.basename(corpus_path),
            "sha256": file_digest(corpus_path),
            "records": len(corpus),
        },
        "questions": {
            "name": os.path.basename(questions_path),
            "sha256": file_digest(questions_path),
            "records": len(questions),
        },
    }
    parent_manifest = write_artifact(
        parents_output,
        {
            "schema_version": PARENT_SCHEMA,
            "dataset": "multihop-rag",
            "source_files": source_files,
            "parents_count": len(parents),
            "parents": parents,
        },
    )
    query_manifest = write_artifact(
        queries_output,
        {
            "schema_version": QUERY_SCHEMA,
            "dataset": "multihop-rag",
            "source_files": source_files,
            "parent_manifest_digest": parent_manifest["artifact_digest"],
            "selection": {
                "question_types": list(question_types),
                "per_type_limit": per_type_limit,
                "selected_by_type": selected_by_type,
            },
            "cases_count": len(cases),
            "cases": cases,
        },
    )
    preflight = write_artifact(
        preflight_output,
        {
            "schema_version": PREFLIGHT_SCHEMA,
            "stage": "dataset",
            "dataset": "multihop-rag",
            "status": status,
            "reasons": reasons,
            "parent_manifest_digest": parent_manifest["artifact_digest"],
            "query_manifest_digest": query_manifest["artifact_digest"],
            "corpus_records": len(corpus),
            "unique_parents": len(parents),
            "selected_cases": len(cases),
            "selected_by_type": selected_by_type,
            "evidence_count": sum(len(case["evidence"]) for case in cases),
            "duplicate_urls": duplicate_urls,
            "empty_evidence_case_ids": empty_evidence_cases,
            "unmapped_evidence_parents": unmapped_parents,
        },
    )
    return parent_manifest, query_manifest, preflight


def map_evidence_to_chunks(
    queries_path: str,
    chunks_path: str,
    cases_output: str,
    preflight_output: str,
) -> Tuple[Dict[str, Any], Dict[str, Any]]:
    """Map every gold evidence fact to stable chunk IDs."""
    queries = load_artifact(queries_path, QUERY_SCHEMA)
    chunks = load_artifact(chunks_path, CHUNK_SCHEMA)
    if (
        chunks.get("parent_manifest_digest")
        != queries.get("parent_manifest_digest")
    ):
        raise ValueError("query and chunk manifests use different parents")

    chunks_by_parent: Dict[str, List[Dict[str, Any]]] = defaultdict(list)
    for index, chunk in enumerate(chunks.get("chunks") or []):
        if not isinstance(chunk, dict):
            raise ValueError(f"chunks[{index}] must be an object")
        parent_id = chunk.get("parent_document_id")
        chunk_id = chunk.get("chunk_id")
        content = chunk.get("content")
        if not all(
            isinstance(value, str) and value
            for value in (parent_id, chunk_id, content)
        ):
            raise ValueError(f"chunks[{index}] has invalid identity/content")
        content_start = chunk.get("content_start")
        content_end = chunk.get("content_end")
        if (
            not isinstance(content_start, int)
            or not isinstance(content_end, int)
            or content_start < 0
            or content_end <= content_start
            or len(content) != content_end - content_start
        ):
            raise ValueError(f"chunks[{index}] has an invalid content interval")
        chunks_by_parent[parent_id].append(dict(chunk))

    normalized_parent_text: Dict[str, str] = {}
    normalized_intervals: Dict[str, List[Tuple[str, int, int]]] = {}
    chunk_size = chunks.get("chunk_size")
    if not isinstance(chunk_size, int) or chunk_size <= 0:
        raise ValueError("chunk manifest has an invalid chunk size")
    for parent_id, parent_chunks in chunks_by_parent.items():
        parent_chunks.sort(key=lambda item: item["chunk_index"])
        pieces: List[str] = []
        expected_index = 1
        for chunk in parent_chunks:
            if chunk.get("chunk_index") != expected_index:
                raise ValueError(
                    f"parent {parent_id} has non-contiguous chunk indices"
                )
            base_start = (expected_index - 1) * chunk_size
            overlap_length = base_start - chunk["content_start"]
            if overlap_length < 0 or overlap_length >= len(chunk["content"]):
                raise ValueError(
                    f"parent {parent_id} chunk {expected_index} has invalid overlap"
                )
            pieces.append(chunk["content"][overlap_length:])
            expected_index += 1
        reconstructed = "".join(pieces)
        if not reconstructed:
            raise ValueError(f"parent {parent_id} reconstructs to empty content")
        if parent_chunks[-1]["content_end"] != len(reconstructed):
            raise ValueError(
                f"parent {parent_id} reconstructed length does not match chunks"
            )
        normalized = _normalized_text(reconstructed)
        normalized_parent_text[parent_id] = normalized
        intervals: List[Tuple[str, int, int]] = []
        for chunk in parent_chunks:
            normalized_start = len(
                _normalized_text(reconstructed[: chunk["content_start"]])
            )
            normalized_end = len(
                _normalized_text(reconstructed[: chunk["content_end"]])
            )
            intervals.append(
                (chunk["chunk_id"], normalized_start, normalized_end)
            )
        normalized_intervals[parent_id] = intervals

    mapped_cases: List[Dict[str, Any]] = []
    unmapped_evidence: List[Dict[str, Any]] = []
    mapped_evidence_count = 0
    ambiguous_evidence_count = 0
    for case in queries.get("cases") or []:
        mapped_case = dict(case)
        mapped_records: List[Dict[str, Any]] = []
        for evidence in case.get("evidence") or []:
            mapped = dict(evidence)
            normalized_fact = _normalized_text(str(evidence.get("fact") or ""))
            parent_id = str(evidence.get("parent_document_id") or "")
            normalized_parent = normalized_parent_text.get(parent_id, "")
            occurrence_starts: List[int] = []
            search_start = 0
            while normalized_fact:
                occurrence = normalized_parent.find(normalized_fact, search_start)
                if occurrence < 0:
                    break
                occurrence_starts.append(occurrence)
                search_start = occurrence + 1
            occurrence_intervals = [
                [start, start + len(normalized_fact)]
                for start in occurrence_starts
            ]
            matching_chunks = [
                chunk_id
                for chunk_id, chunk_start, chunk_end in normalized_intervals.get(
                    parent_id,
                    [],
                )
                if any(
                    chunk_start < evidence_end
                    and chunk_end + 1 > evidence_start
                    for evidence_start, evidence_end in occurrence_intervals
                )
            ]
            mapped["chunk_ids"] = matching_chunks
            mapped["mapping"] = {
                "strategy": "normalized_exact_parent_span_v1",
                "normalized_occurrences": occurrence_intervals,
            }
            if matching_chunks:
                mapped_evidence_count += 1
                if len(occurrence_intervals) > 1:
                    ambiguous_evidence_count += 1
            else:
                unmapped_evidence.append(
                    {
                        "case_id": case.get("case_id"),
                        "evidence_id": evidence.get("evidence_id"),
                        "parent_document_id": parent_id,
                        "fact": evidence.get("fact"),
                    }
                )
            mapped_records.append(mapped)
        mapped_case["evidence"] = mapped_records
        mapped_cases.append(mapped_case)

    total_evidence = sum(
        len(case.get("evidence") or []) for case in mapped_cases
    )
    status = "valid" if not unmapped_evidence else "insufficient"
    reasons = []
    if unmapped_evidence:
        reasons.append(f"unmapped_evidence_chunks:{len(unmapped_evidence)}")
    case_manifest = write_artifact(
        cases_output,
        {
            "schema_version": CASE_SCHEMA,
            "dataset": "multihop-rag",
            "parent_manifest_digest": queries["parent_manifest_digest"],
            "query_manifest_digest": queries["artifact_digest"],
            "chunk_manifest_digest": chunks["artifact_digest"],
            "selection": queries.get("selection"),
            "cases_count": len(mapped_cases),
            "evidence_count": total_evidence,
            "cases": mapped_cases,
        },
    )
    preflight = write_artifact(
        preflight_output,
        {
            "schema_version": PREFLIGHT_SCHEMA,
            "stage": "evidence_mapping",
            "dataset": "multihop-rag",
            "status": status,
            "reasons": reasons,
            "query_manifest_digest": queries["artifact_digest"],
            "chunk_manifest_digest": chunks["artifact_digest"],
            "case_manifest_digest": case_manifest["artifact_digest"],
            "cases_count": len(mapped_cases),
            "evidence_count": total_evidence,
            "mapped_evidence_count": mapped_evidence_count,
            "ambiguous_evidence_count": ambiguous_evidence_count,
            "unmapped_evidence_count": len(unmapped_evidence),
            "unmapped_evidence": unmapped_evidence,
        },
    )
    return case_manifest, preflight


def iter_parent_ids(cases: Iterable[Dict[str, Any]]) -> Iterable[str]:
    """Yield distinct parent IDs in first-observed order."""
    seen = set()
    for case in cases:
        for evidence in case.get("evidence") or []:
            parent_id = evidence.get("parent_document_id")
            if parent_id and parent_id not in seen:
                seen.add(parent_id)
                yield parent_id
