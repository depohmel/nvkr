// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestNightlyNativeArchitectureRefusesCrossExecution(t *testing.T) {
	for _, test := range []struct {
		name, target, os, host string
		pass                   bool
	}{
		{"ARM64 native", "native-arm64-vm", "linux", "arm64", true},
		{"AMD64 native", "native-amd64-vm", "linux", "amd64", true},
		{"ARM64 on AMD64", "native-arm64-vm", "linux", "amd64", false},
		{"AMD64 on ARM64", "native-amd64-vm", "linux", "arm64", false},
		{"unsupported host", "native-amd64-vm", "darwin", "amd64", false},
		{"emulation target", "tcg-amd64-vm", "linux", "amd64", false},
		{"missing target", "", "linux", "amd64", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			arch, err := nightlyArchitecture(test.target)
			if err == nil {
				err = nightlyNativeHost(test.os, test.host, arch)
			}
			if test.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, e.CodeEngineCapability)
			}
		})
	}
}

func TestNightlyPlanArchitectureRefusesMismatchedOutputs(t *testing.T) {
	for _, test := range []struct {
		name      string
		platforms []string
		code      e.Code
	}{
		{"native closure", []string{"linux/amd64", "linux/amd64"}, ""},
		{"foreign image", []string{"linux/amd64", "linux/arm64"}, e.CodeEngineCapability},
		{"foreign disk", []string{"linux/arm64"}, e.CodeEngineCapability},
		{"unspecified platform", []string{""}, e.CodeEngineCapability},
		{"empty closure", nil, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := &factory.Plan{}
			for _, platform := range test.platforms {
				plan.Builds = append(plan.Builds, factory.BuildPlan{Name: "output", Platform: platform})
			}
			err := nightlyPlanArchitecture(plan, NightlyProfile{RuntimeTarget: "native-amd64-vm"})
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, test.code)
			}
		})
	}
}

func TestNightlyProfileAdmissionPreservesSupportedArchitectureNetworks(t *testing.T) {
	const retainedHarness = "b4f90e2c4fc4ed3d07efc18a22066a7e0b9b99c8"
	for _, test := range []struct {
		name, target, network, harness string
		code                           e.Code
	}{
		{"ARM64 NVIDIA cycle source", "native-arm64-vm", "ovn", "", ""},
		{"ARM64 upstream cycle source", "native-arm64-vm", "upstream-ovn", "", ""},
		{"AMD64 NVIDIA cycle source", "native-amd64-vm", "ovn", "", ""},
		{"ARM64 old harness override refused", "native-arm64-vm", "ovn", retainedHarness, e.CodeUnpinnedInput},
		{"AMD64 old harness override refused", "native-amd64-vm", "ovn", retainedHarness, e.CodeUnpinnedInput},
		{"AMD64 upstream endpoint unsupported", "native-amd64-vm", "upstream-ovn", "", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, build := nightlyIdentityFixture()
			profile.RuntimeTarget, profile.Network, profile.HarnessCommit = test.target, test.network, test.harness
			err := validateNightlyProfile(profile)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, test.code)
				build.Profile = profile
				workspace := filepath.Join(t.TempDir(), "not-created")
				// A nil Plan/capture would return CodeUsage if handoff reached
				// artifact preparation. Unsupported profiles must fail first.
				inputs, handoffErr := newNightlyInputsWithCapture(t.Context(), profile, build, nil, lifecycle.Definition{}, lifecycle.Environment{}, workspace, "example/nvkr", "123", build.SourceCommit, "100", "2", nil)
				assertNightlyCode(t, handoffErr, test.code)
				if inputs != nil {
					t.Fatal("unsupported profile returned nightly inputs")
				}
				if _, err := os.Stat(workspace); !os.IsNotExist(err) {
					t.Fatal("unsupported profile performed filesystem preparation", err)
				}
			}
		})
	}
}

func TestNightlyProfilesRefuseForeignAuthenticatedImagePlatform(t *testing.T) {
	publication, release := nightlyProfilesPublicationFixture(t, true)
	defer publication.Close()
	destination, err := nightlyProfiles(t.Context(), publication, release, "controller-profiles", t.TempDir(), "amd64")
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	if destination != "" {
		t.Fatal("foreign profiles image returned staged assets")
	}
}

