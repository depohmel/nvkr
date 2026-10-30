// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const validationIdleMarker = "e74161ab5b41b89e0f4a6e9dce790664059a6d47"

// Unexpected endpoints fail rather than reaching GitHub. Mutable PR fixtures
// model changes between admission and the final acceptance check.
const fakeValidationGitHub = `#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == run && ${2:-} == download ]]; then
  [[ $3 == 7777 ]]
  destination='' repository='' artifact=''
  while [[ $# -gt 0 ]]; do
    case $1 in
      --dir) destination=$2; shift ;;
      --repo) repository=$2; shift ;;
      --name) artifact=$2; shift ;;
    esac
    shift
  done
  [[ $repository == dsx-ai-factory/nvkr && $artifact == nightly-runtime-report-nvke-nightly-arm64-7777-1 ]]
  printf 'DOWNLOAD 7777\n' >>"$FAKE_GH_ROOT/calls"
  mkdir -p "$destination"
  cp "$FAKE_GH_ROOT/cycle.json" "$destination/cycle.json"
  touch "$FAKE_GH_ROOT/downloaded"
  exit 0
fi
[[ ${1:-} == api ]]
method=GET endpoint='' input=''
while [[ $# -gt 0 ]]; do
  case $1 in
    --method) method=$2; shift ;;
    --input) input=$2; shift ;;
    repos/*|graphql) endpoint=$1 ;;
  esac
  shift
done
printf '%s %s\n' "$method" "$endpoint" >>"$FAKE_GH_ROOT/calls"
case "$method $endpoint" in
  'GET repos/dsx-ai-factory/nvkr') cat "$FAKE_GH_ROOT/repository.json" ;;
  'GET repos/dsx-ai-factory/nvkr/pulls/42')
    if [[ ${FAKE_PR_CHANGED_AT_END:-false} == true && -f $FAKE_GH_ROOT/downloaded ]]; then
      jq '.head.sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' "$FAKE_GH_ROOT/pr.json"
    else cat "$FAKE_GH_ROOT/pr.json"; fi ;;
  'GET repos/dsx-ai-factory/nvkr/git/ref/heads/pull-request/42') cat "$FAKE_GH_ROOT/mirror.json" ;;
  'GET repos/dsx-ai-factory/nvkr/actions/runs?branch=nvkr-validation&status='*)
    if [[ ${FAKE_WAITING_RUN:-false} == true && $endpoint == *'status=waiting&'* ]]; then
      printf '{"total_count":0,"workflow_runs":[]}\n{"total_count":1,"workflow_runs":[{"id":999,"status":"waiting"}]}\n'
    else printf '{"total_count":0,"workflow_runs":[]}\n'; fi ;;
  'GET repos/dsx-ai-factory/nvkr/environments/nvkr-arc'|'GET repos/dsx-ai-factory/nvkr/environments/nvkr-release')
    printf '{"deployment_branch_policy":{"custom_branch_policies":true}}\n' ;;
  'GET repos/dsx-ai-factory/nvkr/environments/'*'/deployment-branch-policies?per_page=100')
    printf '{"total_count":1,"branch_policies":[{"type":"branch","name":"nvkr-validation"}]}\n' ;;
  'GET repos/dsx-ai-factory/nvkr/git/matching-refs/heads/nvkr-validation') cat "$FAKE_GH_ROOT/refs.json" ;;
  'GET graphql')
    jq -c '.variables.input.refUpdates' "$input" >>"$FAKE_GH_ROOT/ref-updates"
    if [[ ${FAKE_CAS_FAILURE:-false} == true ]]; then
      printf '{"errors":[{"message":"reference changed"}]}\n'
    else printf '{"data":{"updateRefs":{"clientMutationId":null}}}\n'; fi ;;
  'GET repos/dsx-ai-factory/nvkr/git/ref/heads/nvkr-validation') cat "$FAKE_GH_ROOT/promoted.json" ;;
  'POST repos/dsx-ai-factory/nvkr/actions/workflows/nvkr-nightly.yml/dispatches')
    cp "$input" "$FAKE_GH_ROOT/dispatched.json"
    [[ ${FAKE_DISPATCH_FAILURE:-false} != true ]] || exit 17
    cat "$FAKE_GH_ROOT/dispatch.json" ;;
  'GET repos/dsx-ai-factory/nvkr/actions/runs/7777') cat "$FAKE_GH_ROOT/coordinator.json" ;;
  'GET repos/dsx-ai-factory/nvkr/actions/runs/7777/attempts/1/jobs?per_page=100') cat "$FAKE_GH_ROOT/jobs.json" ;;
  'POST repos/dsx-ai-factory/nvkr/actions/runs/7777/cancel') printf '{}\n' ;;
  *) printf 'unexpected validation API: %s %s\n' "$method" "$endpoint" >&2; exit 99 ;;
esac
`

