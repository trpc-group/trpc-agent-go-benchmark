#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent. All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
"""Tests for the LongMemEval high-similarity memory audit."""

from __future__ import annotations

import csv
import gzip
import tempfile
import unittest
from pathlib import Path

from memory.adapter import longmemeval_memory_audit as audit


def write_snapshot(
    directory: Path,
    label: str,
    memories: list[tuple[str, str, str]],
    pairs: list[tuple[str, str, str, float]],
) -> None:
    """Write one snapshot label in the layout the audit consumes."""
    with gzip.open(
        directory / f"{label}_memories.csv.gz", "wt", newline=""
    ) as handle:
        writer = csv.writer(handle)
        writer.writerow(["memory_id", "user_id", "memory_content"])
        writer.writerows(memories)
    with gzip.open(
        directory / f"{label}_similarity_ge090.csv.gz", "wt", newline=""
    ) as handle:
        writer = csv.writer(handle)
        writer.writerow(
            ["user_id", "memory_id_a", "memory_id_b", "cosine_similarity"]
        )
        writer.writerows(pairs)


class ClassifyTest(unittest.TestCase):
    def test_identical_normalized_text_is_exact(self) -> None:
        self.assertEqual(
            audit.classify(
                "Alice moved to Berlin in 2021.",
                "alice moved to berlin in 2021",
            ),
            "exact",
        )

    def test_differing_number_is_critical_mismatch(self) -> None:
        self.assertEqual(
            audit.classify(
                "The user paid 300 dollars for the bike repair.",
                "The user paid 500 dollars for the bike repair.",
            ),
            "critical_mismatch",
        )

    def test_differing_negation_is_critical_mismatch(self) -> None:
        self.assertEqual(
            audit.classify(
                "The user drinks coffee after dinner.",
                "The user does not drink coffee after dinner.",
            ),
            "critical_mismatch",
        )

    def test_high_lexical_overlap_is_near_duplicate(self) -> None:
        self.assertEqual(
            audit.classify(
                "The user runs five kilometers every morning.",
                "The user runs five kilometers each morning.",
            ),
            "strict_near_duplicate",
        )

    def test_one_sided_extra_detail_is_containment(self) -> None:
        self.assertEqual(
            audit.classify(
                "The user adopted a cat.",
                "The user adopted a cat named Luna.",
            ),
            "directional_containment",
        )

    def test_unrelated_text_is_vector_only(self) -> None:
        self.assertEqual(
            audit.classify(
                "The user booked a flight to Osaka.",
                "The user prefers window seats on trains.",
            ),
            "vector_only",
        )


class SimilarityBandTest(unittest.TestCase):
    def test_band_boundaries(self) -> None:
        cases = {
            1.0: "0.98-1.00",
            0.98: "0.98-1.00",
            0.9799: "0.95-0.98",
            0.95: "0.95-0.98",
            0.9499: "0.94-0.95",
            0.92: "0.92-0.94",
            0.90: "0.90-0.92",
        }
        for similarity, band in cases.items():
            with self.subTest(similarity=similarity):
                self.assertEqual(audit.similarity_band(similarity), band)

    def test_similarity_below_threshold_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            audit.similarity_band(0.8999)


class AuditRunTest(unittest.TestCase):
    def make_run(self, reported: int) -> audit.Run:
        return audit.Run(
            key="demo",
            label="Demo",
            assistant_extraction=False,
            sources=(
                audit.Source(
                    label="primary",
                    table="demo_primary",
                    excluded_cases=("case-b",),
                ),
                audit.Source(
                    label="rebuild",
                    table="demo_rebuild",
                    role="case_local_rebuild",
                ),
            ),
            reported_memories=reported,
            result_dir="demo_run",
            scenario="auto-strict",
        )

    def build_snapshot(self, directory: Path) -> None:
        write_snapshot(
            directory,
            "primary",
            [
                ("m1", "case-a", "The user paid 300 dollars for the repair."),
                ("m2", "case-a", "The user paid 500 dollars for the repair."),
                ("m3", "case-b", "Partial row from the replaced case."),
            ],
            [
                ("case-a", "m1", "m2", 0.991),
                ("case-b", "m1", "m3", 0.955),
            ],
        )
        write_snapshot(
            directory,
            "rebuild",
            [
                ("r1", "case-b", "The user adopted a cat."),
                ("r2", "case-b", "The user adopted a cat named Luna."),
            ],
            [("case-b", "r1", "r2", 0.962)],
        )

    def test_population_excludes_replaced_case_and_counts_pairs(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            self.build_snapshot(directory)
            result = audit.audit_run(directory, self.make_run(4))
        self.assertEqual(result["memories"], 4)
        self.assertEqual(result["cases"], 2)
        self.assertEqual(result["pairs"], 2)
        top_band = next(
            band for band in result["bands"] if band["band"] == "0.98-1.00"
        )
        self.assertEqual(top_band["critical_mismatch"], 1)
        mid_band = next(
            band for band in result["bands"] if band["band"] == "0.95-0.98"
        )
        self.assertEqual(mid_band["directional_containment"], 1)
        self.assertEqual(result["total"]["duplicate_like"], 1)

    def test_population_mismatch_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            self.build_snapshot(directory)
            with self.assertRaises(ValueError):
                audit.audit_run(directory, self.make_run(5))


if __name__ == "__main__":
    unittest.main()
