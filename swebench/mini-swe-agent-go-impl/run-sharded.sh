#!/usr/bin/env bash
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#

set -uo pipefail

run_prefix=""
model_config=""
cases="data/generated/cases.jsonl"
environment_config="config/environments/swebench-testbed.yaml"
plan_dir=""
runs_root="results/runs"
initial_workers=15
max_workers=20
stall_seconds=600
poll_seconds=60
observation_codec="xml"

usage() {
  cat <<'EOF'
usage: run-sharded.sh --run-prefix ID --model-config PATH [options]

Options:
  --cases PATH
  --environment-config PATH
  --plan-dir PATH
  --runs-root PATH
  --initial-workers N
  --max-workers N
  --stall-seconds N
  --poll-seconds N
  --observation-codec xml|json|text
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --run-prefix) run_prefix="$2"; shift 2 ;;
    --model-config) model_config="$2"; shift 2 ;;
    --cases) cases="$2"; shift 2 ;;
    --environment-config) environment_config="$2"; shift 2 ;;
    --plan-dir) plan_dir="$2"; shift 2 ;;
    --runs-root) runs_root="$2"; shift 2 ;;
    --initial-workers) initial_workers="$2"; shift 2 ;;
    --max-workers) max_workers="$2"; shift 2 ;;
    --stall-seconds) stall_seconds="$2"; shift 2 ;;
    --poll-seconds) poll_seconds="$2"; shift 2 ;;
    --observation-codec) observation_codec="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$run_prefix" || -z "$model_config" ]]; then
  usage >&2
  exit 2
