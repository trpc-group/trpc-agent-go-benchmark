#!/usr/bin/env python3
# Tencent is pleased to support the open source community by making
# trpc-agent-go-benchmark available.
#
# Copyright (C) 2026 Tencent. All rights reserved.
#
# trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

"""Report non-secret facts from inside the running official Mem0 container."""

import ast
import hashlib
import importlib
import importlib.metadata
import json
import os
import platform
import stat
import sys
import urllib.parse
from pathlib import Path

ARCHIVE_PATH = Path("/opt/benchmark/mem0-source.tar.gz")
LOCK_PATH = Path("/opt/benchmark/environment.lock.json")
REQUIREMENTS_PATH = Path("/opt/benchmark/requirements.lock")
SPACY_MODEL_PATH = Path("/opt/benchmark/en_core_web_sm-3.8.0-py3-none-any.whl")
LOCKED_SOURCE_ROOT = Path("/opt/mem0")
SERVER_MAIN_PATH = Path("/opt/mem0/server/main.py")
PROC_ROOT = Path("/proc")
PROMPT_SEMANTICS = "additive_custom_instructions"
NLP_LEMMA_PROBE = "Alice attended meetings at Acme Corporation."
NLP_ENTITY_PROBE = "Alice met Bob at Acme Corporation in London."


class ProbeError(Exception):
    """Report a fail-closed runtime probe error."""


def file_sha256(path):
    """Return the lowercase SHA-256 digest for path."""
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_url(value):
    """Validate a non-secret HTTP endpoint and return its canonical form."""
    try:
        parsed = urllib.parse.urlsplit(str(value))
        port = parsed.port
    except ValueError as exc:
        raise ProbeError("OPENAI_BASE_URL is invalid") from exc
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise ProbeError("OPENAI_BASE_URL must be an absolute HTTP(S) URL")
    if parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
        raise ProbeError("OPENAI_BASE_URL must not contain credentials, a query, or a fragment")
    host = parsed.hostname or ""
    if ":" in host and not host.startswith("["):
        host = "[%s]" % host
    if port is not None:
        host = "%s:%d" % (host, port)
    return urllib.parse.urlunsplit((parsed.scheme, host, parsed.path, "", ""))


def required_env(name):
    """Read a required environment variable without including its value in errors."""
    value = os.environ.get(name, "")
    if not value:
        raise ProbeError("required runtime setting %s is missing" % name)
    return value


def find_server_command(proc_root=PROC_ROOT):
    """Find the running Uvicorn command and ensure it serves the official app."""
    matches = []
    try:
        processes = proc_root.iterdir()
    except OSError as exc:
        raise ProbeError("cannot inspect running server process") from exc
    for process in processes:
        if not process.name.isdigit():
            continue
        try:
            raw = (process / "cmdline").read_bytes()
        except OSError:
            continue
        command = [part.decode("utf-8", "replace") for part in raw.split(b"\0") if part]
        if "main:app" in command and any("uvicorn" in part for part in command):
            matches.append(command)
    if len(matches) != 1:
        raise ProbeError("expected exactly one official Mem0 Uvicorn process")
    if any("benchmark_entrypoint" in part for part in matches[0]):
        raise ProbeError("benchmark-specific Mem0 application wrapper is forbidden")
    return matches[0]


