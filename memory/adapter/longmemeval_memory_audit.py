#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent. All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
"""Classify high-similarity memory pairs for the reported LongMemEval runs.

The audit consumes a read-only PGVector snapshot of the memory tables that
produced the report and classifies every same-case memory pair whose cosine
similarity is at least 0.90. Classes are deterministic text predicates, not
semantic judgements, and the script never sends memory text anywhere.

Snapshot layout, one pair of files per source label:

    <label>_memories.csv.gz          memory_id,user_id,memory_content
    <label>_similarity_ge090.csv.gz  user_id,memory_id_a,memory_id_b,cosine

Both files are produced by the queries in ``MEMORY_QUERY`` and ``PAIR_QUERY``
against the tables listed in ``RUNS``. The snapshot itself is not part of the
repository because it contains dataset-derived text; ``provenance.json``
records the SHA-256 digest of every consumed file so a regenerated snapshot
can be compared against the one behind the published aggregates.
"""

from __future__ import annotations

import argparse
import csv
import difflib
import gzip
import hashlib
import json
import math
import re
import sys
from collections import Counter, defaultdict
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterator

SIMILARITY_THRESHOLD = 0.90

MEMORY_QUERY = (
    "SELECT memory_id, user_id, memory_content FROM {table} "
    "WHERE deleted_at IS NULL ORDER BY user_id, memory_id"
)

PAIR_QUERY = (
    "SELECT a.user_id, a.memory_id, b.memory_id, "
    "1 - (a.embedding <=> b.embedding) "
    "FROM {table} a JOIN {table} b "
    "ON a.app_name = b.app_name AND a.user_id = b.user_id "
    "AND a.memory_id < b.memory_id "
    "WHERE a.deleted_at IS NULL AND b.deleted_at IS NULL "
    "AND a.embedding IS NOT NULL AND b.embedding IS NOT NULL "
    "AND 1 - (a.embedding <=> b.embedding) >= 0.90 "
    "ORDER BY a.user_id, a.memory_id, b.memory_id"
)

SIMILARITY_BANDS = (
    ("0.98-1.00", 0.98, math.inf),
    ("0.95-0.98", 0.95, 0.98),
    ("0.94-0.95", 0.94, 0.95),
    ("0.92-0.94", 0.92, 0.94),
    ("0.90-0.92", 0.90, 0.92),
)

STRUCTURAL_CLASSES = (
    "exact",
    "strict_near_duplicate",
    "directional_containment",
    "critical_mismatch",
    "vector_only",
)

CLASS_DEFINITIONS = {
    "exact": "normalized memory contents are identical",
    "strict_near_duplicate": "token Jaccard >= 0.60 or sequence ratio >= 0.82",
    "directional_containment": (
        "max directional token coverage >= 0.85 and min >= 0.30"
    ),
    "critical_mismatch": (
        "number or negation mismatch with min coverage >= 0.50 "
        "and token Jaccard >= 0.35"
    ),
    "vector_only": "none of the deterministic predicates above",
}

DUPLICATE_LIKE_CLASSES = (
    "exact",
    "strict_near_duplicate",
    "directional_containment",
)

TOKEN_RE = re.compile(r"[a-z0-9]+(?:'[a-z0-9]+)?", re.IGNORECASE)
NUMBER_RE = re.compile(
    r"\b(?:\d+(?:[.,:]\d+)*|zero|one|two|three|four|five|six|seven|"
    r"eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|"
    r"seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|hundred)\b",
    re.IGNORECASE,
)
NEGATION_RE = re.compile(
    r"\b(?:no|not|never|none|without|cannot|can't|didn't|doesn't|"
    r"won't|isn't|wasn't|haven't|hasn't)\b",
    re.IGNORECASE,
)
STOPWORDS = {
    'a', 'an', 'and', 'are', 'as', 'at', 'be', 'been', 'being',
    'but', 'by', 'did', 'do', 'does', 'for', 'from', 'had', 'has',
    'have', 'he', 'her', 'hers', 'him', 'his', 'how', 'i', 'in',
    'into', 'is', 'it', 'its', 'me', 'my', 'of', 'on', 'or', 'our',
    'ours', 'she', 'that', 'the', 'their', 'theirs', 'them', 'they',
    'this', 'to', 'up', 'user', 'was', 'we', 'were', 'what', 'when',
    'where', 'which', 'who', 'with', 'you', 'your', 'yours',
}


