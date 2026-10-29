// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestNightlyCandidateBindsActualRefAndDispatchAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, branch, ref string
		allowed, tenant   bool
		code              e.Code
	}{
		{"legacy main", "", "refs/heads/main", false, false, ""},
		{"explicit main", "main", "refs/heads/main", false, false, ""},
		{"candidate", nightlyCandidateBranch, "refs/heads/nvkr-validation", true, true, ""},
		{"candidate without admission", nightlyCandidateBranch, "refs/heads/nvkr-validation", false, true, e.CodeEngineCapability},
		{"candidate without tenant", nightlyCandidateBranch, "refs/heads/nvkr-validation", true, false, e.CodeEngineCapability},
		{"candidate masquerades as main", nightlyCandidateBranch, "refs/heads/main", true, true, e.CodeUnpinnedInput},
		{"main masquerades as candidate", "", "refs/heads/nvkr-validation", true, true, e.CodeUnpinnedInput},
		{"arbitrary allowed branch", "feature/other", "refs/heads/feature/other", true, true, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, build := nightlyIdentityFixture()
			build.SourceBranch = tc.branch
			environment := lifecycle.Environment{}
			if tc.allowed {
				environment.GitHubDispatchBranches = []string{tc.branch}
			}
			err := validateNightlyCycle(profile, build, "example/nvkr", "123", build.SourceCommit, "100", "2", tc.ref)
			if err == nil {
				err = admitNightlyBranch(build, tc.ref, environment)
			}
			if err == nil {
				err = admitNightlyTenantChecks(build, tc.tenant)
			}
			if tc.code != "" {
				assertNightlyCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNightlyCandidateSignerPolicyNarrowsToActualWorkflowRef(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	for _, branch := range []string{"main", nightlyCandidateBranch} {
		t.Run(branch, func(t *testing.T) {
			build.SourceBranch = branch
			environment := lifecycle.Environment{GitHubDispatchBranches: []string{nightlyCandidateBranch}, Signing: &definition.Signing{
				Identity: `^https://github\.com/example/nvkr/\.github/workflows/` + profile.BuildWorkflow + `@refs/heads/(main|nvkr-validation)$`, Issuer: "https://token.actions.githubusercontent.com",
			}}
			before := environment.Clone()
			policy, err := nightlyVerificationPolicy(profile, build, "example/nvkr", "refs/heads/"+branch, environment)
			if err != nil {
				t.Fatal(err)
			}
			pattern, err := regexp.Compile(policy.IdentityRegexp)
			if err != nil {
				t.Fatal(err)
			}
			own := "https://github.com/example/nvkr/.github/workflows/" + profile.BuildWorkflow + "@refs/heads/" + branch
			other := "main"
			if branch == "main" {
				other = nightlyCandidateBranch
			}
			for _, identity := range []string{strings.TrimSuffix(own, branch) + other, own + "-suffix", "prefix" + own, strings.Replace(own, "github.com", "githubXcom", 1)} {
				if pattern.MatchString(identity) {
					t.Fatalf("narrowed signer policy admitted %q", identity)
				}
			}
			if !pattern.MatchString(own) || !reflect.DeepEqual(environment, before) {
				t.Fatal("signer policy lost exact ref or changed producer Environment")
			}
			environment.Signing.Identity = `^https://github\.com/example/nvkr/\.github/workflows/` + profile.BuildWorkflow + `@refs/heads/other$`
			_, err = nightlyVerificationPolicy(profile, build, "example/nvkr", "refs/heads/"+branch, environment)
			assertNightlyCode(t, err, e.CodeUntrustedSigner)
		})
	}
}

func TestNightlyCandidateHandoffBindsProducerAndArtifactBranches(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	build.SourceBranch = nightlyCandidateBranch
	plan, run := nightlyCompiledFixture(t, profile, build)
	for _, scenario := range []string{"success", "main producer", "main artifact", "no Plan admission", "no trusted admission", "cross coordinator ref"} {
		t.Run(scenario, func(t *testing.T) {
			archives := nightlyDownloadFixture(t, plan, run, "success")
			environment := plan.Environment.Clone()
			selected := *plan
			selected.Environment = plan.Environment.Clone()
			if scenario == "no Plan admission" {
				selected.Environment.GitHubDispatchBranches = nil
			}
			if scenario == "no trusted admission" {
				environment.GitHubDispatchBranches = nil
			}
			ref := "refs/heads/nvkr-validation"
			if scenario == "cross coordinator ref" {
				ref = "refs/heads/main"
			}
			downloads := 0
			capture := func(_ context.Context, _ []string, phase string, _ time.Duration) ([]byte, error) {
				if strings.HasPrefix(phase, "nightly-producer-") {
					observed := nightlyAPIRun(profile, build)
					if scenario == "main producer" {
						observed.HeadBranch = "main"
					}
					return json.Marshal(observed)
				}
				index := 0
				if strings.HasSuffix(phase, "1") {
					index = 1
				}
				if strings.HasPrefix(phase, "nightly-artifact-before-") || strings.HasPrefix(phase, "nightly-artifact-after-") {
					name := "nvkr-lifecycle-run"
					if index == 1 {
						name = "nvkr-publication-release"
					}
					artifact := nightlyAPIArtifact(name, build)
					artifact.Size = int64(len(archives[index]))
					artifact.Digest = pipeline.FileDigest(archives[index])
					if scenario == "main artifact" {
						artifact.WorkflowRun.HeadBranch = "main"
					}
					return json.Marshal(nightlyArtifactList{TotalCount: 1, Artifacts: []nightlyGitHubArtifact{artifact}})
				}
				downloads++
				return archives[index], nil
			}
			workspace := t.TempDir()
			if err := os.Chmod(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			inputs, err := newNightlyInputsWithCapture(t.Context(), profile, build, &selected, plan.Definition, environment, workspace, "example/nvkr", "123", build.SourceCommit, "100", "2", ref, capture)
			if scenario == "success" {
				if err != nil || inputs == nil || downloads != 2 {
					t.Fatalf("candidate handoff downloads=%d inputs=%v err=%v", downloads, inputs, err)
				}
				return
			}
			code := e.CodeUnpinnedInput
			if strings.Contains(scenario, "admission") {
				code = e.CodeEngineCapability
			}
			assertNightlyCode(t, err, code)
			if downloads != 0 || inputs != nil {
				t.Fatal("invalid candidate exposed downloaded payload")
			}
		})
	}
}
