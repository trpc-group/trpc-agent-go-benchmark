#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Judge frozen I2 answers without ever invoking an Agent."""

from __future__ import annotations

import copy
import importlib.metadata
import json
import math
import os
import platform
import threading
import time
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, Tuple
from contextual_retrieval.agentic import (
    AGENTIC_ANSWERS_SCHEMA,
    AGENTIC_LINEAGE_SCHEMA,
    AGENTIC_MANIFEST_SCHEMA,
    AGENTIC_REPORT_SCHEMA,
    AGENTIC_TRACE_CONTRACT,
    FORMAL_CASES,
    FORMAL_REPEATS,
    FORMAL_SCHEDULE_SEED,
    KNOWLEDGE_SEARCH_TOOL_NAME,
    TOOL_NOT_FOUND_RESPONSE,
    build_agentic_schedule,
    evaluate_agentic_smoke_gate,
    manifest_has_verified_formal_lineage,
    validate_agentic_checkpoint_evidence,
)
from contextual_retrieval.agentic_controller import (
    AGENTIC_CONTROLLER_REPORT_SCHEMA,
    _capture_source_snapshots,
)
from contextual_retrieval.agentic_metrics import (
    I2_METRICS,
    average_repeats_by_case,
    evaluate_i2_gate,
    stratified_paired_comparison,
)
from contextual_retrieval.artifacts import (
    canonical_digest,
    load_artifact,
    public_endpoint_identity,
    write_artifact,
)
from contextual_retrieval.dataset import CASE_SCHEMA
from contextual_retrieval.controller import (
    _require_clean_snapshot,
)


AGENTIC_JUDGE_MANIFEST_SCHEMA = "contextual-retrieval/agentic-judge-manifest/v2"
AGENTIC_JUDGE_SCORES_SCHEMA = "contextual-retrieval/agentic-judge-scores/v2"
AGENTIC_JUDGE_REPORT_SCHEMA = "contextual-retrieval/agentic-judge-report/v2"
DEFAULT_JUDGE_MODEL = "glm-5.2"
JUDGE_PROVIDER_MODELS = ("glm-5.2", "glm52")
FORMAL_JUDGE_PROFILE = "i2-formal-judge-reference/v1"
JUDGE_SUBCOMMAND = "judge-agentic"
FORMAL_MAX_WORKERS = 8
FORMAL_BATCH_SIZE = 25
FORMAL_RECORD_ATTEMPTS = 5
DEFAULT_RECORD_ATTEMPTS = FORMAL_RECORD_ATTEMPTS
FORMAL_BOOTSTRAP_RESAMPLES = 10000
FORMAL_BOOTSTRAP_SEED = 20260725
JUDGE_RPC_ERROR = "judge_rpc_error"
JUDGE_RECORD_ERROR = "judge_record_error"
JUDGE_METRIC_ERROR = "judge_metric_error"
AGENT_FAILURE_ERROR = "agent_failure"
AGENT_RESULT_NORMALIZATION_POLICY = "tool-name-error-normalization/v1"
TOP_LEVEL_CONTEXT_MISMATCH = (
    "top-level contexts differ from parsed tool-response documents"
)
FROZEN_DEPENDENCY_VERSIONS = {
    "python": "3.11.6",
    "ragas": "0.2.15",
    "langchain-openai": "0.2.14",
    "openai": "1.109.1",
    "datasets": "2.21.0",
}


def _dependency_versions() -> Dict[str, str]:
    versions = {"python": platform.python_version()}
    for package in ("ragas", "langchain-openai", "openai", "datasets"):
        try:
            versions[package] = importlib.metadata.version(package)
        except importlib.metadata.PackageNotFoundError:
            versions[package] = "missing"
    return versions


class _JudgeRecordError(ValueError):
    """Internal marker for a malformed Judge batch response."""


def _stable_judge_error(
    category: str,
    *,
    missing_metrics: Optional[Sequence[str]] = None,
) -> Dict[str, Any]:
    """Return an artifact-safe error without third-party exception text."""
    error: Dict[str, Any] = {"category": category}
    if missing_metrics is not None:
        error["missing_metrics"] = sorted(set(missing_metrics))
    return error


def _judge_exception_category(error: Exception) -> str:
    if isinstance(error, _JudgeRecordError):
        return JUDGE_RECORD_ERROR
    return JUDGE_RPC_ERROR


def _gateway_headers(prefix: str) -> Dict[str, str]:
    headers = {}
    if value := os.environ.get(f"{prefix}_SMG_ROUTING_KEY"):
        headers["X-SMG-Routing-Key"] = value
    if value := os.environ.get(f"{prefix}_SMG_AGENT_NAME"):
        headers["X-SMG-Agent-Name"] = value
    return headers


def judge_config_from_env() -> Dict[str, Any]:
    """Load explicit GLM Judge and BGE-M3 embedding configuration."""
    model = os.environ.get("EVAL_MODEL_NAME", "").strip()
    api_key = os.environ.get("EVAL_API_KEY", "").strip()
    base_url = os.environ.get("EVAL_BASE_URL", "").strip()
    embedding_model = os.environ.get("EMBEDDING_MODEL", "").strip()
    embedding_api_key = os.environ.get("EMBEDDING_API_KEY", "").strip()
    embedding_base_url = os.environ.get("EMBEDDING_BASE_URL", "").strip()
    required = {
        "EVAL_MODEL_NAME": model,
        "EVAL_API_KEY": api_key,
        "EVAL_BASE_URL": base_url,
        "EMBEDDING_MODEL": embedding_model,
        "EMBEDDING_API_KEY": embedding_api_key,
        "EMBEDDING_BASE_URL": embedding_base_url,
    }
    missing = [name for name, value in required.items() if not value]
    if missing:
        raise ValueError("required Judge environment is missing: " + ", ".join(missing))
    if model.lower() not in JUDGE_PROVIDER_MODELS:
        raise ValueError(
            "EVAL_MODEL_NAME must identify GLM-5.2 as glm-5.2 or glm52"
        )
    if embedding_model.lower() != "bge-m3":
        raise ValueError("EMBEDDING_MODEL must be bge-m3 for the frozen I2 run")
    max_tokens = int(os.environ.get("EVAL_MAX_TOKENS", "65536"))
    timeout = float(os.environ.get("EVAL_TIMEOUT_SECONDS", "6000"))
    max_workers = int(
        os.environ.get("EVAL_MAX_WORKERS", str(FORMAL_MAX_WORKERS))
    )
    if max_tokens <= 0 or timeout <= 0 or max_workers <= 0:
        raise ValueError("Judge max tokens, timeout, and workers must be positive")
    dependency_versions = _dependency_versions()
    if dependency_versions != FROZEN_DEPENDENCY_VERSIONS:
        raise ValueError(
            "Judge dependency versions do not match the frozen I2 environment"
        )
    return {
        "model": model,
        "api_key": api_key,
        "base_url": base_url,
        "headers": _gateway_headers("EVAL"),
        "embedding_model": embedding_model,
        "embedding_api_key": embedding_api_key,
        "embedding_base_url": embedding_base_url,
        "embedding_headers": _gateway_headers("EMBEDDING"),
        "max_tokens": max_tokens,
        "timeout_seconds": timeout,
        "max_workers": max_workers,
        "temperature": 0,
        "reasoning_parameter_supplied": False,
        "whole_prompt_attempts": 1,
        "dependency_versions": dependency_versions,
    }


