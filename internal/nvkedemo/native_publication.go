// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
)

// These private inputs retain the existing owners' live capabilities. Neither
// a serialized Plan nor a directory/boolean can select this admission path.
type nativePublicationInputs struct {
	verified         *lifecycle.VerifiedRelease
	receipt          *lifecycle.ExactInputReceipt
	plan             *factory.Plan
	invocation, arch string
	outputs          map[string]lifecycle.Artifact
}

// Published wrapper membership and raw QCOW transport identity remain distinct.
// This binding never masquerades as a completed local Run observation.
type nativePublishedDisk struct {
	path   string
	output lifecycle.Artifact
}

func withPublishedNativeSite(ctx context.Context, cfg Config, verified *lifecycle.VerifiedRelease, receipt *lifecycle.ExactInputReceipt,
	plan *factory.Plan, invocation string, use func(context.Context, *NativeSite) error,
) error {
	if ctx == nil || use == nil {
		return configUsage("native published site requires context and a lifecycle callback")
	}
	if cfg.Conformance {
		return e.New(e.CodeEngineCapability, "published native smoke does not select conformance artifacts")
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "published nightly native site requires Linux")
	}
	mode, err := nativeDeploymentNetwork(cfg.Network)
	if err != nil || mode != "ovn" {
		return e.New(e.CodeEngineCapability, "published native site requires the NVIDIA OVN selection")
	}
	inputs, err := newNativePublicationInputs(ctx, verified, receipt, plan, invocation, runtime.GOARCH)
	if err != nil {
		return nativeSessionError(err, "published native inputs")
	}
	accelerator, err := nativeAccelerator(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator)
	if err != nil {
		return err
	}
	cfg.Cleanup = true
	if err := AdmitDemoDeployment(ctx, cfg); err != nil {
		return err
	}
	selected := NativeConfig{Out: cfg.Out, Accelerator: accelerator, Network: mode, DemoExecutable: cfg.DemoExecutable,
		TenantWorkers: cfg.TenantWorkers, TenantTimeout: cfg.Timeout, Resources: cfg.Resources, Cleanup: true,
		Progress: cfg.Progress, publication: inputs}
	return nativeSessionError(runNativeSite(ctx, selected, func(ctx context.Context, site *NativeSite) error {
		return errors.Join(use(ctx, site), ctx.Err())
	}), "published native site scope")
}

func newNativePublicationInputs(ctx context.Context, verified *lifecycle.VerifiedRelease, receipt *lifecycle.ExactInputReceipt,
	plan *factory.Plan, invocation, arch string,
) (*nativePublicationInputs, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if arch != "arm64" && arch != "amd64" {
		return nil, e.New(e.CodeEngineCapability, "published native site requires an admitted native architecture")
	}
	if strings.TrimSpace(invocation) == "" || plan == nil {
		return nil, e.New(e.CodeUsage, "published native inputs require frozen Plan and producing invocation")
	}
	description, err := verified.Description()
	if err != nil {
		return nil, err
	}
	exact, err := receipt.Description()
	if err != nil {
		return nil, err
	}
	// Freeze the descriptive Plan before validating its identity or projecting
	// roles; subsequent caller mutations cannot change accepted selections.
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var frozen factory.Plan
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return nil, err
	}
	if err := frozen.Validate(); err != nil {
		return nil, err
	}
	digest, err := frozen.Digest()
	if err != nil {
		return nil, err
	}
	if description.Lane != lifecycle.LanePipeline || description.Scope != lifecycle.ScopeArtifactPublication ||
		frozen.RuntimeSourceCommit == "" || frozen.Environment.GitHubRuntimeSource == nil || exact.PlanDigest != digest ||
		exact.ReleaseSubject != description.Subject || exact.PolicyDigest != description.PolicyDigest ||
		exact.DefinitionDigest != frozen.DefinitionDigest || exact.EnvironmentDigest != frozen.EnvironmentDigest {
		return nil, e.New(e.CodeVerifyFailed, "native publication differs from its exact frozen source inputs")
	}
	outputs := make(map[string]lifecycle.Artifact, len(description.Artifacts))
	for _, output := range description.Artifacts {
		if _, duplicate := outputs[output.Name]; duplicate {
			return nil, e.New(e.CodeVerifyFailed, "native publication repeats named output %q", output.Name)
		}
		outputs[output.Name] = output
	}
	inputs := &nativePublicationInputs{verified: verified, receipt: receipt, plan: &frozen, invocation: invocation, arch: arch, outputs: outputs}
	required := nativePublicationRequiredOutputs(arch)
	for _, output := range slices.Sorted(maps.Keys(required)) {
		if _, err := inputs.selected(output, required[output]); err != nil {
			return nil, err
		}
	}
	return inputs, nil
}

