// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

func (environment Environment) validateGitHubHostedKVM() error {
	if environment.GitHubHostedKVM == "" {
		return nil
	}
	if environment.GitHubHostedKVM != "native-amd64-v1" || environment.GitHubARC != "" ||
		environment.GitHubDevelopment || environment.RunsOn != "" || environment.UtilityPlatform != "linux/amd64" || len(environment.GitHubRunners) != 1 {
		return e.New(e.CodeEngineCapability, "githubHostedKVM requires the separate native-amd64-v1 policy without ARC, development or runsOn overrides")
	}
	for platform, runner := range environment.GitHubRunners {
		if platform != "linux/amd64" || runner != "ubuntu-24.04" {
			return e.New(e.CodeEngineCapability, "githubHostedKVM admits only the ubuntu-24.04 AMD64 route")
		}
	}
	for _, class := range environment.ExecutionClasses {
		if class.HostPlatform != "linux/amd64" || class.RunsOn != "ubuntu-24.04" {
			return e.New(e.CodeEngineCapability, "githubHostedKVM requires native AMD64 execution classes on ubuntu-24.04")
		}
	}
	return nil
}
