// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

type nightlyCoordinatorStep struct {
	ID   string            `json:"id"`
	Run  string            `json:"run"`
	Uses string            `json:"uses"`
	Env  map[string]string `json:"env"`
	With map[string]any    `json:"with"`
}

type nightlyCoordinatorJob struct {
	Needs          []string                 `json:"needs"`
	TimeoutMinutes int                      `json:"timeout-minutes"`
	Outputs        map[string]string        `json:"outputs"`
	Env            map[string]string        `json:"env"`
	Steps          []nightlyCoordinatorStep `json:"steps"`
	Strategy       struct {
		FailFast bool `json:"fail-fast"`
		Matrix   struct {
			Include []map[string]string `json:"include"`
		} `json:"matrix"`
	} `json:"strategy"`
}

type nightlyCoordinatorDocument struct {
	On          map[string]json.RawMessage       `json:"on"`
	Permissions map[string]string                `json:"permissions"`
	Jobs        map[string]nightlyCoordinatorJob `json:"jobs"`
	Concurrency struct {
		CancelInProgress bool `json:"cancel-in-progress"`
	} `json:"concurrency"`
}

func nightlyCoordinatorFixture(t *testing.T) nightlyCoordinatorDocument {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow nightlyCoordinatorDocument
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestNightlyBuildsAndTestsInOneArchitectureMatrix(t *testing.T) {
	workflow := nightlyCoordinatorFixture(t)
	var schedules []struct {
		Cron string `json:"cron"`
	}
	if err := json.Unmarshal(workflow.On["schedule"], &schedules); err != nil || len(schedules) != 1 || schedules[0].Cron != "17 3 * * *" || workflow.On["workflow_dispatch"] == nil {
		t.Fatalf("daily/manual trigger changed: %+v %v", schedules, err)
	}
	if len(workflow.On) != 2 || len(workflow.Jobs) != 2 || workflow.Concurrency.CancelInProgress || !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("nightly must retain one resolver and one local matrix without interrupting active cleanup")
	}
	for name, job := range workflow.Jobs {
		if job.TimeoutMinutes <= 0 || job.TimeoutMinutes > 360 {
			t.Fatalf("job %s has no admitted GitHub timeout", name)
		}
	}
	execution := workflow.Jobs["e2e"]
	if len(execution.Strategy.Matrix.Include) != 2 || execution.Strategy.FailFast || !reflect.DeepEqual(execution.Needs, []string{"resolve-source"}) {
		t.Fatal("both native architectures must share one independent build/test job and frozen source")
	}
	arches := map[string]bool{}
	for _, row := range execution.Strategy.Matrix.Include {
		arches[row["arch"]] = true
		if row["arch"] == "amd64" && row["runner"] != "ubuntu-24.04" || row["arch"] == "arm64" && row["runner"] != "arc-arm64-kvm" {
			t.Fatal("native architecture has a foreign runner")
		}
	}
	if !reflect.DeepEqual(arches, map[string]bool{"amd64": true, "arm64": true}) {
		t.Fatalf("unexpected native architecture selection: %v", arches)
	}
	if execution.Env["NVKR_NVKE_E2E"] != "1" || execution.Env["NVKR_NVKE_SOURCE_COMMIT"] != "${{ needs.resolve-source.outputs.nvke_commit }}" {
		t.Fatal("matrix lost the direct Go test opt-in or its cycle-frozen source")
	}
	checkouts, ranTest, deadline := 0, false, false
	for _, step := range execution.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			checkouts++
			depth := float64(0)
			if step.With["repository"] == "dsx-ai-factory/nvkr" {
				depth = 1 // Exact catalog pin may be absent from current main history.
				if step.With["ref"] != "${{ steps.helper.outputs.commit }}" || step.With["path"] != ".nvkr/nightly-sources/nvkr-helper" {
					t.Fatal("identity helper must retain its independent exact source checkout")
				}
			}
			if step.With["persist-credentials"] != false || step.With["fetch-depth"] != depth {
				t.Fatal("source owners require complete Git history without persisted credentials")
			}
			if step.With["repository"] == "dsx-ai-factory/nvke" && step.With["ref"] != "${{ needs.resolve-source.outputs.nvke_commit }}" {
				t.Fatal("NVKE checkout differs from the exact cycle source")
			}
		}
		deadline = deadline || strings.Contains(step.Run, "355 * 60") && strings.Contains(step.Run, "NVKR_JOB_DEADLINE")
		if strings.Contains(step.Run, "go test ./internal/nvkedemo") {
			ranTest = strings.Contains(step.Run, "^TestEndToEnd$") && strings.Contains(step.Run, "-timeout \"${remaining}s\"") && strings.Contains(step.Run, "remaining > 2 * 60 * 60") && strings.Contains(step.Run, "NVKR_NVKE_E2E_CONFIG=")
		}
	}
	if checkouts != 4 || !ranTest || !deadline {
		t.Fatal("nightly omitted exact sources, direct local Go integration or time reserved for cleanup")
	}
}

func TestNightlyFreezesAuthenticatedNVKEMainOnce(t *testing.T) {
	resolver := nightlyCoordinatorFixture(t).Jobs["resolve-source"]
	if resolver.TimeoutMinutes != 5 || len(resolver.Steps) != 1 || resolver.Outputs["nvke_commit"] != "${{ steps.source.outputs.nvke_commit }}" || resolver.Steps[0].Env["GH_TOKEN"] != "${{ secrets.NKE_SOURCE_TOKEN }}" {
		t.Fatal("source resolution lost its authenticated bounded owner")
	}
	const commit = "dddddddddddddddddddddddddddddddddddddddd"
	for _, test := range []struct {
		name, kind, sha string
		apiOK, valid    bool
	}{
		{"exact commit", "commit", commit, true, true},
		{"mutable source", "commit", "main", true, false},
		{"tag object", "tag", commit, true, false},
		{"API failure", "commit", commit, false, false},
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
			if err := os.Symlink(timeout, filepath.Join(root, "timeout")); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"ref": "refs/heads/main", "object": map[string]string{"type": test.kind, "sha": test.sha}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "response.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			stub := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >>\"$SOURCE_FIXTURE/calls\"\n[ \"$SOURCE_API_OK\" = true ]\ncat \"$SOURCE_FIXTURE/response.json\"\n"
			if err := os.WriteFile(filepath.Join(root, "gh"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "outputs")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", resolver.Steps[0].Run)
			command.Env = []string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"), "GH_TOKEN=synthetic-source-read", "SOURCE_FIXTURE=" + root, "SOURCE_API_OK=" + map[bool]string{true: "true", false: "false"}[test.apiOK], "GITHUB_OUTPUT=" + output}
			log, runErr := command.CombinedOutput()
			raw, readErr := os.ReadFile(output)
			if test.valid {
				if runErr != nil || readErr != nil || string(raw) != "nvke_commit="+commit+"\n" {
					t.Fatalf("exact source not frozen: %v %v %s %s", runErr, readErr, raw, log)
				}
			} else if runErr == nil || !os.IsNotExist(readErr) {
				t.Fatalf("rejected source emitted output: %v %v %s", runErr, readErr, raw)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 1 || !strings.Contains(string(calls), "repos/dsx-ai-factory/nvke/git/ref/heads/main") {
				t.Fatalf("source resolution retried or used a different source: %s %v", calls, err)
			}
		})
	}
}
