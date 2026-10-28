// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type nightlyHostedWorkflow struct {
	On struct {
		Schedule []struct{ Cron string } `yaml:"schedule"`
	} `yaml:"on"`
	Jobs map[string]struct {
		If          string                `yaml:"if"`
		RunsOn      string                `yaml:"runs-on"`
		Environment string                `yaml:"environment"`
		Permissions map[string]string     `yaml:"permissions"`
		Env         map[string]string     `yaml:"env"`
		Steps       []nightlyWorkflowStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func nightlyHostedWorkflowFixture(t *testing.T) nightlyHostedWorkflow {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow nightlyHostedWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func nightlyHostedScript(t *testing.T, workflow nightlyHostedWorkflow, job, id string) string {
	t.Helper()
	for _, step := range workflow.Jobs[job].Steps {
		if step.ID == id && step.Run != "" {
			return step.Run
		}
	}
	t.Fatalf("missing authored %s/%s script", job, id)
	return ""
}

func nightlyHostedProfiles(t *testing.T) []NightlyProfile {
	t.Helper()
	raw, err := os.ReadFile("../../inputs/testing/nightly-profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var profiles []NightlyProfile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		t.Fatal(err)
	}
	return profiles
}

func nightlyHostedOutputMatrix(t *testing.T, output string) []NightlyProfile {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if raw, ok := strings.CutPrefix(line, "matrix="); ok {
			var matrix struct {
				Include []NightlyProfile `json:"include"`
			}
			if err := json.Unmarshal([]byte(raw), &matrix); err != nil {
				t.Fatal(err)
			}
			return matrix.Include
		}
	}
	t.Fatalf("missing matrix output: %q", output)
	return nil
}

func TestNightlyHostedWorkflowMaintainedSelection(t *testing.T) {
	workflow := nightlyHostedWorkflowFixture(t)
	if len(workflow.On.Schedule) != 1 || workflow.On.Schedule[0].Cron != "17 3 * * *" {
		t.Fatal("daily trigger changed")
	}
	profiles := nightlyHostedProfiles(t)
	if len(profiles) != 2 {
		t.Fatal("maintained nightly must contain both native NVIDIA targets")
	}
	script := nightlyHostedScript(t, workflow, "select", "profiles")
	for _, test := range []struct {
		name, requested string
		change          func([]NightlyProfile) []NightlyProfile
		count           int
	}{
		{"both architectures", "all", nil, 2},
		{"hosted AMD64", "nvke-nightly-amd64", nil, 1},
		{"existing ARM64", "nvke-nightly-arm64", nil, 1},
		{"unknown requested profile", "foreign", nil, 0},
		{"upstream OVN rejected", "all", func(p []NightlyProfile) []NightlyProfile { p[0].Network = "upstream-ovn"; return p }, 0},
		{"cross target rejected", "all", func(p []NightlyProfile) []NightlyProfile { p[0].RuntimeTarget = "native-arm64-vm"; return p }, 0},
		{"mutable harness rejected", "all", func(p []NightlyProfile) []NightlyProfile { p[0].HarnessCommit = "main"; return p }, 0},
		{"duplicate profile rejected", "all", func(p []NightlyProfile) []NightlyProfile { return append(p, p[0]) }, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := append([]NightlyProfile(nil), profiles...)
			if test.change != nil {
				selected = test.change(selected)
			}
			root := t.TempDir()
			nightlyWorkflowWrite(t, root, "inputs/testing/nightly-profiles.json", selected)
			output, err := nightlyWorkflowRun(t, root, script, map[string]string{"PROFILE": test.requested, "NVKE_COMMIT": strings.Repeat("d", 40), "RUNNER_TEMP": root})
			if test.count == 0 {
				if err == nil || output != "" {
					t.Fatalf("invalid selection emitted matrix: %q, %v", output, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			matrix := nightlyHostedOutputMatrix(t, output)
			if len(matrix) != test.count {
				t.Fatalf("selected %d profiles, want %d", len(matrix), test.count)
			}
			for _, profile := range matrix {
				if profile.Name != "nvke-nightly-"+strings.TrimSuffix(strings.TrimPrefix(profile.RuntimeTarget, "native-"), "-vm") ||
					test.requested != "all" && profile.Name != test.requested {
					t.Fatalf("matrix lost profile/architecture binding: %+v", profile)
				}
			}
		})
	}
}

func TestNightlyHostedWorkflowRuntimeSelectionRequiresExactCycleLocators(t *testing.T) {
	workflow := nightlyHostedWorkflowFixture(t)
	script := nightlyHostedScript(t, workflow, "select-runtime", "completed")
	profiles := nightlyHostedProfiles(t)
	for _, test := range []struct {
		name     string
		change   func([]NightlyProfile) []NightlyProfile
		badFacts bool
		missing  bool
		none     bool
		want     int
		pass     bool
	}{
		{name: "both exact locators", want: 2, pass: true},
		{name: "only AMD64 completed", missing: true, want: 1, pass: true},
		{name: "no producer completed", none: true, pass: true},
		{name: "cross target", change: func(p []NightlyProfile) []NightlyProfile { p[0].RuntimeTarget = "native-arm64-vm"; return p }},
		{name: "unsupported target", change: func(p []NightlyProfile) []NightlyProfile { p[0].RuntimeTarget = "hosted-arm64"; return p }},
		{name: "duplicate profile", change: func(p []NightlyProfile) []NightlyProfile { return append(p, p[0]) }},
		{name: "locator belongs to other source", badFacts: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := append([]NightlyProfile(nil), profiles...)
			if test.change != nil {
				selected = test.change(selected)
			}
			root := t.TempDir()
			sha := strings.Repeat("a", 40)
			for _, profile := range profiles {
				artifact := "nightly-build-" + profile.Name + "-123-2"
				facts := map[string]any{"total_count": 0, "artifacts": []any{}}
				if !test.none && (!test.missing || profile.RuntimeTarget == "native-amd64-vm") {
					artifactSHA := sha
					if test.badFacts {
						artifactSHA = strings.Repeat("b", 40)
					}
					facts = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"name": artifact, "expired": false, "workflow_run": map[string]any{"head_sha": artifactSHA}}}}
				}
				nightlyWorkflowWrite(t, root, artifact+".json", facts)
			}
			entries := make([]map[string]string, len(selected))
			for i, profile := range selected {
				entries[i] = map[string]string{"name": profile.Name, "runtime_target": profile.RuntimeTarget, "nvke_commit": strings.Repeat("d", 40)}
			}
			raw, err := json.Marshal(map[string]any{"include": entries})
			if err != nil {
				t.Fatal(err)
			}
			// Substitute only the provider response; jq admission and matrix production
			// are the actual authored script, including rejection before API access.
			provider := `gh() { local endpoint="${!#}" name; name="${endpoint#*?name=}"; name="${name%&per_page=100}"; cat "$name.json"; }
`
			output, err := nightlyWorkflowRun(t, root, provider+script, map[string]string{
				"MATRIX": string(raw), "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_REPOSITORY": "dsx-ai-factory/nvkr", "GITHUB_SHA": sha,
			})
			if !test.pass {
				if err == nil || output != "" {
					t.Fatalf("invalid runtime matrix emitted outputs: %q, %v", output, err)
				}
				return
			}
			availability := "available=true\n"
			if test.want == 0 {
				availability = "available=false\n"
			}
			if err != nil || len(nightlyHostedOutputMatrix(t, output)) != test.want || !strings.Contains(output, availability) {
				t.Fatalf("runtime matrix: %q, %v", output, err)
			}
		})
	}
}

