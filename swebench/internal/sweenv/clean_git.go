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
if [[ -n "$(git status --porcelain)" ]]; then
  echo "official testbed is dirty before clean-room sanitation" >&2
  exit 1
fi
ancestor_count=$(git rev-list HEAD --count)
temporary=/tmp/tag-swebench-clean-history
rm -rf "$temporary"
git update-ref refs/heads/tag-swebench-clean-base HEAD
git clone -q --no-local --no-tags --no-checkout --single-branch \
  --branch tag-swebench-clean-base . "$temporary"
rm -rf .git
mv "$temporary/.git" .git
rm -rf "$temporary"
git remote remove origin
git reset -q --mixed HEAD
git repack -adq
git prune --expire=now
if [[ -n "$(git status --porcelain)" ]]; then
  echo "testbed changed during clean-room sanitation" >&2
  exit 1
fi
if [[ "$(git rev-list --all --count)" -ne "$ancestor_count" ]]; then
  echo "clean-room history does not match base ancestry" >&2
  exit 1
fi
if [[ "$(git for-each-ref | wc -l)" -ne 1 ]]; then
  echo "clean-room repository contains unexpected refs" >&2
  exit 1
fi
if [[ -n "$(git remote)" || -n "$(git tag --list)" ]]; then
  echo "clean-room repository contains remotes or tags" >&2
  exit 1
fi
unreachable=$(git fsck --unreachable --no-reflogs 2>&1 \
  | grep -E '^(unreachable|dangling) ' || true)
if [[ -n "$unreachable" ]]; then
  echo "clean-room repository contains unreachable objects" >&2
  echo "$unreachable" >&2
  exit 1
fi`

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
