// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

const nativeE2ERepository = "https://github.com/dsx-ai-factory/nvke"

// Freeze source selection in ordinary, captured Definition/overlay files. Local
// execution replays those exact inputs; no generated hosted Plan is involved.
// Catalogs, their pins, commands, and previously selected overlays stay intact.
func freezeNativeE2ESource(ctx context.Context, cfg BuildConfig, commit string, toolDefinitions ...string) (string, error) {
	if _, err := hex.DecodeString(commit); err != nil || len(commit) != 40 || strings.ToLower(commit) != commit {
		return "", e.New(e.CodeUsage, "nightly requires one full lowercase NVKE source commit")
	}
	if cfg.Definition == "" || cfg.Environment == "" || !safePhase(cfg.AttemptPrefix) {
		return "", e.New(e.CodeUsage, "nightly source selection requires explicit local inputs and a fresh attempt")
	}
	root, err := resolveExisting(cfg.Repository)
	if err != nil {
		return "", err
	}
	original := cfg.Definition
	if !filepath.IsAbs(original) {
		original = filepath.Join(root, original)
	}
	declared, err := definition.LoadDefinition(ctx, original)
	if err != nil {
		return "", err
	}
	// Nightly builds host and guest tools in this same local Run. Ordinary
	// developer/qualification Definitions retain their existing output scope.
	for _, path := range toolDefinitions {
		path = filepath.Join(root, path)
		tools, err := definition.LoadDefinition(ctx, path)
		if err != nil {
			return "", err
		}
		for _, output := range tools.Outputs {
			if slices.ContainsFunc(declared.Outputs, func(existing definition.DefinitionOutput) bool { return existing.Name == output.Name }) {
				continue
			}
			selected := tools.Catalog
			if output.Catalog != nil {
				selected = *output.Catalog
			}
			relocate := func(value string) (string, error) {
				if !filepath.IsAbs(value) {
					value = filepath.Join(filepath.Dir(path), value)
				}
				return filepath.Rel(filepath.Dir(original), value)
			}
			selected.Base, err = relocate(selected.Base)
			if err != nil {
				return "", err
			}
			selected.Overlays = slices.Clone(selected.Overlays)
			for i, overlay := range selected.Overlays {
				selected.Overlays[i], err = relocate(overlay)
				if err != nil {
					return "", err
				}
			}
			output.Catalog = &selected
			declared.Outputs = append(declared.Outputs, output)
		}
	}
	sort.Slice(declared.Outputs, func(i, j int) bool { return declared.Outputs[i].Name < declared.Outputs[j].Name })
	revision, err := catalog.ResolveSourceRevision(ctx, nativeE2ERepository, commit)
	if err != nil {
		return "", err
	}
	private := filepath.Join(root, ".nvkr", "nightly-inputs", cfg.AttemptPrefix)
	if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
		return "", err
	}
	if err := os.Mkdir(private, 0700); err != nil {
		return "", err
	}
	// Each distinct authored stack gets one source overlay, shared by all its
	// selected outputs. Existing overlay predecessors are never rewritten.
	selections := map[string]definition.Catalog{}
	freeze := func(selected definition.Catalog) (definition.Catalog, error) {
		key, err := canonical.Marshal(selected)
		if err != nil {
			return definition.Catalog{}, err
		}
		if prior, ok := selections[string(key)]; ok {
			return prior, nil
		}
		result, err := freezeNativeE2ECatalog(ctx, original, private, len(selections), selected, revision)
		if err == nil {
			selections[string(key)] = result
		}
		return result, err
	}
	declared.Catalog, err = freeze(declared.Catalog)
	if err != nil {
		return "", err
	}
	for i := range declared.Outputs {
		if selected := declared.Outputs[i].Catalog; selected != nil {
			updated, err := freeze(*selected)
			if err != nil {
				return "", err
			}
			declared.Outputs[i].Catalog = &updated
		}
	}
	raw, err := canonical.Marshal(declared)
	if err != nil {
		return "", err
	}
	path := filepath.Join(private, "definition.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	plan, _, err := factory.CaptureCompileFiles(ctx, root, filepath.ToSlash(relative), cfg.Environment, "dev")
	if err != nil {
		return "", err
	}
	matched := 0
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if component.FromSource && strings.TrimSuffix(component.Repo, ".git") == nativeE2ERepository {
				matched++
				if component.Commit != commit {
					return "", e.New(e.CodeUnpinnedSource, "nightly output %q does not select the frozen NVKE source", build.Name)
				}
			}
		}
	}
	if matched == 0 {
		return "", e.New(e.CodeUnpinnedSource, "nightly selection contains no source-built NVKE products")
	}
	return filepath.ToSlash(relative), nil
}

