// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"sigs.k8s.io/yaml"
)

type nightlyCoordinatorDocument struct {
	Name        string                     `json:"name"`
	On          map[string]json.RawMessage `json:"on"`
	Permissions map[string]string          `json:"permissions"`
	Concurrency struct {
		Group            string `json:"group"`
		CancelInProgress bool   `json:"cancel-in-progress"`
	} `json:"concurrency"`
	Jobs map[string]struct {
		Name           string            `json:"name"`
		RunsOn         string            `json:"runs-on"`
		Needs          []string          `json:"needs"`
		If             string            `json:"if"`
		Environment    string            `json:"environment"`
		TimeoutMinutes int               `json:"timeout-minutes"`
		Permissions    map[string]string `json:"permissions"`
		Env            map[string]string `json:"env"`
		// Authored action inputs use GitHub's Boolean/integer/string scalars;
		// the generated workflow's string-only With model is intentionally narrower.
		Steps []struct {
			ID             string            `json:"id"`
			Name           string            `json:"name"`
			Run            string            `json:"run"`
			Uses           string            `json:"uses"`
			If             string            `json:"if"`
			Env            map[string]string `json:"env"`
			With           map[string]any    `json:"with"`
			TimeoutMinutes int               `json:"timeout-minutes"`
		} `json:"steps"`
		Outputs     map[string]string `json:"outputs"`
		Strategy    json.RawMessage   `json:"strategy"`
		Concurrency struct {
			Group            string `json:"group"`
			CancelInProgress bool   `json:"cancel-in-progress"`
		} `json:"concurrency"`
	} `json:"jobs"`
}

func nightlyCoordinatorFixture(t *testing.T) (nightlyCoordinatorDocument, []map[string]string) {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow nightlyCoordinatorDocument
	if err := yaml.UnmarshalStrict(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../inputs/testing/nightly-profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var descriptions []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &descriptions); err != nil {
		t.Fatal(err)
	}
	// This fixture projects routing metadata. The native runtime owner validates
	// the nested guest resource settings in the same maintained profile file.
	profiles := make([]map[string]string, 0, len(descriptions))
	for _, description := range descriptions {
		profile := make(map[string]string)
		for key, value := range description {
			if key == "resources" {
				continue
			}
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				t.Fatal(err)
			}
			profile[key] = text
		}
		profiles = append(profiles, profile)
	}
	return workflow, profiles
}

func TestNightlyHostedPreflightCannotBuildOrPublish(t *testing.T) {
	workflow, _ := nightlyCoordinatorFixture(t)
	probe := workflow.Jobs["hosted-preflight"]
	if probe.RunsOn != "ubuntu-24.04" || probe.TimeoutMinutes != 15 ||
		probe.If != "github.event_name == 'workflow_dispatch' && inputs.hosted_preflight_only" ||
		probe.Environment != "" || len(probe.Needs) != 0 || probe.Permissions == nil || len(probe.Permissions) != 0 {
		t.Fatal("hosted probe must be an isolated, manual, unprivileged hosted job")
	}
	for _, step := range probe.Steps {
		if step.Uses != "" || strings.Contains(step.Run, "secrets.") || strings.Contains(step.Run, "github.token") {
			t.Fatal("hosted probe must not check out product sources, use credentials or upload artifacts")
		}
	}
	if len(probe.Steps) != 2 || !strings.Contains(probe.Steps[0].Run, "/dev/kvm") ||
		!strings.Contains(probe.Steps[0].Run, "Linux/x86_64") ||
		!strings.Contains(probe.Steps[0].Run, "github-hosted") ||
		!strings.Contains(probe.Steps[1].Run, "q35,accel=kvm") ||
		!strings.Contains(probe.Steps[1].Run, "qemu-system-x86_64") ||
		!strings.Contains(probe.Steps[1].Run, "VM status: paused") {
		t.Fatal("hosted probe must require accessible KVM and verify actual QEMU initialization")
	}
	for _, job := range []string{"select", "source-contract"} {
		if !strings.Contains(workflow.Jobs[job].If, "!inputs.hosted_preflight_only") {
			t.Fatalf("%s must not run during the hosted capability probe", job)
		}
	}
	// Exercise the authored conflict gate before host inspection or package installation.
	root := t.TempDir()
	command := exec.CommandContext(t.Context(), "bash", "-c", probe.Steps[0].Run)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SOURCE_CONTRACT_ONLY=true", "RUNNER_TEMP=" + root}
	log, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(log), "Select only one diagnostic mode.") {
		t.Fatalf("conflicting diagnostic modes were not rejected: %v %s", err, log)
	}
}

