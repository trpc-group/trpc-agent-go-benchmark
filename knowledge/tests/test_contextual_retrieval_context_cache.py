#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for Context JSONL cache validation."""

import io
import json
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

from contextual_retrieval.artifacts import (
    canonical_digest,
    text_digest,
    write_artifact,
)
from contextual_retrieval.context_cache import (
    CONTEXT_CACHE_SCHEMA,
    _generate_one,
    _new_openai_client,
    _retry_delay,
    generate_context_cache,
    probe_context_generation,
    select_probe_chunks,
    summarize_context_cache,
)
from contextual_retrieval.dataset import CHUNK_SCHEMA, PARENT_SCHEMA


class ContextCacheTest(unittest.TestCase):
    def test_context_set_digest_matches_go_test_vector(self):
        value = [
            {
                "chunk_id": "chunk-1",
                "context_hash": text_digest("Alpha context"),
            },
            {
                "chunk_id": "chunk-2",
                "context_hash": text_digest("Beta context"),
            },
        ]
        self.assertEqual(
            "106d98bbb5d3ede84e002004107e1a747dcb13c7a2a129dcf51107da1d2cbff1",
            canonical_digest(value),
        )

    def test_context_client_disables_hidden_transport_retries(self):
        captured = {}

        def openai_client(**kwargs):
            captured.update(kwargs)
            return object()

        fake_openai = types.ModuleType("openai")
        fake_openai.OpenAI = openai_client
        with mock.patch.dict(sys.modules, {"openai": fake_openai}):
            _new_openai_client(
                {
                    "api_key": "secret",
                    "base_url": "https://context.test/v1",
                    "headers": {},
                    "timeout_seconds": 30,
                }
            )

        self.assertEqual(0, captured["max_retries"])

    def _manifests(self, root, chunks_count=3):
        parent_content = "Title: Test\n\n" + "parent body " * 80
        parents = write_artifact(
            str(root / "parents.json"),
            {
                "schema_version": PARENT_SCHEMA,
                "parents_count": 1,
                "parents": [
                    {
                        "parent_document_id": "parent-1",
                        "content": parent_content,
                        "content_hash": "parent-hash",
                    }
                ],
            },
        )
        chunk_records = [
            {
                "chunk_id": f"chunk-{index}",
                "parent_document_id": "parent-1",
                "parent_content_hash": "parent-hash",
                "chunk_content_hash": f"chunk-hash-{index}",
                "chunk_index": index,
                "content": f"chunk content {index}",
            }
            for index in range(chunks_count)
        ]
        chunks = write_artifact(
            str(root / "chunks.json"),
            {
                "schema_version": CHUNK_SCHEMA,
                "parent_manifest_digest": parents["artifact_digest"],
                "chunk_size": 500,
                "chunk_overlap": 50,
                "chunks_count": chunks_count,
                "chunks": chunk_records,
            },
        )
        return parents, chunks

    def _client(self, fail_first=False):
        state = {"calls": 0}

        class Completions:
            def create(self, **kwargs):
                del kwargs
                state["calls"] += 1
                if fail_first and state["calls"] == 1:
                    raise RuntimeError("transient secret failure")
                message = type(
                    "Message",
                    (),
                    {"content": f"Document context {state['calls']}"},
                )()
                choice = type(
                    "Choice",
                    (),
                    {"message": message, "finish_reason": "stop"},
                )()
                usage = type(
                    "Usage",
                    (),
                    {
                        "prompt_tokens": 10,
                        "completion_tokens": 2,
                        "total_tokens": 12,
                    },
                )()
                return type(
                    "Response",
                    (),
                    {"choices": [choice], "usage": usage},
                )()

        client = type(
            "Client",
            (),
            {"chat": type("Chat", (), {"completions": Completions()})()},
        )()
        return client, state

    def test_generation_omits_reasoning_parameter(self):
        captured = {}

        class Completions:
            def create(self, **kwargs):
                captured.update(kwargs)
                message = type("Message", (), {"content": "Document context"})()
                choice = type(
                    "Choice",
                    (),
                    {"message": message, "finish_reason": "stop"},
                )()
                usage = type(
                    "Usage",
                    (),
                    {
                        "prompt_tokens": 10,
                        "completion_tokens": 2,
                        "total_tokens": 12,
                    },
                )()
                return type(
                    "Response",
                    (),
                    {"choices": [choice], "usage": usage},
                )()

        client = type(
            "Client",
            (),
            {"chat": type("Chat", (), {"completions": Completions()})()},
        )()
        result = _generate_one(
            client,
            {
                "model": "context-model",
                "max_tokens": 4096,
                "api_key": "secret",
            },
            {"content": "parent"},
            {
                "chunk_id": "chunk-1",
                "parent_document_id": "parent-1",
                "parent_content_hash": "parent-hash",
                "chunk_content_hash": "chunk-hash",
                "content": "chunk",
            },
            1,
        )

        self.assertEqual("success", result["status"])
        self.assertEqual(4096, captured["max_tokens"])
        self.assertNotIn("reasoning", captured)
        self.assertEqual("stop", result["finish_reason"])
        self.assertEqual(10, result["usage"]["prompt_tokens"])
        self.assertGreater(result["input_characters"]["prompt_characters"], 0)

    def test_generation_rejects_truncated_finish_and_retains_audit_fields(self):
        class Completions:
            def create(self, **kwargs):
                del kwargs
                message = type("Message", (), {"content": "Partial context"})()
                choice = type(
                    "Choice",
                    (),
                    {"message": message, "finish_reason": "length"},
                )()
                usage = type(
                    "Usage",
                    (),
                    {
                        "prompt_tokens": 100,
                        "completion_tokens": 4096,
                        "total_tokens": 4196,
                    },
                )()
                return type(
                    "Response",
                    (),
                    {"choices": [choice], "usage": usage},
                )()

        client = type(
            "Client",
            (),
            {"chat": type("Chat", (), {"completions": Completions()})()},
        )()
        result = _generate_one(
            client,
            {
                "model": "context-model",
                "max_tokens": 4096,
                "api_key": "secret",
            },
            {"content": "parent"},
            {
                "chunk_id": "chunk-1",
                "parent_document_id": "parent-1",
                "parent_content_hash": "parent-hash",
                "chunk_content_hash": "chunk-hash",
                "content": "chunk",
            },
            1,
        )

        self.assertEqual("error", result["status"])
        self.assertEqual("length", result["finish_reason"])
        self.assertEqual(100, result["usage"]["prompt_tokens"])
        self.assertEqual(6, result["input_characters"]["parent_characters"])
        self.assertIsNone(result["context"])
        self.assertIsNone(result["context_hash"])

    def test_retry_delay_is_bounded_exponential(self):
        config = {
            "retry_base_delay_seconds": 2,
            "retry_max_delay_seconds": 5,
        }
        self.assertEqual(0, _retry_delay(config, 1))
        self.assertEqual(2, _retry_delay(config, 2))
        self.assertEqual(4, _retry_delay(config, 3))
        self.assertEqual(5, _retry_delay(config, 4))

    def test_generation_error_redacts_credentials_and_url_query(self):
        class Completions:
            def create(self, **kwargs):
                del kwargs
                raise RuntimeError(
                    "api-secret routing-secret "
                    "https://context.test/v1?token=url-secret"
                )

        client = type(
            "Client",
            (),
            {"chat": type("Chat", (), {"completions": Completions()})()},
        )()
        result = _generate_one(
            client,
            {
                "model": "context-model",
                "max_tokens": 4096,
                "api_key": "api-secret",
                "base_url": "https://context.test/v1?token=url-secret",
                "headers": {"X-SMG-Routing-Key": "routing-secret"},
            },
            {"content": "parent"},
            {
                "chunk_id": "chunk-1",
                "parent_document_id": "parent-1",
                "parent_content_hash": "parent-hash",
                "chunk_content_hash": "chunk-hash",
                "content": "chunk",
            },
            1,
        )

        self.assertEqual("error", result["status"])
        for secret in ("api-secret", "routing-secret", "url-secret"):
            self.assertNotIn(secret, result["error"])
        self.assertIn("https://context.test/v1", result["error"])

    def test_probe_is_deterministic_and_does_not_create_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            parents, chunks = self._manifests(root)
            first = select_probe_chunks(parents, chunks, count=2)
            second = select_probe_chunks(parents, chunks, count=2)
            self.assertEqual(first, second)
            client, _ = self._client()
            report = probe_context_generation(
                str(root / "parents.json"),
                str(root / "chunks.json"),
                str(root / "probe.json"),
                {
                    "model": "context-model",
                    "api_key": "secret",
                    "base_url": "https://context.test/v1?token=secret",
                    "headers": {},
                    "max_tokens": 4096,
                    "retry_base_delay_seconds": 0,
                    "retry_max_delay_seconds": 0,
                },
                count=2,
                client=client,
            )

            self.assertFalse((root / "contexts.jsonl").exists())

        self.assertEqual("valid", report["status"])
        self.assertEqual(2, len(report["results"]))
        self.assertEqual("unspecified", report["config"]["reasoning"])
        self.assertEqual("https://context.test/v1", report["config"]["endpoint"])

    def test_generation_retries_and_emits_progress(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._manifests(root, chunks_count=1)
            client, state = self._client(fail_first=True)
            progress = io.StringIO()
            config = {
                "model": "context-model",
                "api_key": "secret",
                "base_url": "https://context.test/v1",
                "headers": {},
                "max_tokens": 4096,
                "retry_base_delay_seconds": 0,
                "retry_max_delay_seconds": 0,
            }
            with mock.patch(
                "contextual_retrieval.context_cache._new_openai_client",
                return_value=client,
            ):
                summary = generate_context_cache(
                    str(root / "parents.json"),
                    str(root / "chunks.json"),
                    str(root / "contexts.jsonl"),
                    config,
                    workers=1,
                    attempts_per_run=2,
                    progress_stream=progress,
                )

        self.assertEqual(2, state["calls"])
        self.assertEqual("valid", summary["status"])
        self.assertEqual(2, summary["attempt_records"])
        events = [
            json.loads(line)["event"]
            for line in progress.getvalue().splitlines()
        ]
        self.assertEqual(
            ["context_generation_started", "context_generation_finished"],
            events,
        )

    def test_latest_successful_attempt_completes_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            chunks = write_artifact(
                str(root / "chunks.json"),
                {
                    "schema_version": CHUNK_SCHEMA,
                    "parent_manifest_digest": "parents",
                    "chunk_size": 500,
                    "chunk_overlap": 50,
                    "chunks_count": 1,
                    "chunks": [
                        {
                            "chunk_id": "chunk-1",
                            "parent_document_id": "parent-1",
                            "parent_content_hash": "parent-hash",
                            "chunk_content_hash": "chunk-hash",
                        }
                    ],
                },
            )
            records = [
                {
                    "record_type": "header",
                    "schema_version": CONTEXT_CACHE_SCHEMA,
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "parent_manifest_digest": chunks[
                        "parent_manifest_digest"
                    ],
                    "cache_identity": "cache-1",
                    "config": {},
                },
                {
                    "record_type": "attempt",
                    "chunk_id": "chunk-1",
                    "attempt": 1,
                    "status": "error",
                    "context": None,
                },
                {
                    "record_type": "attempt",
                    "chunk_id": "chunk-1",
                    "attempt": 2,
                    "status": "success",
                    "parent_document_id": "parent-1",
                    "parent_content_hash": "parent-hash",
                    "chunk_content_hash": "chunk-hash",
                    "context": "This chunk discusses the launch date.",
                    "context_hash": text_digest(
                        "This chunk discusses the launch date."
                    ),
                    "finish_reason": "stop",
                    "usage": {"prompt_tokens": 10},
                    "input_characters": {
                        "parent_characters": 100,
                        "chunk_characters": 20,
                        "prompt_characters": 200,
                    },
                },
            ]
            cache_path = root / "contexts.jsonl"
            cache_path.write_text(
                "".join(json.dumps(record) + "\n" for record in records),
                encoding="utf-8",
            )

            summary = summarize_context_cache(
                str(cache_path),
                str(root / "chunks.json"),
            )

        self.assertEqual("valid", summary["status"])
        self.assertEqual(1, summary["successful_chunks"])
        self.assertEqual(2, summary["attempt_records"])
        self.assertEqual(
            canonical_digest(
                [
                    {
                        "chunk_id": "chunk-1",
                        "context_hash": text_digest(
                            "This chunk discusses the launch date."
                        ),
                    }
                ]
            ),
            summary["context_set_digest"],
        )
        self.assertEqual({"stop": 1}, summary["latest_success_finish_reason_counts"])
        self.assertEqual(10, summary["latest_success_usage_totals"]["prompt_tokens"])
        self.assertEqual(
            200,
            summary["latest_success_input_character_totals"][
                "prompt_characters"
            ],
        )

    def test_summary_rejects_context_hash_or_identity_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, chunks = self._manifests(root, chunks_count=1)
            chunk = chunks["chunks"][0]
            base_attempt = {
                "record_type": "attempt",
                "chunk_id": chunk["chunk_id"],
                "attempt": 1,
                "status": "success",
                "parent_document_id": chunk["parent_document_id"],
                "parent_content_hash": chunk["parent_content_hash"],
                "chunk_content_hash": chunk["chunk_content_hash"],
                "context": "Bound context",
                "context_hash": text_digest("Bound context"),
                "finish_reason": "stop",
            }
            header = {
                "record_type": "header",
                "schema_version": CONTEXT_CACHE_SCHEMA,
                "chunk_manifest_digest": chunks["artifact_digest"],
                "parent_manifest_digest": chunks["parent_manifest_digest"],
                "cache_identity": "cache-1",
            }
            cache_path = root / "contexts.jsonl"
            for field, value in (
                ("context_hash", "wrong"),
                ("parent_document_id", "wrong-parent"),
            ):
                attempt = {**base_attempt, field: value}
                cache_path.write_text(
                    json.dumps(header) + "\n" + json.dumps(attempt) + "\n",
                    encoding="utf-8",
                )
                with self.subTest(field=field), self.assertRaises(ValueError):
                    summarize_context_cache(
                        str(cache_path),
                        str(root / "chunks.json"),
                    )

    def test_summary_rejects_unknown_chunk(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, chunks = self._manifests(root, chunks_count=1)
            records = [
                {
                    "record_type": "header",
                    "schema_version": CONTEXT_CACHE_SCHEMA,
                    "chunk_manifest_digest": chunks["artifact_digest"],
                    "parent_manifest_digest": chunks[
                        "parent_manifest_digest"
                    ],
                    "cache_identity": "cache-1",
                },
                {
                    "record_type": "attempt",
                    "chunk_id": "unknown",
                    "attempt": 1,
                    "status": "error",
                },
            ]
            path = root / "contexts.jsonl"
            path.write_text(
                "".join(json.dumps(record) + "\n" for record in records),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "unknown chunk"):
                summarize_context_cache(str(path), str(root / "chunks.json"))


if __name__ == "__main__":
    unittest.main()
