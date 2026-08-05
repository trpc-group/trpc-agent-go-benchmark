#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for guarded Contextual Retrieval server orchestration."""

import tempfile
import unittest
from pathlib import Path
from unittest import mock

from contextual_retrieval.artifacts import (
    public_endpoint_identity,
    text_digest,
    write_artifact,
)
from contextual_retrieval.controller import (
    CONTROLLER_MANIFEST_SCHEMA,
    CONTROLLER_REPORT_SCHEMA,
    INDEX_STATE_SCHEMA,
    SERVICE_CONFIG_SCHEMA,
    _ensure_index,
    _index_identity,
    _load_promoted_smoke_lineage,
    _load_service_config_artifact,
    _sanitize_service_config,
    classify_source_compatibility,
    decide_index_action,
    run_server_formal,
    validate_table_name,
    validate_reused_index,
)
from contextual_retrieval.dataset import CASE_SCHEMA, CHUNK_SCHEMA
from contextual_retrieval.runner import (
    RETRIEVAL_MANIFEST_SCHEMA,
    RETRIEVAL_REPORT_SCHEMA,
)


class ContextualRetrievalControllerTest(unittest.TestCase):
    def _config(self, count=0):
        return {
            "pg_table": "fixture_lane_a_docs",
            "index_variant": "baseline",
            "embedding_model": "bge-m3",
            "embedding_endpoint": public_endpoint_identity(
                "https://embedding.test/v1"
            ),
            "llm_endpoint": public_endpoint_identity(
                "https://llm.test/v1"
            ),
            "embedding_dimensions": 1024,
            "embedding_header_names": [],
            "vectorstore": "pgvector",
            "search_mode": 1,
            "use_rrf": False,
            "hybrid_vector_weight": 0.99999,
            "hybrid_text_weight": 0.00001,
            "chunk_size": 500,
            "chunk_overlap": 50,
            "framework_module": {"path": "tag", "version": "devel"},
            "chunk_manifest_digest": "chunks",
            "parent_manifest_digest": "parents",
            "manifest_chunks_count": 2,
            "context_cache_identity": None,
            "context_set_digest": None,
            "index_document_count": count,
        }

    def test_table_name_rejects_sql_or_qualified_names(self):
        self.assertEqual(
            "fixture_lane_a_docs",
            validate_table_name("fixture_lane_a_docs"),
        )
        for invalid in ("public.table", "table;drop", "quoted-name", "1table"):
            with self.subTest(invalid=invalid):
                with self.assertRaises(ValueError):
                    validate_table_name(invalid)

    def test_service_config_sanitizes_public_endpoint_fields(self):
        config = self._config()
        config["embedding_endpoint"] = (
            "https://user:password@embedding.test/private/v1"
            "?token=value#fragment"
        )
        config["llm_endpoint"] = "https://llm.test/private/chat"
        config["pg_connection"] = {
            "host": "internal-db-host",
            "user": "internal-db-user",
            "database": "internal-db-name",
        }
        sanitized = _sanitize_service_config(config)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "service-config.json"
            write_artifact(
                str(path),
                {
                    "schema_version": SERVICE_CONFIG_SCHEMA,
                    **sanitized,
                },
            )
            serialized = path.read_text(encoding="utf-8")
        self.assertEqual(
            "https://embedding.test|path_sha256="
            + text_digest("/private/v1"),
            sanitized["embedding_endpoint"],
        )
        self.assertEqual(
            "https://llm.test|path_sha256="
            + text_digest("/private/chat"),
            sanitized["llm_endpoint"],
        )
        for secret in (
            "user",
            "password",
            "private",
            "token=value",
            "internal-db-host",
            "internal-db-name",
        ):
            self.assertNotIn(secret, serialized)
        self.assertIn("password", config["embedding_endpoint"])
        self.assertNotIn("pg_connection", sanitized)

    def test_service_config_rejects_invalid_endpoint(self):
        config = self._config()
        config["embedding_endpoint"] = "unix:///var/run/service.sock"
        with self.assertRaisesRegex(ValueError, "valid HTTP"):
            _sanitize_service_config(config)

    def test_service_config_rejects_unknown_fields(self):
        for field, value in (
            ("reranker_enabled", True),
            ("api_key", "unexpected-api-key"),
        ):
            with self.subTest(field=field):
                config = self._config()
                config[field] = value
                with self.assertRaisesRegex(ValueError, "unsupported fields"):
                    _sanitize_service_config(config)

    def test_service_config_artifact_rejects_non_public_content(self):
        cases = (
            ("reranker_enabled", True, "unsupported fields"),
            ("api_key", "unexpected-api-key", "unsupported fields"),
            (
                "pg_connection",
                {"host": "internal-db-host"},
                "not canonical and public",
            ),
            (
                "embedding_endpoint",
                "https://embedding.test/private/v1",
                "not canonical and public",
            ),
        )
        for field, value, error in cases:
            with self.subTest(field=field):
                with tempfile.TemporaryDirectory() as directory:
                    path = Path(directory) / "service-config.json"
                    write_artifact(
                        str(path),
                        {
                            "schema_version": SERVICE_CONFIG_SCHEMA,
                            **self._config(),
                            field: value,
                        },
                    )
                    with self.assertRaisesRegex(ValueError, error):
                        _load_service_config_artifact(path)

    def test_partial_index_requires_explicit_resume(self):
        identity = _index_identity(self._config())
        state = {"status": "building", "identity": identity}
        with self.assertRaisesRegex(ValueError, "--resume-indexes"):
            decide_index_action(1, 2, state, identity, False)
        self.assertEqual(
            "resume_load",
            decide_index_action(1, 2, state, identity, True),
        )

    def test_non_empty_unknown_index_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "no matching controller state"):
            decide_index_action(1, 2, None, {}, False)

    def test_complete_index_can_only_be_reused_at_exact_count(self):
        identity = _index_identity(self._config())
        state = {"status": "complete", "identity": identity}
        self.assertEqual(
            "reuse_complete",
            decide_index_action(2, 2, state, identity, False),
        )
        with self.assertRaisesRegex(ValueError, "unexpected document count"):
            decide_index_action(1, 2, state, identity, False)

    def test_fresh_index_writes_building_load_and_complete_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            before = self._config(count=0)
            after = self._config(count=2)
            with mock.patch(
                "contextual_retrieval.controller._request_json",
                side_effect=[
                    {
                        "success": True,
                        "count": 2,
                        "elapsed_ms": 100,
                        "message": "loaded",
                    },
                    after,
                ],
            ), mock.patch(
                "contextual_retrieval.controller._table_storage_bytes",
                side_effect=[
                    {
                        "status": "available",
                        "pg_total_relation_size_bytes": 0,
                    },
                    {
                        "status": "available",
                        "pg_total_relation_size_bytes": 1024,
                    },
                ],
            ):
                state = _ensure_index(
                    "http://127.0.0.1:8765",
                    before,
                    root / "index-state.json",
                    root / "load-result.json",
                    load_timeout=10,
                    resume_indexes=False,
                    repository_snapshot={
                        "commit": "root-builder",
                        "worktree_dirty": False,
                    },
                    benchmark_repository_snapshot={
                        "commit": "benchmark-builder",
                        "worktree_dirty": False,
                    },
                )

            self.assertTrue((root / "load-result.json").exists())

        self.assertEqual("complete", state["status"])
        self.assertEqual("load_fresh", state["action"])
        self.assertEqual(2, state["count_after"])
        self.assertIsNotNone(state["load_result_digest"])
        self.assertEqual(
            "root-builder",
            state["builder_source"]["repository"]["commit"],
        )

    def test_source_compatibility_allows_only_control_plane_changes(self):
        compatible = classify_source_compatibility(
            ["benchmark"],
            [
                "knowledge/contextual_retrieval/controller.py",
                "knowledge/tests/test_contextual_retrieval_controller.py",
            ],
        )
        incompatible = classify_source_compatibility(
            ["knowledge/default.go"],
            ["knowledge/contextual_retrieval/runner.py"],
        )
        self.assertTrue(compatible["compatible"])
        self.assertFalse(incompatible["compatible"])
        self.assertEqual(
            ["knowledge/default.go"],
            incompatible["retrieval_sensitive_root_changes"],
        )
        self.assertEqual(
            ["knowledge/contextual_retrieval/runner.py"],
            incompatible["retrieval_sensitive_benchmark_changes"],
        )

    def test_formal_reuse_requires_complete_matching_index(self):
        config = self._config(count=2)
        chunks = {"artifact_digest": "chunks", "chunks_count": 2}
        complete = {
            "status": "complete",
            "identity": _index_identity(config),
            "builder_source": {
                "repository": {
                    "commit": "root-builder",
                    "worktree_dirty": False,
                },
                "benchmark_repository": {
                    "commit": "benchmark-builder",
                    "worktree_dirty": False,
                },
            },
            "expected_count": 2,
            "count_after": 2,
        }
        validate_reused_index(
            config,
            config,
            complete,
            "baseline",
            "fixture_lane_a_docs",
            chunks,
        )
        with self.assertRaisesRegex(ValueError, "index_state_status"):
            validate_reused_index(
                config,
                config,
                {**complete, "status": "building"},
                "baseline",
                "fixture_lane_a_docs",
                chunks,
            )

    def test_promoted_smoke_lineage_binds_all_sealed_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            smoke = root / "smoke"
            smoke.mkdir()
            chunks = write_artifact(
                str(root / "chunks.json"),
                {
                    "schema_version": CHUNK_SCHEMA,
                    "parent_manifest_digest": "parents",
                    "chunks_count": 2,
                    "chunks": [],
                },
            )
            cases = write_artifact(
                str(root / "cases.json"),
                {
                    "schema_version": CASE_SCHEMA,
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "cases_count": 3,
                    "cases": [],
                },
            )
            baseline_config_value = self._config(count=2)
            contextual_config_value = {
                **self._config(count=2),
                "pg_table": "fixture_lane_b_docs",
                "index_variant": "contextual",
                "context_cache_identity": "cache-id",
                "context_set_digest": "context-set-id",
            }
            for config in (baseline_config_value, contextual_config_value):
                config["chunk_manifest_digest"] = chunks["artifact_digest"]
            baseline_config = write_artifact(
                str(smoke / "baseline.config.json"),
                {
                    "schema_version": SERVICE_CONFIG_SCHEMA,
                    **baseline_config_value,
                },
            )
            contextual_config = write_artifact(
                str(smoke / "contextual.config.json"),
                {
                    "schema_version": SERVICE_CONFIG_SCHEMA,
                    **contextual_config_value,
                },
            )
            baseline_state = write_artifact(
                str(smoke / "baseline.index-state.json"),
                {
                    "schema_version": INDEX_STATE_SCHEMA,
                    "status": "complete",
                    "identity": _index_identity(baseline_config_value),
                    "builder_source": {
                        "repository": {
                            "commit": "root-smoke",
                            "tracked_dirty": False,
                            "untracked_dirty": False,
                            "worktree_dirty": False,
                        },
                        "benchmark_repository": {
                            "commit": "benchmark-smoke",
                            "tracked_dirty": False,
                            "untracked_dirty": False,
                            "worktree_dirty": False,
                        },
                    },
                    "expected_count": 2,
                    "count_after": 2,
                },
            )
            contextual_state = write_artifact(
                str(smoke / "contextual.index-state.json"),
                {
                    "schema_version": INDEX_STATE_SCHEMA,
                    "status": "complete",
                    "identity": _index_identity(contextual_config_value),
                    "builder_source": {
                        "repository": {
                            "commit": "root-smoke",
                            "tracked_dirty": False,
                            "untracked_dirty": False,
                            "worktree_dirty": False,
                        },
                        "benchmark_repository": {
                            "commit": "benchmark-smoke",
                            "tracked_dirty": False,
                            "untracked_dirty": False,
                            "worktree_dirty": False,
                        },
                    },
                    "expected_count": 2,
                    "count_after": 2,
                },
            )
            smoke_manifest = write_artifact(
                str(smoke / "smoke.manifest.json"),
                {
                    "schema_version": RETRIEVAL_MANIFEST_SCHEMA,
                    "case_manifest_digest": cases["artifact_digest"],
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "baseline_config": baseline_config_value,
                    "contextual_config": contextual_config_value,
                },
            )
            smoke_report = write_artifact(
                str(smoke / "smoke.json"),
                {
                    "schema_version": RETRIEVAL_REPORT_SCHEMA,
                    "manifest_digest": smoke_manifest["artifact_digest"],
                    "evidence_status": "valid",
                    "formal_ab_eligible": False,
                    "runtime_errors": 0,
                    "failed_request_attempts": 0,
                    "smoke_promotion": {
                        "decision": "promote",
                        "formal_method_conclusion": False,
                    },
                },
            )
            controller_manifest = write_artifact(
                str(smoke / "controller.manifest.json"),
                {
                    "schema_version": CONTROLLER_MANIFEST_SCHEMA,
                    "repository": {
                        "commit": "root-smoke",
                        "tracked_dirty": False,
                        "untracked_dirty": False,
                        "worktree_dirty": False,
                    },
                    "benchmark_repository": {
                        "commit": "benchmark-smoke",
                        "tracked_dirty": False,
                        "untracked_dirty": False,
                        "worktree_dirty": False,
                    },
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "case_manifest_digest": cases["artifact_digest"],
                    "context_cache_identity": "cache-id",
                    "context_set_digest": "context-set-id",
                    "baseline": {
                        "table": "fixture_lane_a_docs",
                        "port": 8765,
                    },
                    "contextual": {
                        "table": "fixture_lane_b_docs",
                        "port": 8766,
                    },
                    "baseline_only": False,
                    "agent_initialized": False,
                    "judge_initialized": False,
                },
            )
            write_artifact(
                str(smoke / "controller.report.json"),
                {
                    "schema_version": CONTROLLER_REPORT_SCHEMA,
                    "status": "valid",
                    "phase": "retrieval_smoke_completed",
                    "manifest_digest": controller_manifest["artifact_digest"],
                    "smoke_report_digest": smoke_report["artifact_digest"],
                    "baseline_config_digest": baseline_config[
                        "artifact_digest"
                    ],
                    "contextual_config_digest": contextual_config[
                        "artifact_digest"
                    ],
                    "baseline_index_state_digest": baseline_state[
                        "artifact_digest"
                    ],
                    "contextual_index_state_digest": contextual_state[
                        "artifact_digest"
                    ],
                },
            )
            context_summary = {
                "status": "valid",
                "cache_identity": "cache-id",
                "context_set_digest": "context-set-id",
                "expected_chunks": 2,
                "successful_chunks": 2,
                "error_chunks": 0,
                "missing_chunks": 0,
            }
            with mock.patch(
                "contextual_retrieval.controller._source_compatibility",
                return_value={"compatible": True},
            ):
                lineage = _load_promoted_smoke_lineage(
                    smoke,
                    chunks,
                    cases,
                    context_summary,
                    root,
                    root,
                )

        self.assertEqual("fixture_lane_a_docs", lineage["baseline_table"])
        self.assertEqual("fixture_lane_b_docs", lineage["contextual_table"])
        self.assertEqual(
            "promote",
            lineage["smoke_report"]["smoke_promotion"]["decision"],
        )

    def test_formal_controller_reuses_indexes_without_load(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks = write_artifact(
                str(root / "chunks.json"),
                {
                    "schema_version": CHUNK_SCHEMA,
                    "parent_manifest_digest": "parents",
                    "chunks_count": 2,
                    "chunks": [],
                },
            )
            write_artifact(
                str(root / "cases.json"),
                {
                    "schema_version": CASE_SCHEMA,
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "cases_count": 3,
                    "cases": [],
                },
            )
            baseline = self._config(count=2)
            contextual = {
                **self._config(count=2),
                "pg_table": "fixture_lane_b_docs",
                "index_variant": "contextual",
                "context_cache_identity": "cache-id",
                "context_set_digest": "context-set-id",
            }
            baseline["chunk_manifest_digest"] = chunks["artifact_digest"]
            contextual["chunk_manifest_digest"] = chunks["artifact_digest"]
            baseline_state = {
                "artifact_digest": "baseline-index",
                "status": "complete",
                "identity": _index_identity(baseline),
                "expected_count": 2,
                "count_after": 2,
            }
            contextual_state = {
                "artifact_digest": "contextual-index",
                "status": "complete",
                "identity": _index_identity(contextual),
                "expected_count": 2,
                "count_after": 2,
            }
            lineage = {
                "controller_manifest": {"artifact_digest": "smoke-controller"},
                "controller_report": {"artifact_digest": "smoke-report"},
                "smoke_report": {"artifact_digest": "promoted-smoke"},
                "baseline_config": baseline,
                "contextual_config": contextual,
                "baseline_state": baseline_state,
                "contextual_state": contextual_state,
                "baseline_table": "fixture_lane_a_docs",
                "contextual_table": "fixture_lane_b_docs",
                "baseline_port": 8765,
                "contextual_port": 8766,
                "source_compatibility": {
                    "compatible": True,
                    "current_repository": {
                        "commit": "root-current",
                        "branch": "feat",
                        "tracked_dirty": False,
                    },
                    "current_benchmark_repository": {
                        "commit": "benchmark-current",
                        "branch": "",
                        "tracked_dirty": False,
                    },
                },
            }
            conformance = {
                "artifact_digest": "conformance",
                "smoke_promotion": {"decision": "promote"},
            }
            formal = {
                "artifact_digest": "formal",
                "evidence_status": "valid",
                "formal_ab_eligible": True,
                "gate": {"decision": "pass"},
            }
            handles = [mock.Mock(), mock.Mock()]
            processes = [mock.Mock(), mock.Mock()]
            with mock.patch(
                "contextual_retrieval.controller.summarize_context_cache",
                return_value={
                    "status": "valid",
                    "cache_identity": "cache-id",
                    "context_set_digest": "context-set-id",
                },
            ), mock.patch(
                "contextual_retrieval.controller._load_promoted_smoke_lineage",
                return_value=lineage,
            ), mock.patch(
                "contextual_retrieval.controller._port_is_available",
                return_value=True,
            ), mock.patch(
                "contextual_retrieval.controller._build_service",
                return_value=(root / "service", root / "build.log"),
            ), mock.patch(
                "contextual_retrieval.controller._start_service",
                side_effect=[
                    (processes[0], handles[0], root / "baseline.log"),
                    (processes[1], handles[1], root / "contextual.log"),
                ],
            ), mock.patch(
                "contextual_retrieval.controller._wait_for_health",
            ), mock.patch(
                "contextual_retrieval.controller._request_json",
                side_effect=[baseline, contextual],
            ), mock.patch(
                "contextual_retrieval.controller.validate_reused_index",
            ) as validate_index, mock.patch(
                "contextual_retrieval.controller._ensure_index",
            ) as ensure_index, mock.patch(
                "contextual_retrieval.controller.run_retrieval_ab",
                side_effect=[conformance, formal],
            ) as run_ab, mock.patch(
                "contextual_retrieval.controller._stop_owned_process",
                return_value={"owned": True, "stopped": True},
            ):
                report = run_server_formal(
                    str(root),
                    str(root / "chunks.json"),
                    str(root / "cases.json"),
                    str(root / "contexts.jsonl"),
                    str(root / "smoke"),
                    str(root / "formal"),
                    bootstrap_resamples=20,
                )

        self.assertEqual("retrieval_formal_completed", report["phase"])
        self.assertTrue(report["formal_ab_eligible"])
        self.assertFalse(report["load_endpoint_called"])
        self.assertEqual(2, validate_index.call_count)
        ensure_index.assert_not_called()
        self.assertEqual(2, run_ab.call_count)
        for handle in handles:
            handle.close.assert_called_once()


if __name__ == "__main__":
    unittest.main()