func TestNightlyCoordinatorMaintainsExplicitProducerAndRuntimeBoundary(t *testing.T) {
	workflow, profiles := nightlyCoordinatorFixture(t)
	if len(workflow.On) != 2 || workflow.On["schedule"] == nil || workflow.On["workflow_dispatch"] == nil ||
		!reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) ||
		workflow.Concurrency.CancelInProgress || workflow.Concurrency.Group != "nvkr-nightly-main" {
		t.Fatal("nightly triggers, concurrency or root permissions changed")
	}
	build, runtime := workflow.Jobs["build"], workflow.Jobs["runtime-test"]
	selection := workflow.Jobs["select-runtime"]
	if selection.RunsOn != "ubuntu-24.04" ||
		!reflect.DeepEqual(selection.Permissions, map[string]string{"contents": "read", "actions": "read"}) ||
		!reflect.DeepEqual(runtime.Needs, []string{"select-runtime"}) ||
		!strings.Contains(runtime.If, "needs.select-runtime.outputs.available == 'true'") {
		t.Fatal("runtime target allocated before completed candidate selection")
	}
	if !reflect.DeepEqual(build.Permissions, map[string]string{"contents": "read", "actions": "write"}) ||
		!reflect.DeepEqual(runtime.Permissions, map[string]string{"contents": "read", "actions": "read"}) ||
		runtime.RunsOn != "${{ matrix.runtime_target == 'native-amd64-vm' && 'ubuntu-24.04' || 'arc-arm64-kvm' }}" ||
		runtime.Environment != "${{ matrix.runtime_target == 'native-amd64-vm' && 'nvkr-release' || 'nvkr-arc' }}" ||
		runtime.Env["NATIVE_ARCH"] != "${{ matrix.runtime_target == 'native-amd64-vm' && 'amd64' || 'arm64' }}" ||
		runtime.Concurrency.CancelInProgress || runtime.Concurrency.Group != "nvkr-nightly-${{ matrix.runtime_target }}-runtime" ||
		runtime.TimeoutMinutes != 360 {
		t.Fatal("dispatch credentials escaped into native runtime or shared target admission changed")
	}
	executed, checkedSource, publicUpload := false, false, false
	for _, step := range runtime.Steps {
		if step.With["repository"] == "dsx-ai-factory/nvke" {
			t.Fatal("runtime must use authenticated installer sources rather than a separate suite checkout")
		}
		if step.ID == "suite" {
			checkedSource = strings.Contains(step.Run, ".runtimeSourceCommit == $nvke")
		}
		if strings.Contains(step.Run, "go test ./internal/nvkedemo -run '^TestNightlyPublishedCandidate$'") {
			executed = step.Env["NVKR_NIGHTLY_ENABLE"] == "1" &&
				step.Env["NVKR_NIGHTLY_NETWORK_HELPER"] == "${{ github.workspace }}/bin/nvke-demo" &&
				strings.Contains(step.Run, "-list '^TestNightlyPublishedCandidate$'") &&
				strings.Contains(step.Run, `-timeout "${remaining}s" -count=1 -v`) &&
				strings.Contains(step.Run, `.phase == "runtime" and .runtimePassed == true`) &&
				strings.Contains(step.Run, `.coordinatorRun == $run and .coordinatorAttempt == $attempt`) &&
				strings.Contains(step.Run, `.sourceCommit == $sha`)
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			publicUpload = step.If == "always()" &&
				step.With["path"] == "${{ runner.temp }}/nightly-reports-${{ matrix.name }}/cycle.json"
		}
	}
	if !executed || !checkedSource || !publicUpload || len(profiles) != 2 {
		t.Fatal("nightly omitted real suite execution, exact source or private/public report separation")
	}
	for _, profile := range profiles {
		arch := strings.TrimSuffix(strings.TrimPrefix(profile["runtime_target"], "native-"), "-vm")
		if (arch != "amd64" && arch != "arm64") || profile["name"] != "nvke-nightly-"+arch || profile["network"] != "ovn" {
			t.Fatal("nightly must select NVIDIA OVN")
		}
		input, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", profile["definition"]))
		if err != nil {
			t.Fatal(err)
		}
		environment, err := definition.LoadEnvironment(t.Context(), filepath.Join("../..", profile["environment"]))
		if err != nil {
			t.Fatal(err)
		}
		if environment.GitHubBundle != profile["bundle"] ||
			profile["name"] != profile["bundle"] || profile["build_workflow"] != "nvkr-run-"+profile["bundle"]+".yml" ||
			profile["plan"] != ".github/nvkr-run-"+profile["bundle"]+"/plan.json" {
			t.Fatal("nightly reduced output membership or changed the exact generated producer")
		}
		plan, err := factory.LoadPlanContext(t.Context(), filepath.Join("../..", profile["plan"]))
		if err != nil {
			t.Fatal(err)
		}
		membership := make(map[string]bool, len(plan.LifecycleOutputs))
		for _, output := range plan.LifecycleOutputs {
			membership[output.Name] = true
		}
		if len(input.Outputs) != len(membership) {
			t.Fatal("generated Plan changed Definition output membership")
		}
		disk, images := false, false
		for _, output := range input.Outputs {
			if !membership[output.Name] || output.Platform != "linux/"+arch {
				t.Fatalf("output %q absent from generated Plan", output.Name)
			}
			disk = disk || output.Name == profile["disk_output"]
			images = images || output.Name == profile["profiles_output"]
		}
		if !disk || !images {
			t.Fatal("profile selects absent disk or controller profile output")
		}
	}
}

