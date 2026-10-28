// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

func TestGitHubHostedKVMAdmission(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Environment)
		fail bool
	}{
		{"hosted AMD64", func(*Environment) {}, false},
		{"unsupported policy", func(env *Environment) { env.GitHubHostedKVM = "automatic" }, true},
		{"ARC mixed", func(env *Environment) { env.GitHubARC = "native-kvm-v1" }, true},
		{"legacy override", func(env *Environment) { env.RunsOn = "ubuntu-24.04" }, true},
		{"development", func(env *Environment) { env.GitHubDevelopment = true }, true},
		{"foreign utility", func(env *Environment) { env.UtilityPlatform = "linux/arm64" }, true},
		{"invented larger runner", func(env *Environment) { env.ExecutionClasses[0].RunsOn = "ubuntu-24.04-8cpu" }, true},
		{"ARC execution", func(env *Environment) { env.ExecutionClasses[0].RunsOn = "arc-amd64-kvm" }, true},
		{"foreign class", func(env *Environment) { env.ExecutionClasses[0].HostPlatform = "linux/arm64" }, true},
		{"foreign native route", func(env *Environment) { env.GitHubRunners = map[string]string{"linux/arm64": "ubuntu-24.04-arm"} }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
				Metadata: Metadata{Name: "hosted"}, UtilityPlatform: "linux/amd64",
				UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), GitHubHostedKVM: "native-amd64-v1"}
			env.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04"}
			// Validate the policy independently of the class's resource schema.
			env.ExecutionClasses = []execution.Class{{HostPlatform: "linux/amd64", RunsOn: "ubuntu-24.04"}}
			test.edit(&env)
			err := env.validateGitHubHostedKVM()
			if !test.fail {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("want engine-capability, got %v", err)
			}
		})
	}
}
