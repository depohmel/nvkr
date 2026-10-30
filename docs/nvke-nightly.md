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

Use the Actions **Run workflow** branch selector or GitHub CLI:

```sh
gh workflow run nvkr-nightly.yml --repo dsx-ai-factory/nvkr --ref YOUR_BRANCH
```

The workflow is already registered on the default branch. `YOUR_BRANCH` must
exist in the selected repository; a fork PR can use its existing repository
mirror branch or run in the fork. No candidate-promotion branch, lock ref,
operator script, environment-policy change or merge is needed for this job.
Manual dispatch retains GitHub's normal repository-write authorization.

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
