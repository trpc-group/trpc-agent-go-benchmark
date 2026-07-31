#!/usr/bin/env bash
set -Eeuo pipefail

root=/data/validation/results/cleanroom-native-noloopwarn-timeout4h-r3-20260731
work=/data/validation/worktrees/bench-swe-ast-public-65d9968/swebench
python=/data/validation/swebench-py/bin/python
plan="$root/plan.json"
metrics="$root/v12-metrics.py"

expected_plan_sha=${V12_PLAN_SHA256:?V12_PLAN_SHA256 is required}
expected_queue_sha=${V12_HARNESS_QUEUE_SHA256:?V12_HARNESS_QUEUE_SHA256 is required}
expected_metrics_sha=${V12_METRICS_SHA256:?V12_METRICS_SHA256 is required}

runs=(
  tag-cleanroom-noloopwarn-timeout4h-native-20260731-r1
  tag-cleanroom-noloopwarn-timeout4h-native-20260731-r2
  tag-cleanroom-noloopwarn-timeout4h-native-20260731-r3
)

log() {
  printf '%s %s\n' "$(date --iso-8601=seconds)" "$*"
}

verify_sha256() {
  local expected=$1 path=$2 actual
  actual=$(sha256sum "$path" | awk '{print $1}')
  if [[ "$actual" != "$expected" ]]; then
    log "sha256_mismatch path=$path expected=$expected actual=$actual"
    exit 3
  fi
}

wait_for_all_generation_gates() {
  while [[ ! -s "$root/all-generation-complete-at.txt" ]]; do
    if [[ ! -s "$root/controller.pid" ]]; then
      log "generation_controller_pid_missing"
      exit 7
    fi
    if ! kill -0 "$(tr -d '[:space:]' <"$root/controller.pid")" 2>/dev/null; then
      log "generation_controller_not_active"
      exit 7
    fi
    sleep 30
  done
  local run_id run_dir
  for run_id in "${runs[@]}"; do
    run_dir="$root/$run_id"
    test -s "$run_dir/completed-at.txt"
    test -s "$run_dir/raw/tag/preds.json"
    python3 -c '
import json
import sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert d["issues"] == []
assert d["case_count"] == 500
assert d["prediction_count"] == 500
assert d["tool_loop_warning_enabled"] is False
assert d["tool_loop_warning_count"] == 0
assert d["tool_loop_warning_case_count"] == 0
' "$run_dir/generation-audit.json"
  done
  log "all_generation_gates_verified harness_unblinded=1"
}

wait_for_resource_headroom() {
  local available_kb free_kb memory_full io_full
  while true; do
    available_kb=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
    free_kb=$(df -Pk /data | awk 'NR==2 {print $4}')
    memory_full=$(awk '/^full / {sub("avg10=", "", $2); print $2}' \
      /proc/pressure/memory)
    io_full=$(awk '/^full / {sub("avg10=", "", $2); print $2}' /proc/pressure/io)
    if (( available_kb >= 16 * 1024 * 1024 && free_kb >= 100 * 1024 * 1024 )) \
      && awk -v a="$memory_full" -v b="$io_full" \
        'BEGIN {exit !(a < 0.10 && b < 0.50)}'; then
      log "harness_resource_gate_passed mem_available_kb=$available_kb data_free_kb=$free_kb memory_full_avg10=$memory_full io_full_avg10=$io_full"
      return
    fi
    log "harness_resource_gate_wait mem_available_kb=$available_kb data_free_kb=$free_kb memory_full_avg10=$memory_full io_full_avg10=$io_full"
    sleep 60
  done
}

assert_no_active_harness() {
  if pgrep -f 'go run ./evaluator verify|/evaluator verify|swebench.*run_evaluation' >/dev/null; then
    log "harness_gate_failed evaluator_active"
    exit 5
  fi
  if docker ps --format '{{.Names}}' | grep -Eq '(^|-)eval($|-)|sweb\.eval'; then
    log "harness_gate_failed eval_container_active"
    exit 5
  fi
}

run_harness() {
  local run_id=$1 run_dir harness_dir output rc sar_pid
  run_dir="$root/$run_id"
  harness_dir="$run_dir/harness"
  output="$harness_dir/local-harness-report/tag"
  if [[ -e "$harness_dir" ]]; then
    log "refusing_existing_harness_dir run_id=$run_id path=$harness_dir"
    exit 6
  fi
  wait_for_resource_headroom
  assert_no_active_harness
  mkdir -p "$harness_dir/local-harness-report"
  printf '%s\n' 4 >"$harness_dir/workers.txt"
  date --iso-8601=seconds >"$harness_dir/harness-started-at.txt"
  sar -u -r -d 60 >"$harness_dir/sar.log" 2>&1 &
  sar_pid=$!
  printf '%s\n' "$sar_pid" >"$harness_dir/sar.pid"
  cleanup_sar() {
    if kill -0 "$sar_pid" 2>/dev/null; then
      kill "$sar_pid" 2>/dev/null || true
      wait "$sar_pid" 2>/dev/null || true
    fi
  }
  trap cleanup_sar EXIT INT TERM
  log "harness_start run_id=$run_id workers=4"
  set +e
  (
    cd "$work"
    GOWORK=off /usr/bin/time -v go run ./evaluator verify \
      --run-id "$run_id" \
      --target tag \
      --predictions "$run_dir/raw/tag/preds.json" \
      --output "$output" \
      --python "$python" \
      --harness-workers 4 \
      --verifier-mode calibrated \
      --cache-level instance \
      --clean=false \
      --apply-harness-compat=true \
      --instances-from-predictions=true
  ) >"$harness_dir/harness-controller.log" 2>"$harness_dir/harness-time.txt"
  rc=$?
  set -e
  cleanup_sar
  trap - EXIT INT TERM
  printf '%s\n' "$rc" >"$harness_dir/harness-exit.status"
  date --iso-8601=seconds >"$harness_dir/harness-finished-at.txt"
  if [[ "$rc" -ne 0 ]]; then
    log "harness_stopped run_id=$run_id exit=$rc"
    exit "$rc"
  fi
  set +e
  python3 "$metrics" "$root" "$run_id" 4 "$output" \
    >"$harness_dir/metrics-controller.log" 2>"$harness_dir/metrics-error.log"
  rc=$?
  set -e
  if [[ "$rc" -ne 0 ]]; then
    log "harness_metrics_gate_stopped run_id=$run_id exit=$rc"
    exit "$rc"
  fi
  date --iso-8601=seconds >"$harness_dir/metrics-complete-at.txt"
  log "harness_metrics_gate_passed run_id=$run_id workers=4"
}

mkdir -p "$root"
if [[ -e "$root/harness-queue-started-at.txt" ]]; then
  log "refusing_existing_harness_queue_start"
  exit 6
fi
verify_sha256 "$expected_plan_sha" "$plan"
verify_sha256 "$expected_queue_sha" "$0"
verify_sha256 "$expected_metrics_sha" "$metrics"
test "$(git -C "$(dirname "$work")" rev-parse HEAD)" \
  = 65d99680dfb0af1bccaf46ea2bd477917a136afb
test -z "$(git -C "$(dirname "$work")" status --porcelain)"

printf '%s\n' "$$" >"$root/harness-queue.pid"
date --iso-8601=seconds >"$root/harness-queue-started-at.txt"
log "harness_queue_start blinded_until_all_generation_complete=1"
wait_for_all_generation_gates
for run_id in "${runs[@]}"; do
  run_harness "$run_id"
done
date --iso-8601=seconds >"$root/all-harness-complete-at.txt"
log "all_harness_metrics_complete"
