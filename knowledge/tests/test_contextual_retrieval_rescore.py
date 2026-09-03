#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for deterministic offline retrieval rescoring."""

import copy
import tempfile
import unittest
from pathlib import Path

from contextual_retrieval import RETRIEVAL_EVIDENCE_SCOPE
from contextual_retrieval.artifacts import write_artifact
from contextual_retrieval.dataset import CASE_SCHEMA
from contextual_retrieval.metrics import score_ranking
from contextual_retrieval.rescore import (
    RETRIEVAL_RESCORE_SCHEMA,
    rescore_retrieval_samples,
)
from contextual_retrieval.runner import (
    RETRIEVAL_MANIFEST_SCHEMA,
    RETRIEVAL_SAMPLES_SCHEMA,
)


class ContextualRetrievalRescoreTest(unittest.TestCase):
    def _artifacts(self, root):
        question_types = (
            "comparison_query",
            "inference_query",
            "temporal_query",
        )
        cases = write_artifact(
            str(root / "cases.json"),
            {
                "schema_version": CASE_SCHEMA,
                "chunk_manifest_digest": "chunks-digest",
                "cases_count": 3,
                "cases": [
                    {
                        "case_id": f"case-{index}",
                        "dataset_index": index,
                        "question_type": question_type,
                        "evidence": [
                            {
                                "evidence_id": evidence_id,
                                "parent_document_id": "parent-1",
                                "chunk_ids": ["shared-chunk"],
                            }
                            for evidence_id in ("e1", "e2", "e3")
                        ],
                    }
                    for index, question_type in enumerate(question_types)
                ],
            },
        )
        manifest = write_artifact(
            str(root / "formal.manifest.json"),
            {
                "schema_version": RETRIEVAL_MANIFEST_SCHEMA,
                "run_identity": "run-identity",
                "evidence_scope": RETRIEVAL_EVIDENCE_SCOPE,
                "case_manifest_digest": cases["artifact_digest"],
                "expected_cases": 3,
                "selection": {"limit": None, "smoke_per_type": None},
                "bootstrap": {
                    "method": "paired_percentile",
                    "resamples": 20,
                    "seed": 7,
                },
            },
        )
        ranking = [
            {
                "chunk_id": "shared-chunk",
                "parent_document_id": "parent-1",
            }
        ]
        samples = []
        for index, question_type in enumerate(question_types):
            case = cases["cases"][index]
            old_metrics = dict(score_ranking(case, ranking)["metrics"])
            old_metrics["ndcg_at_20"] = 1.4078361780682693
            samples.append(
                {
                    "case_id": case["case_id"],
                    "question_type": question_type,
                    "baseline": {
                        "status": "success",
                        "attempts": [{"status": "success", "elapsed_ms": 1}],
                        "ranking": ranking,
                        "metrics": old_metrics,
                    },
                    "contextual": {
                        "status": "success",
                        "attempts": [{"status": "success", "elapsed_ms": 1}],
                        "ranking": ranking,
                        "metrics": old_metrics,
                    },
                }
            )
        samples_artifact = write_artifact(
            str(root / "formal.samples.json"),
            {
                "schema_version": RETRIEVAL_SAMPLES_SCHEMA,
                "run_identity": "run-identity",
                "manifest_digest": manifest["artifact_digest"],
                "expected_cases": 3,
                "completed_cases": 3,
                "samples": samples,
            },
        )
        return cases, manifest, samples_artifact

    def test_rescores_frozen_rankings_with_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cases, manifest, samples = self._artifacts(root)

            result = rescore_retrieval_samples(
                str(root / "cases.json"),
                str(root / "formal.manifest.json"),
                str(root / "formal.samples.json"),
                str(root / "formal.rescore.json"),
            )

        self.assertEqual(RETRIEVAL_RESCORE_SCHEMA, result["schema_version"])
        self.assertEqual(manifest["artifact_digest"], result["source_manifest_digest"])
        self.assertEqual(samples["artifact_digest"], result["source_samples_digest"])
        self.assertEqual(
            cases["artifact_digest"],
            result["rescored_case_manifest_digest"],
        )
        self.assertFalse(result["case_manifest_changed"])
        self.assertEqual(3, result["paired_valid_cases"])
        self.assertEqual(0, result["failed_request_attempts"])
        self.assertEqual("valid", result["evidence_status"])
        self.assertTrue(result["formal_ab_eligible"])
        self.assertEqual(
            3,
            result["changed_samples_by_metric"]["baseline"]["ndcg_at_20"],
        )
        self.assertEqual(
            1.0,
            result["aggregates"]["baseline"]["overall"]["metrics"][
                "ndcg_at_20"
            ],
        )

    def test_rejects_unacknowledged_case_manifest_change(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._artifacts(root)
            corrected = write_artifact(
                str(root / "corrected-cases.json"),
                {
                    "schema_version": CASE_SCHEMA,
                    "chunk_manifest_digest": "chunks-digest",
                    "cases_count": 3,
                    "cases": [
                        {
                            "case_id": f"case-{index}",
                            "dataset_index": index,
                            "question_type": question_type,
                            "evidence": [
                                {
                                    "evidence_id": "e1",
                                    "parent_document_id": "parent-1",
                                    "chunk_ids": ["shared-chunk"],
                                }
                            ],
                        }
                        for index, question_type in enumerate(
                            (
                                "comparison_query",
                                "inference_query",
                                "temporal_query",
                            )
                        )
                    ],
                },
            )
            with self.assertRaisesRegex(ValueError, "case manifest differs"):
                rescore_retrieval_samples(
                    str(root / "corrected-cases.json"),
                    str(root / "formal.manifest.json"),
                    str(root / "formal.samples.json"),
                    str(root / "formal.rescore.json"),
                )
            result = rescore_retrieval_samples(
                str(root / "corrected-cases.json"),
                str(root / "formal.manifest.json"),
                str(root / "formal.samples.json"),
                str(root / "formal.rescore.json"),
                allow_case_manifest_change=True,
            )

        self.assertTrue(result["case_manifest_changed"])
        self.assertEqual("valid", result["evidence_status"])
        self.assertTrue(result["formal_ab_eligible"])
        self.assertEqual(
            corrected["artifact_digest"],
            result["rescored_case_manifest_digest"],
        )

    def test_failed_source_attempt_keeps_formal_evidence_insufficient(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, _, samples = self._artifacts(root)
            retried = copy.deepcopy(samples)
            retried.pop("artifact_digest")
            retried["samples"][0]["baseline"]["attempts"] = [
                {"status": "request_error", "elapsed_ms": 1},
                {"status": "success", "elapsed_ms": 1},
            ]
            write_artifact(str(root / "retried.samples.json"), retried)

            result = rescore_retrieval_samples(
                str(root / "cases.json"),
                str(root / "formal.manifest.json"),
                str(root / "retried.samples.json"),
                str(root / "formal.rescore.json"),
            )

        self.assertEqual(1, result["failed_request_attempts"])
        self.assertEqual("insufficient", result["evidence_status"])
        self.assertFalse(result["formal_ab_eligible"])
        self.assertEqual("insufficient", result["gate"]["decision"])

    def test_failed_source_attempt_keeps_smoke_promotion_insufficient(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, manifest, samples = self._artifacts(root)
            smoke_manifest = copy.deepcopy(manifest)
            smoke_manifest.pop("artifact_digest")
            smoke_manifest["selection"] = {
                "limit": None,
                "smoke_per_type": 1,
            }
            smoke_manifest = write_artifact(
                str(root / "smoke.manifest.json"),
                smoke_manifest,
            )
            retried = copy.deepcopy(samples)
            retried.pop("artifact_digest")
            retried["manifest_digest"] = smoke_manifest["artifact_digest"]
            retried["samples"][0]["baseline"]["attempts"] = [
                {"status": "request_error", "elapsed_ms": 1},
                {"status": "success", "elapsed_ms": 1},
            ]
            write_artifact(str(root / "smoke.samples.json"), retried)

            result = rescore_retrieval_samples(
                str(root / "cases.json"),
                str(root / "smoke.manifest.json"),
                str(root / "smoke.samples.json"),
                str(root / "smoke.rescore.json"),
            )

        self.assertEqual(1, result["failed_request_attempts"])
        self.assertEqual("insufficient", result["evidence_status"])
        self.assertEqual(
            "insufficient",
            result["smoke_promotion"]["decision"],
        )
        self.assertFalse(
            result["smoke_promotion"]["checks"][
                "zero_failed_request_attempts"
            ]
        )


if __name__ == "__main__":
    unittest.main()
