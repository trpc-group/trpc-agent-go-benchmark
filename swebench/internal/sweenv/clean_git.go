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
	"fmt"
	"strings"
)

const cleanGitRepositoryScript = `set -euo pipefail
cd /testbed
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "official testbed has tracked changes before clean-room sanitation" >&2
  exit 1
fi
# Preserve ignored build products supplied by the official image. Remove only
# ordinary untracked files, which are not part of the task's base snapshot.
git clean -fd

# Rebuild initialized submodules before the top-level .git directory is
# replaced. Deepest-first ordering keeps recursively nested gitdirs available
# while each parent repository is reconstructed.
mapfile -t submodules < <(
  git submodule foreach --quiet --recursive 'printf "%s\n" "$displaypath"' \
    | awk -F/ '{print NF "\t" $0}' \
    | sort -k1,1nr -k2,2 \
    | cut -f2-
)

clean_repository() {
  local path=$1
  local suffix=$2
  local ancestor_count temporary unreachable

  if ! git -C "$path" diff --quiet || ! git -C "$path" diff --cached --quiet; then
    echo "repository has tracked changes before clean-room sanitation: $path" >&2
    exit 1
  fi
  git -C "$path" clean -fd
  ancestor_count=$(git -C "$path" rev-list HEAD --count)
  temporary="/tmp/tag-swebench-clean-history-$suffix"
  rm -rf "$temporary"
  git -C "$path" update-ref refs/heads/tag-swebench-clean-base HEAD
  git clone -q --no-local --no-tags --no-checkout --single-branch \
    --branch tag-swebench-clean-base "$path" "$temporary"
  rm -rf "$path/.git"
  mv "$temporary/.git" "$path/.git"
  rm -rf "$temporary"
  git -C "$path" remote remove origin
  git -C "$path" reset -q --mixed HEAD
  git -C "$path" repack -adq
  git -C "$path" prune --expire=now
  if [[ -n "$(git -C "$path" status --porcelain)" ]]; then
    echo "repository changed during clean-room sanitation: $path" >&2
    exit 1
  fi
  if [[ "$(git -C "$path" rev-list --all --count)" -ne "$ancestor_count" ]]; then
    echo "clean-room history does not match base ancestry: $path" >&2
    exit 1
  fi
  if [[ "$(git -C "$path" for-each-ref | wc -l)" -ne 1 ]]; then
    echo "clean-room repository contains unexpected refs: $path" >&2
    exit 1
  fi
  if [[ -n "$(git -C "$path" remote)" || -n "$(git -C "$path" tag --list)" ]]; then
    echo "clean-room repository contains remotes or tags: $path" >&2
    exit 1
  fi
  unreachable=$(git -C "$path" fsck --unreachable --no-reflogs 2>&1 \
    | grep -E '^(unreachable|dangling) ' || true)
  if [[ -n "$unreachable" ]]; then
    echo "clean-room repository contains unreachable objects: $path" >&2
    echo "$unreachable" >&2
    exit 1
  fi
}

index=0
for submodule in "${submodules[@]}"; do
  index=$((index + 1))
  clean_repository "$submodule" "submodule-$index"
done
clean_repository . root`

func (f DockerFactory) sanitizeGitRepository(
	ctx context.Context,
	environment *dockerEnvironment,
) error {
	out, err := environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", "-w", "/testbed", environment.name, "bash", "-lc", cleanGitRepositoryScript,
	)
	if err != nil {
		return fmt.Errorf("sanitize testbed Git history: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