def _public_judge_config(config: Mapping[str, Any]) -> Dict[str, Any]:
    return {
        "model": config["model"],
        "canonical_model": DEFAULT_JUDGE_MODEL,
        "endpoint": public_endpoint_identity(str(config["base_url"])),
        "header_names": sorted((config.get("headers") or {}).keys()),
        "embedding_model": config["embedding_model"],
        "embedding_endpoint": public_endpoint_identity(
            str(config["embedding_base_url"])
        ),
        "embedding_header_names": sorted(
            (config.get("embedding_headers") or {}).keys()
        ),
        "max_tokens": int(config["max_tokens"]),
        "timeout_seconds": float(config["timeout_seconds"]),
        "max_workers": int(config["max_workers"]),
        "temperature": 0,
        "reasoning_parameter_supplied": False,
        "whole_prompt_attempts": 1,
        "client_retries": 0,
        "embedding_client_retries": 0,
        "ragas_attempts": 1,
        "dependency_versions": dict(config.get("dependency_versions") or {}),
        "content_block_normalization": "text_only_blocks_to_string",
    }


def normalize_text_content(content: Any) -> Any:
    """Normalize text-only OpenAI-compatible content blocks for RAGAS."""
    if not isinstance(content, list):
        return content
    parts = []
    for block in content:
        if isinstance(block, str):
            parts.append(block)
            continue
        if not isinstance(block, Mapping):
            raise ValueError("Judge response contains an unsupported content block")
        block_type = block.get("type")
        text = block.get("text")
        if isinstance(text, str) and block_type in (None, "text", "output_text"):
            parts.append(text)
            continue
        if isinstance(text, Mapping) and isinstance(text.get("value"), str):
            parts.append(text["value"])
            continue
        raise ValueError(
            "Judge response contains a non-text content block: "
            f"{block_type or 'unknown'}"
        )
    return "\n".join(parts)


class _RagasBatchJudge:
    """Reusable, self-contained RAGAS adapter for one frozen Judge stage."""

    def __init__(self, config: Mapping[str, Any]):
        from langchain_openai import ChatOpenAI, OpenAIEmbeddings
        from pydantic import PrivateAttr, SecretStr

        class CompatibleChatOpenAI(ChatOpenAI):
            _content_lock: Any = PrivateAttr(default_factory=threading.Lock)
            _normalized_content_responses: int = PrivateAttr(default=0)

            @property
            def normalized_content_responses(self) -> int:
                with self._content_lock:
                    return self._normalized_content_responses

            def _create_chat_result(
                self,
                response: Any,
                generation_info: Any = None,
            ) -> Any:
                if isinstance(response, dict):
                    response_dict = copy.deepcopy(response)
                else:
                    try:
                        response_dict = response.model_dump(warnings=False)
                    except TypeError:
                        response_dict = response.model_dump()
                normalized = 0
                for choice in response_dict.get("choices") or []:
                    message = choice.get("message")
                    if not isinstance(message, dict):
                        continue
                    if isinstance(message.get("content"), list):
                        message["content"] = normalize_text_content(
                            message["content"]
                        )
                        normalized += 1
                if normalized:
                    with self._content_lock:
                        self._normalized_content_responses += normalized
                    return super()._create_chat_result(
                        response_dict,
                        generation_info,
                    )
                return super()._create_chat_result(response, generation_info)

        self._config = dict(config)
        self._llm = CompatibleChatOpenAI(
            model=config["model"],
            temperature=0,
            api_key=SecretStr(str(config["api_key"])),
            base_url=config["base_url"],
            default_headers=dict(config.get("headers") or {}) or None,
            max_tokens=int(config["max_tokens"]),
            timeout=float(config["timeout_seconds"]),
            max_retries=0,
        )
        self._embeddings = OpenAIEmbeddings(
            model=config["embedding_model"],
            api_key=SecretStr(str(config["embedding_api_key"])),
            base_url=config["embedding_base_url"],
            default_headers=(
                dict(config.get("embedding_headers") or {}) or None
            ),
            tiktoken_enabled=False,
            check_embedding_ctx_length=False,
            timeout=float(config["timeout_seconds"]),
            max_retries=0,
        )

    def __call__(self, samples: Sequence[Mapping[str, Any]]) -> Dict[str, Any]:
        from datasets import Dataset
        from ragas import evaluate
        from ragas.cost import get_token_usage_for_openai
        from ragas.metrics._answer_correctness import AnswerCorrectness
        from ragas.metrics._answer_relevance import AnswerRelevancy
        from ragas.metrics._answer_similarity import AnswerSimilarity
        from ragas.metrics._context_entities_recall import ContextEntityRecall
        from ragas.metrics._context_precision import ContextPrecision
        from ragas.metrics._context_recall import ContextRecall
        from ragas.metrics._faithfulness import Faithfulness
        from ragas.run_config import RunConfig

        dataset = Dataset.from_dict(
            {
                "question": [sample["question"] for sample in samples],
                "answer": [sample["answer"] for sample in samples],
                "contexts": [sample["contexts"] for sample in samples],
                "ground_truth": [sample["ground_truth"] for sample in samples],
            }
        )
        metrics = [
            Faithfulness(),
            AnswerRelevancy(),
            AnswerCorrectness(),
            AnswerSimilarity(),
            ContextPrecision(),
            ContextRecall(),
            ContextEntityRecall(),
        ]
        result = evaluate(
            dataset,
            metrics=metrics,
            llm=self._llm,
            embeddings=self._embeddings,
            run_config=RunConfig(
                max_workers=int(self._config["max_workers"]),
                timeout=float(self._config["timeout_seconds"]),
                max_retries=1,
            ),
            token_usage_parser=get_token_usage_for_openai,
        )
        records = result.to_pandas().to_dict(orient="records")
        normalized_records = []
        aliases = {
            **{metric: (metric,) for metric in I2_METRICS},
            "answer_similarity": (
                "answer_similarity",
                "semantic_similarity",
            ),
        }
        for record in records:
            metric_values = {}
            for metric, names in aliases.items():
                value = None
                for name in names:
                    try:
                        candidate = float(record.get(name))
                    except (TypeError, ValueError):
                        continue
                    if math.isfinite(candidate):
                        value = candidate
                        break
                metric_values[metric] = value
            normalized_records.append({"metrics": metric_values})
        usage = result.total_tokens()
        if isinstance(usage, list):
            input_tokens = sum(item.input_tokens for item in usage)
            output_tokens = sum(item.output_tokens for item in usage)
        else:
            input_tokens = usage.input_tokens
            output_tokens = usage.output_tokens
        return {
            "records": normalized_records,
            "token_usage": {
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "total_tokens": input_tokens + output_tokens,
            },
            "normalized_content_responses": (
                self._llm.normalized_content_responses
            ),
        }


def _output_paths(output_path: str) -> Dict[str, str]:
    target = Path(output_path)
    stem = target.with_suffix("") if target.suffix else target
    return {
        "report": str(target),
        "manifest": str(stem) + ".manifest.json",
        "scores": str(stem) + ".scores.json",
    }


def _metrics_complete(metrics: Any) -> bool:
    return isinstance(metrics, Mapping) and all(
        isinstance(metrics.get(metric), (int, float))
        and not isinstance(metrics.get(metric), bool)
        and math.isfinite(float(metrics[metric]))
        for metric in I2_METRICS
    )


def _missing_metrics(metrics: Any) -> List[str]:
    if not isinstance(metrics, Mapping):
        return list(I2_METRICS)
    return [
        metric
        for metric in I2_METRICS
        if not (
            isinstance(metrics.get(metric), (int, float))
            and not isinstance(metrics.get(metric), bool)
            and math.isfinite(float(metrics[metric]))
        )
    ]


