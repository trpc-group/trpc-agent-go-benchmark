#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Offline tests for the guarded I2 Agentic service controller."""

import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from contextual_retrieval.agentic import (
    AGENTIC_ANSWERS_SCHEMA,
    AGENTIC_LINEAGE_SCHEMA,
    run_agentic_ab as run_raw_agentic_ab,
)
from contextual_retrieval.agentic_controller import (
    AGENTIC_CONTROLLER_MANIFEST_SCHEMA,
    AGENTIC_CONTROLLER_REPORT_SCHEMA,
    AGENTIC_PROVENANCE_SCHEMA,
    EXPECTED_CONTEXT_CHUNKS,
    _capture_source_snapshots,
    run_agentic_server_ab,
    validate_agentic_prebuilt_inputs,
)
from contextual_retrieval.artifacts import (
    load_artifact,
    public_endpoint_identity,
    text_digest,
    write_artifact,
)
from contextual_retrieval.context_cache import (
    CONTEXT_FINISH_REASON_POLICY,
    CONTEXT_PROMPT,
    CONTEXT_SUMMARY_SCHEMA,
)
from contextual_retrieval.controller import INDEX_STATE_SCHEMA, _index_identity
from contextual_retrieval.dataset import CASE_SCHEMA, CHUNK_SCHEMA


