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

if [[ $# -ne 1 || -z "${1// }" ]]; then
  echo "usage: $0 OUTPUT_DIRECTORY" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
swebench_dir="$(cd -- "$script_dir/.." && pwd)"
output_dir="$(mkdir -p -- "$1" && cd -- "$1" && pwd)"
marker="$output_dir/.tag-swebench-offline-assets"

if [[ -e "$output_dir/SHA256SUMS" && ! -e "$marker" ]]; then
  echo "refusing to replace an unmarked asset directory: $output_dir" >&2
  exit 1
fi
touch "$marker"

compiler_image='swebench/sweb.eval.x86_64.psf_1776_requests-6028:latest'
legacy_image='swebench/sweb.eval.x86_64.psf_1776_requests-2931:latest'
for image in "$compiler_image" "$legacy_image"; do
  docker image inspect "$image" >/dev/null
done

rm -rf -- "$output_dir/bin" "$output_dir/requests-2931" "$output_dir/requests-modern"
mkdir -p -- \
  "$output_dir/bin" \
  "$output_dir/requests-2931/wheels" \
  "$output_dir/requests-modern/wheels"

docker run --rm --pull=never --network=none \
  -v "$swebench_dir/tools/offline-tarpit:/source:ro" \
  -v "$output_dir/bin:/output" \
  "$compiler_image" \
  gcc -static -O2 -Wall -Wextra -Werror \
    -o /output/tag-swebench-tarpit /source/tarpit.c

cp -- "$swebench_dir/config/offline/requests-2931.requirements.txt" \
  "$output_dir/requests-2931/requirements.txt"
cp -- "$swebench_dir/config/offline/requests-modern.requirements.txt" \
  "$output_dir/requests-modern/requirements.txt"

docker run --rm --pull=never --network=bridge \
  -v "$output_dir/requests-2931:/assets" \
  "$legacy_image" bash -lc \
  'source /opt/miniconda3/bin/activate testbed && python -m pip download --dest /assets/wheels -r /assets/requirements.txt'

docker run --rm --pull=never --network=bridge \
  -v "$output_dir/requests-modern:/assets" \
  "$compiler_image" bash -lc \
  'source /opt/miniconda3/bin/activate testbed && python -m pip download --dest /assets/wheels -r /assets/requirements.txt'

(
  cd -- "$output_dir"
  find . -type f ! -name SHA256SUMS -print0 \
    | sort -z \
    | xargs -0 sha256sum >SHA256SUMS
)

echo "offline assets ready: $output_dir"
echo "sha256 manifest: $output_dir/SHA256SUMS"