fi
if [[ ! "$run_prefix" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ || ${#run_prefix} -gt 128 ]]; then
  echo "run prefix must use 1-128 letters, digits, dots, underscores, or hyphens" >&2
  exit 2
fi
if [[ "$observation_codec" != "xml" && "$observation_codec" != "json" && "$observation_codec" != "text" ]]; then
  echo "observation codec must be xml, json, or text" >&2
  exit 2
fi
for value in "$initial_workers" "$max_workers" "$stall_seconds" "$poll_seconds"; do
  if ! [[ "$value" =~ ^[1-9][0-9]*$ ]]; then
    echo "worker counts and polling intervals must be positive integers" >&2
    exit 2
  fi
done
if (( initial_workers > max_workers )); then
  echo "initial workers must not exceed max workers" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "$0")" && pwd)
swebench_dir=$(cd "$script_dir/.." && pwd)
cd "$swebench_dir"

for path in "$model_config" "$cases" "$environment_config"; do
  if [[ ! -f "$path" ]]; then
    echo "required file not found: $path" >&2
    exit 2
  fi
done

if [[ -z "$plan_dir" ]]; then
  plan_dir="data/generated/batches/$run_prefix"
fi
mkdir -p "$plan_dir" "$runs_root/$run_prefix"
if [[ ! -f "$plan_dir/plan.json" ]]; then
  go run ./evaluator plan-batches \
    --cases "$cases" \
    --output-dir "$plan_dir" \
    --run-prefix "$run_prefix" \
    --batch-size 50 || exit $?
fi

binary="$runs_root/$run_prefix/mini-swe-agent-go-impl"
go build -o "$binary" ./mini-swe-agent-go-impl || exit $?

cleanup_run_containers() {
  local id="$1"
  local containers
  containers=$(docker ps -aq --filter "label=swebench.run_id=$id" 2>/dev/null || true)
  if [[ -n "$containers" ]]; then
    docker rm -f $containers >/dev/null
  fi
}

current_run_id=""
current_runner_pid=""

cleanup_active_run() {
  if [[ -n "$current_runner_pid" ]] && kill -0 "$current_runner_pid" 2>/dev/null; then
    kill "$current_runner_pid" 2>/dev/null || true
    wait "$current_runner_pid" 2>/dev/null || true
  fi
  if [[ -n "$current_run_id" ]]; then
    cleanup_run_containers "$current_run_id"
  fi
}

on_exit() {
  local status=$?
  trap - EXIT INT TERM
  cleanup_active_run
  exit "$status"
}

on_signal() {
  local status="$1"
  trap - EXIT INT TERM
  cleanup_active_run
  exit "$status"
}

trap on_exit EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

progress_stalled() {
  local path="$1"
  local threshold="$2"
  python3 - "$path" "$threshold" <<'PY'
import datetime
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
threshold = float(sys.argv[2])
if not path.exists():
    print(0)
    raise SystemExit
try:
    document = json.loads(path.read_text())
except Exception:
    print(0)
    raise SystemExit
now = datetime.datetime.now(datetime.timezone.utc)
anchors = []
for case in document.get("cases", {}).values():
    if case.get("phase") != "running":
        continue
    value = case.get("last_llm_at") or case.get("started_at")
    if value:
        anchors.append(datetime.datetime.fromisoformat(value.replace("Z", "+00:00")))
print(int(bool(anchors) and all((now - anchor).total_seconds() >= threshold for anchor in anchors)))
PY
}

service_error_counts() {
  local path="$1"
  python3 - "$path" <<'PY'
import json
import sys

counts = json.load(open(sys.argv[1])).get("service_error_counts", {})
transient_names = {
    "endpoint_rate_limit",
    "endpoint_unavailable",
    "endpoint_timeout",
    "endpoint_network",
}
transient = sum(value for key, value in counts.items() if key in transient_names)
permanent = sum(value for key, value in counts.items() if key not in transient_names)
print(transient, permanent)
PY
}

record_attempt() {
  local path="$1"
  shift
  python3 - "$path" "$@" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
record = {
    "run_prefix": sys.argv[2],
    "run_id": sys.argv[3],
    "shard": sys.argv[4],
    "attempt": int(sys.argv[5]),
    "workers": int(sys.argv[6]),
    "stall_seconds": int(sys.argv[7]),
    "started_at": sys.argv[8],
    "finished_at": sys.argv[9],
    "runner_exit": int(sys.argv[10]),
    "stalled": bool(int(sys.argv[11])),
    "transient_endpoint_errors": int(sys.argv[12]),
    "permanent_endpoint_errors": int(sys.argv[13]),
    "observation_codec": sys.argv[14],
}
path.parent.mkdir(parents=True, exist_ok=True)
with path.open("a", encoding="utf-8") as handle:
    handle.write(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n")
PY
}

workers=$initial_workers
w1_failures=0
attempts_path="$runs_root/$run_prefix/attempts.jsonl"
for filter_file in "$plan_dir"/batch-*.filter; do
  batch_name=$(basename "$filter_file" .filter)
  batch_index=${batch_name#batch-}
  run_id="$run_prefix-$batch_index"
  current_run_id="$run_id"
  output="$runs_root/$run_id/raw/mini-go"
  log="$runs_root/$run_id/mini-go-runner.log"
  mkdir -p "$output"
  attempt=0

  while true; do
    attempt=$((attempt + 1))
    current_stall_seconds=$stall_seconds
    if (( workers == 1 && current_stall_seconds < 1800 )); then
      current_stall_seconds=1800
    fi
    attempt_started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    echo "$(date -Is) shard=$batch_index attempt=$attempt workers=$workers stall_seconds=$current_stall_seconds" | tee -a "$log"

    "$binary" \
      --run-id "$run_id" \
      --cases "$cases" \
      --model-config "$model_config" \
      --environment-config "$environment_config" \
      --output "$output" \
      --filter "$(<"$filter_file")" \
      --agent-workers "$workers" \
      --resume-policy retryable \
      --observation-codec "$observation_codec" \
      --case-timeout 2h \
      --command-timeout 1m >>"$log" 2>&1 &
    runner_pid=$!
    current_runner_pid="$runner_pid"
    stalled=0
    while kill -0 "$runner_pid" 2>/dev/null; do
      sleep "$poll_seconds"
      if ! kill -0 "$runner_pid" 2>/dev/null; then
        break
      fi
      if [[ $(progress_stalled "$output/mini-go-runner-progress.json" "$current_stall_seconds") == 1 ]]; then
        stalled=1
        echo "$(date -Is) shard=$batch_index no_llm_progress=1 stopping_pid=$runner_pid" | tee -a "$log"
        kill "$runner_pid" 2>/dev/null || true
        for _ in $(seq 1 30); do
          kill -0 "$runner_pid" 2>/dev/null || break
          sleep 1
        done
        kill -9 "$runner_pid" 2>/dev/null || true
        break
      fi
    done
    wait "$runner_pid"
    runner_status=$?
    current_runner_pid=""
    attempt_finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    transient_errors=0
    permanent_errors=0
    if (( stalled == 0 && runner_status == 0 )) && [[ -f "$output/mini-go-runner-manifest.json" ]]; then
      read -r transient_errors permanent_errors < <(service_error_counts "$output/mini-go-runner-manifest.json")
    fi
    record_attempt "$attempts_path" "$run_prefix" "$run_id" "$batch_index" "$attempt" "$workers" \
      "$current_stall_seconds" "$attempt_started_at" "$attempt_finished_at" "$runner_status" "$stalled" \
      "$transient_errors" "$permanent_errors" "$observation_codec"

    if (( stalled == 1 )); then
      cleanup_run_containers "$run_id"
      workers=$((workers - 2))
      (( workers < 1 )) && workers=1
      continue
    fi
    if (( runner_status != 0 )); then
      echo "$(date -Is) shard=$batch_index runner_exit=$runner_status; refusing automatic retry of a non-endpoint failure" | tee -a "$log" >&2
      exit "$runner_status"
    fi
    if (( permanent_errors > 0 )); then
      echo "$(date -Is) shard=$batch_index permanent_endpoint_errors=$permanent_errors; fix configuration before resuming" | tee -a "$log" >&2
      exit 3
    fi
    if (( transient_errors > 0 )); then
      echo "$(date -Is) shard=$batch_index transient_endpoint_errors=$transient_errors" | tee -a "$log"
      workers=$((workers - 2))
      (( workers < 1 )) && workers=1
      if (( workers == 1 )); then
        w1_failures=$((w1_failures + 1))
        if (( w1_failures >= 3 )); then
          echo "$(date -Is) three consecutive endpoint failures at workers=1; pausing" | tee -a "$log" >&2
          exit 4
        fi
      fi
      continue
    fi

    w1_failures=0
    if (( workers < max_workers )); then
      workers=$((workers + 1))
    fi
    echo "$(date -Is) shard=$batch_index completed next_workers=$workers" | tee -a "$log"
    break
  done
done

go run ./evaluator summarize-shards \
  --plan "$plan_dir/plan.json" \
  --runs-root "$runs_root" \
  --raw-subdir raw/mini-go \
  --output "$runs_root/$run_prefix/shards.json" || exit $?
go run ./evaluator merge-predictions \
  --shards "$runs_root/$run_prefix/shards.json" \
  --cases "$cases" \
  --output "$runs_root/$run_prefix/preds.json"
