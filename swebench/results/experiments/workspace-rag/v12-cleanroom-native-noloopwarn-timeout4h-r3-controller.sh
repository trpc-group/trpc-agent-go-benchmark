#!/usr/bin/env bash
set -Eeuo pipefail

root=/data/validation/results/cleanroom-native-noloopwarn-timeout4h-r3-20260731
plan="$root/plan.json"
validator="$root/v12-validate.py"
retry_helper="$root/v11-endpoint-retry.py"
native_binary=/data/validation/bin/trpc-agent-go-impl-cleanroom-a702c2f
preflight_binary=/data/validation/bin/tag-swebench-offline-preflight-a702c2f
cases=/data/validation/trpc-agent-go-benchmark/swebench/data/generated/cases.jsonl
model_config=/data/validation/trpc-agent-go-benchmark/swebench/config/models/glm-5.2.local.yaml
environment_config=/data/validation/trpc-agent-go-benchmark/swebench/config/environments/swebench-testbed.yaml
offline_assets=/data/validation/offline-assets/tag-swebench-v1
experiment_id=cleanroom-native-noloopwarn-timeout4h-r3-20260731
framework_revision=358d376784889fd539a764190e1970ee4f4fc5f6

expected_plan_sha=${V12_PLAN_SHA256:?V12_PLAN_SHA256 is required}
expected_controller_sha=${V12_CONTROLLER_SHA256:?V12_CONTROLLER_SHA256 is required}
expected_validator_sha=${V12_VALIDATOR_SHA256:?V12_VALIDATOR_SHA256 is required}
expected_retry_helper_sha=${V12_RETRY_HELPER_SHA256:?V12_RETRY_HELPER_SHA256 is required}

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

assert_start_resources() {
  local available_kb free_kb
  available_kb=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
  free_kb=$(df -Pk /data | awk 'NR==2 {print $4}')
  if (( available_kb < 16 * 1024 * 1024 )); then
    log "resource_gate_failed mem_available_kb=$available_kb"
    exit 5
  fi
  if (( free_kb < 100 * 1024 * 1024 )); then
    log "resource_gate_failed data_free_kb=$free_kb"
    exit 5
  fi
  if pgrep -f '^/data/validation/bin/trpc-agent-go-impl-' >/dev/null; then
    log "runner_gate_failed another_tag_runner_is_active"
    pgrep -af '^/data/validation/bin/trpc-agent-go-impl-' || true
    exit 5
  fi
  if pgrep -f '^/data/validation/bin/tag-swebench-offline-preflight-' >/dev/null; then
    log "preflight_gate_failed another_preflight_is_active"
    exit 5
  fi
  if pgrep -f 'go run ./evaluator verify|/evaluator verify|swebench.*run_evaluation' >/dev/null; then
    log "evaluator_gate_failed evaluator_is_active"
    exit 5
  fi
  if docker ps --format '{{.Names}}' | grep -Eq '^tag-swe-|(^|-)eval($|-)|sweb\.eval'; then
    log "container_gate_failed active_case_or_eval_containers"
    docker ps --format '{{.Names}} {{.Status}}' \
      | grep -E '^tag-swe-|(^|-)eval($|-)|sweb\.eval' || true
    exit 5
  fi
  log "resource_gate_passed mem_available_kb=$available_kb data_free_kb=$free_kb"
}

wait_for_case_containers() {
  local _
  for _ in $(seq 1 60); do
    if ! docker ps --format '{{.Names}}' | grep -q '^tag-swe-'; then
      return
    fi
    sleep 5
  done
  log "residual_case_containers"
  docker ps --format '{{.Names}} {{.Status}}' | grep '^tag-swe-' || true
  exit 4
}