def _uses_formal_reference_protocol(
    public_config: Mapping[str, Any],
    *,
    batch_size: int,
    record_attempts: int,
    bootstrap_resamples: int,
    bootstrap_seed: int,
) -> bool:
    """Return whether this invocation uses the frozen formal Judge profile."""
    return (
        int(public_config.get("max_workers") or 0) == FORMAL_MAX_WORKERS
        and batch_size == FORMAL_BATCH_SIZE
        and record_attempts == FORMAL_RECORD_ATTEMPTS
        and bootstrap_resamples == FORMAL_BOOTSTRAP_RESAMPLES
        and bootstrap_seed == FORMAL_BOOTSTRAP_SEED
    )


def _validate_formal_execution_grid(
    answers: Mapping[str, Any],
    agentic_manifest: Mapping[str, Any],
    cases: Mapping[str, Any],
) -> None:
    """Fail closed unless the frozen formal artifact is exactly 450 x 3 x 2."""
    case_records = cases.get("cases")
    if not isinstance(case_records, list) or len(case_records) != FORMAL_CASES:
        raise ValueError("formal I2 requires exactly 450 case records")
    case_by_id: Dict[str, Mapping[str, Any]] = {}
    for case in case_records:
        if not isinstance(case, Mapping):
            raise ValueError("formal I2 contains a non-object case")
        case_id = str(case.get("case_id") or "")
        if not case_id or case_id in case_by_id:
            raise ValueError("formal I2 case IDs must be unique and non-empty")
        case_by_id[case_id] = case
    if cases.get("cases_count") != FORMAL_CASES:
        raise ValueError("formal I2 case count is not 450")
    if Counter(str(case.get("question_type") or "") for case in case_records) != {
        "comparison_query": 150,
        "inference_query": 150,
        "temporal_query": 150,
    }:
        raise ValueError("formal I2 requires exactly 150 cases of each type")

    expected_executions = FORMAL_CASES * FORMAL_REPEATS * 2
    if (
        agentic_manifest.get("expected_cases") != FORMAL_CASES
        or agentic_manifest.get("repeats") != FORMAL_REPEATS
        or agentic_manifest.get("expected_executions") != expected_executions
        or answers.get("expected_executions") != expected_executions
        or answers.get("completed_executions") != expected_executions
    ):
        raise ValueError("formal I2 manifest does not describe 2700 executions")
    executions = answers.get("executions")
    if not isinstance(executions, list) or len(executions) != expected_executions:
        raise ValueError("formal I2 answers do not contain 2700 executions")
    schedule_seed = agentic_manifest.get("schedule_seed")
    if schedule_seed != FORMAL_SCHEDULE_SEED:
        raise ValueError("formal I2 requires schedule seed 20260725")
    expected_schedule = build_agentic_schedule(
        case_records,
        FORMAL_REPEATS,
        schedule_seed,
    )
    if agentic_manifest.get("schedule_digest") != canonical_digest(
        expected_schedule
    ):
        raise ValueError("formal I2 schedule digest is invalid")

    observed = set()
    execution_ids = set()
    for execution, scheduled in zip(executions, expected_schedule):
        if not isinstance(execution, Mapping):
            raise ValueError("formal I2 contains a non-object execution")
        for field in (
            "execution_id",
            "pair_id",
            "repeat",
            "case_id",
            "case_position",
            "schedule_position",
            "lane_position",
            "lane",
        ):
            if execution.get(field) != scheduled.get(field):
                raise ValueError(f"formal I2 execution changes schedule field {field}")
        case_id = str(execution.get("case_id") or "")
        case = case_by_id.get(case_id)
        repeat = execution.get("repeat")
        lane = str(execution.get("lane") or "")
        execution_id = str(execution.get("execution_id") or "")
        if (
            case is None
            or not isinstance(repeat, int)
            or isinstance(repeat, bool)
            or repeat not in range(FORMAL_REPEATS)
            or lane not in ("baseline", "contextual")
        ):
            raise ValueError("formal I2 execution has an invalid grid identity")
        expected_id = f"r{repeat}:{case_id}:{lane}"
        key = (case_id, repeat, lane)
        if execution_id != expected_id or execution_id in execution_ids or key in observed:
            raise ValueError("formal I2 execution grid is duplicated or malformed")
        execution_ids.add(execution_id)
        observed.add(key)
        for execution_field, case_field in (
            ("dataset_index", "dataset_index"),
            ("question", "question"),
            ("ground_truth", "answer"),
            ("question_type", "question_type"),
        ):
            if execution.get(execution_field) != case.get(case_field):
                raise ValueError(
                    f"formal I2 execution {execution_id} changes {execution_field}"
                )
        result = execution.get("result")
        if (
            not isinstance(result, Mapping)
            or result.get("status")
            not in ("success", "error", "indeterminate", "protocol_error")
        ):
            raise ValueError("formal I2 execution is not in a terminal state")
    expected_grid = {
        (case_id, repeat, lane)
        for case_id in case_by_id
        for repeat in range(FORMAL_REPEATS)
        for lane in ("baseline", "contextual")
    }
    if observed != expected_grid:
        raise ValueError("formal I2 execution grid is incomplete")


def _tool_response_document_texts(content: Any) -> Optional[List[str]]:
    """Extract evidence text only from a valid knowledge-search response."""
    if not isinstance(content, str):
        return None
    try:
        payload = json.loads(content)
    except json.JSONDecodeError:
        return None
    if not isinstance(payload, Mapping):
        return None
    documents = payload.get("documents")
    if not isinstance(documents, list):
        return None
    texts: List[str] = []
    for document in documents:
        if not isinstance(document, Mapping):
            return None
        text = document.get("text")
        if not isinstance(text, str) or not text:
            return None
        texts.append(text)
    return texts


