// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func nightlyCandidateAdapterRun(t *testing.T, script string, values map[string]string) ([]byte, error) {
	t.Helper()
	adapter, err := filepath.Abs("nightly-candidate.sh")
	if err != nil {
		t.Fatal(err)
	}
	values["PATH"], values["ADAPTER"] = os.Getenv("PATH"), adapter
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", "-c", `set -euo pipefail
fail() { printf '%s\n' "$*" >&2; exit 1; }
source "$ADAPTER"
uname() { case "$1" in -s) printf '%s\n' "$TEST_OS" ;; -m) printf '%s\n' "$TEST_ARCH" ;; *) return 95 ;; esac; }
`+script)
	command.Env = SetEnv(nil, values)
	if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
		t.Fatal(err)
	}
	return command.CombinedOutput()
}

func TestNightlyCandidateAdapterNativeResourceAdmission(t *testing.T) {
	for _, test := range []struct {
		name, arch, host, runner, policy, memory, disk string
		pass                                           bool
	}{
		{"hosted AMD64 observation", "amd64", "x86_64", "github-hosted", "github-hosted-amd64-v1", "", "", true},
		{"wrong native architecture", "amd64", "aarch64", "github-hosted", "github-hosted-amd64-v1", "", "", false},
		{"self-hosted observation rejected", "amd64", "x86_64", "self-hosted", "github-hosted-amd64-v1", "", "", false},
		{"missing provider rejected", "amd64", "x86_64", "", "github-hosted-amd64-v1", "", "", false},
		{"unsupported architecture rejected", "386", "x86_64", "github-hosted", "github-hosted-amd64-v1", "", "", false},
		{"unknown resource policy rejected", "amd64", "x86_64", "github-hosted", "unknown", "", "", false},
		{"mixed observation and floors rejected", "amd64", "x86_64", "github-hosted", "github-hosted-amd64-v1", "24576", "92160", false},
		{"explicit existing budgets", "amd64", "x86_64", "", "", "24576", "92160", true},
		{"ARM64 existing budgets", "arm64", "aarch64", "", "", "24576", "92160", true},
		{"ARM64 rejects AMD64 hosted policy", "arm64", "aarch64", "github-hosted", "github-hosted-amd64-v1", "", "", false},
		{"missing budget rejected", "amd64", "x86_64", "", "", "24576", "", false},
		{"zero budget rejected", "amd64", "x86_64", "", "", "0", "92160", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := nightlyCandidateAdapterRun(t, "candidate_native_host\ncandidate_resource_inputs\n", map[string]string{
				"TEST_OS": "Linux", "TEST_ARCH": test.host, "RUNNER_ENVIRONMENT": test.runner,
				"NKE_E2E_CANDIDATE_ARCH": test.arch, "NKE_E2E_CANDIDATE_RESOURCE_POLICY": test.policy,
				"NKE_E2E_CANDIDATE_MIN_AVAILABLE_MIB": test.memory, "NKE_E2E_CANDIDATE_MIN_DISK_MIB": test.disk,
			})
			if (err == nil) != test.pass {
				t.Fatalf("native resource admission: %v, want pass=%t: %s", err, test.pass, output)
			}
		})
	}
}

func TestNightlyCandidateAdapterResourceObservationUsesStagedFilesystem(t *testing.T) {
	for _, test := range []struct {
		name, policy, available, disk string
		pass                          bool
	}{
		{"hosted observations have no invented floor", "github-hosted-amd64-v1", "0", "0", true},
		{"fixed memory rejected", "", "24575", "92160", false},
		{"fixed disk rejected", "", "24576", "92159", false},
		{"fixed budgets available", "", "24576", "92160", true},
		{"invalid memory observation", "github-hosted-amd64-v1", "missing", "92160", false},
		{"invalid disk observation", "github-hosted-amd64-v1", "24576", "missing", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			output, err := nightlyCandidateAdapterRun(t, `
