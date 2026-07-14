#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.

"""Generate deterministic text hashes from a pinned mini-swe-agent checkout."""

import argparse
import hashlib
import json
import subprocess
from pathlib import Path

import yaml
from jinja2 import StrictUndefined, Template


UPSTREAM_COMMIT = "3a9b8e874d322a9cfb1f391ff4f4df67721c108c"
TASK = "fix 100% behavior\nsecond line"
FORMAT_ERRORS = [
    "No tool calls found in the response. Every response MUST include at least one tool call.",
    "Unknown tool 'other'.Missing 'command' argument in bash tool call.",
    "Error parsing tool call arguments: Expecting value: line 1 column 12 (char 11). Missing 'command' argument in bash tool call.",
]


def record(value: str) -> dict:
    return {
        "length": len(value),
        "sha256": hashlib.sha256(value.encode()).hexdigest(),
    }


def render(template: str, **values) -> str:
    return Template(template, undefined=StrictUndefined).render(**values)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("checkout", type=Path)
    args = parser.parse_args()
    commit = subprocess.check_output(
        ["git", "-C", str(args.checkout), "rev-parse", "HEAD"], text=True
    ).strip()
    if commit != UPSTREAM_COMMIT:
        raise SystemExit(f"checkout is {commit}, expected {UPSTREAM_COMMIT}")

    config_path = args.checkout / "src/minisweagent/config/benchmarks/swebench.yaml"
    config = yaml.safe_load(config_path.read_text())
    system = render(config["agent"]["system_template"], task=TASK)
    instance = render(config["agent"]["instance_template"], task=TASK)
    observations = []
    for output_chars, exception_info, returncode in [
        (0, "", 0),
        (9999, "", 7),
        (10000, "boom", 0),
        (10001, "", 1),
    ]:
        value = render(
            config["model"]["observation_template"],
            output={
                "output": "界" * output_chars,
                "returncode": returncode,
                "exception_info": exception_info,
            },
        )
        observations.append(
            {
                "output_chars": output_chars,
                "exception_info": exception_info,
                "returncode": returncode,
                **record(value),
            }
        )
    format_errors = [
        {
            "error": error,
            **record(render(config["model"]["format_error_template"], error=error)),
        }
        for error in FORMAT_ERRORS
    ]
    document = {
        "upstream_commit": commit,
        "task": TASK,
        "system": record(system),
        "instance": record(instance),
        "observations": observations,
        "format_errors": format_errors,
    }
    print(json.dumps(document, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
