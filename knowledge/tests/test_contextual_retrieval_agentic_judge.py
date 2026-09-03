#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for frozen-answer Judge execution and I2 statistics."""

import json
import math
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from contextual_retrieval.agentic import (
    AGENTIC_ANSWERS_SCHEMA,
    AGENTIC_LINEAGE_SCHEMA,
    AGENTIC_MANIFEST_SCHEMA,
    AGENTIC_REPORT_SCHEMA,
    AGENTIC_TRACE_CONTRACT,
    TOOL_NOT_FOUND_RESPONSE,
    build_agentic_schedule,
)
from contextual_retrieval.agentic_controller import (
    AGENTIC_CONTROLLER_REPORT_SCHEMA,
)
from contextual_retrieval.agentic_judge import (
    FORMAL_BATCH_SIZE,
    FROZEN_DEPENDENCY_VERSIONS,
    _normalize_frozen_agent_executions,
    _public_judge_config,
    _validate_formal_execution_grid,
    judge_agentic_answers,
)
from contextual_retrieval.agentic_metrics import (
    I2_METRICS,
    average_repeats_by_case,
    evaluate_i2_gate,
    stratified_paired_comparison,
)
from contextual_retrieval.artifacts import (
    canonical_digest,
    file_digest,
    load_artifact,
    write_artifact,
)
from contextual_retrieval.dataset import CASE_SCHEMA


def _metrics(value, context_precision=0.5):
    return {
        metric: (
            context_precision if metric == "context_precision" else value
        )
        for metric in I2_METRICS
    }


class AgenticMetricsTest(unittest.TestCase):
    def test_repeats_are_averaged_before_stratified_bootstrap(self):
        records = []
        for case_index, question_type in enumerate(
            ("comparison_query", "inference_query", "temporal_query")
        ):
            for repeat in range(3):
                for lane, value in (("baseline", 0.2), ("contextual", 0.3)):
                    records.append(
                        {
                            "case_id": f"case-{case_index}",
                            "question_type": question_type,
                            "repeat": repeat,
                            "lane": lane,
                            "metrics": _metrics(value),
                        }
                    )

        averaged = average_repeats_by_case(records, repeats=3)
        comparison = stratified_paired_comparison(
            averaged,
            resamples=50,
            seed=7,
        )
        gate = evaluate_i2_gate(
            comparison,
            evidence_complete=True,
            baseline_agent_failures=0,
            contextual_agent_failures=0,
            executions_per_lane=9,
        )

        self.assertEqual(6, len(averaged))
        self.assertEqual(3, comparison["samples"])
        self.assertAlmostEqual(
            0.1,
            comparison["overall"]["answer_correctness"]["delta"],
        )
        self.assertEqual("method_effective", gate["decision"])


