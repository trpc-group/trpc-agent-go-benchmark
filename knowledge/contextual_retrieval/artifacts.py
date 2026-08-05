#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Strict JSON artifact helpers for contextual-retrieval experiments."""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
from pathlib import Path
from typing import Any, Dict, Optional


def atomic_write_json(path: str, payload: Dict[str, Any]) -> None:
    """Write one JSON object atomically without depending on the legacy runner."""
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(
        prefix=f".{target.name}.",
        suffix=".tmp",
        dir=str(target.parent),
    )
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(
                payload,
                handle,
                indent=2,
                ensure_ascii=False,
                allow_nan=False,
            )
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, target)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def canonical_digest(value: Any) -> str:
    """Return a SHA-256 digest of a canonical JSON representation."""
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def file_digest(path: str) -> str:
    """Return the SHA-256 digest of a file without loading it all at once."""
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while block := handle.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def text_digest(value: str) -> str:
    """Return the SHA-256 digest of UTF-8 text."""
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def seal_artifact(payload: Dict[str, Any]) -> Dict[str, Any]:
    """Attach a digest that detects any change to an artifact payload."""
    sealed = dict(payload)
    sealed.pop("artifact_digest", None)
    sealed["artifact_digest"] = canonical_digest(sealed)
    return sealed


def write_artifact(path: str, payload: Dict[str, Any]) -> Dict[str, Any]:
    """Seal and atomically write an artifact."""
    sealed = seal_artifact(payload)
    atomic_write_json(path, sealed)
    return sealed


def load_artifact(
    path: str,
    expected_schema: Optional[str] = None,
) -> Dict[str, Any]:
    """Load a sealed artifact and verify its schema and digest."""
    with open(path, "r", encoding="utf-8") as handle:
        payload = json.load(handle)
    if not isinstance(payload, dict):
        raise ValueError(f"artifact must be a JSON object: {path}")
    if expected_schema and payload.get("schema_version") != expected_schema:
        raise ValueError(
            "unsupported artifact schema: "
            f"{payload.get('schema_version')!r}, expected {expected_schema!r}"
        )
    expected_digest = payload.get("artifact_digest")
    if not isinstance(expected_digest, str) or not expected_digest:
        raise ValueError(f"artifact digest is missing: {path}")
    unsigned = dict(payload)
    unsigned.pop("artifact_digest", None)
    actual_digest = canonical_digest(unsigned)
    if actual_digest != expected_digest:
        raise ValueError(f"artifact digest does not match content: {path}")
    return payload


def relative_artifact_path(path: str, base: str) -> str:
    """Return a stable relative path when possible."""
    target = Path(path).resolve()
    root = Path(base).resolve()
    try:
        return str(target.relative_to(root))
    except ValueError:
        return target.name