awk() {
  case "$1" in /MemAvailable:*) printf '%s\n' "$TEST_AVAILABLE" ;; *) command awk "$@" ;; esac
}
df() {
  [[ "$1" == -Pm && "$2" == "$STAGED_WORK" ]] || return 95
  printf 'Filesystem 1024-blocks Used Available Capacity Mounted-on\nfixture 999999 0 %s 0%% /fixture\n' "$TEST_DISK"
}
candidate_check_resources
`, map[string]string{
				"STATE_DIR": filepath.Join(work, "suite"), "STAGED_WORK": work,
				"NKE_E2E_CANDIDATE_RESOURCE_POLICY":   test.policy,
				"NKE_E2E_CANDIDATE_MIN_AVAILABLE_MIB": "24576", "NKE_E2E_CANDIDATE_MIN_DISK_MIB": "92160",
				"TEST_AVAILABLE": test.available, "TEST_DISK": test.disk,
			})
			if (err == nil) != test.pass {
				t.Fatalf("staged filesystem observation: %v, want pass=%t: %s", err, test.pass, output)
			}
		})
	}
}

func TestNightlyCandidateAdapterPrebuiltSelectionNeverBuilds(t *testing.T) {
	for _, test := range []struct {
		name, pin string
		mode      os.FileMode
		pass      bool
	}{
		{"exact executable", "", 0700, true},
		{"wrong bytes", "sha256:" + strings.Repeat("0", 64), 0700, false},
		{"non-executable", "", 0600, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cli, forbidden := filepath.Join(root, "nvkectl"), filepath.Join(root, "source-build-called")
			content := []byte("#!/bin/sh\nexit 92\n")
			if err := os.WriteFile(cli, content, test.mode); err != nil {
				t.Fatal(err)
			}
			pin := test.pin
			if pin == "" {
				pin = "sha256:" + plainSHA(content)
			}
			output, err := nightlyCandidateAdapterRun(t, `nvkectl_smoke_build() { touch "$FORBIDDEN"; return 93; }
candidate_select_prebuilt_cli
test "$NVKECTL_BIN" = "$NKE_E2E_CANDIDATE_NVKECTL"
`, map[string]string{"NKE_E2E_CANDIDATE_NVKECTL": cli, "NKE_E2E_CANDIDATE_NVKECTL_SHA256": pin, "FORBIDDEN": forbidden})
			if (err == nil) != test.pass {
				t.Fatalf("prebuilt CLI selection: %v, want pass=%t: %s", err, test.pass, output)
			}
			if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
				t.Fatal("candidate selected the stock source-build helper", err)
			}
			if actual, err := os.ReadFile(cli); err != nil || !bytes.Equal(actual, content) {
				t.Fatal("candidate changed its supplied executable", err)
			}
		})
	}
}

func TestNightlyCandidateAdapterVerifiesExactNativeDiskEndpoint(t *testing.T) {
	for _, test := range []struct {
		arch, filename string
		pass           bool
	}{
		{"amd64", "nvke-node-image-amd64.qcow2", true},
		{"amd64", "nvke-node-image-upstream-ovn-amd64.qcow2", true},
		{"arm64", "nvke-node-image-arm64.qcow2", true},
		{"arm64", "nvke-node-image-upstream-ovn-arm64.qcow2", true},
		{"amd64", "nvke-node-image-arm64.qcow2", false},
		{"amd64", "foreign.qcow2", false},
	} {
		t.Run(test.arch+"/"+test.filename, func(t *testing.T) {
			output, err := nightlyCandidateAdapterRun(t, `