func TestNightlyHostedWorkflowSuitePinMatchesGoOwner(t *testing.T) {
	script := nightlyWorkflowScript(t, "suite")
	for _, arch := range []string{"amd64", "arm64"} {
		for _, stale := range []bool{false, true} {
			t.Run(arch+map[bool]string{false: "/same revision", true: "/stale revision"}[stale], func(t *testing.T) {
				profile, build := nightlyIdentityFixture()
				profile.RuntimeTarget = "native-" + arch + "-vm"
				plan, _ := nightlyRuntimeCompiledFixture(t, profile, build, build.NVKECommit)
				if stale {
					plan.Builds[0].ResolvedComponents[0].Commit = strings.Repeat("e", 40)
				}
				expected, expectedErr := nightlyPlanHarnessCommit(plan, profile)
				root := t.TempDir()
				nightlyWorkflowWrite(t, root, "inputs/testing/nightly-profiles.json", []NightlyProfile{profile})
				nightlyWorkflowWrite(t, root, ".nightly-private/runtime-inputs/plan.json", plan)
				output, err := nightlyWorkflowRun(t, root, script, map[string]string{"PROFILE": profile.Name, "NVKE_COMMIT": build.NVKECommit, "RUNTIME_PLAN": "true"})
				if stale {
					if expectedErr == nil || err == nil || output != "" {
						t.Fatalf("stale source accepted: owner=%v script=%v output=%q", expectedErr, err, output)
					}
				} else if expectedErr != nil || err != nil || output != "commit="+expected+"\n" {
					t.Fatalf("suite differs: %q %v %v", output, err, expectedErr)
				}
			})
		}
	}
}