def locked_mem0_source_facts(source_root=LOCKED_SOURCE_ROOT, module=None):
    """Prove that the imported Mem0 module and prompt code use locked source."""
    try:
        if module is None:
            module = importlib.import_module("mem0")
        root = source_root.resolve(strict=True)
        module_file = Path(module.__file__).resolve(strict=True)
        expected_module_file = (root / "mem0" / "__init__.py").resolve(strict=True)
        memory_main = (root / "mem0" / "memory" / "main.py").resolve(strict=True)
    except (AttributeError, ImportError, OSError, TypeError) as exc:
        raise ProbeError("cannot locate imported Mem0 source") from exc
    if module_file != expected_module_file or not memory_main.is_file():
        raise ProbeError("imported Mem0 module is outside the locked source tree")

    checked_paths = 0
    try:
        for path in (root, *root.rglob("*")):
            if path.is_symlink():
                continue
            checked_paths += 1
            if stat.S_IMODE(path.stat().st_mode) & 0o222:
                raise ProbeError("locked Mem0 source tree has writable paths")
    except OSError as exc:
        raise ProbeError("cannot inspect locked Mem0 source permissions") from exc
    return memory_main, {
        "root": str(root),
        "module_file": str(module_file),
        "tree_read_only": True,
        "checked_paths": checked_paths,
    }


def _function(class_node, name):
    return next(
        (
            item
            for item in class_node.body
            if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)) and item.name == name
        ),
        None,
    )


def _assigned_value(function, name):
    for node in ast.walk(function):
        assigned = isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == name for target in getattr(node, "targets", ())
        )
        if assigned:
            return node.value
    return None


def _is_name(node, name):
    return isinstance(node, ast.Name) and node.id == name


def _is_self_attribute(node, name):
    return (
        isinstance(node, ast.Attribute)
        and node.attr == name
        and isinstance(node.value, ast.Name)
        and node.value.id == "self"
    )


def _forwards_prompt(function, callee):
    for node in ast.walk(function):
        if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute) or node.func.attr != callee:
            continue
        if any(keyword.arg == "prompt" and _is_name(keyword.value, "prompt") for keyword in node.keywords):
            return True
    return False


def _builds_additive_user_prompt(function):
    for node in ast.walk(function):
        if not isinstance(node, ast.Call) or not _is_name(node.func, "generate_additive_extraction_prompt"):
            continue
        if any(
            keyword.arg == "custom_instructions" and _is_name(keyword.value, "custom_instr")
            for keyword in node.keywords
        ):
            return True
    return False


def _uses_base_and_user_prompts(function):
    for node in ast.walk(function):
        if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute):
            continue
        if node.func.attr != "generate_response":
            continue
        messages = next((keyword.value for keyword in node.keywords if keyword.arg == "messages"), None)
        if not isinstance(messages, ast.List):
            continue
        roles = set()
        for message in messages.elts:
            if not isinstance(message, ast.Dict):
                continue
            fields = {
                key.value: value
                for key, value in zip(message.keys, message.values)
                if isinstance(key, ast.Constant) and isinstance(key.value, str)
            }
            role = fields.get("role")
            content = fields.get("content")
            if isinstance(role, ast.Constant) and isinstance(role.value, str) and isinstance(content, ast.Name):
                roles.add((role.value, content.id))
        if {("system", "system_prompt"), ("user", "user_prompt")}.issubset(roles):
            return True
    return False


