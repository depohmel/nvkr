#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Run this reviewed operator helper, not a copy from the candidate checkout.
# --approve explicitly trusts the exact candidate with source/registry reads,
# private publication, signing and privileged KVM execution. No merge is made.
set -euo pipefail
umask 077
fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "${3:-49}"; }
# Preserve explicitly mapped failures; malformed provider records must also cross
# the operator boundary with a coded error rather than a bare jq exit status.
trap 'status=$?; case "$status" in 25|29|30|31|47|49) exit "$status" ;; *) fail NVKR-E-VERIFY-FAILED "candidate operator step failed; inspect the retained records" 22 ;; esac' ERR
mode=${1:-}
if [[ $# == 4 && $mode == --approve ]]; then
  pr=$2 sha=$3 out=$4
elif [[ $# == 3 && $mode == --unlock ]]; then
  pr=0 sha=$2 out=$3
else
  fail NVKR-E-USAGE 'nightly-validate.sh --approve PR EXACT_COMMIT FRESH_OUT | --unlock LOCK_COMMIT FRESH_OUT' 2
fi
[[ (($mode == --unlock && $pr == 0) || $pr =~ ^[1-9][0-9]*$) && $sha =~ ^[0-9a-f]{40}$ ]] ||
  fail NVKR-E-USAGE 'approval must name one PR and its full lowercase commit SHA' 2
# This immutable checkpoint is only an idle concurrency marker. It grants no
# artifact, source-selection, publication or validation authority.
idle_marker=e74161ab5b41b89e0f4a6e9dce790664059a6d47
[[ $mode != --approve || $sha != "$idle_marker" ]] ||
  fail NVKR-E-USAGE 'the reserved idle marker cannot be approved as a candidate' 2
[[ $out == /* ]] || fail NVKR-E-USAGE 'validation records require an absolute output directory' 2
for command in gh jq timeout; do
  command -v "$command" >/dev/null || fail NVKR-E-ENGINE-CAPABILITY "missing $command" 29
done
[[ ! -e $out && ! -L $out ]] || fail NVKR-E-IMMUTABLE-VIOLATION 'validation output must be fresh' 47
mkdir -m 700 "$out"
repo=dsx-ai-factory/nvkr
branch=nvkr-validation
lock=nvkr-validation-lock
zero=0000000000000000000000000000000000000000
owned=false run_id='' terminal=false dispatch_uncertain=false validated=false
api() {
  timeout --signal=TERM --kill-after=5s 60s gh api --hostname github.com -H 'X-GitHub-Api-Version: 2026-03-10' "$@" || {
    printf 'nvkr: [NVKR-E-UNAVAILABLE] GitHub request failed; do not infer its mutation outcome.\n' >&2
    return 30
  }
}
api "repos/$repo" >"$out/repository.json"
repository_id=$(jq -er --arg repo "$repo" '
  if .full_name == $repo and .id == 1363007688 and .default_branch == "main" then .node_id
  else error("wrong validation repository") end' "$out/repository.json")

check_pr() {
  api "repos/$repo/pulls/$pr" >"$out/pr.json"
  jq -e --arg sha "$sha" --arg repo "$repo" '
    .head.sha == $sha and .state == "open" and .draft == false and
    .base.ref == "main" and .base.repo.full_name == $repo
  ' "$out/pr.json" >/dev/null || fail NVKR-E-UNPINNED-INPUT 'approved commit is no longer the ready open PR head'
  api "repos/$repo/git/ref/heads/pull-request/$pr" >"$out/mirror.json"
  jq -e --arg sha "$sha" '.object.type == "commit" and .object.sha == $sha' "$out/mirror.json" >/dev/null ||
    fail NVKR-E-UNPINNED-INPUT 'copy-pr-bot mirror does not hold the approved PR commit'
}
idle() {
  local status
  # Query every documented nonterminal status separately. Looking through only
  # the latest historical page can miss an older waiting deployment.
  for status in queued requested waiting pending in_progress; do
    api --paginate "repos/$repo/actions/runs?branch=$branch&status=$status&per_page=100" >"$out/branch-runs.json" || return 1
    jq -se 'length == 1 and .[0].total_count == 0 and .[0].workflow_runs == []' "$out/branch-runs.json" >/dev/null || return 1
  done
}
main_nightly_idle() {
  local status
  # Only the registered main nightly shares this candidate's coordinator slot.
  # Unrelated main workflows do not prevent validation or operator recovery.
  for status in queued requested waiting pending in_progress; do
    api --paginate "repos/$repo/actions/workflows/nvkr-nightly.yml/runs?branch=main&status=$status&per_page=100" >"$out/main-nightly-runs.json" || return 1
    jq -se 'length == 1 and .[0].total_count == 0 and .[0].workflow_runs == []' "$out/main-nightly-runs.json" >/dev/null || return 1
  done
}
refs_update() {
  local updates=$1
  jq -n --arg repo "$repository_id" --argjson updates "$updates" '{
    query:"mutation($input:UpdateRefsInput!) { updateRefs(input:$input) { clientMutationId } }",
    variables:{input:{repositoryId:$repo,refUpdates:$updates}}
  }' >"$out/ref-request.json"
  api graphql --input "$out/ref-request.json" >"$out/ref-response.json"
  jq -e '.errors == null and .data.updateRefs != null' "$out/ref-response.json" >/dev/null
}
release_lock() {
  local updates
  updates=$(jq -nc --arg sha "$sha" --arg idle "$idle_marker" --arg lock "refs/heads/$lock" \
    '[{name:$lock,beforeOid:$sha,afterOid:$idle,force:true}]')
  refs_update "$updates"
}
cleanup() {
  local status=$? deadline
  trap - EXIT
  trap '' INT TERM
  if [[ -n $run_id && $terminal != true ]]; then
    api --method POST "repos/$repo/actions/runs/$run_id/cancel" >"$out/cancel.json" 2>"$out/cancel-error.txt" || true
  fi
  if [[ $owned == true ]]; then
    if [[ $dispatch_uncertain == true ]]; then
      printf '::error::Validation lock retained after ambiguous dispatch. Inspect candidate runs, then use --unlock with this exact commit.\n' >&2
      exit 30
    fi
    if [[ $validated != true ]]; then
      # A failed/canceled coordinator may itself have an ambiguous producer
      # dispatch. An idle list cannot prove that delayed child creation ended.
      printf '::error::Validation lock retained after incomplete validation. Inspect coordinator and producer dispatches before explicit --unlock recovery.\n' >&2
      exit "$status"
    fi
    # Never move the candidate back or unlock while its producer may still run.
    deadline=$((SECONDS + 120))
    while (( SECONDS < deadline )); do
      if idle; then
        if ! release_lock; then
          printf '::error::Validation lock could not be released; inspect %s.\n' "$out" >&2
          status=30
        fi
        owned=false
        break
      fi
      sleep 5
    done
    if [[ $owned == true ]]; then
      printf '::error::Validation lock retained: an active or unreadable run remains. Inspect %s before operator recovery.\n' "$out" >&2
      status=30
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 31' INT TERM
if [[ $mode == --unlock ]]; then
  idle || fail NVKR-E-UNAVAILABLE 'candidate runs remain active or unreadable; keep the validation lock' 30
  release_lock || fail NVKR-E-IMMUTABLE-VIOLATION 'lock does not match the requested recovery commit' 47
  printf 'Released the idle validation lock for %s; candidate branch unchanged.\n' "$sha"
  exit 0
fi
check_pr
idle || fail NVKR-E-UNAVAILABLE 'an earlier validation run is active; do not move its branch' 30
main_nightly_idle || fail NVKR-E-UNAVAILABLE 'the main nightly is active or unreadable; do not promote the candidate' 30
for environment in nvkr-arc nvkr-release; do
  api "repos/$repo/environments/$environment" >"$out/environment.json"
  jq -e '.deployment_branch_policy.custom_branch_policies == true' "$out/environment.json" >/dev/null ||
    fail NVKR-E-TRUST-POLICY-MISSING 'validation requires explicit protected-environment branch policies' 25
  api "repos/$repo/environments/$environment/deployment-branch-policies?per_page=100" >"$out/policy.json"
  jq -e --arg branch "$branch" '
    .total_count == (.branch_policies | length) and
    any(.branch_policies[]; .type == "branch" and .name == $branch)
  ' "$out/policy.json" >/dev/null ||
    fail NVKR-E-ENGINE-CAPABILITY "one-time setup missing: $environment must explicitly admit $branch" 29
done
api "repos/$repo/git/matching-refs/heads/$branch" >"$out/refs.json"
lock_before=$(jq -er --arg ref "refs/heads/$lock" --arg idle "$idle_marker" --arg zero "$zero" '
  if type != "array" then error("invalid matching reference response") else . end |
  [.[] | select(.ref == $ref)] |
  if length == 0 then $zero
  elif length == 1 and .[0].object.type == "commit" and .[0].object.sha == $idle then $idle
  else error("validation lock is held or malformed") end' "$out/refs.json") ||
  fail NVKR-E-UNAVAILABLE 'validation lock is held or malformed; do not move the candidate' 30
before=$(jq -er --arg ref "refs/heads/$branch" --arg zero "$zero" '
  [.[] | select(.ref == $ref)] |
  if length == 0 then $zero
  elif length == 1 and .[0].object.type == "commit" then .[0].object.sha
  else error("ambiguous candidate reference") end' "$out/refs.json")
check_pr
updates=$(jq -nc --arg sha "$sha" --arg before "$before" --arg lock_before "$lock_before" \
  --arg branch "refs/heads/$branch" --arg lock "refs/heads/$lock" '
  [{name:$lock,beforeOid:$lock_before,afterOid:$sha,force:true},
   {name:$branch,beforeOid:$before,afterOid:$sha,force:true}]')
# Atomic compare-and-swap acquires the operator lock and promotes only the
# automation-owned candidate ref. Never update main, a PR branch or its mirror.
refs_update "$updates" || fail NVKR-E-IMMUTABLE-VIOLATION 'promotion was not confirmed; inspect the exact candidate and lock before recovery' 47
owned=true
idle || fail NVKR-E-UNAVAILABLE 'another validation run appeared; candidate remains frozen' 30
main_nightly_idle || fail NVKR-E-UNAVAILABLE 'the main nightly appeared after promotion; candidate remains frozen' 30
check_pr
api "repos/$repo/git/ref/heads/$branch" >"$out/promoted.json"
jq -e --arg sha "$sha" '.object.sha == $sha and .object.type == "commit"' "$out/promoted.json" >/dev/null
jq -n --arg branch "$branch" --arg sha "$sha" --arg pr "$pr" '
  {ref:$branch,inputs:{profile:"nvke-nightly-arm64",tenant_checks:true,candidate_sha:$sha,candidate_pr:$pr}}
' >"$out/dispatch-request.json"
# An ambiguous POST is never replayed and never replaced with a latest-run lookup.
dispatch_uncertain=true
api --method POST "repos/$repo/actions/workflows/nvkr-nightly.yml/dispatches" \
  --input "$out/dispatch-request.json" >"$out/dispatch.json" ||
  fail NVKR-E-UNAVAILABLE 'dispatch outcome is ambiguous; inspect exact candidate runs before retrying' 30
run_id=$(jq -er '.workflow_run_id | select(type == "number" and . > 0 and . <= 9007199254740991 and floor == .) | tostring' "$out/dispatch.json") ||
  fail NVKR-E-UNAVAILABLE 'dispatch returned no exact run ID; do not guess a run' 30
dispatch_uncertain=false
printf 'Candidate %s, PR #%s: https://github.com/%s/actions/runs/%s\n' "$sha" "$pr" "$repo" "$run_id"
deadline=$((SECONDS + 15 * 60 * 60))
while (( SECONDS < deadline )); do
  api "repos/$repo/actions/runs/$run_id" >"$out/coordinator.json"
  jq -e --arg sha "$sha" --arg run "$run_id" --arg repo "$repo" --arg branch "$branch" '
    (.id|tostring) == $run and .head_sha == $sha and .head_branch == $branch and
    .repository.full_name == $repo and .repository.id == 1363007688 and
    .head_repository.id == 1363007688 and .head_repository.full_name == $repo and .event == "workflow_dispatch" and
    (.path == ".github/workflows/nvkr-nightly.yml" or .path == (".github/workflows/nvkr-nightly.yml@" + $branch)) and .run_attempt == 1
  ' "$out/coordinator.json" >/dev/null || fail NVKR-E-UNPINNED-INPUT 'coordinator differs from approved exact candidate'
  if [[ $(jq -er '.status' "$out/coordinator.json") == completed ]]; then
    terminal=true
    break
  fi
  sleep 30
done
[[ $terminal == true ]] || fail NVKR-E-TIMEOUT 'candidate exceeded its bounded 15-hour cycle wait' 31
jq -e '.conclusion == "success"' "$out/coordinator.json" >/dev/null || fail NVKR-E-BUILD-FAILED 'candidate pipeline failed; fix the PR and validate its new commit before merge' 21
api --paginate "repos/$repo/actions/runs/$run_id/attempts/1/jobs?per_page=100" >"$out/jobs.json"
jq -se '[.[].jobs[]] | all(.[]; .conclusion == "success") and
  any(.[]; .name == "Build and publish nvke-nightly-arm64") and
  any(.[]; .name == "Test published nvke-nightly-arm64")' "$out/jobs.json" >/dev/null ||
  fail NVKR-E-VERIFY-FAILED 'expected build/runtime jobs did not all pass' 22
GH_HOST=github.com timeout --kill-after=5s 60s gh run download "$run_id" --repo "$repo" \
  --name "nightly-runtime-report-nvke-nightly-arm64-$run_id-1" --dir "$out/runtime"
jq -e --arg sha "$sha" --arg run "$run_id" --arg branch "$branch" '
  .sourceCommit == $sha and .sourceBranch == $branch and .coordinatorRun == $run and
  .coordinatorAttempt == "1" and .profile == "nvke-nightly-arm64" and
  .phase == "runtime" and .runtimePassed == true and .tenantChecksPassed == true and
  (.producerRun | type == "number" and . > 0) and .producerAttempt == 1 and
  (.nvkeCommit | type == "string" and test("^[0-9a-f]{40}$")) and .qualificationAuthority == "none"
' "$out/runtime/cycle.json" >/dev/null || fail NVKR-E-VERIFY-FAILED 'missing full runtime, tenant and joined-cleanup verdict for approved commit' 22
check_pr
api "repos/$repo/git/ref/heads/$branch" >"$out/final-ref.json"
jq -e --arg sha "$sha" '.object.type == "commit" and .object.sha == $sha' "$out/final-ref.json" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'candidate branch moved during validation'
validated=true
printf 'Full candidate validation passed for PR #%s at %s. No merge performed.\n' "$pr" "$sha"
