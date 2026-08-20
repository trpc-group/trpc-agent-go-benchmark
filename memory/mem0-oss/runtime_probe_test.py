#!/usr/bin/env python3
# Tencent is pleased to support the open source community by making
# trpc-agent-go-benchmark available.
#
# Copyright (C) 2026 Tencent. All rights reserved.
#
# trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

"""Tests for the in-container, non-secret Mem0 runtime probe."""

import io
import json
import os
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import runtime_probe


RUNTIME_ENV = {
    "AUTH_DISABLED": "true",
    "MEM0_DEFAULT_EMBEDDER_MODEL": "text-embedding-3-small",
    "MEM0_DEFAULT_LLM_MODEL": "gpt-4o-mini",
    "MEM0_SOURCE_COMMIT": "3b9aed866ae70d29043388ed0ae5cc4e1844f3e8",
    "MEM0_TELEMETRY": "false",
    "OPENAI_BASE_URL": "http://host.docker.internal:18080/v1",
    "POSTGRES_COLLECTION_NAME": "memories",
    "POSTGRES_DB": "postgres",
    "POSTGRES_HOST": "postgres",
    "POSTGRES_PASSWORD": "database-secret",
    "POSTGRES_PORT": "5432",
    "POSTGRES_USER": "postgres",
}

SUPPORTED_PROMPT_SOURCE = """
class Memory:
    def add(self, messages, *, prompt=None):
        return self._add_to_vector_store(messages, {}, {}, True, prompt=prompt)

    def _add_to_vector_store(self, messages, metadata, filters, infer, prompt=None):
        system_prompt = ADDITIVE_EXTRACTION_PROMPT
        custom_instr = prompt or self.custom_instructions
        user_prompt = generate_additive_extraction_prompt(
            new_messages=messages,
            custom_instructions=custom_instr,
        )
        return self.llm.generate_response(
            messages=[
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_prompt},
            ]
        )
"""


def database_mocks(rows):
    cursor = mock.MagicMock()
    cursor.__enter__.return_value = cursor
    cursor.fetchone.side_effect = rows
    connection = mock.MagicMock()
    connection.__enter__.return_value = connection
    connection.cursor.return_value = cursor
    connect = mock.Mock(return_value=connection)
    return connect, cursor


class URLTest(unittest.TestCase):
    def test_safe_url_accepts_locked_proxy(self):
        self.assertEqual(
            runtime_probe.safe_url("http://host.docker.internal:18080/v1"),
            "http://host.docker.internal:18080/v1",
        )

    def test_safe_url_rejects_credentials(self):
        with self.assertRaisesRegex(runtime_probe.ProbeError, "must not contain credentials"):
            runtime_probe.safe_url("http://alice:secret@example.test/v1")


class ProcessTest(unittest.TestCase):
    def test_only_official_uvicorn_process_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "1").mkdir()
            (root / "1" / "cmdline").write_bytes(b"/sbin/docker-init\0")
            (root / "7").mkdir()
            command = b"/usr/local/bin/python\0/usr/local/bin/uvicorn\0main:app\0--workers\0" b"1\0"
            (root / "7" / "cmdline").write_bytes(command)
            self.assertIn("main:app", runtime_probe.find_server_command(root))

    def test_benchmark_wrapper_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "7").mkdir()
            command = b"uvicorn\0main:app\0benchmark_entrypoint\0"
            (root / "7" / "cmdline").write_bytes(command)
            with self.assertRaisesRegex(runtime_probe.ProbeError, "wrapper is forbidden"):
                runtime_probe.find_server_command(root)

    def test_missing_server_process_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(runtime_probe.ProbeError, "exactly one"):
                runtime_probe.find_server_command(Path(directory))