func TestNightlyHostedWorkflowKeepsNativeVerifierAndPublicMetadataBoundary(t *testing.T) {
	workflow := nightlyHostedWorkflowFixture(t)
	runtime := workflow.Jobs["runtime-test"]
	for _, test := range []struct{ name, actual, expected string }{
		{"runner", runtime.RunsOn, "${{ matrix.runtime_target == 'native-amd64-vm' && 'ubuntu-24.04' || 'arc-arm64-kvm' }}"},
		{"protection", runtime.Environment, "${{ matrix.runtime_target == 'native-amd64-vm' && 'nvkr-release' || 'nvkr-arc' }}"},
		{"native architecture", runtime.Env["NATIVE_ARCH"], "${{ matrix.runtime_target == 'native-amd64-vm' && 'amd64' || 'arm64' }}"},
		{"verifier digest", runtime.Env["COSIGN_DIGEST"], "${{ matrix.runtime_target == 'native-amd64-vm' && '4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71' || 'c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a' }}"},
	} {
		if test.actual != test.expected {
			t.Fatalf("%s binding changed: %q", test.name, test.actual)
		}
	}
	if !reflect.DeepEqual(runtime.Permissions, map[string]string{"contents": "read", "actions": "read"}) {
		t.Fatal("runtime acquired publication or OIDC permissions")
	}
	allowed := map[string]string{
		"select":       "${{ runner.temp }}/nightly-selection.json",
		"build":        "${{ runner.temp }}/nightly-build-${{ matrix.name }}/build.json",
		"runtime-test": "${{ runner.temp }}/nightly-reports-${{ matrix.name }}/cycle.json",
	}
	verifierFound := false
	for job, definition := range workflow.Jobs {
		for _, step := range definition.Steps {
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && (allowed[job] == "" || step.With["path"] != allowed[job]) {
				t.Fatalf("public upload escaped exact metadata allowlist: %s/%s %q", job, step.Name, step.With["path"])
			}
			if step.Name == "Fetch and check the independent native verifier" {
				verifierFound = true
				if !strings.Contains(step.Run, "cosign-linux-$NATIVE_ARCH") || !strings.Contains(step.Run, `"$COSIGN_DIGEST" "$destination/cosign" | sha256sum -c -`) {
					t.Fatal("native verifier download lost its exact-byte check")
				}
			}
		}
	}
	if !verifierFound {
		t.Fatal("missing independent native verifier")
	}
}