func nativePublicationRequiredOutputs(arch string) map[string]string {
	outputs := map[string]string{"nvkectl": "executable", "installer-tools-" + arch: "executable", "nicosim-tools-" + arch: "executable",
		"site-startup-tools-" + arch: "executable", "installer-sources-" + arch: "executable", "nvke-node-image-" + arch: "disk-image",
		"cwe": "oci", "controller-profiles": "oci", "capnico-controller-" + arch: "oci", "cluster-api-provider-provisioned-" + arch: "oci",
		"ovn-image-" + arch: "oci", "workload-image-" + arch: "oci", "capi-exec-init-image-" + arch: "oci", "capi-exec-plugin-image-" + arch: "oci"}
	for _, role := range runtimeImages {
		outputs[role+"-image-"+arch] = "oci"
	}
	return outputs
}

func (inputs *nativePublicationInputs) selected(output, format string) (lifecycle.Artifact, error) {
	observed, exists := inputs.outputs[output]
	if !exists || observed.Kind != format || observed.OS != "linux" || observed.Arch != inputs.arch {
		return lifecycle.Artifact{}, e.New(e.CodeVerifyFailed, "native publication omits exact %s %s output %q", inputs.arch, format, output)
	}
	for _, build := range inputs.plan.Builds {
		if build.Name == output && build.Platform == "linux/"+inputs.arch && build.BuildDigest == observed.BuildDigest && slices.Equal(build.Components, observed.Components) {
			return observed, nil
		}
	}
	return lifecycle.Artifact{}, e.New(e.CodeVerifyFailed, "native output %q differs from its original named producer", output)
}

func (inputs *nativePublicationInputs) component(output, component string) (catalog.Component, error) {
	observed, err := inputs.selected(output, "executable")
	if err != nil {
		return catalog.Component{}, err
	}
	for _, build := range inputs.plan.Builds {
		if build.Name != observed.Name {
			continue
		}
		for _, item := range build.ResolvedComponents {
			if item.Name == component {
				return item.Component, nil
			}
		}
	}
	return catalog.Component{}, e.New(e.CodeVerifyFailed, "native output %q omits selected component %q", output, component)
}

func (inputs *nativePublicationInputs) installer() (*nativeInstallerSelection, error) {
	installer, err := inputs.component("installer-sources-"+inputs.arch, "nvke-installer-sources")
	if err != nil {
		return nil, err
	}
	policy := inputs.plan.Environment.GitHubRuntimeSource
	if !installer.FromSource || len(installer.Patches) != 0 || installer.Commit != inputs.plan.RuntimeSourceCommit ||
		strings.TrimSuffix(installer.Repo, ".git") != policy.Repository {
		return nil, e.New(e.CodeVerifyFailed, "native installer differs from the frozen unmodified source")
	}
	for _, role := range []struct{ output, component string }{{"nvkectl", "nvkectl"}, {"nicosim-tools-" + inputs.arch, "nico-sim"}, {"nicosim-tools-" + inputs.arch, "nico-simctl"}} {
		component, err := inputs.component(role.output, role.component)
		if err != nil {
			return nil, err
		}
		if !component.FromSource || component.Commit != installer.Commit || component.TreeHash != installer.TreeHash ||
			strings.TrimSuffix(component.Repo, ".git") != policy.Repository {
			return nil, e.New(e.CodeVerifyFailed, "native installer and %s source selections differ", role.component)
		}
	}
	clusterctl, err := inputs.component("installer-tools-"+inputs.arch, "clusterctl")
	if err != nil {
		return nil, err
	}
	version, err := semver.StrictNewVersion(strings.TrimPrefix(clusterctl.Tag, "v"))
	if err != nil || clusterctl.Tag != "v"+version.String() || version.Prerelease() != "" || version.Metadata() != "" {
		return nil, e.New(e.CodeVerifyFailed, "native installer requires exact stable selected clusterctl")
	}
	return &nativeInstallerSelection{repo: installer.Repo, commit: installer.Commit, sourceDate: installer.SourceDate, capiVersion: clusterctl.Tag, arch: inputs.arch}, nil
}