func newValidationFixture(t *testing.T) *nightlyFixture {
	t.Helper()
	f := newNightlyFixture(t)
	if err := os.WriteFile(filepath.Join(f.bin, "gh"), []byte(fakeValidationGitHub), 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureJSON(t, filepath.Join(f.root, "repository.json"), map[string]any{"id": 1363007688, "node_id": "R_validation_fixture", "full_name": "dsx-ai-factory/nvkr", "default_branch": "main"})
	fixtureJSON(t, filepath.Join(f.root, "pr.json"), map[string]any{"head": map[string]string{"sha": nightlySHA}, "state": "open", "draft": false, "base": map[string]any{"ref": "main", "repo": map[string]string{"full_name": "dsx-ai-factory/nvkr"}}})
	ref := map[string]any{"ref": "refs/heads/nvkr-validation", "object": map[string]string{"type": "commit", "sha": nightlySHA}}
	fixtureJSON(t, filepath.Join(f.root, "promoted.json"), ref)
	fixtureJSON(t, filepath.Join(f.root, "mirror.json"), ref)
	fixtureJSON(t, filepath.Join(f.root, "refs.json"), []map[string]any{{"ref": "refs/heads/nvkr-validation", "object": map[string]string{"type": "commit", "sha": strings.Repeat("c", 40)}}})
	fixtureJSON(t, filepath.Join(f.root, "dispatch.json"), map[string]int{"workflow_run_id": 7777})
	identity := map[string]any{"id": 1363007688, "full_name": "dsx-ai-factory/nvkr"}
	fixtureJSON(t, filepath.Join(f.root, "coordinator.json"), map[string]any{"id": 7777, "head_sha": nightlySHA, "head_branch": "nvkr-validation", "repository": identity, "head_repository": identity, "event": "workflow_dispatch", "path": ".github/workflows/nvkr-nightly.yml", "run_attempt": 1, "status": "completed", "conclusion": "success"})
	fixtureJSON(t, filepath.Join(f.root, "jobs.json"), map[string]any{"jobs": []map[string]string{{"name": "Build and publish nvke-nightly-arm64", "conclusion": "success"}, {"name": "Test published nvke-nightly-arm64", "conclusion": "success"}}})
	fixtureJSON(t, filepath.Join(f.root, "cycle.json"), map[string]any{"sourceCommit": nightlySHA, "sourceBranch": "nvkr-validation", "coordinatorRun": "7777", "coordinatorAttempt": "1", "profile": "nvke-nightly-arm64", "phase": "runtime", "runtimePassed": true, "tenantChecksPassed": true, "producerRun": 8888, "producerAttempt": 1, "nvkeCommit": nightlyNVKESHA, "qualificationAuthority": "none"})
	return f
}

func runValidation(t *testing.T, f *nightlyFixture, mode string) (string, int) {
	t.Helper()
	return runValidationAtCommit(t, f, mode, nightlySHA)
}

func runValidationAtCommit(t *testing.T, f *nightlyFixture, mode, sha string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate validation script")
	}
	args := []string{filepath.Join(filepath.Dir(filename), "../../scripts/nightly-validate.sh"), mode}
	if mode == "--approve" {
		args = append(args, "42")
	}
	args = append(args, sha, f.out)
	command := exec.CommandContext(ctx, "bash", args...)
	command.Env = f.environment
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("validation fixture exceeded its bound: %v\n%s", ctx.Err(), output)
	}
	if err == nil {
		return string(output), 0
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatal(err)
	}
	return string(output), exited.ExitCode()
}

func validationRefUpdates(t *testing.T, f *nightlyFixture) [][]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "ref-updates"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var requests [][]map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var request []map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	return requests
}

