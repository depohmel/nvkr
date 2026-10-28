// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

func hostedKVMTestPlan(t *testing.T, buildOnly bool) *Plan {
	t.Helper()
	base := arcTestPlan(t, "linux/amd64")
	if buildOnly {
		base = githubDiskBuildPlan(t, "linux/amd64", "none")
	}
	env := base.Environment.Clone()
	env.GitHubARC, env.GitHubHostedKVM = "", "native-amd64-v1"
	env.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04"}
	env.ExecutionClasses = env.ExecutionClasses[:1]
	for i := range env.ExecutionClasses {
		env.ExecutionClasses[i].RunsOn = "ubuntu-24.04"
	}
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, env, stack, base.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	return plan
}

func TestGitHubHostedDiskCapacityRejectsBeforeTransfer(t *testing.T) {
	stage := githubStage{Nodes: []Node{{ExecutionBinding: &execution.Binding{Class: execution.Class{
		Resources: execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 32768, EphemeralStorageMiB: 153600},
	}}}}}
	for _, test := range []struct {
		name                 string
		cpu, memory, storage uint64
		fail                 bool
	}{
		{"sufficient coarse facts", 8, 32768, 153600, false},
		{"standard hosted CPU", 4, 32768, 153600, true},
		{"standard hosted memory", 8, 16000, 153600, true},
		{"insufficient scratch", 8, 32768, 90000, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			memory := filepath.Join(root, "meminfo")
			for path, contents := range map[string]string{
				memory:                       fmt.Sprintf("MemAvailable: %d kB\n", test.memory*1024),
				filepath.Join(root, "nproc"): fmt.Sprintf("#!/bin/sh\nprintf '%d\\n'\n", test.cpu),
				filepath.Join(root, "df"):    fmt.Sprintf("#!/bin/sh\nprintf 'Filesystem 1048576-blocks Used Available Capacity Mounted on\\nfixture 200000 0 %d 0%%%% /\\n'\n", test.storage),
			} {
				if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(githubHostedDiskCapacityCheck(stage), "/proc/meminfo", shellQuote(memory)) + "printf transferred\\n\n"
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if !test.fail {
				if err != nil || !strings.Contains(string(output), "transferred") {
					t.Fatalf("coarse admission: %v %s", err, output)
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeEngineCapability, "fixture")) ||
				strings.Contains(string(output), "transferred") || !strings.Contains(string(output), "NVKR-E-ENGINE-CAPABILITY") {
				t.Fatalf("insufficient capacity did not stop before transfer: %v %s", err, output)
			}
		})
	}
}

func TestGitHubHostedKVMNativeDiskGeneration(t *testing.T) {
	for _, buildOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "publication", true: "build-only"}[buildOnly], func(t *testing.T) {
			plan := hostedKVMTestPlan(t, buildOnly)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatal(err)
			}
			workflow := string(expandedGitHubWorkflow(t, bundle))
			if strings.Contains(workflow, "arc-amd64-kvm") || strings.Contains(workflow, "'nvkr-arc'") {
				t.Fatal("hosted policy inherited the private ARC route")
			}
			for _, required := range []string{"nvkr-release", "github-hosted", "factory github run-disk"} {
				if !strings.Contains(workflow, required) {
					t.Fatalf("hosted workflow omits %q", required)
				}
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			for _, stage := range stages {
				if stage.DiskOutput == "" {
					continue
				}
				job := document.Jobs[stage.ID]
				if job.RunsOn != "ubuntu-24.04" || job.Environment != "nvkr-release" {
					t.Fatal("foreign disk runner or deployment environment")
				}
				if job.Permissions["id-token"] != "" || job.Permissions["packages"] == "write" || job.Env["NVKR_CREDENTIAL"] != "" {
					t.Fatal("hosted disk job received publication or signing authority")
				}
				if len(job.Steps) < 3 || job.Steps[0].Name != githubPlatformCheckName ||
					job.Steps[1].Name != githubDiskAccessCheck(plan, stage).Name || job.Steps[2].Uses != testActionRefs(t, plan).Checkout {
					t.Fatal("hosted disk checks did not precede checkout and transfer")
				}
				if !strings.Contains(job.Steps[1].Run, "CPU %s/8000 milliCPU") {
					t.Fatal("disk preflight lost its exact selected class resource requirement")
				}
			}
		})
	}
}

func TestGitHubHostedKVMRejectsProviderBeforeDeviceAccess(t *testing.T) {
	plan := hostedKVMTestPlan(t, false)
	check := githubDiskAccessCheck(plan, githubStage{})
	// A rejected allocation must fail before even inspecting the device path.
	check.Run = strings.ReplaceAll(check.Run, "/dev/kvm", "$(printf device-inspected >&2)/dev/kvm")
	for _, provider := range []string{"", "self-hosted", "unknown"} {
		t.Run(provider, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), "bash", "-c", check.Run)
			command.Env = append(os.Environ(), "RUNNER_ENVIRONMENT="+provider)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeEngineCapability, "fixture")) ||
				strings.Contains(string(output), "device-inspected") || !strings.Contains(string(output), "requires an actual GitHub-hosted AMD64 runner") {
				t.Fatalf("provider gate did not precede device work: %v %s", err, output)
			}
		})
	}
}

func TestGitHubHostedKVMRejectsUnsupportedRunnerBeforeOutput(t *testing.T) {
	for _, runner := range []string{"arc-amd64-kvm", "ubuntu-24.04-8cpu", "ubuntu-24.04-arm", "self-hosted"} {
		t.Run(runner, func(t *testing.T) {
			plan := hostedKVMTestPlan(t, false)
			for i := range plan.Environment.ExecutionClasses {
				plan.Environment.ExecutionClasses[i].RunsOn = runner
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if bundle != nil {
				t.Fatal("unsupported hosted runner emitted a partial bundle")
			}
		})
	}
}
