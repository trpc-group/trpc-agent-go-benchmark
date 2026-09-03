#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for the isolated Contextual Retrieval I2 runner."""

import json
import tempfile
import unittest
from collections import Counter
from pathlib import Path

from contextual_retrieval.agentic import (
    AGENTIC_ANSWERS_SCHEMA,
    AGENTIC_CHECKPOINT_SCHEMA,
    AGENTIC_MANIFEST_SCHEMA,
    AGENTIC_TRACE_CONTRACT,
    TOOL_ARGUMENT_REPAIR_EXHAUSTED,
    TOOL_NOT_FOUND_RESPONSE,
    _answer_once,
    _chunk_indexes,
    _normalize_answer_payload,
    _public_agent_config,
    build_agentic_schedule,
    evaluate_agentic_smoke_gate,
    run_agentic_ab,
    validate_agentic_service_pair,
)
from contextual_retrieval.artifacts import (
    canonical_digest,
    file_digest,
    load_artifact,
    text_digest,
    write_artifact,
)
from contextual_retrieval.dataset import CASE_SCHEMA, CHUNK_SCHEMA


class AgenticRunnerTest(unittest.TestCase):
    def _fixture(self, root):
        chunk_records = []
        for index in range(4):
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
                "parent_manifest_digest": "parents",
                "chunk_size": 500,
                "chunk_overlap": 50,
                "chunks_count": len(chunk_records),
                "chunks": chunk_records,
            },
        )
        cases = []
        for index, question_type in enumerate(
            ("comparison_query", "inference_query", "temporal_query")
        ):
            cases.append(
                {
                    "case_id": f"case-{index}",
                    "dataset_index": index,
                    "question": f"question-{index}",
                    "answer": f"truth-{index}",
                    "question_type": question_type,
                    "evidence": [
                        {
                            "evidence_id": f"evidence-{index}",
                            "parent_document_id": f"parent-{index}",
                            "chunk_ids": [f"chunk-{index}"],
                        }
                    ],
                }
            )
        write_artifact(
            str(root / "cases.json"),
            {
                "schema_version": CASE_SCHEMA,
                "chunk_manifest_digest": chunks["artifact_digest"],
                "cases_count": len(cases),
                "cases": cases,
            },
        )
        return chunks, chunk_records

    @staticmethod
    def _config(chunks, variant):
        return {
            "index_variant": variant,
            "search_mode": 1,
            "agent_search_mode_enforced": True,
            "agent_search_mode_effective": 1,
            "tool_argument_policy": "query-guard/v1",
            "max_argument_repairs": 1,
            "silent_argument_rewrite": False,
            "provider_strict": False,
            "vectorstore": "pgvector",
            "pg_table": f"table_{variant}",
            "model_name": "deepseek-v3.2",
            "llm_endpoint": "https://agent.test/v1",
            "llm_header_names": ["X-SMG-Agent-Name"],
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
            "chunk_manifest_digest": chunks["artifact_digest"],
            "parent_manifest_digest": "parents",
            "manifest_chunks_count": chunks["chunks_count"],
            "index_document_count": chunks["chunks_count"],
            "context_cache_identity": (
                "deepseek-contexts" if variant == "contextual" else None
            ),
            "context_set_digest": (
                "deepseek-context-set" if variant == "contextual" else None
            ),
        }

    def _session(self, baseline, contextual, chunks):
        class Response:
            def __init__(self, payload):
                self.payload = payload

            def raise_for_status(self):
                return None

            def json(self):
                return self.payload

        class Session:
            def __init__(self):
                self.posts = []

            def get(inner, url, timeout):
                del timeout
                return Response(baseline if "baseline" in url else contextual)

            def post(inner, url, json, timeout):
                del timeout
                inner.posts.append((url, dict(json)))
                case_index = int(json["question"].rsplit("-", 1)[1])
                chunk = chunks[case_index]
                metadata = {
                    "contextual_retrieval_chunk_id": chunk["chunk_id"],
                    "contextual_retrieval_parent_document_id": chunk[
                        "parent_document_id"
                    ],
                }
                tool_response = {
                    "documents": [
                        {
                            "text": chunk["content"],
                            "score": 0.9,
                            "metadata": metadata,
                        }
                    ]
                }
                return Response(
                    {
                        "answer": "contextual" if "contextual" in url else "baseline",
                        "documents": [{"text": chunk["content"]}],
                        "trace": {
                            "tool_calls": [
                                {
                                    "id": "tool-1",
                                    "name": "search_knowledge_base",
                                    "arguments": '{"query":"focused query"}',
                                }
                            ],
                            "tool_responses": [
                                {
                                    "tool_id": "tool-1",
                                    "content": __import__("json").dumps(tool_response),
                                }
                            ],
                            "reasoning": ["reason"],
                            "searches": [
                                {
                                    "query": "focused query",
                                    "request": {
                                        "max_results": 4,
                                        "min_score": 0,
                                        "search_mode": 1,
                                        "history_length": 0,
                                        "has_filter_condition": False,
                                    },
                                    "results": [
                                        {
                                            "rank": 1,
                                            "document_id": chunk["chunk_id"],
                                            "content_sha256": chunk[
                                                "chunk_content_hash"
                                            ],
                                            "metadata": metadata,
                                            "score": 0.9,
                                        }
                                    ],
                                }
                            ],
                        },
                    }
                )

        return Session()

    @staticmethod
    def _single_answer_payload(
        chunk,
        *,
        tool_metadata=None,
        recorded_metadata=None,
        tool_score=0.9,
        recorded_score=0.9,
    ):
        identity = {
            "contextual_retrieval_chunk_id": chunk["chunk_id"],
            "contextual_retrieval_parent_document_id": chunk[
                "parent_document_id"
            ],
        }
        tool_metadata = identity if tool_metadata is None else tool_metadata
        recorded_metadata = (
            identity if recorded_metadata is None else recorded_metadata
        )
        tool_response = {
            "documents": [
                {
                    "text": chunk["content"],
                    "score": tool_score,
                    "metadata": tool_metadata,
                }
            ]
        }
        return {
            "answer": "answer",
            "documents": [{"text": chunk["content"]}],
            "trace": {
                "tool_calls": [
                    {
                        "id": "tool-1",
                        "name": "search_knowledge_base",
                        "arguments": '{"query":"focused query"}',
                    }
                ],
                "tool_responses": [
                    {
                        "tool_id": "tool-1",
                        "content": json.dumps(tool_response),
                    }
                ],
                "searches": [
                    {
                        "query": "focused query",
                        "request": {
                            "max_results": 4,
                            "min_score": 0,
                            "search_mode": 1,
                            "history_length": 0,
                            "has_filter_condition": False,
                        },
                        "results": [
                            {
                                "rank": 1,
                                "document_id": chunk["chunk_id"],
                                "content_sha256": chunk[
                                    "chunk_content_hash"
                                ],
                                "metadata": recorded_metadata,
                                "score": recorded_score,
                            }
                        ],
                    }
                ],
            },
        }

    @staticmethod
    def _normalize_payload(chunks, payload):
        chunks_by_id, chunks_by_hash = _chunk_indexes({"chunks": chunks})
        return _normalize_answer_payload(
            {"evidence": []},
            payload,
            chunks_by_id,
            chunks_by_hash,
        )

    def test_trace_contract_ignores_metadata_and_score_representation(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        chunk = chunks[0]
        tool_metadata = {
            "contextual_retrieval_chunk_id": chunk["chunk_id"],
            "contextual_retrieval_parent_document_id": chunk[
                "parent_document_id"
            ],
            "public": "tool",
        }
        recorded_metadata = {
            "contextual_retrieval_chunk_id": chunk["chunk_id"],
            "contextual_retrieval_parent_document_id": chunk[
                "parent_document_id"
            ],
            "trpc_agent_go_source": "internal",
        }
        normalized = self._normalize_payload(
            chunks,
            self._single_answer_payload(
                chunk,
                tool_metadata=tool_metadata,
                recorded_metadata=recorded_metadata,
                tool_score=0.9,
                recorded_score=0.887,
            ),
        )

        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["tool_runtime_errors"])
        self.assertEqual([], normalized["failure_categories"])
        diagnostics = normalized["trace_diagnostics"]
        self.assertEqual(AGENTIC_TRACE_CONTRACT, diagnostics["contract_version"])
        self.assertEqual(1, diagnostics["document_pairs_compared"])
        self.assertEqual(1, diagnostics["metadata_mismatch_documents"])
        self.assertEqual(1, diagnostics["score_mismatch_documents"])
        self.assertAlmostEqual(0.013, diagnostics["score_max_abs_delta"])
        self.assertEqual(
            ["public", "trpc_agent_go_source"],
            diagnostics["metadata_difference_keys"],
        )

    def test_trace_contract_accepts_unique_content_hash_identity_fallback(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        normalized = self._normalize_payload(
            chunks,
            self._single_answer_payload(chunks[0], tool_metadata={}),
        )

        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["failure_categories"])
        self.assertEqual(
            "content_hash",
            normalized["searches"][0]["tool_call"]["documents"][0][
                "identity_source"
            ],
        )
        self.assertEqual(
            1,
            normalized["trace_diagnostics"]["identity_fallback_documents"],
        )

    def test_trace_contract_rejects_document_identity_change(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        first, second = chunks[:2]
        payload = self._single_answer_payload(first)
        second_metadata = {
            "contextual_retrieval_chunk_id": second["chunk_id"],
            "contextual_retrieval_parent_document_id": second[
                "parent_document_id"
            ],
        }
        payload["documents"] = [{"text": second["content"]}]
        payload["trace"]["tool_responses"][0]["content"] = json.dumps(
            {
                "documents": [
                    {
                        "text": second["content"],
                        "score": 0.9,
                        "metadata": second_metadata,
                    }
                ]
            }
        )
        normalized = self._normalize_payload(chunks, payload)

        self.assertEqual(
            [
                "search 1 tool response document identity/order differs "
                "from server record"
            ],
            normalized["trace_validation_errors"],
        )
        self.assertEqual(
            ["trace_contract_error"], normalized["failure_categories"]
        )

    def test_trace_contract_rejects_document_order_change(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        first, second = chunks[:2]
        payload = self._single_answer_payload(first)

        def tool_document(chunk):
            return {
                "text": chunk["content"],
                "score": 0.9,
                "metadata": {
                    "contextual_retrieval_chunk_id": chunk["chunk_id"],
                    "contextual_retrieval_parent_document_id": chunk[
                        "parent_document_id"
                    ],
                },
            }

        def recorded_document(rank, chunk):
            return {
                "rank": rank,
                "document_id": chunk["chunk_id"],
                "content_sha256": chunk["chunk_content_hash"],
                "metadata": {
                    "contextual_retrieval_chunk_id": chunk["chunk_id"],
                    "contextual_retrieval_parent_document_id": chunk[
                        "parent_document_id"
                    ],
                },
                "score": 0.9,
            }

        payload["documents"] = [
            {"text": second["content"]},
            {"text": first["content"]},
        ]
        payload["trace"]["tool_responses"][0]["content"] = json.dumps(
            {"documents": [tool_document(second), tool_document(first)]}
        )
        payload["trace"]["searches"][0]["results"] = [
            recorded_document(1, first),
            recorded_document(2, second),
        ]
        normalized = self._normalize_payload(chunks, payload)

        self.assertEqual(
            [
                "search 1 tool response document identity/order differs "
                "from server record"
            ],
            normalized["trace_validation_errors"],
        )

    def test_trace_contract_rejects_content_change_for_known_chunk(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        chunk = chunks[0]
        payload = self._single_answer_payload(chunk)
        tool_response = json.loads(
            payload["trace"]["tool_responses"][0]["content"]
        )
        tool_response["documents"][0]["text"] = "tampered-content"
        payload["documents"] = [{"text": "tampered-content"}]
        payload["trace"]["tool_responses"][0]["content"] = json.dumps(
            tool_response
        )
        normalized = self._normalize_payload(chunks, payload)

        self.assertTrue(
            any(
                "content hash does not match chunk" in error
                for error in normalized["trace_validation_errors"]
            )
        )

    def test_trace_contract_rejects_conflicting_parent_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        chunk = chunks[0]
        normalized = self._normalize_payload(
            chunks,
            self._single_answer_payload(
                chunk,
                tool_metadata={
                    "contextual_retrieval_chunk_id": chunk["chunk_id"],
                    "contextual_retrieval_parent_document_id": "wrong-parent",
                },
            ),
        )

        self.assertTrue(
            any(
                "parent ID does not match chunk" in error
                for error in normalized["trace_validation_errors"]
            )
        )
        self.assertIn(
            "search_1_invalid_tool_response",
            normalized["trace_diagnostics"]["comparison_skipped_reasons"],
        )

    def test_missing_search_trace_only_reports_no_search_violation(self):
        normalized = self._normalize_payload(
            [],
            {
                "answer": "answer",
                "documents": [],
                "trace": {"tool_calls": [], "tool_responses": []},
            },
        )

        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["tool_runtime_errors"])
        self.assertEqual(
            ["no_search_tool_call"], normalized["protocol_violations"]
        )
        self.assertEqual(
            ["no_search_tool_call"], normalized["failure_categories"]
        )

    def test_non_json_tool_error_does_not_cascade_document_alignment(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        payload = self._single_answer_payload(chunks[0])
        payload["trace"]["tool_calls"].insert(
            0,
            {
                "id": "tool-error",
                "name": "search_knowledge_base",
                "arguments": '{"query":"failed query"}',
            },
        )
        payload["trace"]["tool_responses"].insert(
            0,
            {"tool_id": "tool-error", "content": "upstream unavailable"},
        )
        normalized = self._normalize_payload(chunks, payload)

        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual(1, len(normalized["tool_runtime_errors"]))
        self.assertIn(
            "tool response is not JSON", normalized["tool_runtime_errors"][0]
        )
        self.assertEqual(
            ["tool_runtime_error"],
            normalized["failure_categories"],
        )
        self.assertEqual(
            "tool-1", normalized["searches"][0]["tool_call_id"]
        )
        self.assertIn(
            "runtime_failed_tool_calls_excluded",
            normalized["trace_diagnostics"]["comparison_skipped_reasons"],
        )

    def test_wrong_tool_name_then_valid_search_is_recovered(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        payload = self._single_answer_payload(chunks[0])
        payload["documents"].insert(0, {"text": TOOL_NOT_FOUND_RESPONSE})
        payload["trace"]["tool_calls"][0]["name"] = "knowledge_search"
        payload["trace"]["tool_calls"].insert(
            0,
            {
                "id": "tool-name-error",
                "name": "search_knowledge_base",
                "arguments": '{"query":"focused query"}',
            },
        )
        payload["trace"]["tool_responses"].insert(
            0,
            {
                "tool_id": "tool-name-error",
                "content": TOOL_NOT_FOUND_RESPONSE,
            },
        )

        normalized = self._normalize_payload(chunks, payload)

        self.assertEqual([chunks[0]["content"]], normalized["contexts"])
        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["tool_runtime_errors"])
        self.assertEqual([], normalized["agent_errors"])
        self.assertEqual([], normalized["failure_categories"])
        self.assertEqual(1, normalized["tool_name_error_attempts"])
        self.assertEqual(1, normalized["recovered_tool_name_errors"])
        self.assertEqual(0, normalized["unrecovered_tool_name_errors"])
        self.assertEqual(
            1,
            normalized["trace_diagnostics"][
                "excluded_tool_error_contexts"
            ],
        )

    def test_wrong_tool_name_without_valid_search_is_agent_failure(self):
        normalized = self._normalize_payload(
            [],
            {
                "answer": "unable to retrieve",
                "documents": [{"text": TOOL_NOT_FOUND_RESPONSE}],
                "trace": {
                    "tool_calls": [
                        {
                            "id": "tool-name-error",
                            "name": "search_knowledge_base",
                            "arguments": '{"query":"focused query"}',
                        }
                    ],
                    "tool_responses": [
                        {
                            "tool_id": "tool-name-error",
                            "content": TOOL_NOT_FOUND_RESPONSE,
                        }
                    ],
                    "searches": [],
                },
            },
        )

        self.assertEqual([], normalized["contexts"])
        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["tool_runtime_errors"])
        self.assertEqual(1, normalized["unrecovered_tool_name_errors"])
        self.assertEqual(
            ["tool name repair was not completed"],
            normalized["agent_errors"],
        )
        self.assertEqual(
            ["no_search_tool_call", "tool_name_error"],
            normalized["failure_categories"],
        )

    def test_query_guard_feedback_then_corrected_call_is_recovered(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        payload = self._single_answer_payload(chunks[0])
        payload["trace"]["tool_calls"].insert(
            0,
            {
                "id": "tool-invalid",
                "name": "knowledge_search",
                "arguments": '{"query: ":"focused query"}',
            },
        )
        payload["trace"]["tool_responses"].insert(
            0,
            {
                "tool_id": "tool-invalid",
                "content": json.dumps(
                    {
                        "type": "tool_argument_validation_error",
                        "policy": "query-guard/v1",
                        "message": "Call knowledge_search again with query.",
                        "allowed": ["query"],
                        "missing": ["query"],
                        "unexpected": ["query: "],
                        "retryable": True,
                        "remaining_repairs": 1,
                    }
                ),
            },
        )
        normalized = self._normalize_payload(chunks, payload)

        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["tool_runtime_errors"])
        self.assertEqual([], normalized["agent_errors"])
        self.assertEqual([], normalized["failure_categories"])
        self.assertEqual(1, normalized["tool_argument_error_attempts"])
        self.assertEqual(1, normalized["recovered_tool_argument_errors"])
        self.assertEqual(0, normalized["unrecovered_tool_argument_errors"])
        self.assertTrue(normalized["tool_argument_errors"][0]["recovered"])
        self.assertEqual("tool-1", normalized["searches"][0]["tool_call_id"])
        self.assertIn(
            "tool_argument_validation_calls_excluded",
            normalized["trace_diagnostics"]["comparison_skipped_reasons"],
        )

    def test_unrecovered_query_guard_feedback_is_agent_failure(self):
        normalized = self._normalize_payload(
            [],
            {
                "answer": "unable to retrieve",
                "documents": [],
                "trace": {
                    "tool_calls": [
                        {
                            "id": "tool-invalid",
                            "name": "knowledge_search",
                            "arguments": '{"query: ":"focused query"}',
                        }
                    ],
                    "tool_responses": [
                        {
                            "tool_id": "tool-invalid",
                            "content": json.dumps(
                                {
                                    "type": "tool_argument_validation_error",
                                    "policy": "query-guard/v1",
                                    "message": "Call again with query.",
                                    "allowed": ["query"],
                                    "missing": ["query"],
                                    "unexpected": ["query: "],
                                    "retryable": True,
                                    "remaining_repairs": 1,
                                }
                            ),
                        }
                    ],
                    "searches": [],
                },
            },
        )

        self.assertEqual([], normalized["trace_validation_errors"])
        self.assertEqual([], normalized["tool_runtime_errors"])
        self.assertEqual(1, normalized["unrecovered_tool_argument_errors"])
        self.assertEqual(
            ["tool argument repair was not completed"],
            normalized["agent_errors"],
        )
        self.assertEqual(
            ["no_search_tool_call", "tool_argument_error"],
            normalized["failure_categories"],
        )

    def test_invalid_arguments_without_query_guard_feedback_fail_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            _, chunks = self._fixture(Path(directory))
        payload = self._single_answer_payload(chunks[0])
        payload["trace"]["tool_calls"][0]["arguments"] = (
            '{"query: ":"focused query"}'
        )
        normalized = self._normalize_payload(chunks, payload)

        self.assertTrue(
            any(
                "were not handled by query-guard/v1" in error
                for error in normalized["trace_validation_errors"]
            )
        )
        self.assertIn(
            "trace_contract_error", normalized["failure_categories"]
        )

    def test_agent_services_must_align_query_guard_policy(self):
        chunks = {"artifact_digest": "chunks", "chunks_count": 1}
        baseline = self._config(chunks, "baseline")
        contextual = self._config(chunks, "contextual")
        contextual["max_argument_repairs"] = 2

        with self.assertRaisesRegex(ValueError, "max argument repairs"):
            validate_agentic_service_pair(baseline, contextual, chunks)

    def test_repair_exhaustion_response_is_counted_as_agent_failure(self):
        class Response:
            status_code = 500

            @staticmethod
            def json():
                return {
                    "error_type": TOOL_ARGUMENT_REPAIR_EXHAUSTED,
                    "message": "Agent execution failed",
                }

            @staticmethod
            def raise_for_status():
                raise RuntimeError("HTTP 500")

        class Session:
            @staticmethod
            def post(url, json, timeout):
                del url, json, timeout
                return Response()

        result = _answer_once(
            Session(),
            "http://agent.test",
            {"question": "question"},
            10,
            {},
            {},
            "2026-07-26T00:00:00+00:00",
        )

        self.assertEqual("error", result["status"])
        self.assertEqual(
            TOOL_ARGUMENT_REPAIR_EXHAUSTED,
            result["request_attempt"]["error_type"],
        )
        self.assertEqual(2, result["tool_argument_error_attempts"])
        self.assertEqual(1, result["unrecovered_tool_argument_errors"])
        self.assertEqual(
            ["tool_argument_error"],
            result["failure_categories"],
        )

    def test_empty_answer_is_not_classified_as_request_failure(self):
        class Response:
            status_code = 200

            @staticmethod
            def json():
                return {
                    "answer": "",
                    "documents": [],
                    "trace": {"tool_calls": [], "tool_responses": []},
                }

            @staticmethod
            def raise_for_status():
                return None

        class Session:
            @staticmethod
            def post(url, json, timeout):
                del url, json, timeout
                return Response()

        result = _answer_once(
            Session(),
            "http://agent.test",
            {"question": "question"},
            10,
            {},
            {},
            "2026-07-26T00:00:00+00:00",
        )

        self.assertEqual("error", result["status"])
        self.assertEqual("success", result["request_attempt"]["status"])
        self.assertIsNone(result["request_attempt"]["error_type"])
        self.assertEqual("empty_answer", result["response_error_type"])
        self.assertEqual(["empty_answer"], result["failure_categories"])

    def test_smoke_gate_allows_bounded_agent_failures(self):
        gate = evaluate_agentic_smoke_gate(
            {"baseline": 1, "contextual": 3},
            30,
            chain_complete=True,
        )

        self.assertEqual("pass", gate["decision"])
        self.assertEqual(27, gate["requirements"]["minimum_successes_per_lane"])
        self.assertEqual(
            2, gate["requirements"]["maximum_failure_count_delta"]
        )
        self.assertEqual(2, gate["observed"]["failure_count_delta"])

    def test_smoke_gate_rejects_excessive_or_unbalanced_failures(self):
        excessive = evaluate_agentic_smoke_gate(
            {"baseline": 1, "contextual": 4},
            30,
            chain_complete=True,
        )
        protocol_error = evaluate_agentic_smoke_gate(
            {"baseline": 0, "contextual": 0},
            30,
            chain_complete=True,
            protocol_errors=1,
        )

        self.assertEqual("fail", excessive["decision"])
        self.assertFalse(excessive["checks"]["minimum_successes_met"])
        self.assertFalse(excessive["checks"]["failure_delta_within_limit"])
        self.assertEqual("fail", protocol_error["decision"])
        self.assertFalse(protocol_error["checks"]["protocol_errors_zero"])

    def test_agentic_smoke_freezes_one_call_per_lane_and_full_trace(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            session = self._session(baseline, contextual, chunk_records)

            report = run_agentic_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "agentic.json"),
                repeats=1,
                smoke_per_type=1,
                http_session=session,
            )
            answers = load_artifact(
                str(root / "agentic.answers.json"),
                AGENTIC_ANSWERS_SCHEMA,
            )
            manifest = load_artifact(
                str(root / "agentic.manifest.json"),
                AGENTIC_MANIFEST_SCHEMA,
            )
            checkpoint_path = root / "agentic.checkpoint.jsonl"
            checkpoint_records = [
                json.loads(line)
                for line in checkpoint_path.read_text(encoding="utf-8").splitlines()
            ]

        self.assertEqual(6, len(session.posts))
        self.assertTrue(all(payload["k"] == 4 for _, payload in session.posts))
        self.assertEqual("pass", report["smoke"]["decision"])
        self.assertEqual(6, answers["completed_executions"])
        result = answers["executions"][0]["result"]
        self.assertEqual("success", result["status"])
        self.assertEqual(["focused query"], result["search_queries"])
        self.assertEqual(1.0, result["evidence"]["cumulative_evidence_recall"])
        self.assertEqual(1, result["searches"][0]["request"]["search_mode"])
        self.assertEqual(AGENTIC_TRACE_CONTRACT, manifest["trace_contract"])
        self.assertEqual(AGENTIC_TRACE_CONTRACT, report["trace_contract"])
        self.assertEqual(0, report["protocol_errors"])
        self.assertEqual(
            AGENTIC_TRACE_CONTRACT,
            result["trace_diagnostics"]["contract_version"],
        )
        self.assertEqual(0, report["runtime"]["baseline"]["tool_runtime_errors"])
        self.assertEqual(13, len(checkpoint_records))
        self.assertEqual(13, answers["checkpoint_records"])
        self.assertEqual(13, report["checkpoint_records"])
        self.assertEqual(13, manifest["expected_checkpoint_records"])
        self.assertEqual(
            AGENTIC_CHECKPOINT_SCHEMA,
            checkpoint_records[0]["schema_version"],
        )
        self.assertEqual("header", checkpoint_records[0]["record_type"])
        self.assertEqual(
            6,
            sum(
                record.get("execution", {}).get("result", {}).get("status")
                == "started"
                for record in checkpoint_records
            ),
        )
        self.assertEqual(answers["checkpoint_sha256"], report["checkpoint_sha256"])

    def test_service_without_effective_vector_enforcement_is_rejected(self):
        chunks = {
            "artifact_digest": "chunks",
            "chunks_count": 1,
        }
        baseline = self._config(
            {"artifact_digest": "chunks", "chunks_count": 1},
            "baseline",
        )
        contextual = self._config(
            {"artifact_digest": "chunks", "chunks_count": 1},
            "contextual",
        )
        contextual["agent_search_mode_enforced"] = False
        with self.assertRaisesRegex(ValueError, "does not enforce"):
            validate_agentic_service_pair(baseline, contextual, chunks)

    def test_agent_config_is_public_and_rejects_unknown_fields(self):
        chunks = {
            "artifact_digest": "chunks",
            "chunks_count": 1,
        }
        baseline = self._config(chunks, "baseline")
        baseline["llm_endpoint"] = (
            "https://llm-user:llm-password@agent.test/private/llm"
            "?llm-token=value#private"
        )
        baseline["embedding_endpoint"] = (
            "https://embedding-user:embedding-password@embedding.test/"
            "private/embedding?embedding-token=value#private"
        )

        public = _public_agent_config(baseline)

        self.assertTrue(public["llm_endpoint"].startswith("https://agent.test|"))
        self.assertTrue(
            public["embedding_endpoint"].startswith(
                "https://embedding.test|"
            )
        )
        serialized = json.dumps(public, sort_keys=True)
        for secret in (
            "llm-user",
            "llm-password",
            "private/llm",
            "llm-token=value",
            "embedding-user",
            "embedding-password",
            "private/embedding",
            "embedding-token=value",
        ):
            self.assertNotIn(secret, serialized)

        baseline["unsupported_secret_field"] = "must-not-be-serialized"
        with self.assertRaisesRegex(ValueError, "unsupported_secret_field"):
            _public_agent_config(baseline)

    def test_agentic_error_artifacts_do_not_expose_endpoint_or_exception_text(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            delegate = self._session(baseline, contextual, chunk_records)

            class FailingSession:
                def get(inner, url, timeout):
                    return delegate.get(url, timeout)

                def post(inner, url, json, timeout):
                    del inner, url, json, timeout
                    raise RuntimeError(
                        "https://exception-user:exception-password@error.test/"
                        "raw/private/path?exception-token=value#private "
                        "third-party-exception-text"
                    )

            run_agentic_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "https://lane-user:lane-password@baseline.test/private/a"
                "?lane-token=value#private",
                "https://lane-user:lane-password@contextual.test/private/b"
                "?lane-token=value#private",
                str(root / "agentic.json"),
                repeats=1,
                smoke_per_type=1,
                http_session=FailingSession(),
            )
            artifacts = "\n".join(
                path.read_text(encoding="utf-8")
                for path in root.glob("agentic*")
            )

        for secret in (
            "lane-user",
            "lane-password",
            "private/a",
            "private/b",
            "lane-token=value",
            "exception-user",
            "exception-password",
            "raw/private/path",
            "exception-token=value",
            "third-party-exception-text",
        ):
            self.assertNotIn(secret, artifacts)
        self.assertIn("request_error", artifacts)

    def test_unknown_agent_completion_is_not_sampled_again_on_resume(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            delegate = self._session(baseline, contextual, chunk_records)

            class InterruptingSession:
                def get(inner, url, timeout):
                    return delegate.get(url, timeout)

                def post(inner, url, json, timeout):
                    del inner, url, json, timeout
                    raise KeyboardInterrupt("request completion is unknown")

            with self.assertRaises(KeyboardInterrupt):
                run_agentic_ab(
                    str(root / "cases.json"),
                    str(root / "chunks.json"),
                    "http://baseline.test",
                    "http://contextual.test",
                    str(root / "agentic.json"),
                    repeats=1,
                    smoke_per_type=1,
                    http_session=InterruptingSession(),
                )

            resumed = self._session(baseline, contextual, chunk_records)
            report = run_agentic_ab(
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "agentic.json"),
                repeats=1,
                smoke_per_type=1,
                http_session=resumed,
            )
            answers = load_artifact(
                str(root / "agentic.answers.json"),
                AGENTIC_ANSWERS_SCHEMA,
            )
            checkpoint_records = [
                json.loads(line)
                for line in (root / "agentic.checkpoint.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]

        self.assertEqual(5, len(resumed.posts))
        self.assertEqual("fail", report["smoke"]["decision"])
        statuses = [
            record["result"]["status"] for record in answers["executions"]
        ]
        self.assertEqual(1, statuses.count("indeterminate"))
        self.assertEqual(13, len(checkpoint_records))
        indeterminate = next(
            record["execution"]["result"]
            for record in checkpoint_records
            if record.get("execution", {}).get("result", {}).get("status")
            == "indeterminate"
        )
        self.assertEqual(
            "indeterminate",
            indeterminate["request_attempt"]["status"],
        )

    def test_complete_answers_are_reused_without_checkpoint_or_answer_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            first_session = self._session(baseline, contextual, chunk_records)
            arguments = (
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "agentic.json"),
            )
            run_agentic_ab(
                *arguments,
                repeats=1,
                smoke_per_type=1,
                http_session=first_session,
            )
            checkpoint_path = root / "agentic.checkpoint.jsonl"
            answers_path = root / "agentic.answers.json"
            report_path = root / "agentic.json"
            checkpoint_digest = file_digest(str(checkpoint_path))
            answers_digest = file_digest(str(answers_path))
            report_digest = file_digest(str(report_path))
            checkpoint_mtime = checkpoint_path.stat().st_mtime_ns
            answers_mtime = answers_path.stat().st_mtime_ns
            report_mtime = report_path.stat().st_mtime_ns

            second_session = self._session(baseline, contextual, chunk_records)
            run_agentic_ab(
                *arguments,
                repeats=1,
                smoke_per_type=1,
                http_session=second_session,
            )

            self.assertEqual([], second_session.posts)
            self.assertEqual(checkpoint_digest, file_digest(str(checkpoint_path)))
            self.assertEqual(answers_digest, file_digest(str(answers_path)))
            self.assertEqual(report_digest, file_digest(str(report_path)))
            self.assertEqual(checkpoint_mtime, checkpoint_path.stat().st_mtime_ns)
            self.assertEqual(answers_mtime, answers_path.stat().st_mtime_ns)
            self.assertEqual(report_mtime, report_path.stat().st_mtime_ns)

    def test_unterminated_torn_tail_is_repaired_before_safe_resume(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            delegate = self._session(baseline, contextual, chunk_records)

            class InterruptingSession:
                def get(inner, url, timeout):
                    return delegate.get(url, timeout)

                def post(inner, url, json, timeout):
                    del inner, url, json, timeout
                    raise KeyboardInterrupt("request completion is unknown")

            arguments = (
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "agentic.json"),
            )
            with self.assertRaises(KeyboardInterrupt):
                run_agentic_ab(
                    *arguments,
                    repeats=1,
                    smoke_per_type=1,
                    http_session=InterruptingSession(),
                )
            checkpoint = root / "agentic.checkpoint.jsonl"
            with checkpoint.open("ab") as handle:
                handle.write(b'{"schema_version":"torn')

            resumed = self._session(baseline, contextual, chunk_records)
            run_agentic_ab(
                *arguments,
                repeats=1,
                smoke_per_type=1,
                http_session=resumed,
            )
            records = [
                json.loads(line)
                for line in checkpoint.read_text(encoding="utf-8").splitlines()
            ]
            answers = load_artifact(
                str(root / "agentic.answers.json"),
                AGENTIC_ANSWERS_SCHEMA,
            )

        self.assertEqual(5, len(resumed.posts))
        self.assertEqual(13, len(records))
        self.assertEqual(
            1,
            sum(
                execution["result"]["status"] == "indeterminate"
                for execution in answers["executions"]
            ),
        )

    def test_newline_terminated_corrupt_checkpoint_tail_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            delegate = self._session(baseline, contextual, chunk_records)

            class InterruptingSession:
                def get(inner, url, timeout):
                    return delegate.get(url, timeout)

                def post(inner, url, json, timeout):
                    del inner, url, json, timeout
                    raise KeyboardInterrupt("request completion is unknown")

            arguments = (
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "agentic.json"),
            )
            with self.assertRaises(KeyboardInterrupt):
                run_agentic_ab(
                    *arguments,
                    repeats=1,
                    smoke_per_type=1,
                    http_session=InterruptingSession(),
                )
            with (root / "agentic.checkpoint.jsonl").open("ab") as handle:
                handle.write(b"{broken}\n")
            resumed = self._session(baseline, contextual, chunk_records)
            with self.assertRaisesRegex(ValueError, "invalid JSON"):
                run_agentic_ab(
                    *arguments,
                    repeats=1,
                    smoke_per_type=1,
                    http_session=resumed,
                )

        self.assertEqual([], resumed.posts)

    def test_checkpoint_header_execution_ids_are_strictly_validated(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks, chunk_records = self._fixture(root)
            baseline = self._config(chunks, "baseline")
            contextual = self._config(chunks, "contextual")
            arguments = (
                str(root / "cases.json"),
                str(root / "chunks.json"),
                "http://baseline.test",
                "http://contextual.test",
                str(root / "agentic.json"),
            )
            run_agentic_ab(
                *arguments,
                repeats=1,
                smoke_per_type=1,
                http_session=self._session(baseline, contextual, chunk_records),
            )
            checkpoint_path = root / "agentic.checkpoint.jsonl"
            records = [
                json.loads(line)
                for line in checkpoint_path.read_text(encoding="utf-8").splitlines()
            ]
            records[0]["execution_ids"] = list(reversed(records[0]["execution_ids"]))
            unsigned_header = dict(records[0])
            unsigned_header.pop("artifact_digest")
            records[0]["artifact_digest"] = canonical_digest(unsigned_header)
            checkpoint_path.write_text(
                "\n".join(
                    json.dumps(record, separators=(",", ":"), sort_keys=True)
                    for record in records
                )
                + "\n",
                encoding="utf-8",
            )

            with self.assertRaisesRegex(ValueError, "another run"):
                run_agentic_ab(
                    *arguments,
                    repeats=1,
                    smoke_per_type=1,
                    http_session=self._session(
                        baseline,
                        contextual,
                        chunk_records,
                    ),
                )

    def test_three_repeat_schedule_is_balanced_and_deterministic(self):
        cases = [{"case_id": f"case-{index}"} for index in range(450)]
        first = build_agentic_schedule(cases, repeats=3, seed=7)
        second = build_agentic_schedule(cases, repeats=3, seed=7)

        self.assertEqual(first, second)
        self.assertEqual(2700, len(first))
        first_lanes = Counter(
            item["lane"] for item in first if item["lane_position"] == 0
        )
        self.assertEqual(675, first_lanes["baseline"])
        self.assertEqual(675, first_lanes["contextual"])


if __name__ == "__main__":
    unittest.main()
