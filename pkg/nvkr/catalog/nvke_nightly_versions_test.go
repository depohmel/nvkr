// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
)

func TestNVKENightlyChartVersionsBindCommitWithoutLabelUnsafeMetadata(t *testing.T) {
	// NVKE's source-owned Helm helper copies Chart.AppVersion directly into
	// app.kubernetes.io/version. Keep these exact rendered bytes usable there;
	// SemVer build metadata's '+' must not be introduced by a stamp template.
	commit := strings.Repeat("a", 40)
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			base, err := LoadContext(t.Context(), Dir(filepath.Join("../../../inputs/catalogs/nvke", "nightly-site-"+architecture)))
			if err != nil {
				t.Fatal(err)
			}
			stack, err := ApplyStack(base)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := ApplySourceRevision(stack, &SourceRevision{repository: "https://github.com/dsx-ai-factory/nvke", issued: true,
				pin: Pin{Tag: commit, Commit: commit, TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1733939955}})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"nvke-controller-chart", "nktp-chart"} {
				producer := frozen.Catalog.Components[name]
				version := producer.VersionStamp["BUILD_VERSION"]
				if version != "0.0.0-nightly.g"+commit || producer.Commit != commit {
					t.Fatalf("%s changed the exact commit-bound chart/label version: %q", name, version)
				}
				if _, err := semver.StrictNewVersion(version); err != nil {
					t.Fatalf("%s stamp is not a strict chart SemVer: %v", name, err)
				}
			}
			if frozen.Catalog.Components["nvkectl"].VersionStamp["NVKECTL_VERSION"] != "v0.0.0-nightly+"+commit ||
				frozen.Catalog.Components["nvke-controller"].VersionStamp["BUILD_VERSION"] != "v0.0.0-nightly+"+commit {
				t.Fatal("chart label compatibility changed CLI or controller version stamps")
			}
		})
	}
}
