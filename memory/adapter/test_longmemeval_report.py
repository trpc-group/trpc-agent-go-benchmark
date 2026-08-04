#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent. All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
"""Tests for maintained LongMemEval report validation."""

from __future__ import annotations

import gzip
import hashlib
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from memory.adapter import longmemeval_report as report
from memory.adapter import longmemeval_validation as validation


class LongMemEvalReportTest(unittest.TestCase):
    def test_turn_pair_validator_rejects_unsupported_role(self) -> None:
        turns = [
            {
                "turn_index": 0,
                "turn_id": "a1",
                "role": "tool",
                "content": "unsupported payload",
            }
        ]
        with self.assertRaisesRegex(ValueError, "turn identity"):
            validation._turn_pair_groups(turns)

    def test_turn_pair_validator_rejects_relabeled_session_batch(self) -> None:
        replay_case = {
            "version": 2,
            "case_id": "case-1",
            "sessions": [{
                "session_index": 0,
                "session_id": "session-1",
                "observation_time": "2026-01-01T00:00:00Z",
                "turns": [
                    {"turn_index": 0, "turn_id": "u1", "role": "user", "content": "u1"},
                    {"turn_index": 1, "turn_id": "a1", "role": "assistant", "content": "a1"},
                    {"turn_index": 2, "turn_id": "u2", "role": "user", "content": "u2"},
                ],
            }],
        }
        build_case = {
            "version": 5,
            "case_id": "case-1",
            "sessions": [{
                "session_index": 0,
                "session_id": "session-1",
                "observation_time": "2026-01-01T00:00:00Z",
                "pairs": [{
                    "pair_id": "pair-1",
                    "source_turn_ids": ["u1", "a1", "u2"],
                }],
            }],
        }
        with self.assertRaisesRegex(ValueError, "turn-pair"):
            validation._validate_turn_pair_case(replay_case, build_case, 4096)

    def test_turn_pair_validator_rejects_truncated_content(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            result_path = self._write_fixture(Path(temp_dir))
            shared_root = result_path.parent.parent
            replay_case = json.loads(
                (shared_root / "replay" / "case-1.json").read_text(
                    encoding="utf-8"
                )
            )
            build_case = json.loads(
                (
                    shared_root
                    / "build-plans"
                    / "fixture"
                    / "case-1.json"
                ).read_text(encoding="utf-8")
            )
            part = build_case["sessions"][0]["pairs"][0]["chunks"][0]["turns"][0]
            part["content"] = "fixture"
            part["end_byte"] = 7
            with self.assertRaisesRegex(
                ValueError,
                "accounting|lossless|reconstructed",
            ):
                validation._validate_turn_pair_case(
                    replay_case,
                    build_case,
                    4096,
                )

    def test_trace_sources_must_match_build_plan(self) -> None:
        expected = [{"source_id": "expected"}]
        with self.assertRaisesRegex(ValueError, "differs"):
            validation._validate_trace_sources(
                [{"source_id": "different"}],
                expected,
                "success",
            )
        validation._validate_trace_sources([], expected, "build_error")

    def test_result_loader_rejects_oversized_file_before_json_decode(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "results.json"
            with path.open("wb") as stream:
                stream.truncate((64 << 20) + 1)
            with self.assertRaisesRegex(ValueError, "bounded"):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_trace_outcome_without_gold_requires_terminal_error(self) -> None:
        validation._validate_outcome_without_gold({
            "failure_stage": "build_error",
            "correct": False,
            "error": "build failed",
        })
        with self.assertRaisesRegex(ValueError, "precedes gold"):
            validation._validate_outcome_without_gold({
                "failure_stage": "retrieval_miss",
                "correct": False,
                "error": "retrieval failed",
            })

    def test_load_and_render_maintained_result(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            view = report.load_result(
                path,
                report.ResultSpec("Auto", "auto/results.json", "internal"),
                1,
                "single-session-user",
            )
            self.assertEqual("maintained", view.classification)
            english = report.render([view], zh=False)
            self.assertIn("Overall Results", english)
            self.assertIn("Memory Build Audit", english)
            self.assertIn("| Auto | turn-pair | 1 | 2 | 1 | 1 |", english)
            self.assertIn("extraction custom instructions", english)
            self.assertIn("总体结果", report.render([view], zh=True))

    def test_comparison_rejects_methodology_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as first_dir, tempfile.TemporaryDirectory() as second_dir:
            first = report.load_result(
                self._write_fixture(Path(first_dir)),
                report.ResultSpec("Auto", "auto/results.json", "internal"),
                1,
                "single-session-user",
            )
            second = report.load_result(
                self._write_fixture(Path(second_dir)),
                report.ResultSpec("Mem0", "mem0/results.json", "external"),
                1,
                "single-session-user",
            )
            second.methodology_identity = dict(second.methodology_identity or {})
            second.methodology_identity["tokenizer_name"] = "different-tokenizer"
            with self.assertRaisesRegex(ValueError, "methodology mismatch"):
                report.validate_comparable_case_sets([first, second])

    def test_result_loader_rejects_metadata_only_mem0_temporal_context(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            manifest_path = path.parent / "run_manifest.json"
            run_manifest = json.loads(
                manifest_path.read_text(encoding="utf-8")
            )
            run_manifest["run"]["scenario"] = "mem0_oss"
            run_manifest["run"]["backend"] = "mem0_oss"
            run_manifest["run"].pop("auto_update_policy", None)
            run_manifest["run"]["temporal_context"] = (
                "storage_metadata_only"
            )
            run_manifest["run"]["backend_revision"] = "a" * 40
            blockers = validation._derive_manifest_blockers(run_manifest)
            self.assertTrue(
                any("temporal_context" in item for item in blockers),
                blockers,
            )

    def test_memory_build_identity_rejects_temporal_mismatch(self) -> None:
        manifest = {
            "run": {
                "scenario": "mem0_oss",
                "build_protocol": "turn-pair",
                "temporal_context": "custom_prompt_reference_date",
            },
            "config": {
                "temporal_reference_source": (
                    "build_plan_session_observation_time"
                ),
                "temporal_reference_format": "YYYY-MM-DD",
                "mem0_preflight_digest": "sha256:preflight",
                "mem0_environment_lock_digest": "sha256:environment",
            },
        }
        metadata = {
            "memory_build": {
                "protocol": "turn-pair",
                "temporal_context": "storage_metadata_only",
                "temporal_reference_source": (
                    "build_plan_session_observation_time"
                ),
                "temporal_reference_format": "YYYY-MM-DD",
                "custom_extraction_prompt": True,
                "observation_prompt_verified": True,
                "preflight_digest": "sha256:other",
                "environment_lock_digest": "sha256:environment",
            }
        }
        blockers = validation._validate_memory_build_identity(
            manifest,
            metadata,
        )
        self.assertTrue(
            any("temporal context" in item for item in blockers),
            blockers,
        )
        self.assertTrue(
            any("preflight_digest" in item for item in blockers),
            blockers,
        )

    def test_diagnostic_result_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            raw = json.loads(path.read_text(encoding="utf-8"))
            raw.pop("publication")
            raw["metadata"]["recovered_from_logs"] = True
            path.write_text(json.dumps(raw), encoding="utf-8")
            with self.assertRaisesRegex(
                report.ResultEligibilityError,
                "diagnostic",
            ):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_tokens_known_must_be_explicit(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            raw = json.loads(path.read_text(encoding="utf-8"))
            del raw["cost"]["llm"]["qa"]["tokens_known"]
            path.write_text(json.dumps(raw), encoding="utf-8")
            with self.assertRaisesRegex(
                report.ResultEligibilityError,
                "tokens_known",
            ):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_missing_bad_case_artifact_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            (path.parent / "bad_cases.md").unlink()
            with self.assertRaisesRegex(
                report.ResultEligibilityError,
                "bad_cases_en",
            ):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_run_compatibility_digest_is_recomputed(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            manifest_path = path.parent / "run_manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["config"]["max_tasks"] = 2
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(
                report.ResultEligibilityError,
                "compatibility digest is invalid",
            ):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_result_must_match_aggregate(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            raw = json.loads(path.read_text(encoding="utf-8"))
            raw["summary"]["successful_cases"] = 0
            path.write_text(json.dumps(raw), encoding="utf-8")
            with self.assertRaisesRegex(
                report.ResultEligibilityError,
                "aggregate artifact does not match",
            ):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_invalid_machine_artifact_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = self._write_fixture(Path(temp_dir))
            raw = json.loads(path.read_text(encoding="utf-8"))
            aggregate = path.parent / "aggregate.json"
            aggregate.write_text("{", encoding="utf-8")
            raw["publication"]["artifacts"]["aggregate"]["sha256"] = (
                report._digest_path(aggregate)
            )
            path.write_text(json.dumps(raw), encoding="utf-8")
            with self.assertRaisesRegex(
                report.ResultEligibilityError,
                "artifact content is invalid",
            ):
                report.load_result(
                    path,
                    report.ResultSpec("Auto", "auto/results.json", "internal"),
                    1,
                    "single-session-user",
                )

    def test_trace_decoded_byte_limit_applies_to_jsonl_and_gzip(self) -> None:
        payload = b'{"oversized":"' + (b"x" * 64) + b'"}\n'
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            for gzip_enabled in (False, True):
                with self.subTest(gzip=gzip_enabled):
                    suffix = ".jsonl.gz" if gzip_enabled else ".jsonl"
                    path = root / ("trace" + suffix)
                    if gzip_enabled:
                        path.write_bytes(gzip.compress(payload))
                    else:
                        path.write_bytes(payload)
                    with mock.patch.object(
                        validation,
                        "_TRACE_MAX_DECODED_BYTES",
                        32,
                    ):
                        with self.assertRaisesRegex(
                            ValueError,
                            "decoded size limit",
                        ):
                            validation._read_selected_trace(
                                path,
                                "case-1",
                                "hash",
                            )

    def test_trace_record_limit_applies_to_jsonl_and_gzip(self) -> None:
        record = {
            "schema_version": "longmemeval.build_trace/v4",
            "sequence": 1,
            "recorded_at": "2026-01-01T00:00:00Z",
            "case_id": "case-1",
            "content_mode": "hash",
            "event": "gold_join",
            "gold": {
                "answer_session_ids": [],
                "joined_after_qa": True,
            },
        }
        line = (json.dumps(record, separators=(",", ":")) + "\n").encode()
        payload = line + line
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            for gzip_enabled in (False, True):
                with self.subTest(gzip=gzip_enabled):
                    suffix = ".jsonl.gz" if gzip_enabled else ".jsonl"
                    path = root / ("trace" + suffix)
                    if gzip_enabled:
                        path.write_bytes(gzip.compress(payload))
                    else:
                        path.write_bytes(payload)
                    with mock.patch.object(
                        validation,
                        "_TRACE_MAX_RECORDS",
                        1,
                    ):
                        with self.assertRaisesRegex(
                            ValueError,
                            "record limit",
                        ):
                            validation._read_selected_trace(
                                path,
                                "case-1",
                                "hash",
                            )

    def test_input_artifact_path_safety_rejects_traversal_and_symlinks(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            with self.assertRaisesRegex(ValueError, "escapes its base"):
                validation._resolve_input_locator(root, "../outside.json")

            target = root / "target"
            target.mkdir()
            (target / "artifact.json").write_text("{}\n", encoding="utf-8")
            link = root / "link"
            link.symlink_to(target, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                validation._resolve_input_locator(root, "link/artifact.json")
            with self.assertRaisesRegex(ValueError, "symbolic-link"):
                validation._digest_input_artifact(link)

    @unittest.skipUnless(hasattr(os, "mkfifo"), "requires os.mkfifo")
    def test_input_artifact_rejects_non_regular_files(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "artifact.fifo"
            os.mkfifo(path)
            for digest in (
                validation._digest_input_artifact,
                validation._digest_path,
            ):
                with self.subTest(digest=digest.__name__):
                    with self.assertRaisesRegex(
                        ValueError,
                        "regular file or directory",
                    ):
                        digest(path)

    def test_atomic_report_failure_preserves_destination(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "REPORT.md"
            path.write_text("old", encoding="utf-8")
            with mock.patch.object(
                report.os,
                "replace",
                side_effect=OSError("injected replace failure"),
            ):
                with self.assertRaisesRegex(OSError, "injected"):
                    report._atomic_write_text(path, "new")
            self.assertEqual("old", path.read_text(encoding="utf-8"))

    def _write_fixture(self, root: Path) -> Path:
        scenario_dir = root / "auto"
        scenario_dir.mkdir(parents=True)
        case_ids = ["case-1"]
        denominator = {
            "total_cases": 1,
            "case_ids": case_ids,
            "digest": validation._go_json_digest(case_ids),
        }
        bucket = {
            "calls": 0,
            "requests": 0,
            "cache_hits": 0,
            "prompt_tokens": 0,
            "completion_tokens": 0,
            "total_tokens": 0,
            "cached_tokens": 0,
            "tokens_known": True,
        }
        summary = {
            "total_cases": 1,
            "completed_cases": 1,
            "successful_cases": 1,
            "failed_cases": 0,
            "judge_failed_cases": 0,
            "overall": {"accuracy": 1.0},
        }
        by_type = {
            "single-session-user": {
                "count": 1,
                "metrics": {"accuracy": 1.0},
            },
        }
        cases = [
            {
                "question_id": "case-1",
                "question_type": "single-session-user",
                "status": "succeeded",
                "correct": True,
                "failure_stage": "success",
                "build_observability": "snapshot_diff",
            },
        ]
        manifest_artifacts = self._write_manifest_inputs(scenario_dir)
        replay_index = json.loads(
            (scenario_dir.parent / "replay" / "index.json").read_text(
                encoding="utf-8"
            )
        )
        build_index = json.loads(
            (
                scenario_dir.parent
                / "build-plans"
                / "fixture"
                / "index.json"
            ).read_text(encoding="utf-8")
        )
        manifest_config = {
            "question_types": ["single-session-user"],
            "max_tasks": 1,
            "retrieval_top_k": 20,
            "build_max_tokens": 4096,
            "build_tokenizer": "test-tokenizer",
            "build_tokenizer_model": "test-model",
            "build_tokenizer_encoding": "",
            "build_stats": {
                "case_count": 1,
                "session_count": 1,
                "turn_count": 2,
                "pair_count": 1,
                "chunk_count": 1,
                "chunked_session_count": 0,
                "chunked_pair_count": 0,
                "split_turn_count": 0,
                "original_tokens": 12,
                "final_tokens": 12,
                "original_bytes": 12,
                "final_bytes": 12,
                "max_original_turn_tokens": 8,
                "max_original_pair_tokens": 12,
                "max_session_tokens": 12,
                "max_chunk_tokens": 12,
            },
            "replay_digest": replay_index["replay_digest"],
            "build_plan_digest": build_index["build_plan_digest"],
            "max_retries": 2,
            "answer_max_tokens": 256,
            "judge_max_tokens": 128,
            "transport_retry_enabled": True,
            "transport_retry_strategy": "exponential",
            "temporal_reference_source": (
                "build_plan_session_observation_time"
            ),
            "temporal_reference_format": "YYYY-MM-DD",
            "auto_qa_only": False,
            "trace_content_mode": "hash",
            "trace_gzip": False,
        }
        run_manifest = {
            "schema_version": 5,
            "created_at": "2026-01-01T00:00:00Z",
            "reproducible": True,
            "official_status": "eligible",
            "code": {
                "go_version": "go1.24",
                "benchmark": {
                    "revision": "abc123",
                    "dirty_state": "clean",
                    "source": "git",
                },
                "trpc_agent_go_root_revision": "root-abc123",
                "trpc_agent_go_modules": [
                    {
                        "path": "trpc.group/trpc-go/trpc-agent-go",
                        "effective_path": "trpc.group/trpc-go/trpc-agent-go",
                        "effective_version": "v0.0.0-test",
                        "revision": "module-abc123",
                        "checksum": "h1:test",
                        "resolved": True,
                    },
                ],
            },
            "artifacts": manifest_artifacts,
            "case_ids": case_ids,
            "run": {
                "scenario": "auto",
                "backend": "pgvector",
                "auto_update_policy": "merge_similar",
                "temporal_context": "extractor_reference_date",
                "model_name": "test-model",
                "embed_model_name": "test-embedding",
                "llm_endpoint_fingerprint": "provider-default",
                "embedding_endpoint_fingerprint": "provider-default",
                "tokenizer_name": "test-tokenizer",
                "effective_top_k": 20,
                "build_protocol": "turn-pair",
                "case_manifest_schema_version": 1,
                "case_manifest_method": "full-category",
                "case_manifest_legacy": False,
            },
            "config": manifest_config,
        }
        compatibility = report._run_compatibility_digest(run_manifest)
        comparison = report._run_comparison_digest(run_manifest)
        run_manifest["compatibility_digest"] = compatibility
        run_manifest["comparison_digest"] = comparison
        result = {
            "metadata": {
                "framework": "trpc-agent-go",
                "dataset_format": "longmemeval",
                "scenario": "auto",
                "memory_backend": "pgvector",
                "fairly_comparable": True,
                "comparison_status": "comparable",
                "comparison_limitations": [],
                "memory_build": {
                    "status": "completed",
                    "protocol": "turn-pair",
                    "total_sessions_ingested": 1,
                    "temporal_context": "extractor_reference_date",
                    "temporal_reference_source": (
                        "build_plan_session_observation_time"
                    ),
                    "temporal_reference_format": "YYYY-MM-DD",
                    **manifest_config["build_stats"],
                },
                "run_manifest_version": 5,
                "run_compatibility_digest": compatibility,
                "run_comparison_digest": comparison,
                "official_status": "eligible",
                "config": {
                    "model_name": "test-model",
                    "embed_model_name": "test-embedding",
                    "retrieval_top_k": 20,
                    "trace_content_mode": "hash",
                },
            },
            "summary": summary,
            "by_type": by_type,
            "cases": cases,
            "cost": {
                "llm": {
                    "total": dict(bucket),
                    "memory_build": dict(bucket),
                    "qa": dict(bucket),
                    "judge": dict(bucket),
                },
                "embedding": {
                    "total": dict(bucket),
                    "memory_build": dict(bucket),
                    "qa_retrieval": dict(bucket),
                },
            },
            "publication": {
                "schema_version": 2,
                "classification": "maintained",
                "origin": "native_runner",
                "eligible": True,
                "finalized_at": "2026-01-01T00:00:00Z",
                "run_manifest": {
                    "schema_version": 5,
                    "compatibility_digest": compatibility,
                    "comparison_digest": comparison,
                },
                "fixed_denominator": denominator,
            },
        }

        aggregate = {
            "schema_version": 2,
            "classification": "maintained",
            "scenario": "auto",
            "backend": "pgvector",
            "run_compatibility_digest": compatibility,
            "comparison_digest": comparison,
            "fixed_denominator": denominator,
            "summary": summary,
            "by_type": by_type,
            "cases": [
                {
                    **cases[0],
                    "failure_stage": "success",
                },
            ],
        }
        bad_cases = {
            "schema_version": 2,
            "classification": "maintained",
            "scenario": "auto",
            "backend": "pgvector",
            "run_compatibility_digest": compatibility,
            "comparison_digest": comparison,
            "fixed_denominator": denominator,
            "cases": [],
        }
        artifacts: dict[str, dict[str, object]] = {}
        for key, name, content in (
            ("aggregate", "aggregate.json", json.dumps(aggregate) + "\n"),
            ("bad_cases", "bad_cases.json", json.dumps(bad_cases) + "\n"),
            ("bad_cases_en", "bad_cases.md", "# Bad cases\n"),
            ("bad_cases_zh_cn", "bad_cases.zh_CN.md", "# 失败样本\n"),
        ):
            artifact_path = scenario_dir / name
            artifact_path.write_text(content, encoding="utf-8")
            artifacts[key] = {
                "path": name,
                "sha256": report._digest_path(artifact_path),
            }
        artifacts["build_trace"] = self._write_trace_selection(scenario_dir)
        result["publication"]["artifacts"] = artifacts
        path = scenario_dir / "results.json"
        path.write_text(json.dumps(result), encoding="utf-8")
        (scenario_dir / "run_manifest.json").write_text(
            json.dumps(run_manifest),
            encoding="utf-8",
        )
        return path

    def _write_manifest_inputs(
        self,
        scenario_dir: Path,
    ) -> dict[str, dict[str, object]]:
        shared_root = scenario_dir.parent
        source_dir = shared_root / "source"
        source_dir.mkdir()
        dataset = source_dir / "dataset.json"
        case_manifest = source_dir / "case_manifest.json"
        dataset.write_text('{"question_id":"case-1"}\n', encoding="utf-8")
        case_manifest.write_text('["case-1"]\n', encoding="utf-8")

        dataset_digest = validation._digest_input_artifact(dataset)
        manifest_digest = validation._digest_input_artifact(case_manifest)
        replay_digest = "1" * 64
        build_plan_digest = "2" * 64
        replay_root = shared_root / "replay"
        replay_root.mkdir()
        (replay_root / "index.json").write_text(
            json.dumps({
                "version": 2,
                "dataset_digest": dataset_digest.removeprefix("sha256:"),
                "manifest_digest": manifest_digest.removeprefix("sha256:"),
                "selection_digest": "3" * 64,
                "replay_digest": replay_digest,
                "cases": [{"case_id": "case-1", "file": "case-1.json"}],
            }),
            encoding="utf-8",
        )
        replay_case = {
            "version": 2,
            "case_id": "case-1",
            "sessions": [{
                "session_index": 0,
                "session_id": "session-1",
                "observation_time": "2026-01-01T00:00:00Z",
                "turns": [
                    {
                        "turn_index": 0,
                        "turn_id": "turn-1",
                        "role": "user",
                        "content": "fixture ",
                    },
                    {
                        "turn_index": 1,
                        "turn_id": "turn-2",
                        "role": "assistant",
                        "content": "text",
                    },
                ],
            }],
        }
        (replay_root / "case-1.json").write_text(
            json.dumps(replay_case) + "\n",
            encoding="utf-8",
        )

        build_root = shared_root / "build-plans" / "fixture"
        build_root.mkdir(parents=True)
        (build_root / "index.json").write_text(
            json.dumps({
                "version": 5,
                "protocol": "turn-pair",
                "config": {
                    "tokenizer": "test-tokenizer",
                    "model": "test-model",
                    "max_tokens": 4096,
                    "replay_digest": replay_digest,
                },
                "config_digest": "4" * 64,
                "build_plan_digest": build_plan_digest,
                "stats": {
                    "case_count": 1,
                    "session_count": 1,
                    "turn_count": 2,
                    "pair_count": 1,
                    "chunk_count": 1,
                    "chunked_session_count": 0,
                    "chunked_pair_count": 0,
                    "split_turn_count": 0,
                    "original_tokens": 12,
                    "final_tokens": 12,
                    "original_bytes": 12,
                    "final_bytes": 12,
                    "max_original_turn_tokens": 8,
                    "max_original_pair_tokens": 12,
                    "max_session_tokens": 12,
                    "max_chunk_tokens": 12,
                },
                "cases": [{"case_id": "case-1", "file": "case-1.json"}],
            }),
            encoding="utf-8",
        )
        stats = {
            "case_count": 1,
            "session_count": 1,
            "turn_count": 2,
            "pair_count": 1,
            "chunk_count": 1,
            "chunked_session_count": 0,
            "chunked_pair_count": 0,
            "split_turn_count": 0,
            "original_tokens": 12,
            "final_tokens": 12,
            "original_bytes": 12,
            "final_bytes": 12,
            "max_original_turn_tokens": 8,
            "max_original_pair_tokens": 12,
            "max_session_tokens": 12,
            "max_chunk_tokens": 12,
        }
        build_case = {
            "version": 5,
            "case_id": "case-1",
            "replay_digest": replay_digest,
            "config_digest": "4" * 64,
            "stats": stats,
            "sessions": [{
                "session_index": 0,
                "session_id": "session-1",
                "observation_time": "2026-01-01T00:00:00Z",
                "pairs": [{
                    "pair_id": "pair-1",
                    "source_turn_ids": ["turn-1", "turn-2"],
                    "original_tokens": 12,
                    "final_tokens": 12,
                    "original_bytes": 12,
                    "final_bytes": 12,
                    "chunks": [{
                        "chunk_id": "chunk-1",
                        "index": 0,
                        "token_count": 12,
                        "byte_count": 12,
                        "turns": [
                            {
                                "source_turn_id": "turn-1",
                                "source_turn_index": 0,
                                "role": "user",
                                "content": "fixture ",
                                "start_byte": 0,
                                "end_byte": 8,
                                "start_token": 0,
                                "end_token": 8,
                            },
                            {
                                "source_turn_id": "turn-2",
                                "source_turn_index": 1,
                                "role": "assistant",
                                "content": "text",
                                "start_byte": 0,
                                "end_byte": 4,
                                "start_token": 0,
                                "end_token": 4,
                            },
                        ],
                    }],
                }],
            }],
        }
        (build_root / "case-1.json").write_text(
            json.dumps(build_case) + "\n",
            encoding="utf-8",
        )

        return {
            "dataset": {
                "configured": True,
                "available": True,
                "digest": dataset_digest,
            },
            "case_manifest": {
                "configured": True,
                "available": True,
                "digest": manifest_digest,
            },
            "canonical_replay": {
                "configured": True,
                "available": True,
                "path": replay_root.relative_to(shared_root).as_posix(),
                "digest": validation._digest_input_artifact(replay_root),
            },
            "build_plan": {
                "configured": True,
                "available": True,
                "path": build_root.relative_to(shared_root).as_posix(),
                "digest": validation._digest_input_artifact(build_root),
            },
        }

    def _write_trace_selection(
        self,
        scenario_dir: Path,
    ) -> dict[str, object]:
        identity = {
            "case_id": "case-1",
            "session_id": "session-1",
            "turn_ids": ["turn-1", "turn-2"],
            "chunk_id": "chunk-1",
        }
        source = {
            "source_id": hashlib.sha256(
                json.dumps(identity, separators=(",", ":")).encode()
            ).hexdigest(),
            "session_id": "session-1",
            "runner_session_id": "session-1",
            "turn_ids": ["turn-1", "turn-2"],
            "chunk_id": "chunk-1",
            "observation_time": "2026-01-01T00:00:00Z",
        }
        hashed_text = {
            "sha256": hashlib.sha256(b"fixture text").hexdigest(),
            "bytes": len(b"fixture text"),
        }
        records = [
            {
                "event": "extraction",
                "source": source,
                "extraction": {
                    "input": hashed_text,
                    "operation_count": 0,
                    "effective_operations": "unavailable",
                    "unavailable_reason": "backend does not expose operations",
                },
            },
            {
                "event": "persistence",
                "source": source,
                "persistence": {
                    "acknowledged": True,
                    "diff": {"unchanged": 0},
                    "actual_operations": "unavailable",
                    "unavailable_reason": "backend does not expose operations",
                },
            },
            {
                "event": "retrieval",
                "retrieval": {
                    "step": 0,
                    "query": hashed_text,
                },
            },
            {
                "event": "gold_join",
                "gold": {
                    "answer_session_ids": ["session-1"],
                    "joined_after_qa": True,
                },
            },
            {
                "event": "outcome",
                "outcome": {
                    "failure_stage": "success",
                    "build_observability": "snapshot_diff",
                    "correct": True,
                },
            },
        ]
        encoded_records: list[bytes] = []
        for sequence, record in enumerate(records, 1):
            value = {
                "schema_version": "longmemeval.build_trace/v4",
                "sequence": sequence,
                "recorded_at": "2026-01-01T00:00:00Z",
                "case_id": "case-1",
                "content_mode": "hash",
                **record,
            }
            encoded_records.append(
                (json.dumps(value, separators=(",", ":")) + "\n").encode()
            )
        trace_data = b"".join(encoded_records)
        trace_file = validation._trace_file_name("case-1") + ".jsonl"
        trace_digest = "sha256:" + hashlib.sha256(trace_data).hexdigest()
        index = {
            "schema_version": "longmemeval.build_trace_selection/v1",
            "purpose": "best-effort-diagnostic",
            "comparability": "backend-specific-not-cross-comparable",
            "scenario": "auto",
            "backend": "pgvector",
            "content_mode": "hash",
            "cases": [
                {
                    "case_id": "case-1",
                    "file": trace_file,
                    "sha256": trace_digest,
                },
            ],
        }
        index_data = validation._canonical_trace_selection(index)
        selection_name = (
            "maintained-" + hashlib.sha256(index_data).hexdigest()[:16]
        )
        selection_dir = scenario_dir / "build_trace" / selection_name
        selection_dir.mkdir(parents=True)
        (selection_dir / "manifest.json").write_bytes(index_data)
        (selection_dir / trace_file).write_bytes(trace_data)
        return {
            "path": selection_dir.relative_to(scenario_dir).as_posix(),
            "sha256": validation._digest_path(selection_dir),
            "purpose": "best-effort-diagnostic",
            "comparability": "backend-specific-not-cross-comparable",
            "content_mode": "hash",
            "selected_cases": 1,
        }


if __name__ == "__main__":
    unittest.main()