func (inputs *nativePublicationInputs) prepare(ctx context.Context, out string) (assets NativeAssets, disk nativePublishedDisk, result error) {
	if err := requireContext(ctx); err != nil {
		return assets, disk, err
	}
	selection, err := inputs.installer()
	if err != nil {
		return assets, disk, err
	}
	for _, selected := range []struct{ output, component string }{{"site-startup-tools-" + inputs.arch, nativeIdentityHelper}, {"nvke-node-image-" + inputs.arch, "nvke-spire-agent"}} {
		output := inputs.outputs[selected.output]
		if _, err := nativeIdentityComponentForPlatform(inputs.plan, selected.output, output.BuildDigest, selected.component, "linux/"+inputs.arch); err != nil {
			return assets, disk, err
		}
	}
	if _, err := freshDestination(out); err != nil {
		return assets, disk, err
	}
	root := out
	// This existing owner authenticates both receipt ownership and the original
	// aggregate invocation before exposing any publication payload bytes.
	if err := inputs.verified.ExportForInvocation(ctx, inputs.receipt, inputs.invocation, root); err != nil {
		return assets, disk, err
	}
	description, err := inputs.verified.Description()
	if err != nil {
		return assets, disk, err
	}
	_, digest, ok := strings.Cut(description.Subject, "@")
	if !ok {
		return assets, disk, e.New(e.CodeVerifyFailed, "verified native publication lacks its immutable root")
	}
	publication, err := delivery.OpenPublication(ctx, root, digest)
	if err != nil {
		return assets, disk, err
	}
	defer func() { result = errors.Join(result, publication.Close()) }()
	assets = NativeAssets{arch: inputs.arch, HostTools: filepath.Join(out, "host"), SiteTools: filepath.Join(out, "site"), Installer: filepath.Join(out, "installer"),
		Files: filepath.Join(out, "files"), Images: make(map[string]NativeImageAsset), RuntimeOutputs: make(map[string]SelectedOutput), importTags: make(map[string]string), publishedOutputs: inputs.outputs}
	for _, directory := range []string{assets.HostTools, assets.Installer, assets.Files} {
		if err := os.Mkdir(directory, 0700); err != nil {
			return assets, disk, err
		}
	}
	for _, output := range []string{"nvkectl", "installer-tools-" + inputs.arch, "nicosim-tools-" + inputs.arch, "installer-sources-" + inputs.arch} {
		if err := extractNativePublishedHostBundle(ctx, publication, output, inputs.arch, filepath.Join(out, output), assets.HostTools); err != nil {
			return assets, disk, err
		}
	}
	startup, err := publication.ExtractBinaryBundle(ctx, "site-startup-tools-"+inputs.arch, assets.SiteTools)
	if err != nil {
		return assets, disk, err
	}
	if startup.OS != "linux" || startup.Arch != inputs.arch || len(startup.Entries) != 2 {
		return assets, disk, e.New(e.CodeVerifyFailed, "native startup bundle has the wrong platform or membership")
	}
	if _, err := sitePreparationMembers(inputs.outputs["site-startup-tools-"+inputs.arch].Components); err != nil {
		return assets, disk, err
	}
	if err := verifyToolMembers(assets.SiteTools, []string{"nerdctl", nativeIdentityHelper}); err != nil {
		return assets, disk, err
	}
	if err := copyPrivate(ctx, filepath.Join(assets.SiteTools, "nerdctl"), filepath.Join(assets.HostTools, "nerdctl"), 0755); err != nil {
		return assets, disk, err
	}
	if err := verifyToolMembers(assets.HostTools, []string{"clusterctl", "helm", "kind", "kubectl", "nerdctl", "nico-sim", "nico-simctl", "nvke-installer-sources", "nvkectl", "oras"}); err != nil {
		return assets, disk, err
	}
	assets.CLI = filepath.Join(assets.HostTools, "nvkectl")
	assets.installerSelection, err = extractNativeInstaller(ctx, filepath.Join(assets.HostTools, "nvke-installer-sources"), assets.Installer, selection)
	if err != nil {
		return assets, disk, err
	}
	if err := inputs.prepareImages(ctx, publication, &assets); err != nil {
		return assets, disk, err
	}
	if err := prepareNativeIdentityAssets(ctx, &assets); err != nil {
		return assets, disk, err
	}
	disk.output = inputs.outputs["nvke-node-image-"+inputs.arch]
	if disk.output.Disk == nil || disk.output.Disk.Encryption != "none" || disk.output.Disk.Format != "qcow2" || disk.output.Disk.Outcome != "succeeded" {
		return assets, disk, e.New(e.CodeVerifyFailed, "native publication requires successful plain QCOW2 membership")
	}
	disk.path = filepath.Join(out, "node.qcow2")
	file, err := os.OpenFile(disk.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return assets, disk, err
	}
	written, writeErr := publication.WriteDiskPayload(ctx, disk.output.Name, file)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return assets, disk, err
	}
	if written != disk.output.Disk.Size {
		return assets, disk, e.New(e.CodeVerifyFailed, "native QCOW payload differs from its published size")
	}
	actual, err := DigestFile(ctx, disk.path)
	if err != nil || actual != disk.output.Disk.PayloadDigest {
		return assets, disk, errors.Join(err, e.New(e.CodeVerifyFailed, "native raw QCOW differs from its published payload digest"))
	}
	return assets, disk, nil
}

