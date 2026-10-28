// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func nightlySeedFixture() []byte {
	return fmt.Appendf(nil, `# Synthetic source seed, not publication evidence.
sites:
  - {id: %s, backend: vm, future_capability: {version: 2}}
  - {id: other-site, backend: container}
instance_types:
  - {id: %s, name: source-name-preserved, site_id: %s, gpu_count: 0, cpu_count: 4, memory_gib: 6, disk_gib: 60, vendor_extension: {preserve: true}}
  - {id: unrelated, name: untouched, site_id: other-site, gpu_count: 8, cpu_count: 77, memory_gib: 77, disk_gib: 77}
future_source_section: {schema: v2, values: [one, two], optional: null}
`, SiteID, TenantInstanceType, SiteID)
}

func nightlySeedFixtureWrite(t *testing.T, work string, raw []byte) string {
	t.Helper()
	path := filepath.Join(work, "internal/seed/default-seed.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := privateWrite(path, raw); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNightlySeedProjectionChangesOnlySelectedSizingAndRecordsActualBytes(t *testing.T) {
	for _, sourceShape := range []struct{ cpu, memory, disk int }{{4, 6, 60}, {6, 12, 60}, {32, 64, 120}} {
		t.Run(fmt.Sprintf("source-%d-%d-%d", sourceShape.cpu, sourceShape.memory, sourceShape.disk), func(t *testing.T) {
			var expected map[string]any
			if err := yaml.Unmarshal(nightlySeedFixture(), &expected); err != nil {
				t.Fatal(err)
			}
			target := expected["instance_types"].([]any)[0].(map[string]any)
			target["cpu_count"], target["memory_gib"], target["disk_gib"] = sourceShape.cpu, sourceShape.memory, sourceShape.disk
			raw, err := yaml.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			work := t.TempDir()
			source := nightlySeedFixtureWrite(t, work, raw)
			projection, err := projectNightlySeed(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(original, raw) {
				t.Fatal("authenticated source bytes changed", err)
			}
			runtime, err := os.ReadFile(filepath.Join(work, "laptop-seed.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]any
			if err := yaml.Unmarshal(runtime, &actual); err != nil {
				t.Fatal(err)
			}
			target["cpu_count"], target["memory_gib"], target["disk_gib"] = 6, 12, 60
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("runtime projection changed source names, unknown fields or unrelated rows", actual, expected)
			}
			if projection.RawSourceDigest != "sha256:"+plainSHA(raw) || projection.ProjectedRuntimeDigest != "sha256:"+plainSHA(runtime) || projection.InstanceType != TenantInstanceType || projection.VCPUs != 6 || projection.MemoryGiB != 12 || projection.DiskGiB != 60 {
				t.Fatal("projection record is not bound to actual source/runtime bytes and intended sizing", projection)
			}
			record, err := os.ReadFile(filepath.Join(work, "seed-projection.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored nightlySeedProjection
			if err := json.Unmarshal(record, &stored); err != nil || stored != projection {
				t.Fatal("private projection record differs from the selected runtime seed", err)
			}
			for _, name := range []string{"laptop-seed.yaml", "seed-projection.json"} {
				info, err := os.Stat(filepath.Join(work, name))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("projection output is not private", name, err)
				}
			}
		})
	}
}

func TestNightlySeedProjectionRejectsAmbiguousOrForeignVMSelectionBeforeOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing target", func(doc map[string]any) { doc["instance_types"] = doc["instance_types"].([]any)[1:] }},
		{"missing target id", func(doc map[string]any) { delete(doc["instance_types"].([]any)[0].(map[string]any), "id") }},
		{"duplicate target", func(doc map[string]any) {
			doc["instance_types"] = append(doc["instance_types"].([]any), doc["instance_types"].([]any)[0])
		}},
		{"wrong site", func(doc map[string]any) { doc["instance_types"].([]any)[0].(map[string]any)["site_id"] = "other-site" }},
		{"missing selected site", func(doc map[string]any) { doc["sites"] = doc["sites"].([]any)[1:] }},
		{"duplicate selected site", func(doc map[string]any) { doc["sites"] = append(doc["sites"].([]any), doc["sites"].([]any)[0]) }},
		{"container backend", func(doc map[string]any) { doc["sites"].([]any)[0].(map[string]any)["backend"] = "container" }},
		{"missing backend", func(doc map[string]any) { delete(doc["sites"].([]any)[0].(map[string]any), "backend") }},
		{"GPU target", func(doc map[string]any) { doc["instance_types"].([]any)[0].(map[string]any)["gpu_count"] = 1 }},
		{"missing GPU count", func(doc map[string]any) { delete(doc["instance_types"].([]any)[0].(map[string]any), "gpu_count") }},
		{"floating CPU", func(doc map[string]any) { doc["instance_types"].([]any)[0].(map[string]any)["cpu_count"] = 1.5 }},
		{"string memory", func(doc map[string]any) { doc["instance_types"].([]any)[0].(map[string]any)["memory_gib"] = "6" }},
		{"zero CPU", func(doc map[string]any) { doc["instance_types"].([]any)[0].(map[string]any)["cpu_count"] = 0 }},
		{"negative disk", func(doc map[string]any) { doc["instance_types"].([]any)[0].(map[string]any)["disk_gib"] = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var doc map[string]any
			if err := yaml.Unmarshal(nightlySeedFixture(), &doc); err != nil {
				t.Fatal(err)
			}
			test.edit(doc)
			raw, err := yaml.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			work := t.TempDir()
			source := nightlySeedFixtureWrite(t, work, raw)
			_, err = projectNightlySeed(t.Context(), work)
			assertNightlyCode(t, err, e.CodeVerifyFailed)
			for _, name := range []string{"laptop-seed.yaml", "seed-projection.json"} {
				if _, err := os.Stat(filepath.Join(work, name)); !os.IsNotExist(err) {
					t.Fatal("rejected source produced output", name, err)
				}
			}
			if actual, err := os.ReadFile(source); err != nil || !bytes.Equal(actual, raw) {
				t.Fatal("rejected source asset changed", err)
			}
		})
	}
}

func TestNightlySeedProjectionRejectsMalformedOrUnavailableInputs(t *testing.T) {
	for _, test := range []struct {
		name, existing       string
		raw                  []byte
		nilContext, canceled bool
		code                 e.Code
	}{
		{name: "malformed YAML", raw: []byte("sites: ["), code: e.CodeVerifyFailed},
		{name: "second YAML document", raw: append(nightlySeedFixture(), []byte("\n---\nextra: value\n")...), code: e.CodeVerifyFailed},
		{name: "duplicate source key", raw: []byte(strings.Replace(string(nightlySeedFixture()), "cpu_count: 4", "cpu_count: 4, cpu_count: 8", 1)), code: e.CodeVerifyFailed},
		{name: "oversized source", raw: bytes.Repeat([]byte("# source comment\n"), 5000), code: e.CodeVerifyFailed},
		{name: "nil context", raw: nightlySeedFixture(), nilContext: true, code: e.CodeUsage},
		{name: "canceled context", raw: nightlySeedFixture(), canceled: true, code: e.CodeTimeout},
		{name: "existing runtime seed", raw: nightlySeedFixture(), existing: "laptop-seed.yaml", code: e.CodeImmutableViolation},
		{name: "existing projection record", raw: nightlySeedFixture(), existing: "seed-projection.json", code: e.CodeImmutableViolation},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			nightlySeedFixtureWrite(t, work, test.raw)
			if test.existing != "" {
				if err := privateWrite(filepath.Join(work, test.existing), []byte("existing private record")); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if test.nilContext {
				ctx = nil
			} else if test.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := projectNightlySeed(ctx, work)
			assertNightlyCode(t, err, test.code)
			for _, name := range []string{"laptop-seed.yaml", "seed-projection.json"} {
				raw, err := os.ReadFile(filepath.Join(work, name))
				if name == test.existing {
					if err != nil || string(raw) != "existing private record" {
						t.Fatal("existing private output was replaced", name, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatal("rejected projection produced output", name, err)
				}
			}
		})
	}
}

func TestNightlySeedCurrentSourceContract(t *testing.T) {
	source := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT")
	if source == "" {
		t.Skip("set NVKR_NVKE_SOURCE_CONTRACT and NVKR_NVKE_SOURCE_COMMIT to check the current source seed")
	}
	work := privateTestDir(t)
	if err := nightlySource(t.Context(), Runner{Work: work, Env: NativeEnvironment()}, source, os.Getenv("NVKR_NVKE_SOURCE_COMMIT")); err != nil {
		t.Fatal(err)
	}
	raw, err := boundedRegularRead(filepath.Join(source, "controller/nico-sim/internal/seed/default-seed.yaml"), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	nightlySeedFixtureWrite(t, work, raw)
	projection, err := projectNightlySeed(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if projection.RawSourceDigest != "sha256:"+plainSHA(raw) {
		t.Fatal("current source projection recorded a fixed fixture digest", projection)
	}
	t.Logf("current source seed %s projected to %s", projection.RawSourceDigest, projection.ProjectedRuntimeDigest)
}