def _normalize_frozen_tool_name_error(
    execution: Mapping[str, Any],
) -> Tuple[Mapping[str, Any], Optional[Dict[str, Any]]]:
    """Repair the narrow legacy tool-name accounting bug without an LLM call."""
    result = execution.get("result")
    if not isinstance(result, Mapping):
        return execution, None
    trace = result.get("trace")
    if not isinstance(trace, Mapping):
        return execution, None
    calls = trace.get("tool_calls")
    responses = trace.get("tool_responses")
    if not isinstance(calls, list) or not isinstance(responses, list):
        return execution, None

    response_by_id = {
        str(response["tool_id"]): response
        for response in responses
        if isinstance(response, Mapping)
        and isinstance(response.get("tool_id"), str)
    }
    invalid_calls: List[Dict[str, Any]] = []
    for position, call in enumerate(calls):
        if not isinstance(call, Mapping):
            continue
        call_id = str(call.get("id") or "")
        response = response_by_id.get(call_id)
        if (
            str(call.get("name") or "") != KNOWLEDGE_SEARCH_TOOL_NAME
            and response is not None
            and isinstance(response.get("content"), str)
            and response["content"].strip() == TOOL_NOT_FOUND_RESPONSE
        ):
            invalid_calls.append(
                {
                    "tool_call_id": call_id or None,
                    "call_position": position,
                    "legacy_search_like": (
                        "search" in str(call.get("name") or "").lower()
                    ),
                }
            )
    if not invalid_calls:
        return execution, None

    trace_errors = list(result.get("trace_validation_errors") or [])
    runtime_errors = list(result.get("tool_runtime_errors") or [])
    contexts = result.get("contexts")
    if (
        not trace_errors
        and not runtime_errors
        and isinstance(result.get("tool_name_errors"), list)
    ):
        # Results produced after the capture fix are already normalized.
        return execution, None

    rejection_reason = None
    if not trace_errors or any(
        error != TOP_LEVEL_CONTEXT_MISMATCH for error in trace_errors
    ):
        rejection_reason = "unexpected_trace_errors"
    expected_runtime_errors = [
        invalid
        for invalid in invalid_calls
        if invalid["legacy_search_like"]
    ]
    if rejection_reason is None and (
        len(runtime_errors) != len(expected_runtime_errors)
        or any(
            "tool response is not JSON" not in str(error)
            for error in runtime_errors
        )
    ):
        rejection_reason = "unexpected_tool_runtime_errors"
    if rejection_reason is None and (
        not isinstance(contexts, list) or not all(
            isinstance(context, str) for context in contexts
        )
    ):
        rejection_reason = "invalid_contexts"
    if rejection_reason is None:
        for invalid in expected_runtime_errors:
            call_id = str(invalid.get("tool_call_id") or "")
            if not any(
                call_id in str(error)
                and "tool response is not JSON" in str(error)
                for error in runtime_errors
            ):
                rejection_reason = "unmatched_tool_runtime_error"
                break
    if rejection_reason is not None:
        return execution, {
            "applied": False,
            "reason": rejection_reason,
            "tool_name_error_attempts": len(invalid_calls),
        }

    filtered_contexts = [
        context
        for context in contexts
        if context.strip() != TOOL_NOT_FOUND_RESPONSE
    ]
    removed_contexts = len(contexts) - len(filtered_contexts)
    if removed_contexts != len(invalid_calls):
        return execution, {
            "applied": False,
            "reason": "dispatcher_context_count_mismatch",
            "tool_name_error_attempts": len(invalid_calls),
        }

    valid_search_positions: List[int] = []
    expected_contexts: List[str] = []
    for position, call in enumerate(calls):
        if (
            not isinstance(call, Mapping)
            or str(call.get("name") or "") != KNOWLEDGE_SEARCH_TOOL_NAME
        ):
            continue
        response = response_by_id.get(str(call.get("id") or ""))
        texts = _tool_response_document_texts(
            response.get("content") if response is not None else None
        )
        if texts:
            valid_search_positions.append(position)
            expected_contexts.extend(texts)
    if filtered_contexts != expected_contexts:
        return execution, {
            "applied": False,
            "reason": "retrieved_contexts_do_not_match_valid_tool_responses",
            "tool_name_error_attempts": len(invalid_calls),
        }

    normalized_errors = []
    for invalid in invalid_calls:
        recovered = any(
            position > int(invalid["call_position"])
            for position in valid_search_positions
        )
        normalized_errors.append(
            {
                "tool_call_id": invalid.get("tool_call_id"),
                "call_position": invalid["call_position"],
                "recovered": recovered,
            }
        )
    recovered_errors = sum(
        bool(error["recovered"]) for error in normalized_errors
    )
    unrecovered_errors = len(normalized_errors) - recovered_errors

    normalized_execution = copy.deepcopy(execution)
    normalized_result = normalized_execution["result"]
    normalized_result["contexts"] = filtered_contexts
    normalized_result["trace_validation_errors"] = []
    normalized_result["tool_runtime_errors"] = []
    normalized_result["tool_name_errors"] = normalized_errors
    normalized_result["tool_name_error_attempts"] = len(normalized_errors)
    normalized_result["recovered_tool_name_errors"] = recovered_errors
    normalized_result["unrecovered_tool_name_errors"] = unrecovered_errors

    diagnostics = dict(normalized_result.get("trace_diagnostics") or {})
    diagnostics["excluded_tool_error_contexts"] = (
        int(diagnostics.get("excluded_tool_error_contexts") or 0)
        + removed_contexts
    )
    skipped = list(diagnostics.get("comparison_skipped_reasons") or [])
    if "tool_name_error_calls_excluded" not in skipped:
        skipped.append("tool_name_error_calls_excluded")
    diagnostics["comparison_skipped_reasons"] = skipped
    normalized_result["trace_diagnostics"] = diagnostics

    categories = [
        category
        for category in normalized_result.get("failure_categories") or []
        if category not in ("trace_contract_error", "tool_runtime_error")
    ]
    agent_errors = list(normalized_result.get("agent_errors") or [])
    protocol_violations = list(
        normalized_result.get("protocol_violations") or []
    )
    if unrecovered_errors:
        repair_error = "tool name repair was not completed"
        if repair_error not in agent_errors:
            agent_errors.append(repair_error)
        if "tool_name_error" not in categories:
            categories.append("tool_name_error")
    normalized_result["agent_errors"] = agent_errors
    normalized_result["failure_categories"] = categories

    answer = normalized_result.get("answer")
    remaining_errors = [*protocol_violations, *agent_errors]
    if (
        unrecovered_errors == 0
        and not remaining_errors
        and isinstance(answer, str)
        and answer.strip()
    ):
        normalized_result["status"] = "success"
        normalized_result["error"] = None
        disposition = "recovered"
    else:
        normalized_result["status"] = "error"
        normalized_result["error"] = "; ".join(remaining_errors)[:4000]
        disposition = "unrecovered"
    normalized_result["agent_result_normalization"] = {
        "policy": AGENT_RESULT_NORMALIZATION_POLICY,
        "raw_status": result.get("status"),
        "disposition": disposition,
        "excluded_contexts": removed_contexts,
    }
    return normalized_execution, {
        "applied": True,
        "disposition": disposition,
        "tool_name_error_attempts": len(normalized_errors),
        "recovered_tool_name_errors": recovered_errors,
        "unrecovered_tool_name_errors": unrecovered_errors,
        "excluded_contexts": removed_contexts,
    }


def _normalize_frozen_agent_executions(
    executions: Sequence[Mapping[str, Any]],
) -> Tuple[List[Mapping[str, Any]], Dict[str, Any]]:
    """Return Judge inputs derived from frozen answers, never rerunning Agent."""
    normalized: List[Mapping[str, Any]] = []
    details: List[Dict[str, Any]] = []
    for execution in executions:
        normalized_execution, detail = _normalize_frozen_tool_name_error(
            execution
        )
        normalized.append(normalized_execution)
        if detail is not None:
            details.append(detail)
    applied = [detail for detail in details if detail.get("applied")]
    rejected = [detail for detail in details if not detail.get("applied")]
    return normalized, {
        "policy": AGENT_RESULT_NORMALIZATION_POLICY,
        "source_executions": len(executions),
        "legacy_candidates": len(details),
        "normalized_executions": len(applied),
        "recovered_executions": sum(
            detail.get("disposition") == "recovered" for detail in applied
        ),
        "unrecovered_executions": sum(
            detail.get("disposition") == "unrecovered" for detail in applied
        ),
        "tool_name_error_attempts": sum(
            int(detail.get("tool_name_error_attempts") or 0)
            for detail in applied
        ),
        "recovered_tool_name_errors": sum(
            int(detail.get("recovered_tool_name_errors") or 0)
            for detail in applied
        ),
        "unrecovered_tool_name_errors": sum(
            int(detail.get("unrecovered_tool_name_errors") or 0)
            for detail in applied
        ),
        "excluded_contexts": sum(
            int(detail.get("excluded_contexts") or 0)
            for detail in applied
        ),
        "rejected_candidates": len(rejected),
        "rejection_reasons": dict(
            Counter(str(detail.get("reason") or "unknown") for detail in rejected)
        ),
        "agent_reruns": 0,
    }


def _checkpoint(
    path: str,
    run_identity: str,
    manifest_digest: str,
    expected: int,
    batches: Sequence[Mapping[str, Any]],
    scores: Sequence[Mapping[str, Any]],
) -> Dict[str, Any]:
    return write_artifact(
        path,
        {
            "schema_version": AGENTIC_JUDGE_SCORES_SCHEMA,
            "run_identity": run_identity,
            "manifest_digest": manifest_digest,
            "expected_scores": expected,
            "completed_scores": len(scores),
            "batches": list(batches),
            "scores": list(scores),
        },
    )