func TestNightlyRuntimeImagesRefuseCrossArchitectureSubjects(t *testing.T) {
	for _, scenario := range []string{"native", "foreign logical output", "foreign authenticated architecture"} {
		t.Run(scenario, func(t *testing.T) {
			plan, _ := runtimeValueInputs(t)
			for i := range plan.Builds {
				plan.Builds[i].Name = strings.ReplaceAll(plan.Builds[i].Name, "arm64", "amd64")
			}
			release := nightlyRuntimeRelease()
			for i := range release.Artifacts {
				release.Artifacts[i].Name = strings.ReplaceAll(release.Artifacts[i].Name, "arm64", "amd64")
				release.Artifacts[i].Arch = "amd64"
			}
			code := e.CodeVerifyFailed
			if scenario == "foreign logical output" {
				release.Artifacts[0].Name = strings.ReplaceAll(release.Artifacts[0].Name, "amd64", "arm64")
			}
			if scenario == "foreign authenticated architecture" {
				release.Artifacts[0].Arch = "arm64"
				code = e.CodeEngineCapability
			}
			values, err := nightlyRuntimeValues(plan, release, "amd64")
			if scenario == "native" {
				if err != nil || len(values) == 0 {
					t.Fatalf("native runtime image projection failed: %v", err)
				}
			} else {
				assertNightlyCode(t, err, code)
				if values != nil {
					t.Fatal("foreign runtime image produced values")
				}
			}
		})
	}
}

func TestNightlyAMD64ProfilesPreserveNativeDiskAndBootBindings(t *testing.T) {
	for _, scenario := range []string{"exact", "foreign disk architecture", "foreign metadata architecture", "foreign profile name", "foreign disk endpoint", "foreign output name"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			profile := NightlyProfile{RuntimeTarget: "native-amd64-vm", Network: "ovn", DiskOutput: "nvke-node-image-amd64", OSProfile: "nvke-local-26-04-amd64-plain"}
			metadata, ipxe, userData := tenantReleaseFixtureBytes()
			metadata = []byte(strings.ReplaceAll(string(metadata), "arm64", "amd64"))
			ipxe = []byte(strings.ReplaceAll(string(ipxe), "arm64", "amd64"))
			digest := "sha256:" + strings.Repeat("1", 64)
			image := lifecycle.Artifact{Name: profile.DiskOutput, Kind: "disk-image", OS: "linux", Arch: "amd64", Digest: digest, Disk: &delivery.DiskOutput{PayloadDigest: digest, Size: 123, Format: "qcow2", Encryption: "none"}}
			switch scenario {
			case "foreign disk architecture":
				image.Arch = "arm64"
			case "foreign metadata architecture":
				metadata = []byte(strings.ReplaceAll(string(metadata), "architecture: amd64", "architecture: arm64"))
			case "foreign profile name":
				metadata = []byte(strings.ReplaceAll(string(metadata), profile.OSProfile, TenantProfile))
			case "foreign disk endpoint":
				ipxe = []byte(strings.ReplaceAll(string(ipxe), profile.DiskOutput, DiskOutputName))
			case "foreign output name":
				image.Name = DiskOutputName
			}
			base := filepath.Join(root, "profiles/osProfiles")
			if err := os.MkdirAll(filepath.Join(base, profile.OSProfile), 0o700); err != nil {
				t.Fatal(err)
			}
			for path, data := range map[string][]byte{"metadata.yaml": metadata, profile.OSProfile + "/ipxeScript": ipxe, profile.OSProfile + "/userData.tmpl": userData} {
				if err := privateWrite(filepath.Join(base, path), data); err != nil {
					t.Fatal(err)
				}
			}
			destination := filepath.Join(root, "resources")
			err := nightlyProfileResources(t.Context(), profile, lifecycle.Release{Artifacts: []lifecycle.Artifact{image}}, root, destination)
			if scenario != "exact" {
				assertNightlyCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("mismatched architecture produced release resources", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			machine, err := os.ReadFile(filepath.Join(destination, "nvke-nico-vms-24.04-amd64-fde-machineimage.yaml"))
			var document nightlyReleaseResource
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(machine, &document); err != nil {
				t.Fatal(err)
			}
			if document.Spec.(map[string]any)["architecture"] != "amd64" || document.Metadata.Annotations["nvkr.dev/diagnostic-disk-digest"] != digest || document.Metadata.Annotations["nvkr.dev/diagnostic-source-profile"] != profile.OSProfile || document.Metadata.Annotations["nvkr.dev/diagnostic-test-alias"] != "stock-nvke-name-only;no-fde-coverage" {
				t.Fatalf("native machine image lost architecture or digest binding: %s %v", machine, err)
			}
		})
	}
}