class SourceIdentityTest(unittest.TestCase):
    def _source_tree(self, directory):
        root = Path(directory) / "opt" / "mem0"
        package = root / "mem0"
        memory = package / "memory"
        memory.mkdir(parents=True)
        init = package / "__init__.py"
        main = memory / "main.py"
        init.write_text("", encoding="utf-8")
        main.write_text(SUPPORTED_PROMPT_SOURCE, encoding="utf-8")
        return root, init, main

    def test_imported_module_must_resolve_to_read_only_locked_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root, init, main = self._source_tree(directory)
            paths = [root, root / "mem0", root / "mem0" / "memory", init, main]
            try:
                for path in paths:
                    path.chmod(0o555 if path.is_dir() else 0o444)
                memory_main, facts = runtime_probe.locked_mem0_source_facts(
                    root,
                    module=SimpleNamespace(__file__=str(init)),
                )
                self.assertEqual(memory_main, main)
                self.assertEqual(facts["module_file"], str(init))
                self.assertTrue(facts["tree_read_only"])
            finally:
                for path in paths:
                    path.chmod(0o755 if path.is_dir() else 0o644)

    def test_site_packages_module_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, _ = self._source_tree(directory)
            outside = Path(directory) / "site-packages" / "mem0" / "__init__.py"
            outside.parent.mkdir(parents=True)
            outside.write_text("", encoding="utf-8")
            with self.assertRaisesRegex(runtime_probe.ProbeError, "outside the locked source tree"):
                runtime_probe.locked_mem0_source_facts(
                    root,
                    module=SimpleNamespace(__file__=str(outside)),
                )

    def test_writable_locked_source_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, init, _ = self._source_tree(directory)
            with self.assertRaisesRegex(runtime_probe.ProbeError, "writable paths"):
                runtime_probe.locked_mem0_source_facts(
                    root,
                    module=SimpleNamespace(__file__=str(init)),
                )


