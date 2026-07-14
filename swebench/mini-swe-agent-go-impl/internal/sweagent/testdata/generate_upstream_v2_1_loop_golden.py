#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.

"""Run upstream DefaultAgent with deterministic fake model/environment."""

import argparse
import hashlib
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace

import yaml


UPSTREAM_COMMIT = "3a9b8e874d322a9cfb1f391ff4f4df67721c108c"
TASK = "fix deterministic issue"
SUBMISSION = "diff --git a/a b/a\n"


def digest(value: str) -> dict:
    return {"length": len(value), "sha256": hashlib.sha256(value.encode()).hexdigest()}


def tool_call(call_id: str, command: str):
    return SimpleNamespace(
        id=call_id,
        function=SimpleNamespace(name="bash", arguments=json.dumps({"command": command})),
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("checkout", type=Path)
    args = parser.parse_args()
    commit = subprocess.check_output(
        ["git", "-C", str(args.checkout), "rev-parse", "HEAD"], text=True
    ).strip()
    if commit != UPSTREAM_COMMIT:
        raise SystemExit(f"checkout is {commit}, expected {UPSTREAM_COMMIT}")
    sys.path.insert(0, str(args.checkout / "src"))
    os.environ["MSWEA_SILENT_STARTUP"] = "1"
    os.environ["MSWEA_GLOBAL_CONFIG_DIR"] = tempfile.mkdtemp(prefix="mini-swe-agent-oracle-")

    from minisweagent.agents.default import DefaultAgent
    from minisweagent.exceptions import Submitted
    from minisweagent.models.utils.actions_toolcall import (
        format_toolcall_observation_messages,
        parse_toolcall_actions,
    )

    config = yaml.safe_load(
        (args.checkout / "src/minisweagent/config/benchmarks/swebench.yaml").read_text()
    )

    class FakeModel:
        def __init__(self):
            self.index = 0

        def query(self, messages):
            self.index += 1
            if self.index == 1:
                calls = []
                content = "invalid assistant is not retained"
            elif self.index == 2:
                calls = [tool_call("inspect", "inspect")]
                content = "inspect"
            else:
                calls = [tool_call("before", "before-submit"), tool_call("submit", "submit")]
                content = "finish"
            actions = parse_toolcall_actions(
                calls, format_error_template=config["model"]["format_error_template"]
            )
            return {
                "role": "assistant",
                "content": content,
                "extra": {"actions": actions, "cost": 0.0},
            }

        def format_message(self, **kwargs):
            return kwargs

        def format_observation_messages(self, message, outputs, template_vars=None):
            return format_toolcall_observation_messages(
                actions=message["extra"]["actions"],
                outputs=outputs,
                observation_template=config["model"]["observation_template"],
                template_vars=template_vars,
            )

        def get_template_vars(self):
            return {}

        def serialize(self):
            return {}

    class FakeEnvironment:
        def __init__(self):
            self.commands = []

        def execute(self, action):
            command = action["command"]
            self.commands.append(command)
            if command == "submit":
                raise Submitted(
                    {
                        "role": "exit",
                        "content": SUBMISSION,
                        "extra": {"exit_status": "Submitted", "submission": SUBMISSION},
                    }
                )
            if command == "inspect":
                return {"output": "inspected\n", "returncode": 0, "exception_info": ""}
            return {"output": "before\n", "returncode": 0, "exception_info": ""}

        def get_template_vars(self):
            return {}

        def serialize(self):
            return {}

    environment = FakeEnvironment()
    agent = DefaultAgent(FakeModel(), environment, **config["agent"])
    info = agent.run(TASK)
    normalized_messages = []
    for message in agent.messages:
        role = message["role"]
        item = {"role": role}
        if role in {"system", "user", "tool"}:
            item["content"] = digest(message.get("content", ""))
        else:
            item["content"] = message.get("content", "")
        if role == "assistant":
            item["actions"] = [
                {"command": action["command"], "tool_call_id": action["tool_call_id"]}
                for action in message["extra"]["actions"]
            ]
        if role == "tool":
            item["tool_call_id"] = message["tool_call_id"]
            item["raw_output"] = message["extra"]["raw_output"]
            item["returncode"] = message["extra"]["returncode"]
        if role == "exit":
            item["exit_status"] = message["extra"]["exit_status"]
            item["submission"] = message["extra"]["submission"]
        normalized_messages.append(item)
    document = {
        "upstream_commit": commit,
        "task": TASK,
        "info": info,
        "api_calls": agent.n_calls,
        "commands": environment.commands,
        "messages": normalized_messages,
    }
    print(json.dumps(document, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
