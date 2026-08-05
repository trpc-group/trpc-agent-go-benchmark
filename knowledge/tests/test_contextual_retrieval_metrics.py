#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for retrieval-only metrics and paired comparison."""

import unittest

from contextual_retrieval.metrics import (
    evaluate_smoke_promotion,
    paired_comparison,
    score_ranking,
)


class ContextualRetrievalMetricsTest(unittest.TestCase):
    def test_score_ranking_uses_frozen_evidence_chunks(self):
        case = {
            "case_id": "case-1",
            "evidence": [
                {
                    "evidence_id": "e1",
                    "parent_document_id": "p1",
                    "chunk_ids": ["c1", "c2"],
                },
                {
                    "evidence_id": "e2",
                    "parent_document_id": "p2",
                    "chunk_ids": ["c3"],
                },
            ],
        }
        ranking = [
            {"chunk_id": "other", "parent_document_id": "other"},
            {"chunk_id": "c2", "parent_document_id": "p1"},
            {"chunk_id": "c3", "parent_document_id": "p2"},
        ]
        result = score_ranking(case, ranking, cutoffs=(2, 3, 4))

        self.assertEqual(0.5, result["metrics"]["evidence_recall_at_2"])
        self.assertEqual(1.0, result["metrics"]["evidence_recall_at_3"])
        self.assertEqual(1.0, result["metrics"]["all_evidence_recall_at_3"])
        self.assertEqual(0.5, result["metrics"]["mrr"])
        self.assertEqual(["e1"], result["hits"]["at_2"]["evidence_ids"])

    def test_paired_comparison_preserves_case_alignment(self):
        baseline = []
        contextual = []
        for index, question_type in enumerate(
            ("comparison_query", "inference_query", "temporal_query")
        ):
            baseline.append(
                {
                    "case_id": f"case-{index}",
                    "question_type": question_type,
                    "metrics": {"metric": 0.0},
                }
            )
            contextual.append(
                {
                    "case_id": f"case-{index}",
                    "question_type": question_type,
                    "metrics": {"metric": 1.0},
                }
            )

        comparison = paired_comparison(
            baseline,
            contextual,
            resamples=50,
            seed=7,
        )

        self.assertEqual(1.0, comparison["overall"]["metric"]["delta"])
        self.assertEqual(
            [1.0, 1.0],
            comparison["overall"]["metric"]["ci_95"],
        )
        self.assertEqual(3, len(comparison["by_question_type"]))

    def test_smoke_promotion_requires_complete_runtime_and_signal(self):
        metrics = {
            "all_evidence_recall_at_10": {"delta": 0.04},
            "evidence_recall_at_10": {"delta": 0.02},
            "document_recall_at_4": {"delta": 0.0},
            "evidence_recall_at_4": {"delta": 0.0},
        }
        comparison = {
            "overall": metrics,
            "by_question_type": {
                question_type: {
                    "all_evidence_recall_at_10": {"delta": 0.01}
                }
                for question_type in (
                    "comparison_query",
                    "inference_query",
                    "temporal_query",
                )
            },
        }

        promoted = evaluate_smoke_promotion(
            comparison,
            evidence_complete=True,
            runtime_errors=0,
            failed_attempts=0,
        )
        insufficient = evaluate_smoke_promotion(
            comparison,
            evidence_complete=True,
            runtime_errors=0,
            failed_attempts=1,
        )
        no_signal = {
            **comparison,
            "overall": {
                **metrics,
                "all_evidence_recall_at_10": {"delta": 0.0},
                "evidence_recall_at_10": {"delta": 0.0},
            },
        }
        stopped = evaluate_smoke_promotion(
            no_signal,
            evidence_complete=True,
            runtime_errors=0,
            failed_attempts=0,
        )

        self.assertEqual("promote", promoted["decision"])
        self.assertFalse(promoted["formal_method_conclusion"])
        self.assertEqual("insufficient", insufficient["decision"])
        self.assertEqual("stop", stopped["decision"])


if __name__ == "__main__":
    unittest.main()
