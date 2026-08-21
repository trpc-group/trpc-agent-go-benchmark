#!/usr/bin/env python3
# Tencent is pleased to support the open source community by making
# trpc-agent-go-benchmark available.
#
# Copyright (C) 2026 Tencent. All rights reserved.
#
# trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

"""Tests for the fail-closed Mem0 OSS benchmark preflight."""

import copy
import hashlib
import json
import os
import re
import stat
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import preflight


HERE = Path(__file__).resolve().parent


def digest_text(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def repository_lock():
    return json.loads((HERE / "environment.lock.json").read_text(encoding="utf-8"))


def write_lock_fixture(root, lock, requirements_input=None, requirements_lock=None, initialization=None):
    """Write a self-consistent temporary lock and requirements pair."""
    if requirements_input is None:
        requirements_input = (HERE / "requirements.in").read_text(encoding="utf-8")
    if requirements_lock is None:
        requirements_lock = (HERE / "requirements.lock").read_text(encoding="utf-8")
    if initialization is None:
        initialization = (HERE / "init-db.sql").read_text(encoding="utf-8")
    lock = copy.deepcopy(lock)
    for artifact in lock["deployment"].values():
        contents = (HERE / artifact["path"]).read_bytes()
        artifact["sha256"] = hashlib.sha256(contents).hexdigest()
        (root / artifact["path"]).write_bytes(contents)
    requirements = lock["python"]["requirements"]
    requirements["input_sha256"] = digest_text(requirements_input)
    requirements["lock_sha256"] = digest_text(requirements_lock)
    (root / requirements["input_path"]).write_text(requirements_input, encoding="utf-8")
    (root / requirements["lock_path"]).write_text(requirements_lock, encoding="utf-8")
    database_initialization = lock["database"]["initialization"]
    database_initialization["sha256"] = digest_text(initialization)
    (root / database_initialization["path"]).write_text(initialization, encoding="utf-8")
    path = root / "environment.lock.json"
    path.write_text(json.dumps(lock), encoding="utf-8")
    return path


def valid_probe(lock, lock_digest):
    postgres = lock["database"]["postgres"]
    runtime = lock["runtime"]
    return {
        "source": {
            "commit": lock["mem0"]["commit"],
            "archive_sha256": lock["mem0"]["source_archive"]["sha256"],
            "server_main_sha256": lock["mem0"]["server_main_sha256"],
            "memory_main_sha256": lock["mem0"]["runtime_source"]["memory_main_sha256"],
            "identity": {
                "root": lock["mem0"]["runtime_source"]["root"],
                "module_file": lock["mem0"]["runtime_source"]["module_file"],
                "tree_read_only": True,
                "checked_paths": 100,
            },
        },
        "artifacts": {
            "environment_lock_sha256": lock_digest,
            "requirements_lock_sha256": lock["python"]["requirements"]["lock_sha256"],
            "spacy_model_sha256": lock["python"]["nlp"]["model"]["sha256"],
        },
        "python": {
            "version": lock["python"]["version"],
            "implementation": "CPython",
            "machine": "x86_64",
        },
        "distribution": lock["mem0"]["distribution"],
        "server": {"module": "main:app", "workers": "1"},
        "runtime": {
            "auth_disabled": True,
            "telemetry_enabled": False,
            "openai_base_url": runtime["llm"]["base_url"],
            "llm_model": runtime["llm"]["model"],
            "embedder_model": runtime["embedder"]["model"],
            "collection_name": runtime["vector_store"]["collection"],
        },
        "capabilities": {"add_prompt_semantics": preflight.PROMPT_SEMANTICS},
        "nlp": {
            "spacy_version": lock["python"]["nlp"]["spacy_version"],
            "model_name": lock["python"]["nlp"]["model"]["name"],
            "model_version": lock["python"]["nlp"]["model"]["version"],
            "full_pipeline": lock["python"]["nlp"]["required_pipeline_components"],
            "lemma_pipeline": ["attribute_ruler", "lemmatizer", "tagger", "tok2vec"],
            "lemmatization_verified": True,
            "entity_extraction_verified": True,
        },
        "database": {
            "postgres_version": "%s (%s)" % (postgres["version"], postgres["distribution"]),
            "postgres_version_num": postgres["version_num"],
            "pgvector_installed": True,
            "pgvector_version": lock["database"]["pgvector"]["version"],
            "vector_dimensions": 3,
            "l2_distance": 1.0,
            "hnsw_available": True,
        },
    }


def valid_api(lock):
    runtime = lock["runtime"]
    openapi = {
        "info": {
            "title": runtime["server"]["api_title"],
            "version": runtime["server"]["api_version"],
        },
        "paths": {
            path: {method: {} for method in methods}
            for path, methods in preflight.REQUIRED_API_METHODS.items()
        },
        "components": {
            "schemas": {
                "MemoryCreate": {
                    "properties": {
                        "prompt": {"anyOf": [{"type": "string"}, {"type": "null"}]},
                    }
                },
                "SearchRequest": {
                    "properties": {
                        "explain": {"anyOf": [{"type": "boolean"}, {"type": "null"}]},
                    }
                },
            }
        },
    }
    openapi["paths"]["/memories"]["post"] = {
        "requestBody": {
            "content": {
                "application/json": {"schema": {"$ref": "#/components/schemas/MemoryCreate"}}
            }
        }
    }
    openapi["paths"]["/search"]["post"] = {
        "requestBody": {
            "content": {
                "application/json": {"schema": {"$ref": "#/components/schemas/SearchRequest"}}
            }
        }
    }
    vector = runtime["vector_store"]
    config = {
        "version": "v1.1",
        "vector_store": {
            "provider": vector["provider"],
            "config": {
                "host": vector["host"],
                "port": vector["port"],
                "dbname": vector["database"],
                "user": "postgres",
                "password": "[redacted]",
                "collection_name": vector["collection"],
            },
        },
        "llm": {
            "provider": runtime["llm"]["provider"],
            "config": {"api_key": "[redacted]", "model": runtime["llm"]["model"]},
        },
        "embedder": {
            "provider": runtime["embedder"]["provider"],
            "config": {"api_key": "[redacted]", "model": runtime["embedder"]["model"]},
        },
    }
    providers = {"llm": ["openai", "anthropic"], "embedder": ["openai"]}
    return openapi, config, providers


def prompted_memory(payload):
    """Model a result that proves both observation content and prompt-only marker survived."""
    marker = re.search(r"OBSERVATION_KEEP_[0-9a-f]+", payload["prompt"]).group(0)
    source = re.search(r"OBSERVATION_SOURCE_[0-9a-f]+", payload["messages"][0]["content"]).group(0)
    return "Alice attended meetings at Acme Corporation in London using %s. %s" % (
        source,
        marker,
    )


class LockTest(unittest.TestCase):
    def test_repository_lock_is_valid(self):
        lock = preflight.load_lock(HERE / "environment.lock.json")
        self.assertEqual(lock["python"]["version"], "3.12.13")
        self.assertEqual(lock["database"]["postgres"]["version"], "17.6")

    def test_floating_image_reference_is_rejected(self):
        lock = repository_lock()
        lock["database"]["pgvector"]["image_reference"] = "docker.io/pgvector/pgvector:latest"
        with tempfile.TemporaryDirectory() as directory:
            path = write_lock_fixture(Path(directory), lock)
            with self.assertRaisesRegex(preflight.PreflightError, "floating latest"):
                preflight.load_lock(path)

    def test_unpinned_direct_dependency_is_rejected(self):
        requirements = "mem0ai>=2.0\n"
        with tempfile.TemporaryDirectory() as directory:
            path = write_lock_fixture(Path(directory), repository_lock(), requirements_input=requirements)
            with self.assertRaisesRegex(preflight.PreflightError, "unpinned dependency"):
                preflight.load_lock(path)

    def test_unhashed_locked_dependency_is_rejected(self):
        requirements_lock = "mem0ai==2.0.11\n"
        with tempfile.TemporaryDirectory() as directory:
            path = write_lock_fixture(Path(directory), repository_lock(), requirements_lock=requirements_lock)
            with self.assertRaisesRegex(preflight.PreflightError, "unhashed dependency"):
                preflight.load_lock(path)

    def test_requirements_digest_mismatch_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = write_lock_fixture(root, repository_lock())
            (root / "requirements.lock").write_text("changed\n", encoding="utf-8")
            with self.assertRaisesRegex(preflight.PreflightError, "requirements lock digest mismatch"):
                preflight.load_lock(path)

    def test_runtime_policy_cannot_enable_telemetry(self):
        lock = repository_lock()
        lock["runtime"]["telemetry_enabled"] = True
        with tempfile.TemporaryDirectory() as directory:
            path = write_lock_fixture(Path(directory), lock)
            with self.assertRaisesRegex(preflight.PreflightError, "benchmark policy"):
                preflight.load_lock(path)


class ArtifactConsistencyTest(unittest.TestCase):
    def test_deployment_files_match_environment_lock(self):
        lock = preflight.load_lock(HERE / "environment.lock.json")
        dockerfile = (HERE / "Dockerfile").read_text(encoding="utf-8")
        compose = (HERE / "compose.yaml").read_text(encoding="utf-8")
        entrypoint = (HERE / "entrypoint.sh").read_text(encoding="utf-8")

        python_image = lock["python"]["image"]
        self.assertIn("%s@%s" % (python_image["reference"], python_image["index_digest"]), dockerfile)
        self.assertIn(lock["mem0"]["commit"], dockerfile)
        self.assertIn(lock["mem0"]["source_archive"]["sha256"], dockerfile)
        self.assertIn("PYTHONPATH=/opt/mem0:/opt/mem0/server", dockerfile)
        self.assertIn("import mem0; print(mem0.__file__)", dockerfile)
        self.assertIn("chmod -R a-w /opt/mem0", dockerfile)
        nlp = lock["python"]["nlp"]
        self.assertIn(nlp["extra"], (HERE / "requirements.in").read_text(encoding="utf-8"))
        self.assertIn(nlp["model"]["url"], dockerfile)
        self.assertIn(nlp["model"]["sha256"], dockerfile)
        pgvector = lock["database"]["pgvector"]
        self.assertIn("%s@%s" % (pgvector["image_reference"], pgvector["image_index_digest"]), compose)
        self.assertIn("platform: %s" % lock["platform"]["compose"], compose)
        self.assertIn("exec uvicorn main:app", entrypoint)
        self.assertIn("CREATE EXTENSION IF NOT EXISTS vector", (HERE / "init-db.sql").read_text(encoding="utf-8"))
        self.assertNotIn("benchmark_entrypoint", dockerfile + compose + entrypoint)
        self.assertNotIn(":latest", dockerfile + compose)

    def test_direct_requirements_match_locked_versions(self):
        direct = {}
        for line in (HERE / "requirements.in").read_text(encoding="utf-8").splitlines():
            if not line or line.startswith("#"):
                continue
            name, version = line.split("==", 1)
            direct[re.sub(r"\[.*\]$", "", name)] = version
        locked = dict(
            re.findall(
                r"^([A-Za-z0-9_.-]+)==([^\s\\]+)",
                (HERE / "requirements.lock").read_text(encoding="utf-8"),
                re.M,
            )
        )
        self.assertTrue(direct)
        self.assertEqual({name: locked[name] for name in direct}, direct)

    def test_mem0_service_is_least_privilege_and_loopback_only(self):
        dockerfile = (HERE / "Dockerfile").read_text(encoding="utf-8")
        compose = (HERE / "compose.yaml").read_text(encoding="utf-8")
        self.assertIn("USER 10001:10001", dockerfile)
        self.assertIn("read_only: true", compose)
        self.assertIn("no-new-privileges:true", compose)
        self.assertIn("cap_drop:", compose)
        self.assertIn('"127.0.0.1:${MEM0_PORT:-8888}:8000"', compose)
        self.assertNotIn("MEM0_POSTGRES_PORT", compose)
        self.assertNotIn("privileged:", compose)
        self.assertNotIn("docker.sock", compose)

    def test_repository_does_not_contain_populated_secret_files(self):
        self.assertFalse((HERE / ".env").exists())
        example = (HERE / ".env.example").read_text(encoding="utf-8")
        self.assertRegex(example, r"(?m)^POSTGRES_PASSWORD=$")
        lock_text = (HERE / "environment.lock.json").read_text(encoding="utf-8").lower()
        self.assertNotIn("api_key", lock_text)
        self.assertNotIn("password", lock_text)


class SanitizationTest(unittest.TestCase):
    def test_secrets_and_url_credentials_are_redacted(self):
        original = {
            "api_key": "top-secret-key",
            "nested": {
                "postgres_dsn": "postgres://alice:secret@db.example/mem0?token=hidden",
                "base_url": "https://alice:secret@example.test/v1?token=hidden",
            },
        }
        rendered = json.dumps(preflight.sanitize(original), sort_keys=True)
        for secret in ("top-secret-key", "alice", "secret", "hidden"):
            self.assertNotIn(secret, rendered)
        self.assertIn("https://example.test/v1", rendered)

    def test_endpoint_credentials_are_rejected(self):
        with self.assertRaisesRegex(preflight.PreflightError, "must not contain credentials"):
            preflight.validate_url("http://alice:secret@localhost:8888", "Mem0 host")


class RuntimeValidationTest(unittest.TestCase):
    def setUp(self):
        self.lock_path = HERE / "environment.lock.json"
        self.lock = preflight.load_lock(self.lock_path)
        self.lock_digest = preflight.file_sha256(self.lock_path)
        self.probe = valid_probe(self.lock, self.lock_digest)

    def test_matching_runtime_is_accepted(self):
        preflight.validate_runtime_probe(self.lock, self.lock_digest, self.probe)

    def test_exact_postgres_version_mismatch_is_rejected(self):
        self.probe["database"]["postgres_version"] = "17.7 (Debian 17.7-1.pgdg12+1)"
        with self.assertRaisesRegex(preflight.PreflightError, "PostgreSQL version mismatch"):
            preflight.validate_runtime_probe(self.lock, self.lock_digest, self.probe)

    def test_missing_pgvector_extension_is_rejected(self):
        self.probe["database"]["pgvector_installed"] = False
        with self.assertRaisesRegex(preflight.PreflightError, "pgvector installation mismatch"):
            preflight.validate_runtime_probe(self.lock, self.lock_digest, self.probe)

    def test_non_official_server_module_is_rejected(self):
        self.probe["server"]["module"] = "benchmark_entrypoint:app"
        with self.assertRaisesRegex(preflight.PreflightError, "server module mismatch"):
            preflight.validate_runtime_probe(self.lock, self.lock_digest, self.probe)

    def test_replacement_prompt_semantics_are_rejected(self):
        self.probe["capabilities"]["add_prompt_semantics"] = "replacement_system_prompt"
        with self.assertRaisesRegex(preflight.PreflightError, "add prompt semantics mismatch"):
            preflight.validate_runtime_probe(self.lock, self.lock_digest, self.probe)

    def test_site_packages_mem0_import_is_rejected(self):
        self.probe["source"]["identity"]["module_file"] = (
            "/usr/local/lib/python3.12/site-packages/mem0/__init__.py"
        )
        with self.assertRaisesRegex(preflight.PreflightError, "imported mem0.__file__ mismatch"):
            preflight.validate_runtime_probe(self.lock, self.lock_digest, self.probe)

    @mock.patch("preflight.run_command")
    def test_compose_runtime_requires_one_loopback_endpoint(self, run_command):
        pgvector = self.lock["database"]["pgvector"]
        run_command.side_effect = [
            "a" * 64,
            "b" * 64,
            "127.0.0.1:8888",
            "%s\t10001:10001\ttrue\t[\"ALL\"]\t[\"no-new-privileges:true\"]\t%s"
            % ("sha256:" + "c" * 64, self.lock["mem0"]["commit"]),
            pgvector["config_digest"],
            "amd64/linux\t[]",
            "amd64/linux\t%s"
            % json.dumps(["pgvector/pgvector@%s" % pgvector["image_index_digest"]]),
            json.dumps(self.probe),
        ]
        container_id, port, probe = preflight.inspect_compose_runtime(
            ["docker", "compose"], 1.0, self.lock
        )
        self.assertEqual(container_id, "a" * 64)
        self.assertEqual(port, 8888)
        self.assertEqual(probe, self.probe)

    @mock.patch("preflight.run_command")
    def test_wrong_pgvector_image_is_rejected(self, run_command):
        run_command.side_effect = [
            "a" * 64,
            "b" * 64,
            "127.0.0.1:8888",
            "%s\t10001:10001\ttrue\t[\"ALL\"]\t[\"no-new-privileges\"]\t%s"
            % ("sha256:" + "c" * 64, self.lock["mem0"]["commit"]),
            "sha256:" + "d" * 64,
        ]
        with self.assertRaisesRegex(preflight.PreflightError, "pgvector image ID mismatch"):
            preflight.inspect_compose_runtime(["docker", "compose"], 1.0, self.lock)

    def test_http_host_must_match_compose_port(self):
        with self.assertRaisesRegex(preflight.PreflightError, "does not match"):
            preflight.validate_service_endpoint("http://localhost:9999", 8888)


class OfficialAPIValidationTest(unittest.TestCase):
    def setUp(self):
        self.lock = preflight.load_lock(HERE / "environment.lock.json")
        self.openapi, self.config, self.providers = valid_api(self.lock)

    def test_matching_official_api_is_accepted(self):
        preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)

    def test_benchmark_specific_route_is_rejected(self):
        self.openapi["paths"]["/benchmark/provenance"] = {"get": {}}
        with self.assertRaisesRegex(preflight.PreflightError, "routes are forbidden"):
            preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)

    def test_missing_search_method_is_rejected(self):
        self.openapi["paths"]["/search"] = {}
        with self.assertRaisesRegex(preflight.PreflightError, "missing required methods"):
            preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)

    def test_missing_add_prompt_field_is_rejected(self):
        del self.openapi["components"]["schemas"]["MemoryCreate"]["properties"]["prompt"]
        with self.assertRaisesRegex(preflight.PreflightError, "missing.*prompt"):
            preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)

    def test_missing_search_explain_field_is_rejected(self):
        del self.openapi["components"]["schemas"]["SearchRequest"]["properties"]["explain"]
        with self.assertRaisesRegex(preflight.PreflightError, "missing.*explain"):
            preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)

    def test_unredacted_api_key_is_rejected(self):
        self.config["llm"]["config"]["api_key"] = "not-for-output"
        with self.assertRaisesRegex(preflight.PreflightError, "exposed an API key"):
            preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)

    def test_unredacted_database_password_is_rejected(self):
        self.config["vector_store"]["config"]["password"] = "not-for-output"
        with self.assertRaisesRegex(preflight.PreflightError, "exposed a database password"):
            preflight.validate_official_api(self.lock, self.openapi, self.config, self.providers)