@dataclass(frozen=True)
class Source:
    """One memory table contributing to a run population.

    ``role`` is ``primary`` for the table the run built into, and
    ``case_local_rebuild`` for the table holding a case that was rebuilt on
    its own after a transient infrastructure failure. ``excluded_cases``
    lists cases whose rows the report's memory inventory does not count,
    either because the case was rebuilt elsewhere or because its build did
    not complete.
    """

    label: str
    table: str
    role: str = "primary"
    excluded_cases: tuple[str, ...] = ()


@dataclass(frozen=True)
class Run:
    """One reported run and the memory population behind it.

    ``result_dir`` and ``scenario`` locate the run's checkpoint under the
    results tree. When that tree is available the audit copies the run
    manifest version and comparison digests into its provenance, which binds
    the aggregates to the exact run configuration behind the report.
    """

    key: str
    label: str
    assistant_extraction: bool
    sources: tuple[Source, ...]
    reported_memories: int
    result_dir: str
    scenario: str


RUNS = (
    Run(
        key="merge_similar",
        label="Merge Similar",
        assistant_extraction=False,
        sources=(
            Source(
                label="merge_similar",
                table="memory_eval_auto_lme50_pair_auto_compatible_20260717",
            ),
        ),
        reported_memories=2955,
        result_dir="turn_pair_full50_parallel4_20260717",
        scenario="auto-compatible",
    ),
    Run(
        key="preserve_history",
        label="Preserve History",
        assistant_extraction=False,
        sources=(
            Source(
                label="preserve_history",
                table="memory_eval_auto_lme50_pair_auto_strict_20260717",
                excluded_cases=("a1eacc2a",),
            ),
            Source(
                label="preserve_history_recovery",
                table="memory_eval_auto_r_strict_a1_0719",
                role="case_local_rebuild",
            ),
        ),
        reported_memories=15353,
        result_dir="turn_pair_full50_parallel4_20260717",
        scenario="auto-strict",
    ),
    Run(
        key="append_only",
        label="Append Only",
        assistant_extraction=False,
        sources=(
            Source(
                label="append_only",
                table="memory_eval_auto_lme50_pair_auto_add_only_20260717",
                excluded_cases=("a1eacc2a",),
            ),
            Source(
                label="append_only_recovery",
                table="memory_eval_auto_r_add_only_a1_0719",
                role="case_local_rebuild",
            ),
        ),
        reported_memories=16280,
        result_dir="turn_pair_full50_parallel4_20260717",
        scenario="auto-add-only",
    ),
    Run(
        key="merge_similar_assistant",
        label="Merge Similar + assistant",
        assistant_extraction=True,
        sources=(
            Source(
                label="merge_similar_assistant",
                table="memory_lme_assistant_a_compatible_20260731_2035",
            ),
        ),
        reported_memories=6011,
        result_dir="assistant_original_a_full50_20260731_2035",
        scenario="auto-compatible",
    ),
    Run(
        key="preserve_history_assistant",
        label="Preserve History + assistant",
        assistant_extraction=True,
        sources=(
            Source(
                label="preserve_history_assistant",
                table="memory_lme_assistant_a_strict_20260731_2035",
                excluded_cases=("60159905",),
            ),
        ),
        reported_memories=17696,
        result_dir="assistant_original_a_full50_20260731_2035",
        scenario="auto-strict",
    ),
    Run(
        key="append_only_assistant",
        label="Append Only + assistant",
        assistant_extraction=True,
        sources=(
            Source(
                label="append_only_assistant",
                table="memory_lme_assistant_a_add_only_20260731_2035",
                excluded_cases=("58ef2f1c",),
            ),
        ),
        reported_memories=18728,
        result_dir="assistant_original_a_full50_20260731_2035",
        scenario="auto-add-only",
    ),
)


