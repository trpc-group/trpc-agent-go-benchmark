//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

var fullGitCommit = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

const cleanRoomPolicySchema = "swebench-clean-room-v1"

// CleanRoomPolicySHA256 returns the portable identity of the Git sanitation
// policy. Allowlist entries are exact workspace-relative ignored paths; a
// trailing slash allows that directory subtree. With an empty allowlist every
// regular ignored file is removed.
func CleanRoomPolicySHA256(ignoredAllowlist []string) (string, error) {
	entries, err := validateIgnoredAllowlist(ignoredAllowlist)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = fmt.Fprintln(h, cleanRoomPolicySchema)
	for _, entry := range entries {
		_, _ = fmt.Fprintln(h, entry)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateIgnoredAllowlist(values []string) ([]string, error) {
	entries := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry == "" || entry != strings.TrimSpace(entry) || strings.IndexFunc(entry, func(r rune) bool {
			return r < 0x20 || r == 0x7f
		}) >= 0 {
			return nil, fmt.Errorf("invalid clean-room ignored allowlist path %q", entry)
		}
		directory := strings.HasSuffix(entry, "/")
		withoutSlash := strings.TrimSuffix(entry, "/")
		if strings.HasPrefix(withoutSlash, "/") || withoutSlash == "." || withoutSlash == ".." ||
			path.Clean(withoutSlash) != withoutSlash || strings.HasPrefix(withoutSlash, "../") ||
			strings.Contains(withoutSlash, "\\") {
			return nil, fmt.Errorf("invalid clean-room ignored allowlist path %q", entry)
		}
		canonical := withoutSlash
		if directory {
			canonical += "/"
		}
		if _, ok := seen[canonical]; ok {
			return nil, fmt.Errorf("duplicate clean-room ignored allowlist path %q", canonical)
		}
		seen[canonical] = struct{}{}
	}
	sort.Strings(entries)
	return entries, nil
}

const cleanGitRepositoryScript = `set -euo pipefail
expected_base=$1
repository_root=$2
shift 2
cd "$repository_root"

actual_head=$(git rev-parse --verify HEAD)
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "official testbed has tracked changes before clean-room sanitation" >&2
  exit 1
fi
if [[ "$actual_head" != "$expected_base" ]]; then
  revision_line=$(git rev-list --parents -n 1 "$actual_head")
  read -r -a revision_parts <<<"$revision_line"
  subject=$(git show -s --format=%s "$actual_head")
  if [[ "${#revision_parts[@]}" -ne 2 || "${revision_parts[1]}" != "$expected_base" \
    || "$subject" != "SWE-bench" ]]; then
    echo "testbed HEAD $actual_head is not expected base $expected_base or its official setup commit" >&2
    exit 1
  fi
  # Official images wrap the task base in a synthetic setup commit. Never
  # expose that commit's tree to the model: reset to the dataset base before
  # recursively rebuilding the reachable Git history.
  git reset -q --hard "$expected_base"
fi
actual_base=$(git rev-parse --verify HEAD)
if [[ "$actual_base" != "$expected_base" ]]; then
  echo "testbed did not reset to expected base $expected_base" >&2
  exit 1
fi

submodule_status_file=$(mktemp)
if ! git submodule status --recursive >"$submodule_status_file"; then
  echo "cannot inspect recursive submodule status" >&2
  exit 1
fi
while IFS= read -r status; do
  [[ -z "$status" ]] && continue
  if [[ "${status:0:1}" != " " ]]; then
    echo "submodule is uninitialized, conflicted, or not at its recorded gitlink: $status" >&2
    exit 1
  fi
done <"$submodule_status_file"
rm -f "$submodule_status_file"

raw_submodules=$(mktemp)
if ! git submodule foreach --quiet --recursive 'set -e; printf "%s\n" "$displaypath"' >"$raw_submodules"; then
  echo "cannot enumerate initialized recursive submodules" >&2
  exit 1
fi
sorted_submodules=$(mktemp)
if ! awk -F/ '{print NF "\t" $0}' "$raw_submodules" \
  | sort -k1,1nr -k2,2 \
  | cut -f2- >"$sorted_submodules"; then
  echo "cannot order recursive submodules" >&2
  exit 1
fi
rm -f "$raw_submodules"
submodules=()
while IFS= read -r submodule; do
  [[ -z "$submodule" ]] && continue
  submodules+=("$submodule")
done <"$sorted_submodules"
rm -f "$sorted_submodules"

is_allowlisted() {
  local candidate=$1
  shift
  local allowed
  for allowed in "$@"; do
    if [[ "$allowed" == */ ]]; then
      [[ "$candidate" == "$allowed"* ]] && return 0
    elif [[ "$candidate" == "$allowed" ]]; then
      return 0
    fi
  done
  return 1
}

remove_unapproved_ignored() {
  local repository=$1
  local prefix=$2
  shift 2
  local ignored candidate scan
  scan=$(mktemp)
  if ! git -C "$repository" ls-files --others --ignored --exclude-standard -z >"$scan"; then
    echo "cannot enumerate ignored files: $repository" >&2
    exit 1
  fi
  while IFS= read -r -d '' ignored; do
    candidate="$prefix$ignored"
    if [[ -L "$repository/$ignored" || ! -f "$repository/$ignored" ]]; then
      echo "ignored path is not a regular file: $candidate" >&2
      exit 1
    fi
    if ! is_allowlisted "$candidate" "$@"; then
      rm -f -- "$repository/$ignored"
    fi
  done <"$scan"
  rm -f "$scan"

  scan=$(mktemp)
  if ! git -C "$repository" ls-files --others --ignored --exclude-standard -z >"$scan"; then
    echo "cannot verify ignored files: $repository" >&2
    exit 1
  fi
  while IFS= read -r -d '' ignored; do
    candidate="$prefix$ignored"
    if [[ -L "$repository/$ignored" || ! -f "$repository/$ignored" ]] \
      || ! is_allowlisted "$candidate" "$@"; then
      echo "unapproved ignored path survived sanitation: $candidate" >&2
      exit 1
    fi
  done <"$scan"
  rm -f "$scan"
}

clean_repository() {
  local repository=$1
  local suffix=$2
  local prefix=$3
  shift 3
  local ancestor_count temporary unreachable actual fsck_output

  if ! git -C "$repository" diff --quiet || ! git -C "$repository" diff --cached --quiet; then
    echo "repository has tracked changes before clean-room sanitation: $repository" >&2
    exit 1
  fi
  remove_unapproved_ignored "$repository" "$prefix" "$@"
  git -C "$repository" clean -fd
  ancestor_count=$(git -C "$repository" rev-list HEAD --count)
  temporary="/tmp/swebench-clean-history-$suffix-$$"
  rm -rf "$temporary"
  git -C "$repository" update-ref refs/heads/swebench-clean-base HEAD
  git clone -q --no-local --no-tags --no-checkout --single-branch \
    --branch swebench-clean-base "$repository" "$temporary"
  actual=$(git -C "$temporary" rev-parse --verify HEAD)
  rm -rf "$repository/.git"
  mv "$temporary/.git" "$repository/.git"
  rm -rf "$temporary"
  git -C "$repository" remote remove origin
  # Some Git versions leave a dangling refs/remotes/origin/HEAD symbolic ref
  # after removing the clone remote. It is not reachable via for-each-ref but
  # makes fsck fail, so remove the remote-ref namespace explicitly.
  rm -rf "$repository/.git/refs/remotes"
  git -C "$repository" reset -q --mixed HEAD
  git -C "$repository" reflog expire --expire=now --all
  rm -rf "$repository/.git/logs"
  git -C "$repository" repack -adq
  git -C "$repository" prune --expire=now
  if [[ "$(git -C "$repository" rev-parse --verify HEAD)" != "$actual" ]]; then
    echo "repository HEAD changed during clean-room sanitation: $repository" >&2
    exit 1
  fi
  if [[ -n "$(git -C "$repository" status --porcelain)" ]]; then
    echo "repository changed during clean-room sanitation: $repository" >&2
    exit 1
  fi
  if [[ "$(git -C "$repository" rev-list --all --count)" -ne "$ancestor_count" ]]; then
    echo "clean-room history does not match base ancestry: $repository" >&2
    exit 1
  fi
  if [[ "$(git -C "$repository" for-each-ref | wc -l)" -ne 1 ]] \
    || [[ -n "$(git -C "$repository" rev-list --all --not HEAD)" ]]; then
    echo "clean-room repository contains unexpected refs: $repository" >&2
    exit 1
  fi
  if [[ -n "$(git -C "$repository" remote)" || -n "$(git -C "$repository" tag --list)" ]]; then
    echo "clean-room repository contains remotes or tags: $repository" >&2
    exit 1
  fi
  if [[ -f "$repository/.git/objects/info/alternates" || -e "$repository/.git/logs" ]]; then
    echo "clean-room repository contains alternates or reflogs: $repository" >&2
    exit 1
  fi
  fsck_output=$(mktemp)
  if ! git -C "$repository" fsck --unreachable --no-reflogs >"$fsck_output" 2>&1; then
    echo "clean-room repository failed Git object verification: $repository" >&2
    cat "$fsck_output" >&2
    exit 1
  fi
  unreachable=$(grep -E '^(unreachable|dangling) ' "$fsck_output" || true)
  rm -f "$fsck_output"
  if [[ -n "$unreachable" ]]; then
    echo "clean-room repository contains unreachable objects: $repository" >&2
    echo "$unreachable" >&2
    exit 1
  fi
}

index=0
for submodule in "${submodules[@]}"; do
  index=$((index + 1))
  clean_repository "$submodule" "submodule-$index" "$submodule/" "$@"
done
clean_repository . root "" "$@"

if [[ "$(git rev-parse --verify HEAD)" != "$expected_base" ]]; then
  echo "testbed HEAD changed after clean-room sanitation" >&2
  exit 1
fi
if ! git submodule foreach --quiet --recursive 'set -e
test -z "$(git status --porcelain)"
test "$(git for-each-ref --format="%(refname)" | wc -l)" -eq 1
test -z "$(git remote)"
test -z "$(git tag --list)"
test ! -f .git/objects/info/alternates
test ! -e .git/logs
git rev-parse --verify HEAD >/dev/null
git diff --quiet
'; then
  echo "recursive submodule verification failed after sanitation" >&2
  exit 1
fi`

const verifyCleanGitRepositoryScript = `set -euo pipefail
expected_base=$1
repository_root=$2
shift 2
cd "$repository_root"

is_allowlisted() {
  local candidate=$1
  shift
  local allowed
  for allowed in "$@"; do
    if [[ "$allowed" == */ ]]; then
      [[ "$candidate" == "$allowed"* ]] && return 0
    elif [[ "$candidate" == "$allowed" ]]; then
      return 0
    fi
  done
  return 1
}

verify_ignored() {
  local repository=$1
  local prefix=$2
  shift 2
  local ignored candidate scan
  scan=$(mktemp)
  if ! git -C "$repository" ls-files --others --ignored --exclude-standard -z >"$scan"; then
    echo "cannot enumerate ignored files during final verification: $repository" >&2
    exit 1
  fi
  while IFS= read -r -d '' ignored; do
    candidate="$prefix$ignored"
    if [[ -L "$repository/$ignored" || ! -f "$repository/$ignored" ]] \
      || ! is_allowlisted "$candidate" "$@"; then
      echo "unapproved ignored path after clean-room setup: $candidate" >&2
      exit 1
    fi
  done <"$scan"
  rm -f "$scan"
}

verify_repository() {
  local repository=$1
  local prefix=$2
  shift 2
  local unreachable gitlinks record metadata mode expected stage submodule actual fsck_output

  if [[ -n "$(git -C "$repository" status --porcelain)" ]]; then
    echo "repository is not clean after clean-room setup: $repository" >&2
    exit 1
  fi
  verify_ignored "$repository" "$prefix" "$@"
  if [[ "$(git -C "$repository" for-each-ref | wc -l)" -ne 1 ]] \
    || [[ -n "$(git -C "$repository" rev-list --all --not HEAD)" ]]; then
    echo "clean-room repository contains unexpected refs after setup: $repository" >&2
    exit 1
  fi
  if [[ -n "$(git -C "$repository" remote)" || -n "$(git -C "$repository" tag --list)" ]]; then
    echo "clean-room repository contains remotes or tags after setup: $repository" >&2
    exit 1
  fi
  if [[ -f "$repository/.git/objects/info/alternates" || -e "$repository/.git/logs" ]]; then
    echo "clean-room repository contains alternates or reflogs after setup: $repository" >&2
    exit 1
  fi
  fsck_output=$(mktemp)
  if ! git -C "$repository" fsck --unreachable --no-reflogs >"$fsck_output" 2>&1; then
    echo "clean-room repository failed Git object verification after setup: $repository" >&2
    cat "$fsck_output" >&2
    exit 1
  fi
  unreachable=$(grep -E '^(unreachable|dangling) ' "$fsck_output" || true)
  rm -f "$fsck_output"
  if [[ -n "$unreachable" ]]; then
    echo "clean-room repository contains unreachable objects after setup: $repository" >&2
    echo "$unreachable" >&2
    exit 1
  fi

  gitlinks=$(mktemp)
  if ! git -C "$repository" ls-files --stage -z >"$gitlinks"; then
    echo "cannot enumerate gitlinks during final verification: $repository" >&2
    exit 1
  fi
  while IFS= read -r -d '' record; do
    metadata=${record%%$'\t'*}
    submodule=${record#*$'\t'}
    read -r mode expected stage <<<"$metadata"
    [[ "$mode" != "160000" ]] && continue
    if [[ -z "$submodule" || "$submodule" == /* || "$submodule" == ".." \
      || "$submodule" == ../* || "$submodule" == */../* || "$submodule" == */.. ]]; then
      echo "unsafe gitlink path during final verification: $submodule" >&2
      exit 1
    fi
    if [[ -L "$repository/$submodule" || ! -d "$repository/$submodule" ]]; then
      echo "gitlink working tree is missing or unsafe after setup: $prefix$submodule" >&2
      exit 1
    fi
    if ! actual=$(git -C "$repository/$submodule" rev-parse --verify HEAD); then
      echo "gitlink is not an initialized repository after setup: $prefix$submodule" >&2
      exit 1
    fi
    if [[ "$actual" != "$expected" ]]; then
      echo "gitlink HEAD changed after clean-room setup: $prefix$submodule" >&2
      exit 1
    fi
    verify_repository "$repository/$submodule" "$prefix$submodule/" "$@"
  done <"$gitlinks"
  rm -f "$gitlinks"
}

if [[ "$(git rev-parse --verify HEAD)" != "$expected_base" ]]; then
  echo "testbed HEAD changed after clean-room setup" >&2
  exit 1
fi

verify_repository . "" "$@"`

func (f DockerFactory) sanitizeGitRepository(
	ctx context.Context,
	environment *dockerEnvironment,
	expectedBaseCommit string,
) error {
	allowlist, err := validateIgnoredAllowlist(f.GitIgnoredAllowlist)
	if err != nil {
		return err
	}
	args := []string{
		"exec", "-w", "/testbed", environment.name,
		"bash", "-c", cleanGitRepositoryScript, "swebench-clean-room", expectedBaseCommit, "/testbed",
	}
	args = append(args, allowlist...)
	out, err := environment.commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return fmt.Errorf("sanitize testbed Git history: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (f DockerFactory) verifyGitRepository(
	ctx context.Context,
	environment *dockerEnvironment,
	expectedBaseCommit string,
) error {
	allowlist, err := validateIgnoredAllowlist(f.GitIgnoredAllowlist)
	if err != nil {
		return err
	}
	args := []string{
		"exec", "-w", "/testbed", environment.name,
		"bash", "-c", verifyCleanGitRepositoryScript, "swebench-clean-room", expectedBaseCommit, "/testbed",
	}
	args = append(args, allowlist...)
	out, err := environment.commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return fmt.Errorf("verify testbed Git state after setup: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
