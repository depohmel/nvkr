// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

// TestNativeNightlyCurrentSourceContract executes the authored installer producer
// against a private export of the cycle's exact NVKE source. It validates source
// compatibility only; no producer capability, image execution or release is faked.
func TestNativeNightlyCurrentSourceContract(t *testing.T) {
	source := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT")
	if source == "" {
		t.Skip("set NVKR_NVKE_SOURCE_CONTRACT and NVKR_NVKE_SOURCE_COMMIT for the exact source contract")
	}
	commit := os.Getenv("NVKR_NVKE_SOURCE_COMMIT")
	if commit == "" {
		t.Fatal("the exact cycle source commit is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run := func(t *testing.T, directory string, argv ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, argv[0], argv[1:]...)
		command.Dir = directory
		var raw bytes.Buffer
		command.Stdout, command.Stderr = &raw, &raw
		if err := ownedcmd.Run(ctx, command); err != nil {
			t.Fatalf("source contract %q: %v: %s", argv, err, raw.String())
		}
		return strings.TrimSpace(raw.String())
	}
	if actual := run(t, source, "git", "rev-parse", "HEAD"); actual != commit {
		t.Fatalf("source checkout is %s, expected exact cycle %s", actual, commit)
	}
	epoch := run(t, source, "git", "show", "-s", "--format=%ct", commit)
	if !strings.Contains(run(t, source, "tar", "--version"), "GNU tar") {
		t.Fatal("the authored Linux producer requires GNU tar")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			loaded, err := catalog.LoadContext(ctx, catalog.Dir(filepath.Join("..", "..", "inputs", "catalogs", "tools", "nightly-bootstrap-"+arch)))
			if err != nil {
				t.Fatal(err)
			}
			producer, exists := loaded.Components["nvke-installer-sources"]
			if !exists || !producer.FromSource || len(producer.Patches) != 0 || producer.ArtifactPath != "bin/nvke-installer-sources.tar.gz" || producer.WriteDir != "bin" {
				t.Fatal("nightly installer source producer changed its output contract")
			}
			private := t.TempDir()
			archive := filepath.Join(private, "source.tar")
			// Git owns the exact source export; tar executes only inside this fresh
			// private directory. The original checkout is never patched or built.
			run(t, source, append([]string{"git", "archive", "--format=tar", "--output=" + archive, commit}, nativeInstallerContractInputs...)...)
			run(t, private, "tar", "-xf", archive)
			if err := os.Mkdir(filepath.Join(private, producer.WriteDir), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, producer.BuildCommand[0], producer.BuildCommand[1:]...)
			command.Dir = private
			command.Env = SetEnv(os.Environ(), map[string]string{"NKECTL_COMMIT": commit, "SOURCE_DATE_EPOCH": epoch})
			var raw bytes.Buffer
			command.Stdout, command.Stderr = &raw, &raw
			if err := ownedcmd.Run(ctx, command); err != nil {
				t.Fatalf("actual nightly installer producer failed: %v: %s", err, raw.String())
			}
			tools, err := catalog.LoadContext(ctx, catalog.Dir(filepath.Join("..", "..", "inputs", "catalogs", "tools", "installer-hosted-"+arch)))
			if err != nil {
				t.Fatal(err)
			}
			installer := filepath.Join(private, "installer")
			if err := os.Mkdir(installer, 0700); err != nil {
				t.Fatal(err)
			}
			selected, err := extractNativeInstaller(ctx, filepath.Join(private, producer.ArtifactPath), installer,
				&nativeInstallerSelection{repo: "https://github.com/dsx-ai-factory/nvke.git", commit: commit, capiVersion: tools.Components["clusterctl"].Tag, arch: arch})
			if err != nil {
				t.Fatalf("actual source archive does not satisfy native %s installer: %v", arch, err)
			}
			if _, err := nativeKubernetesVersion(ctx, NativeAssets{Installer: installer, installerSelection: selected}); err != nil {
				t.Fatal(err)
			}
			assets := NativeAssets{Installer: installer, Images: map[string]NativeImageAsset{}}
			acquired := make(map[string]string)
			for _, role := range nativeCAPIAuthImages {
				component := loaded.Components["native-"+role+"-image"]
				if component.FromSource || component.Prebuilt == nil || component.Prebuilt.Digests[arch] == "" {
					t.Fatalf("native %s acquisition omits its pinned %s child", role, arch)
				}
				acquired[role] = component.Prebuilt.Image
				assets.Images[role] = NativeImageAsset{Reference: "docker.io/nvkr-local/" + role + "@" + component.Prebuilt.Digests[arch], ManifestDigest: component.Prebuilt.Digests[arch]}
			}
			// This existing consumer owns full YAML parsing, exact source parent
			// matching and the bounded local overlay transformation for all four
			// controllers. Descriptive image locators make no byte/runtime claim.
			if err := prepareNativeCAPIAuth(ctx, assets, acquired); err != nil {
				t.Fatalf("current source CAPI references differ from %s acquisitions: %v", arch, err)
			}
		})
	}
}

var nativeInstallerContractInputs = []string{
	"make/tool-versions.mk", "deploy/nvke-deploy.sh", "deploy/lib", "deploy/nico",
	nativeSiteSpecPath, "controller/deploy/charts/controller", "nktp/deploy/charts/nktp",
	"controller/hack/apply-capi-spiffe-exec-auth.sh", "controller/deploy/k8s/capi-spiffe-exec-auth",
	"controller/test/fixtures/releases", "controller/nico-sim/internal/seed/default-seed.yaml", "controller/nico-sim/ui",
}
