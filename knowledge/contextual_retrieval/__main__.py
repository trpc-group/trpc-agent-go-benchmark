#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Command-line entrypoint for the retrieval-only validation lane."""

from __future__ import annotations

import argparse
import json
from typing import Any, Dict

from contextual_retrieval.agentic import run_agentic_ab
from contextual_retrieval.agentic_controller import run_agentic_server_ab
from contextual_retrieval.agentic_judge import judge_agentic_answers
from contextual_retrieval.context_cache import (
    context_config_from_env,
    generate_context_cache,
    probe_context_generation,
    summarize_context_cache,
)
from contextual_retrieval.controller import (
    run_server_formal,
    run_server_smoke,
)
from contextual_retrieval.dataset import (
    DEFAULT_QUESTION_TYPES,
    map_evidence_to_chunks,
    prepare_dataset,
)
from contextual_retrieval.rescore import rescore_retrieval_samples
from contextual_retrieval.runner import run_retrieval_ab


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Contextual Retrieval I1 and Agentic I2 experiment tools",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    prepare = subparsers.add_parser(
        "prepare-dataset",
        help="Prepare stable MultiHop-RAG parent/query manifests",
    )
    prepare.add_argument("--corpus", required=True)
    prepare.add_argument("--questions", required=True)
    prepare.add_argument("--parents-output", required=True)
    prepare.add_argument("--queries-output", required=True)
    prepare.add_argument("--preflight-output", required=True)
    prepare.add_argument("--per-type-limit", type=int, default=150)
    prepare.add_argument(
        "--question-type",
        action="append",
        dest="question_types",
        choices=DEFAULT_QUESTION_TYPES,
    )

    mapping = subparsers.add_parser(
        "map-evidence",
        help="Freeze exact evidence-to-parent/chunk mappings",
    )
    mapping.add_argument("--queries", required=True)
    mapping.add_argument("--chunks", required=True)
    mapping.add_argument("--cases-output", required=True)
    mapping.add_argument("--preflight-output", required=True)

    contexts = subparsers.add_parser(
        "generate-contexts",
        help="Generate or resume the durable Context JSONL cache",
    )
    contexts.add_argument("--parents", required=True)
    contexts.add_argument("--chunks", required=True)
    contexts.add_argument("--cache", required=True)
    contexts.add_argument("--summary-output")
    contexts.add_argument("--workers", type=int, default=8)
    contexts.add_argument("--attempts-per-run", type=int, default=3)
    contexts.add_argument("--progress-interval", type=float, default=10)

    probe = subparsers.add_parser(
        "probe-contexts",
        help="Probe Context model compatibility without polluting the cache",
    )
    probe.add_argument("--parents", required=True)
    probe.add_argument("--chunks", required=True)
    probe.add_argument("--output", required=True)
    probe.add_argument("--count", type=int, default=20)
    probe.add_argument("--attempts-per-item", type=int, default=3)

    summary = subparsers.add_parser(
        "summarize-contexts",
        help="Validate and summarize an existing Context cache",
    )
    summary.add_argument("--chunks", required=True)
    summary.add_argument("--cache", required=True)
    summary.add_argument("--output")

    run = subparsers.add_parser(
        "run",
        help="Run the paired retrieval-only A/B",
    )
    run.add_argument("--cases", required=True)
    run.add_argument("--chunks", required=True)
    run.add_argument("--baseline-url", required=True)
    run.add_argument("--contextual-url", required=True)
    run.add_argument("--output", required=True)
    run.add_argument("--timeout", type=float, default=120)
    run.add_argument("--request-attempts", type=int, default=3)
    run.add_argument("--bootstrap-resamples", type=int, default=10000)
    run.add_argument("--bootstrap-seed", type=int, default=20260722)
    run.add_argument("--limit", type=int)
    run.add_argument("--smoke-per-type", type=int)

    rescore = subparsers.add_parser(
        "rescore-retrieval",
        help="Recompute metrics from frozen retrieval rankings",
    )
    rescore.add_argument("--cases", required=True)
    rescore.add_argument("--source-manifest", required=True)
    rescore.add_argument("--source-samples", required=True)
    rescore.add_argument("--output", required=True)
    rescore.add_argument("--bootstrap-resamples", type=int)
    rescore.add_argument("--bootstrap-seed", type=int)
    rescore.add_argument(
        "--allow-case-manifest-change",
        action="store_true",
        help="Allow an explicitly audited corrected case manifest",
    )

    agentic = subparsers.add_parser(
        "run-agentic",
        help="Freeze paired I2 Agent answers without invoking a Judge",
    )
    agentic.add_argument("--cases", required=True)
    agentic.add_argument("--chunks", required=True)
    agentic.add_argument("--baseline-url", required=True)
    agentic.add_argument("--contextual-url", required=True)
    agentic.add_argument("--output", required=True)
    agentic.add_argument("--repeats", type=int, default=3)
    agentic.add_argument("--timeout", type=float, default=1800)
    agentic.add_argument("--schedule-seed", type=int, default=20260725)
    agentic.add_argument("--smoke-per-type", type=int)
    agentic.add_argument(
        "--expected-agent-model",
        default="deepseek-v3.2",
    )

    agentic_server = subparsers.add_parser(
        "run-agentic-server",
        help="Run one guarded I2 smoke or formal answer-freezing phase",
    )
    agentic_server.add_argument(
        "--mode",
        choices=("smoke", "formal"),
        required=True,
    )
    agentic_server.add_argument("--go-service-dir", required=True)
    agentic_server.add_argument("--chunks", required=True)
    agentic_server.add_argument("--cases", required=True)
    agentic_server.add_argument("--context-cache", required=True)
    agentic_server.add_argument("--context-summary", required=True)
    agentic_server.add_argument("--baseline-index-state", required=True)
    agentic_server.add_argument("--contextual-index-state", required=True)
    agentic_server.add_argument("--output-dir", required=True)
    agentic_server.add_argument("--baseline-port", type=int, default=8765)
    agentic_server.add_argument("--contextual-port", type=int, default=8766)
    agentic_server.add_argument(
        "--service-start-timeout",
        type=float,
        default=120,
    )
    agentic_server.add_argument("--request-timeout", type=float, default=1800)
    agentic_server.add_argument(
        "--schedule-seed",
        type=int,
        default=20260725,
    )
    agentic_server.add_argument("--smoke-per-type", type=int, default=10)
    agentic_server.add_argument("--framework-repository-root")

    judge_agentic = subparsers.add_parser(
        "judge-agentic",
        help="Run GLM RAGAS over immutable I2 answers",
    )
    judge_agentic.add_argument("--answers", required=True)
    judge_agentic.add_argument("--agentic-manifest", required=True)
    judge_agentic.add_argument("--cases", required=True)
    judge_agentic.add_argument("--output", required=True)
    judge_agentic.add_argument("--batch-size", type=int, default=25)
    judge_agentic.add_argument("--record-attempts", type=int, default=5)
    judge_agentic.add_argument(
        "--bootstrap-resamples",
        type=int,
        default=10000,
    )
    judge_agentic.add_argument(
        "--bootstrap-seed",
        type=int,
        default=20260725,
    )
    judge_agentic.add_argument("--controller-report")
    judge_agentic.add_argument("--verified-lineage")
    judge_agentic.add_argument("--agentic-report")
    judge_agentic.add_argument("--agentic-checkpoint")
    judge_agentic.add_argument("--framework-repository-root")

    server_smoke = subparsers.add_parser(
        "run-server-smoke",
        help="Build isolated A/B indexes and run the guarded retrieval smoke",
    )
    server_smoke.add_argument("--go-service-dir", required=True)
    server_smoke.add_argument("--chunks", required=True)
    server_smoke.add_argument("--cases", required=True)
    server_smoke.add_argument("--context-cache", required=True)
    server_smoke.add_argument("--output-dir", required=True)
    server_smoke.add_argument("--baseline-table", required=True)
    server_smoke.add_argument("--contextual-table", required=True)
    server_smoke.add_argument("--baseline-port", type=int, default=8765)
    server_smoke.add_argument("--contextual-port", type=int, default=8766)
    server_smoke.add_argument("--smoke-per-type", type=int, default=10)
    server_smoke.add_argument("--bootstrap-resamples", type=int, default=1000)
    server_smoke.add_argument("--bootstrap-seed", type=int, default=20260722)
    server_smoke.add_argument("--service-start-timeout", type=float, default=120)
    server_smoke.add_argument("--load-timeout", type=float, default=7200)
    server_smoke.add_argument("--resume-indexes", action="store_true")
    server_smoke.add_argument("--baseline-only", action="store_true")
    server_smoke.add_argument("--framework-repository-root")

    server_formal = subparsers.add_parser(
        "run-server-formal",
        help="Reuse promoted smoke indexes and run the guarded formal A/B",
    )
    server_formal.add_argument("--go-service-dir", required=True)
    server_formal.add_argument("--chunks", required=True)
    server_formal.add_argument("--cases", required=True)
    server_formal.add_argument("--context-cache", required=True)
    server_formal.add_argument("--smoke-dir", required=True)
    server_formal.add_argument("--output-dir", required=True)
    server_formal.add_argument("--baseline-port", type=int)
    server_formal.add_argument("--contextual-port", type=int)
    server_formal.add_argument(
        "--conformance-smoke-per-type",
        type=int,
        default=10,
    )
    server_formal.add_argument(
        "--conformance-bootstrap-resamples",
        type=int,
        default=1000,
    )
    server_formal.add_argument("--bootstrap-resamples", type=int, default=10000)
    server_formal.add_argument("--bootstrap-seed", type=int, default=20260722)
    server_formal.add_argument("--service-start-timeout", type=float, default=120)
    server_formal.add_argument("--request-timeout", type=float, default=120)
    server_formal.add_argument("--request-attempts", type=int, default=3)
    server_formal.add_argument("--framework-repository-root")
    return parser


