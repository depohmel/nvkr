// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Keep the nightly's public-runner and non-distribution boundaries explicit.
func TestNightlyUsesOnlyHostedBuildAndGoE2E(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]json.RawMessage `json:"on"`
		Permissions map[string]string          `json:"permissions"`
		Jobs        map[string]struct {
			RunsOn         string            `json:"runs-on"`
			Environment    string            `json:"environment"`
			TimeoutMinutes int               `json:"timeout-minutes"`
			Permissions    map[string]string `json:"permissions"`
			Steps          []struct {
				Run  string            `json:"run"`
				Uses string            `json:"uses"`
				Env  map[string]string `json:"env"`
				With map[string]any    `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	var schedule []struct{ Cron string }
	if err := json.Unmarshal(workflow.On["schedule"], &schedule); err != nil || len(schedule) != 1 || schedule[0].Cron != "17 3 * * *" {
		t.Fatalf("nightly must retain its daily trigger: %v", err)
	}
	if len(workflow.On) != 2 || workflow.On["workflow_dispatch"] == nil || len(workflow.Jobs) != 1 || !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("nightly must retain ordinary manual dispatch and read-only permissions")
	}
	for name, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" || job.Environment != "" || len(job.Permissions) != 0 || job.TimeoutMinutes <= 0 || job.TimeoutMinutes > 360 {
			t.Fatalf("job %s must use a bounded public hosted runner without protected NVIDIA environments", name)
		}
		built, tenant, source, host := false, false, false, false
		for _, step := range job.Steps {
			host = host || strings.Contains(step.Run, `test "$RUNNER_ENVIRONMENT" = github-hosted`)
			source = source || strings.Contains(step.Run, "NVKR_NVKE_E2E_SOURCE_COMMIT=")
			if strings.Contains(step.Run, "go test ./internal/nvkedemo -run '^TestEndToEnd$'") {
				built = step.Env["NVKR_NVKE_E2E"] == "1" && strings.Contains(step.Run, `-timeout "${remaining}s"`)
				tenant = step.Env["NVKR_NVKE_E2E_TENANT"] == "1"
			}
			if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.With["cache"] != false {
				t.Fatal("nightly must not publish build outputs through the Go Actions cache")
			}
			if strings.HasPrefix(step.Uses, "docker/setup-buildx-action@") && step.With["cache-binary"] != false {
				t.Fatal("nightly must not enable Buildx binary caching")
			}
		}
		if !built || !tenant || !source || !host {
			t.Fatal("nightly must build the frozen main source and run the native site/tenant Go E2E on an admitted hosted runner")
		}
	}
	for _, forbidden := range []string{"secrets.", "actions/upload-artifact@", "actions/cache@", "nightly-validate.sh", "nvkr-validation", "updateRefs", "docker push", "oras push"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("nightly introduces credentials, distribution or ref mutation through %q", forbidden)
		}
	}
}
