#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for evidence-aware MultiHop-RAG preparation."""

import tempfile
import unittest
from pathlib import Path

from contextual_retrieval.artifacts import write_artifact
from contextual_retrieval.dataset import (
    CHUNK_SCHEMA,
    QUERY_SCHEMA,
    map_evidence_to_chunks,
)


class ContextualRetrievalDatasetTest(unittest.TestCase):
    def test_cross_boundary_fact_maps_to_both_chunks(self):
        parent_id = "parent-1"
        parent_text = "abcdefghijKLMNOPQRSTuvwxyz"
        fact = "ijKLMN"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            queries = write_artifact(
                str(root / "queries.json"),
                {
                    "schema_version": QUERY_SCHEMA,
                    "parent_manifest_digest": "parents-digest",
                    "selection": {},
                    "cases_count": 1,
                    "cases": [
                        {
                            "case_id": "case-1",
                            "dataset_index": 0,
                            "question": "question",
                            "answer": "answer",
                            "question_type": "comparison_query",
                            "evidence": [
                                {
                                    "evidence_id": "evidence-1",
                                    "fact": fact,
                                    "parent_document_id": parent_id,
                                }
                            ],
                        }
                    ],
                },
            )
            chunks = write_artifact(
                str(root / "chunks.json"),
                {
                    "schema_version": CHUNK_SCHEMA,
                    "parent_manifest_digest": "parents-digest",
                    "chunk_size": 10,
                    "chunk_overlap": 2,
                    "chunks_count": 3,
                    "chunks": [
                        {
                            "parent_document_id": parent_id,
                            "chunk_id": "chunk-1",
                            "chunk_index": 1,
                            "content_start": 0,
                            "content_end": 10,
                            "content": parent_text[0:10],
                        },
                        {
                            "parent_document_id": parent_id,
                            "chunk_id": "chunk-2",
                            "chunk_index": 2,
                            "content_start": 8,
                            "content_end": 20,
                            "content": parent_text[8:20],
                        },
                        {
                            "parent_document_id": parent_id,
                            "chunk_id": "chunk-3",
                            "chunk_index": 3,
                            "content_start": 18,
                            "content_end": len(parent_text),
                            "content": parent_text[18:],
                        },
                    ],
                },
            )
            cases, preflight = map_evidence_to_chunks(
                str(root / "queries.json"),
                str(root / "chunks.json"),
                str(root / "cases.json"),
                str(root / "preflight.json"),
            )

        self.assertEqual(queries["artifact_digest"], cases["query_manifest_digest"])
        self.assertEqual(chunks["artifact_digest"], cases["chunk_manifest_digest"])
        self.assertEqual("valid", preflight["status"])
        self.assertEqual(1, preflight["mapped_evidence_count"])
        self.assertEqual(
            ["chunk-1", "chunk-2"],
            cases["cases"][0]["evidence"][0]["chunk_ids"],
        )


if __name__ == "__main__":
    unittest.main()