def _run(args: argparse.Namespace) -> Dict[str, Any]:
    if args.command == "prepare-dataset":
        _, _, preflight = prepare_dataset(
            args.corpus,
            args.questions,
            args.parents_output,
            args.queries_output,
            args.preflight_output,
            per_type_limit=args.per_type_limit,
            question_types=args.question_types or DEFAULT_QUESTION_TYPES,
        )
        return preflight
    if args.command == "map-evidence":
        _, preflight = map_evidence_to_chunks(
            args.queries,
            args.chunks,
            args.cases_output,
            args.preflight_output,
        )
        return preflight
    if args.command == "generate-contexts":
        result = generate_context_cache(
            args.parents,
            args.chunks,
            args.cache,
            context_config_from_env(),
            workers=args.workers,
            attempts_per_run=args.attempts_per_run,
            progress_interval_seconds=args.progress_interval,
        )
        if args.summary_output:
            result = summarize_context_cache(
                args.cache,
                args.chunks,
                args.summary_output,
            )
        return result
    if args.command == "probe-contexts":
        return probe_context_generation(
            args.parents,
            args.chunks,
            args.output,
            context_config_from_env(),
            count=args.count,
            attempts_per_item=args.attempts_per_item,
        )
    if args.command == "summarize-contexts":
        return summarize_context_cache(args.cache, args.chunks, args.output)
    if args.command == "run":
        return run_retrieval_ab(
            args.cases,
            args.chunks,
            args.baseline_url,
            args.contextual_url,
            args.output,
            timeout=args.timeout,
            request_attempts=args.request_attempts,
            bootstrap_resamples=args.bootstrap_resamples,
            bootstrap_seed=args.bootstrap_seed,
            limit=args.limit,
            smoke_per_type=args.smoke_per_type,
        )
    if args.command == "rescore-retrieval":
        return rescore_retrieval_samples(
            args.cases,
            args.source_manifest,
            args.source_samples,
            args.output,
            bootstrap_resamples=args.bootstrap_resamples,
            bootstrap_seed=args.bootstrap_seed,
            allow_case_manifest_change=args.allow_case_manifest_change,
        )
    if args.command == "run-agentic":
        return run_agentic_ab(
            args.cases,
            args.chunks,
            args.baseline_url,
            args.contextual_url,
            args.output,
            repeats=args.repeats,
            timeout=args.timeout,
            schedule_seed=args.schedule_seed,
            smoke_per_type=args.smoke_per_type,
            expected_agent_model=args.expected_agent_model,
        )
    if args.command == "run-agentic-server":
        return run_agentic_server_ab(
            args.go_service_dir,
            args.chunks,
            args.cases,
            args.context_cache,
            args.context_summary,
            args.baseline_index_state,
            args.contextual_index_state,
            args.output_dir,
            mode=args.mode,
            baseline_port=args.baseline_port,
            contextual_port=args.contextual_port,
            service_start_timeout=args.service_start_timeout,
            request_timeout=args.request_timeout,
            schedule_seed=args.schedule_seed,
            smoke_per_type=args.smoke_per_type,
            framework_repository_root=args.framework_repository_root,
        )
    if args.command == "judge-agentic":
        return judge_agentic_answers(
            args.answers,
            args.agentic_manifest,
            args.cases,
            args.output,
            batch_size=args.batch_size,
            record_attempts=args.record_attempts,
            bootstrap_resamples=args.bootstrap_resamples,
            bootstrap_seed=args.bootstrap_seed,
            controller_report_path=args.controller_report,
            verified_lineage_path=args.verified_lineage,
            agentic_report_path=args.agentic_report,
            agentic_checkpoint_path=args.agentic_checkpoint,
            framework_repository_root=args.framework_repository_root,
        )
    if args.command == "run-server-smoke":
        return run_server_smoke(
            args.go_service_dir,
            args.chunks,
            args.cases,
            args.context_cache,
            args.output_dir,
            args.baseline_table,
            args.contextual_table,
            baseline_port=args.baseline_port,
            contextual_port=args.contextual_port,
            smoke_per_type=args.smoke_per_type,
            bootstrap_resamples=args.bootstrap_resamples,
            bootstrap_seed=args.bootstrap_seed,
            service_start_timeout=args.service_start_timeout,
            load_timeout=args.load_timeout,
            resume_indexes=args.resume_indexes,
            baseline_only=args.baseline_only,
            framework_repository_root=args.framework_repository_root,
        )
    if args.command == "run-server-formal":
        return run_server_formal(
            args.go_service_dir,
            args.chunks,
            args.cases,
            args.context_cache,
            args.smoke_dir,
            args.output_dir,
            baseline_port=args.baseline_port,
            contextual_port=args.contextual_port,
            conformance_smoke_per_type=args.conformance_smoke_per_type,
            conformance_bootstrap_resamples=(
                args.conformance_bootstrap_resamples
            ),
            bootstrap_resamples=args.bootstrap_resamples,
            bootstrap_seed=args.bootstrap_seed,
            service_start_timeout=args.service_start_timeout,
            request_timeout=args.request_timeout,
            request_attempts=args.request_attempts,
            framework_repository_root=args.framework_repository_root,
        )
    raise AssertionError(f"unsupported command {args.command}")


def main() -> None:
    result = _run(_parser().parse_args())
    print(json.dumps(result, indent=2, ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
