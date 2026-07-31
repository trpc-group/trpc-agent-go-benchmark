#!/usr/bin/env python3
import hashlib
import json
import pathlib
import sys


EXPECTED = {
    "native_binary_sha256": "839a4f327704554697592335b1fa4a3fd9fac32ec9f8de19923a5e870806e4b8",
    "cases_sha256": "4b2a050a82d356963320cbfa8e2efdf6a133af8863f31b291a973ab4dd349d07",
    "case_list_sha256": "a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9",
    "model_config_sha256": "fbfdf25e9fced3ecc51d244ccbb652c078b02ac640abbdbcca53ddbaa7af27de",
    "environment_config_sha256": "3cfa72f92f4010d242e6adb9bb507ccdf9db261ba5726163b5adecd509c140f0",
    "framework_revision": "358d376784889fd539a764190e1970ee4f4fc5f6",
    "workspace_representation_schema": "workspace-representation/v1;reader=text;chunk=fixed;size=1024;overlap=128;whitespace=trim-lines",
    "workspace_representation_sha256": "4e76ab4ec67f611c6660585c6161fe167b33bc6e7de53950808aff32f989933a",
}

ALLOWED_RUN_IDS = {
    "tag-cleanroom-noloopwarn-timeout4h-native-20260731-r1",
    "tag-cleanroom-noloopwarn-timeout4h-native-20260731-r2",
    "tag-cleanroom-noloopwarn-timeout4h-native-20260731-r3",
}


