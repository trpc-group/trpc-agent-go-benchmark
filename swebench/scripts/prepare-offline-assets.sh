#!/usr/bin/env bash
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#

set -euo pipefail
export LC_ALL=C

if [[ $# -ne 1 || -z "${1// }" ]]; then
  echo "usage: $0 OUTPUT_DIRECTORY" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
swebench_dir="$(cd -- "$script_dir/.." && pwd)"
requested=$1
parent="$(mkdir -p -- "$(dirname -- "$requested")" && cd -- "$(dirname -- "$requested")" && pwd)"
output_dir="$parent/$(basename -- "$requested")"
marker=.swebench-offline-assets
lock_dir="$parent/.$(basename -- "$output_dir").prepare.lock"
lock_acquired=false
temporary=
backup=
cleanup() {
  if [[ -n "$temporary" ]]; then
    rm -rf -- "$temporary"
  fi
  if [[ -n "$backup" && -e "$backup" ]]; then
    if [[ ! -e "$output_dir" ]]; then
      mv -- "$backup" "$output_dir"
    else
      rm -rf -- "$backup"
    fi
  fi
  if [[ "$lock_acquired" == true ]]; then
    rmdir -- "$lock_dir" 2>/dev/null || true
  fi
}
trap cleanup EXIT

if ! mkdir -- "$lock_dir"; then
  echo "another asset preparation owns the lock: $lock_dir" >&2
  exit 1
fi
lock_acquired=true

if [[ -e "$output_dir" && ! -f "$output_dir/$marker" ]]; then
  echo "refusing to replace an unmarked asset directory: $output_dir" >&2
  exit 1
fi

temporary="$(mktemp -d "$parent/.swebench-offline-assets.XXXXXX")"

compiler_image='docker.io/swebench/sweb.eval.x86_64.psf_1776_requests-6028:latest'
legacy_image='docker.io/swebench/sweb.eval.x86_64.psf_1776_requests-2931:latest'
compiler_id="$(docker image inspect --format '{{.Id}}' "$compiler_image")"
legacy_id="$(docker image inspect --format '{{.Id}}' "$legacy_image")"
for image_id in "$compiler_id" "$legacy_id"; do
  if [[ ! "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "invalid local Docker image ID: $image_id" >&2
    exit 1
  fi
done

mkdir -p -- \
  "$temporary/bin" \
  "$temporary/httpbin" \
  "$temporary/requests-2931/wheels" \
  "$temporary/requests-modern/wheels"
touch "$temporary/$marker"
printf '%s  %s\n%s  %s\n' \
  "$compiler_image" "$compiler_id" \
  "$legacy_image" "$legacy_id" >"$temporary/SOURCE_IMAGES"

docker run --rm --pull=never --network=none \
  -v "$swebench_dir/tools/offline-tarpit:/source:ro" \
  -v "$temporary/bin:/output" \
  "$compiler_id" \
  gcc -static -O2 -Wall -Wextra -Werror \
    -o /output/swebench-tarpit /source/tarpit.c
chmod 0555 "$temporary/bin/swebench-tarpit"

(
  cd -- "$swebench_dir"
  go run ./tools/offline-httpbin-certs -output "$temporary/httpbin"
)

cp -- "$swebench_dir/config/offline/requests-2931.requirements.txt" \
  "$temporary/requests-2931/requirements.txt"
cp -- "$swebench_dir/config/offline/requests-modern.requirements.txt" \
  "$temporary/requests-modern/requirements.txt"

docker run --rm --pull=never --network=bridge \
  -v "$temporary/requests-2931:/assets" \
  "$legacy_id" bash -lc \
  'source /opt/miniconda3/bin/activate testbed && python -m pip download --dest /assets/wheels -r /assets/requirements.txt'

docker run --rm --pull=never --network=bridge \
  -v "$temporary/requests-modern:/assets" \
  "$compiler_id" bash -lc \
  'source /opt/miniconda3/bin/activate testbed && python -m pip download --dest /assets/wheels -r /assets/requirements.txt'

(
  cd -- "$temporary"
  while IFS= read -r -d '' file; do
    relative=${file#./}
    digest=$(sha256sum -- "$relative" | cut -d' ' -f1)
    printf '%s  %s\n' "$digest" "$relative"
  done < <(find . -type f ! -name SHA256SUMS -print0 | sort -z)
) >"$temporary/SHA256SUMS"

(
  cd -- "$temporary"
  sha256sum -c SHA256SUMS >/dev/null
)

if [[ -e "$output_dir" ]]; then
  backup="$parent/.swebench-offline-assets.backup.$$"
  mv -- "$output_dir" "$backup"
fi
mv -- "$temporary" "$output_dir"
temporary=
if [[ -n "$backup" ]]; then
  rm -rf -- "$backup"
  backup=
fi

echo "offline assets ready: $output_dir"
echo "sha256 manifest: $output_dir/SHA256SUMS"