func freezeNativeE2ECatalog(ctx context.Context, original, destination string, index int, selected definition.Catalog, revision *catalog.SourceRevision) (definition.Catalog, error) {
	resolve := func(path string) string {
		if !filepath.IsAbs(path) {
			return filepath.Join(filepath.Dir(original), path)
		}
		return path
	}
	base := resolve(selected.Base)
	loaded, err := catalog.LoadContext(ctx, catalog.Dir(base))
	if err != nil {
		return definition.Catalog{}, err
	}
	result := definition.Catalog{}
	result.Base, err = filepath.Rel(destination, base)
	if err != nil {
		return result, err
	}
	var overlays []catalog.Overlay
	for _, path := range selected.Overlays {
		absolute := resolve(path)
		overlay, err := catalog.LoadOverlayContext(ctx, absolute)
		if err != nil {
			return result, err
		}
		overlays = append(overlays, overlay)
		relative, err := filepath.Rel(destination, absolute)
		if err != nil {
			return result, err
		}
		result.Overlays = append(result.Overlays, filepath.ToSlash(relative))
	}
	stack, err := catalog.ApplyStack(loaded, overlays...)
	if err != nil {
		return result, err
	}
	frozen, err := catalog.ApplySourceRevision(stack, revision)
	if err != nil {
		return result, err
	}
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
		Name: "nightly-nvke-main", RequiresCatalogDigest: stack.Digest, Components: map[string]catalog.ComponentReplacement{}}
	for name, component := range frozen.Catalog.Components {
		if component.FromSource && strings.TrimSuffix(component.Repo, ".git") == nativeE2ERepository {
			overlay.Components[name] = catalog.ComponentReplacement{Component: &component}
		}
	}
	// This reviewed acquisition differs between the retained tagged demo and
	// main. NVKE's installer still checks its exact parent reference before use.
	if current, ok := frozen.Catalog.Components["native-capi-exec-init-image-main"]; ok {
		overlay.Components["native-capi-exec-init-image"] = catalog.ComponentReplacement{Component: &current}
	}
	if len(overlay.Components) != 0 {
		if _, err := catalog.ApplyStack(stack.Catalog, overlay); err != nil {
			return result, err
		}
		raw, err := canonical.Marshal(overlay)
		if err != nil {
			return result, err
		}
		name := fmt.Sprintf("catalog-%02d.json", index)
		if err := os.WriteFile(filepath.Join(destination, name), raw, 0600); err != nil {
			return result, err
		}
		result.Overlays = append(result.Overlays, name)
	}
	result.Base = filepath.ToSlash(result.Base)
	return result, nil
}