def load(path):
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def main():
    if len(sys.argv) != 5:
        raise SystemExit(
            "usage: v12-validate.py <output> <cases> <run-id> <audit-output>"
        )

    output = pathlib.Path(sys.argv[1])
    cases_path = pathlib.Path(sys.argv[2])
    run_id = sys.argv[3]
    audit_path = pathlib.Path(sys.argv[4])
    if run_id not in ALLOWED_RUN_IDS:
        raise SystemExit("run-id is not in the frozen V12 queue: %s" % run_id)

    issues = []
    digest = hashlib.sha256(cases_path.read_bytes()).hexdigest()
    if digest != EXPECTED["cases_sha256"]:
        issues.append({"kind": "cases_sha256", "actual": digest})
    cases = [
        json.loads(line)["instance_id"]
        for line in cases_path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]
    case_set = set(cases)
    if len(cases) != 500 or len(case_set) != 500:
        issues.append(
            {"kind": "case_membership", "total": len(cases), "unique": len(case_set)}
        )

    preds = load(output / "preds.json")
    progress = load(output / "tag-runner-progress.json")
    manifest = load(output / "tag-runner-manifest.json")
    if not isinstance(preds, dict) or set(preds) != case_set:
        issues.append(
            {
                "kind": "predictions_membership",
                "count": len(preds) if isinstance(preds, dict) else None,
            }
        )
    progress_cases = progress.get("cases", {})
    if not isinstance(progress_cases, dict) or set(progress_cases) != case_set:
        issues.append(
            {
                "kind": "progress_membership",
                "count": len(progress_cases)
                if isinstance(progress_cases, dict)
                else None,
            }
        )

    expected_manifest = {
        "run_id": run_id,
        "binary_sha256": EXPECTED["native_binary_sha256"],
        "cases_sha256": EXPECTED["cases_sha256"],
        "model_config_sha256": EXPECTED["model_config_sha256"],
        "environment_config_sha256": EXPECTED["environment_config_sha256"],
        "framework_revision": EXPECTED["framework_revision"],
        "selected_case_set_sha256": EXPECTED["case_list_sha256"],
        "case_count": 500,
        "prediction_count": 500,
        "workers": 15,
        "redo_existing": False,
        "observation_codec": "xml",
        "tool_loop_warning": False,
        "workspace_preload": False,
        "command_timeout": "1m0s",
        "case_timeout": "4h0m0s",
        "code_search": False,
        "workspace_representation": "current-fixed",
        "workspace_representation_schema": EXPECTED[
            "workspace_representation_schema"
        ],
        "workspace_representation_sha256": EXPECTED[
            "workspace_representation_sha256"
        ],
        "source_modified": False,
    }
    for key, expected in expected_manifest.items():
        if manifest.get(key) != expected:
            issues.append(
                {
                    "kind": "manifest_mismatch",
                    "key": key,
                    "expected": expected,
                    "actual": manifest.get(key),
                }
            )
    if manifest.get("embedding_config_sha256", "") != "":
        issues.append({"kind": "manifest_embedding_config_sha256"})
    if manifest.get("filter", "") not in {"", None}:
        issues.append({"kind": "unexpected_filter", "value": manifest.get("filter")})
    if manifest.get("case_list", "") not in {"", None}:
        issues.append(
            {"kind": "unexpected_case_list", "value": manifest.get("case_list")}
        )
    if int(manifest.get("tool_loop_warning_count", 0)) != 0:
        issues.append(
            {
                "kind": "manifest_warning_count_nonzero",
                "actual": manifest.get("tool_loop_warning_count"),
            }
        )
    if int(manifest.get("tool_loop_warning_case_count", 0)) != 0:
        issues.append(
            {
                "kind": "manifest_warning_case_count_nonzero",
                "actual": manifest.get("tool_loop_warning_case_count"),
            }
        )

    counts = {
        "submitted": 0,
        "exact_call_limit": 0,
        "four_hour_case_timeout": 0,
    }
    terminal_details = []
    warning_literal = "<tool_loop_detected>"
    for instance_id in cases:
        case_dir = output / instance_id
        tag_path = case_dir / (instance_id + ".tag.json")
        responses_path = case_dir / (instance_id + ".responses.json")
        if not tag_path.is_file() or not responses_path.is_file():
            issues.append({"kind": "missing_artifact", "instance_id": instance_id})
            continue
        tag_text = tag_path.read_text(encoding="utf-8")
        tag = json.loads(tag_text)
        responses = load(responses_path)
        llm_calls = tag.get("llm_calls")
        response_count = len(responses) if isinstance(responses, list) else None
        patch = tag.get("model_patch", "")
        info = tag.get("info") or {}
        status = info.get("exit_status")

        if tag.get("instance_id") != instance_id:
            issues.append({"kind": "tag_instance", "instance_id": instance_id})
        if int(tag.get("tool_loop_warning_count", 0)) != 0:
            issues.append(
                {
                    "kind": "tag_warning_count_nonzero",
                    "instance_id": instance_id,
                    "actual": tag.get("tool_loop_warning_count"),
                }
            )
        if warning_literal in tag_text:
            issues.append(
                {"kind": "model_visible_warning_present", "instance_id": instance_id}
            )
        prediction = preds.get(instance_id, {}) if isinstance(preds, dict) else {}
        if (
            prediction.get("instance_id") != instance_id
            or prediction.get("model_patch", "") != patch
        ):
            issues.append(
                {"kind": "prediction_tag_mismatch", "instance_id": instance_id}
            )

        if status == "Submitted":
            counts["submitted"] += 1
            if response_count != llm_calls:
                issues.append(
                    {
                        "kind": "responses_llm_calls",
                        "instance_id": instance_id,
                        "status": status,
                        "llm_calls": llm_calls,
                        "responses": response_count,
                    }
                )
            continue

        exact_call_limit = (
            status == "Error"
            and info.get("error_category") == "agent_error"
            and info.get("error") == "max LLM calls (250) exceeded"
            and llm_calls == 250
            and response_count == 250
            and patch == ""
        )
        if exact_call_limit:
            counts["exact_call_limit"] += 1
            terminal_details.append(
                {"instance_id": instance_id, "classification": "exact_call_limit"}
            )
            continue

        exact_four_hour_timeout = (
            status == "Timeout"
            and info.get("error_category") == "case_timeout"
            and info.get("error") == "context deadline exceeded"
            and isinstance(llm_calls, int)
            and response_count in {llm_calls, llm_calls - 1}
            and patch == ""
        )
        if exact_four_hour_timeout:
            counts["four_hour_case_timeout"] += 1
            terminal_details.append(
                {
                    "instance_id": instance_id,
                    "classification": "four_hour_case_timeout",
                    "llm_calls": llm_calls,
                    "responses": response_count,
                    "duration_ms": tag.get("duration_ms"),
                }
            )
            continue

        issues.append(
            {
                "kind": "unaudited_terminal",
                "instance_id": instance_id,
                "exit_status": status,
                "error_category": info.get("error_category"),
                "error": info.get("error"),
                "retryable": info.get("retryable"),
                "llm_calls": llm_calls,
                "responses": response_count,
                "patch_length": len(patch),
            }
        )

    report = {
        "run_id": run_id,
        "arm": "native_no_loop_warning",
        "case_count": len(cases),
        "prediction_count": len(preds) if isinstance(preds, dict) else None,
        "counts": counts,
        "terminal_details": terminal_details,
        "issues": issues,
        "tool_loop_warning_enabled": manifest.get("tool_loop_warning"),
        "tool_loop_warning_count": manifest.get("tool_loop_warning_count", 0),
        "tool_loop_warning_case_count": manifest.get(
            "tool_loop_warning_case_count", 0
        ),
        "selected_case_set_sha256": manifest.get("selected_case_set_sha256"),
    }
    audit_path.write_text(
        json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(report, ensure_ascii=False, sort_keys=True))
    if issues:
        raise SystemExit(2)


if __name__ == "__main__":
    main()