run_preflight() {
  local preflight="$root/preflight"
  if [[ -e "$preflight" ]]; then
    log "refusing_existing_preflight_dir path=$preflight"
    exit 6
  fi
  assert_start_resources
  mkdir -p "$preflight"
  printf '%s\n' "$$" >"$preflight/controller.pid"
  date --iso-8601=seconds >"$preflight/started-at.txt"
  log "preflight_start cases=500 workers=4"
  set +e
  /usr/bin/time -v "$preflight_binary" \
    --cases "$cases" \
    --environment-config "$environment_config" \
    --offline-assets-dir "$offline_assets" \
    --workers 4 \
    --command-timeout 15s \
    --case-timeout 10m \
    >"$preflight/runner.log" 2>"$preflight/time.log"
  local status=$?
  set -e
  printf '%s\n' "$status" >"$preflight/runner-exit.status"
  date --iso-8601=seconds >"$preflight/finished-at.txt"
  if [[ "$status" -ne 0 ]]; then
    log "preflight_stopped exit=$status"
    exit "$status"
  fi
  local passed failed unique summary_count
  passed=$(grep -c 'status=Passed' "$preflight/runner.log" || true)
  failed=$(grep -c 'status=Failed' "$preflight/runner.log" || true)
  unique=$(sed -n 's/.*instance=\([^ ]*\) status=Passed.*/\1/p' "$preflight/runner.log" \
    | sort -u | wc -l | tr -d ' ')
  summary_count=$(grep -Ec '^preflight cases=500 failed=0 duration_ms=[0-9]+$' \
    "$preflight/runner.log" || true)
  if [[ "$passed" -ne 500 || "$unique" -ne 500 || "$failed" -ne 0 \
    || "$summary_count" -ne 1 ]]; then
    log "preflight_gate_failed passed=$passed unique=$unique failed=$failed summary_count=$summary_count"
    exit 4
  fi
  if ! grep -q 'Exit status: 0' "$preflight/time.log"; then
    log "preflight_gate_failed time_exit_not_zero"
    exit 4
  fi
  wait_for_case_containers
  date --iso-8601=seconds >"$preflight/validated-at.txt"
  log "preflight_gate_passed cases=500 failed=0"
}

run_retry_attempt() {
  local parent_run_id=$1 run_dir=$2 instance_id=$3 attempt=$4
  local retry_id retry_dir list status kind
  retry_id="${parent_run_id}-endpoint-retry-${instance_id//[^a-zA-Z0-9_.-]/_}-a${attempt}"
  retry_dir="$run_dir/infrastructure-retries/$instance_id/attempt-$attempt"
  list="$retry_dir/case-list.txt"
  if [[ -e "$retry_dir" ]]; then
    log "refusing_existing_retry_dir path=$retry_dir"
    exit 6
  fi
  assert_start_resources
  mkdir -p "$retry_dir"
  printf '%s\n' "$instance_id" >"$list"
  sha256sum "$list" >"$retry_dir/case-list.sha256"
  date --iso-8601=seconds >"$retry_dir/started-at.txt"
  log "endpoint_retry_start parent_run_id=$parent_run_id instance=$instance_id attempt=$attempt"
  set +e
  (
    cd /data/validation/trpc-agent-go-benchmark/swebench
    /usr/bin/time -v "$native_binary" \
      --run-id "$retry_id" \
      --cases "$cases" \
      --case-list "$list" \
      --model-config "$model_config" \
      --environment-config "$environment_config" \
      --offline-assets-dir "$offline_assets" \
      --output "$retry_dir/raw/tag" \
      --agent-workers 15 \
      --command-timeout 1m \
      --case-timeout 4h \
      --observation-codec xml \
      --billing-tag "$retry_id" \
      --experiment-id "$experiment_id" \
      --framework-revision "$framework_revision" \
      --code-search=false \
      --workspace-preload=false \
      --workspace-representation=current-fixed \
      --tool-loop-warning=false \
      >"$retry_dir/runner.log" 2>"$retry_dir/time.txt"
  )
  status=$?
  set -e
  wait_for_case_containers
  printf '%s\n' "$status" >"$retry_dir/runner-exit.status"
  date --iso-8601=seconds >"$retry_dir/finished-at.txt"
  if [[ "$status" -ne 0 ]]; then
    log "endpoint_retry_runner_stopped parent_run_id=$parent_run_id instance=$instance_id attempt=$attempt exit=$status"
    exit "$status"
  fi
  python3 "$retry_helper" inspect "$retry_dir/raw/tag" "$instance_id" \
    >"$retry_dir/terminal-inspection.json"
  kind=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["kind"])' \
    "$retry_dir/terminal-inspection.json")
  if [[ "$kind" == single_attempt_endpoint_timeout ]]; then
    log "endpoint_retry_repeat_timeout parent_run_id=$parent_run_id instance=$instance_id attempt=$attempt"
    return 75
  fi
  if [[ "$kind" == other_infrastructure_failure ]]; then
    log "endpoint_retry_other_infrastructure parent_run_id=$parent_run_id instance=$instance_id attempt=$attempt"
    return 76
  fi
  python3 "$retry_helper" promote \
    "$run_dir/raw/tag" "$retry_dir/raw/tag" "$run_dir" "$instance_id" "$attempt"
  log "endpoint_retry_promoted parent_run_id=$parent_run_id instance=$instance_id attempt=$attempt"
  return 0
}

