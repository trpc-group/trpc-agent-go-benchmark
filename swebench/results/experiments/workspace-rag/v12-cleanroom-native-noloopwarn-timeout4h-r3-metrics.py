#!/usr/bin/env python3
import collections
import json
import os
import pathlib
import sys
from decimal import Decimal


QUEUE = [
    "tag-cleanroom-noloopwarn-timeout4h-native-20260731-r1",
    "tag-cleanroom-noloopwarn-timeout4h-native-20260731-r2",
    "tag-cleanroom-noloopwarn-timeout4h-native-20260731-r3",
]
FIXED_CACHE_HIT_RATES = [Decimal("0"), Decimal("0.90"), Decimal("0.95"), Decimal("0.98"), Decimal("1")]


def load(path):
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def dump(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + ".tmp")
    temp.write_text(
        json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.replace(str(temp), str(path))


def find_harness_report(output):
    matches = []
    for path in output.glob("*.json"):
        value = load(path)
        if isinstance(value, dict) and {
            "total_instances",
            "resolved_instances",
            "error_instances",
            "resolved_ids",
        }.issubset(value):
            matches.append((path, value))
    if len(matches) != 1:
        raise SystemExit("expected one harness report, found %d" % len(matches))
    return matches[0]


def standardized_cost(prompt, completion, hit_rate, pricing):
    unit = Decimal(str(pricing.get("unit_tokens", 1000000)))
    cached_rate = Decimal(str(pricing.get("cached_input", 0)))
    uncached_rate = Decimal(str(pricing.get("uncached_input", 0)))
    output_rate = Decimal(str(pricing.get("output", 0)))
    prompt_rate = hit_rate * cached_rate + (Decimal("1") - hit_rate) * uncached_rate
    return float(
        (Decimal(prompt) * prompt_rate + Decimal(completion) * output_rate) / unit
    )


def main():
    if len(sys.argv) != 5:
        raise SystemExit(
            "usage: v12-metrics.py <root> <run-id> <harness-workers> <harness-output>"
        )
    root = pathlib.Path(sys.argv[1])
    run_id = sys.argv[2]
    harness_workers = int(sys.argv[3])
    harness_output = pathlib.Path(sys.argv[4])
    if run_id not in QUEUE:
        raise SystemExit("run is not in the frozen V12 queue")

    run_dir = root / run_id
    generation = run_dir / "raw" / "tag"
    manifest = load(generation / "tag-runner-manifest.json")
    predictions = load(generation / "preds.json")
    audit = load(run_dir / "generation-audit.json")
    if manifest.get("run_id") != run_id or len(predictions) != 500:
        raise SystemExit("generation manifest or predictions mismatch")
    if audit.get("issues") != [] or audit.get("case_count") != 500:
        raise SystemExit("generation audit did not pass")
    if manifest.get("tool_loop_warning") is not False:
        raise SystemExit("tool_loop_warning was not disabled")

    statuses = collections.Counter()
    error_categories = collections.Counter()
    usage = collections.Counter()
    llm_calls = 0
    tool_calls = 0
    warning_count = 0
    warning_cases = 0
    duration_ms = 0
    tag_count = 0
    call_thresholds = collections.Counter()
    for path in generation.glob("*/*.tag.json"):
        tag = load(path)
        tag_count += 1
        info = tag.get("info") or {}
        statuses[info.get("exit_status")] += 1
        if info.get("error_category"):
            error_categories[info.get("error_category")] += 1
        calls = int(tag.get("llm_calls", 0))
        llm_calls += calls
        tool_calls += int(tag.get("tool_calls", 0))
        warnings = int(tag.get("tool_loop_warning_count", 0))
        warning_count += warnings
        warning_cases += int(warnings > 0)
        duration_ms += int(tag.get("duration_ms", 0))
        call_thresholds["at_least_100"] += int(calls >= 100)
        call_thresholds["at_least_200"] += int(calls >= 200)
        call_thresholds["exactly_250"] += int(calls == 250)
        item = tag.get("usage") or {}
        prompt = int(item.get("prompt_tokens", 0))
        completion = int(item.get("completion_tokens", 0))
        usage["prompt_tokens"] += prompt
        usage["cached_input_tokens"] += int(
            (item.get("prompt_tokens_details") or {}).get("cached_tokens", 0)
        )
        usage["completion_tokens"] += completion
        usage["reasoning_tokens"] += int(
            (item.get("completion_tokens_details") or {}).get("reasoning_tokens", 0)
        )
        usage["total_tokens"] += int(item.get("total_tokens", prompt + completion))
    if tag_count != 500:
        raise SystemExit("expected 500 tags, found %d" % tag_count)
    if warning_count != 0 or warning_cases != 0:
        raise SystemExit("disabled warning run contains warning events")

    pricing = manifest.get("pricing") or {}
    prompt = usage["prompt_tokens"]
    cached = usage["cached_input_tokens"]
    uncached = prompt - cached
    completion = usage["completion_tokens"]
    unit = Decimal(str(pricing.get("unit_tokens", 1000000)))
    actual_cost = (
        Decimal(cached) * Decimal(str(pricing.get("cached_input", 0)))
        + Decimal(uncached) * Decimal(str(pricing.get("uncached_input", 0)))
        + Decimal(completion) * Decimal(str(pricing.get("output", 0)))
    ) / unit
    fixed_costs = {
        ("%g" % float(rate)): standardized_cost(prompt, completion, rate, pricing)
        for rate in FIXED_CACHE_HIT_RATES
    }

    report_path, harness = find_harness_report(harness_output)
    summary = {
        "schema_version": 1,
        "run_id": run_id,
        "arm": "native_no_loop_warning",
        "replicate": int(run_id.rsplit("-r", 1)[1]),
        "harness": {
            "workers": harness_workers,
            "report": str(report_path),
            "total_instances": harness.get("total_instances"),
            "submitted_instances": harness.get("submitted_instances"),
            "completed_instances": harness.get("completed_instances"),
            "resolved_instances": harness.get("resolved_instances"),
            "resolved_rate": harness.get("resolved_instances", 0) / 500,
            "unresolved_instances": harness.get("unresolved_instances"),
            "empty_patch_instances": harness.get("empty_patch_instances"),
            "error_instances": harness.get("error_instances"),
            "resolved_ids": harness.get("resolved_ids", []),
            "error_ids": harness.get("error_ids", []),
            "rate_denominator": 500,
        },
        "generation": {
            "terminal_counts": dict(statuses),
            "error_category_counts": dict(error_categories),
            "llm_calls": llm_calls,
            "tool_calls": tool_calls,
            "tool_loop_warning_enabled": False,
            "tool_loop_warning_count": warning_count,
            "tool_loop_warning_case_count": warning_cases,
            "case_duration_sum_ms": duration_ms,
            "call_threshold_case_counts": dict(call_thresholds),
            "usage": dict(usage),
            "cached_over_prompt_ratio": cached / prompt if prompt else None,
            "cached_over_total_ratio": cached / usage["total_tokens"]
            if usage["total_tokens"]
            else None,
            "cost": {
                "actual": float(actual_cost),
                "all_prompt_uncached": fixed_costs["0"],
                "fixed_prompt_cache_hit_rate": fixed_costs,
                "observed_cache_savings_vs_all_uncached": fixed_costs["0"]
                - float(actual_cost),
                "currency": pricing.get("currency"),
                "pricing": pricing,
            },
        },
        "shadow_repeat_analysis_status": "pending_final_offline_replay",
    }
    summary_path = run_dir / "harness" / "metrics-summary.json"
    dump(summary_path, summary)

    index = []
    for queued in QUEUE:
        path = root / queued / "harness" / "metrics-summary.json"
        if path.is_file():
            value = load(path)
            index.append(
                {
                    "run_id": queued,
                    "replicate": value["replicate"],
                    "resolved_instances": value["harness"]["resolved_instances"],
                    "resolved_rate": value["harness"]["resolved_rate"],
                    "prompt_tokens": value["generation"]["usage"]["prompt_tokens"],
                    "completion_tokens": value["generation"]["usage"][
                        "completion_tokens"
                    ],
                    "total_tokens": value["generation"]["usage"]["total_tokens"],
                    "cached_over_prompt_ratio": value["generation"][
                        "cached_over_prompt_ratio"
                    ],
                    "actual_cost": value["generation"]["cost"]["actual"],
                    "all_prompt_uncached_cost": value["generation"]["cost"][
                        "all_prompt_uncached"
                    ],
                }
            )
    dump(root / "harness-metrics-index.json", {"runs": index})
    print(json.dumps(index[-1], ensure_ascii=False, sort_keys=True))

    if harness.get("total_instances") != 500:
        raise SystemExit("harness total_instances is not 500")
    if harness.get("submitted_instances") != 500:
        raise SystemExit("harness submitted_instances is not 500")
    if harness.get("error_instances") != 0:
        raise SystemExit("harness contains error instances")


if __name__ == "__main__":
    main()
