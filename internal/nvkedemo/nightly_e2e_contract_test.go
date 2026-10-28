// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"go.yaml.in/yaml/v3"
)

func TestNightlyAuthoredRuntimeStepConsumesActualCycleWriter(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/nvkr-nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	runtimeStepShell := ""
	for _, step := range workflow.Jobs["runtime-test"].Steps {
		if step.Name == "Authenticate the publication and run the existing NVKE Go suite" {
			if runtimeStepShell != "" {
				t.Fatal("ambiguous authored runtime step")
			}
			runtimeStepShell = step.Run
		}
	}
	if runtimeStepShell == "" {
		t.Fatal("missing authored runtime step")
	}
	// Only Go is substituted. Execute the entire authored step, including its
	// list check and real jq postcondition. A zero-exit skipped Go invocation
	// alone must never satisfy the authored step's successful-cycle check.
	goFixture := `go() {
  case "$*" in
    *"-list "*) printf '%s\n' TestNightlyPublishedCandidate ;;
    *"-run "*) printf '%s\n' "$FIXTURE_GO_RESULT" ;;
    *) return 91 ;;
  esac
}
`
	for _, test := range []struct {
		name, phase, envKey, envValue string
		result                        error
		skip                          bool
		edit                          func(map[string]any)
		pass                          bool
	}{
		{name: "successful writer", phase: "runtime", pass: true},
		{name: "successful Go skip without cycle", skip: true},
		{name: "wrong profile", phase: "runtime", envKey: "NVKR_NIGHTLY_PROFILE", envValue: "other"},
		{name: "wrong source", phase: "runtime", envKey: "GITHUB_SHA", envValue: strings.Repeat("b", 40)},
		{name: "wrong NVKE main source", phase: "runtime", envKey: "NVKR_NVKE_SOURCE_COMMIT", envValue: strings.Repeat("d", 40)},
		{name: "wrong phase", phase: "preparation"},
		{name: "wrong coordinator run", phase: "runtime", envKey: "GITHUB_RUN_ID", envValue: "124"},
		{name: "wrong coordinator attempt", phase: "runtime", envKey: "GITHUB_RUN_ATTEMPT", envValue: "3"},
		{name: "failed runtime or cleanup", phase: "runtime", result: errors.New("joined cleanup failed")},
		{name: "wrong authority", phase: "runtime", edit: func(cycle map[string]any) { cycle["qualificationAuthority"] = "qualified" }},
		{name: "numeric coordinator run", phase: "runtime", edit: func(cycle map[string]any) { cycle["coordinatorRun"] = 123 }},
		{name: "numeric coordinator attempt", phase: "runtime", edit: func(cycle map[string]any) { cycle["coordinatorAttempt"] = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			public := t.TempDir()
			profile := NightlyProfile{Name: "fixture-profile"}
			build := NightlyBuild{CoordinatorRun: "123", CoordinatorAttempt: "2", ProducerRun: 456, ProducerAttempt: 1, SourceCommit: strings.Repeat("a", 40), NVKECommit: strings.Repeat("c", 40)}
			if !test.skip {
				if err := writeNightlyCycle(t.Context(), public, profile, build, "fixture-subject", test.phase, test.result); err != nil {
					t.Fatal(err)
				}
				if test.edit != nil {
					path := filepath.Join(public, "cycle.json")
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var cycle map[string]any
					if err := json.Unmarshal(raw, &cycle); err != nil {
						t.Fatal(err)
					}
					test.edit(cycle)
					raw, err = json.Marshal(cycle)
					if err != nil {
						t.Fatal(err)
					}
					if err := privateReplace(path, raw); err != nil {
						t.Fatal(err)
					}
				}
			}
			values := map[string]string{"PATH": os.Getenv("PATH"), "NVKR_NIGHTLY_REPORTS": public,
				"NVKR_NIGHTLY_PROFILE": profile.Name, "GITHUB_RUN_ID": build.CoordinatorRun,
				"GITHUB_RUN_ATTEMPT": build.CoordinatorAttempt, "GITHUB_SHA": build.SourceCommit,
				"NVKR_NVKE_SOURCE_COMMIT": build.NVKECommit,
				"FIXTURE_GO_RESULT":       "--- PASS: TestNightlyPublishedCandidate"}
			if test.skip {
				values["FIXTURE_GO_RESULT"] = "--- SKIP: TestNightlyPublishedCandidate"
			}
			if test.envKey != "" {
				values[test.envKey] = test.envValue
			}
			env := SetEnv(nil, values)
			bash, err := commandPath("bash", env)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := commandPath("jq", env); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", goFixture+runtimeStepShell)
			command.Env = env
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			output, err := command.CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("authored runtime step pass=%t, want %t: %v\n%s", err == nil, test.pass, err, output)
			}
		})
	}
}