func TestNightlyCoordinatorSelectionFailsClosedBeforeDispatch(t *testing.T) {
	workflow, profiles := nightlyCoordinatorFixture(t)
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("nightly contract tests require %s: %v", tool, err)
		}
	}
	var script string
	for _, step := range workflow.Jobs["select"].Steps {
		if step.ID == "profiles" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("nightly select job omitted maintained profile admission")
	}
	for _, test := range []struct {
		name      string
		selection string
		mutation  string
		want      int
	}{
		{"maintained all", "all", "", 2},
		{"maintained ARM64", "nvke-nightly-arm64", "", 1},
		{"maintained AMD64", "nvke-nightly-amd64", "", 1},
		{"retired upstream", "nvke-nightly-upstream-ovn-arm64", "", 0},
		{"stale two-profile schedule", "all", "upstream profile", 0},
		{"upstream network substituted", "all", "upstream network", 0},
		{"unknown", "unknown", "", 0},
		{"unreviewed harness", "all", "harness_commit", 0},
		{"unsupported runtime", "all", "runtime_target", 0},
		{"cross architecture runtime", "all", "cross runtime", 0},
		{"unsupported network", "all", "network", 0},
		{"duplicate profile", "all", "duplicate", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fixture := make([]map[string]string, len(profiles))
			for i, profile := range profiles {
				fixture[i] = make(map[string]string, len(profile))
				for key, value := range profile {
					fixture[i][key] = value
				}
			}
			if test.mutation == "duplicate" {
				fixture = append(fixture, fixture[0])
			} else if test.mutation == "upstream profile" {
				fixture = append(fixture, map[string]string{
					"name": "nvke-nightly-upstream-ovn-arm64", "network": "upstream-ovn",
					"runtime_target": profiles[0]["runtime_target"], "harness_commit": profiles[0]["harness_commit"],
				})
			} else if test.mutation == "upstream network" {
				fixture[0]["network"] = "upstream-ovn"
			} else if test.mutation == "cross runtime" {
				fixture[0]["runtime_target"] = fixture[1]["runtime_target"]
			} else if test.mutation != "" {
				fixture[0][test.mutation] = "unsupported"
			}
			raw, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "inputs/testing"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "inputs/testing/nightly-profiles.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "outputs")
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Dir = root
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "PROFILE=" + test.selection,
				"NVKE_COMMIT=" + nightlyNVKESHA, "GITHUB_OUTPUT=" + output, "RUNNER_TEMP=" + root}
			log, err := command.CombinedOutput()
			if test.want == 0 {
				if err == nil {
					t.Fatal("invalid profile emitted a runtime matrix")
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("rejected selection emitted outputs before dispatch")
				}
				return
			}
			if err != nil {
				t.Fatalf("read-only fixture selection failed: %v %s", err, log)
			}
			raw, err = os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var matrix struct {
				Include []map[string]string `json:"include"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(string(raw)), "matrix=")), &matrix); err != nil || len(matrix.Include) != test.want {
				t.Fatalf("selected wrong profile closure: %v %s", err, raw)
			}
			for _, entry := range matrix.Include {
				if entry["nvke_commit"] != nightlyNVKESHA {
					t.Fatal("selected matrix lost the cycle-frozen NVKE source")
				}
			}
		})
	}
}

func TestNightlyRuntimeSelectionDoesNotAllocateUnavailableCandidates(t *testing.T) {
	workflow, profiles := nightlyCoordinatorFixture(t)
	var script string
	for _, step := range workflow.Jobs["select-runtime"].Steps {
		if step.ID == "completed" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("completed candidate selector missing")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("nightly contract tests require %s: %v", tool, err)
		}
	}
	for _, test := range []struct {
		name      string
		available int
		mutation  string
		reject    bool
	}{
		{"no locator", 0, "", false},
		{"one completed project", 1, "", false},
		{"both completed projects", 2, "", false},
		{"retired upstream matrix", 1, "upstream", true},
		{"wrong source", 1, "sha", true},
		{"expired locator", 1, "expired", true},
		{"duplicate locator", 1, "duplicate", true},
		{"unknown matrix", 1, "matrix", true},
		{"cross architecture matrix", 1, "cross runtime", true},
		{"missing NVKE revision", 1, "missing nvke", true},
		{"mutable NVKE revision", 1, "mutable nvke", true},
		{"different NVKE source per architecture", 2, "mixed nvke", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			fake := `#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  if [[ "$argument" == repos/* ]]; then
    case "$argument" in
      *name=nightly-build-nvke-nightly-amd64-101-3\&per_page=100) cat "$FAKE_GH_ROOT/0.json"; exit ;;
      *name=nightly-build-nvke-nightly-arm64-101-3\&per_page=100) cat "$FAKE_GH_ROOT/1.json"; exit ;;
      *) exit 99 ;;
    esac
  fi