func assertValidationFailure(t *testing.T, output string, status, want int) {
	t.Helper()
	codes := map[int]string{2: "NVKR-E-USAGE", 21: "NVKR-E-BUILD-FAILED", 22: "NVKR-E-VERIFY-FAILED", 30: "NVKR-E-UNAVAILABLE", 47: "NVKR-E-IMMUTABLE-VIOLATION", 49: "NVKR-E-UNPINNED-INPUT"}
	if status != want || codes[want] == "" || !strings.Contains(output, codes[want]) {
		t.Fatalf("validation returned %d, want coded refusal %d %s\n%s", status, want, codes[want], output)
	}
}

func TestNightlyValidationPromotesAndTestsOnlyApprovedCandidate(t *testing.T) {
	f := newValidationFixture(t)
	output, status := runValidation(t, f, "--approve")
	if status != 0 || !strings.Contains(output, "Full candidate validation passed") {
		t.Fatalf("approved candidate: %d\n%s", status, output)
	}
	updates := validationRefUpdates(t, f)
	if len(updates) != 2 || len(updates[0]) != 2 || updates[0][0]["name"] != "refs/heads/nvkr-validation-lock" || updates[0][0]["beforeOid"] != strings.Repeat("0", 40) || updates[0][0]["afterOid"] != nightlySHA || updates[0][0]["force"] != true || updates[0][1]["name"] != "refs/heads/nvkr-validation" || updates[0][1]["beforeOid"] != strings.Repeat("c", 40) || updates[0][1]["afterOid"] != nightlySHA || len(updates[1]) != 1 || updates[1][0]["name"] != "refs/heads/nvkr-validation-lock" || updates[1][0]["beforeOid"] != nightlySHA || updates[1][0]["afterOid"] != validationIdleMarker || updates[1][0]["force"] != true {
		t.Fatalf("promotion or release escaped leased candidate refs: %+v", updates)
	}
	var request struct {
		Ref    string         `json:"ref"`
		Inputs map[string]any `json:"inputs"`
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "dispatched.json"))
	if err != nil || json.Unmarshal(raw, &request) != nil || request.Ref != "nvkr-validation" || request.Inputs["candidate_sha"] != nightlySHA || request.Inputs["candidate_pr"] != "42" || request.Inputs["tenant_checks"] != true || request.Inputs["profile"] != "nvke-nightly-arm64" {
		t.Fatalf("dispatch lost approved candidate or full tenant selection: %s (%v)", raw, err)
	}
}

func TestNightlyValidationReusesExactIdleLockMarker(t *testing.T) {
	f := newValidationFixture(t)
	fixtureJSON(t, filepath.Join(f.root, "refs.json"), []map[string]any{
		{"ref": "refs/heads/nvkr-validation", "object": map[string]string{"type": "commit", "sha": strings.Repeat("c", 40)}},
		{"ref": "refs/heads/nvkr-validation-lock", "object": map[string]string{"type": "commit", "sha": validationIdleMarker}},
		{"ref": "refs/heads/nvkr-validation-lock-other", "object": map[string]string{"type": "commit", "sha": nightlySHA}},
	})
	output, status := runValidation(t, f, "--approve")
	if status != 0 {
		t.Fatalf("idle marker reuse failed: %d\n%s", status, output)
	}
	updates := validationRefUpdates(t, f)
	if len(updates) != 2 || len(updates[0]) != 2 || updates[0][0]["beforeOid"] != validationIdleMarker || updates[0][0]["afterOid"] != nightlySHA || updates[0][0]["force"] != true || updates[1][0]["afterOid"] != validationIdleMarker {
		t.Fatalf("idle marker was not leased and restored with exact CAS: %+v", updates)
	}
}

