#!/usr/bin/env bash
set -Eeuo pipefail

root=/data/validation/results/cleanroom-loopwarn-timeout4h-r3-20260729
base_controller="$root/v11-timeout4h-controller-amendment-03.sh"
amendment_04="$root/amendment-04.json"
expected_amendment_04_sha=${V11_AMENDMENT04_SHA256:?V11_AMENDMENT04_SHA256 is required}
authorized_at=${V11_RECOVERY_AUTHORIZED_AT:?V11_RECOVERY_AUTHORIZED_AT is required}

base_controller_sha=0137b007870103b4af0c9840d41d9933188f6ba7733b750bf4970f0a9848f769
actual_base_controller_sha=$(sha256sum "$base_controller" | awk '{print $1}')
if [[ "$actual_base_controller_sha" != "$base_controller_sha" ]]; then
  printf '%s base_controller_sha_mismatch expected=%s actual=%s\n' \
    "$(date --iso-8601=seconds)" "$base_controller_sha" "$actual_base_controller_sha"
  exit 3
fi

# Reuse only the verified variable and function definitions. Line 281 begins
# amendment-03's top-level execution, which must never be replayed.
source <(sed -n '1,280p' "$base_controller")

amendment_04="$root/amendment-04.json"
verify_sha256 "$expected_amendment_04_sha" "$amendment_04"
verify_sha256 3635981520871e6c0c9d3f51bd351eae6d86a0e761875056ffa341805796b620 \
  "$root/v11-cleanroom-loopwarn-timeout4h-r3-amendment-01.json"
verify_sha256 9f5c1c47e511fe8ccf308d5214bd4c47556884fbf31158abfb8a86f06ba3b412 \
  "$root/v11-cleanroom-loopwarn-timeout4h-r3-amendment-02.json"
verify_sha256 62ae3012d2896785e3c7f89489748e73ea48e78133bce542d449dfb81a1b7167 \
  "$root/amendment-03.json"
verify_sha256 4296c53120124962b0a358f90203605f0e4445a1fbb22040f104a2aa6d399972 \
  "$validator"
verify_sha256 4fc02177ce6e96fc93cfa738dee0461c7705ac5306e9b097eedd123c78df415b \
  "$retry_helper"
verify_sha256 23deef6cb328a5c98557cd6c99496d03aada1d4cb3ba97e4b71eb079694f3f0c \
  "$plan"
verify_sha256 839a4f327704554697592335b1fa4a3fd9fac32ec9f8de19923a5e870806e4b8 \
  "$native_binary"
verify_sha256 5df758c43eebe7ae72b16a9408c17f140e831c8532ba9a689b76fc3d61a1439a \
  "$adapt_binary"
verify_sha256 4b2a050a82d356963320cbfa8e2efdf6a133af8863f31b291a973ab4dd349d07 \
  "$cases"
verify_sha256 fbfdf25e9fced3ecc51d244ccbb652c078b02ac640abbdbcca53ddbaa7af27de \
  "$model_config"
verify_sha256 2452624a6a38fbd6f414a70b0c66e48653204662fe1b96c9026350bcc6d79bd1 \
  "$embedding_config"
verify_sha256 3cfa72f92f4010d242e6adb9bb507ccdf9db261ba5726163b5adecd509c140f0 \
  "$environment_config"

