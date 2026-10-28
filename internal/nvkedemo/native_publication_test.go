// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func assertNativePublicationCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestNativePublicationRequiresLiveCapabilitiesBeforeWork(t *testing.T) {
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

func TestNativePublicationPreservesIndependentNamedOutputMembership(t *testing.T) {
	for _, test := range []struct {
		name     string
		edit     func(*nativePublicationInputs)
		accepted bool
	}{
		{"exact", func(*nativePublicationInputs) {}, true},
		{"sibling with same build", func(in *nativePublicationInputs) { delete(in.outputs, "nvkectl") }, false},
		{"wrong build", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.BuildDigest = "different"
			in.outputs["nvkectl"] = out
		}, false},
		{"wrong members", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Components = []string{"sibling"}
			in.outputs["nvkectl"] = out
		}, false},
		{"cross architecture", func(in *nativePublicationInputs) {
			out := in.outputs["nvkectl"]
			out.Arch = "arm64"
			in.outputs["nvkectl"] = out
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := lifecycle.Artifact{Name: "nvkectl", Kind: "executable", OS: "linux", Arch: "amd64", BuildDigest: "selected", Components: []string{"nvkectl"}}
			in := &nativePublicationInputs{arch: "amd64", outputs: map[string]lifecycle.Artifact{"nvkectl": output, "sibling": output},
				plan: &factory.Plan{Builds: []factory.BuildPlan{{Name: "nvkectl", BuildDigest: "selected", Platform: "linux/amd64", Components: []string{"nvkectl"}}}}}
			test.edit(in)
			got, err := in.selected("nvkectl", "executable")
			if test.accepted {
				if err != nil || got.Name != "nvkectl" {
					t.Fatal("exact output rejected", err)
				}
			} else {
				assertNativePublicationCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestNativePublishedAcquisitionsPreserveIndependentProvenance(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name     string
			edit     func(*catalog.Component, *factory.BuildPlan)
			accepted bool
		}{
			{"exact independent source", func(*catalog.Component, *factory.BuildPlan) {}, true},
			{"substituted parent", func(c *catalog.Component, _ *factory.BuildPlan) {
				c.Prebuilt.ImageParentDigest = "sha256:" + strings.Repeat("c", 64)
			}, false},
			{"substituted child", func(c *catalog.Component, _ *factory.BuildPlan) {
				c.Prebuilt.Digests[arch] = "sha256:" + strings.Repeat("c", 64)
			}, false},
			{"patched image", func(c *catalog.Component, _ *factory.BuildPlan) { c.Patches = []catalog.Patch{{Name: "changed"}} }, false},
			{"wrong named producer", func(_ *catalog.Component, b *factory.BuildPlan) { b.Name = "sibling" }, false},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				parent, child := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
				component := catalog.Component{Repo: "https://example.invalid/independent-acquisition", Commit: strings.Repeat("e", 40), SourceDate: 7,
					Prebuilt: &catalog.Prebuilt{Image: "example.invalid/plugin@" + parent, ImageParentDigest: parent, Digests: map[string]string{arch: child}}}
				build := factory.BuildPlan{Name: "capi-exec-plugin-image-" + arch, BuildDigest: "selected", Platform: "linux/" + arch}
				test.edit(&component, &build)
				build.ResolvedComponents = []catalog.NamedComponent{{Name: "native-capi-exec-plugin-image", Component: component}}
				output := lifecycle.Artifact{Name: "capi-exec-plugin-image-" + arch, BuildDigest: "selected", Digest: child}
				got, err := nativePublishedAcquiredImage(&factory.Plan{Builds: []factory.BuildPlan{build}}, output, arch, "capi-exec-plugin")
				if test.accepted {
					if err != nil || got.DigestStr() != parent || component.Commit != strings.Repeat("e", 40) || component.Repo != "https://example.invalid/independent-acquisition" {
						t.Fatal("immutable independent acquisition was relabeled or rejected", err)
					}
				} else {
					assertNativePublicationCode(t, err, e.CodeVerifyFailed)
				}
			})
		}
	}
}