class CapabilityTest(unittest.TestCase):
    def test_all_required_capabilities_are_exercised(self):
        calls = []

        def requester(base_url, path, api_key, timeout, method="GET", payload=None):
            del base_url, api_key, timeout
            calls.append((method, path, payload))
            if path == "/generate-instructions":
                return {"custom_instructions": "instructions", "test_message": "test"}
            if path == "/memories" and method == "POST":
                return {"results": [{"id": "memory-id", "memory": prompted_memory(payload)}]}
            if path == "/search":
                return {
                    "results": [{
                        "id": "memory-id",
                        "score_details": {"bm25_score": 0.4, "entity_boost": 0.5},
                    }]
                }
            if path.startswith("/memories?") and method == "DELETE":
                return {"message": "deleted"}
            self.fail("unexpected request %s %s" % (method, path))

        capabilities = preflight.exercise_capabilities("http://localhost:8888", "", 1.0, requester=requester)
        self.assertEqual(set(capabilities), preflight.REQUIRED_CAPABILITIES)
        create_payload = next(payload for method, path, payload in calls if method == "POST" and path == "/memories")
        self.assertIs(create_payload["infer"], True)
        self.assertIn("prompt", create_payload)
        self.assertRegex(create_payload["prompt"], r"OBSERVATION_KEEP_[0-9a-f]+")
        self.assertNotRegex(create_payload["messages"][0]["content"], r"OBSERVATION_KEEP_[0-9a-f]+")
        self.assertRegex(create_payload["messages"][0]["content"], r"OBSERVATION_SOURCE_[0-9a-f]+")
        self.assertRegex(create_payload["messages"][0]["content"], r"OBSERVATION_SKIP_[0-9a-f]+")
        search_payload = next(payload for method, path, payload in calls if method == "POST" and path == "/search")
        self.assertIs(search_payload["explain"], True)
        self.assertEqual(search_payload["threshold"], 0.1)
        self.assertEqual(calls[-1][0], "DELETE")

    def test_failed_search_still_cleans_up_canary(self):
        calls = []

        def requester(base_url, path, api_key, timeout, method="GET", payload=None):
            del base_url, api_key, timeout
            calls.append((method, path))
            if path == "/generate-instructions":
                return {"custom_instructions": "instructions", "test_message": "test"}
            if path == "/memories" and method == "POST":
                return {"results": [{"id": "memory-id", "memory": prompted_memory(payload)}]}
            if path == "/search":
                return {"results": []}
            if path.startswith("/memories?") and method == "DELETE":
                return {"message": "deleted"}
            self.fail("unexpected request")

        with self.assertRaisesRegex(preflight.PreflightError, "did not return results"):
            preflight.exercise_capabilities("http://localhost:8888", "", 1.0, requester=requester)
        self.assertEqual(calls[-1][0], "DELETE")

    def test_missing_bm25_score_fails_closed_and_cleans_up(self):
        calls = []

        def requester(base_url, path, api_key, timeout, method="GET", payload=None):
            del base_url, api_key, timeout
            calls.append((method, path))
            if path == "/generate-instructions":
                return {"custom_instructions": "instructions", "test_message": "test"}
            if path == "/memories" and method == "POST":
                return {"results": [{"id": "memory-id", "memory": prompted_memory(payload)}]}
            if path == "/search":
                return {
                    "results": [{
                        "id": "memory-id",
                        "score_details": {"bm25_score": 0.0, "entity_boost": 0.5},
                    }]
                }
            if path.startswith("/memories?") and method == "DELETE":
                return {"message": "deleted"}
            self.fail("unexpected request")

        with self.assertRaisesRegex(preflight.PreflightError, "BM25 scoring"):
            preflight.exercise_capabilities("http://localhost:8888", "", 1.0, requester=requester)
        self.assertEqual(calls[-1][0], "DELETE")

    def test_missing_entity_score_fails_closed_and_cleans_up(self):
        calls = []

        def requester(base_url, path, api_key, timeout, method="GET", payload=None):
            del base_url, api_key, timeout
            calls.append((method, path))
            if path == "/generate-instructions":
                return {"custom_instructions": "instructions", "test_message": "test"}
            if path == "/memories" and method == "POST":
                return {"results": [{"id": "memory-id", "memory": prompted_memory(payload)}]}
            if path == "/search":
                return {
                    "results": [{
                        "id": "memory-id",
                        "score_details": {"bm25_score": 0.4, "entity_boost": 0.0},
                    }]
                }
            if path.startswith("/memories?") and method == "DELETE":
                return {"message": "deleted"}
            self.fail("unexpected request")

        with self.assertRaisesRegex(preflight.PreflightError, "entity scoring"):
            preflight.exercise_capabilities("http://localhost:8888", "", 1.0, requester=requester)
        self.assertEqual(calls[-1][0], "DELETE")

    def test_cleanup_failure_fails_closed(self):
        def requester(base_url, path, api_key, timeout, method="GET", payload=None):
            del base_url, api_key, timeout
            if path == "/generate-instructions":
                return {"custom_instructions": "instructions", "test_message": "test"}
            if path == "/memories" and method == "POST":
                return {"results": [{"id": "memory-id", "memory": prompted_memory(payload)}]}
            if path == "/search":
                return {
                    "results": [{
                        "id": "memory-id",
                        "score_details": {"bm25_score": 0.4, "entity_boost": 0.5},
                    }]
                }
            raise preflight.PreflightError("delete failed")

        with self.assertRaisesRegex(preflight.PreflightError, "cleanup failed"):
            preflight.exercise_capabilities("http://localhost:8888", "", 1.0, requester=requester)

    def test_ignored_observation_prompt_fails_closed_and_cleans_up(self):
        calls = []

        def requester(base_url, path, api_key, timeout, method="GET", payload=None):
            del base_url, api_key, timeout
            calls.append((method, path))
            if path == "/generate-instructions":
                return {"custom_instructions": "instructions", "test_message": "test"}
            if path == "/memories" and method == "POST":
                return {
                    "results": [
                        {
                            "id": "memory-id",
                            "memory": payload["messages"][0]["content"],
                        }
                    ]
                }
            if path.startswith("/memories?") and method == "DELETE":
                return {"message": "deleted"}
            self.fail("unexpected request")

        with self.assertRaisesRegex(preflight.PreflightError, "did not apply observation instructions"):
            preflight.exercise_capabilities("http://localhost:8888", "", 1.0, requester=requester)
        self.assertEqual(calls[-1][0], "DELETE")


