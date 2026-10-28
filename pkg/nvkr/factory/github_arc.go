// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Prepare the operator-trusted ARC device before downloading disk-stage inputs.
// This fixed bootstrap grants no execution authority: the helper still admits
// native capacity and holds an API-validated KVM descriptor before work.
// Only disk producers need it; a build-only finalizer on ARC does not use KVM.
func githubKVMAccessCheck() githubStep {
	return githubStep{
		Name:           "Prepare KVM access before transfer",
		TimeoutMinutes: 1,
		Run: fmt.Sprintf(`set -eu
if [ -L /dev/kvm ] || [ ! -c /dev/kvm ]; then
  printf 'NVKR-E-ENGINE-CAPABILITY: native disk runner lacks /dev/kvm\n' >&2
  exit %[1]d
fi
if ! (exec 3<>/dev/kvm && exec 3>&-) 2>/dev/null; then
  printf 'Preparing /dev/kvm access on the operator-trusted ARC runner\n'
  if ! /usr/bin/timeout --kill-after=5s 15s /usr/bin/sudo -n -- /usr/bin/chmod 0666 /dev/kvm; then
    printf 'NVKR-E-ENGINE-CAPABILITY: unable to prepare /dev/kvm access\n' >&2
    exit %[1]d
  fi
fi
if ! (exec 3<>/dev/kvm && exec 3>&-); then
  printf 'NVKR-E-ENGINE-CAPABILITY: runner still cannot open /dev/kvm read/write\n' >&2
  id >&2 || true
  stat -c 'uid=%%u gid=%%g mode=%%a' /dev/kvm >&2 || true
  exit %[1]d
fi
stat -c 'KVM ready: uid=%%u gid=%%g mode=%%a' /dev/kvm || true`, e.ExitCode(e.New(e.CodeEngineCapability, "KVM access unavailable"))),
	}
}

// The explicit policy trusts these operator-managed, single-job ARC pools.
// Runtime admission still measures native capacity and holds KVM; a label alone
// never substitutes for that admission or for qualified provider evidence.
func githubARCRunner(platform string) string {
	switch platform {
	case "linux/amd64":
		return "arc-amd64-kvm"
	case "linux/arm64":
		return "arc-arm64-kvm"
	default:
		return ""
	}
}

// The hosted route is separate from operator-managed ARC. Unknown policies or
// platforms have no runner; a label never grants capacity or KVM authority.
func githubNativeDiskRunner(plan *Plan, platform string) string {
	if plan == nil {
		return ""
	}
	if plan.Environment.GitHubARC == "native-kvm-v1" && plan.Environment.GitHubHostedKVM == "" {
		return githubARCRunner(platform)
	}
	if plan.Environment.GitHubHostedKVM == "native-amd64-v1" && plan.Environment.GitHubARC == "" && platform == "linux/amd64" {
		return "ubuntu-24.04"
	}
	return ""
}

func githubNativeDiskPolicy(plan *Plan) bool {
	return githubNativeDiskRunner(plan, "linux/amd64") != ""
}

func githubDiskAccessCheck(plan *Plan, stage githubStage) githubStep {
	check := githubKVMAccessCheck()
	if plan.Environment.GitHubHostedKVM == "native-amd64-v1" {
		check.Name = "Admit hosted AMD64 provider, disk capacity and KVM access"
		check.Run = fmt.Sprintf(`set -eu
if [ "${RUNNER_ENVIRONMENT:-}" != github-hosted ] || [ "$(uname -s)/$(uname -m)" != Linux/x86_64 ]; then
  printf 'NVKR-E-ENGINE-CAPABILITY: native-amd64-v1 requires an actual GitHub-hosted AMD64 runner\n' >&2
  exit %d
fi
`, e.ExitCode(e.New(e.CodeEngineCapability, "hosted runner unavailable"))) + githubHostedDiskCapacityCheck(stage) + strings.ReplaceAll(check.Run, "operator-trusted ARC", "GitHub-hosted")
	}
	return check
}