def tokenize(value: str) -> list[str]:
    """Return lowercase content tokens with stopwords removed."""
    return [
        token
        for token in TOKEN_RE.findall(value or "")
        if token.lower() not in STOPWORDS
    ]


def normalize_text(value: str) -> str:
    """Return the token sequence used for exact-duplicate comparison."""
    return " ".join(TOKEN_RE.findall((value or "").lower()))


def directional_coverage(needle: list[str], haystack: list[str]) -> float:
    """Return the share of ``needle`` token types present in ``haystack``."""
    needle_set = {token.lower() for token in needle}
    if not needle_set:
        return 0.0
    haystack_set = {token.lower() for token in haystack}
    return len(needle_set & haystack_set) / len(needle_set)


def token_jaccard(left: list[str], right: list[str]) -> float:
    """Return the Jaccard similarity of two token-type sets."""
    left_set = {token.lower() for token in left}
    right_set = {token.lower() for token in right}
    if not left_set or not right_set:
        return 0.0
    return len(left_set & right_set) / len(left_set | right_set)


def classify(left: str, right: str) -> str:
    """Return the structural class of one memory pair."""
    if normalize_text(left) == normalize_text(right):
        return "exact"
    left_tokens = tokenize(left)
    right_tokens = tokenize(right)
    left_numbers = {value.lower() for value in NUMBER_RE.findall(left or "")}
    right_numbers = {value.lower() for value in NUMBER_RE.findall(right or "")}
    numbers_mismatch = bool(
        left_numbers and right_numbers and left_numbers != right_numbers
    )
    negation_mismatch = bool(NEGATION_RE.search(left or "")) != bool(
        NEGATION_RE.search(right or "")
    )
    coverage_left = directional_coverage(left_tokens, right_tokens)
    coverage_right = directional_coverage(right_tokens, left_tokens)
    coverage_min = min(coverage_left, coverage_right)
    coverage_max = max(coverage_left, coverage_right)
    jaccard = token_jaccard(left_tokens, right_tokens)
    if (
        (numbers_mismatch or negation_mismatch)
        and coverage_min >= 0.50
        and jaccard >= 0.35
    ):
        return "critical_mismatch"
    sequence_ratio = difflib.SequenceMatcher(
        None, normalize_text(left), normalize_text(right)
    ).ratio()
    if jaccard >= 0.60 or sequence_ratio >= 0.82:
        return "strict_near_duplicate"
    if coverage_max >= 0.85 and coverage_min >= 0.30:
        return "directional_containment"
    return "vector_only"


def similarity_band(similarity: float) -> str:
    """Return the band label for a cosine similarity of at least 0.90."""
    for label, lower, upper in SIMILARITY_BANDS:
        if lower <= similarity < upper:
            return label
    raise ValueError(f"similarity {similarity} is outside [0.90, 1.00]")


def file_digest(path: Path) -> dict[str, object]:
    """Return the SHA-256 digest and byte size of one snapshot file."""
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return {
        "file": path.name,
        "sha256": digest.hexdigest(),
        "bytes": path.stat().st_size,
    }


def read_csv_gz(path: Path) -> Iterator[list[str]]:
    """Yield data rows of a gzipped CSV file, skipping its header."""
    with gzip.open(path, "rt", encoding="utf-8", newline="") as handle:
        reader = csv.reader(handle)
        next(reader, None)
        yield from reader


def load_memories(
    snapshot: Path, label: str, excluded: tuple[str, ...]
) -> dict[str, tuple[str, str]]:
    """Return ``memory_id -> (case_id, content)`` for one snapshot label."""
    path = snapshot / f"{label}_memories.csv.gz"
    memories: dict[str, tuple[str, str]] = {}
    for memory_id, case_id, content in read_csv_gz(path):
        if case_id in excluded:
            continue
        memories[memory_id] = (case_id, content)
    return memories


