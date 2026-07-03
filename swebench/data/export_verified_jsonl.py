#!/usr/bin/env python3
"""Export SWE-Bench-Verified instances to JSONL.

This helper intentionally lives outside the Go runner so the benchmark can
keep a small dependency footprint. Install the optional dependency with:

    python3 -m pip install datasets
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dataset", default="princeton-nlp/SWE-bench_Verified")
    parser.add_argument("--split", default="test")
    parser.add_argument("--output", default="swebench_verified_cases.jsonl")
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument(
        "--instances",
        default="",
        help="Comma-separated instance IDs to export. Empty exports the split order.",
    )
    return parser.parse_args()


def normalize(value: Any) -> Any:
    if hasattr(value, "tolist"):
        return value.tolist()
    if isinstance(value, dict):
        return {str(k): normalize(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [normalize(v) for v in value]
    return value


def main() -> None:
    args = parse_args()
    try:
        from datasets import load_dataset
    except ImportError as exc:
        raise SystemExit(
            "missing dependency: install with `python3 -m pip install datasets`"
        ) from exc

    wanted = {
        item.strip()
        for item in args.instances.split(",")
        if item.strip()
    }
    ds = load_dataset(args.dataset, split=args.split)
    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)

    count = 0
    with out.open("w", encoding="utf-8") as f:
        for row in ds:
            row = normalize(dict(row))
            if wanted and row.get("instance_id") not in wanted:
                continue
            f.write(json.dumps(row, ensure_ascii=False, sort_keys=True))
            f.write("\n")
            count += 1
            if args.limit and count >= args.limit:
                break
    print(f"wrote {count} instances to {out}")


if __name__ == "__main__":
    main()