func extractNativePublishedHostBundle(ctx context.Context, publication *delivery.Publication, output, arch, destination, host string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	bundle, err := publication.ExecutableBundle(output)
	if err != nil {
		return err
	}
	if bundle.OS != "linux" || bundle.Arch != arch {
		return e.New(e.CodeVerifyFailed, "native host bundle %q has the wrong platform", output)
	}
	var members []string
	switch output {
	case "nvkectl":
		members = []string{"nvkectl"}
	case "installer-tools-" + arch:
		members = []string{"clusterctl", "helm", "kind", "kubectl", "oras"}
	case "nicosim-tools-" + arch:
		members = []string{"nico-sim", "nico-sim-runtime-assets", "nico-simctl"}
	case "installer-sources-" + arch:
		members = []string{"nvke-installer-sources"}
	default:
		return e.New(e.CodeVerifyFailed, "native host view does not select bundle %q", output)
	}
	if len(bundle.Entries) != len(members) {
		return e.New(e.CodeVerifyFailed, "native host bundle %q has incomplete or extra membership", output)
	}
	for _, entry := range bundle.Entries {
		if !filepath.IsLocal(entry.Name) || filepath.Base(entry.Name) != entry.Name {
			return e.New(e.CodeVerifyFailed, "native host bundle %q contains a non-flat member", output)
		}
		mode := nativeToolMemberMode(entry.Name)
		if entry.Name == "nico-sim-runtime-assets" {
			mode = 0644
		}
		if !slices.Contains(members, entry.Name) || os.FileMode(entry.Mode) != mode {
			return e.New(e.CodeVerifyFailed, "native host bundle %q has an unexpected member or delivered mode", output)
		}
	}
	if _, err := publication.ExtractBinaryBundle(ctx, output, destination); err != nil {
		return err
	}
	for _, entry := range bundle.Entries {
		role := entry.Name
		if role == "nico-sim-runtime-assets" {
			continue // The complete source installer archive owns these same assets.
		}
		if err := copyPrivate(ctx, filepath.Join(destination, entry.Name), filepath.Join(host, role), os.FileMode(entry.Mode)); err != nil {
			return err
		}
		copied, err := DigestFile(ctx, filepath.Join(host, role))
		if err != nil || copied != entry.SHA256 {
			return errors.Join(err, e.New(e.CodeVerifyFailed, "native host member differs from held publication bytes"))
		}
	}
	return nil
}