// Real Git transport and ordinary captured-input replay exercise the nightly's
// source handoff without fetching an upstream repository or running producers.
func TestNativeE2ESourceFreezePreservesStacksAndReplays(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_DATE", "1733939955 +0000")
	t.Setenv("GIT_COMMITTER_DATE", "1733939955 +0000")
	source, root := t.TempDir(), privateTestDir(t)
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "-q")
	commit := func(contents string) string {
		t.Helper()
		writeTenantFixture(t, filepath.Join(source, "README"), []byte(contents), 0600)
		git("add", "README")
		git("-c", "user.name=Nightly Test", "-c", "user.email=nightly@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
		return git("rev-parse", "HEAD")
	}
	original := commit("original source")
	pin, err := catalog.ResolveCommitPin(t.Context(), catalog.Source{Repo: source}, original)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_AUTHOR_DATE", "1733939956 +0000")
	t.Setenv("GIT_COMMITTER_DATE", "1733939956 +0000")
	latest := commit("latest source")
	tree := git("rev-parse", "HEAD^{tree}")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+source+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", nativeE2ERepository)
	write := func(path string, value any) {
		t.Helper()
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		writeTenantFixture(t, filepath.Join(root, path), raw, 0600)
	}
	component := func(source string) map[string]any {
		return map[string]any{"fromSource": true, "source": source, "platforms": []string{"linux/amd64", "linux/arm64"},
			"outputType": "host-package", "artifactPath": "bin/tool", "writeDir": "bin", "buildCommand": []string{"sh", "-ec", "true"}}
	}
	toolchain := "golang@sha256:" + strings.Repeat("3", 64)
	write("catalog/registry.yaml", map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
		"metadata":   catalog.Metadata{Name: "nightly-source-fixture"},
		"sources":    map[string]catalog.Source{"nvke": {Repo: nativeE2ERepository, ToolchainImage: toolchain}, "external": {Repo: "https://example.invalid/dependency", ToolchainImage: toolchain}},
		"components": map[string]any{"nvkectl": component("nvke"), "installer": component("external"), "dependency": component("external")}})
	write("catalog/pins.lock.yaml", catalog.PinLock{APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: map[string]catalog.Pin{"nvke": pin, "external": pin}})
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	installer := base.Components["installer"]
	installer.Repo, installer.VersionStamp = nativeE2ERepository, map[string]string{"NVKECTL_COMMIT": "{{.Commit}}"}
	installer.BuildCommand = []string{"sh", "-ec", "printf existing-overlay"}
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "existing-producer", RequiresCatalogDigest: digest,
		Components: map[string]catalog.ComponentReplacement{"installer": {Component: &installer}}}
	write("config/existing.json", overlay)
	selected := definition.Catalog{Base: "../catalog", Overlays: []string{"existing.json"}}
	write("config/definition.json", definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind,
		Metadata: definition.Metadata{Name: "nightly-fixture"}, Catalog: selected,
		Outputs: []definition.DefinitionOutput{
			{Name: "cli", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"nvkectl"}},
			{Name: "dependency", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"dependency"}, Catalog: &definition.Catalog{Base: "../catalog"}},
			{Name: "installer", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"installer"}, Catalog: &selected},
		}})
	write("config/environment.json", definition.Environment{APIVersion: definition.EnvironmentAPIVersion, Kind: definition.EnvironmentKind,
		Metadata: definition.Metadata{Name: "nightly-fixture"}, UtilityContainer: toolchain, UtilityPlatform: "linux/amd64"})
	write("config/tools/definition.json", definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind,
		Metadata: definition.Metadata{Name: "host-tools"}, Catalog: definition.Catalog{Base: "../../catalog"},
		Outputs: []definition.DefinitionOutput{{Name: "host-tools", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"nvkectl"}}}})
	path, err := freezeNativeE2ESource(t.Context(), BuildConfig{Repository: root, Definition: "config/definition.json", Environment: "config/environment.json", AttemptPrefix: "source-fixture"}, latest, "config/tools/definition.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, snapshot, err := factory.CaptureCompileFiles(t.Context(), root, path, "config/environment.json", "dev")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, build := range plan.Builds {
		names = append(names, build.Name)
	}
	if !slices.Equal(names, []string{"cli", "dependency", "host-tools", "installer"}) {
		t.Fatalf("nightly tool composition lost an exact output: %v", names)
	}
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if component.Name == "dependency" {
				if component.Commit != original || component.Repo != "https://example.invalid/dependency" {
					t.Fatal("independent dependency source changed")
				}
				continue
			}
			if component.Commit != latest || component.TreeHash != tree || component.SourceDate != 1733939956 {
				t.Fatalf("source differs from the fetched exact Git tree: %+v", component)
			}
			if component.Name == "installer" && (!slices.Equal(component.BuildCommand, installer.BuildCommand) || component.VersionStamp["NVKECTL_COMMIT"] != latest) {
				t.Fatal("existing inline producer or its version stamp was lost")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".nvkr/nightly-inputs/source-fixture/catalog-02.json")); !os.IsNotExist(err) {
		t.Fatal("identical output catalog selections were not shared")
	}
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// A changed predecessor must fail fresh compilation; the captured original
	// closure still replays independently of those later host-file changes.
	overlay.RequiresCatalogDigest = "sha256:" + strings.Repeat("9", 64)
	write("config/existing.json", overlay)
	_, _, err = factory.CaptureCompileFiles(t.Context(), root, path, "config/environment.json", "dev")
	assertNightlyCode(t, err, e.CodeUnverifiedRef)
	if err := os.Rename(root, filepath.Join(t.TempDir(), "retained")); err != nil {
		t.Fatal(err)
	}
	replayed, err := factory.CompileSnapshot(t.Context(), snapshot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	after, err := replayed.Digest()
	if err != nil || before != after {
		t.Fatalf("ordinary captured inputs did not replay: %s %s %v", before, after, err)
	}
}