# Replace amendment-03's empty-list-sensitive implementation. For an empty
# JSON array, sys.stdout.write emits zero bytes, so mapfile creates zero items.
recover_endpoint_timeouts() {
  local arm=$1 run_id=$2 run_dir="$root/$2" output="$root/$2/raw/tag"
  local scan_path="$root/$2/infrastructure-retries/qualifying-endpoint-timeouts.json"
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
      run_retry_attempt "$arm" "$run_id" "$run_dir" "$instance" "$attempt"
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

canonical_tree_digest() {
  local output=$1
  find "$output" -mindepth 2 -maxdepth 2 -type f \
    \( -name '*.tag.json' -o -name '*.responses.json' \) -print0 \
    | LC_ALL=C sort -z \
    | xargs -0 sha256sum \
    | sha256sum \
    | awk '{print $1}'
}

canonical_metadata_digest() {
  local output=$1
  sha256sum \
    "$output/preds.json" \
    "$output/tag-runner-progress.json" \
    "$output/tag-runner-manifest.json" \
    | sha256sum \
    | awk '{print $1}'
}

resume_adapt_r1_gate() {
  local run_id=tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r1
  local run_dir="$root/$run_id" output="$run_dir/raw/tag"
  local scan_path="$run_dir/infrastructure-retries/qualifying-endpoint-timeouts.json"
  local failed_attempt="$run_dir/infrastructure-retries/attempt-1"
  local started_at pre_tree post_tree pre_metadata post_metadata failed_attempt_digest

  test -d "$run_dir"
  test ! -e "$run_dir/completed-at.txt"
  test ! -e "$run_dir/generation-audit.json"
  test ! -e "$run_dir/validation.log"
  test -s "$output/tag-runner-manifest.json"
  test -s "$output/preds.json"
  test -s "$output/tag-runner-progress.json"
  test "$(find "$output" -mindepth 2 -maxdepth 2 -name '*.tag.json' | wc -l | tr -d ' ')" -eq 500
  test "$(find "$output" -mindepth 2 -maxdepth 2 -name '*.responses.json' | wc -l | tr -d ' ')" -eq 500

  python3 -c 'import json,sys; assert json.load(open(sys.argv[1], encoding="utf-8")) == []' "$scan_path"
  test -f "$failed_attempt/case-list.txt"
  test "$(wc -c <"$failed_attempt/case-list.txt" | tr -d ' ')" -eq 1
  test "$(tr -d '[:space:]' <"$failed_attempt/runner-exit.status")" = 1
  grep -q 'case list is empty' "$failed_attempt/time.txt"
  test ! -e "$failed_attempt/raw/tag/preds.json"
  failed_attempt_digest=$(
    find "$failed_attempt" -maxdepth 2 -type f -print0 \
      | LC_ALL=C sort -z \
      | xargs -0 sha256sum \
      | sha256sum \
      | awk '{print $1}'
  )

  assert_start_resources
  pre_tree=$(canonical_tree_digest "$output")
  pre_metadata=$(canonical_metadata_digest "$output")
  started_at=$(date --iso-8601=seconds)
  printf '%s\n' "$started_at" >"$run_dir/control-plane-recovery-started-at.txt"
  log "adapt_r1_control_plane_recovery_start run_id=$run_id endpoint_scan=empty"

  # Re-scan without mutating canonical artifacts. An empty result is mandatory.
  local scan_json
  scan_json=$(python3 "$retry_helper" scan "$output")
  if [[ "$scan_json" != '[]' ]]; then
    log "adapt_r1_control_plane_recovery_stopped unexpected_endpoint_scan=$scan_json"
    exit 9
  fi
  log "endpoint_retry_none run_id=$run_id"

  set +e
  python3 "$validator" \
    "$output" "$cases" "$run_id" rag_adapt "$run_dir/generation-audit.json" \
    >"$run_dir/validation.log" 2>&1
  local status=$?
  set -e
  if [[ "$status" -ne 0 ]]; then
    log "generation_gate_stopped arm=rag_adapt run_id=$run_id validator_exit=$status"
    exit "$status"
  fi

  post_tree=$(canonical_tree_digest "$output")
  post_metadata=$(canonical_metadata_digest "$output")
  if [[ "$pre_tree" != "$post_tree" || "$pre_metadata" != "$post_metadata" ]]; then
    log "adapt_r1_control_plane_recovery_stopped canonical_digest_changed"
    exit 10
  fi

  python3 - \
    "$run_dir/control-plane-recovery-incident.json" \
    "$authorized_at" "$started_at" \
    "$pre_tree" "$pre_metadata" "$failed_attempt_digest" \
    "$(sha256sum "$0" | awk '{print $1}')" \
    "$expected_amendment_04_sha" <<'PY'
import json
import pathlib
import sys
from datetime import datetime

(
    output_path,
    authorized_at,
    started_at,
    canonical_tree_sha256,
    canonical_metadata_sha256,
    failed_attempt_tree_sha256,
    controller_sha256,
    amendment_sha256,
) = sys.argv[1:]
value = {
    "schema_version": 1,
    "incident_id": "v11-empty-endpoint-scan-control-plane-failure-adapt-r1",
    "authorized_at": authorized_at,
    "recovery_started_at": started_at,
    "recovery_completed_at": datetime.now().astimezone().isoformat(timespec="seconds"),
    "run_id": "tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r1",
    "cause": "An empty endpoint-timeout scan emitted one newline; mapfile interpreted it as one empty instance id.",
    "failed_attempt": {
        "instance_id": "",
        "runner_exit": 1,
        "model_calls": 0,
        "canonical_artifacts_changed": False,
        "tree_sha256": failed_attempt_tree_sha256,
    },
    "endpoint_timeout_scan": [],
    "canonical_attestation": {
        "tag_count": 500,
        "responses_count": 500,
        "tree_sha256_before_and_after": canonical_tree_sha256,
        "metadata_sha256_before_and_after": canonical_metadata_sha256,
    },
    "recovery": {
        "quality_adaptive": False,
        "adapt_r1_harness_seen": False,
        "native_r1_harness_metrics_already_existed": True,
        "controller_sha256": controller_sha256,
        "amendment_04_sha256": amendment_sha256,
    },
}
path = pathlib.Path(output_path)
temp = path.with_name(path.name + ".tmp")
temp.write_text(json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
temp.replace(path)
PY

  date --iso-8601=seconds >"$run_dir/control-plane-recovery-complete-at.txt"
  date --iso-8601=seconds >"$run_dir/completed-at.txt"
  log "generation_gate_passed arm=rag_adapt run_id=$run_id recovery=amendment-04"
}

if pgrep -f '^/data/validation/bin/trpc-agent-go-impl-' >/dev/null; then
  log "resume_gate_failed tag_runner_active"
  exit 5
fi
if docker ps --format '{{.Names}}' | grep -q '^tag-swe-'; then
  log "resume_gate_failed active_case_containers"
  exit 5
fi
old_controller_pid=$(tr -d '[:space:]' <"$root/controller.pid")
if [[ "$old_controller_pid" != 1831936 ]] || kill -0 "$old_controller_pid" 2>/dev/null; then
  log "resume_gate_failed unexpected_controller_pid=$old_controller_pid"
  exit 5
fi

cp -a "$root/controller.pid" "$root/controller-amendment-03-stale.pid"
printf '%s\n' "$$" >"$root/controller-amendment-04.pid"
printf '%s\n' "$$" >"$root/controller.pid"
date --iso-8601=seconds >"$root/controller-amendment-04-started-at.txt"
log "amendment_04_controller_start stale_controller_pid=$old_controller_pid"

resume_adapt_r1_gate
run_arm rag_adapt tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r2 6 true ast-structured
run_arm native tag-cleanroom-loopwarn-timeout4h-native-20260729-r2 15 false current-fixed
run_arm native tag-cleanroom-loopwarn-timeout4h-native-20260729-r3 15 false current-fixed
run_arm rag_adapt tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r3 6 true ast-structured

date --iso-8601=seconds >"$root/all-generation-complete-at.txt"
log "all_generation_gates_passed harness_queue_managed_separately=1 recovery=amendment-04"
