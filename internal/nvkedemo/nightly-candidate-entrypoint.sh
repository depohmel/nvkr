#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# The sourced adapter and stock smoke helpers consume these shared globals.
# shellcheck disable=SC2034

# NVKR owns this adapter. The selected NVKE checkout supplies product code and
# its existing backend/CLI test helpers, and remains unchanged by this entrypoint.
set -Eeuo pipefail
# Existing stock deployment helpers must not create bytecode in the checkout.
export PYTHONDONTWRITEBYTECODE=1

log() { printf '[nvkr-nightly-candidate] %s\n' "$*"; }
fail() { log "ERROR: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }

[[ $# == 2 ]] || fail "expected an absolute NVKE source root and NVKR candidate library"
[[ "$1" == /* && -d "$1" && ! -L "$1" ]] || fail "selected NVKE source root must be an absolute non-symlink directory"
[[ "$2" == /* && -f "$2" && ! -L "$2" ]] || fail "NVKR candidate library must be an absolute regular non-symlink file"
WORKSPACE_ROOT="$1"
CONTROLLER_ROOT="${WORKSPACE_ROOT}/controller"
candidate_library="$2"
for source_file in "${CONTROLLER_ROOT}/hack/backend-e2e-lib.sh" "${CONTROLLER_ROOT}/hack/ci/nvkectl-smoke.sh"; do
  [[ -f "${source_file}" && ! -L "${source_file}" ]] || fail "selected stock NVKE test helper is missing or symlinked"
done

# shellcheck disable=SC1090
source "${candidate_library}"
case "${NKE_E2E_CANDIDATE_ARCH:-}" in
  amd64|arm64) ;;
  *) fail "NVKR-owned candidate entrypoint requires explicit AMD64 or ARM64" ;;
esac
candidate_native_host
candidate_resource_inputs
# Only admitted hosts may execute the selected product helpers at source time.
# shellcheck disable=SC1091
source "${CONTROLLER_ROOT}/hack/backend-e2e-lib.sh"
# shellcheck disable=SC1091
source "${CONTROLLER_ROOT}/hack/ci/nvkectl-smoke.sh"

RUN_ID="${NKE_E2E_RUN_ID:-${GITHUB_RUN_ID:-local-$$}}"
RUN_ID="${RUN_ID//[^a-zA-Z0-9_.-]/-}"
STATE_DIR="${NKE_E2E_STATE_DIR:?parent must provide a fresh private candidate state directory}"
[[ "${STATE_DIR}" == /* && ! -e "${STATE_DIR}" && ! -L "${STATE_DIR}" ]] || fail "candidate state directory must be fresh and absolute"
case "${STATE_DIR}/" in "${WORKSPACE_ROOT}/"*) fail "candidate state must be outside the immutable product checkout" ;; esac
export KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-nvkr-nightly-${NKE_E2E_CANDIDATE_ARCH}}"
LABEL_FILTER="${NKE_E2E_LABEL_FILTER:-}"
KEEP="${NKE_E2E_KEEP:-}"
LIBVIRT_URI="${NICO_VMS_LIBVIRT_URI:-qemu:///session}"
E2E_BIN="${STATE_DIR}/cwe-e2e.test"
E2E_CONFIG="${STATE_DIR}/e2e-config.json"
E2E_KUBECONFIG="${STATE_DIR}/kubeconfig"
NVKECTL_BIN="${NKE_E2E_CANDIDATE_NVKECTL:-}"
NVKECTL_SMOKE_KUBECONFIG="${E2E_KUBECONFIG}"
TMP_DIR="${STATE_DIR}"
BACKEND=nico-vms
KIND_CLUSTER_OWNED=false
NICO_VMS_ORG_ID=novxwn552ssc
NICO_VMS_TOKEN=nico-vmsctl-token
export NKE_E2E_REQUIRE_AUTHN=true
unset NVKECTL_SMOKE_RUNNER
cd "${CONTROLLER_ROOT}"
run_candidate_e2e