class PreflightOrchestrationTest(unittest.TestCase):
    @mock.patch("preflight.exercise_capabilities")
    @mock.patch("preflight.request_json")
    @mock.patch("preflight.inspect_compose_runtime")
    def test_audit_record_is_sanitized(self, inspect_runtime, request_json, exercise):
        lock_path = HERE / "environment.lock.json"
        lock = preflight.load_lock(lock_path)
        probe = valid_probe(lock, preflight.file_sha256(lock_path))
        probe["ignored_api_key"] = "probe-secret"
        inspect_runtime.return_value = ("a" * 64, 8888, probe)
        openapi, config, providers = valid_api(lock)
        config["debug_url"] = "https://alice:secret@example.test/v1?token=hidden"
        request_json.side_effect = [openapi, config, providers]
        exercise.return_value = {name: True for name in preflight.REQUIRED_CAPABILITIES}
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / ".env"
            env_file.write_text("POSTGRES_PASSWORD=not-read\n", encoding="utf-8")
            args = SimpleNamespace(
                lock=str(lock_path),
                compose_file=str(HERE / "compose.yaml"),
                env_file=str(env_file),
                host="http://localhost:8888",
                api_key="request-secret",
                timeout=1.0,
            )
            result = preflight.run_preflight(args)
        rendered = json.dumps(result, sort_keys=True)
        for secret in ("probe-secret", "request-secret", "alice", "secret", "hidden"):
            self.assertNotIn(secret, rendered)

    def test_private_output_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "preflight.json"
            preflight.write_private(path, "{}\n")
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
