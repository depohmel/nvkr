#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Sample the whole host and Docker alongside process timing. This records one
# experimental allocation; it does not derive minimum resource requirements.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo 'usage: ci-measure-workload.sh COMMAND [ARG...]' >&2
  exit 2
fi
[[ $(uname -s) == Linux && -x /usr/bin/time ]] || {
  echo 'measurement requires Linux and /usr/bin/time' >&2
  exit 1
}

snapshot() {
  printf '\n--- resource sample %s ---\n' "$(date -u +%FT%TZ)"
  free -m
  df -Pm .
  if [[ -r /sys/fs/cgroup/memory.peak ]]; then
    printf 'host cgroup memory.peak bytes: '
    cat /sys/fs/cgroup/memory.peak
  fi
  if [[ -r /sys/fs/cgroup/memory.events ]]; then
    printf 'host cgroup memory.events:\n'
    cat /sys/fs/cgroup/memory.events
  fi
  timeout --kill-after=2s 10s docker stats --no-stream \
    --format 'container={{.Name}} cpu={{.CPUPerc}} memory={{.MemUsage}} block_io={{.BlockIO}}' || \
    echo 'Docker usage sample unavailable'
}

sampler=''
cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$sampler" ]]; then
    kill -- "-$sampler" 2>/dev/null || true
    wait "$sampler" 2>/dev/null || true
  fi
  snapshot || true
  printf '\nWorkload exit status: %s\n' "$status"
  echo 'Process maximum RSS excludes Docker. Host/container samples are observations, not proven minima.'
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

snapshot
export -f snapshot
setsid bash -c 'while sleep 15; do snapshot || true; done' &
sampler=$!
/usr/bin/time -v "$@"
