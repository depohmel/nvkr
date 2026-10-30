# Nightly NVKR builds and NVKE end-to-end tests

The authored [workflow](../.github/workflows/nvkr-nightly.yml) runs daily at
03:17 UTC and supports ordinary manual branch dispatch. One `ubuntu-24.04`
GitHub-hosted AMD64 job builds and tests together. It uses no NVIDIA runner,
protected environment, repository secret, signing permission or registry writer.
It uploads no Actions artifacts or caches. Binaries, OCI images, QCOW2 files and
raw diagnostics stay on the disposable runner's local disk.

The job checks out the dispatched NVKR commit and resolves NVKE `main` once.
It passes that exact NVKE commit to NVKR's existing `TestEndToEnd` Go test.
The local preparation owner derives temporary source overlays from the existing
AMD64 NVIDIA inputs and the existing current-main disk-material/CAPI-init
compatibility selections; committed catalogs and locks remain unchanged.
Source-built NVKE producers use the frozen main commit. Other dependency pins
retain their owners. NVIDIA OVN remains selected.

`TestEndToEnd` calls `BuildArtifacts`, then the existing native site owner.
That builds fresh local NVKR Runs and bootstraps a real site-controller KVM VM
through the temporary kind/Lima backend. Site API and Ready-node checks are
followed by a tenant with one control plane and one worker, including workload
and DNS checks. VM, backend and network cleanup remain owned and joined by Go.
The workflow does not invoke the demo's E2E CLI or NVKE's separate test harness.
The existing demo executable supplies its existing network privilege helper.

## Run a workflow branch before merging

The workflow must already exist on the repository's default branch, and the
person dispatching it needs repository write access, as described in
[GitHub's manual-run documentation](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow).
The selected branch supplies the workflow and NVKR code; NVKE is still resolved
from its latest `main` when the job starts.

For a PR branch in `dsx-ai-factory/nvkr`, use its branch name:

```sh
gh workflow run nvkr-nightly.yml --repo dsx-ai-factory/nvkr --ref YOUR_BRANCH
```

For a fork PR with an existing copy-pr-bot mirror, use `pull-request/NUMBER`
in the upstream repository. For example, to test PR #283, first verify that the
mirror has caught up with the PR's current commit, then dispatch:

```sh
set -eu
repo=dsx-ai-factory/nvkr
pr=283
branch="pull-request/$pr"
pr_sha=$(gh pr view "$pr" --repo "$repo" --json headRefOid --jq '.headRefOid')
branch_sha=$(gh api "repos/$repo/git/ref/heads/$branch" --jq '.object.sha')
test "$pr_sha" = "$branch_sha" &&
  gh workflow run nvkr-nightly.yml --repo "$repo" --ref "$branch"
```

Replace `283` with the PR number. If the mirror is missing or the commits differ,
wait for the existing mirror process before dispatching. Alternatively, run in
the fork using `--repo FORK_OWNER/nvkr --ref YOUR_BRANCH`, provided Actions is
enabled there and the workflow is registered on that fork's default branch.
These commands do not create or move branches or change repository settings.

In the web UI, open **Actions → NVKR nightly NVKE end-to-end tests → Run workflow**,
select the same branch, and click **Run workflow**.

Use the run URL returned by the dispatch. If it is not returned, list runs for
the exact branch and PR commit and select the matching dispatch:

```sh
gh run list --repo "$repo" --workflow nvkr-nightly.yml \
  --branch "$branch" --commit "$pr_sha" --event workflow_dispatch \
  --json databaseId,headSha,status,conclusion,url
```

With that run's numeric ID, inspect and follow it:

```sh
gh run view RUN_ID --repo dsx-ai-factory/nvkr --json headSha,status,conclusion,url
gh run watch RUN_ID --repo dsx-ai-factory/nvkr --exit-status
# After a failed run completes:
gh run view RUN_ID --repo dsx-ai-factory/nvkr --log-failed
```

For a fork run, use its repository in these commands. Confirm `headSha` matches
the commit being reviewed and the completed run's `conclusion` is `success`.
The run summary records both NVKR and NVKE commits. After pushing a fix, dispatch
again; [rerunning the old run](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)
keeps its original NVKR commit. A passing PR CI
gate alone does not establish a successful full build/site/tenant E2E.

## Resources, timeouts and evidence

The six-hour job has bounded setup steps and passes its remaining deadline to
Go. `TestEndToEnd` reserves two hours inside that deadline for independent owned
cleanup. Setup refuses a non-hosted/non-AMD64 host or missing KVM.

The explicit first hosted profile selects a 4-GiB site VM, 2 GiB per tenant VM,
two vCPUs per guest and 1 GiB of host/QEMU headroom. Runtime admission observes
actual available capacity; these settings are an initial experiment, not a proven
minimum. The earlier [hosted experiment](https://github.com/depohmel/nvkr/actions/runs/40578231640)
demonstrated AMD64 KVM, a disk build and NVKE's separate VM test harness, but
does not prove this native NVKR topology or its cleanup. That experiment also
encountered severe disk pressure. A completed run of this exact workflow is
required before claiming hosted acceptance.

The generated ARM64 publishing workflows and `TestNightlyPublishedCandidate`
retain their separate artifact-consumer diagnostic scope. This local hosted
nightly grants no qualified release or publication acceptance.