curl() {
  [[ "${@: -1}" == "$EXPECTED_URL" ]] || return 95
  printf fixture-disk
}
timeout() { shift 3; "$@"; }
candidate_verify_disk_service
`, map[string]string{
				"NKE_E2E_CANDIDATE_ARCH": test.arch, "NKE_E2E_CANDIDATE_QCOW2": filepath.Join(t.TempDir(), test.filename),
				"NKE_E2E_CANDIDATE_QCOW2_SHA256": "sha256:" + plainSHA([]byte("fixture-disk")),
				"EXPECTED_URL":                   "http://172.30.0.1:18080/" + test.filename,
			})
			if (err == nil) != test.pass {
				t.Fatalf("native disk endpoint: %v, want pass=%t: %s", err, test.pass, output)
			}
		})
	}
}

func TestNightlyCandidateAdapterEntryRejectsBeforeProductHelpers(t *testing.T) {
	entry, err := filepath.Abs("nightly-candidate-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := filepath.Abs("nightly-candidate.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"", "amd64", "arm64", "386"} {
		t.Run("architecture="+arch, func(t *testing.T) {
			root, state := t.TempDir(), filepath.Join(t.TempDir(), "fresh-state")
			marker := filepath.Join(t.TempDir(), "product-helper-sourced")
			for _, name := range []string{"controller/hack/backend-e2e-lib.sh", "controller/hack/ci/nvkectl-smoke.sh"} {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("printf sourced >\"$SOURCE_MARKER\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", entry, root, adapter)
			command.Env = SetEnv(nil, map[string]string{"PATH": os.Getenv("PATH"), "NKE_E2E_CANDIDATE_ARCH": arch, "NKE_E2E_CANDIDATE_RESOURCE_POLICY": "unknown", "NKE_E2E_STATE_DIR": state, "SOURCE_MARKER": marker})
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("unsupported entrypoint executed: %v %s", err, output)
			}
			for _, path := range []string{state, marker} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("rejected entrypoint performed runtime or product work", path, err)
				}
			}
		})
	}
}

func TestNightlyCandidateAdapterWritesOnlyAcceptedStockTestConfig(t *testing.T) {
	for _, version := range []string{"1.34.2", "1.35.0", ""} {
		t.Run("version="+version, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.json")
			output, err := nightlyCandidateAdapterRun(t, "candidate_write_test_config", map[string]string{
				"E2E_CONFIG": path, "E2E_KUBECONFIG": filepath.Join(root, "kubeconfig"), "KIND_CLUSTER_NAME": "fixture",
				"NVKECTL_BIN": filepath.Join(root, "verified-cli"), "NKE_E2E_CANDIDATE_KUBERNETES_VERSION": version,
			})
			if version != "1.34.2" {
				if err == nil {
					t.Fatal("unsupported test version accepted")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("rejected test version wrote config", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("config generation: %v %s", err, output)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Kube      struct{ Kubeconfig, Context string }
				Nvkectl   struct{ Binary string }
				Lifecycle struct{ SkipDelete, PreserveOnFailure bool }
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&config); err != nil {
				t.Fatal("config contains a field absent from stock Go tests", err)
			}
			if config.Kube.Context != "kind-fixture" || config.Kube.Kubeconfig != filepath.Join(root, "kubeconfig") || config.Nvkectl.Binary != filepath.Join(root, "verified-cli") || config.Lifecycle.SkipDelete || config.Lifecycle.PreserveOnFailure {
				t.Fatal("stock test config lost exact inputs or cleanup", string(raw))
			}
		})
	}
}

func TestNightlyCandidateAdapterRequiresCompleteStockRuntimeReports(t *testing.T) {
	passed := map[string]any{"LeafNodeType": "It", "State": "passed"}
	excluded := map[string]any{"LeafNodeType": "It", "State": "skipped"}
	runtimeSkip := func(file string) map[string]any {
		return map[string]any{"LeafNodeType": "It", "State": "skipped", "Failure": map[string]any{"Location": map[string]string{"FileName": "/stock/" + file}}}
	}
	for _, test := range []struct {
		name                         string
		selected                     int
		specs                        []map[string]any
		succeeded, ownerReject, pass bool
	}{
		{name: "passed selection", selected: 1, specs: []map[string]any{passed, excluded}, succeeded: true, pass: true},
		{name: "stock multi-CIDR runtime skip", selected: 2, specs: []map[string]any{passed, runtimeSkip("pod_cidr_test.go"), excluded}, succeeded: true, pass: true},
		{name: "empty selection", specs: []map[string]any{excluded}, succeeded: true},
		{name: "unexpected OIDC skip", selected: 2, specs: []map[string]any{passed, runtimeSkip("oidc_test.go")}, succeeded: true},
		{name: "incomplete selected work", selected: 3, specs: []map[string]any{passed, runtimeSkip("pod_cidr_test.go"), excluded}, succeeded: true},
		{name: "failed suite", selected: 1, specs: []map[string]any{passed}},
		{name: "stock owner rejects skip", selected: 1, specs: []map[string]any{passed}, succeeded: true, ownerReject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := t.TempDir()
			if err := privateJSON(filepath.Join(state, "ginkgo.json"), []any{map[string]any{"SuiteSucceeded": test.succeeded, "PreRunStats": map[string]int{"SpecsThatWillRun": test.selected}, "SpecReports": test.specs}}); err != nil {
				t.Fatal(err)
			}
			if err := privateWrite(filepath.Join(state, "junit.xml"), []byte("<testsuite/>")); err != nil {
				t.Fatal(err)
			}
			output, err := nightlyCandidateAdapterRun(t, `log() { :; }
e2e_assert_no_new_skips() {
  [[ "${E2E_EXPECTED_RUNTIME_SKIPS[*]}" == pod_cidr_test.go && "$OWNER_REJECT" == false ]]
}
candidate_verify_reports
`, map[string]string{"STATE_DIR": state, "OWNER_REJECT": fmt.Sprint(test.ownerReject)})
			if (err == nil) != test.pass {
				t.Fatalf("complete runtime report admission: %v, want pass=%t: %s", err, test.pass, output)
			}
		})
	}
}
