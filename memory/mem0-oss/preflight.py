#!/usr/bin/env python3
# Tencent is pleased to support the open source community by making
# trpc-agent-go-benchmark available.
#
# Copyright (C) 2026 Tencent. All rights reserved.
#
# trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

"""Fail closed unless a running Mem0 OSS service matches its environment lock."""

import argparse
import hashlib
import ipaddress
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path


DIGEST_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
HEX_DIGEST_PATTERN = re.compile(r"^[0-9a-f]{64}$")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
VERSION_PATTERN = re.compile(r"^[0-9]+(?:\.[0-9]+){1,3}$")
CONTAINER_ID_PATTERN = re.compile(r"^[0-9a-f]{12,64}$")
LOCKED_REQUIREMENT_PATTERN = re.compile(r"^[A-Za-z0-9_.-]+==[^\s\\]+(?:\s*\\)?$")
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
REQUIRED_API_METHODS = {
    "/configure": {"get", "post"},
    "/configure/providers": {"get"},
    "/generate-instructions": {"post"},
    "/memories": {"get", "post", "delete"},
    "/memories/{memory_id}": {"get", "put", "delete"},
    "/search": {"post"},
}
REQUIRED_CAPABILITIES = {
    "bm25_scoring",
    "configuration",
    "entity_scoring",
    "llm_generation",
    "memory_create",
    "memory_search",
    "memory_delete",
    "observation_prompt",
    "search_explain",
}
PROMPT_SEMANTICS = "additive_custom_instructions"
SENSITIVE_KEYS = {
    "admin_api_key",
    "api_key",
    "authorization",
    "dsn",
    "jwt_secret",
    "password",
    "password_hash",
    "secret",
    "token",
}


class PreflightError(Exception):
    """Report a fail-closed environment validation error."""


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Prevent a configured endpoint from silently redirecting elsewhere."""

    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        del request, file_pointer, code, message, headers, new_url
        return None


def file_sha256(path):
    """Return the lowercase SHA-256 digest for path."""
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def validate_url(value, name, root_only=False):
    """Validate an HTTP URL and reject embedded credentials or hidden routing data."""
    try:
        parsed = urllib.parse.urlsplit(str(value))
        port = parsed.port
    except ValueError as exc:
        raise PreflightError("%s is not a valid URL" % name) from exc
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise PreflightError("%s must be an absolute HTTP(S) URL" % name)
    if parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
        raise PreflightError("%s must not contain credentials, a query, or a fragment" % name)
    if root_only and parsed.path not in {"", "/"}:
        raise PreflightError("%s must reference the service root" % name)
    host = parsed.hostname
    if ":" in host and not host.startswith("["):
        host = "[%s]" % host
    if port is not None:
        host = "%s:%d" % (host, port)
    path = "" if root_only else parsed.path
    return urllib.parse.urlunsplit((parsed.scheme, host, path, "", ""))


def sanitize_url(value):
    """Remove credentials and query data from a URL without raising."""
    try:
        parsed = urllib.parse.urlsplit(str(value))
        port = parsed.port
    except ValueError:
        return "[invalid URL]"
    if not parsed.scheme or not parsed.netloc:
        return str(value)
    host = parsed.hostname or ""
    if ":" in host and not host.startswith("["):
        host = "[%s]" % host
    if port is not None:
        host = "%s:%d" % (host, port)
    return urllib.parse.urlunsplit((parsed.scheme, host, parsed.path, "", ""))


def sanitize(value, key=""):
    """Recursively redact secrets and credential-bearing URLs."""
    normalized_key = key.lower().replace("-", "_")
    is_sensitive = normalized_key in SENSITIVE_KEYS or any(
        normalized_key.endswith("_" + suffix)
        for suffix in ("api_key", "authorization", "dsn", "password", "secret", "token")
    )
    if is_sensitive:
        return "[redacted]" if value not in (None, "") else value
    if isinstance(value, dict):
        return {item_key: sanitize(item_value, item_key) for item_key, item_value in value.items()}
    if isinstance(value, list):
        return [sanitize(item, key) for item in value]
    if isinstance(value, str) and "://" in value:
        return sanitize_url(value)
    return value


def _require_mapping(value, name):
    if not isinstance(value, dict):
        raise PreflightError("%s must be an object" % name)
    return value


def _require_string(value, name):
    if not isinstance(value, str) or not value.strip():
        raise PreflightError("%s is required" % name)
    return value


def _require_digest(value, name):
    if not isinstance(value, str) or not DIGEST_PATTERN.fullmatch(value):
        raise PreflightError("%s must be an immutable sha256 digest" % name)


def _require_hex_digest(value, name):
    if not isinstance(value, str) or not HEX_DIGEST_PATTERN.fullmatch(value):
        raise PreflightError("%s must be a lowercase SHA-256 value" % name)


def _reject_floating_reference(value, name):
    value = _require_string(value, name)
    lowered = value.lower()
    if lowered.endswith(":latest") or ":latest@" in lowered:
        raise PreflightError("%s must not use a floating latest tag" % name)
    if ":" not in value.rsplit("/", 1)[-1]:
        raise PreflightError("%s must include an auditable tag" % name)


def _local_artifact(root, name, digest, label):
    if not isinstance(name, str) or not name or Path(name).name != name:
        raise PreflightError("%s path must be a local file name" % label)
    _require_hex_digest(digest, "%s digest" % label)
    path = root / name
    if not path.is_file():
        raise PreflightError("%s is missing" % label)
    if file_sha256(path) != digest:
        raise PreflightError("%s digest mismatch" % label)
    return path


def validate_requirements_input(path):
    """Reject direct dependencies that are not pinned exactly."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise PreflightError("cannot read requirements input") from exc
    requirements = [line.strip() for line in lines if line.strip() and not line.lstrip().startswith("#")]
    if not requirements or any("==" not in requirement for requirement in requirements):
        raise PreflightError("requirements input contains an unpinned dependency")
    for requirement in requirements:
        package, version = requirement.rsplit("==", 1)
        invalid_operator = any(operator in package + version for operator in (">", "<", "~=", "!=", "@"))
        if not package or not version or invalid_operator:
            raise PreflightError("requirements input contains an unpinned dependency")