func TestNativePublishedInstallerJoinsOriginalOutputBuilds(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
			plan := &factory.Plan{RuntimeSourceCommit: commit, Environment: definition.Environment{GitHubRuntimeSource: &definition.GitHubRuntimeSource{Repository: "https://github.com/example/nvke", Ref: "refs/heads/main"}}}
			in := &nativePublicationInputs{arch: arch, plan: plan, outputs: map[string]lifecycle.Artifact{}}
			for _, role := range []struct {
				output     string
				components []string
			}{{"installer-sources-" + arch, []string{"nvke-installer-sources"}}, {"nvkectl", []string{"nvkectl"}}, {"nicosim-tools-" + arch, []string{"nico-sim", "nico-simctl"}}, {"installer-tools-" + arch, []string{"clusterctl"}}} {
				build := factory.BuildPlan{Name: role.output, BuildDigest: "original-" + role.output, Platform: "linux/" + arch, Components: role.components}
				for _, key := range role.components {
					component := catalog.Component{FromSource: true, Repo: "https://github.com/example/nvke.git", Commit: commit, TreeHash: tree, SourceDate: 1}
					if key == "clusterctl" {
						component.Tag = "v1.13.6"
					}
					if key == "nico-sim" {
						component.Patches = []catalog.Patch{{Name: "declared-interface.patch"}}
					}
					build.ResolvedComponents = append(build.ResolvedComponents, catalog.NamedComponent{Name: key, Component: component})
				}
				plan.Builds = append(plan.Builds, build)
				in.outputs[role.output] = lifecycle.Artifact{Name: role.output, Kind: "executable", OS: "linux", Arch: arch, BuildDigest: build.BuildDigest, Components: role.components}
			}
			selected, err := in.installer()
			if err != nil || selected.arch != arch || selected.commit != commit {
				t.Fatal("cross-output installer selection failed", err)
			}
			plan.Builds[1].ResolvedComponents[0].Commit = strings.Repeat("c", 40)
			_, err = in.installer()
			assertNativePublicationCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestNativePublishedHostBundleExtractsHeldBytesAndRejectsWrongPlatform(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			root := privateTestDir(t)
			original := filepath.Join(root, "nvkectl")
			if err := os.WriteFile(original, []byte("exact published executable"), 0755); err != nil {
				t.Fatal(err)
			}
			bundleRoot, publicationRoot := filepath.Join(root, "bundle"), filepath.Join(root, "publication")
			built, err := delivery.BuildBinaryBundle(t.Context(), bundleRoot, delivery.BinaryBundleRequest{OS: "linux", Arch: arch, Purpose: "nvkectl", Epoch: 1,
				Files: []delivery.BinaryFileInput{{Name: "nvkectl", Path: original, InstallPath: "/usr/local/bin/nvkectl", Mode: 0755}}})
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := delivery.PayloadDescriptor(t.Context(), bundleRoot)
			if err != nil {
				t.Fatal(err)
			}
			build, payload := "sha256:"+plainSHA([]byte("selected build")), "sha256:"+plainSHA([]byte("exact published executable"))
			output := delivery.Output{Name: "nvkectl", Kind: "executable", Digest: descriptor.Digest, BuildDigest: build, Components: []string{"nvkectl"}}
			content, err := delivery.ContentDigest([]delivery.Output{output})
			if err != nil {
				t.Fatal(err)
			}
			bom, err := sbom.NewSubject(build, "nvkectl", payload)
			if err != nil {
				t.Fatal(err)
			}
			bom, err = sbom.ForSubject(bom, payload, "build-output", payload)
			if err != nil {
				t.Fatal(err)
			}
			document, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			semantic, err := bom.Digest()
			if err != nil {
				t.Fatal(err)
			}
			published, err := delivery.BuildPublicationRoot(t.Context(), publicationRoot, delivery.PublicationRequest{
				Release: &delivery.ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content},
				Objects: []delivery.ObjectInput{
					{Name: "nvkectl", Kind: "binary-bundle", Path: bundleRoot, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: built.SemanticDigest, BlobDigest: built.RootDigest, OS: "linux", Arch: arch, Layout: true,
						Output: &delivery.OutputBinding{Repository: "nvkectl", Format: "executable", BuildDigest: build, Components: output.Components, BuildSubjects: []delivery.BuildSubject{{Component: "nvkectl", Digest: payload}}}},
					{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: document,
						Evidence: &delivery.EvidenceBinding{Outputs: []string{"nvkectl"}, Component: "nvkectl", Stage: "build-output", SubjectKind: "file", SubjectDigest: payload}},
				}})
			if err != nil {
				t.Fatal(err)
			}
			publication, err := delivery.OpenPublication(t.Context(), publicationRoot, published.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			if err := os.WriteFile(original, []byte("changed producer workspace"), 0755); err != nil {
				t.Fatal(err)
			}
			host := filepath.Join(root, "host")
			if err := os.Mkdir(host, 0700); err != nil {
				t.Fatal(err)
			}
			extracted := filepath.Join(root, "extracted")
			if err := extractNativePublishedHostBundle(t.Context(), publication, "nvkectl", arch, extracted, host); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(host, "nvkectl"))
			if err != nil || string(got) != "exact published executable" {
				t.Fatalf("held bytes changed: %q %v", got, err)
			}
			wrong := "amd64"
			if arch == wrong {
				wrong = "arm64"
			}
			rejected := filepath.Join(root, "must-not-extract")
			assertNativePublicationCode(t, extractNativePublishedHostBundle(t.Context(), publication, "nvkectl", wrong, rejected, host), e.CodeVerifyFailed)
			if _, err := os.Stat(rejected); !os.IsNotExist(err) {
				t.Fatal("wrong-platform extraction created output", err)
			}
		})
	}
}
