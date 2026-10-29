// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
)

func assertNativePublicationCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestNativePublicationRequiresPlainSuccessfulDiskPayload(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*lifecycle.Artifact)
	}{
		{"missing disk", func(o *lifecycle.Artifact) { o.Disk = nil }},
		{"RAW format", func(o *lifecycle.Artifact) { o.Disk.Format = "raw" }},
		{"encrypted", func(o *lifecycle.Artifact) { o.Disk.Encryption = "luks2" }},
		{"undeclared encryption", func(o *lifecycle.Artifact) { o.Disk.Encryption = "" }},
		{"failed producer", func(o *lifecycle.Artifact) { o.Disk.Outcome = "failed" }},
		{"empty payload", func(o *lifecycle.Artifact) { o.Disk.Size = 0 }},
		{"negative size", func(o *lifecycle.Artifact) { o.Disk.Size = -1 }},
		{"missing payload digest", func(o *lifecycle.Artifact) { o.Disk.PayloadDigest = "" }},
		{"malformed payload digest", func(o *lifecycle.Artifact) { o.Disk.PayloadDigest = "sha256:invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := lifecycle.Artifact{Digest: "sha256:" + strings.Repeat("a", 64), Disk: &delivery.DiskOutput{
				PayloadDigest: "sha256:" + strings.Repeat("b", 64), Size: 1024, Format: "qcow2", Encryption: "none", Outcome: "succeeded",
			}}
			payload, err := nativePublicationDiskPayload(output)
			if err != nil || payload.PayloadDigest != output.Disk.PayloadDigest || payload.PayloadDigest == output.Digest || payload.Size != output.Disk.Size {
				t.Fatal("plain payload admission conflated raw QCOW and publication wrapper", payload, err)
			}
			test.edit(&output)
			payload, err = nativePublicationDiskPayload(output)
			assertNativePublicationCode(t, err, e.CodeVerifyFailed)
			if payload != (delivery.DiskOutput{}) {
				t.Fatal("rejected disk returned a runtime payload description", payload)
			}
		})
	}
}

func TestNativePublicationAliasesSatisfyNativeDeploymentConsumers(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	const source = "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:source"
	capnico, err := name.NewTag(source, name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			cfg := startupDescription()
			cfg.arch, cfg.OVNVersion = arch, siteOVNVersion
			addStartupRuntimeImages(&cfg)
			cfg.imageTags = map[string]string{"capnico": source}
			for role := range cfg.ImageImports {
				cfg.ImageImports[role] = nativePublicationImageAlias(role, digest, capnico.Context().Name())
			}
			cfg.ControllerImage = cfg.ImageImports["controller"]
			cfg.ProviderImage = nativePublicationImageAlias("provisioned-provider", digest, capnico.Context().Name())
			cfg.OVNImage = nativePublicationImageAlias("ovn", digest, capnico.Context().Name())
			if _, err := nativeOVNValues(siteOVNVersion, cfg.OVNImage); err != nil {
				t.Fatal("publication OVN alias rejected by the deployment values owner", err)
			}
			if _, err := SiteStartupUserData(startupTestContext(t), cfg); err != nil {
				t.Fatal("publication image aliases rejected by the native startup owner", err)
			}
		})
	}
}