recover_endpoint_timeouts() {
  local run_id=$1 run_dir="$root/$1" output="$root/$1/raw/tag"
  local scan_path="$root/$1/infrastructure-retries/qualifying-endpoint-timeouts.json"
  mkdir -p "$(dirname "$scan_path")"
  python3 "$retry_helper" scan "$output" >"$scan_path"
  local ids=()
  mapfile -t ids < <(
    python3 -c '
import json
import sys

items = json.load(open(sys.argv[1], encoding="utf-8"))
if not isinstance(items, list):
    raise SystemExit("endpoint scan is not a list")
ids = [item.get("instance_id") for item in items]
if any(not isinstance(instance_id, str) or not instance_id for instance_id in ids):
    raise SystemExit("endpoint scan contains an empty instance id")
sys.stdout.write("".join(instance_id + "\n" for instance_id in ids))
' "$scan_path"
  )
  if [[ "${#ids[@]}" -eq 0 ]]; then
    log "endpoint_retry_none run_id=$run_id"
    return
  fi
  log "endpoint_retry_membership_frozen run_id=$run_id count=${#ids[@]} instances=$(IFS=,; echo "${ids[*]}")"
  local instance attempt status promoted
  for instance in "${ids[@]}"; do
    promoted=false
    for attempt in 1 2 3; do
      set +e
      run_retry_attempt "$run_id" "$run_dir" "$instance" "$attempt"
      status=$?
      set -e
      if [[ "$status" -eq 0 ]]; then
        promoted=true
        break
      fi
      if [[ "$status" -eq 75 ]]; then
        continue
      fi
      exit "$status"
    done
    if [[ "$promoted" != true ]]; then
      log "endpoint_retry_exhausted run_id=$run_id instance=$instance attempts=3"
      exit 8
    fi
  done
  python3 "$retry_helper" refresh-manifest "$output"
  date --iso-8601=seconds >"$run_dir/infrastructure-retries/recovery-complete-at.txt"
  log "endpoint_retry_recovery_complete run_id=$run_id count=${#ids[@]}"
}

validate_run() {
  local run_id=$1 run_dir="$root/$1"
  set +e
  python3 "$validator" \
    "$run_dir/raw/tag" "$cases" "$run_id" "$run_dir/generation-audit.json" \
    >"$run_dir/validation.log" 2>&1
  local status=$?
  set -e
  if [[ "$status" -ne 0 ]]; then
    log "generation_gate_stopped run_id=$run_id validator_exit=$status"
    exit "$status"
  fi
  date --iso-8601=seconds >"$run_dir/completed-at.txt"
  log "generation_gate_passed run_id=$run_id"
}

