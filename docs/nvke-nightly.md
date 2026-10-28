# Nightly NVKR builds and NVKE end-to-end tests

The authored [GitHub workflow](../.github/workflows/nvkr-nightly.yml) resolves NVKE
`main` once, then builds and tests that exact commit on the same runner. It runs
at 03:17 UTC daily and supports manual dispatch. The two matrix rows use
`ubuntu-24.04` for AMD64 and `arc-arm64-kvm` for ARM64.

Each row invokes the existing NVKR Go integration test, `TestEndToEnd`. It uses
`BuildArtifacts`, `WithNativeSite` and the existing native cleanup owners:

1. Create private source-selection overlays over the existing catalogs. Every
   source-built NVKE component selects the cycle's exact commit, including complete
   disk producer replacements and the installer. Checked-in pins and the ordinary
   ARM demo output scope remain unchanged. The nightly privately adds its existing
   host/site tool outputs to the same Run.
2. Build the local Definition through the existing NVKR local executor, retaining
   its captured inputs, observations and exact artifact verification.
3. Run the built `nvkectl bootstrap site` using a temporary kind/Lima backend and a
   real QEMU/KVM site-controller VM. Deploy through the existing native owners.
4. Check the live Kubernetes API `/readyz`, the identified Ready node and its
   selected architecture. Stop and join owned guests, backend and network cleanup.

Manual `tenant_checks` additionally creates a tenant and checks workload and DNS.
Conformance remains a longer local opt-in. This workflow runs NVKR's Go test;
NVKE supplies the source-built CLI, simulator and installer.

Build products stay local to the runner. The workflow has no publication or
signing stage, binary uploads, Actions artifact uploads or shared build caches.
It consumes existing private toolchain images with the independent registry
reader, in a bounded pull step whose temporary credentials are removed before
the integration test. Existing release and manually dispatched producer workflows
have their own publication policies and are separate from this nightly.

## Inputs and access

Both architectures reuse the existing native NVKE, tools, runtime dependency,
CAPNICo and profiles catalogs. The local Definitions are
`inputs/definitions/demo/{amd64,arm64}-nvidia-local-kvm.json`; the matching local
Environments contain neither publication nor signing configuration.

`NKE_SOURCE_TOKEN` reads NVKE, NVIDIA OVN and the catalog-pinned NVKR identity-helper
source. Private repositories are checked out without persisting credentials;
process-local Git URL rewrites let the existing
source owners fetch their exact commits from those local checkouts. The original
repository URLs remain part of source identity. `NVKR_REGISTRY_READ` and
`NVKR_REGISTRY_READ_USER` read existing private toolchains. No writer credential
or OIDC signing permission is supplied.

The Go test accepts `NVKR_NVKE_E2E_CONFIG`, pointing to the existing demo YAML
schema. The workflow supplies explicit local build inputs, a fresh attempt,
cleanup enabled, and a four-vCPU, 4 GiB site/tenant trial shape with 1 GiB headroom.
These values are configuration, not measured workload minima. The existing
owners observe actual host capacity and require native KVM before work.

The source-selection helper appends ordinary catalog overlays below the private
attempt directory. Existing overlay predecessors remain intact. The local input
capture/replay owner records those exact files; no synthetic completed Run or
hosted publication receipt is introduced. Acquired dependencies retain reviewed
pins. The retained tagged demo and latest-main CAPI init image have separate
reviewed acquisition entries; the unchanged installer check rejects an unexpected
source-selected parent reference.

## Timeouts and evidence

Source resolution has a five-minute job limit. Each architecture job has a
360-minute limit. Setup and toolchain pulls have smaller step/command limits.
The Go test receives the remaining time below a deadline set five minutes before
the job limit, and reserves two hours for independent cleanup after work stops.
SIGINT/SIGTERM cancel work through that same cleanup lifecycle.

The outcome is an unsigned repository integration-test result, outside qualified
release acceptance. A configured schedule, passing local contracts or successful
KVM probe does not establish a completed hosted build/bootstrap/cleanup cycle.
That full matrix cycle remains unverified until an actual run completes.