func (inputs *nativePublicationInputs) prepareImages(ctx context.Context, publication *delivery.Publication, assets *NativeAssets) error {
	capnico, err := nativeCAPNICoReference(ctx, assets.Installer)
	if err != nil {
		return err
	}
	capnicoTag, err := name.NewTag(capnico, name.StrictValidation)
	if err != nil {
		return err
	}
	roles := map[string]string{"controller": "cwe", "profiles": "controller-profiles", "ovn": "ovn-image-" + inputs.arch,
		"capnico": "capnico-controller-" + inputs.arch, "provisioned-provider": "cluster-api-provider-provisioned-" + inputs.arch,
		"workload": "workload-image-" + inputs.arch, "capi-exec-init": "capi-exec-init-image-" + inputs.arch, "capi-exec-plugin": "capi-exec-plugin-image-" + inputs.arch}
	for _, role := range runtimeImages {
		roles["runtime-"+role] = role + "-image-" + inputs.arch
	}
	acquired := make(map[string]string)
	for _, role := range slices.Sorted(maps.Keys(roles)) {
		output := inputs.outputs[roles[role]]
		alias := "docker.io/nvkr-local/" + strings.TrimPrefix(role, "runtime-") + "@" + output.Digest
		if role == "capnico" {
			alias = capnicoTag.Context().Name() + "@" + output.Digest
			assets.importTags[role] = capnico
		}
		reference, err := name.NewDigest(alias, name.StrictValidation)
		if err != nil {
			return err
		}
		archive := filepath.Join(assets.Files, role+".tar")
		exported, err := publication.ExportImage(ctx, output.Name, reference.Name(), "linux/"+inputs.arch, archive)
		if err != nil {
			return err
		}
		if exported.ManifestDigest != output.Digest {
			return e.New(e.CodeVerifyFailed, "native image export changed named output %q", output.Name)
		}
		assets.Images[role] = NativeImageAsset{File: archive, Reference: alias, ManifestDigest: exported.ManifestDigest}
		if strings.HasPrefix(role, "runtime-") {
			selected := SelectedOutput{Observation: output}
			if strings.HasPrefix(role, "runtime-temporal-") {
				selected.imageTag, err = runtimeImageTag(inputs.plan, output)
				if err != nil {
					return err
				}
			}
			assets.RuntimeOutputs[role] = selected
		}
		if slices.Contains(nativeCAPIAuthImages, role) {
			parent, err := nativePublishedAcquiredImage(inputs.plan, output, inputs.arch, role)
			if err != nil {
				return err
			}
			acquired[role] = parent.String()
		}
	}
	// The same-main installer owns expected CAPI references. Preserve independent
	// acquisition provenance; compare exact external parents and native children.
	if err := prepareNativeCAPIAuth(ctx, *assets, acquired); err != nil {
		return err
	}
	return nil
}

func nativePublishedAcquiredImage(plan *factory.Plan, output lifecycle.Artifact, arch, role string) (name.Digest, error) {
	for _, build := range plan.Builds {
		if build.Name != output.Name || build.BuildDigest != output.BuildDigest || build.Platform != "linux/"+arch {
			continue
		}
		if len(build.ResolvedComponents) != 1 {
			return name.Digest{}, e.New(e.CodeVerifyFailed, "native CAPI image requires one selected acquisition")
		}
		component := build.ResolvedComponents[0]
		if component.Name != "native-"+role+"-image" || component.FromSource || component.Prebuilt == nil || len(component.Patches) != 0 {
			return name.Digest{}, e.New(e.CodeVerifyFailed, "native CAPI image differs from its immutable acquisition")
		}
		parent, err := name.NewDigest(component.Prebuilt.Image, name.StrictValidation)
		if err != nil || parent.Name() != component.Prebuilt.Image || parent.DigestStr() != component.Prebuilt.ImageParentDigest || component.Prebuilt.Digests[arch] != output.Digest {
			return name.Digest{}, e.New(e.CodeVerifyFailed, "native CAPI parent or selected child differs from publication")
		}
		return parent, nil
	}
	return name.Digest{}, e.New(e.CodeVerifyFailed, "native CAPI output %q has no exact selected acquisition", output.Name)
}