run_arm() {
  local run_id=$1 run_dir="$root/$1"
  if [[ -e "$run_dir" ]]; then
    log "refusing_existing_run_dir path=$run_dir"
    exit 6
  fi
  assert_start_resources
  mkdir -p "$run_dir"
  printf '%s\n' "$$" >"$run_dir/controller.pid"
  sar -u -r -d 1 1 >"$run_dir/sar-preflight.log" 2>&1
  if [[ ! -s "$run_dir/sar-preflight.log" ]]; then
    log "sar_preflight_failed run_id=$run_id"
    exit 5
  fi
  sar -u -r -d 60 >"$run_dir/sar.log" 2>&1 &
  local sar_pid=$!
  printf '%s\n' "$sar_pid" >"$run_dir/sar.pid"
  cleanup_sar() {
    if kill -0 "$sar_pid" 2>/dev/null; then
      kill "$sar_pid" 2>/dev/null || true
      wait "$sar_pid" 2>/dev/null || true
    fi
  }
  trap cleanup_sar EXIT INT TERM
  date --iso-8601=seconds >"$run_dir/started-at.txt"
  log "generation_start arm=native_no_loop_warning run_id=$run_id workers=15 case_timeout=4h tool_loop_warning=false"
  set +e
  (
    cd /data/validation/trpc-agent-go-benchmark/swebench
    /usr/bin/time -v "$native_binary" \
      --run-id "$run_id" \
      --cases "$cases" \
      --model-config "$model_config" \
      --environment-config "$environment_config" \
      --offline-assets-dir "$offline_assets" \
      --output "$run_dir/raw/tag" \
      --agent-workers 15 \
      --command-timeout 1m \
      --case-timeout 4h \
      --observation-codec xml \
      --billing-tag "$run_id" \
      --experiment-id "$experiment_id" \
      --framework-revision "$framework_revision" \
      --code-search=false \
      --workspace-preload=false \
      --workspace-representation=current-fixed \
      --tool-loop-warning=false \
      >"$run_dir/runner.log" 2>"$run_dir/time.txt"
  )
  local status=$?
  set -e
  cleanup_sar
  trap - EXIT INT TERM
  printf '%s\n' "$status" >"$run_dir/runner-exit.status"
  date --iso-8601=seconds >"$run_dir/finished-at.txt"
  wait_for_case_containers
  if [[ "$status" -ne 0 ]]; then
    log "generation_stopped run_id=$run_id runner_exit=$status"
    exit "$status"
  fi
  recover_endpoint_timeouts "$run_id"
  validate_run "$run_id"
}

mkdir -p "$root"
if [[ -e "$root/controller-started-at.txt" ]]; then
  log "refusing_existing_controller_start path=$root/controller-started-at.txt"
  exit 6
fi

verify_sha256 "$expected_plan_sha" "$plan"
verify_sha256 "$expected_controller_sha" "$0"
verify_sha256 "$expected_validator_sha" "$validator"
verify_sha256 "$expected_retry_helper_sha" "$retry_helper"
verify_sha256 839a4f327704554697592335b1fa4a3fd9fac32ec9f8de19923a5e870806e4b8 \
  "$native_binary"
verify_sha256 33ab173c379b668386ef4409a9534641ee232a79d80dd03ea7ee54bea7fbf863 \
  "$preflight_binary"
verify_sha256 4b2a050a82d356963320cbfa8e2efdf6a133af8863f31b291a973ab4dd349d07 \
  "$cases"
verify_sha256 fbfdf25e9fced3ecc51d244ccbb652c078b02ac640abbdbcca53ddbaa7af27de \
  "$model_config"
verify_sha256 3cfa72f92f4010d242e6adb9bb507ccdf9db261ba5726163b5adecd509c140f0 \
  "$environment_config"

printf '%s\n' "$$" >"$root/controller.pid"
date --iso-8601=seconds >"$root/controller-started-at.txt"
log "controller_start plan_sha=$expected_plan_sha tool_loop_warning=false"

run_preflight
run_arm tag-cleanroom-noloopwarn-timeout4h-native-20260731-r1
run_arm tag-cleanroom-noloopwarn-timeout4h-native-20260731-r2
run_arm tag-cleanroom-noloopwarn-timeout4h-native-20260731-r3

date --iso-8601=seconds >"$root/all-generation-complete-at.txt"
log "all_generation_gates_passed harness_queue_managed_separately=1"
