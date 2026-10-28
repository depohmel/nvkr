# ARM64 nightly producer inputs

This candidate selects the same 29 named outputs, components, platforms and Helm image bindings as
`arm64-nvidia-tcg.json`. The CLI, controller image, controller chart and NKTP chart
use the profile-owned [site catalog](../../catalogs/nvke/nightly-site-arm64/README.md); installer tools use the separate
compatibility catalog described below. Other output selections retain their explicit catalogs. The root
`../../catalogs/nvke/module-tags` supplies only the existing pinned GitHub Actions, Syft and cosign tools; it does not
replace an output's producer selection. Acquired inputs and the mixed SPIRE source remain explicit.

`../../catalogs/tools/installer-hosted-arm64` preserves the original clusterctl, kind, kubectl and ORAS source pins and
build commands, declaring their actual executable outputs as `host-package`. Helm is the explicit
acquired exception: the same upstream 4.2.4 ARM64 release, pinned by the catalog owner to its archive
and executable hashes. Hosted source fetching rejects Helm's external test symlinks before its local
diagnostic preparation could remove them. The profile does not weaken that fetch boundary or claim
that acquired Helm is source-built. See the [tool evidence and security finding](../../catalogs/tools/installer-hosted-arm64/README.md).

The disk selects the base M2 catalog's required-KVM ARM64 producer. `../../catalogs/node-images/variants/nvidia-kvm.json`
retains three NVIDIA OVN replacements and their exact predecessor catalog digest, without the TCG-only
disk replacement. These embedded producers still select 1.5.18; the local TCG overlay, standalone OVN
outputs and selected profiles use 2.4.0. This unresolved content mismatch is preserved by the ownership
move and requires a reviewed producer update before claiming matched NVIDIA runtime inputs.
The alternative upstream OVN profile is authored separately in
`arm64-upstream-kvm.json`; there is no automatic fallback between them.

`../../environments/github-nightly-nvidia.json` selects the admitted native ARC KVM route and the M2 candidate's ARM64
resource budgets, with `build-provenance-v1` disk evidence and `per-output-v1` signed publication.
The workflow namespace is `nvke-nightly-arm64`; its exact main-branch signing identity is declared in
the Environment. Publication uses `ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64`.
These are authored candidate settings, not evidence of available runners, protected-environment
admission, completed publication, runtime compatibility, FIPS/STIG compliance or qualification.
The exact private OVN fork and NVKE source-access bindings pass the existing
`NKE_SOURCE_TOKEN` only to their selected fetch steps. Public upstream OVN does
not receive that binding.

Generate the initial reviewed workflow contract through the existing owner:

```sh
bin/nvkr definition validate --definition inputs/definitions/demo/arm64-nvidia-kvm.json
bin/nvkr run generate \
  --definition inputs/definitions/demo/arm64-nvidia-kvm.json \
  --environment inputs/environments/github-nightly-nvidia.json
```

The Environment selects `githubRuntimePlan: true`. Routine catalog-pin updates are compiled on each
producer dispatch and do not require another regeneration commit. The producer freezes one Plan for
all jobs; `run generate --runtime --check` validates its unchanged workflow/action and Environment
contract without writing files. Changes to that contract require normal regeneration and review.

This generated workflow remains a manually dispatched publication diagnostic.
The scheduled [nightly Go test](../../../docs/nvke-nightly.md) builds latest NVKE
main and bootstraps a site locally in each architecture job; it does not dispatch
this producer or consume its publication. Definition validation and generation
do not establish a successful build, VM runtime or hardware acceptance.
