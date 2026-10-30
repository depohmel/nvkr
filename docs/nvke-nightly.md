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

Follow [Run E2E tests for your pull request](pr-e2e.md) to trigger this workflow
on your own PR branch, watch the result and share it with reviewers. The guide
starts with running in your own fork and also covers branches in this repository.

## Resources, timeouts and evidence

The six-hour job has bounded setup steps and passes its remaining deadline to
Go. `TestEndToEnd` reserves two hours inside that deadline for independent owned
cleanup. Setup refuses a non-hosted/non-AMD64 host or missing KVM.

The Go test prints phase start/end/failure and child process start/exit
observations. Once a minute, a parent-test heartbeat reports the last started
phase, its age and how long the public child stream has been idle. That heartbeat
shows parent liveness only; it does not establish child progress or success.
Exact-source nightly builds retain machine-command stdout in their bounded
captures and private logs instead of forwarding large Plan JSON to Actions.
Command stderr and the final build's ordinary output continue streaming.

Before checkout and builds, the disposable runner removes its unused Android,
.NET and Haskell SDK directories and records their sizes and remaining disk
space. Product outputs, Docker and Go stay local and intact. The disk producer's
64-GiB free-workspace admission remains unchanged. [Run 41075323371](https://github.com/dsx-ai-factory/nvkr/actions/runs/41075323371)
reached that gate after preparing the installer ISO, with 48,748 MiB available
against 65,536 MiB required. The cleanup change still needs a complete hosted run
to establish sufficient space for disk build and deployment.

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
