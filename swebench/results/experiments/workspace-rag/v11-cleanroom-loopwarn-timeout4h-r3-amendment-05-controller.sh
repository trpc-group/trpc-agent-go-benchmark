#!/usr/bin/env bash
set -Eeuo pipefail

root=/data/validation/results/cleanroom-loopwarn-timeout4h-r3-20260729
base_recovery="$root/v11-timeout4h-controller-amendment-04.sh"
amendment_05="$root/amendment-05.json"
expected_amendment_04_sha=${V11_AMENDMENT04_SHA256:?V11_AMENDMENT04_SHA256 is required}
expected_amendment_05_sha=${V11_AMENDMENT05_SHA256:?V11_AMENDMENT05_SHA256 is required}
expected_controller_05_sha=${V11_CONTROLLER05_SHA256:?V11_CONTROLLER05_SHA256 is required}
authorized_at=${V11_RECOVERY_AUTHORIZED_AT:?V11_RECOVERY_AUTHORIZED_AT is required}

base_recovery_sha=ab8cbd216741475dfd20a4cd425b9054fa8cefd415318d49c22e9e44a4eedda0
actual_base_recovery_sha=$(sha256sum "$base_recovery" | awk '{print $1}')
if [[ "$actual_base_recovery_sha" != "$base_recovery_sha" ]]; then
  printf '%s base_recovery_sha_mismatch expected=%s actual=%s\n' \
    "$(date --iso-8601=seconds)" "$base_recovery_sha" "$actual_base_recovery_sha"
  exit 3
fi
actual_controller_05_sha=$(sha256sum "$0" | awk '{print $1}')
if [[ "$actual_controller_05_sha" != "$expected_controller_05_sha" ]]; then
  printf '%s controller_05_sha_mismatch expected=%s actual=%s\n' \
    "$(date --iso-8601=seconds)" "$expected_controller_05_sha" "$actual_controller_05_sha"
  exit 3
fi

# Amendment-04's top-level execution starts at line 250. Reuse only its
# verified declarations and functions, mechanically splitting line 125 so
# Bash nounset never expands output from an as-yet-unassigned local run_dir.
source <(
  awk '
NR == 125 {
  print "  local run_dir=\"$root/$run_id\""
  print "  local output=\"$run_dir/raw/tag\""
  next
}
NR <= 248 { print }
' "$base_recovery"
)

verify_sha256 "$expected_amendment_05_sha" "$amendment_05"

if pgrep -f '^/data/validation/bin/trpc-agent-go-impl-' >/dev/null; then
  log "resume_gate_failed tag_runner_active"
  exit 5
fi
if docker ps --format '{{.Names}}' | grep -q '^tag-swe-'; then
  log "resume_gate_failed active_case_containers"
  exit 5
fi
old_controller_pid=$(tr -d '[:space:]' <"$root/controller.pid")
if [[ "$old_controller_pid" != 336450 ]] || kill -0 "$old_controller_pid" 2>/dev/null; then
  log "resume_gate_failed unexpected_controller_pid=$old_controller_pid"
  exit 5
fi
test "$(tr -d '[:space:]' <"$root/controller-amendment-04.pid")" = 336450
grep -q 'line 125: run_dir: unbound variable' "$root/controller-amendment-04-time.txt"
grep -q 'Exit status: 1' "$root/controller-amendment-04-time.txt"
test ! -e "$root/tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r1/generation-audit.json"
test ! -e "$root/tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r1/completed-at.txt"
test ! -e "$root/tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r2"

cp -a "$root/controller.pid" "$root/controller-amendment-04-stale.pid"
printf '%s\n' "$$" >"$root/controller-amendment-05.pid"
printf '%s\n' "$$" >"$root/controller.pid"
date --iso-8601=seconds >"$root/controller-amendment-05-started-at.txt"
log "amendment_05_controller_start stale_controller_pid=$old_controller_pid amendment_04_exit=1 model_calls=0"

resume_adapt_r1_gate
date --iso-8601=seconds >"$root/controller-amendment-05-recovery-complete-at.txt"
log "amendment_05_recovery_gate_passed run_id=tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r1"

run_arm rag_adapt tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r2 6 true ast-structured
run_arm native tag-cleanroom-loopwarn-timeout4h-native-20260729-r2 15 false current-fixed
run_arm native tag-cleanroom-loopwarn-timeout4h-native-20260729-r3 15 false current-fixed
run_arm rag_adapt tag-cleanroom-loopwarn-timeout4h-adapt-20260729-r3 6 true ast-structured

date --iso-8601=seconds >"$root/all-generation-complete-at.txt"
log "all_generation_gates_passed harness_queue_managed_separately=1 recovery=amendment-05"