func TestNightlyValidationRejectsHeldOrMalformedLockBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"busy", "duplicate idle", "invalid SHA", "wrong object type", "missing object", "invalid response shape"} {
		t.Run(scenario, func(t *testing.T) {
			f := newValidationFixture(t)
			object := map[string]string{"type": "commit", "sha": nightlySHA}
			ref := map[string]any{"ref": "refs/heads/nvkr-validation-lock", "object": object}
			refs := []map[string]any{ref}
			switch scenario {
			case "duplicate idle":
				object["sha"] = validationIdleMarker
				refs = append(refs, ref)
			case "invalid SHA":
				object["sha"] = "invalid"
			case "wrong object type":
				object["sha"], object["type"] = validationIdleMarker, "tag"
			case "missing object":
				delete(ref, "object")
			}
			if scenario == "invalid response shape" {
				object["sha"] = validationIdleMarker
				fixtureJSON(t, filepath.Join(f.root, "refs.json"), map[string]any{"unexpected": ref})
			} else {
				fixtureJSON(t, filepath.Join(f.root, "refs.json"), refs)
			}
			output, status := runValidation(t, f, "--approve")
			assertValidationFailure(t, output, status, 30)
			for _, call := range f.calls(t) {
				if strings.Contains(call, "graphql") || strings.HasPrefix(call, "POST ") {
					t.Fatalf("held/malformed lock performed mutation: %v", f.calls(t))
				}
			}
		})
	}
}

func TestNightlyValidationReservedMarkerIsNotCandidate(t *testing.T) {
	f := newValidationFixture(t)
	output, status := runValidationAtCommit(t, f, "--approve", validationIdleMarker)
	assertValidationFailure(t, output, status, 2)
	if len(f.calls(t)) != 0 || len(validationRefUpdates(t, f)) != 0 {
		t.Fatal("reserved marker approval reached the provider")
	}
}

func TestNightlyValidationRecoveryRestoresPersistentIdleMarker(t *testing.T) {
	for _, sha := range []string{nightlySHA, validationIdleMarker} {
		t.Run(sha, func(t *testing.T) {
			f := newValidationFixture(t)
			output, status := runValidationAtCommit(t, f, "--unlock", sha)
			if status != 0 {
				t.Fatalf("idle recovery failed: %d\n%s", status, output)
			}
			updates := validationRefUpdates(t, f)
			if len(updates) != 1 || len(updates[0]) != 1 || updates[0][0]["name"] != "refs/heads/nvkr-validation-lock" || updates[0][0]["beforeOid"] != sha || updates[0][0]["afterOid"] != validationIdleMarker || updates[0][0]["force"] != true {
				t.Fatalf("recovery did not CAS only the lock to its idle marker: %+v", updates)
			}
		})
	}
}

func TestNightlyValidationRejectsBeforePromotion(t *testing.T) {
	for _, scenario := range []string{"stale PR", "stale mirror", "waiting run on another page", "CAS conflict"} {
		t.Run(scenario, func(t *testing.T) {
			f := newValidationFixture(t)
			want := 49
			switch scenario {
			case "stale PR":
				fixtureJSON(t, filepath.Join(f.root, "pr.json"), map[string]any{"head": map[string]string{"sha": strings.Repeat("b", 40)}, "state": "open", "draft": false, "base": map[string]any{"ref": "main", "repo": map[string]string{"full_name": "dsx-ai-factory/nvkr"}}})
			case "stale mirror":
				fixtureJSON(t, filepath.Join(f.root, "mirror.json"), map[string]any{"object": map[string]string{"type": "commit", "sha": strings.Repeat("b", 40)}})
			case "waiting run on another page":
				f.setEnv("FAKE_WAITING_RUN", "true")
				want = 30
			case "CAS conflict":
				f.setEnv("FAKE_CAS_FAILURE", "true")
				want = 47
			}
			output, status := runValidation(t, f, "--approve")
			assertValidationFailure(t, output, status, want)
			for _, call := range f.calls(t) {
				if strings.HasPrefix(call, "POST ") || scenario != "CAS conflict" && strings.Contains(call, "graphql") {
					t.Fatalf("rejected candidate performed mutation: %v", f.calls(t))
				}
			}
		})
	}
}

func TestNightlyValidationRetainsLockForAmbiguousDispatch(t *testing.T) {
	for _, scenario := range []string{"missing returned ID", "POST outcome unknown"} {
		t.Run(scenario, func(t *testing.T) {
			f := newValidationFixture(t)
			fixtureJSON(t, filepath.Join(f.root, "dispatch.json"), map[string]any{})
			if scenario == "POST outcome unknown" {
				f.setEnv("FAKE_DISPATCH_FAILURE", "true")
			}
			output, status := runValidation(t, f, "--approve")
			assertValidationFailure(t, output, status, 30)
			if status != 30 || !strings.Contains(output, "lock retained after ambiguous dispatch") {
				t.Fatalf("ambiguous dispatch: %d\n%s", status, output)
			}
			posts := 0
			for _, call := range f.calls(t) {
				if strings.HasPrefix(call, "POST ") {
					posts++
				}
				if strings.Contains(call, "/actions/runs/7777") || strings.Contains(call, "DOWNLOAD") {
					t.Fatalf("ambiguous response guessed a run: %v", f.calls(t))
				}
			}
			if posts != 1 || len(validationRefUpdates(t, f)) != 1 {
				t.Fatalf("ambiguous dispatch retried or unlocked: %v", f.calls(t))
			}
		})
	}
}