class AgenticJudgeTest(unittest.TestCase):
    @staticmethod
    def _snapshot():
        return {
            "commit": "judge-shared-commit",
            "branch": "feat",
            "tracked_dirty": False,
            "untracked_dirty": False,
            "worktree_dirty": False,
        }

    def setUp(self):
        patcher = mock.patch(
            "contextual_retrieval.agentic_judge._capture_source_snapshots",
            return_value=(self._snapshot(), self._snapshot()),
        )
        self.capture_source_snapshots = patcher.start()
        self.addCleanup(patcher.stop)

    @staticmethod
    def _config():
        return {
            "model": "glm-5.2",
            "api_key": "judge-secret",
            "base_url": "https://judge.test/v1",
            "headers": {"X-SMG-Agent-Name": "judge"},
            "embedding_model": "bge-m3",
            "embedding_api_key": "embedding-secret",
            "embedding_base_url": "https://embedding.test/v1",
            "embedding_headers": {},
            "max_tokens": 65536,
            "timeout_seconds": 6000,
            "max_workers": 8,
            "temperature": 0,
            "reasoning_parameter_supplied": False,
            "whole_prompt_attempts": 1,
            "dependency_versions": dict(FROZEN_DEPENDENCY_VERSIONS),
        }

    def test_public_config_uses_shared_secret_free_endpoint_identity(self):
        config = self._config()
        config["base_url"] = (
            "https://judge-user:judge-password@judge.test/private/v1"
            "?token=judge-query#judge-fragment"
        )
        config["embedding_base_url"] = (
            "https://embed-user:embed-password@embedding.test/private/v1"
            "?token=embedding-query#embedding-fragment"
        )

        public = _public_judge_config(config)
        serialized = json.dumps(public, sort_keys=True)

        self.assertTrue(public["endpoint"].startswith("https://judge.test"))
        self.assertTrue(
            public["embedding_endpoint"].startswith(
                "https://embedding.test"
            )
        )
        self.assertIn("|path_sha256=", public["endpoint"])
        self.assertIn("|path_sha256=", public["embedding_endpoint"])
        for private_value in (
            "judge-user",
            "judge-password",
            "private/v1",
            "judge-query",
            "judge-fragment",
            "embed-user",
            "embed-password",
            "embedding-query",
            "embedding-fragment",
        ):
            self.assertNotIn(private_value, serialized)

    def test_frozen_tool_name_errors_are_normalized_without_agent_rerun(self):
        def legacy_execution(execution_id, recovered):
            invalid_name = (
                "search_knowledge_base" if recovered else "lookup_documents"
            )
            calls = [
                {
                    "id": f"{execution_id}-bad",
                    "name": invalid_name,
                    "arguments": '{"query":"question"}',
                }
            ]
            responses = [
                {
                    "tool_id": f"{execution_id}-bad",
                    "content": TOOL_NOT_FOUND_RESPONSE,
                }
            ]
            contexts = [TOOL_NOT_FOUND_RESPONSE]
            searches = []
            protocols = ["no_search_tool_call"]
            categories = ["trace_contract_error", "no_search_tool_call"]
            if recovered:
                calls.append(
                    {
                        "id": f"{execution_id}-good",
                        "name": "knowledge_search",
                        "arguments": '{"query":"question"}',
                    }
                )
                responses.append(
                    {
                        "tool_id": f"{execution_id}-good",
                        "content": json.dumps(
                            {"documents": [{"text": "retrieved context"}]}
                        ),
                    }
                )
                contexts.append("retrieved context")
                searches.append({"query": "question", "documents": [{}]})
                protocols = []
                categories = ["trace_contract_error", "tool_runtime_error"]
            return {
                "execution_id": execution_id,
                "lane": "baseline",
                "result": {
                    "status": "protocol_error",
                    "answer": "answer",
                    "contexts": contexts,
                    "trace": {
                        "tool_calls": calls,
                        "tool_responses": responses,
                    },
                    "searches": searches,
                    "trace_validation_errors": [
                        "top-level contexts differ from parsed "
                        "tool-response documents"
                    ],
                    "tool_runtime_errors": (
                        [
                            f"tool response {execution_id}-bad: "
                            "tool response is not JSON"
                        ]
                        if recovered
                        else []
                    ),
                    "agent_errors": [],
                    "protocol_violations": protocols,
                    "failure_categories": categories,
                },
            }

        source = [
            legacy_execution("recovered", True),
            legacy_execution("unrecovered", False),
        ]
        normalized, summary = _normalize_frozen_agent_executions(source)

        self.assertEqual("protocol_error", source[0]["result"]["status"])
        self.assertEqual("success", normalized[0]["result"]["status"])
        self.assertEqual(["retrieved context"], normalized[0]["result"]["contexts"])
        self.assertIsNone(normalized[0]["result"]["error"])
        self.assertEqual("error", normalized[1]["result"]["status"])
        self.assertEqual([], normalized[1]["result"]["contexts"])
        self.assertEqual(
            ["no_search_tool_call", "tool_name_error"],
            normalized[1]["result"]["failure_categories"],
        )
        self.assertEqual(2, summary["normalized_executions"])
        self.assertEqual(1, summary["recovered_executions"])
        self.assertEqual(1, summary["unrecovered_executions"])
        self.assertEqual(0, summary["rejected_candidates"])
        self.assertEqual(0, summary["agent_reruns"])

    def _formal_artifacts(self, root, agent_failure_lane=None):
        cases_payload = {
            "schema_version": CASE_SCHEMA,
            "chunk_manifest_digest": "chunks",
            "cases_count": 450,
            "cases": [
                {
                    "case_id": f"case-{index}",
                    "dataset_index": index,
                    "question": f"question-{index}",
                    "answer": f"truth-{index}",
                    "question_type": (
                        "comparison_query"
                        if index < 150
                        else "inference_query"
                        if index < 300
                        else "temporal_query"
                    ),
                }
                for index in range(450)
            ],
        }
        cases = write_artifact(str(root / "cases.json"), cases_payload)
        lineage = write_artifact(
            str(root / "verified-lineage.json"),
            {
                "schema_version": AGENTIC_LINEAGE_SCHEMA,
                "status": "valid",
                "mode": "formal",
                "controller_manifest_digest": "controller-manifest",
                "provenance_digest": "provenance",
                "repository": self._snapshot(),
                "benchmark_repository": self._snapshot(),
                "context_summary_digest": "context-summary",
                "baseline_index_state_digest": "baseline-index",
                "contextual_index_state_digest": "contextual-index",
                "baseline_runtime_config_digest": "baseline-config",
                "contextual_runtime_config_digest": "contextual-config",
                "context_cache_identity": "deepseek-contexts",
                "context_set_digest": "deepseek-context-set",
                "case_manifest_digest": cases["artifact_digest"],
                "chunk_manifest_digest": "chunks",
                "expected_cases": 450,
                "expected_executions": 2700,
                "repeats": 3,
                "search_k": 4,
                "tool_argument_policy": "query-guard/v1",
                "max_argument_repairs": 1,
                "silent_argument_rewrite": False,
                "provider_strict": False,
                "load_endpoint_called": False,
                "judge_initialized": False,
            },
        )
        schedule_seed = 20260725
        schedule = build_agentic_schedule(
            cases_payload["cases"],
            repeats=3,
            seed=schedule_seed,
        )
        manifest = write_artifact(
            str(root / "agentic.manifest.json"),
            {
                "schema_version": AGENTIC_MANIFEST_SCHEMA,
                "trace_contract": AGENTIC_TRACE_CONTRACT,
                "run_identity": "agentic-run",
                "case_manifest_digest": cases["artifact_digest"],
                "chunk_manifest_digest": "chunks",
                "evidence_scope": "agentic_effectiveness",
                "expected_cases": 450,
                "expected_executions": 2700,
                "repeats": 3,
                "schedule_seed": schedule_seed,
                "schedule_digest": canonical_digest(schedule),
                "baseline_config": {"embedding_model": "bge-m3"},
                "contextual_config": {"embedding_model": "bge-m3"},
                "verified_lineage_digest": lineage["artifact_digest"],
                "verified_lineage": lineage,
                "selection": {"smoke_per_type": None},
            },
        )
        case_by_id = {
            case["case_id"]: case for case in cases_payload["cases"]
        }
        executions = []
        for scheduled in schedule:
            case = case_by_id[scheduled["case_id"]]
            lane = scheduled["lane"]
            executions.append(
                {
                    **scheduled,
                    "dataset_index": case["dataset_index"],
                    "question_type": case["question_type"],
                    "question": case["question"],
                    "ground_truth": case["answer"],
                    "result": {
                        "status": "success",
                        "answer": lane,
                        "contexts": ["context"],
                        "search_call_count": 1,
                        "evidence": {
                            "cumulative_evidence_recall": 1.0,
                        },
                    },
                }
            )
        if agent_failure_lane is not None:
            failed = next(
                execution
                for execution in executions
                if execution["lane"] == agent_failure_lane
            )
            failed["result"] = {
                "status": "protocol_error",
                "answer": "",
                "contexts": [],
                "search_call_count": 0,
                "trace_validation_errors": [],
                "tool_runtime_errors": [],
                "protocol_violations": ["no_search_tool_call"],
                "failure_categories": ["no_search_tool_call"],
                "evidence": None,
                "error": "no_search_tool_call",
            }
        checkpoint_path = root / "agentic.checkpoint.jsonl"
        checkpoint_path.write_text(
            '{"fixture":"sealed-agent-checkpoint"}\n',
            encoding="utf-8",
        )
        checkpoint_sha256 = file_digest(str(checkpoint_path))
        checkpoint_records = 1
        answers = write_artifact(
            str(root / "agentic.answers.json"),
            {
                "schema_version": AGENTIC_ANSWERS_SCHEMA,
                "run_identity": "agentic-run",
                "manifest_digest": manifest["artifact_digest"],
                "expected_executions": len(executions),
                "completed_executions": len(executions),
                "checkpoint_sha256": checkpoint_sha256,
                "checkpoint_records": checkpoint_records,
                "executions": executions,
            },
        )
        agentic_report = write_artifact(
            str(root / "agentic.json"),
            {
                "schema_version": AGENTIC_REPORT_SCHEMA,
                "trace_contract": AGENTIC_TRACE_CONTRACT,
                "manifest_digest": manifest["artifact_digest"],
                "answers_digest": answers["artifact_digest"],
                "checkpoint_sha256": checkpoint_sha256,
                "checkpoint_records": checkpoint_records,
                "formal_answers_eligible": True,
            },
        )
        write_artifact(
            str(root / "controller.report.json"),
            {
                "schema_version": AGENTIC_CONTROLLER_REPORT_SCHEMA,
                "status": "valid",
                "mode": "formal",
                "verified_lineage_digest": lineage["artifact_digest"],
                "agentic_report_digest": agentic_report["artifact_digest"],
                "agentic_answers_digest": answers["artifact_digest"],
                "checkpoint_sha256": checkpoint_sha256,
                "checkpoint_records": checkpoint_records,
                "formal_answers_eligible": True,
            },
        )
        return cases, manifest, answers

    def _nonreference_artifacts(self, root):
        cases = write_artifact(
            str(root / "cases.json"),
            {
                "schema_version": CASE_SCHEMA,
                "chunk_manifest_digest": "chunks",
                "cases_count": 1,
                "cases": [
                    {
                        "case_id": "case-0",
                        "dataset_index": 0,
                        "question": "question-0",
                        "answer": "truth-0",
                        "question_type": "comparison_query",
                    }
                ],
            },
        )
        manifest = write_artifact(
            str(root / "agentic.manifest.json"),
            {
                "schema_version": AGENTIC_MANIFEST_SCHEMA,
                "trace_contract": AGENTIC_TRACE_CONTRACT,
                "run_identity": "nonreference-run",
                "case_manifest_digest": cases["artifact_digest"],
                "chunk_manifest_digest": "chunks",
                "evidence_scope": "diagnostic",
                "expected_cases": 1,
                "expected_executions": 2,
                "repeats": 1,
                "baseline_config": {"embedding_model": "bge-m3"},
                "contextual_config": {"embedding_model": "bge-m3"},
                "selection": {"smoke_per_type": None},
            },
        )
        executions = []
        for lane in ("baseline", "contextual"):
            executions.append(
                {
                    "execution_id": f"r0:case-0:{lane}",
                    "case_id": "case-0",
                    "question_type": "comparison_query",
                    "repeat": 0,
                    "lane": lane,
                    "question": "question-0",
                    "ground_truth": "truth-0",
                    "result": {
                        "status": "success",
                        "answer": lane,
                        "contexts": ["context"],
                        "search_call_count": 1,
                        "evidence": {"cumulative_evidence_recall": 1.0},
                    },
                }
            )
        write_artifact(
            str(root / "agentic.answers.json"),
            {
                "schema_version": AGENTIC_ANSWERS_SCHEMA,
                "run_identity": "nonreference-run",
                "manifest_digest": manifest["artifact_digest"],
                "expected_executions": 2,
                "completed_executions": 2,
                "executions": executions,
            },
        )
        return cases, manifest

    def test_nonreference_run_is_allowed_but_never_formal_eligible(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._nonreference_artifacts(root)
            config = self._config()
            config["max_workers"] = 2

            report = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=2,
                record_attempts=1,
                bootstrap_resamples=20,
                bootstrap_seed=7,
                config=config,
                judge_batch=lambda samples, ignored: {
                    "records": [
                        {"metrics": _metrics(0.3)} for _ in samples
                    ]
                },
                framework_repository_root="/explicit/framework/repository",
            )
            judge_manifest = load_artifact(
                str(root / "judge.manifest.json"),
                "contextual-retrieval/agentic-judge-manifest/v2",
            )
            serialized_manifest = json.dumps(judge_manifest, sort_keys=True)

        self.assertEqual("insufficient", report["evidence_status"])
        self.assertFalse(report["formal_ab_eligible"])
        self.assertIsNone(report["reference_profile"])
        self.assertEqual("judge-agentic", judge_manifest["invocation"])
        self.assertNotIn(str(root), serialized_manifest)
        self.assertNotIn("/explicit/framework/repository", serialized_manifest)
        self.capture_source_snapshots.assert_called_once()
        self.assertEqual(
            "/explicit/framework/repository",
            self.capture_source_snapshots.call_args.args[1],
        )

    def test_judge_exceptions_use_stable_categories_without_raw_text(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._nonreference_artifacts(root)
            config = self._config()
            config["max_workers"] = 2
            private_error = "provider-secret raw upstream response"

            def failing_judge(samples, ignored):
                del samples, ignored
                raise RuntimeError(private_error)

            report = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=2,
                record_attempts=2,
                bootstrap_resamples=20,
                bootstrap_seed=7,
                config=config,
                judge_batch=failing_judge,
            )
            scores = load_artifact(
                str(root / "judge.scores.json"),
                "contextual-retrieval/agentic-judge-scores/v2",
            )
            serialized = "\n".join(
                path.read_text(encoding="utf-8")
                for path in (
                    root / "judge.manifest.json",
                    root / "judge.scores.json",
                    root / "judge.json",
                )
            )

        self.assertNotIn(private_error, serialized)
        self.assertEqual("insufficient", report["evidence_status"])
        self.assertEqual(
            {"judge_rpc_error": 2},
            report["judge_error_categories"],
        )
        self.assertEqual(
            {"judge_rpc_error": 4},
            report["judge_attempt_error_categories"],
        )
        for score in scores["scores"]:
            self.assertEqual("judge_error", score["status"])
            self.assertIsNone(score["metrics"])
            self.assertFalse(score["recovered"])
            self.assertEqual(
                "judge_rpc_error",
                score["error"]["category"],
            )

    def test_formal_judge_rejects_nonreference_execution_parameters(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            for field, kwargs in (
                ("batch", {"batch_size": 26}),
                ("attempts", {"record_attempts": 4}),
                ("bootstrap", {"bootstrap_resamples": 9999}),
                ("seed", {"bootstrap_seed": 7}),
            ):
                with self.subTest(field=field):
                    with self.assertRaisesRegex(
                        ValueError,
                        "formal I2 requires batch size 25",
                    ):
                        judge_agentic_answers(
                            str(root / "agentic.answers.json"),
                            str(root / "agentic.manifest.json"),
                            str(root / "cases.json"),
                            str(root / f"judge-{field}.json"),
                            config=self._config(),
                            judge_batch=lambda samples, ignored: {
                                "records": []
                            },
                            controller_report_path=str(
                                root / "controller.report.json"
                            ),
                            verified_lineage_path=str(
                                root / "verified-lineage.json"
                            ),
                            agentic_report_path=str(root / "agentic.json"),
                            agentic_checkpoint_path=str(
                                root / "agentic.checkpoint.jsonl"
                            ),
                            **kwargs,
                        )

            config = self._config()
            config["max_workers"] = 4
            with self.assertRaisesRegex(
                ValueError,
                "formal I2 requires 8 Judge workers",
            ):
                judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge-workers.json"),
                    config=config,
                    judge_batch=lambda samples, ignored: {"records": []},
                    controller_report_path=str(
                        root / "controller.report.json"
                    ),
                    verified_lineage_path=str(
                        root / "verified-lineage.json"
                    ),
                    agentic_report_path=str(root / "agentic.json"),
                    agentic_checkpoint_path=str(
                        root / "agentic.checkpoint.jsonl"
                    ),
                )

    def test_formal_judge_uses_frozen_answers_and_applies_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            judged_samples = 0

            def fake_judge(samples, config):
                nonlocal judged_samples
                self.assertEqual(1, config["whole_prompt_attempts"])
                judged_samples += len(samples)
                return {
                    "records": [
                        {
                            "metrics": _metrics(
                                0.3 if sample["answer"] == "contextual" else 0.2
                            )
                        }
                        for sample in samples
                    ],
                    "token_usage": {
                        "input_tokens": len(samples),
                        "output_tokens": len(samples),
                        "total_tokens": 2 * len(samples),
                    },
                }

            report = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=FORMAL_BATCH_SIZE,
                bootstrap_resamples=10000,
                config=self._config(),
                judge_batch=fake_judge,
                controller_report_path=str(root / "controller.report.json"),
                verified_lineage_path=str(root / "verified-lineage.json"),
                agentic_report_path=str(root / "agentic.json"),
                agentic_checkpoint_path=str(root / "agentic.checkpoint.jsonl"),
            )
            frozen_paths = [
                root / "judge.manifest.json",
                root / "judge.scores.json",
                root / "judge.json",
            ]
            frozen_state = {
                path.name: (file_digest(str(path)), path.stat().st_mtime_ns)
                for path in frozen_paths
            }

            def must_not_rejudge(samples, config):
                del samples, config
                raise AssertionError("complete Judge output must be read-only")

            reused = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=FORMAL_BATCH_SIZE,
                bootstrap_resamples=10000,
                config=self._config(),
                judge_batch=must_not_rejudge,
                controller_report_path=str(root / "controller.report.json"),
                verified_lineage_path=str(root / "verified-lineage.json"),
                agentic_report_path=str(root / "agentic.json"),
                agentic_checkpoint_path=str(root / "agentic.checkpoint.jsonl"),
            )
            reused_state = {
                path.name: (file_digest(str(path)), path.stat().st_mtime_ns)
                for path in frozen_paths
            }

        self.assertEqual(2700, judged_samples)
        self.assertEqual(report["artifact_digest"], reused["artifact_digest"])
        self.assertEqual(frozen_state, reused_state)
        self.assertEqual("valid", report["evidence_status"])
        self.assertTrue(report["formal_ab_eligible"])
        self.assertEqual("method_effective", report["gate"]["decision"])
        self.assertEqual(450, report["comparison"]["samples"])
        self.assertEqual(
            450,
            report["success_only_diagnostics"][
                "paired_cases_all_repeats_successful"
            ],
        )
        self.assertAlmostEqual(
            0.1,
            report["success_only_diagnostics"]["paired_point_estimates"]
            ["answer_correctness"]["delta"],
        )

    def test_incomplete_record_is_retried_individually(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            calls = []
            failed_key = None

            def flaky_judge(samples, config):
                nonlocal failed_key
                del config
                calls.append(len(samples))
                records = []
                for sample in samples:
                    key = (sample["question"], sample["answer"])
                    metrics = _metrics(0.3)
                    if failed_key is None:
                        failed_key = key
                        metrics["answer_correctness"] = None
                    elif key == failed_key:
                        failed_key = ("recovered", "recovered")
                    records.append({"metrics": metrics})
                return {"records": records}

            report = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=FORMAL_BATCH_SIZE,
                record_attempts=5,
                bootstrap_resamples=10000,
                config=self._config(),
                judge_batch=flaky_judge,
                controller_report_path=str(root / "controller.report.json"),
                verified_lineage_path=str(root / "verified-lineage.json"),
                agentic_report_path=str(root / "agentic.json"),
                agentic_checkpoint_path=str(root / "agentic.checkpoint.jsonl"),
            )
            scores = load_artifact(
                str(root / "judge.scores.json"),
                "contextual-retrieval/agentic-judge-scores/v2",
            )

        recovered = [score for score in scores["scores"] if score.get("recovered")]
        self.assertEqual([FORMAL_BATCH_SIZE, 1], calls[:2])
        self.assertEqual(109, len(calls))
        self.assertTrue(
            all(size == FORMAL_BATCH_SIZE for size in calls[2:])
        )
        self.assertEqual(1, len(recovered))
        self.assertEqual(2, recovered[0]["attempts"])
        self.assertEqual("valid", report["evidence_status"])
        self.assertEqual(0, report["judge_errors"])
        self.assertEqual(1, report["judge_retry_attempts"])
        self.assertEqual(1, report["judge_recovered_records"])

    def test_judge_rejects_answers_from_an_older_trace_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            manifest = load_artifact(
                str(root / "agentic.manifest.json"),
                AGENTIC_MANIFEST_SCHEMA,
            )
            manifest.pop("artifact_digest")
            manifest.pop("trace_contract")
            write_artifact(str(root / "agentic.manifest.json"), manifest)

            with self.assertRaisesRegex(
                ValueError, "unsupported trace contract"
            ):
                judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge.json"),
                    config=self._config(),
                    judge_batch=lambda samples, config: None,
                )

    def test_formal_agent_failure_is_zero_scored_not_protocol_invalid(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root, agent_failure_lane="baseline")

            def fake_judge(samples, config):
                del config
                return {
                    "records": [
                        {
                            "metrics": _metrics(
                                0.3
                                if sample["answer"] == "contextual"
                                else 0.2
                            )
                        }
                        for sample in samples
                    ],
                    "token_usage": {
                        "input_tokens": len(samples),
                        "output_tokens": len(samples),
                        "total_tokens": 2 * len(samples),
                    },
                }

            report = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=FORMAL_BATCH_SIZE,
                bootstrap_resamples=10000,
                config=self._config(),
                judge_batch=fake_judge,
                controller_report_path=str(root / "controller.report.json"),
                verified_lineage_path=str(root / "verified-lineage.json"),
                agentic_report_path=str(root / "agentic.json"),
                agentic_checkpoint_path=str(root / "agentic.checkpoint.jsonl"),
            )

        self.assertEqual(0, report["protocol_errors"])
        self.assertEqual(1, report["agent_failures"]["baseline"])
        self.assertEqual(0, report["agent_failures"]["contextual"])
        self.assertTrue(report["formal_ab_eligible"])
        self.assertEqual("valid", report["evidence_status"])

    def test_unknown_judge_batch_is_never_sampled_again(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)

            def interrupting_judge(samples, config):
                del samples, config
                raise KeyboardInterrupt("Judge completion is unknown")

            with self.assertRaises(KeyboardInterrupt):
                judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge.json"),
                    batch_size=FORMAL_BATCH_SIZE,
                    bootstrap_resamples=10000,
                    config=self._config(),
                    judge_batch=interrupting_judge,
                    controller_report_path=str(root / "controller.report.json"),
                    verified_lineage_path=str(root / "verified-lineage.json"),
                    agentic_report_path=str(root / "agentic.json"),
                    agentic_checkpoint_path=str(
                        root / "agentic.checkpoint.jsonl"
                    ),
                )

            calls = 0

            def must_not_run(samples, config):
                nonlocal calls
                del samples, config
                calls += 1
                return {"records": []}

            with self.assertRaisesRegex(RuntimeError, "partial Judge output"):
                judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge.json"),
                    batch_size=FORMAL_BATCH_SIZE,
                    bootstrap_resamples=10000,
                    config=self._config(),
                    judge_batch=must_not_run,
                    controller_report_path=str(root / "controller.report.json"),
                    verified_lineage_path=str(root / "verified-lineage.json"),
                    agentic_report_path=str(root / "agentic.json"),
                    agentic_checkpoint_path=str(
                        root / "agentic.checkpoint.jsonl"
                    ),
                )

        self.assertEqual(0, calls)

    def test_formal_judge_rejects_non_bge_m3_embedding(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            config = self._config()
            config["embedding_model"] = "another-embedding-model"

            with self.assertRaisesRegex(
                ValueError,
                "must both be the frozen BGE-M3",
            ):
                judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge.json"),
                    batch_size=FORMAL_BATCH_SIZE,
                    bootstrap_resamples=10000,
                    config=config,
                    judge_batch=lambda samples, ignored: {"records": []},
                    controller_report_path=str(root / "controller.report.json"),
                    verified_lineage_path=str(root / "verified-lineage.json"),
                    agentic_report_path=str(root / "agentic.json"),
                    agentic_checkpoint_path=str(
                        root / "agentic.checkpoint.jsonl"
                    ),
                )

    def test_controlled_judge_rejects_missing_or_corrupt_agent_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            checkpoint = root / "agentic.checkpoint.jsonl"

            def run_judge():
                return judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge.json"),
                    batch_size=FORMAL_BATCH_SIZE,
                    bootstrap_resamples=10000,
                    config=self._config(),
                    judge_batch=lambda samples, ignored: {"records": []},
                    controller_report_path=str(root / "controller.report.json"),
                    verified_lineage_path=str(root / "verified-lineage.json"),
                    agentic_report_path=str(root / "agentic.json"),
                    agentic_checkpoint_path=str(checkpoint),
                )

            checkpoint.unlink()
            with self.assertRaisesRegex(ValueError, "checkpoint file is missing"):
                run_judge()

            self._formal_artifacts(root)
            with checkpoint.open("ab") as handle:
                handle.write(b"corrupt")
            with self.assertRaisesRegex(ValueError, "inconsistent"):
                run_judge()

    def test_controlled_judge_records_agent_and_judge_commits_separately(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            changed = {**self._snapshot(), "commit": "different-commit"}
            with mock.patch(
                "contextual_retrieval.agentic_judge._capture_source_snapshots",
                return_value=(changed, self._snapshot()),
            ):
                report = judge_agentic_answers(
                    str(root / "agentic.answers.json"),
                    str(root / "agentic.manifest.json"),
                    str(root / "cases.json"),
                    str(root / "judge.json"),
                    batch_size=FORMAL_BATCH_SIZE,
                    bootstrap_resamples=10000,
                    config=self._config(),
                    judge_batch=lambda samples, ignored: {
                        "records": [
                            {"metrics": _metrics(0.3)} for _ in samples
                        ]
                    },
                    controller_report_path=str(root / "controller.report.json"),
                    verified_lineage_path=str(root / "verified-lineage.json"),
                    agentic_report_path=str(root / "agentic.json"),
                    agentic_checkpoint_path=str(
                        root / "agentic.checkpoint.jsonl"
                    ),
                )
            manifest = load_artifact(
                str(root / "judge.manifest.json"),
                "contextual-retrieval/agentic-judge-manifest/v2",
            )

        self.assertEqual("valid", report["evidence_status"])
        self.assertEqual("different-commit", manifest["repository"]["commit"])
        self.assertEqual(
            "judge-shared-commit",
            manifest["agent_repository"]["commit"],
        )

    def test_judge_error_keeps_report_insufficient_and_diagnostics_safe(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._formal_artifacts(root)
            persistent_key = None
            persistent_attempts_remaining = 5

            def incomplete_judge(samples, config):
                nonlocal persistent_key, persistent_attempts_remaining
                del config
                records = []
                for index, sample in enumerate(samples):
                    key = (sample["question"], sample["answer"])
                    fail_this_attempt = False
                    if persistent_key is None and index == 0:
                        persistent_key = key
                        fail_this_attempt = True
                    elif (
                        len(samples) == 1
                        and key == persistent_key
                        and persistent_attempts_remaining > 0
                    ):
                        fail_this_attempt = True
                    metrics = _metrics(0.3)
                    if fail_this_attempt:
                        metrics["answer_correctness"] = math.nan
                        persistent_attempts_remaining -= 1
                    records.append({"metrics": metrics})
                return {"records": records}

            report = judge_agentic_answers(
                str(root / "agentic.answers.json"),
                str(root / "agentic.manifest.json"),
                str(root / "cases.json"),
                str(root / "judge.json"),
                batch_size=FORMAL_BATCH_SIZE,
                record_attempts=5,
                bootstrap_resamples=10000,
                config=self._config(),
                judge_batch=incomplete_judge,
                controller_report_path=str(root / "controller.report.json"),
                verified_lineage_path=str(root / "verified-lineage.json"),
                agentic_report_path=str(root / "agentic.json"),
                agentic_checkpoint_path=str(root / "agentic.checkpoint.jsonl"),
            )
            scores = load_artifact(
                str(root / "judge.scores.json"),
                "contextual-retrieval/agentic-judge-scores/v2",
            )

        self.assertEqual("insufficient", report["evidence_status"])
        self.assertEqual(0, persistent_attempts_remaining)
        self.assertFalse(report["formal_ab_eligible"])
        self.assertEqual(1, report["judge_errors"])
        self.assertEqual(
            {"judge_metric_error": 1},
            report["judge_error_categories"],
        )
        self.assertEqual(5, report["judge_incomplete_metric_attempts"])
        self.assertEqual(0, report["judge_recovered_records"])
        failed = [
            score
            for score in scores["scores"]
            if score["status"] == "judge_error"
        ]
        self.assertEqual(1, len(failed))
        self.assertIsNone(failed[0]["metrics"])
        self.assertFalse(failed[0]["recovered"])
        self.assertEqual(
            "judge_metric_error",
            failed[0]["error"]["category"],
        )
        self.assertEqual(
            449,
            report["success_only_diagnostics"][
                "paired_cases_all_repeats_successful"
            ],
        )
        self.assertIsNone(report["comparison"])

    def test_formal_execution_grid_rejects_missing_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            cases, manifest, answers = self._formal_artifacts(Path(directory))
            malformed = dict(answers)
            malformed["executions"] = list(answers["executions"][:-1])

            with self.assertRaisesRegex(ValueError, "2700 executions"):
                _validate_formal_execution_grid(malformed, manifest, cases)


if __name__ == "__main__":
    unittest.main()