class AgenticControllerTest(unittest.TestCase):
    def _artifacts(self, root: Path, cases_count: int = 450):
        content = "shared evidence"
        chunk = {
            "chunk_id": "chunk-0",
            "parent_document_id": "parent-0",
            "chunk_content_hash": text_digest(content),
            "content": content,
        }
        chunks = write_artifact(
            str(root / "chunks.json"),
            {
                "schema_version": CHUNK_SCHEMA,
                "parent_manifest_digest": "parents",
                "chunk_size": 500,
                "chunk_overlap": 50,
                "chunks_count": EXPECTED_CONTEXT_CHUNKS,
                "chunks": [chunk],
            },
        )
        case_records = [
            {
                "case_id": f"case-{index}",
                "dataset_index": index,
                "question": f"question-{index}",
                "answer": "shared answer",
                "question_type": (
                    "comparison_query"
                    if index < 150
                    else "inference_query"
                    if index < 300
                    else "temporal_query"
                ),
                "evidence": [
                    {
                        "evidence_id": f"evidence-{index}",
                        "parent_document_id": "parent-0",
                        "chunk_ids": ["chunk-0"],
                    }
                ],
            }
            for index in range(cases_count)
        ]
        cases = write_artifact(
            str(root / "cases.json"),
            {
                "schema_version": CASE_SCHEMA,
                "chunk_manifest_digest": chunks["artifact_digest"],
                "cases_count": cases_count,
                "cases": case_records,
            },
        )
        summary = write_artifact(
            str(root / "context-summary.json"),
            {
                "schema_version": CONTEXT_SUMMARY_SCHEMA,
                "status": "valid",
                "cache_identity": "deepseek-context-cache",
                "context_set_digest": "deepseek-context-set",
                "chunk_manifest_digest": chunks["artifact_digest"],
                "expected_chunks": EXPECTED_CONTEXT_CHUNKS,
                "successful_chunks": EXPECTED_CONTEXT_CHUNKS,
                "error_chunks": 0,
                "missing_chunks": 0,
                "config": {
                    "model": "deepseek-v3.2",
                    "prompt_id": "anthropic-contextual-retrieval-v1",
                    "prompt_hash": text_digest(CONTEXT_PROMPT),
                    "temperature": 0,
                    "reasoning": "unspecified",
                    "finish_reason_policy": CONTEXT_FINISH_REASON_POLICY,
                    "transport_max_retries": 0,
                },
            },
        )
        baseline_config = self._config(chunks, "baseline", None)
        contextual_config = self._config(
            chunks,
            "contextual",
            summary["cache_identity"],
        )
        baseline_state = self._state(
            root / "baseline.index-state.json",
            baseline_config,
        )
        contextual_state = self._state(
            root / "contextual.index-state.json",
            contextual_config,
        )
        return (
            chunks,
            cases,
            summary,
            baseline_config,
            contextual_config,
            baseline_state,
            contextual_state,
        )

    @staticmethod
    def _config(chunks, variant, cache_identity):
        return {
            "pg_table": f"i2_{variant}_001",
            "index_variant": variant,
            "embedding_model": "bge-m3",
            "embedding_endpoint": public_endpoint_identity(
                "https://embedding.test/v1"
            ),
            "embedding_dimensions": 1024,
            "embedding_header_names": [],
            "vectorstore": "pgvector",
            "search_mode": 1,
            "agent_search_mode_enforced": True,
            "agent_search_mode_effective": 1,
            "tool_argument_policy": "query-guard/v1",
            "max_argument_repairs": 1,
            "silent_argument_rewrite": False,
            "provider_strict": False,
            "use_rrf": False,
            "hybrid_vector_weight": 0.99999,
            "hybrid_text_weight": 0.00001,
            "chunk_size": 500,
            "chunk_overlap": 50,
            "framework_module": {"path": "tag", "version": "devel"},
            "chunk_manifest_digest": chunks["artifact_digest"],
            "parent_manifest_digest": "parents",
            "manifest_chunks_count": EXPECTED_CONTEXT_CHUNKS,
            "context_cache_identity": cache_identity,
            "context_set_digest": (
                "deepseek-context-set" if variant == "contextual" else None
            ),
            "index_document_count": EXPECTED_CONTEXT_CHUNKS,
            "model_name": "deepseek-v3.2",
            "llm_endpoint": public_endpoint_identity(
                "https://agent.test/v1"
            ),
            "llm_header_names": [],
        }

    @staticmethod
    def _state(path: Path, config):
        return write_artifact(
            str(path),
            {
                "schema_version": INDEX_STATE_SCHEMA,
                "status": "complete",
                "identity": _index_identity(config),
                "builder_source": {
                    "repository": AgenticControllerTest._snapshot(),
                    "benchmark_repository": AgenticControllerTest._snapshot(),
                },
                "expected_count": EXPECTED_CONTEXT_CHUNKS,
                "count_after": EXPECTED_CONTEXT_CHUNKS,
            },
        )

    @staticmethod
    def _snapshot(worktree_dirty=False):
        return {
            "commit": "abc",
            "branch": "feat",
            "tracked_dirty": worktree_dirty,
            "untracked_dirty": False,
            "worktree_dirty": worktree_dirty,
        }

    @staticmethod
    def _agent_session(baseline, contextual, chunk, interrupt=False):
        class Response:
            status_code = 200

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
                return Response(baseline if ":8765" in url else contextual)

            def post(inner, url, json, timeout):
                del timeout
                inner.posts.append((url, dict(json)))
                if interrupt:
                    raise KeyboardInterrupt("unknown Agent completion")
                metadata = {
                    "contextual_retrieval_chunk_id": chunk["chunk_id"],
                    "contextual_retrieval_parent_document_id": chunk[
                        "parent_document_id"
                    ],
                }
                tool_payload = {
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
                        "answer": "shared answer",
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
                                    "content": __import__("json").dumps(
                                        tool_payload
                                    ),
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

    def test_input_validation_binds_context_cache_to_contextual_index(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            values = self._artifacts(root)
            chunks, _, summary, _, _, baseline_state, contextual_state = values
            tables = validate_agentic_prebuilt_inputs(
                summary,
                baseline_state,
                contextual_state,
                chunks,
                self._snapshot(),
                self._snapshot(),
            )
            bad_contextual = {
                **contextual_state,
                "identity": {
                    **contextual_state["identity"],
                    "context_cache_identity": "another-cache",
                },
            }
            with self.assertRaisesRegex(ValueError, "context_cache_identity"):
                validate_agentic_prebuilt_inputs(
                    summary,
                    baseline_state,
                    bad_contextual,
                    chunks,
                    self._snapshot(),
                    self._snapshot(),
                )

            bad_context_set = {
                **contextual_state,
                "identity": {
                    **contextual_state["identity"],
                    "context_set_digest": "another-context-set",
                },
            }
            with self.assertRaisesRegex(ValueError, "context_set_digest"):
                validate_agentic_prebuilt_inputs(
                    summary,
                    baseline_state,
                    bad_context_set,
                    chunks,
                    self._snapshot(),
                    self._snapshot(),
                )

            with self.assertRaisesRegex(ValueError, "dirty"):
                validate_agentic_prebuilt_inputs(
                    summary,
                    baseline_state,
                    contextual_state,
                    chunks,
                    self._snapshot(worktree_dirty=True),
                    self._snapshot(),
                )

            bad_builder = {
                **contextual_state,
                "builder_source": {
                    **contextual_state["builder_source"],
                    "repository": {
                        **contextual_state["builder_source"]["repository"],
                        "commit": "old-builder",
                    },
                },
            }
            reused_tables = validate_agentic_prebuilt_inputs(
                summary,
                baseline_state,
                bad_builder,
                chunks,
                self._snapshot(),
                self._snapshot(),
            )

        self.assertEqual(("i2_baseline_001", "i2_contextual_001"), tables)
        self.assertEqual(tables, reused_tables)

    def test_source_snapshot_defaults_to_benchmark_module(self):
        benchmark = Path("/benchmark")
        snapshot = self._snapshot()
        with mock.patch(
            "contextual_retrieval.agentic_controller._git_snapshot",
            return_value=snapshot,
        ) as git_snapshot:
            framework, captured_benchmark = _capture_source_snapshots(
                benchmark,
                None,
            )
            self.assertEqual(1, git_snapshot.call_count)
            self.assertEqual("benchmark_module_only", framework["scope"])
            self.assertEqual(snapshot, captured_benchmark)

            explicit, captured_benchmark = _capture_source_snapshots(
                benchmark,
                "/framework",
            )
            self.assertEqual(3, git_snapshot.call_count)
            self.assertEqual("explicit_framework_repository", explicit["scope"])
            self.assertEqual(snapshot, captured_benchmark)

    def test_smoke_owns_services_without_loading_or_promoting(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            values = self._artifacts(root)
            (
                _,
                _,
                _,
                baseline_config,
                contextual_config,
                _,
                _,
            ) = values
            baseline_runtime = {
                **baseline_config,
                "pg_connection": "postgres://user:secret@db/private",
                "llm_endpoint": (
                    "https://user:secret@agent.test/v1?token=secret#fragment"
                ),
                "embedding_endpoint": (
                    "https://user:secret@embedding.test/v1?token=secret#fragment"
                ),
            }
            contextual_runtime = {
                **contextual_config,
                "pg_connection": "postgres://user:secret@db/private",
                "llm_endpoint": (
                    "https://user:secret@agent.test/v1?token=secret#fragment"
                ),
                "embedding_endpoint": (
                    "https://user:secret@embedding.test/v1?token=secret#fragment"
                ),
            }
            output = root / "run"
            processes = [mock.Mock(pid=11), mock.Mock(pid=12)]
            with mock.patch(
                "contextual_retrieval.agentic_controller._port_is_available",
                return_value=True,
            ), mock.patch(
                "contextual_retrieval.agentic_controller._git_snapshot",
                return_value={
                    "commit": "abc",
                    "branch": "feat",
                    "tracked_dirty": False,
                    "untracked_dirty": False,
                    "worktree_dirty": False,
                },
            ), mock.patch(
                "contextual_retrieval.agentic_controller._build_service",
                return_value=(root / "service", root / "build.log"),
            ), mock.patch(
                "contextual_retrieval.agentic_controller._start_service",
                side_effect=[
                    (processes[0], io.StringIO(), root / "baseline.log"),
                    (processes[1], io.StringIO(), root / "contextual.log"),
                ],
            ), mock.patch(
                "contextual_retrieval.agentic_controller._wait_for_health",
            ), mock.patch(
                "contextual_retrieval.agentic_controller._request_json",
                side_effect=[baseline_runtime, contextual_runtime],
            ) as request_json, mock.patch(
                "contextual_retrieval.agentic_controller._stop_owned_process",
                side_effect=lambda process: {
                    "pid": process.pid,
                    "owned": True,
                    "stopped": True,
                    "forced": False,
                },
            ), mock.patch(
                "contextual_retrieval.agentic_controller.run_agentic_ab",
                return_value={
                    "artifact_digest": "agentic-report",
                    "evidence_scope": "agentic_operational_smoke",
                    "formal_answers_eligible": False,
                },
            ) as run_agentic:
                report = run_agentic_server_ab(
                    str(root),
                    str(root / "chunks.json"),
                    str(root / "cases.json"),
                    str(root / "contexts.jsonl"),
                    str(root / "context-summary.json"),
                    str(root / "baseline.index-state.json"),
                    str(root / "contextual.index-state.json"),
                    str(output),
                    mode="smoke",
                    smoke_per_type=150,
                )

            call = run_agentic.call_args
            lineage = call.kwargs["verified_lineage"]
            self.assertEqual("smoke", lineage["mode"])
            self.assertEqual(1, call.kwargs["repeats"])
            self.assertEqual(450, lineage["expected_cases"])
            self.assertEqual(150, call.kwargs["smoke_per_type"])
            self.assertEqual(2, len(request_json.call_args_list))
            self.assertFalse(report["load_endpoint_called"])
            self.assertFalse(report["automatic_formal_promotion"])
            self.assertEqual(2, len(report["cleanup"]))
            self.assertEqual("captured", report["build_log"])
            self.assertEqual(
                {"baseline": "captured", "contextual": "captured"},
                report["service_logs"],
            )
            baseline_artifact = load_artifact(
                str(output / "baseline.config.json"),
                "contextual-retrieval/service-config/v2",
            )
            contextual_artifact = load_artifact(
                str(output / "contextual.config.json"),
                "contextual-retrieval/service-config/v2",
            )
            serialized = "\n".join(
                path.read_text(encoding="utf-8")
                for path in sorted(output.glob("*.json"))
            )
            self.assertEqual(
                "contextual-retrieval/service-config/v2",
                baseline_artifact["schema_version"],
            )
            self.assertEqual(
                "contextual-retrieval/service-config/v2",
                contextual_artifact["schema_version"],
            )
            self.assertNotIn("pg_connection", serialized)
            self.assertNotIn("user:secret", serialized)
            self.assertNotIn("token=secret", serialized)
            self.assertNotIn(str(root), serialized)

    def test_unknown_runtime_config_fails_and_still_cleans_owned_services(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            values = self._artifacts(root)
            baseline_config = values[3]
            contextual_config = {
                **values[4],
                "unsupported_secret": f"secret-value-at-{root}",
            }
            output = root / "run"
            handles = [io.StringIO(), io.StringIO()]
            with mock.patch(
                "contextual_retrieval.agentic_controller._port_is_available",
                return_value=True,
            ), mock.patch(
                "contextual_retrieval.agentic_controller._git_snapshot",
                return_value={
                    "commit": "abc",
                    "branch": "feat",
                    "tracked_dirty": False,
                    "untracked_dirty": False,
                    "worktree_dirty": False,
                },
            ), mock.patch(
                "contextual_retrieval.agentic_controller._build_service",
                return_value=(root / "service", root / "build.log"),
            ), mock.patch(
                "contextual_retrieval.agentic_controller._start_service",
                side_effect=[
                    (mock.Mock(pid=21), handles[0], root / "baseline.log"),
                    (mock.Mock(pid=22), handles[1], root / "contextual.log"),
                ],
            ), mock.patch(
                "contextual_retrieval.agentic_controller._wait_for_health",
            ), mock.patch(
                "contextual_retrieval.agentic_controller._request_json",
                side_effect=[baseline_config, contextual_config],
            ), mock.patch(
                "contextual_retrieval.agentic_controller._stop_owned_process",
                return_value={"owned": True, "stopped": True},
            ) as stop:
                with self.assertRaisesRegex(RuntimeError, "controller failed"):
                    run_agentic_server_ab(
                        str(root),
                        str(root / "chunks.json"),
                        str(root / "cases.json"),
                        str(root / "contexts.jsonl"),
                        str(root / "context-summary.json"),
                        str(root / "baseline.index-state.json"),
                        str(root / "contextual.index-state.json"),
                        str(output),
                        mode="formal",
                    )
            report = load_artifact(
                str(output / "controller.report.json"),
                AGENTIC_CONTROLLER_REPORT_SCHEMA,
            )

        self.assertEqual(2, stop.call_count)
        self.assertEqual("insufficient", report["status"])
        self.assertFalse(report["formal_answers_eligible"])
        self.assertEqual(
            {"type": "ValueError", "message": "agentic controller failed"},
            report["error"],
        )
        self.assertNotIn("unsupported_secret", json.dumps(report))
        self.assertNotIn(str(root), json.dumps(report))

    def test_controller_resume_reuses_identity_and_never_resends_unknown_call(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            values = self._artifacts(root)
            chunks = values[0]
            baseline_config = values[3]
            contextual_config = values[4]
            chunk = chunks["chunks"][0]
            output = root / "run"
            interrupted = self._agent_session(
                baseline_config,
                contextual_config,
                chunk,
                interrupt=True,
            )
            resumed = self._agent_session(
                baseline_config,
                contextual_config,
                chunk,
            )
            agent_sessions = [interrupted, resumed]

            def run_with_fake_session(*args, **kwargs):
                return run_raw_agentic_ab(
                    *args,
                    **kwargs,
                    http_session=agent_sessions.pop(0),
                )

            processes = [mock.Mock(pid=100 + index) for index in range(4)]
            handles = [io.StringIO() for _ in range(4)]
            starts = [
                (
                    processes[index],
                    handles[index],
                    root / f"service-{index}.log",
                )
                for index in range(4)
            ]
            clean_snapshot = {
                "commit": "abc",
                "branch": "feat",
                "tracked_dirty": False,
                "untracked_dirty": False,
                "worktree_dirty": False,
            }
            arguments = (
                str(root),
                str(root / "chunks.json"),
                str(root / "cases.json"),
                str(root / "contexts.jsonl"),
                str(root / "context-summary.json"),
                str(root / "baseline.index-state.json"),
                str(root / "contextual.index-state.json"),
                str(output),
            )
            with mock.patch(
                "contextual_retrieval.agentic_controller._port_is_available",
                return_value=True,
            ), mock.patch(
                "contextual_retrieval.agentic_controller._git_snapshot",
                return_value=clean_snapshot,
            ), mock.patch(
                "contextual_retrieval.agentic_controller._build_service",
                return_value=(root / "service", root / "build.log"),
            ) as build_service, mock.patch(
                "contextual_retrieval.agentic_controller._start_service",
                side_effect=starts,
            ) as start_service, mock.patch(
                "contextual_retrieval.agentic_controller._wait_for_health",
            ), mock.patch(
                "contextual_retrieval.agentic_controller._request_json",
                side_effect=[
                    baseline_config,
                    contextual_config,
                    baseline_config,
                    contextual_config,
                ],
            ), mock.patch(
                "contextual_retrieval.agentic_controller._stop_owned_process",
                side_effect=lambda process: {
                    "pid": process.pid,
                    "owned": True,
                    "stopped": True,
                    "forced": False,
                },
            ), mock.patch(
                "contextual_retrieval.agentic_controller.run_agentic_ab",
                side_effect=run_with_fake_session,
            ) as controlled_run:
                with self.assertRaisesRegex(RuntimeError, "controller failed"):
                    run_agentic_server_ab(*arguments, mode="smoke")
                provenance_first = load_artifact(
                    str(output / "provenance.json"),
                    AGENTIC_PROVENANCE_SCHEMA,
                )
                manifest_first = load_artifact(
                    str(output / "controller.manifest.json"),
                    AGENTIC_CONTROLLER_MANIFEST_SCHEMA,
                )
                lineage_first = load_artifact(
                    str(output / "verified-lineage.json"),
                    AGENTIC_LINEAGE_SCHEMA,
                )

                report = run_agentic_server_ab(*arguments, mode="smoke")
                provenance_second = load_artifact(
                    str(output / "provenance.json"),
                    AGENTIC_PROVENANCE_SCHEMA,
                )
                manifest_second = load_artifact(
                    str(output / "controller.manifest.json"),
                    AGENTIC_CONTROLLER_MANIFEST_SCHEMA,
                )
                lineage_second = load_artifact(
                    str(output / "verified-lineage.json"),
                    AGENTIC_LINEAGE_SCHEMA,
                )
                answers = load_artifact(
                    str(output / "agentic.answers.json"),
                    AGENTIC_ANSWERS_SCHEMA,
                )
                report_path = output / "controller.report.json"
                final_report_digest = report["artifact_digest"]
                final_report_mtime = report_path.stat().st_mtime_ns

                reused_report = run_agentic_server_ab(*arguments, mode="smoke")
                with self.assertRaisesRegex(RuntimeError, "preserved"):
                    run_agentic_server_ab(
                        *arguments,
                        mode="smoke",
                        request_timeout=42,
                    )
                checkpoint_path = output / "agentic.checkpoint.jsonl"
                checkpoint_bytes = checkpoint_path.read_bytes()
                checkpoint_path.unlink()
                with self.assertRaisesRegex(RuntimeError, "preserved"):
                    run_agentic_server_ab(*arguments, mode="smoke")
                checkpoint_path.write_bytes(checkpoint_bytes + b"corrupt")
                with self.assertRaisesRegex(RuntimeError, "preserved"):
                    run_agentic_server_ab(*arguments, mode="smoke")

            self.assertEqual(
                provenance_first["artifact_digest"],
                provenance_second["artifact_digest"],
            )
            self.assertEqual(
                provenance_first["captured_at"],
                provenance_second["captured_at"],
            )
            self.assertEqual(
                manifest_first["artifact_digest"],
                manifest_second["artifact_digest"],
            )
            self.assertEqual(
                manifest_first["created_at"],
                manifest_second["created_at"],
            )
            self.assertEqual(
                lineage_first["artifact_digest"],
                lineage_second["artifact_digest"],
            )
            self.assertEqual(
                lineage_first["verified_at"],
                lineage_second["verified_at"],
            )
            self.assertEqual(1, len(interrupted.posts))
            self.assertEqual(59, len(resumed.posts))
            statuses = [
                execution["result"]["status"]
                for execution in answers["executions"]
            ]
            self.assertEqual(1, statuses.count("indeterminate"))
            self.assertEqual("valid", report["status"])
            self.assertEqual(final_report_digest, reused_report["artifact_digest"])
            self.assertEqual(final_report_mtime, report_path.stat().st_mtime_ns)
            self.assertEqual(2, build_service.call_count)
            self.assertEqual(4, start_service.call_count)
            self.assertEqual(2, controlled_run.call_count)


if __name__ == "__main__":
    unittest.main()
