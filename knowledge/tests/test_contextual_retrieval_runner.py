#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for strict retrieval service-pair validation."""

import tempfile
import unittest
from pathlib import Path

from contextual_retrieval.artifacts import load_artifact, text_digest, write_artifact
from contextual_retrieval.dataset import CASE_SCHEMA, CHUNK_SCHEMA
from contextual_retrieval.runner import (
    RETRIEVAL_SAMPLES_SCHEMA,
    run_retrieval_ab,
    validate_service_pair,
)


class ContextualRetrievalRunnerTest(unittest.TestCase):
    def _config(self, variant, table, context_identity=None):
        return {
            "index_variant": variant,
            "search_mode": 1,
            "vectorstore": "pgvector",
            "pg_table": table,
            "embedding_model": "bge-m3",
            "embedding_endpoint": "https://embedding.test/v1",
            "embedding_dimensions": 1024,
            "embedding_header_names": [],
            "use_rrf": False,
            "hybrid_vector_weight": 0.99999,
            "hybrid_text_weight": 0.00001,
            "chunk_size": 500,
            "chunk_overlap": 50,
            "framework_module": {"path": "tag", "version": "devel"},
            "chunk_manifest_digest": "chunks-digest",
            "parent_manifest_digest": "parents-digest",
            "manifest_chunks_count": 2,
            "index_document_count": 2,
            "context_cache_identity": context_identity,
        }

    def _formal_fixture(self, root):
        chunk_records = []
        for index in range(20):
            content = f"content-{index}"
            chunk_records.append(
                {
                    "chunk_id": f"chunk-{index}",
                    "parent_document_id": f"parent-{index}",
                    "chunk_content_hash": text_digest(content),
                    "content": content,
                }
            )
        chunks = write_artifact(
            str(root / "chunks.json"),
            {
                "schema_version": CHUNK_SCHEMA,
                "parent_manifest_digest": "parents-digest",
                "chunk_size": 500,
                "chunk_overlap": 50,
                "chunks_count": 20,
                "chunks": chunk_records,
            },
        )
        write_artifact(
            str(root / "cases.json"),
            {
                "schema_version": CASE_SCHEMA,
                "chunk_manifest_digest": chunks["artifact_digest"],
                "cases_count": 3,
                "cases": [
                    {
                        "case_id": f"case-{index}",
                        "dataset_index": index,
                        "question": f"question-{index}",
                        "question_type": question_type,
                        "evidence": [
                            {
                                "evidence_id": "evidence-1",
                                "parent_document_id": "parent-0",
                                "chunk_ids": ["chunk-0"],
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
        baseline_config = self._config("baseline", "baseline_table")
        contextual_config = self._config(
            "contextual",
            "contextual_table",
            "cache-id",
        )
        for config in (baseline_config, contextual_config):
            config["chunk_manifest_digest"] = chunks["artifact_digest"]
            config["manifest_chunks_count"] = 20
            config["index_document_count"] = 20
        session = self._session(
            baseline_config,
            contextual_config,
            chunk_records,
            list(range(1, 20)) + [0],
            list(range(20)),
        )
        return session

    def test_valid_pair(self):
        validate_service_pair(
            self._config("baseline", "baseline_table"),
            self._config("contextual", "contextual_table", "cache-id"),
            {"artifact_digest": "chunks-digest", "chunks_count": 2},
        )

    def test_shared_table_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "different PG tables"):
            validate_service_pair(
                self._config("baseline", "shared"),
                self._config("contextual", "shared", "cache-id"),
                {"artifact_digest": "chunks-digest", "chunks_count": 2},
            )

    def test_smoke_run_writes_paired_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunk_records = []
            for index in range(20):
                content = f"content-{index}"
                chunk_records.append(
                    {
                        "chunk_id": f"chunk-{index}",
                        "parent_document_id": f"parent-{index}",
                        "chunk_content_hash": text_digest(content),
                        "content": content,
                    }
                )
            chunks = write_artifact(
                str(root / "chunks.json"),
                {
                    "schema_version": CHUNK_SCHEMA,
                    "parent_manifest_digest": "parents-digest",
                    "chunk_size": 500,
                    "chunk_overlap": 50,
                    "chunks_count": 20,
                    "chunks": chunk_records,
                },
            )
            write_artifact(
                str(root / "cases.json"),
                {
                    "schema_version": CASE_SCHEMA,
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "cases_count": 3,
                    "cases": [
                        {
                            "case_id": f"case-{index}",
                            "dataset_index": index,
                            "question": f"question-{index}",
                            "question_type": question_type,
                            "evidence": [
                                {
                                    "evidence_id": "evidence-1",
                                    "parent_document_id": "parent-0",
                                    "chunk_ids": ["chunk-0"],
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
            baseline_config = self._config("baseline", "baseline_table")
            contextual_config = self._config(
                "contextual",
                "contextual_table",
                "cache-id",
            )
            for config in (baseline_config, contextual_config):
                config["chunk_manifest_digest"] = chunks["artifact_digest"]
                config["manifest_chunks_count"] = 20
                config["index_document_count"] = 20
            baseline_order = list(range(1, 20)) + [0]
            contextual_order = list(range(20))
            session = self._session(
                baseline_config,
                contextual_config,
                chunk_records,
                baseline_order,
                contextual_order,
            )
            report = run_retrieval_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "smoke.json"),
                bootstrap_resamples=20,
                smoke_per_type=1,
                http_session=session,
            )

        self.assertEqual("valid", report["evidence_status"])
        self.assertEqual(3, report["paired_valid_cases"])
        self.assertEqual("promote", report["smoke_promotion"]["decision"])
        self.assertFalse(
            report["smoke_promotion"]["formal_method_conclusion"]
        )
        metric = report["comparison"]["overall"]["evidence_recall_at_4"]
        self.assertEqual(1.0, metric["delta"])

    def test_formal_run_is_eligible_and_applies_registered_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            session = self._formal_fixture(root)
            report = run_retrieval_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "formal.json"),
                bootstrap_resamples=20,
                http_session=session,
            )

        self.assertEqual("valid", report["evidence_status"])
        self.assertTrue(report["formal_ab_eligible"])
        self.assertEqual(3, report["paired_valid_cases"])
        self.assertEqual("pass", report["gate"]["decision"])

    def test_formal_checkpoint_resumes_without_repeating_completed_case(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = self._formal_fixture(root)

            class InterruptingSession:
                def __init__(self, delegate):
                    self.delegate = delegate
                    self.posts = 0

                def get(self, url, timeout):
                    return self.delegate.get(url, timeout)

                def post(self, url, json, timeout):
                    if self.posts == 2:
                        raise KeyboardInterrupt("simulated interruption")
                    self.posts += 1
                    return self.delegate.post(url, json, timeout)

            with self.assertRaises(KeyboardInterrupt):
                run_retrieval_ab(
                    str(root / "cases.json"),
                    str(root / "chunks.json"),
                    "http://baseline.test",
                    "http://contextual.test",
                    str(root / "formal.json"),
                    bootstrap_resamples=20,
                    http_session=InterruptingSession(first),
                )
            checkpoint = load_artifact(
                str(root / "formal.samples.json"),
                RETRIEVAL_SAMPLES_SCHEMA,
            )
            self.assertEqual(1, checkpoint["completed_cases"])

            second = self._formal_fixture(root)

            class CountingSession:
                def __init__(self, delegate):
                    self.delegate = delegate
                    self.posts = 0

                def get(self, url, timeout):
                    return self.delegate.get(url, timeout)

                def post(self, url, json, timeout):
                    self.posts += 1
                    return self.delegate.post(url, json, timeout)

            counting = CountingSession(second)
            report = run_retrieval_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "formal.json"),
                bootstrap_resamples=20,
                http_session=counting,
            )

        self.assertEqual(4, counting.posts)
        self.assertTrue(report["formal_ab_eligible"])

    def test_failed_request_attempt_invalidates_formal_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            delegate = self._formal_fixture(root)

            class FlakySession:
                def __init__(self, wrapped):
                    self.wrapped = wrapped
                    self.failed = False

                def get(self, url, timeout):
                    return self.wrapped.get(url, timeout)

                def post(self, url, json, timeout):
                    if not self.failed:
                        self.failed = True
                        raise OSError("transient request failure")
                    return self.wrapped.post(url, json, timeout)

            report = run_retrieval_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "formal.json"),
                bootstrap_resamples=20,
                http_session=FlakySession(delegate),
            )

        self.assertEqual(1, report["failed_request_attempts"])
        self.assertEqual("insufficient", report["evidence_status"])
        self.assertFalse(report["formal_ab_eligible"])

    def _session(
        self,
        baseline_config,
        contextual_config,
        chunks,
        baseline_order,
        contextual_order,
    ):
        class Response:
            def __init__(self, payload):
                self.payload = payload

            def raise_for_status(self):
                return None

            def json(self):
                return self.payload

        def documents(order):
            return [
                {
                    "text": chunks[index]["content"],
                    "score": 1.0 - rank / 100,
                    "metadata": {
                        "contextual_retrieval_chunk_id": chunks[index][
                            "chunk_id"
                        ],
                        "contextual_retrieval_parent_document_id": chunks[
                            index
                        ]["parent_document_id"],
                    },
                }
                for rank, index in enumerate(order)
            ]

        class Session:
            def get(self, url, timeout):
                del timeout
                return Response(
                    baseline_config
                    if "baseline.test" in url
                    else contextual_config
                )

            def post(self, url, json, timeout):
                del json, timeout
                order = (
                    baseline_order
                    if "baseline.test" in url
                    else contextual_order
                )
                return Response({"documents": documents(order)})

        return Session()


if __name__ == "__main__":
    unittest.main()
