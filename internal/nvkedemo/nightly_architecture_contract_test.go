// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"os"
	"path/filepath"
	"testing"

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
		{"ARM64 upstream is producer-only diagnostics", "native-arm64-vm", "upstream-ovn", "", e.CodeEngineCapability},
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