// This early coarse check avoids known-insufficient hosted allocations downloading
// large private inputs. It does not replace the execution owner's cgroup-aware
// admission or authenticate any provider allocation.
func githubHostedDiskCapacityCheck(stage githubStage) string {
	var want execution.ResourceCapacity
	for _, node := range stage.Nodes {
		if node.ExecutionBinding != nil {
			class := node.ExecutionBinding.Class.Resources
			want.CPUMillis = max(want.CPUMillis, class.CPUMillis)
			want.MemoryMiB = max(want.MemoryMiB, class.MemoryMiB)
			want.EphemeralStorageMiB = max(want.EphemeralStorageMiB, class.EphemeralStorageMiB)
		}
	}
	return fmt.Sprintf(`if ! cpu_count=$(nproc) ||
   ! memory_mib=$(awk '$1 == "MemAvailable:" { print int($2 / 1024); found=1 } END { if (!found) exit 1 }' /proc/meminfo) ||
   ! disk_report=$(df -Pm .) ||
   ! storage_mib=$(printf '%%s\n' "$disk_report" | awk 'NR == 2 { print $4; found=1 } END { if (!found) exit 1 }'); then
  printf 'NVKR-E-ENGINE-CAPABILITY: cannot observe hosted disk capacity\n' >&2
  exit %[4]d
fi
case "$cpu_count:$memory_mib:$storage_mib" in
  *[!0-9:]*|:*|*::*|*:) printf 'NVKR-E-ENGINE-CAPABILITY: invalid hosted disk capacity observation\n' >&2; exit %[4]d ;;
esac
cpu_millis=$((cpu_count * 1000))
printf 'Hosted disk preflight: CPU %%s/%[1]d milliCPU, memory %%s/%[2]d MiB, scratch %%s/%[3]d MiB\n' "$cpu_millis" "$memory_mib" "$storage_mib"
if ! [ "$cpu_millis" -ge %[1]d ] || ! [ "$memory_mib" -ge %[2]d ] || ! [ "$storage_mib" -ge %[3]d ]; then
  printf 'NVKR-E-ENGINE-CAPABILITY: hosted disk class capacity unavailable before transfer\n' >&2
  exit %[4]d
fi
`, want.CPUMillis, want.MemoryMiB, want.EphemeralStorageMiB, e.ExitCode(e.New(e.CodeEngineCapability, "hosted disk capacity unavailable")))
}

func githubBootstrapRunner(runner string) string {
	switch runner {
	case "arc-amd64-kvm":
		return "ubuntu-24.04"
	case "arc-arm64-kvm":
		return "ubuntu-24.04-arm"
	default:
		return runner
	}
}