done
exit 99
`
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			matrix := make([]map[string]string, len(profiles))
			for i, p := range profiles {
				matrix[i] = map[string]string{"name": p["name"], "nvke_commit": nightlyNVKESHA, "runtime_target": p["runtime_target"]}
				artifacts := []map[string]any{}
				if i < test.available {
					artifact := map[string]any{"name": "nightly-build-" + p["name"] + "-101-3", "expired": false,
						"workflow_run": map[string]string{"head_sha": nightlySHA}}
					if test.mutation == "sha" {
						artifact["workflow_run"] = map[string]string{"head_sha": strings.Repeat("b", 40)}
					}
					if test.mutation == "expired" {
						artifact["expired"] = true
					}
					artifacts = append(artifacts, artifact)
					if test.mutation == "duplicate" {
						artifacts = append(artifacts, artifact)
					}
				}
				fixtureJSON(t, filepath.Join(root, string(rune('0'+i))+".json"), map[string]any{"total_count": len(artifacts), "artifacts": artifacts})
			}
			switch test.mutation {
			case "missing nvke":
				delete(matrix[0], "nvke_commit")
			case "mutable nvke":
				matrix[0]["nvke_commit"] = "main"
			case "mixed nvke":
				matrix[0]["nvke_commit"] = strings.Repeat("e", 40)
			case "matrix":
				matrix[0]["name"] = "foreign"
			case "upstream":
				matrix[0]["name"] = "nvke-nightly-upstream-ovn-arm64"
			case "cross runtime":
				matrix[0]["runtime_target"] = matrix[1]["runtime_target"]
			}
			raw, err := json.Marshal(map[string]any{"include": matrix})
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "outputs")
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "FAKE_GH_ROOT=" + root,
				"GITHUB_OUTPUT=" + out, "GITHUB_REPOSITORY=example/nvkr", "GITHUB_RUN_ID=101", "GITHUB_RUN_ATTEMPT=3",
				"GITHUB_SHA=" + nightlySHA, "MATRIX=" + string(raw)}
			log, err := command.CombinedOutput()
			if test.reject {
				if err == nil {
					t.Fatalf("invalid locator admitted: %s", log)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("rejected locator emitted runtime matrix")
				}
				return
			}
			if err != nil {
				t.Fatalf("selector failed: %v %s", err, log)
			}
			raw, err = os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			var selected struct {
				Include []map[string]string `json:"include"`
			}
			if len(lines) != 2 {
				t.Fatalf("invalid selector output: %s", raw)
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[0], "matrix=")), &selected); err != nil {
				t.Fatal(err)
			}
			if len(selected.Include) != test.available || lines[1] != "available="+map[bool]string{true: "true", false: "false"}[test.available > 0] {
				t.Fatalf("wrong runtime eligibility: %s", raw)
			}
			for _, entry := range selected.Include {
				if entry["nvke_commit"] != nightlyNVKESHA {
					t.Fatal("runtime matrix changed the frozen NVKE source")
				}
			}
		})
	}
}

func TestNightlyCoordinatorFreezesAuthenticatedNVKEMainOnce(t *testing.T) {
	workflow, _ := nightlyCoordinatorFixture(t)
	resolver := workflow.Jobs["resolve-source"]
	if resolver.RunsOn != "ubuntu-24.04" || resolver.TimeoutMinutes != 5 || len(resolver.Permissions) != 0 || len(resolver.Steps) != 1 || resolver.Outputs["nvke_commit"] != "${{ steps.source.outputs.nvke_commit }}" || resolver.Steps[0].Env["GH_TOKEN"] != "${{ secrets.NKE_SOURCE_TOKEN }}" {
		t.Fatal("source resolution lost its authenticated, bounded single-owner binding")
	}
	for _, job := range []string{"select", "source-contract"} {
		if !reflect.DeepEqual(workflow.Jobs[job].Needs, []string{"resolve-source"}) {
			t.Fatalf("%s does not depend on the one source resolution", job)
		}
	}
	for _, test := range []struct {
		name, ref, kind, sha    string
		credential, apiOK, pass bool
	}{
		{"exact main", "refs/heads/main", "commit", nightlyNVKESHA, true, true, true},
		{"other ref", "refs/heads/other", "commit", nightlyNVKESHA, true, true, false},
		{"tag object", "refs/heads/main", "tag", nightlyNVKESHA, true, true, false},
		{"mutable SHA", "refs/heads/main", "commit", "main", true, true, false},
		{"missing SHA", "refs/heads/main", "commit", "", true, true, false},
		{"uppercase SHA", "refs/heads/main", "commit", strings.ToUpper(nightlyNVKESHA), true, true, false},
		{"API failure", "refs/heads/main", "commit", nightlyNVKESHA, true, false, false},
		{"missing credential", "refs/heads/main", "commit", nightlyNVKESHA, false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			timeout, err := exec.LookPath("timeout")
			if err != nil {
				timeout, err = exec.LookPath("gtimeout")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(timeout, filepath.Join(root, "timeout")); err != nil {
				t.Fatal(err)
			}
			fixtureJSON(t, filepath.Join(root, "response.json"), map[string]any{"ref": test.ref, "object": map[string]string{"type": test.kind, "sha": test.sha}})
			stub := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$SOURCE_FIXTURE/calls"
[[ "$*" == 'api --hostname github.com -H X-GitHub-Api-Version: 2026-03-10 repos/dsx-ai-factory/nvke/git/ref/heads/main' ]]
[[ "$SOURCE_API_OK" == true ]]
cat "$SOURCE_FIXTURE/response.json"
`
			if err = os.WriteFile(filepath.Join(root, "gh"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "outputs")
			command := exec.CommandContext(t.Context(), "bash", "-c", resolver.Steps[0].Run)
			token := ""
			if test.credential {
				token = "synthetic-source-read"
			}
			command.Env = []string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"), "GH_TOKEN=" + token, "SOURCE_FIXTURE=" + root, "SOURCE_API_OK=" + map[bool]string{true: "true", false: "false"}[test.apiOK], "GITHUB_OUTPUT=" + output}
			log, runErr := command.CombinedOutput()
			raw, readErr := os.ReadFile(output)
			if test.pass {
				if runErr != nil || readErr != nil || string(raw) != "nvke_commit="+nightlyNVKESHA+"\n" {
					t.Fatalf("exact source not frozen: %v %v %s %s", runErr, readErr, raw, log)
				}
			} else if runErr == nil || !os.IsNotExist(readErr) {
				t.Fatalf("invalid source emitted outputs: %v %v %s", runErr, readErr, raw)
			}
			calls, callErr := os.ReadFile(filepath.Join(root, "calls"))
			if test.credential {
				if callErr != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 1 {
					t.Fatal("source resolution was retried or skipped", callErr)
				}
			} else if !os.IsNotExist(callErr) {
				t.Fatal("API called before credential admission")
			}
		})
	}
}
