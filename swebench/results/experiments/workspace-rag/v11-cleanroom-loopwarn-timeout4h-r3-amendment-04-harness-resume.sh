#!/usr/bin/env bash
set -Eeuo pipefail

root=/data/validation/results/cleanroom-loopwarn-timeout4h-r3-20260729
base_queue="$root/v11-harness-queue.sh"
amendment_04="$root/amendment-04.json"
expected_amendment_04_sha=${V11_AMENDMENT04_SHA256:?V11_AMENDMENT04_SHA256 is required}

base_queue_sha=079ffdc568776e0738acdee6bfa30ec224375b135e660b5b63af4adbe2f6c3d4
actual_base_queue_sha=$(sha256sum "$base_queue" | awk '{print $1}')
if [[ "$actual_base_queue_sha" != "$base_queue_sha" ]]; then
  printf '%s base_queue_sha_mismatch expected=%s actual=%s\n' \
    "$(date --iso-8601=seconds)" "$base_queue_sha" "$actual_base_queue_sha"
  exit 3
fi

# Reuse only the verified declarations and functions. Line 142 begins the
# original queue's top-level execution and would replay Native R1.
source <(sed -n '1,140p' "$base_queue")

verify_file_sha() {
  local expected=$1 path=$2 actual
  actual=$(sha256sum "$path" | awk '{print $1}')
  if [[ "$actual" != "$expected" ]]; then
    log "sha256_mismatch path=$path expected=$expected actual=$actual"
    exit 3
  fi
}

verify_file_sha "$expected_amendment_04_sha" "$amendment_04"
verify_file_sha 3635981520871e6c0c9d3f51bd351eae6d86a0e761875056ffa341805796b620 \
  "$root/v11-cleanroom-loopwarn-timeout4h-r3-amendment-01.json"
verify_file_sha 9f5c1c47e511fe8ccf308d5214bd4c47556884fbf31158abfb8a86f06ba3b412 \
  "$root/v11-cleanroom-loopwarn-timeout4h-r3-amendment-02.json"
verify_file_sha 62ae3012d2896785e3c7f89489748e73ea48e78133bce542d449dfb81a1b7167 \
  "$root/amendment-03.json"
verify_file_sha 5f698de46f36f82ba9e4408bf8b5b8855b93dff3b05b665c650cbdbe97e0674f \
  "$metrics"

native_r1=tag-cleanroom-loopwarn-timeout4h-native-20260729-r1
native_metrics="$root/$native_r1/harness/metrics-summary.json"
test -s "$root/$native_r1/harness/metrics-complete-at.txt"
test -s "$native_metrics"
python3 - "$root/harness-metrics-index.json" "$native_metrics" <<'PY'
import json
import sys

index = json.load(open(sys.argv[1], encoding="utf-8"))
metrics = json.load(open(sys.argv[2], encoding="utf-8"))
runs = index.get("runs") or []
assert len(runs) == 1
assert runs[0]["run_id"] == "tag-cleanroom-loopwarn-timeout4h-native-20260729-r1"
assert metrics["run_id"] == runs[0]["run_id"]
assert metrics["harness"]["error_instances"] == 0
assert metrics["harness"]["completed_instances"] == 496
PY

if pgrep -f 'go run ./evaluator verify|/evaluator verify|swebench.*run_evaluation' >/dev/null; then
  log "harness_resume_gate_failed evaluator_active"
  exit 5
fi
if docker ps --format '{{.Names}}' | grep -Eq '(^|-)eval($|-)|sweb\.eval'; then
  log "harness_resume_gate_failed eval_container_active"
  exit 5
fi
old_queue_pid=$(tr -d '[:space:]' <"$root/harness-queue.pid")
if [[ "$old_queue_pid" != 1230838 ]] || kill -0 "$old_queue_pid" 2>/dev/null; then
  log "harness_resume_gate_failed unexpected_queue_pid=$old_queue_pid"
  exit 5
fi

cp -a "$root/harness-queue.pid" "$root/harness-queue-primary-stale.pid"
printf '%s\n' "$$" >"$root/harness-queue-amendment-04.pid"
printf '%s\n' "$$" >"$root/harness-queue.pid"
date --iso-8601=seconds >"$root/harness-queue-amendment-04-started-at.txt"
log "harness_queue_resume_start completed_native_r1_verified=1"

runs=(
  tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r1
  tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r2
  tag-cleanroom-loopwarn-timeout4h-native-20260729-r2
  tag-cleanroom-loopwarn-timeout4h-native-20260729-r3
  tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r3
)
arms=(rag_adapt rag_adapt native native rag_adapt)

for index in "${!runs[@]}"; do
  wait_for_generation_gate "${runs[$index]}"
  run_harness "${runs[$index]}" "${arms[$index]}" "$index"
done
date --iso-8601=seconds >"$root/all-harness-complete-at.txt"
log "all_harness_metrics_complete recovery=amendment-04"