def audit_run(snapshot: Path, run: Run) -> dict[str, object]:
    """Classify every high-similarity pair of one reported run."""
    memories: dict[str, tuple[str, str]] = {}
    counts: Counter[tuple[str, str]] = Counter()
    cases: dict[str, set[str]] = defaultdict(set)
    pairs_read = 0
    for source in run.sources:
        source_memories = load_memories(
            snapshot, source.label, source.excluded_cases
        )
        overlap = memories.keys() & source_memories.keys()
        if overlap:
            raise ValueError(
                f"run {run.key}: memory id reused across sources: "
                f"{sorted(overlap)[:3]}"
            )
        memories.update(source_memories)
        pair_path = snapshot / f"{source.label}_similarity_ge090.csv.gz"
        for case_id, memory_id_a, memory_id_b, similarity in read_csv_gz(
            pair_path
        ):
            if case_id in source.excluded_cases:
                continue
            left = source_memories[memory_id_a][1]
            right = source_memories[memory_id_b][1]
            band = similarity_band(float(similarity))
            counts[(band, classify(left, right))] += 1
            cases[band].add(case_id)
            pairs_read += 1
    if len(memories) != run.reported_memories:
        raise ValueError(
            f"run {run.key}: audited {len(memories)} memories but the report "
            f"states {run.reported_memories}"
        )
    bands = []
    for band, _lower, _upper in SIMILARITY_BANDS:
        population = sum(counts[(band, name)] for name in STRUCTURAL_CLASSES)
        row = {
            "band": band,
            "population_pairs": population,
            "cases": len(cases[band]),
        }
        row.update(
            {name: counts[(band, name)] for name in STRUCTURAL_CLASSES}
        )
        row["duplicate_like"] = sum(
            counts[(band, name)] for name in DUPLICATE_LIKE_CLASSES
        )
        row["duplicate_like_rate"] = (
            round(row["duplicate_like"] / population, 4) if population else 0.0
        )
        bands.append(row)
    total = {
        "band": "0.90-1.00",
        "population_pairs": pairs_read,
        "cases": len({case for group in cases.values() for case in group}),
    }
    total.update(
        {
            name: sum(
                counts[(band, name)] for band, _l, _u in SIMILARITY_BANDS
            )
            for name in STRUCTURAL_CLASSES
        }
    )
    total["duplicate_like"] = sum(
        total[name] for name in DUPLICATE_LIKE_CLASSES
    )
    total["duplicate_like_rate"] = (
        round(total["duplicate_like"] / pairs_read, 4) if pairs_read else 0.0
    )
    return {
        "run": run.key,
        "run_label": run.label,
        "assistant_extraction": run.assistant_extraction,
        "memories": len(memories),
        "cases": len({case for case, _ in memories.values()}),
        "pairs": pairs_read,
        "pairs_per_1k_memories": round(
            pairs_read / len(memories) * 1000, 1
        ),
        "bands": bands,
        "total": total,
    }


def summary_rows(results: list[dict[str, object]]) -> list[dict[str, object]]:
    """Flatten per-run band results into CSV rows."""
    rows = []
    for result in results:
        for band in [*result["bands"], result["total"]]:
            row = {
                "run": result["run"],
                "run_label": result["run_label"],
                "assistant_extraction": (
                    "true" if result["assistant_extraction"] else "false"
                ),
                "memories": result["memories"],
            }
            row.update(band)
            rows.append(row)
    return rows