func TestNightlyCycleWriterRejectsUnavailableContextAndDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil context", nil, e.CodeUsage},
		{"canceled context", ctx, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			public := t.TempDir()
			assertNightlyCode(t, writeNightlyCycle(test.ctx, public, NightlyProfile{}, NightlyBuild{}, "", "runtime", nil), test.code)
			if _, err := os.Stat(filepath.Join(public, "cycle.json")); !os.IsNotExist(err) {
				t.Fatal("refused writer exposed cycle", err)
			}
		})
	}
	assertNightlyCode(t, writeNightlyCycle(t.Context(), filepath.Join(t.TempDir(), "missing"), NightlyProfile{}, NightlyBuild{}, "", "runtime", nil), e.CodeImmutableViolation)
}

func TestNightlyProfilesUsePinnedNestedRunbooksLayout(t *testing.T) {
	// Exact upstream bytes copied from nvke-profiles pin 8fb2d53368823bd386b4fbd55b4ab6d3c0931345,
	// controller/profiles/example/Dockerfile. This is a byte/layout fixture check,
	// not a Dockerfile interpreter or an upstream-image execution claim.
	dockerfile, err := os.ReadFile("testdata/pinned-profiles.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%x", sha256.Sum256(dockerfile))
	if pin != "789f7ad1e406b82f0f55a52152eff7b08846b4122582145fade588c7d931cc3e" ||
		!strings.Contains(string(dockerfile), "\nCOPY profiles/ /profiles/\nCOPY runbooks/ /profiles/runbooks/\n") {
		t.Fatal("profiles fixture differs from the source-owned pinned Dockerfile layout")
	}
	for _, catalog := range []string{"nvidia-arm64", "upstream-arm64"} {
		raw, err := os.ReadFile(filepath.Join("../../inputs/catalogs/controller-profiles", catalog, "registry.yaml"))
		if err != nil || !strings.Contains(string(raw), pin+"  controller/profiles/example/Dockerfile") {
			t.Fatal("producer Dockerfile pin differs from the layout fixture", catalog, err)
		}
	}
	for _, metadata := range []bool{true, false} {
		t.Run(fmt.Sprintf("metadata=%t", metadata), func(t *testing.T) {
			publication, release := nightlyProfilesPublicationFixture(t, metadata)
			defer publication.Close()
			destination, err := nightlyProfiles(t.Context(), publication, release, "controller-profiles", t.TempDir(), "arm64")
			if !metadata {
				assertNightlyCode(t, err, e.CodeVerifyFailed)
				if destination != "" {
					t.Fatal("missing metadata returned profiles assets")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"profiles/osProfiles/metadata.yaml", "profiles/configProfiles/worker.yaml", "profiles/runbooks/setup.yaml"} {
				if raw, err := os.ReadFile(filepath.Join(destination, path)); err != nil || len(raw) == 0 {
					t.Fatal(path, string(raw), err)
				}
			}
			for _, path := range []string{"runbooks", "etc"} {
				if _, err := os.Stat(filepath.Join(destination, path)); !os.IsNotExist(err) {
					t.Fatal("unexpected top-level image root exported", path, err)
				}
			}
		})
	}
}

func nightlyProfilesPublicationFixture(t *testing.T, metadata bool) (*delivery.Publication, lifecycle.Release) {
	t.Helper()
	// Exact source-owned Dockerfile layout, checksum 789f7ad1e406b82f0f55a52152eff7b08846b4122582145fade588c7d931cc3e:
	// Alpine base; COPY profiles/ /profiles/; COPY runbooks/ /profiles/runbooks/.
	// Synthetic bytes exercise the real OCI/delivery owners without rebuilding
	// an upstream image or fetching an unrelated registry fixture.
	var raw bytes.Buffer
	archive := tar.NewWriter(&raw)
	for _, file := range []struct{ path, content string }{
		{"etc/alpine-release", "3.24.1\n"},
		{"profiles/configProfiles/worker.yaml", "kind: ConfigProfile\n"},
		{"profiles/runbooks/setup.yaml", "kind: Runbook\n"},
		{"profiles/osProfiles/metadata.yaml", "kind: OSProfileMetadata\n"},
	} {
		if !metadata && file.path == "profiles/osProfiles/metadata.yaml" {
			continue
		}
		if err := archive.WriteHeader(&tar.Header{Name: file.path, Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(file.content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(file.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	image := mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.OCIConfigJSON)
	image, err := mutate.AppendLayers(image, static.NewLayer(raw.Bytes(), types.OCIUncompressedLayer))
	if err != nil {
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config.OS, config.Architecture = "linux", "arm64"
	config.Config.Cmd = []string{"/bin/sh"}
	image, err = mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "image")
	index, err := layout.Write(source, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	inspected, err := delivery.InspectContainerImage(t.Context(), source, "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	build := "sha256:" + strings.Repeat("a", 64)
	output := delivery.Output{Name: "controller-profiles", Kind: "oci", Digest: inspected.ManifestDigest, BuildDigest: build, Components: []string{"profiles"}}
	content, err := delivery.ContentDigest([]delivery.Output{output})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, "profiles", inspected.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, inspected.ManifestDigest, "build-output", inspected.ManifestDigest)
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
	root := filepath.Join(t.TempDir(), "publication")
	result, err := delivery.BuildPublicationRoot(t.Context(), root, delivery.PublicationRequest{
		Release: &delivery.ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content},
		Objects: []delivery.ObjectInput{
			{Name: output.Name, Kind: "container-image", Path: source, Layout: true, ArtifactType: delivery.MediaOCIManifest,
				SemanticDigest: inspected.ManifestDigest, BlobDigest: inspected.RootDigest, OS: "linux", Arch: "arm64",
				Output: &delivery.OutputBinding{Repository: output.Name, Format: "oci", BuildDigest: build, Components: output.Components,
					BuildSubjects: []delivery.BuildSubject{{Component: "profiles", Digest: inspected.ManifestDigest}}}},
			{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json",
				LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: document,
				Evidence: &delivery.EvidenceBinding{Outputs: []string{output.Name}, Component: "profiles", Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: inspected.ManifestDigest}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := delivery.OpenPublication(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	// Extraction must continue through held image capabilities, not path reads.
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	return publication, lifecycle.Release{Artifacts: []lifecycle.Artifact{{Name: output.Name, Kind: lifecycle.OutputOCI,
		Digest: inspected.ManifestDigest, Reference: "registry.example/team/profiles@" + inspected.ManifestDigest}}}
}

func nightlyRuntimeRelease() lifecycle.Release {
	release := lifecycle.Release{}
	for _, role := range runtimeImages {
		digest := "sha256:" + strings.Repeat("a", 64)
		release.Artifacts = append(release.Artifacts, lifecycle.Artifact{Name: role + "-image-arm64", Kind: lifecycle.OutputOCI, OS: "linux", Arch: "arm64", Digest: digest, Reference: "registry.example:5000/team/" + role + "@" + digest, Components: []string{role + "-image"}})
	}
	return release
}

func TestNightlyRuntimeImageRolesPreserveExactRegistryAndDigest(t *testing.T) {
	plan, _ := runtimeValueInputs(t)
	for i := range plan.Builds {
		if plan.Builds[i].Name == "temporal-server-image-arm64" {
			plan.Builds[i].ResolvedComponents[0].Prebuilt.Image = "temporalio/server:9.4.2"
		}
	}
	release := nightlyRuntimeRelease()
	values, err := nightlyRuntimeValues(plan, release, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	image := values["temporal"].(map[string]any)["server"].(map[string]any)["image"].(map[string]any)
	if image["tag"] != "9.4.2@sha256:"+strings.Repeat("a", 64) {
		t.Fatal("nightly ignored its selected producer tag", image)
	}
	seen := map[string]int{}
	var walk func(map[string]any)
	walk = func(object map[string]any) {
		if repo, ok := object["repository"].(string); ok {
			combined := repo
			if registry, ok := object["registry"].(string); ok {
				combined = registry + "/" + repo
			}
			role := strings.TrimPrefix(combined, "registry.example:5000/team/")
			if role == combined {
				t.Fatalf("incorrect registry/repository projection: %s", combined)
			}
			seen[role]++
			digest := object["digest"]
			if tag, ok := object["tag"].(string); ok && tag != "" {
				digest = tag
			}
			if !strings.HasSuffix(digest.(string), "sha256:"+strings.Repeat("a", 64)) {
				t.Fatalf("unpinned %s: %v", role, object)
			}
		}
		for _, value := range object {
			if child, ok := value.(map[string]any); ok {
				walk(child)
			}
		}
	}
	for _, value := range values {
		walk(value.(map[string]any))
	}
	for _, role := range runtimeImages {
		if seen[role] == 0 {
			t.Fatalf("unmapped role %s", role)
		}
	}
	for _, test := range []struct {
		name   string
		change func(*lifecycle.Release)
	}{
		{"missing role", func(r *lifecycle.Release) { r.Artifacts = r.Artifacts[1:] }},
		{"duplicate role", func(r *lifecycle.Release) { r.Artifacts = append(r.Artifacts, r.Artifacts[0]) }},
		{"digest substitution", func(r *lifecycle.Release) { r.Artifacts[0].Digest = "sha256:" + strings.Repeat("b", 64) }},
		{"tag reference", func(r *lifecycle.Release) { r.Artifacts[0].Reference = "registry.example:5000/team/image:latest" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := nightlyRuntimeRelease()
			test.change(&changed)
			_, err := nightlyRuntimeValues(plan, changed, "arm64")
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func nightlyVersionPlan() *factory.Plan {
	return &factory.Plan{Builds: []factory.BuildPlan{{Name: "cwe", Graph: &ir.Graph{Nodes: []ir.Node{{Kind: ir.KindBuild, Component: "nvke-controller", Env: []ir.EnvVar{{Name: "BUILD_VERSION", Value: "producer-source-version"}}}}}}}}
}

func TestNightlyControllerVersionComesFromExactProducer(t *testing.T) {
	version, err := nightlyControllerVersion(nightlyVersionPlan(), "cwe")
	if err != nil || version != "producer-source-version" {
		t.Fatal(version, err)
	}
	for _, test := range []struct {
		name   string
		change func(*factory.Plan)
	}{
		{"missing build", func(p *factory.Plan) { p.Builds = nil }},
		{"duplicate build", func(p *factory.Plan) { p.Builds = append(p.Builds, p.Builds[0]) }},
		{"missing graph", func(p *factory.Plan) { p.Builds[0].Graph = nil }},
		{"wrong component", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Component = "other" }},
		{"duplicate node", func(p *factory.Plan) { g := p.Builds[0].Graph; g.Nodes = append(g.Nodes, g.Nodes[0]) }},
		{"missing binding", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Env = nil }},
		{"duplicate binding", func(p *factory.Plan) { n := &p.Builds[0].Graph.Nodes[0]; n.Env = append(n.Env, n.Env[0]) }},
		{"empty binding", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Env[0].Value = "" }},
		{"unresolved stamp", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Env[0].Value = "{{.Commit}}" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := nightlyVersionPlan()
			test.change(plan)
			_, err := nightlyControllerVersion(plan, "cwe")
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
	_, err = nightlyControllerVersion(nil, "cwe")
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	plan := nightlyVersionPlan()
	plan.Builds[0].Name = "project-controller"
	if _, err := nightlyControllerVersion(plan, "project-controller"); err != nil {
		t.Fatal(err)
	}
}

func TestNightlyDeadlineReservesEveryCleanupStage(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name     string
		deadline time.Time
		work     time.Duration
		invalid  bool
	}{
		{"workflow330minutes", now.Add(330 * time.Minute), 295 * time.Minute, false},
		{"longer enclosing timeout caps work", now.Add(360 * time.Minute), nightlyMaximumWork, false},
		{"shorter enclosing timeout", now.Add(40 * time.Minute), 5 * time.Minute, false},
		{"no test timeout", time.Time{}, nightlyMaximumWork, false},
		{"cleanup would consume entire deadline", now.Add(nightlyCleanupReserve), 0, true},
		{"expired deadline", now.Add(-time.Second), 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget, err := nightlyTimeBudget(now, test.deadline)
			if test.invalid {
				assertNightlyCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || budget.WorkDeadline.Sub(now) != test.work {
				t.Fatal(budget, err)
			}
			cleanup := nightlySuiteGrace + nightlyVerdictGrace + nightlyDiskGrace + nightlyNetworkGrace + nightlyReportGrace
			if budget.WorkDeadline.Add(cleanup).After(budget.CleanupDeadline) || !test.deadline.IsZero() && !budget.CleanupDeadline.Before(test.deadline) {
				t.Fatal("work, process join and cleanup exceed enclosing deadline")
			}
			parent, cancel := context.WithCancel(t.Context())
			cancel()
			bounded, stop := budget.cleanupContext(parent, time.Minute)
			defer stop()
			deadline, ok := bounded.Deadline()
			if !ok || deadline.After(budget.CleanupDeadline) || bounded.Err() != nil {
				t.Fatal("cleanup lost its explicit budget or inherited work cancellation")
			}
		})
	}
}

func TestNightlyOptInCannotSilentlySkipRuntimeJob(t *testing.T) {
	for _, test := range []struct {
		enable, actions, profile string
		enabled, refused         bool
	}{
		{"", "", "", false, false},
		{"", "true", "", false, false},
		{"", "true", "selected-profile", false, true},
		{"0", "true", "selected-profile", false, true},
		{"1", "true", "selected-profile", true, false},
	} {
		enabled, err := nightlyTestAdmission(test.enable, test.actions, test.profile)
		if enabled != test.enabled {
			t.Fatal(test, enabled)
		}
		if test.refused {
			assertNightlyCode(t, err, e.CodeUsage)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNightlySiteImagesRequireUniqueImmutableOCIOutputs(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	good := lifecycle.Artifact{Name: "project-controller", Kind: lifecycle.OutputOCI, Digest: digest, Reference: "registry.example/team/controller@" + digest}
	for _, test := range []struct {
		name      string
		artifacts []lifecycle.Artifact
		output    string
		valid     bool
	}{
		{"exact", []lifecycle.Artifact{good}, good.Name, true},
		{"missing", nil, good.Name, false},
		{"empty output", []lifecycle.Artifact{good}, "", false},
		{"duplicate", []lifecycle.Artifact{good, good}, good.Name, false},
		{"wrong kind", []lifecycle.Artifact{{Name: good.Name, Kind: lifecycle.OutputHelm, Digest: digest, Reference: good.Reference}}, good.Name, false},
		{"digest substitution", []lifecycle.Artifact{{Name: good.Name, Kind: good.Kind, Digest: "sha256:" + strings.Repeat("b", 64), Reference: good.Reference}}, good.Name, false},
		{"tag locator", []lifecycle.Artifact{{Name: good.Name, Kind: good.Kind, Digest: digest, Reference: "registry.example/team/controller:latest"}}, good.Name, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			image, err := nightlyNamedImage(lifecycle.Release{Artifacts: test.artifacts}, test.output)
			if test.valid {
				if err != nil || image.Name != good.Name || image.Reference != good.Reference || image.Digest != good.Digest {
					t.Fatal(image, err)
				}
				return
			}
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
	if nightlyControllerOutput(NightlyProfile{}) != "cwe" || nightlyControllerOutput(NightlyProfile{ControllerOutput: "project-controller"}) != "project-controller" {
		t.Fatal("explicit controller selection or legacy default lost")
	}
}

func TestNightlySuiteUsesAdmittedInterpreterAndActualEntrypoint(t *testing.T) {
	source, admitted, ambient := t.TempDir(), t.TempDir(), t.TempDir()
	for _, directory := range []string{admitted, ambient} {
		if err := os.WriteFile(filepath.Join(directory, "bash"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", ambient)
	runner := Runner{Work: t.TempDir(), Env: []string{"PATH=" + admitted}}
	for _, path := range nightlySourceFiles[1:] {
		path = filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic stock input\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := nightlySuiteCommand(t.Context(), runner, source)
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	if entries, err := os.ReadDir(runner.Work); err != nil || len(entries) != 0 {
		t.Fatal("rejected source wrote an adapter", entries, err)
	}
	if err := os.WriteFile(filepath.Join(source, nightlySourceFiles[0]), []byte("synthetic suite\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := nightlySuiteCommand(t.Context(), runner, source)
	entry, library := filepath.Join(runner.Work, "nightly-adapter/entrypoint.sh"), filepath.Join(runner.Work, "nightly-adapter/candidate.sh")
	if err != nil || command.Path != filepath.Join(admitted, "bash") || len(command.Args) != 4 || command.Args[1] != entry || command.Args[2] != source || command.Args[3] != library || command.Dir != source {
		t.Fatal(command, err)
	}
	for path, want := range map[string][]byte{entry: nightlyCandidateEntrypoint, library: nightlyCandidateLibrary} {
		raw, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || !bytes.Equal(raw, want) || info.Mode().Perm() != 0600 {
			t.Fatal("adapter differs from embedded bytes or private mode", path, err, statErr)
		}
	}
	_, err = nightlySuiteCommand(t.Context(), runner, source)
	assertNightlyCode(t, err, e.CodeImmutableViolation)
	_, err = nightlySuiteCommand(t.Context(), Runner{Env: []string{"PATH=" + t.TempDir()}}, source)
	assertNightlyCode(t, err, e.CodeEngineCapability)
	var missingContext context.Context
	_, err = nightlySuiteCommand(missingContext, runner, source)
	assertNightlyCode(t, err, e.CodeUsage)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = nightlySuiteCommand(ctx, runner, source)
	assertNightlyCode(t, err, e.CodeTimeout)
}

func TestNightlySourceAdmitsExactStockGoSuiteWithoutCandidateEntrypoint(t *testing.T) {
	for _, test := range []struct {
		name, missing   string
		dirty, wrongPin bool
	}{
		{name: "stock Go suite with no candidate entrypoint"},
		{name: "missing suite", missing: "controller/test/e2e/suite_test.go"},
		{name: "missing stock helper", missing: "controller/hack/backend-e2e-lib.sh"},
		{name: "missing deploy input", missing: "deploy/nvke-deploy.sh"},
		{name: "missing matrix metadata", missing: "controller/test/fixtures/fleet-releases/index.yaml"},
		{name: "wrong source pin", wrongPin: true},
		{name: "additional source file", dirty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, work := privateTestDir(t), privateTestDir(t)
			git := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, "git", append([]string{"-C", source}, args...)...)
				command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}
				raw, err := command.CombinedOutput()
				if err != nil {
					t.Fatal(err, string(raw))
				}
				return strings.TrimSpace(string(raw))
			}
			git("init", "-q")
			for _, path := range nightlySourceFiles {
				if path == test.missing {
					continue
				}
				path = filepath.Join(source, path)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("synthetic stock input\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			pin := git("rev-parse", "HEAD")
			if test.wrongPin {
				pin = strings.Repeat("a", 40)
			}
			if test.dirty {
				if err := os.WriteFile(filepath.Join(source, "injected-helper.sh"), []byte("fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := nightlySource(t.Context(), Runner{Work: work, Env: NativeEnvironment()}, source, pin)
			if test.missing == "" && !test.dirty && !test.wrongPin {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestNightlySignalsCancelTheOwnedTestContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestNightlySignalContextHelper$", "-test.timeout=8s")
			command.Env = []string{"NVKR_NIGHTLY_TEST_SIGNAL=1"}
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = ownedcmd.KillDescendants(command) }()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "nightly-signal-ready" {
				cancel()
				_ = command.Wait()
				t.Fatal("signal helper did not start", scanner.Err())
			}
			if err := command.Process.Signal(signal); err != nil {
				cancel()
				_ = command.Wait()
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatal("signal did not reach orderly context cancellation", err)
			}
		})
	}
}

func TestNightlySignalContextHelper(t *testing.T) {
	if os.Getenv("NVKR_NIGHTLY_TEST_SIGNAL") != "1" {
		t.Skip("owned signal subprocess fixture")
	}
	ctx, cancel := nightlySignalContext(t.Context(), time.Now().Add(6*time.Second))
	defer cancel()
	fmt.Println("nightly-signal-ready")
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatal("signal was not received", ctx.Err())
	}
}

func TestNightlyCandidateVerdictSeparatesTestsFromJoinedCleanup(t *testing.T) {
	profile := NightlyProfile{OSProfile: "os-profile", KubernetesVersion: "1.34.2"}
	for _, test := range []struct {
		name, raw   string
		clean, pass bool
		code        e.Code
	}{
		{"passed", `{"cleanup_exit":0,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, true, true, ""},
		{"test failed cleanup joined", `{"cleanup_exit":0,"test_exit":1,"entry_exit":1,"profile":"os-profile","kubernetes":"1.34.2"}`, true, false, e.CodeGateUnmet},
		{"cleanup failed", `{"cleanup_exit":1,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"cleanup missing", `{"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"null cleanup", `{"cleanup_exit":null,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"wrong profile", `{"cleanup_exit":0,"test_exit":0,"entry_exit":0,"profile":"other","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"wrong kubernetes", `{"cleanup_exit":0,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.35.0"}`, false, false, e.CodeGateUnmet},
		{"duplicate verdict", `{"cleanup_exit":0,"cleanup_exit":1}`, false, false, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			clean, err := nightlyCandidateVerdict([]byte(test.raw), profile)
			if clean != test.clean {
				t.Fatalf("cleanup=%v; want %v", clean, test.clean)
			}
			if test.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, test.code)
			}
		})
	}
}

func TestNightlyPreparationCachesArePrivateAndCredentialsExcluded(t *testing.T) {
	t.Setenv("GH_TOKEN", "must-not-propagate")
	t.Setenv("GITHUB_TOKEN", "must-not-propagate")
	work := t.TempDir()
	binding := map[string]string{"HOME": "/home/runtime", "XDG_RUNTIME_DIR": "/run/user/123"}
	env, err := nightlyPreparationEnvironment(NativeEnvironment(), binding, work)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"HOME": binding["HOME"], "XDG_RUNTIME_DIR": binding["XDG_RUNTIME_DIR"], "GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "-mod=readonly", "GH_TOKEN": "", "GITHUB_TOKEN": ""} {
		if got := environmentValue(env, key); got != want {
			t.Fatalf("%s=%q; want %q", key, got, want)
		}
	}
	for _, name := range []string{"go-cache", "go-modules", "go-path"} {
		info, err := os.Stat(filepath.Join(work, name))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal(name, info, err)
		}
	}
	_, err = nightlyPreparationEnvironment(NativeEnvironment(), binding, work)
	assertNightlyCode(t, err, e.CodeImmutableViolation)
	_, err = nightlyPreparationEnvironment(nil, map[string]string{}, t.TempDir())
	assertNightlyCode(t, err, e.CodeEngineCapability)
}

func TestNightlyCycleKeepsSuiteReportsPrivate(t *testing.T) {
	private, public := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{"junit.xml": "<testsuite>private diagnostic</testsuite>", "candidate-outcome.json": "{}", "SAFE_TO_UPLOAD": "", "private-log": "private fixture"} {
		if err := privateWrite(filepath.Join(private, name), []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeNightlyCycle(t.Context(), public, NightlyProfile{Name: "fixture-profile"}, NightlyBuild{}, "fixture-subject", "runtime", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(public)
	if err != nil || len(entries) != 1 || entries[0].Name() != "cycle.json" {
		t.Fatal("public attachments must contain only the parent-authored cycle", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(public, "cycle.json"))
	if err != nil || bytes.Contains(raw, []byte("private diagnostic")) {
		t.Fatal("private suite data reached the public cycle", err)
	}
}

func TestNightlyBundleRolesFailClosed(t *testing.T) {
	bundle := delivery.BinaryBundle{Entries: []delivery.BinaryEntry{{Name: "first", InstallPath: "/a/kind"}, {Name: "second", InstallPath: "/b/kind"}}}
	_, err := nightlyBundleEntries(bundle, t.TempDir())
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	bundle.Entries = []delivery.BinaryEntry{{Name: "../kind", InstallPath: "/bin/kind"}}
	_, err = nightlyBundleEntries(bundle, t.TempDir())
	assertNightlyCode(t, err, e.CodeVerifyFailed)
}
