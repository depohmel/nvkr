#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Real VM sizing experiment using source-owned NVKE E2E. The caller prepares
# the exact same-job image, compatible scripts, pinned Go and projected seed.
set -euo pipefail

[[ $# == 3 ]] || { echo 'usage: ci-hosted-vm-e2e.sh NVKE_ROOT QCOW2 TOOLCHAIN_IMAGE' >&2; exit 2; }
nvke_root=$1 disk=$2 toolchain_image=$3
[[ "$nvke_root" == /* && -d "$nvke_root/controller" && -s "$disk" ]] || exit 1
test "$RUNNER_ENVIRONMENT" = github-hosted
test "$(uname -s)/$(uname -m)" = Linux/x86_64
test -c /dev/kvm
(exec 3<>/dev/kvm)

bin="$RUNNER_TEMP/nvke-e2e-tools"
mkdir -p "$bin"
export PATH="$bin:$PATH"
pin() { make --no-print-directory -C "$nvke_root" -f make/versions.mk "print-$1"; }
fetch() { curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 300 -o "$1" "$2"; }
for tool in kind kubectl clusterctl; do
  case "$tool" in
    kind) version=$(pin KIND_VERSION); sha=$(pin KIND_SHA256_AMD64)
      url="https://github.com/kubernetes-sigs/kind/releases/download/$version/kind-linux-amd64" ;;
    kubectl) version=$(pin KUBECTL_VERSION); sha=$(pin KUBECTL_SHA256_AMD64)
      url="https://dl.k8s.io/release/$version/bin/linux/amd64/kubectl" ;;
    clusterctl) version=$(pin CAPI_VERSION); sha=$(pin CLUSTERCTL_SHA256_AMD64)
      url="https://github.com/kubernetes-sigs/cluster-api/releases/download/$version/clusterctl-linux-amd64" ;;
  esac
  [[ "$sha" =~ ^[0-9a-f]{64}$ ]]
  fetch "$bin/$tool" "$url"
  printf '%s  %s\n' "$sha" "$bin/$tool" | sha256sum -c
  chmod 0755 "$bin/$tool"
done

# This toolchain was freshly built and natively validated in this same job.
docker run --rm --network none --entrypoint cat "$toolchain_image" /usr/local/bin/helm > "$bin/helm-real"
chmod 0755 "$bin/helm-real"
test "$("$bin/helm-real" version --template '{{.Version}}')" = "$(pin HELM_VERSION)"
install -m 0755 "$nvke_root/controller/hack/ci/helm-dockerhub-mirror.sh" "$bin/helm"
export NKE_E2E_HELM_REAL_BIN="$bin/helm-real"
export NKE_E2E_DOCKERHUB_MIRROR=mirror.gcr.io

# Exact AMD64 Tilt version/checksum from the selected product's existing CI.
grep -Fq ': "${TILT_VERSION:=v0.37.5}"' "$nvke_root/controller/hack/ci/backend-e2e.sh"
grep -Fq '9efb56969c48ddea4adfb56a604b9ca5ed254924ddf140fc3e068a305cacf558' "$nvke_root/controller/hack/ci/backend-e2e.sh"
fetch "$bin/tilt.tgz" https://github.com/tilt-dev/tilt/releases/download/v0.37.5/tilt.0.37.5.linux.x86_64.tar.gz
printf '%s  %s\n' 9efb56969c48ddea4adfb56a604b9ca5ed254924ddf140fc3e068a305cacf558 "$bin/tilt.tgz" | sha256sum -c
tar -xzf "$bin/tilt.tgz" -C "$bin" tilt

cd "$nvke_root"
export CAPNICO_SOURCE_DIR="$RUNNER_TEMP/capnico-source"
source_dir=$(go mod download -json github.com/dsx-ai-factory/cluster-api-provider-nico | jq -er .Dir)
mkdir -p "$CAPNICO_SOURCE_DIR"
cp -R "$source_dir/." "$CAPNICO_SOURCE_DIR/"
chmod -R u+w "$CAPNICO_SOURCE_DIR"

export NKE_E2E_OS_IMAGE_FILE="$disk"
NKE_E2E_OS_IMAGE_SHA256=$(sha256sum "$disk")
export NKE_E2E_OS_IMAGE_SHA256="${NKE_E2E_OS_IMAGE_SHA256%% *}"
unset NKE_E2E_OS_IMAGE_TAG
export NICO_SIM_SEED_PATH="$nvke_root/controller/nico-sim/laptop-seed.yaml"
test -s "$NICO_SIM_SEED_PATH"
export NKE_E2E_LABEL_FILTER='(nico-vms && ovn) || health'
export NKE_E2E_GINKGO_TIMEOUT=270m
export NKE_E2E_NICO_VMS_CNI=ovn-kubernetes
export NKE_E2E_STATE_DIR="$RUNNER_TEMP/nvke-vm-e2e"
export NICO_VMS_BASE_IMAGE="$nvke_root/controller/nico-sim/images/ubuntu-cloud.qcow2"
export NICO_VMS_BRIDGE_NAME=cwebr0
export NICO_VMS_BRIDGE_CIDR=172.30.0.0/24
export NICO_VMS_LIBVIRT_URI=qemu:///session
export NICO_VMS_READINESS_TIMEOUT=30m
export KIND_CLUSTER_NAME=cwe-nico-vms
export NKE_CI_HOST_FDE=true
export NKE_BRINGUP_ARTIFACT_DIR="$NKE_E2E_STATE_DIR/host"

cleanup() {
  local status=$?
  trap - EXIT
  bash controller/test/kubeadm/scripts/teardown-ci-host.sh || status=1
  exit "$status"
}
trap cleanup EXIT

for setting in fs.inotify.max_user_instances=1024 fs.inotify.max_user_watches=1048576; do
  key=${setting%=*} floor=${setting#*=}
  if (( $(sysctl -n "$key") < floor )); then sudo -n sysctl -w "$setting"; fi
done
bash controller/test/kubeadm/scripts/setup-ci-host.sh
test -c /dev/kvm && test -r /dev/kvm && test -w /dev/kvm
echo 'Run the existing Go/Ginkgo OVN and health tests with the nightly projected guest shape.'
bash controller/hack/ci/nico-vms-e2e.sh