def write_csv(path: Path, rows: list[dict[str, object]]) -> None:
    """Write aggregate rows as CSV with a stable column order."""
    fields = [
        "run",
        "run_label",
        "assistant_extraction",
        "memories",
        "band",
        "population_pairs",
        "cases",
        *STRUCTURAL_CLASSES,
        "duplicate_like",
        "duplicate_like_rate",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def write_json(path: Path, value: object) -> None:
    """Write one JSON document with a trailing newline."""
    path.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def run_identity(results_root: Path | None, run: Run) -> dict[str, object]:
    """Return the run's manifest version and digests from its checkpoint."""
    identity: dict[str, object] = {
        "result_dir": run.result_dir,
        "scenario": run.scenario,
    }
    if results_root is None:
        return identity
    checkpoint = (
        results_root
        / run.result_dir
        / run.scenario
        / "longmemeval"
        / "auto_pgvector"
        / "checkpoint.json"
    )
    metadata = json.loads(checkpoint.read_text(encoding="utf-8"))["metadata"]
    for field in (
        "timestamp",
        "run_manifest_version",
        "run_compatibility_digest",
        "run_comparison_digest",
    ):
        if field in metadata:
            identity[field] = metadata[field]
    return identity


def build_provenance(
    snapshot: Path,
    results: list[dict[str, object]],
    results_root: Path | None,
) -> dict[str, object]:
    """Return snapshot provenance bound to input digests."""
    runs = []
    inputs = []
    seen: set[str] = set()
    for run, result in zip(RUNS, results):
        sources = []
        for source in run.sources:
            sources.append(
                {
                    "table": source.table,
                    "role": source.role,
                    "snapshot_label": source.label,
                    "excluded_cases": list(source.excluded_cases),
                }
            )
            for suffix in ("_memories.csv.gz", "_similarity_ge090.csv.gz"):
                path = snapshot / f"{source.label}{suffix}"
                if path.name not in seen:
                    seen.add(path.name)
                    inputs.append(file_digest(path))
        runs.append(
            {
                "run": run.key,
                "run_label": run.label,
                "assistant_extraction": run.assistant_extraction,
                "identity": run_identity(results_root, run),
                "reported_memories": run.reported_memories,
                "audited_memories": result["memories"],
                "audited_cases": result["cases"],
                "audited_pairs": result["pairs"],
                "sources": sources,
            }
        )
    return {
        "generated_at": datetime.now(timezone.utc).isoformat(
            timespec="seconds"
        ),
        "similarity_threshold": SIMILARITY_THRESHOLD,
        "pair_scope": "same case, same app, memory_id_a < memory_id_b",
        "queries": {"memories": MEMORY_QUERY, "pairs": PAIR_QUERY},
        "classification": CLASS_DEFINITIONS,
        "interpretation": (
            "deterministic text predicates over the full pair population; "
            "the classes are structural signals, not semantic labels"
        ),
        "snapshot_note": (
            "the snapshot holds dataset-derived memory text and is not "
            "committed; regenerate it with the queries above and compare the "
            "digests below"
        ),
        "runs": runs,
        "inputs": inputs,
    }


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse command-line arguments."""
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--snapshot-dir",
        required=True,
        type=Path,
        help="directory holding the exported memory and pair files",
    )
    parser.add_argument(
        "--output-dir",
        required=True,
        type=Path,
        help="directory receiving the aggregate and provenance files",
    )
    parser.add_argument(
        "--results-dir",
        type=Path,
        default=None,
        help=(
            "optional LongMemEval results tree; when given, each run's "
            "manifest version and digests are copied into the provenance"
        ),
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    """Run the audit and write the aggregate and provenance files."""
    args = parse_args(argv)
    results = [audit_run(args.snapshot_dir, run) for run in RUNS]
    args.output_dir.mkdir(parents=True, exist_ok=True)
    write_csv(
        args.output_dir / "high_similarity_summary.csv",
        summary_rows(results),
    )
    write_json(
        args.output_dir / "high_similarity_summary.json",
        {
            "similarity_threshold": SIMILARITY_THRESHOLD,
            "classification": CLASS_DEFINITIONS,
            "duplicate_like_classes": list(DUPLICATE_LIKE_CLASSES),
            "runs": results,
        },
    )
    write_json(
        args.output_dir / "provenance.json",
        build_provenance(args.snapshot_dir, results, args.results_dir),
    )
    for result in results:
        print(
            f"{result['run_label']}: {result['memories']} memories, "
            f"{result['pairs']} pairs at cosine >= {SIMILARITY_THRESHOLD}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