class PromptSemanticsTest(unittest.TestCase):
    def test_add_prompt_is_verified_as_custom_instructions(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "main.py"
            path.write_text(SUPPORTED_PROMPT_SOURCE, encoding="utf-8")
            self.assertEqual(
                runtime_probe.verify_add_prompt_semantics(path),
                runtime_probe.PROMPT_SEMANTICS,
            )

    def test_replacement_system_prompt_is_rejected(self):
        source = SUPPORTED_PROMPT_SOURCE.replace(
            "system_prompt = ADDITIVE_EXTRACTION_PROMPT",
            "system_prompt = prompt or ADDITIVE_EXTRACTION_PROMPT",
        )
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "main.py"
            path.write_text(source, encoding="utf-8")
            with self.assertRaisesRegex(runtime_probe.ProbeError, "semantics are unsupported"):
                runtime_probe.verify_add_prompt_semantics(path)

    def test_unsupported_semantics_are_reported_without_source_details(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "main.py"
            path.write_text("class Memory: pass\n", encoding="utf-8")
            self.assertEqual(runtime_probe.detect_add_prompt_semantics(path), "unsupported")


class NLPTest(unittest.TestCase):
    def test_valid_outputs_prove_lemmatization_and_entities(self):
        facts = runtime_probe.validate_nlp_outputs(
            "3.8.14",
            "3.8.0",
            ["tok2vec", "tagger", "parser", "attribute_ruler", "lemmatizer", "ner"],
            ["tok2vec", "tagger", "attribute_ruler", "lemmatizer"],
            "alice attend meeting acme corporation",
            [("PROPER", "Alice"), ("PROPER", "Acme Corporation")],
        )
        self.assertTrue(facts["lemmatization_verified"])
        self.assertTrue(facts["entity_extraction_verified"])

    def test_unlemmatized_output_is_rejected(self):
        with self.assertRaisesRegex(runtime_probe.ProbeError, "lemmatization capability"):
            runtime_probe.validate_nlp_outputs(
                "3.8.14",
                "3.8.0",
                ["lemmatizer", "ner"],
                ["lemmatizer"],
                "alice attended meetings at acme corporation",
                [("PROPER", "Alice"), ("PROPER", "Acme Corporation")],
            )

    def test_missing_entities_are_rejected(self):
        with self.assertRaisesRegex(runtime_probe.ProbeError, "entity extraction capability"):
            runtime_probe.validate_nlp_outputs(
                "3.8.14",
                "3.8.0",
                ["lemmatizer", "ner"],
                ["lemmatizer"],
                "alice attend meeting acme corporation",
                [],
            )


class DatabaseTest(unittest.TestCase):
    def test_versions_and_vector_capabilities_are_measured(self):
        rows = [
            ("17.6 (Debian 17.6-1.pgdg12+1)",),
            ("170006",),
            ("0.8.0",),
            (3,),
            (1.0,),
            (True,),
        ]
        connect, cursor = database_mocks(rows)
        with mock.patch.dict(os.environ, RUNTIME_ENV, clear=True):
            facts = runtime_probe.database_facts(connect=connect, database_error=RuntimeError)
        self.assertTrue(facts["pgvector_installed"])
        self.assertTrue(facts["hnsw_available"])
        self.assertEqual(facts["pgvector_version"], "0.8.0")
        self.assertEqual(cursor.execute.call_count, 6)
        rendered = json.dumps(facts)
        self.assertNotIn("database-secret", rendered)

    def test_missing_extension_is_rejected(self):
        rows = [
            ("17.6 (Debian 17.6-1.pgdg12+1)",),
            ("170006",),
            None,
        ]
        connect, _ = database_mocks(rows)
        with mock.patch.dict(os.environ, RUNTIME_ENV, clear=True):
            with self.assertRaisesRegex(runtime_probe.ProbeError, "not installed"):
                runtime_probe.database_facts(connect=connect, database_error=RuntimeError)

    def test_broken_vector_operator_is_rejected(self):
        rows = [
            ("17.6 (Debian 17.6-1.pgdg12+1)",),
            ("170006",),
            ("0.8.0",),
            (3,),
            (2.0,),
            (True,),
        ]
        connect, _ = database_mocks(rows)
        with mock.patch.dict(os.environ, RUNTIME_ENV, clear=True):
            with self.assertRaisesRegex(runtime_probe.ProbeError, "operations are unavailable"):
                runtime_probe.database_facts(connect=connect, database_error=RuntimeError)


class ReportTest(unittest.TestCase):
    @mock.patch(
        "runtime_probe.detect_add_prompt_semantics",
        return_value=runtime_probe.PROMPT_SEMANTICS,
    )
    @mock.patch(
        "runtime_probe.locked_mem0_source_facts",
        return_value=(
            Path("/opt/mem0/mem0/memory/main.py"),
            {
                "root": "/opt/mem0",
                "module_file": "/opt/mem0/mem0/__init__.py",
                "tree_read_only": True,
                "checked_paths": 100,
            },
        ),
    )
    @mock.patch("runtime_probe.database_facts")
    @mock.patch("runtime_probe.find_server_command")
    @mock.patch("runtime_probe.file_sha256")
    @mock.patch("runtime_probe.importlib.metadata.version")
    @mock.patch("runtime_probe.platform.machine")
    @mock.patch("runtime_probe.platform.python_version")
    def test_report_contains_facts_but_no_secrets(
        self,
        python_version,
        machine,
        distribution_version,
        file_sha256,
        find_server_command,
        database_facts,
        locked_mem0_source_facts,
        detect_add_prompt_semantics,
    ):
        del locked_mem0_source_facts, detect_add_prompt_semantics
        python_version.return_value = "3.12.13"
        machine.return_value = "x86_64"
        distribution_version.return_value = "2.0.11"
        file_sha256.side_effect = [
            "archive-digest",
            "server-main-digest",
            "memory-main-digest",
            "lock-digest",
            "requirements-digest",
            "spacy-model-digest",
        ]
        find_server_command.return_value = ["uvicorn", "main:app", "--workers", "1"]
        database_facts.return_value = {"pgvector_installed": True}
        nlp = {
            "spacy_version": "3.8.14",
            "model_name": "en_core_web_sm",
            "model_version": "3.8.0",
            "full_pipeline": ["lemmatizer", "ner"],
            "lemma_pipeline": ["lemmatizer"],
            "lemmatization_verified": True,
            "entity_extraction_verified": True,
        }
        with mock.patch.dict(os.environ, RUNTIME_ENV, clear=True), mock.patch(
            "runtime_probe.nlp_facts", return_value=nlp
        ):
            report = runtime_probe.collect_facts()
        rendered = json.dumps(report, sort_keys=True)
        self.assertEqual(report["server"]["module"], "main:app")
        self.assertEqual(report["capabilities"]["add_prompt_semantics"], runtime_probe.PROMPT_SEMANTICS)
        self.assertTrue(report["nlp"]["entity_extraction_verified"])
        self.assertNotIn("database-secret", rendered)
        self.assertNotIn("POSTGRES_PASSWORD", rendered)

    @mock.patch("runtime_probe.collect_facts", side_effect=runtime_probe.ProbeError("secret detail"))
    def test_failure_output_is_generic(self, collect_facts):
        del collect_facts
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            self.assertEqual(runtime_probe.main(), 1)
        self.assertEqual(stderr.getvalue(), "Mem0 runtime probe failed\n")


if __name__ == "__main__":
    unittest.main()