def _mechanism_summary(
    executions: Sequence[Mapping[str, Any]],
    lane: str,
) -> Dict[str, Any]:
    selected = [execution for execution in executions if execution["lane"] == lane]
    successful = [
        execution
        for execution in selected
        if execution["result"]["status"] == "success"
    ]
    recalls = [
        execution["result"]["evidence"]["cumulative_evidence_recall"]
        for execution in successful
        if execution["result"].get("evidence")
        and execution["result"]["evidence"].get(
            "cumulative_evidence_recall"
        )
        is not None
    ]
    return {
        "executions": len(selected),
        "successful_agent_executions": len(successful),
        "average_search_calls": (
            sum(execution["result"]["search_call_count"] for execution in selected)
            / len(selected)
            if selected
            else None
        ),
        "average_cumulative_evidence_recall": (
            sum(recalls) / len(recalls) if recalls else None
        ),
    }


def _success_only_diagnostics(
    executions: Sequence[Mapping[str, Any]],
    scores: Sequence[Mapping[str, Any]],
    repeats: int,
) -> Dict[str, Any]:
    """Report non-confirmatory quality after excluding Agent failures."""
    execution_by_id = {
        str(execution["execution_id"]): execution for execution in executions
    }
    successful_scores = [
        score
        for score in scores
        if score.get("status") == "success"
        and execution_by_id.get(str(score.get("execution_id")), {})
        .get("result", {})
        .get("status")
        == "success"
        and _metrics_complete(score.get("metrics"))
    ]
    by_lane: Dict[str, Any] = {}
    for lane in ("baseline", "contextual"):
        selected = [score for score in successful_scores if score["lane"] == lane]
        by_lane[lane] = {
            "successful_executions": len(selected),
            "metrics": {
                metric: (
                    sum(float(score["metrics"][metric]) for score in selected)
                    / len(selected)
                    if selected
                    else None
                )
                for metric in I2_METRICS
            },
        }

    executions_by_case: Dict[str, List[Mapping[str, Any]]] = {}
    for execution in executions:
        executions_by_case.setdefault(str(execution["case_id"]), []).append(
            execution
        )
    complete_case_ids = {
        case_id
        for case_id, group in executions_by_case.items()
        if len(group) == repeats * 2
        and all(item["result"]["status"] == "success" for item in group)
    }
    successful_scores_by_case: Dict[str, List[Mapping[str, Any]]] = {}
    for score in successful_scores:
        successful_scores_by_case.setdefault(str(score["case_id"]), []).append(
            score
        )
    complete_case_ids = {
        case_id
        for case_id in complete_case_ids
        if len(successful_scores_by_case.get(case_id, [])) == repeats * 2
    }
    paired_scores = [
        score
        for score in successful_scores
        if str(score["case_id"]) in complete_case_ids
    ]
    paired_point_estimates = None
    paired_count = 0
    if paired_scores:
        averaged = average_repeats_by_case(paired_scores, repeats)
        averaged_by_case: Dict[str, Dict[str, Mapping[str, Any]]] = {}
        for record in averaged:
            averaged_by_case.setdefault(str(record["case_id"]), {})[
                str(record["lane"])
            ] = record
        paired = [
            lanes
            for lanes in averaged_by_case.values()
            if set(lanes) == {"baseline", "contextual"}
        ]
        if paired:
            paired_count = len(paired)
            paired_point_estimates = {
                metric: {
                    "baseline": sum(
                        float(lanes["baseline"]["metrics"][metric])
                        for lanes in paired
                    )
                    / len(paired),
                    "contextual": sum(
                        float(lanes["contextual"]["metrics"][metric])
                        for lanes in paired
                    )
                    / len(paired),
                }
                for metric in I2_METRICS
            }
            for values in paired_point_estimates.values():
                values["delta"] = values["contextual"] - values["baseline"]
    return {
        "confirmatory": False,
        "selection": "successful Agent executions only",
        "by_lane": by_lane,
        "paired_cases_all_repeats_successful": paired_count,
        "paired_point_estimates": paired_point_estimates,
    }