def validate_requirements_lock(path):
    """Require exact versions and SHA-256 hashes for every locked package."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise PreflightError("cannot read requirements lock") from exc
    starts = [index for index, line in enumerate(lines) if line and not line[0].isspace() and not line.startswith("#")]
    if not starts:
        raise PreflightError("requirements lock is empty")
    for position, start in enumerate(starts):
        end = starts[position + 1] if position + 1 < len(starts) else len(lines)
        if not LOCKED_REQUIREMENT_PATTERN.fullmatch(lines[start]):
            raise PreflightError("requirements lock contains a non-exact dependency")
        block = "\n".join(lines[start:end])
        if "--hash=sha256:" not in block:
            raise PreflightError("requirements lock contains an unhashed dependency")
        if "http://" in block or "https://" in block or "git+" in block:
            raise PreflightError("requirements lock contains a direct URL")


def load_lock(path):
    """Load and validate every reproducibility claim in the environment lock."""
    try:
        lock = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise PreflightError("cannot read environment lock") from exc
    _require_mapping(lock, "environment lock")
    if lock.get("schema_version") != 2 or lock.get("environment") != "longmemeval-mem0-oss":
        raise PreflightError("unsupported environment lock")

    target = _require_mapping(lock.get("platform"), "platform")
    expected_platform = {
        "compose": "linux/amd64",
        "os": "linux",
        "architecture": "amd64",
        "machine": "x86_64",
    }
    if target != expected_platform:
        raise PreflightError("platform must be locked to linux/amd64")

    root = path.parent
    deployment = _require_mapping(lock.get("deployment"), "deployment")
    if set(deployment) != {"dockerfile", "compose", "entrypoint", "preflight", "runtime_probe"}:
        raise PreflightError("deployment artifact lock is incomplete")
    for name, artifact in deployment.items():
        artifact = _require_mapping(artifact, "deployment.%s" % name)
        _local_artifact(root, artifact.get("path"), artifact.get("sha256"), "deployment %s" % name)

    mem0 = _require_mapping(lock.get("mem0"), "mem0")
    if mem0.get("repository") != "https://github.com/mem0ai/mem0.git":
        raise PreflightError("mem0.repository must reference the official repository")
    if not COMMIT_PATTERN.fullmatch(str(mem0.get("commit", ""))):
        raise PreflightError("mem0.commit must be a full Git commit")
    source_archive = _require_mapping(mem0.get("source_archive"), "mem0.source_archive")
    archive_url = _require_string(source_archive.get("url"), "mem0.source_archive.url")
    if mem0["commit"] not in archive_url or not archive_url.startswith("https://github.com/mem0ai/mem0/archive/"):
        raise PreflightError("mem0.source_archive.url must reference the locked official commit")
    _require_hex_digest(source_archive.get("sha256"), "mem0.source_archive.sha256")
    _require_hex_digest(mem0.get("server_main_sha256"), "mem0.server_main_sha256")
    runtime_source = _require_mapping(mem0.get("runtime_source"), "mem0.runtime_source")
    if runtime_source.get("root") != "/opt/mem0" or runtime_source.get("module_file") != "/opt/mem0/mem0/__init__.py":
        raise PreflightError("mem0.runtime_source must use the locked source tree")
    _require_hex_digest(runtime_source.get("memory_main_sha256"), "mem0.runtime_source.memory_main_sha256")
    distribution = _require_mapping(mem0.get("distribution"), "mem0.distribution")
    if distribution.get("name") != "mem0ai" or not VERSION_PATTERN.fullmatch(
        str(distribution.get("version", ""))
    ):
        raise PreflightError("mem0.distribution must pin an exact mem0ai version")

    python = _require_mapping(lock.get("python"), "python")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", str(python.get("version", ""))):
        raise PreflightError("python.version must be exact")
    python_image = _require_mapping(python.get("image"), "python.image")
    _reject_floating_reference(python_image.get("reference"), "python.image.reference")
    for field in ("index_digest", "platform_digest", "config_digest"):
        _require_digest(python_image.get(field), "python.image.%s" % field)
    requirements = _require_mapping(python.get("requirements"), "python.requirements")
    input_path = _local_artifact(
        root,
        requirements.get("input_path"),
        requirements.get("input_sha256"),
        "requirements input",
    )
    lock_path = _local_artifact(
        root,
        requirements.get("lock_path"),
        requirements.get("lock_sha256"),
        "requirements lock",
    )
    _require_string(requirements.get("resolver"), "python.requirements.resolver")
    _require_string(requirements.get("exclude_newer"), "python.requirements.exclude_newer")
    validate_requirements_input(input_path)
    validate_requirements_lock(lock_path)
    nlp = _require_mapping(python.get("nlp"), "python.nlp")
    if nlp.get("extra") != "mem0ai[nlp]" or not VERSION_PATTERN.fullmatch(
        str(nlp.get("spacy_version", ""))
    ):
        raise PreflightError("python.nlp must lock the Mem0 NLP extra and spaCy version")
    model = _require_mapping(nlp.get("model"), "python.nlp.model")
    model_name = model.get("name")
    model_version = model.get("version")
    model_url = _require_string(model.get("url"), "python.nlp.model.url")
    if model_name != "en_core_web_sm" or not VERSION_PATTERN.fullmatch(str(model_version or "")):
        raise PreflightError("python.nlp.model must lock en_core_web_sm")
    expected_model_prefix = (
        "https://github.com/explosion/spacy-models/releases/download/"
        "en_core_web_sm-%s/en_core_web_sm-%s-" % (model_version, model_version)
    )
    if not model_url.startswith(expected_model_prefix) or not model_url.endswith(".whl"):
        raise PreflightError("python.nlp.model.url must reference the locked official model release")
    _require_hex_digest(model.get("sha256"), "python.nlp.model.sha256")
    components = nlp.get("required_pipeline_components")
    if not isinstance(components, list) or not {"lemmatizer", "ner"}.issubset(components):
        raise PreflightError("python.nlp.required_pipeline_components is incomplete")

    database = _require_mapping(lock.get("database"), "database")
    initialization = _require_mapping(database.get("initialization"), "database.initialization")
    _local_artifact(
        root,
        initialization.get("path"),
        initialization.get("sha256"),
        "database initialization",
    )
    postgres = _require_mapping(database.get("postgres"), "database.postgres")
    major = _require_string(postgres.get("major_version"), "database.postgres.major_version")
    version = _require_string(postgres.get("version"), "database.postgres.version")
    if not major.isdigit() or not version.startswith(major + "."):
        raise PreflightError("database.postgres version is inconsistent")
    if not _require_string(postgres.get("version_num"), "database.postgres.version_num").isdigit():
        raise PreflightError("database.postgres.version_num must be numeric")
    _require_string(postgres.get("distribution"), "database.postgres.distribution")
    _reject_floating_reference(postgres.get("base_image_reference"), "database.postgres.base_image_reference")
    _require_digest(postgres.get("base_image_digest"), "database.postgres.base_image_digest")
    pgvector = _require_mapping(database.get("pgvector"), "database.pgvector")
    if not VERSION_PATTERN.fullmatch(str(pgvector.get("version", ""))):
        raise PreflightError("database.pgvector.version must be exact")
    if not COMMIT_PATTERN.fullmatch(str(pgvector.get("source_commit", ""))):
        raise PreflightError("database.pgvector.source_commit must be a full Git commit")
    _reject_floating_reference(pgvector.get("image_reference"), "database.pgvector.image_reference")
    for field in ("image_index_digest", "platform_digest", "config_digest"):
        _require_digest(pgvector.get(field), "database.pgvector.%s" % field)

    runtime = _require_mapping(lock.get("runtime"), "runtime")
    server = _require_mapping(runtime.get("server"), "runtime.server")
    if server.get("module") != "main:app":
        raise PreflightError("runtime.server.module must use the official Mem0 app")
    _require_string(server.get("api_title"), "runtime.server.api_title")
    if not VERSION_PATTERN.fullmatch(str(server.get("api_version", ""))):
        raise PreflightError("runtime.server.api_version must be exact")
    if runtime.get("auth_disabled") is not True or runtime.get("telemetry_enabled") is not False:
        raise PreflightError("runtime auth and telemetry modes do not match benchmark policy")
    for component in ("llm", "embedder"):
        config = _require_mapping(runtime.get(component), "runtime.%s" % component)
        if config.get("provider") != "openai":
            raise PreflightError("runtime.%s.provider must be openai" % component)
        _require_string(config.get("model"), "runtime.%s.model" % component)
        config["base_url"] = validate_url(config.get("base_url"), "runtime.%s.base_url" % component)
    if runtime["llm"]["base_url"] != runtime["embedder"]["base_url"]:
        raise PreflightError("LLM and embedder must use the locked split proxy endpoint")
    vector = _require_mapping(runtime.get("vector_store"), "runtime.vector_store")
    if vector != {
        "provider": "pgvector",
        "host": "postgres",
        "port": 5432,
        "database": "postgres",
        "collection": "memories",
    }:
        raise PreflightError("runtime.vector_store does not match the locked topology")
    capabilities = runtime.get("required_api_capabilities")
    if not isinstance(capabilities, list) or set(capabilities) != REQUIRED_CAPABILITIES:
        raise PreflightError("runtime.required_api_capabilities is incomplete")
    return lock


def _compose_base(compose_file, env_file):
    compose_file = Path(compose_file).resolve()
    env_file = Path(env_file).resolve()
    if not compose_file.is_file():
        raise PreflightError("Compose file is missing")
    if not env_file.is_file():
        raise PreflightError("Compose environment file is missing")
    return [
        "docker",
        "compose",
        "--project-directory",
        str(compose_file.parent),
        "--env-file",
        str(env_file),
        "--file",
        str(compose_file),
    ]


def run_command(command, timeout, description):
    """Run a local command while keeping captured diagnostics out of errors."""
    try:
        result = subprocess.run(
            command,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise PreflightError("%s failed" % description) from exc
    if result.returncode != 0 or len(result.stdout.encode("utf-8")) > MAX_RESPONSE_BYTES:
        raise PreflightError("%s failed" % description)
    return result.stdout.strip()


def inspect_compose_runtime(compose_base, timeout, lock):
    """Locate the exact Compose service, published endpoint, and runtime probe."""
    container_id = run_command(compose_base + ["ps", "--quiet", "mem0"], timeout, "Compose service lookup")
    if not CONTAINER_ID_PATTERN.fullmatch(container_id):
        raise PreflightError("Compose must have exactly one running mem0 container")
    postgres_id = run_command(
        compose_base + ["ps", "--quiet", "postgres"], timeout, "PostgreSQL service lookup"
    )
    if not CONTAINER_ID_PATTERN.fullmatch(postgres_id):
        raise PreflightError("Compose must have exactly one running postgres container")
    port_output = run_command(compose_base + ["port", "mem0", "8000"], timeout, "Compose port lookup")
    endpoints = [line.strip() for line in port_output.splitlines() if line.strip()]
    if len(endpoints) != 1:
        raise PreflightError("Mem0 must publish exactly one HTTP endpoint")
    try:
        published = urllib.parse.urlsplit("//" + endpoints[0])
        published_port = published.port
        published_host = published.hostname
    except ValueError as exc:
        raise PreflightError("Compose returned an invalid Mem0 endpoint") from exc
    if published_host not in {"127.0.0.1", "::1"} or published_port is None:
        raise PreflightError("Mem0 endpoint must be bound to loopback only")

    mem0_metadata = run_command(
        [
            "docker",
            "inspect",
            "--type",
            "container",
            "--format",
            '{{.Image}}\t{{.Config.User}}\t{{.HostConfig.ReadonlyRootfs}}\t{{json .HostConfig.CapDrop}}\t'
            '{{json .HostConfig.SecurityOpt}}\t{{index .Config.Labels "org.opencontainers.image.revision"}}',
            container_id,
        ],
        timeout,
        "Mem0 container inspection",
    ).split("\t")
    if len(mem0_metadata) != 6:
        raise PreflightError("Mem0 container inspection returned invalid metadata")
    mem0_image, user, read_only, cap_drop_raw, security_raw, revision = mem0_metadata
    _require_digest(mem0_image, "running Mem0 image ID")
    try:
        cap_drop = json.loads(cap_drop_raw)
        security_options = json.loads(security_raw)
    except json.JSONDecodeError as exc:
        raise PreflightError("Mem0 container security metadata is invalid") from exc
    if user != "10001:10001" or read_only != "true" or cap_drop != ["ALL"]:
        raise PreflightError("running Mem0 container does not enforce least privilege")
    if not isinstance(security_options, list) or not any(
        option in {"no-new-privileges", "no-new-privileges:true"} for option in security_options
    ):
        raise PreflightError("running Mem0 container permits privilege escalation")
    _expect(revision, lock["mem0"]["commit"], "running Mem0 image revision")

    postgres_image = run_command(
        ["docker", "inspect", "--type", "container", "--format", "{{.Image}}", postgres_id],
        timeout,
        "PostgreSQL container inspection",
    )
    _expect(postgres_image, lock["database"]["pgvector"]["config_digest"], "running pgvector image ID")
    mem0_image_metadata = run_command(
        [
            "docker",
            "image",
            "inspect",
            "--format",
            '{{.Architecture}}/{{.Os}}\t{{json .RepoDigests}}',
            mem0_image,
        ],
        timeout,
        "Mem0 image inspection",
    ).split("\t")
    postgres_image_metadata = run_command(
        [
            "docker",
            "image",
            "inspect",
            "--format",
            '{{.Architecture}}/{{.Os}}\t{{json .RepoDigests}}',
            postgres_image,
        ],
        timeout,
        "PostgreSQL image inspection",
    ).split("\t")
    if len(mem0_image_metadata) != 2 or len(postgres_image_metadata) != 2:
        raise PreflightError("running image metadata is invalid")
    expected_image_platform = "%s/%s" % (lock["platform"]["architecture"], lock["platform"]["os"])
    _expect(mem0_image_metadata[0], expected_image_platform, "Mem0 image platform")
    _expect(postgres_image_metadata[0], expected_image_platform, "pgvector image platform")
    try:
        postgres_repo_digests = json.loads(postgres_image_metadata[1])
    except json.JSONDecodeError as exc:
        raise PreflightError("pgvector RepoDigests metadata is invalid") from exc
    expected_repo_digest = "%s@%s" % (
        lock["database"]["pgvector"]["image_reference"].split(":", 1)[0],
        lock["database"]["pgvector"]["image_index_digest"],
    )
    if not isinstance(postgres_repo_digests, list) or not any(
        digest == expected_repo_digest or digest.endswith("@" + lock["database"]["pgvector"]["image_index_digest"])
        for digest in postgres_repo_digests
    ):
        raise PreflightError("running pgvector image RepoDigest mismatch")
    probe_output = run_command(
        compose_base
        + ["exec", "--no-TTY", "mem0", "python", "/opt/benchmark/runtime_probe.py"],
        timeout,
        "container runtime probe",
    )
    try:
        probe = json.loads(probe_output)
    except json.JSONDecodeError as exc:
        raise PreflightError("container runtime probe returned invalid JSON") from exc
    return container_id, published_port, _require_mapping(probe, "container runtime probe")


def validate_service_endpoint(host, published_port):
    """Ensure HTTP checks target the same loopback port published by Compose."""
    canonical = validate_url(host, "Mem0 host", root_only=True)
    parsed = urllib.parse.urlsplit(canonical)
    try:
        loopback = parsed.hostname == "localhost" or ipaddress.ip_address(parsed.hostname).is_loopback
    except ValueError:
        loopback = False
    actual_port = parsed.port or (443 if parsed.scheme == "https" else 80)
    if not loopback or parsed.scheme != "http" or actual_port != published_port:
        raise PreflightError("Mem0 host does not match the loopback Compose endpoint")
    return canonical.rstrip("/")


def request_json(base_url, path, api_key, timeout, method="GET", payload=None):
    """Call one official Mem0 endpoint without redirects or credential-bearing errors."""
    url = "%s/%s" % (base_url.rstrip("/"), path.lstrip("/"))
    headers = {"Accept": "application/json"}
    body = None
    if api_key:
        headers["X-API-Key"] = api_key
    if payload is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    opener = urllib.request.build_opener(NoRedirectHandler())
    try:
        with opener.open(request, timeout=timeout) as response:
            content_type = response.headers.get_content_type()
            raw = response.read(MAX_RESPONSE_BYTES + 1)
    except (OSError, urllib.error.URLError, urllib.error.HTTPError) as exc:
        raise PreflightError("official Mem0 request failed for %s" % sanitize_url(url)) from exc
    if content_type != "application/json" or len(raw) > MAX_RESPONSE_BYTES:
        raise PreflightError("official Mem0 returned an invalid response for %s" % sanitize_url(url))
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PreflightError("official Mem0 returned invalid JSON for %s" % sanitize_url(url)) from exc
    return _require_mapping(value, "official Mem0 response")


def _expect(actual, expected, name):
    if actual != expected:
        raise PreflightError("%s mismatch: got %r, want %r" % (name, sanitize(actual), sanitize(expected)))


def _value_at(mapping, path):
    value = mapping
    for part in path:
        if not isinstance(value, dict) or part not in value:
            raise PreflightError("service configuration is missing %s" % ".".join(path))
        value = value[part]
    return value


def validate_runtime_probe(lock, lock_digest, probe):
    """Compare facts measured inside the running container with the lock."""
    mem0 = lock["mem0"]
    python = lock["python"]
    database = lock["database"]
    runtime = lock["runtime"]
    _expect(_value_at(probe, ("source", "commit")), mem0["commit"], "Mem0 source commit")
    _expect(
        _value_at(probe, ("source", "archive_sha256")),
        mem0["source_archive"]["sha256"],
        "Mem0 source archive",
    )
    _expect(
        _value_at(probe, ("source", "server_main_sha256")),
        mem0["server_main_sha256"],
        "official Mem0 REST handler",
    )
    _expect(
        _value_at(probe, ("source", "memory_main_sha256")),
        mem0["runtime_source"]["memory_main_sha256"],
        "locked Mem0 memory core",
    )
    source_identity = _require_mapping(_value_at(probe, ("source", "identity")), "Mem0 source identity")
    _expect(source_identity.get("root"), mem0["runtime_source"]["root"], "Mem0 source root")
    _expect(
        source_identity.get("module_file"),
        mem0["runtime_source"]["module_file"],
        "imported mem0.__file__",
    )
    _expect(source_identity.get("tree_read_only"), True, "locked Mem0 source read-only state")
    if not isinstance(source_identity.get("checked_paths"), int) or source_identity["checked_paths"] <= 0:
        raise PreflightError("locked Mem0 source permission scan is incomplete")
    _expect(
        _value_at(probe, ("artifacts", "environment_lock_sha256")),
        lock_digest,
        "embedded environment lock",
    )
    _expect(
        _value_at(probe, ("artifacts", "requirements_lock_sha256")),
        python["requirements"]["lock_sha256"],
        "embedded requirements lock",
    )
    _expect(
        _value_at(probe, ("artifacts", "spacy_model_sha256")),
        python["nlp"]["model"]["sha256"],
        "embedded spaCy model",
    )
    _expect(_value_at(probe, ("python", "version")), python["version"], "Python version")
    _expect(_value_at(probe, ("python", "implementation")), "CPython", "Python implementation")
    _expect(_value_at(probe, ("python", "machine")), lock["platform"]["machine"], "machine architecture")
    _expect(
        _value_at(probe, ("distribution", "name")),
        mem0["distribution"]["name"],
        "Mem0 dependency distribution",
    )
    _expect(
        _value_at(probe, ("distribution", "version")),
        mem0["distribution"]["version"],
        "Mem0 dependency distribution version",
    )
    _expect(_value_at(probe, ("server", "module")), runtime["server"]["module"], "Mem0 server module")
    _expect(_value_at(probe, ("server", "workers")), "1", "Mem0 worker count")
    _expect(
        _value_at(probe, ("capabilities", "add_prompt_semantics")),
        PROMPT_SEMANTICS,
        "Mem0 add prompt semantics",
    )
    measured_nlp = _require_mapping(probe.get("nlp"), "measured NLP runtime")
    _expect(measured_nlp.get("spacy_version"), python["nlp"]["spacy_version"], "spaCy version")
    _expect(measured_nlp.get("model_name"), python["nlp"]["model"]["name"], "spaCy model name")
    _expect(measured_nlp.get("model_version"), python["nlp"]["model"]["version"], "spaCy model version")
    full_pipeline = measured_nlp.get("full_pipeline")
    if not isinstance(full_pipeline, list) or not set(python["nlp"]["required_pipeline_components"]).issubset(
        full_pipeline
    ):
        raise PreflightError("spaCy full pipeline is incomplete")
    lemma_pipeline = measured_nlp.get("lemma_pipeline")
    if not isinstance(lemma_pipeline, list) or "lemmatizer" not in lemma_pipeline:
        raise PreflightError("spaCy lemma pipeline is incomplete")
    _expect(measured_nlp.get("lemmatization_verified"), True, "BM25 lemmatization capability")
    _expect(measured_nlp.get("entity_extraction_verified"), True, "entity extraction capability")

    measured = _require_mapping(probe.get("runtime"), "measured runtime")
    _expect(measured.get("auth_disabled"), runtime["auth_disabled"], "AUTH_DISABLED")
    _expect(measured.get("telemetry_enabled"), runtime["telemetry_enabled"], "MEM0_TELEMETRY")
    _expect(measured.get("openai_base_url"), runtime["llm"]["base_url"], "OpenAI base URL")
    _expect(measured.get("llm_model"), runtime["llm"]["model"], "LLM model")
    _expect(measured.get("embedder_model"), runtime["embedder"]["model"], "embedder model")
    _expect(measured.get("collection_name"), runtime["vector_store"]["collection"], "collection name")

    measured_database = _require_mapping(probe.get("database"), "measured database")
    postgres = database["postgres"]
    expected_postgres = "%s (%s)" % (postgres["version"], postgres["distribution"])
    _expect(measured_database.get("postgres_version"), expected_postgres, "PostgreSQL version")
    _expect(measured_database.get("postgres_version_num"), postgres["version_num"], "PostgreSQL version number")
    _expect(measured_database.get("pgvector_installed"), True, "pgvector installation")
    _expect(measured_database.get("pgvector_version"), database["pgvector"]["version"], "pgvector version")
    _expect(measured_database.get("vector_dimensions"), 3, "pgvector dimensions function")
    _expect(measured_database.get("l2_distance"), 1.0, "pgvector distance operator")
    _expect(measured_database.get("hnsw_available"), True, "pgvector HNSW access method")


def _supports_string(schema):
    if not isinstance(schema, dict):
        return False
    if schema.get("type") == "string":
        return True
    variants = []
    for field in ("anyOf", "oneOf"):
        value = schema.get(field)
        if isinstance(value, list):
            variants.extend(value)
    return any(isinstance(variant, dict) and variant.get("type") == "string" for variant in variants)


def _supports_boolean(schema):
    if not isinstance(schema, dict):
        return False
    if schema.get("type") == "boolean":
        return True
    variants = []
    for field in ("anyOf", "oneOf"):
        value = schema.get(field)
        if isinstance(value, list):
            variants.extend(value)
    return any(isinstance(variant, dict) and variant.get("type") == "boolean" for variant in variants)


def validate_official_api(lock, openapi, config, providers):
    """Verify official API identity, required routes, and effective configuration."""
    runtime = lock["runtime"]
    _expect(_value_at(openapi, ("info", "title")), runtime["server"]["api_title"], "Mem0 API title")
    _expect(_value_at(openapi, ("info", "version")), runtime["server"]["api_version"], "Mem0 API version")
    paths = _require_mapping(openapi.get("paths"), "OpenAPI paths")
    if "/benchmark/provenance" in paths:
        raise PreflightError("benchmark-specific Mem0 API routes are forbidden")
    for path, methods in REQUIRED_API_METHODS.items():
        available = _require_mapping(paths.get(path), "OpenAPI path %s" % path)
        if not methods.issubset(available):
            raise PreflightError("official Mem0 API is missing required methods for %s" % path)
    memory_post = _require_mapping(paths["/memories"].get("post"), "OpenAPI POST /memories")
    request_schema = _require_mapping(
        _value_at(memory_post, ("requestBody", "content", "application/json", "schema")),
        "OpenAPI POST /memories request schema",
    )
    if request_schema.get("$ref") != "#/components/schemas/MemoryCreate":
        raise PreflightError("official POST /memories does not use MemoryCreate")
    memory_create = _require_mapping(
        _value_at(openapi, ("components", "schemas", "MemoryCreate")),
        "OpenAPI MemoryCreate schema",
    )
    prompt_schema = _require_mapping(
        _value_at(memory_create, ("properties", "prompt")),
        "OpenAPI MemoryCreate.prompt schema",
    )
    if not _supports_string(prompt_schema):
        raise PreflightError("locked Mem0 version does not expose add(prompt=...) as a string field")
    search_post = _require_mapping(paths["/search"].get("post"), "OpenAPI POST /search")
    search_schema = _require_mapping(
        _value_at(search_post, ("requestBody", "content", "application/json", "schema")),
        "OpenAPI POST /search request schema",
    )
    if search_schema.get("$ref") != "#/components/schemas/SearchRequest":
        raise PreflightError("official POST /search does not use SearchRequest")
    search_request = _require_mapping(
        _value_at(openapi, ("components", "schemas", "SearchRequest")),
        "OpenAPI SearchRequest schema",
    )
    explain_schema = _require_mapping(
        _value_at(search_request, ("properties", "explain")),
        "OpenAPI SearchRequest.explain schema",
    )
    if not _supports_boolean(explain_schema):
        raise PreflightError("locked Mem0 version does not expose search explain")

    vector = runtime["vector_store"]
    _expect(_value_at(config, ("version",)), "v1.1", "Mem0 config version")
    _expect(_value_at(config, ("vector_store", "provider")), vector["provider"], "vector provider")
    _expect(_value_at(config, ("vector_store", "config", "host")), vector["host"], "PostgreSQL host")
    _expect(_value_at(config, ("vector_store", "config", "port")), vector["port"], "PostgreSQL port")
    _expect(_value_at(config, ("vector_store", "config", "dbname")), vector["database"], "PostgreSQL database")
    _expect(
        _value_at(config, ("vector_store", "config", "collection_name")),
        vector["collection"],
        "vector collection",
    )
    exposed_password = _value_at(config, ("vector_store", "config", "password"))
    if exposed_password not in {None, "", "[redacted]"}:
        raise PreflightError("official Mem0 configuration exposed a database password")
    for component in ("llm", "embedder"):
        expected = runtime[component]
        _expect(_value_at(config, (component, "provider")), expected["provider"], "%s provider" % component)
        _expect(_value_at(config, (component, "config", "model")), expected["model"], "%s model" % component)
        exposed_key = _value_at(config, (component, "config", "api_key"))
        if exposed_key not in {None, "", "[redacted]"}:
            raise PreflightError("official Mem0 configuration exposed an API key")
    for component in ("llm", "embedder"):
        available = providers.get(component)
        if not isinstance(available, list) or runtime[component]["provider"] not in available:
            raise PreflightError("configured %s provider is not bundled" % component)


def _results(response, name):
    results = response.get("results")
    if not isinstance(results, list) or not results:
        raise PreflightError("%s did not return results" % name)
    return results


def exercise_capabilities(base_url, api_key, timeout, requester=request_json):
    """Exercise LLM generation plus reversible prompt, memory, and vector operations."""
    generated = requester(
        base_url,
        "/generate-instructions",
        api_key,
        timeout,
        method="POST",
        payload={"use_case": "A deterministic preflight capability check."},
    )
    instruction_fields = ("custom_instructions", "test_message")
    if not all(isinstance(generated.get(key), str) and generated[key].strip() for key in instruction_fields):
        raise PreflightError("LLM capability probe returned an invalid response")

    nonce = uuid.uuid4().hex
    user_id = "mem0-preflight-%s" % nonce
    source_token = "OBSERVATION_SOURCE_%s" % nonce
    selected_marker = "OBSERVATION_KEEP_%s" % nonce
    excluded_token = "OBSERVATION_SKIP_%s" % nonce
    observation_instruction = (
        "Use the standard fact-extraction protocol. For this observation, extract only the fact "
        "containing the exact code %s. Preserve Alice, attended meetings, Acme Corporation, London, "
        "and the selected code in that memory, append the exact marker %s, ignore the fact containing "
        "%s, and preserve both selected strings verbatim."
        % (source_token, selected_marker, excluded_token)
    )
    failure = None
    capabilities = {}
    try:
        created = requester(
            base_url,
            "/memories",
            api_key,
            timeout,
            method="POST",
            payload={
                "messages": [{
                    "role": "user",
                    "content": (
                        "Alice attended meetings at Acme Corporation in London while using active calibration "
                        "code %s. My obsolete calibration code is %s."
                    ) % (source_token, excluded_token),
                }],
                "user_id": user_id,
                "infer": True,
                "prompt": observation_instruction,
            },
        )
        created_results = _results(created, "memory create capability probe")
        created_memories = [item for item in created_results if isinstance(item, dict)]
        memory_ids = {
            item.get("id") for item in created_memories if isinstance(item.get("id"), str) and item.get("id")
        }
        memory_text = "\n".join(
            item.get("memory", "") for item in created_memories if isinstance(item.get("memory"), str)
        )
        if not memory_ids or not memory_text:
            raise PreflightError("memory create capability probe returned no structured extracted memory")
        required_tokens = (
            source_token.lower(),
            selected_marker.lower(),
            "alice",
            "attended meetings",
            "acme corporation",
            "london",
        )
        missing_required = not all(token in memory_text.lower() for token in required_tokens)
        included_excluded = excluded_token.lower() in memory_text.lower()
        if missing_required or included_excluded:
            raise PreflightError(
                "locked Mem0 add(prompt=...) did not apply observation instructions within the extraction protocol"
            )
        capabilities["memory_create"] = True
        capabilities["observation_prompt"] = True

        searched = requester(
            base_url,
            "/search",
            api_key,
            timeout,
            method="POST",
            payload={
                "query": "Where did Alice attend meetings at Acme Corporation?",
                "filters": {"user_id": user_id},
                "top_k": 5,
                "threshold": 0.1,
                "explain": True,
            },
        )
        search_results = _results(searched, "memory search capability probe")
        matches = [
            item
            for item in search_results
            if isinstance(item, dict) and item.get("id") in memory_ids
        ]
        if not matches:
            raise PreflightError("memory search capability probe did not find its canary")
        capabilities["memory_search"] = True
        details = matches[0].get("score_details")
        if not isinstance(details, dict):
            raise PreflightError("memory search capability probe did not return score details")
        if not isinstance(details.get("bm25_score"), (int, float)) or details["bm25_score"] <= 0:
            raise PreflightError("memory search capability probe did not exercise BM25 scoring")
        capabilities["bm25_scoring"] = True
        if not isinstance(details.get("entity_boost"), (int, float)) or details["entity_boost"] <= 0:
            raise PreflightError("memory search capability probe did not exercise entity scoring")
        capabilities["entity_scoring"] = True
        capabilities["search_explain"] = True
    except PreflightError as exc:
        failure = exc
    try:
        deleted = requester(
            base_url,
            "/memories?%s" % urllib.parse.urlencode({"user_id": user_id}),
            api_key,
            timeout,
            method="DELETE",
        )
        if not isinstance(deleted.get("message"), str) or not deleted["message"]:
            raise PreflightError("memory cleanup capability probe returned an invalid response")
        capabilities["memory_delete"] = True
    except PreflightError as exc:
        raise PreflightError("memory capability probe cleanup failed") from exc
    if failure is not None:
        raise failure
    capabilities.update({"configuration": True, "llm_generation": True})
    if set(capabilities) != REQUIRED_CAPABILITIES or not all(capabilities.values()):
        raise PreflightError("required Mem0 capabilities are incomplete")
    return capabilities


def run_preflight(args):
    """Run all checks and return a sanitized, reproducible audit record."""
    if args.timeout <= 0:
        raise PreflightError("timeout must be positive")
    lock_path = Path(args.lock).resolve()
    lock = load_lock(lock_path)
    lock_digest = file_sha256(lock_path)
    compose_base = _compose_base(args.compose_file, args.env_file)
    container_id, published_port, probe = inspect_compose_runtime(compose_base, args.timeout, lock)
    service_url = validate_service_endpoint(args.host, published_port)
    validate_runtime_probe(lock, lock_digest, probe)

    openapi = request_json(service_url, "/openapi.json", args.api_key, args.timeout)
    config = request_json(service_url, "/configure", args.api_key, args.timeout)
    providers = request_json(service_url, "/configure/providers", args.api_key, args.timeout)
    validate_official_api(lock, openapi, config, providers)
    capabilities = exercise_capabilities(service_url, args.api_key, args.timeout)
    return {
        "status": "ok",
        "service_url": service_url,
        "container_id": container_id[:12],
        "environment_lock": {"path": str(lock_path), "sha256": lock_digest},
        "runtime": sanitize(probe),
        "official_api": {
            "title": openapi["info"]["title"],
            "version": openapi["info"]["version"],
        },
        "configuration": sanitize(config),
        "capabilities": capabilities,
    }


def parse_args(argv=None):
    """Parse preflight command-line arguments."""
    root = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default=os.environ.get("MEM0_HOST", "http://localhost:8888"))
    parser.add_argument("--api-key", default=os.environ.get("MEM0_API_KEY", ""))
    parser.add_argument("--lock", default=str(root / "environment.lock.json"))
    parser.add_argument("--compose-file", default=str(root / "compose.yaml"))
    parser.add_argument("--env-file", default=os.environ.get("MEM0_ENV_FILE", str(root / ".env")))
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--output", help="Write the sanitized audit record to this JSON file")
    return parser.parse_args(argv)


def write_private(path, text):
    """Write an audit record with owner-only permissions."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            descriptor = -1
            output.write(text)
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def main(argv=None):
    """Run preflight and return a shell-compatible exit code."""
    args = parse_args(argv)
    try:
        result = run_preflight(args)
        output = json.dumps(result, indent=2, sort_keys=True) + "\n"
        if args.output:
            write_private(args.output, output)
        else:
            sys.stdout.write(output)
    except (OSError, PreflightError) as exc:
        message = str(exc) if isinstance(exc, PreflightError) else "cannot write preflight output"
        print("Mem0 benchmark preflight failed: %s" % message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