func TestNativePublicationRequiresLiveCapabilities(t *testing.T) {
	for _, test := range []struct {
		name     string
		verified *lifecycle.VerifiedRelease
		receipt  *lifecycle.ExactInputReceipt
	}{{"missing", nil, nil}, {"unissued release", &lifecycle.VerifiedRelease{}, nil}, {"unissued receipt", &lifecycle.VerifiedRelease{}, &lifecycle.ExactInputReceipt{}}} {
		t.Run(test.name, func(t *testing.T) {
			inputs, err := newNativePublicationInputs(t.Context(), test.verified, test.receipt, &factory.Plan{}, "https://github.com/example/nvkr/actions/runs/1/attempts/1", "amd64")
			assertNativePublicationCode(t, err, e.CodeGateUnmet)
			if inputs != nil {
				t.Fatal("unissued capabilities produced native inputs")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := newNativePublicationInputs(ctx, nil, nil, nil, "", "amd64"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled constructor reached capabilities", err)
	}
}

func TestNativePublicationRejectsSiblingAndChangedOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*nativePublicationInputs)
	}{
		{"sibling with same build", func(in *nativePublicationInputs) { delete(in.outputs, "nvkectl") }},
		{"name substituted", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Name = "sibling"
			in.outputs["nvkectl"] = out
		}},
		{"different build", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.BuildDigest = "different"
			in.outputs["nvkectl"] = out
		}},
		{"different members", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Components = []string{"sibling"}
			in.outputs["nvkectl"] = out
		}},
		{"different architecture", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Arch = "arm64"
			in.outputs["nvkectl"] = out
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := lifecycle.Artifact{Name: "nvkectl", Kind: "executable", OS: "linux", Arch: "amd64", BuildDigest: "selected", Components: []string{"nvkectl"}}
			in := &nativePublicationInputs{arch: "amd64", outputs: map[string]lifecycle.Artifact{"nvkectl": output, "sibling": output},
				plan: &factory.Plan{Builds: []factory.BuildPlan{{Name: "nvkectl", BuildDigest: "selected", Platform: "linux/amd64", Components: []string{"nvkectl"}}}}}
			if got, err := in.selected("nvkectl", "executable"); err != nil || got.Name != "nvkectl" {
				t.Fatal("exact output rejected", err)
			}
			test.edit(in)
			_, err := in.selected("nvkectl", "executable")
			assertNativePublicationCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestNativePublicationRequiresCoherentSourceProducts(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name     string
			edit     func(*catalog.Component)
			accepted bool
		}{
			{"same source", func(*catalog.Component) {}, true},
			{"different NVKE commit", func(c *catalog.Component) { c.Commit = strings.Repeat("c", 40) }, false},
			{"different NVKE tree", func(c *catalog.Component) { c.TreeHash = strings.Repeat("d", 40) }, false},
			{"independent acquisition", func(c *catalog.Component) { c.FromSource = false; c.Commit = strings.Repeat("e", 40) }, true},
			{"independent source", func(c *catalog.Component) { c.Repo = "https://example.invalid/ovn"; c.Commit = strings.Repeat("e", 40) }, true},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				source := catalog.Component{FromSource: true, Repo: "https://example.invalid/nvke.git", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1}
				host := factory.BuildPlan{Name: "native-tools-linux-" + arch, BuildDigest: "host-build", Platform: "linux/" + arch}
				for _, key := range []string{"nvke-installer-sources", "nvkectl", "nico-sim", "nico-simctl", "clusterctl-linux"} {
					component := source
					if key == "clusterctl-linux" {
						component = catalog.Component{Tag: "v1.13.6"}
					}
					host.ResolvedComponents = append(host.ResolvedComponents, catalog.NamedComponent{Name: key, Component: component})
				}
				product := source
				product.Repo = strings.TrimSuffix(product.Repo, ".git")
				test.edit(&product)
				in := &nativePublicationInputs{arch: arch, outputs: map[string]lifecycle.Artifact{host.Name: {Name: host.Name, BuildDigest: host.BuildDigest}},
					plan: &factory.Plan{Builds: []factory.BuildPlan{host, {Name: "cwe", ResolvedComponents: []catalog.NamedComponent{{Name: "controller", Component: product}}}}}}
				selected, err := in.installer()
				if test.accepted {
					if err != nil || selected.arch != arch || selected.commit != source.Commit {
						t.Fatal("coherent source rejected", err)
					}
				} else {
					assertNativePublicationCode(t, err, e.CodeVerifyFailed)
				}
			})
		}
	}
}