func admitGitHubRequirements(plan *Plan, adapter string) error {
	if PerOutputPublication(plan) {
		for _, name := range sortedKeys(plan.Targets) {
			target := plan.Targets[name]
			if plan.Environment.GitHubDevelopment {
				if target.CredentialRef != development.WriteCredentialRef {
					return e.New(e.CodeEngineCapability, "%s development target %q requires its fixed scoped writer credential", adapter, name)
				}
			} else if target.CredentialRef != "" && target.CredentialRef != "github:GITHUB_TOKEN" && target.CredentialRef != ProductionRegistryWriteCredential {
				return e.New(e.CodeEngineCapability, "%s per-output target %q requires the job-scoped GITHUB_TOKEN or fixed production read/write credentials", adapter, name)
			}
		}
	}
	if plan.Environment.GitHubARC == "" && plan.Environment.GitHubHostedKVM == "" {
		return rejectUnsupportedPlanRequirements(plan, adapter)
	}
	deny := func() error {
		if plan.Environment.GitHubHostedKVM != "" {
			return e.New(e.CodeEngineCapability, "%s requires the separate native-amd64-v1 GitHub-hosted disk route", adapter)
		}
		return e.New(e.CodeEngineCapability, "%s requires the native-kvm-v1 ARC disk route", adapter)
	}
	if !githubNativeDiskPolicy(plan) ||
		(plan.Qualification != TierPipelineAttested && !GitHubDiskBuildOnly(plan)) ||
		plan.Environment.RunsOn != "" ||
		plan.Environment.RuntimeBinary() != "docker" || plan.Environment.Runtime.RuntimeImage != "" ||
		len(plan.Environment.Runtime.Services) != 0 || len(plan.Environment.Runtime.Setup) != 0 ||
		len(plan.Environment.Runtime.Variables) != 0 {
		return deny()
	}
	disks := 0
	for _, build := range plan.Builds {
		if plan.Environment.GitHubHostedKVM != "" && build.Platform != "linux/amd64" {
			return deny()
		}
		for _, component := range build.ResolvedComponents {
			if component.BuildRequirements != nil && component.BuilderType != ir.BuilderVMImage {
				return deny()
			}
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.Requirements == nil && instruction.BuilderType != ir.BuilderVMImage {
				continue
			}
			if instruction.Kind != ir.KindBuild || instruction.BuilderType != ir.BuilderVMImage ||
				instruction.Packer == nil || len(instruction.Command) != 0 || instruction.Disk == nil ||
				instruction.Disk.Format != "qcow2" ||
				instruction.Packer.AcquisitionArchive == "" || instruction.Requirements == nil || instruction.ExecutionBinding == nil {
				return deny()
			}
			if err := pipeline.ValidateVMImagePackerDiskExecution(instruction); err != nil {
				return err
			}
			if !GitHubDiskBuildOnly(plan) {
				if err := admitDiskPublicationEncryption(plan, instruction); err != nil {
					return err
				}
			}
			binding := instruction.ExecutionBinding
			platform := string(binding.Class.HostPlatform)
			if platform != build.Platform || platform != instruction.ToolchainPlatform ||
				binding.Class.RunsOn != githubNativeDiskRunner(plan, platform) || githubNativeDiskRunner(plan, platform) == "" ||
				len(instruction.Requirements.Capabilities) != 1 ||
				instruction.Requirements.Capabilities[0] != (execution.CapabilityRequirement{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}) {
				return deny()
			}
			// Production inputs use transportable immutable OCI references, never
			// an inherited local image-store ID.
			if !strings.Contains(instruction.Toolchain, "@sha256:") {
				return e.New(e.CodeUnpinnedInput, "ARC disk toolchain requires a published native OCI digest")
			}
			if githubDiskInspectionRequired(plan) {
				_, profile, err := diskPublicationInspectionProfile(build, plan.Environment)
				if err != nil {
					return err
				}
				if err := requireProductionDiskScratch(profile); err != nil {
					return err
				}
				if err := validateDiskScannerSelection(plan, platform, profile.ScannerDigest); err != nil {
					return err
				}
			}
			disks++
		}
	}
	if disks == 0 {
		return deny()
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == PackageDiskImage && !GitHubDiskBuildOnly(plan) {
			if _, err := admitDiskEvidenceBuild(plan, output); err != nil {
				return err
			}
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.Requirements != nil {
			return deny()
		}
	}
	return nil
}

func githubDiskOutput(plan *Plan, node Node) string {
	if !githubNativeDiskPolicy(plan) {
		return ""
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == PackageDiskImage && PerOutputPublication(plan) &&
			node.ID == "package/"+outputPublicationPackage(output.Name) &&
			node.Kind == NodePackage && node.PackageKind == PackageOCI && node.Network == ir.NetworkNone {
			return output.Name
		}
		if (output.Name != node.BuildTarget && node.ID != "package/"+output.Name) || output.Kind != PackageDiskImage {
			continue
		}
		if node.IRNode != "" && node.Kind != NodeFetch || node.Kind == NodeVerify || node.Kind == NodeSBOM || node.Kind == NodePackage {
			return output.Name
		}
	}
	return ""
}

func projectARCNode(plan *Plan, node Node) Node {
	// Source vendoring runs native tools, but its credentials must remain on
	// generic hosted runners outside the ARC disk group.
	if plan.Environment.GitHubARC == "native-kvm-v1" && node.Kind == NodeFetch {
		if runner := githubARCRunner(node.Platform); runner != "" {
			node.RunsOn = githubBootstrapRunner(runner)
		}
	}
	output := githubDiskOutput(plan, node)
	if output != "" {
		_, artifacts := planIndex(plan)
		var transported []string
		for _, id := range node.Outputs {
			if _, raw := diskPublicationRequirement(plan, artifacts[id]); !raw {
				transported = append(transported, id)
			}
		}
		node.Outputs = transported
	}
	if output == "" {
		return node
	}
	for _, build := range plan.Builds {
		if build.Name != output {
			continue
		}
		for _, root := range build.Graph.Nodes {
			if root.BuilderType == ir.BuilderVMImage && root.ExecutionBinding != nil {
				node.RunsOn = root.ExecutionBinding.Class.RunsOn
				node.Platform = string(root.ExecutionBinding.Class.HostPlatform)
				return node
			}
		}
	}
	return node
}
