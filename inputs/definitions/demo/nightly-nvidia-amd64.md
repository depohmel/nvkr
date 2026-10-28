# AMD64 NVIDIA nightly inputs

`amd64-nvidia-kvm.json` prepares the same 29 named output roles as the NVIDIA ARM64 nightly for
native Linux AMD64. `../../environments/github-nightly-nvidia-amd64.json` selects GitHub's
`ubuntu-24.04` hosted runner through the explicit hosted-KVM policy. The coordinator selects
`nvke-nightly-amd64` on its daily schedule and manual `all` dispatch, alongside the existing
ARM64 profile. The producer uses the existing main-only `nvkr-release` Environment.

The AMD64-only disk overlay and matching execution class select 4 CPUs, 10 GiB memory and
64 GiB ephemeral storage. Other disk producers retain their existing budgets. These configured
admission limits are not measured minimum resource requirements. The AMD64 candidate runtime
selects the explicit `github-hosted-amd64-v1` resource observation policy; it records current
capacity and attempts the real workload without the unsupported 24/90 GiB runtime floors.
The existing ARM64 runtime retains its fixed policy.

The standard hosted AMD64 [probe job](https://github.com/depohmel/nvkr/actions/runs/40555397725/job/122369991078)
established KVM initialization and observed 15,158 MiB available memory and 88,694 MiB free disk.
It failed the configured E2E resource checks, without attempting the product build or E2E suite.
It therefore does not prove that either workload cannot complete on that runner.

The later [source-built workload Run](https://github.com/depohmel/nvkr/actions/runs/40578231640)
built and checked the QCOW2, then passed 11 Go E2E specs using kind and real KVM VMs with the
nightly guest shape. One selected multi-CIDR spec skipped. The job lasted 60 minutes 52 seconds
and uploaded no artifacts. Resource samples reached only 373 MiB free disk and used almost all
available swap, so this is a successful experiment with little headroom. It does not establish
the authenticated candidate's resource envelope or a completed publication-to-runtime nightly.

| Output role | Source of the AMD64 selection |
| --- | --- |
| CLI, controller and controller/NKTP charts | `../../catalogs/nvke/nightly-site-amd64`, following the ARM64 source-owned commands at NVKE `v0.22.0` |
| CAPNICo | `../../catalogs/capnico/local-amd64`, existing CAPNICo `v0.0.45` source-owned Dockerfile |
| Installer tools | `../../catalogs/tools/installer-hosted-amd64`, same exact clusterctl, Helm, kind, kubectl and ORAS releases as ARM64, with official AMD64 Helm bytes |
| Operator tools | Existing `../../catalogs/tools/operator-amd64` release acquisitions |
| Profiles | `../../catalogs/controller-profiles/nvidia-amd64`, the additive local profile using AMD64 metadata and `ttyS0`, matching the pinned NVKE Packer console selection |
| Simulator and assets | `../../catalogs/nicosim/amd64`, same NVKE source and version-interface patch as ARM64 |
| Plain QCOW2 and its materials | Existing dual-architecture node-image producers, explicit AMD64 installer/root components and `../../catalogs/node-images/variants/nvidia-amd64-kvm.json` |
| NVIDIA OVN | Existing `../../catalogs/ovn-nvidia` and the current `native-nvidia.json` producer declarations, commit `8af51273d52dfa07454435d18d37230e582d797a`, stream `2.4.2` |
| Dependency images and remaining charts | Existing dual-architecture release and runtime-dependency catalogs and their AMD64 artifact pins |

Catalog locks are generated through `catalog pin` or `catalog bump --all` and
retain their reviewed baseline. The nightly's explicit runtime source policy
instead freezes NVKE `main` once per cycle and applies that exact commit to every
source-built NVKE producer. It does not update these locks. The local Go E2E suite
is compiled from the same commit and consumes the authenticated NVKR artifacts
through NVKR-owned setup; there is no separate NVKE harness PR dependency.
The current Go 1.27.3, BuildKit and source Alpine index pins contain native Linux AMD64 children.
The chart vendor closure uses the existing operator catalog's Helm 4.1.4 AMD64 archive and executable
digests; the installer's separate acquired Helm 4.2.4 remains unchanged.

The disk overlay and simulator use the existing AMD64 Packer image
`ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer-native@sha256:1415193ed0c50cd9c3d5b3bbd6207728262db3469c6ccd36717d47a6f50e3c0e`.
The [native measurement Run](https://github.com/dsx-ai-factory/nvkr/actions/runs/39522193703)
successfully assembled and validated the AMD64 toolchain. The package version is tagged
`native-20261020-go1.27.3-amd64`. This establishes the existing preparation evidence, not a fresh
image bake, complete hosted Run, nightly runtime result or organizational toolchain acceptance.

Publication retains private/internal storage admission and exact signed workflow identity. Preparing
AMD64 inputs does not permit public binary, image, chart or QCOW2 distribution. The existing ARM64
Definition, catalog pins, generated bundle and runtime route are preserved.

Generate and check this named bundle with the repository owner:

```sh
bin/nvkr-exec run generate --engine github \
  --definition inputs/definitions/demo/amd64-nvidia-kvm.json \
  --environment inputs/environments/github-nightly-nvidia-amd64.json --root .
bin/nvkr-exec run generate --engine github \
  --definition inputs/definitions/demo/amd64-nvidia-kvm.json \
  --environment inputs/environments/github-nightly-nvidia-amd64.json --root . --runtime --check
```

Catalog pin updates within the reviewed workflow contract are compiled at producer dispatch.
Workflow or Environment changes still require regeneration through this owner. The catalog
maintenance closure and `make workflows-check` include this bundle.