func TestNightlyValidationRequiresFullCurrentRuntimeVerdict(t *testing.T) {
	for _, scenario := range []string{"skipped runtime job", "missing tenant verdict", "false tenant verdict", "PR head changed after runtime"} {
		t.Run(scenario, func(t *testing.T) {
			f := newValidationFixture(t)
			want := 22
			switch scenario {
			case "skipped runtime job":
				fixtureJSON(t, filepath.Join(f.root, "jobs.json"), map[string]any{"jobs": []map[string]string{{"name": "Build and publish nvke-nightly-arm64", "conclusion": "success"}, {"name": "Test published nvke-nightly-arm64", "conclusion": "skipped"}}})
			case "PR head changed after runtime":
				f.setEnv("FAKE_PR_CHANGED_AT_END", "true")
				want = 49
			default:
				raw, err := os.ReadFile(filepath.Join(f.root, "cycle.json"))
				if err != nil {
					t.Fatal(err)
				}
				var cycle map[string]any
				if err := json.Unmarshal(raw, &cycle); err != nil {
					t.Fatal(err)
				}
				delete(cycle, "tenantChecksPassed")
				if scenario == "false tenant verdict" {
					cycle["tenantChecksPassed"] = false
				}
				fixtureJSON(t, filepath.Join(f.root, "cycle.json"), cycle)
			}
			output, status := runValidation(t, f, "--approve")
			assertValidationFailure(t, output, status, want)
			if strings.Contains(output, "Full candidate validation passed") {
				t.Fatalf("incomplete/stale candidate accepted: %d\n%s", status, output)
			}
			if !strings.Contains(output, "lock retained after incomplete validation") || len(validationRefUpdates(t, f)) != 1 {
				t.Fatalf("incomplete validation released its lease: %v\n%s", validationRefUpdates(t, f), output)
			}
		})
	}
}

func TestNightlyValidationRetainsLeaseAfterTerminalCoordinatorFailure(t *testing.T) {
	f := newValidationFixture(t)
	raw, err := os.ReadFile(filepath.Join(f.root, "coordinator.json"))
	if err != nil {
		t.Fatal(err)
	}
	var coordinator map[string]any
	if err := json.Unmarshal(raw, &coordinator); err != nil {
		t.Fatal(err)
	}
	coordinator["conclusion"] = "failure"
	fixtureJSON(t, filepath.Join(f.root, "coordinator.json"), coordinator)
	// Every active-run response in this fixture is empty. A failed coordinator
	// may still have an ambiguous child dispatch that has not appeared yet.
	output, status := runValidation(t, f, "--approve")
	assertValidationFailure(t, output, status, 21)
	updates := validationRefUpdates(t, f)
	if len(updates) != 1 || len(updates[0]) != 2 || !strings.Contains(output, "lock retained after incomplete validation") {
		t.Fatalf("terminal failure released the lease despite unknown child outcome: %+v\n%s", updates, output)
	}
	for _, call := range f.calls(t) {
		if strings.Contains(call, "/cancel") || strings.Contains(call, "DOWNLOAD") {
			t.Fatalf("terminal failure canceled or consumed a different run: %v", f.calls(t))
		}
	}
}

func TestNightlyValidationRecoveryKeepsActiveCandidateLocked(t *testing.T) {
	f := newValidationFixture(t)
	f.setEnv("FAKE_WAITING_RUN", "true")
	output, status := runValidation(t, f, "--unlock")
	assertValidationFailure(t, output, status, 30)
	if status != 30 || !strings.Contains(output, "keep the validation lock") || len(validationRefUpdates(t, f)) != 0 {
		t.Fatalf("active recovery released validation lock: %d\n%s", status, output)
	}
}