def verify_add_prompt_semantics(path):
    """Verify that add(prompt=...) augments, rather than replaces, extraction."""
    try:
        tree = ast.parse(path.read_text(encoding="utf-8"))
    except (OSError, SyntaxError, UnicodeError) as exc:
        raise ProbeError("cannot inspect locked Mem0 prompt semantics") from exc
    memory_class = next((node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Memory"), None)
    if memory_class is None:
        raise ProbeError("locked Mem0 prompt semantics are unsupported")
    add_method = _function(memory_class, "add")
    extraction_method = _function(memory_class, "_add_to_vector_store")
    if add_method is None or extraction_method is None:
        raise ProbeError("locked Mem0 prompt semantics are unsupported")
    arguments = add_method.args.args + add_method.args.kwonlyargs
    system_prompt = _assigned_value(extraction_method, "system_prompt")
    custom_instructions = _assigned_value(extraction_method, "custom_instr")
    custom_is_additive = (
        isinstance(custom_instructions, ast.BoolOp)
        and isinstance(custom_instructions.op, ast.Or)
        and len(custom_instructions.values) == 2
        and _is_name(custom_instructions.values[0], "prompt")
        and _is_self_attribute(custom_instructions.values[1], "custom_instructions")
    )
    supported = all(
        (
            any(argument.arg == "prompt" for argument in arguments),
            _forwards_prompt(add_method, "_add_to_vector_store"),
            _is_name(system_prompt, "ADDITIVE_EXTRACTION_PROMPT"),
            custom_is_additive,
            _builds_additive_user_prompt(extraction_method),
            _uses_base_and_user_prompts(extraction_method),
        )
    )
    if not supported:
        raise ProbeError("locked Mem0 prompt semantics are unsupported")
    return PROMPT_SEMANTICS


def detect_add_prompt_semantics(path):
    """Return a safe capability value so the host can report an explicit mismatch."""
    try:
        return verify_add_prompt_semantics(path)
    except ProbeError:
        return "unsupported"


def validate_nlp_outputs(
    spacy_version,
    model_version,
    full_pipeline,
    lemma_pipeline,
    lemmatized,
    entities,
):
    """Validate deterministic NLP outputs and return sanitized facts."""
    lemma_tokens = set(str(lemmatized).casefold().split())
    if not {"attend", "meeting"}.issubset(lemma_tokens):
        raise ProbeError("Mem0 BM25 lemmatization capability is unavailable")
    if {"attended", "meetings"} & lemma_tokens:
        raise ProbeError("Mem0 BM25 lemmatization capability is unavailable")
    entity_texts = {
        str(text).casefold()
        for entity_type, text in entities
        if str(entity_type).strip() and str(text).strip()
    }
    if not {"alice", "acme corporation"}.issubset(entity_texts):
        raise ProbeError("Mem0 entity extraction capability is unavailable")
    full_components = sorted(str(component) for component in full_pipeline)
    lemma_components = sorted(str(component) for component in lemma_pipeline)
    if "ner" not in full_components or "lemmatizer" not in full_components:
        raise ProbeError("spaCy full pipeline is incomplete")
    if "lemmatizer" not in lemma_components:
        raise ProbeError("spaCy lemma pipeline is incomplete")
    return {
        "spacy_version": str(spacy_version),
        "model_name": "en_core_web_sm",
        "model_version": str(model_version),
        "full_pipeline": full_components,
        "lemma_pipeline": lemma_components,
        "lemmatization_verified": True,
        "entity_extraction_verified": True,
    }


def nlp_facts():
    """Load Mem0's NLP paths and verify lemmatization plus entity extraction."""
    try:
        import spacy
        from mem0.utils.entity_extraction import extract_entities
        from mem0.utils.lemmatization import lemmatize_for_bm25
        from mem0.utils.spacy_models import get_nlp_full, get_nlp_lemma

        full = get_nlp_full()
        lemma = get_nlp_lemma()
        if full is None or lemma is None or not spacy.util.is_package("en_core_web_sm"):
            raise ProbeError("spaCy English model is unavailable")
        return validate_nlp_outputs(
            importlib.metadata.version("spacy"),
            importlib.metadata.version("en-core-web-sm"),
            full.pipe_names,
            lemma.pipe_names,
            lemmatize_for_bm25(NLP_LEMMA_PROBE),
            extract_entities(NLP_ENTITY_PROBE),
        )
    except ProbeError:
        raise
    except (ImportError, importlib.metadata.PackageNotFoundError, RuntimeError, ValueError) as exc:
        raise ProbeError("cannot verify Mem0 NLP capabilities") from exc


def database_facts(connect=None, database_error=Exception):
    """Query exact database versions and exercise required pgvector operations."""
    if connect is None:
        try:
            import psycopg
        except ImportError as exc:
            raise ProbeError("psycopg is not installed") from exc
        connect = psycopg.connect
        database_error = psycopg.Error
    try:
        connection = connect(
            host=required_env("POSTGRES_HOST"),
            port=int(required_env("POSTGRES_PORT")),
            dbname=required_env("POSTGRES_DB"),
            user=required_env("POSTGRES_USER"),
            password=required_env("POSTGRES_PASSWORD"),
            connect_timeout=5,
        )
        with connection:
            with connection.cursor() as cursor:
                cursor.execute("SHOW server_version")
                postgres_version = cursor.fetchone()[0]
                cursor.execute("SHOW server_version_num")
                postgres_version_num = cursor.fetchone()[0]
                cursor.execute("SELECT extversion FROM pg_extension WHERE extname = 'vector'")
                extension = cursor.fetchone()
                if extension is None:
                    raise ProbeError("pgvector extension is not installed")
                cursor.execute("SELECT vector_dims('[1,2,3]'::vector)")
                dimensions = cursor.fetchone()[0]
                cursor.execute("SELECT '[1,2,3]'::vector <-> '[1,2,4]'::vector")
                distance = float(cursor.fetchone()[0])
                cursor.execute("SELECT EXISTS (SELECT 1 FROM pg_am WHERE amname = 'hnsw')")
                hnsw_available = bool(cursor.fetchone()[0])
    except ProbeError:
        raise
    except (OSError, TypeError, ValueError, database_error) as exc:
        raise ProbeError("database capability probe failed") from exc
    if dimensions != 3 or abs(distance - 1.0) > 1e-12 or not hnsw_available:
        raise ProbeError("required pgvector operations are unavailable")
    return {
        "postgres_version": str(postgres_version),
        "postgres_version_num": str(postgres_version_num),
        "pgvector_installed": True,
        "pgvector_version": str(extension[0]),
        "vector_dimensions": dimensions,
        "l2_distance": distance,
        "hnsw_available": hnsw_available,
    }


def collect_facts():
    """Collect sanitized runtime facts without modifying the Mem0 app."""
    command = find_server_command()
    memory_main, source_identity = locked_mem0_source_facts()
    return {
        "source": {
            "commit": required_env("MEM0_SOURCE_COMMIT"),
            "archive_sha256": file_sha256(ARCHIVE_PATH),
            "server_main_sha256": file_sha256(SERVER_MAIN_PATH),
            "memory_main_sha256": file_sha256(memory_main),
            "identity": source_identity,
        },
        "artifacts": {
            "environment_lock_sha256": file_sha256(LOCK_PATH),
            "requirements_lock_sha256": file_sha256(REQUIREMENTS_PATH),
            "spacy_model_sha256": file_sha256(SPACY_MODEL_PATH),
        },
        "python": {
            "version": platform.python_version(),
            "implementation": platform.python_implementation(),
            "machine": platform.machine(),
        },
        "distribution": {
            "name": "mem0ai",
            "version": importlib.metadata.version("mem0ai"),
        },
        "server": {
            "module": "main:app",
            "workers": command[command.index("--workers") + 1] if "--workers" in command else "1",
        },
        "runtime": {
            "auth_disabled": os.environ.get("AUTH_DISABLED", "").lower() == "true",
            "telemetry_enabled": os.environ.get("MEM0_TELEMETRY", "").lower() == "true",
            "openai_base_url": safe_url(required_env("OPENAI_BASE_URL")),
            "llm_model": required_env("MEM0_DEFAULT_LLM_MODEL"),
            "embedder_model": required_env("MEM0_DEFAULT_EMBEDDER_MODEL"),
            "collection_name": required_env("POSTGRES_COLLECTION_NAME"),
        },
        "capabilities": {
            "add_prompt_semantics": detect_add_prompt_semantics(memory_main),
        },
        "nlp": nlp_facts(),
        "database": database_facts(),
    }


def main():
    """Print a single sanitized JSON object for the host-side preflight."""
    try:
        report = collect_facts()
    except (OSError, ProbeError, importlib.metadata.PackageNotFoundError, ValueError):
        print("Mem0 runtime probe failed", file=sys.stderr)
        return 1
    json.dump(report, sys.stdout, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