def judge_agentic_answers(
    answers_path: str,
    agentic_manifest_path: str,
    cases_path: str,
    output_path: str,
    batch_size: int = FORMAL_BATCH_SIZE,
    record_attempts: int = DEFAULT_RECORD_ATTEMPTS,
    bootstrap_resamples: int = FORMAL_BOOTSTRAP_RESAMPLES,
    bootstrap_seed: int = FORMAL_BOOTSTRAP_SEED,
    config: Optional[Mapping[str, Any]] = None,
    judge_batch: Optional[
        Callable[[Sequence[Mapping[str, Any]], Mapping[str, Any]], Mapping[str, Any]]
    ] = None,
    controller_report_path: Optional[str] = None,
    verified_lineage_path: Optional[str] = None,
    agentic_report_path: Optional[str] = None,
    agentic_checkpoint_path: Optional[str] = None,
    framework_repository_root: Optional[str] = None,
) -> Dict[str, Any]:
    """Score immutable Agent answers; retry incomplete Judge records."""
    if batch_size <= 0 or record_attempts <= 0 or bootstrap_resamples <= 0:
        raise ValueError(
            "batch size, record attempts, and bootstrap resamples must be positive"
        )
    answers = load_artifact(answers_path, AGENTIC_ANSWERS_SCHEMA)
    agentic_manifest = load_artifact(
        agentic_manifest_path,
        AGENTIC_MANIFEST_SCHEMA,
    )
    if agentic_manifest.get("trace_contract") != AGENTIC_TRACE_CONTRACT:
        raise ValueError("Agentic manifest uses an unsupported trace contract")
    cases = load_artifact(cases_path, CASE_SCHEMA)
    if answers.get("manifest_digest") != agentic_manifest.get("artifact_digest"):
        raise ValueError("frozen answers do not match the Agentic manifest")
    if answers.get("completed_executions") != answers.get("expected_executions"):
        raise ValueError("frozen Agent answers are incomplete")
    if agentic_manifest.get("case_manifest_digest") != cases.get("artifact_digest"):
        raise ValueError("Agentic answers do not use the requested cases")
    evidence_scope = agentic_manifest.get("evidence_scope")
    controlled_formal = evidence_scope == "agentic_effectiveness"
    controlled_run = evidence_scope in (
        "agentic_operational_smoke",
        "agentic_effectiveness",
    )
    if controlled_formal:
        _validate_formal_execution_grid(answers, agentic_manifest, cases)
    if controlled_formal and (
        batch_size != FORMAL_BATCH_SIZE
        or record_attempts != FORMAL_RECORD_ATTEMPTS
        or bootstrap_resamples != FORMAL_BOOTSTRAP_RESAMPLES
        or bootstrap_seed != FORMAL_BOOTSTRAP_SEED
    ):
        raise ValueError(
            "formal I2 requires batch size 25, 5 record attempts, 10000 "
            "bootstrap resamples, and seed 20260725"
        )
    controller_report = None
    verified_lineage = None
    agentic_report = None
    checkpoint_evidence = None
    if (
        controlled_run
        or controller_report_path
        or verified_lineage_path
        or agentic_report_path
        or agentic_checkpoint_path
    ):
        if (
            not controller_report_path
            or not verified_lineage_path
            or not agentic_report_path
            or not agentic_checkpoint_path
        ):
            raise ValueError(
                "Agentic checkpoint, report, controller report, and lineage "
                "must be supplied together"
            )
        controller_report = load_artifact(
            controller_report_path,
            AGENTIC_CONTROLLER_REPORT_SCHEMA,
        )
        verified_lineage = load_artifact(
            verified_lineage_path,
            AGENTIC_LINEAGE_SCHEMA,
        )
        agentic_report = load_artifact(
            agentic_report_path,
            AGENTIC_REPORT_SCHEMA,
        )
        checkpoint_evidence = validate_agentic_checkpoint_evidence(
            agentic_checkpoint_path,
            answers,
            agentic_report,
        )
        embedded_lineage = agentic_manifest.get("verified_lineage")
        if (
            embedded_lineage != verified_lineage
            or agentic_manifest.get("verified_lineage_digest")
            != verified_lineage.get("artifact_digest")
            or controller_report.get("verified_lineage_digest")
            != verified_lineage.get("artifact_digest")
            or controller_report.get("status") != "valid"
            or controller_report.get("mode") != verified_lineage.get("mode")
            or controller_report.get("agentic_report_digest") is None
            or controller_report.get("agentic_report_digest")
            != agentic_report.get("artifact_digest")
            or agentic_report.get("manifest_digest")
            != agentic_manifest.get("artifact_digest")
            or agentic_report.get("trace_contract") != AGENTIC_TRACE_CONTRACT
            or agentic_report.get("answers_digest")
            != answers.get("artifact_digest")
            or controller_report.get("agentic_answers_digest")
            != answers.get("artifact_digest")
            or controller_report.get("checkpoint_sha256")
            != checkpoint_evidence["sha256"]
            or controller_report.get("checkpoint_records")
            != checkpoint_evidence["records"]
        ):
            raise ValueError("Agentic controller chain is incomplete or inconsistent")
    if controlled_formal and (
        controller_report is None
        or verified_lineage is None
        or agentic_report is None
        or verified_lineage.get("mode") != "formal"
        or controller_report.get("formal_answers_eligible") is not True
        or agentic_report.get("formal_answers_eligible") is not True
    ):
        raise ValueError(
            "formal Judge requires the valid controller report and lineage"
        )

    benchmark_root = Path(__file__).resolve().parents[2]
    repository_snapshot, benchmark_snapshot = _capture_source_snapshots(
        benchmark_root,
        framework_repository_root,
    )
    _require_clean_snapshot("Judge repository", repository_snapshot)
    _require_clean_snapshot("Judge benchmark repository", benchmark_snapshot)

    private_config = dict(config or judge_config_from_env())
    public_config = _public_judge_config(private_config)
    if str(public_config["model"]).lower() not in JUDGE_PROVIDER_MODELS:
        raise ValueError("Judge provider model must identify GLM-5.2")
    agent_embedding_model = (
        agentic_manifest.get("baseline_config") or {}
    ).get("embedding_model")
    if not agent_embedding_model:
        raise ValueError("Agentic manifest has no embedding model identity")
    if (
        str(public_config["embedding_model"]).strip().lower() != "bge-m3"
        or str(agent_embedding_model).strip().lower() != "bge-m3"
    ):
        raise ValueError(
            "Judge and A/B embedding models must both be the frozen BGE-M3"
        )
    if (
        public_config["max_tokens"] != 65536
        or public_config["temperature"] != 0
        or public_config["reasoning_parameter_supplied"] is not False
        or public_config["whole_prompt_attempts"] != 1
        or public_config["client_retries"] != 0
        or public_config["embedding_client_retries"] != 0
        or public_config["ragas_attempts"] != 1
        or public_config["dependency_versions"]
        != FROZEN_DEPENDENCY_VERSIONS
    ):
        raise ValueError("Judge generation protocol is not the frozen I2 config")
    formal_reference_protocol = _uses_formal_reference_protocol(
        public_config,
        batch_size=batch_size,
        record_attempts=record_attempts,
        bootstrap_resamples=bootstrap_resamples,
        bootstrap_seed=bootstrap_seed,
    )
    if controlled_formal and not formal_reference_protocol:
        raise ValueError(
            "formal I2 requires 8 Judge workers and the frozen reference profile"
        )
    raw_executions = list(answers["executions"])
    raw_protocol_errors = sum(
        len(
            execution.get("result", {}).get("trace_validation_errors")
            or []
        )
        for execution in raw_executions
    )
    executions, agent_result_normalization = (
        _normalize_frozen_agent_executions(raw_executions)
    )
    protocol_errors = sum(
        len(
            execution.get("result", {}).get("trace_validation_errors")
            or []
        )
        for execution in executions
    )
    identity_payload = {
        "agentic_answers_digest": answers["artifact_digest"],
        "agentic_manifest_digest": agentic_manifest["artifact_digest"],
        "case_manifest_digest": cases["artifact_digest"],
        "agentic_checkpoint_sha256": (
            checkpoint_evidence["sha256"] if checkpoint_evidence else None
        ),
        "agentic_checkpoint_records": (
            checkpoint_evidence["records"] if checkpoint_evidence else None
        ),
        "repository": repository_snapshot,
        "benchmark_repository": benchmark_snapshot,
        "agent_repository": (
            verified_lineage.get("repository") if verified_lineage else None
        ),
        "agent_benchmark_repository": (
            verified_lineage.get("benchmark_repository")
            if verified_lineage
            else None
        ),
        "judge_config": public_config,
        "metrics": list(I2_METRICS),
        "batch_size": batch_size,
        "record_attempts": record_attempts,
        "bootstrap_resamples": bootstrap_resamples,
        "bootstrap_seed": bootstrap_seed,
        "reference_profile": (
            FORMAL_JUDGE_PROFILE
            if controlled_formal and formal_reference_protocol
            else None
        ),
        "agent_failure_policy": "zero_all_metrics",
        "agent_result_normalization": agent_result_normalization,
        "judge_failure_policy": (
            "retry_incomplete_record_then_evidence_insufficient"
        ),
        "controller_report_digest": (
            controller_report.get("artifact_digest")
            if controller_report
            else None
        ),
        "agentic_report_digest": (
            agentic_report.get("artifact_digest") if agentic_report else None
        ),
        "verified_lineage_digest": (
            verified_lineage.get("artifact_digest")
            if verified_lineage
            else None
        ),
    }
    run_identity = canonical_digest(identity_payload)
    paths = _output_paths(output_path)
    manifest_payload = {
        "schema_version": AGENTIC_JUDGE_MANIFEST_SCHEMA,
        **identity_payload,
        "run_identity": run_identity,
        "expected_scores": len(executions),
        "created_at": datetime.now(timezone.utc).isoformat(),
        "invocation": JUDGE_SUBCOMMAND,
    }
    if os.path.exists(paths["manifest"]):
        manifest = load_artifact(
            paths["manifest"],
            AGENTIC_JUDGE_MANIFEST_SCHEMA,
        )
        if manifest.get("run_identity") != run_identity:
            raise ValueError("existing Judge manifest belongs to another run")
        for field, expected in {
            **identity_payload,
            "expected_scores": len(executions),
        }.items():
            if manifest.get(field) != expected:
                raise ValueError(
                    f"existing Judge manifest has incompatible {field}"
                )
    else:
        manifest = write_artifact(paths["manifest"], manifest_payload)

    batches: List[Dict[str, Any]] = []
    scores: List[Dict[str, Any]] = []
    scores_artifact: Optional[Dict[str, Any]] = None
    scores_reused = False
    if os.path.exists(paths["scores"]):
        checkpoint = load_artifact(
            paths["scores"],
            AGENTIC_JUDGE_SCORES_SCHEMA,
        )
        if checkpoint.get("run_identity") != run_identity:
            raise ValueError("existing Judge scores belong to another run")
        if (
            checkpoint.get("manifest_digest") != manifest["artifact_digest"]
            or checkpoint.get("expected_scores") != len(executions)
        ):
            raise ValueError("existing Judge scores have incompatible lineage")
        batches = list(checkpoint.get("batches") or [])
        scores = list(checkpoint.get("scores") or [])
        pending = [batch for batch in batches if batch.get("status") == "started"]
        score_ids = [str(score.get("execution_id") or "") for score in scores]
        expected_score_ids = [str(item["execution_id"]) for item in executions]
        if (
            pending
            or checkpoint.get("completed_scores") != len(executions)
            or len(scores) != len(executions)
            or len(set(score_ids)) != len(score_ids)
            or set(score_ids) != set(expected_score_ids)
        ):
            raise RuntimeError(
                "partial Judge output is fail-stop; use a new output path"
            )
        scores_artifact = checkpoint
        scores_reused = True

    if scores_reused and os.path.exists(paths["report"]):
        report = load_artifact(paths["report"], AGENTIC_JUDGE_REPORT_SCHEMA)
        if (
            report.get("run_identity") != run_identity
            or report.get("manifest_digest") != manifest["artifact_digest"]
            or report.get("scores_digest") != scores_artifact["artifact_digest"]
            or report.get("expected_scores") != len(executions)
            or report.get("completed_scores") != len(executions)
        ):
            raise ValueError("existing Judge report differs from frozen scores")
        return report

    scores_by_id = {str(score["execution_id"]): score for score in scores}
    for execution in executions:
        execution_id = str(execution["execution_id"])
        if execution_id in scores_by_id:
            continue
        if execution["result"]["status"] == "success":
            continue
        score = {
            "execution_id": execution_id,
            "case_id": execution["case_id"],
            "question_type": execution["question_type"],
            "repeat": execution["repeat"],
            "lane": execution["lane"],
            "status": "agent_failure_zero",
            "metrics": {metric: 0.0 for metric in I2_METRICS},
            "error": _stable_judge_error(AGENT_FAILURE_ERROR),
        }
        scores.append(score)
        scores_by_id[execution_id] = score
    if not scores_reused:
        _checkpoint(
            paths["scores"],
            run_identity,
            manifest["artifact_digest"],
            len(executions),
            batches,
            scores,
        )

    judge_candidates = [
        execution
        for execution in executions
        if execution["result"]["status"] == "success"
        and execution["execution_id"] not in scores_by_id
    ]
    if judge_candidates:
        if judge_batch is None:
            ragas_judge = _RagasBatchJudge(private_config)

            def invoke(
                samples: Sequence[Mapping[str, Any]],
                ignored_config: Mapping[str, Any],
            ) -> Mapping[str, Any]:
                del ignored_config
                return ragas_judge(samples)

        else:
            invoke = judge_batch
    next_batch = len(batches)
    for offset in range(0, len(judge_candidates), batch_size):
        selected = judge_candidates[offset : offset + batch_size]
        batch_id = f"batch-{next_batch:06d}"
        next_batch += 1
        batch_record: Dict[str, Any] = {
            "batch_id": batch_id,
            "status": "started",
            "execution_ids": [item["execution_id"] for item in selected],
            "started_at": datetime.now(timezone.utc).isoformat(),
        }
        batch_started = time.monotonic()
        batches.append(batch_record)
        _checkpoint(
            paths["scores"],
            run_identity,
            manifest["artifact_digest"],
            len(executions),
            batches,
            scores,
        )
        samples = [
            {
                "question": item["question"],
                "answer": item["result"]["answer"],
                "contexts": item["result"]["contexts"],
                "ground_truth": item["ground_truth"],
            }
            for item in selected
        ]
        histories: Dict[str, List[Dict[str, Any]]] = {
            str(item["execution_id"]): [] for item in selected
        }
        final_scores: Dict[str, Dict[str, Any]] = {}
        unresolved = list(selected)
        token_usage = {
            "input_tokens": 0,
            "output_tokens": 0,
            "total_tokens": 0,
        }
        normalized_content_responses = 0

        def add_usage(judged_result: Mapping[str, Any]) -> None:
            nonlocal normalized_content_responses
            usage = judged_result.get("token_usage")
            if isinstance(usage, Mapping):
                for field in token_usage:
                    value = usage.get(field)
                    if isinstance(value, (int, float)) and not isinstance(
                        value, bool
                    ):
                        token_usage[field] += value
            normalized = judged_result.get("normalized_content_responses")
            if isinstance(normalized, int) and not isinstance(normalized, bool):
                normalized_content_responses += normalized

        def complete_score(
            execution: Mapping[str, Any],
            metrics: Mapping[str, Any],
        ) -> Dict[str, Any]:
            execution_id = str(execution["execution_id"])
            history = histories[execution_id]
            return {
                "execution_id": execution_id,
                "case_id": execution["case_id"],
                "question_type": execution["question_type"],
                "repeat": execution["repeat"],
                "lane": execution["lane"],
                "status": "success",
                "metrics": {
                    metric: float(metrics[metric]) for metric in I2_METRICS
                },
                "error": None,
                "attempts": len(history),
                "recovered": len(history) > 1,
                "attempt_history": history,
            }

        try:
            judged = invoke(samples, private_config)
            if not isinstance(judged, Mapping):
                raise _JudgeRecordError("Judge batch returned a non-object")
            records = judged.get("records")
            if not isinstance(records, list) or len(records) != len(selected):
                raise _JudgeRecordError(
                    "Judge batch returned the wrong record count"
                )
            add_usage(judged)
            unresolved = []
            for execution, judged_record in zip(selected, records):
                execution_id = str(execution["execution_id"])
                metrics = (
                    judged_record.get("metrics")
                    if isinstance(judged_record, Mapping)
                    else None
                )
                missing = _missing_metrics(metrics)
                histories[execution_id].append(
                    {
                        "attempt": 1,
                        "status": "success" if not missing else "incomplete",
                        "missing_metrics": missing,
                    }
                )
                if missing:
                    unresolved.append(execution)
                else:
                    final_scores[execution_id] = complete_score(
                        execution,
                        metrics,
                    )
        except Exception as error:
            initial_error = _stable_judge_error(
                _judge_exception_category(error)
            )
            for execution in selected:
                histories[str(execution["execution_id"])].append(
                    {
                        "attempt": 1,
                        "status": "error",
                        "error": initial_error,
                    }
                )
            batch_record["initial_error"] = initial_error

        for execution in unresolved:
            execution_id = str(execution["execution_id"])
            sample = {
                "question": execution["question"],
                "answer": execution["result"]["answer"],
                "contexts": execution["result"]["contexts"],
                "ground_truth": execution["ground_truth"],
            }
            for attempt in range(2, record_attempts + 1):
                if judge_batch is None:
                    time.sleep(min(5 * (attempt - 1), 15))
                try:
                    retried = invoke([sample], private_config)
                    if not isinstance(retried, Mapping):
                        raise _JudgeRecordError(
                            "Judge retry returned a non-object"
                        )
                    records = retried.get("records")
                    if not isinstance(records, list) or len(records) != 1:
                        raise _JudgeRecordError(
                            "Judge retry returned the wrong record count"
                        )
                    add_usage(retried)
                    judged_record = records[0]
                    metrics = (
                        judged_record.get("metrics")
                        if isinstance(judged_record, Mapping)
                        else None
                    )
                    missing = _missing_metrics(metrics)
                    histories[execution_id].append(
                        {
                            "attempt": attempt,
                            "status": (
                                "success" if not missing else "incomplete"
                            ),
                            "missing_metrics": missing,
                        }
                    )
                    if not missing:
                        final_scores[execution_id] = complete_score(
                            execution,
                            metrics,
                        )
                        break
                except Exception as error:
                    histories[execution_id].append(
                        {
                            "attempt": attempt,
                            "status": "error",
                            "error": _stable_judge_error(
                                _judge_exception_category(error)
                            ),
                        }
                    )

            if execution_id not in final_scores:
                history = histories[execution_id]
                last_attempt = history[-1]
                final_scores[execution_id] = {
                    "execution_id": execution_id,
                    "case_id": execution["case_id"],
                    "question_type": execution["question_type"],
                    "repeat": execution["repeat"],
                    "lane": execution["lane"],
                    "status": "judge_error",
                    "metrics": None,
                    "error": (
                        last_attempt.get("error")
                        or _stable_judge_error(
                            JUDGE_METRIC_ERROR,
                            missing_metrics=last_attempt.get(
                                "missing_metrics", list(I2_METRICS)
                            ),
                        )
                    ),
                    "attempts": len(history),
                    "recovered": False,
                    "attempt_history": history,
                }

        selected_scores = [
            final_scores[str(execution["execution_id"])]
            for execution in selected
        ]
        for score in selected_scores:
            scores.append(score)
            scores_by_id[str(score["execution_id"])] = score
        batch_record["status"] = (
            "success"
            if all(score["status"] == "success" for score in selected_scores)
            else "partial"
        )
        batch_record["retry_attempts"] = sum(
            max(0, int(score.get("attempts", 1)) - 1)
            for score in selected_scores
        )
        batch_record["recovered_records"] = sum(
            bool(score.get("recovered")) for score in selected_scores
        )
        batch_record["token_usage"] = token_usage
        batch_record["normalized_content_responses"] = (
            normalized_content_responses
        )
        batch_record["completed_at"] = datetime.now(timezone.utc).isoformat()
        batch_record["elapsed_ms"] = round(
            (time.monotonic() - batch_started) * 1000,
            3,
        )
        _checkpoint(
            paths["scores"],
            run_identity,
            manifest["artifact_digest"],
            len(executions),
            batches,
            scores,
        )

    ordered_scores = [scores_by_id[str(item["execution_id"])] for item in executions]
    judge_errors = sum(score["status"] == "judge_error" for score in ordered_scores)
    judge_error_categories = Counter(
        str((score.get("error") or {}).get("category") or "unknown")
        for score in ordered_scores
        if score["status"] == "judge_error"
    )
    judge_attempt_error_categories = Counter(
        str((attempt.get("error") or {}).get("category") or "unknown")
        for score in ordered_scores
        if score["status"] != "agent_failure_zero"
        for attempt in score.get("attempt_history") or []
        if attempt.get("status") == "error"
    )
    judge_incomplete_metric_attempts = sum(
        attempt.get("status") == "incomplete"
        for score in ordered_scores
        if score["status"] != "agent_failure_zero"
        for attempt in score.get("attempt_history") or []
    )
    judge_retry_attempts = sum(
        max(0, int(score.get("attempts", 1)) - 1)
        for score in ordered_scores
        if score["status"] != "agent_failure_zero"
    )
    judge_recovered_records = sum(
        bool(score.get("recovered")) for score in ordered_scores
    )
    formal_shape = (
        agentic_manifest.get("evidence_scope") == "agentic_effectiveness"
        and formal_reference_protocol
        and agentic_manifest.get("expected_cases") == FORMAL_CASES
        and agentic_manifest.get("repeats") == FORMAL_REPEATS
        and manifest_has_verified_formal_lineage(agentic_manifest)
        and protocol_errors == 0
    )
    evidence_complete = len(ordered_scores) == len(executions) and judge_errors == 0
    comparison = None
    averaging_error = None
    if evidence_complete:
        try:
            averaged = average_repeats_by_case(
                ordered_scores,
                int(agentic_manifest["repeats"]),
            )
            comparison = stratified_paired_comparison(
                averaged,
                resamples=bootstrap_resamples,
                seed=bootstrap_seed,
            )
        except ValueError:
            averaging_error = {"category": "paired_aggregation_error"}
            evidence_complete = False

    failures = {
        lane: sum(
            execution["lane"] == lane
            and execution["result"]["status"] != "success"
            for execution in executions
        )
        for lane in ("baseline", "contextual")
    }
    executions_per_lane = len(executions) // 2
    gate = evaluate_i2_gate(
        comparison,
        evidence_complete=evidence_complete and formal_shape,
        baseline_agent_failures=failures["baseline"],
        contextual_agent_failures=failures["contextual"],
        executions_per_lane=executions_per_lane,
    )
    is_smoke = (agentic_manifest.get("selection") or {}).get(
        "smoke_per_type"
    ) is not None
    smoke = None
    if is_smoke:
        smoke = evaluate_agentic_smoke_gate(
            failures,
            executions_per_lane,
            chain_complete=evidence_complete,
            protocol_errors=protocol_errors,
            judge_errors=judge_errors,
        )
        gate = {
            "decision": "not_applicable",
            "reason": "smoke validates the chain and never gates I2 by effect size",
        }
    valid_evidence = (
        smoke["decision"] == "pass"
        if smoke is not None
        else evidence_complete and formal_shape
    )
    if not scores_reused:
        scores_artifact = _checkpoint(
            paths["scores"],
            run_identity,
            manifest["artifact_digest"],
            len(executions),
            batches,
            ordered_scores,
        )
    if scores_artifact is None:
        raise RuntimeError("Judge scores artifact was not finalized")
    report_payload = {
        "schema_version": AGENTIC_JUDGE_REPORT_SCHEMA,
        "run_identity": run_identity,
        "evidence_scope": agentic_manifest["evidence_scope"],
        "evidence_status": "valid" if valid_evidence else "insufficient",
        "formal_ab_eligible": evidence_complete and formal_shape,
        "reference_profile": (
            FORMAL_JUDGE_PROFILE if formal_shape else None
        ),
        "manifest_digest": manifest["artifact_digest"],
        "scores_digest": scores_artifact["artifact_digest"],
        "expected_scores": len(executions),
        "completed_scores": len(ordered_scores),
        "judge_errors": judge_errors,
        "judge_error_categories": dict(judge_error_categories),
        "judge_attempt_error_categories": dict(
            judge_attempt_error_categories
        ),
        "judge_incomplete_metric_attempts": (
            judge_incomplete_metric_attempts
        ),
        "judge_retry_attempts": judge_retry_attempts,
        "judge_recovered_records": judge_recovered_records,
        "paired_valid_cases": (
            comparison.get("samples") if comparison is not None else 0
        ),
        "averaging_error": averaging_error,
        "agent_failures": failures,
        "raw_protocol_errors": raw_protocol_errors,
        "protocol_errors": protocol_errors,
        "agent_result_normalization": agent_result_normalization,
        "success_only_diagnostics": _success_only_diagnostics(
            executions,
            ordered_scores,
            int(agentic_manifest["repeats"]),
        ),
        "mechanism": {
            lane: _mechanism_summary(executions, lane)
            for lane in ("baseline", "contextual")
        },
        "comparison": comparison,
        "gate": gate,
        "smoke": smoke,
    }
    if os.path.exists(paths["report"]):
        report = load_artifact(paths["report"], AGENTIC_JUDGE_REPORT_SCHEMA)
        expected_fields = set(report_payload) | {
            "completed_at",
            "artifact_digest",
        }
        if set(report) != expected_fields or any(
            report.get(field) != expected
            for field, expected in report_payload.items()
        ):
            raise ValueError("existing Judge report differs from frozen scores")
        return report
    return write_artifact(
        paths["report"],
        {
            **report_payload,
            "completed_at": datetime.now(timezone.utc).isoformat(),
        },
    )
