// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"path/filepath"
	"strconv"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

type nightlySeedProjection struct {
	RawSourceDigest        string `json:"rawSourceDigest"`
	ProjectedRuntimeDigest string `json:"projectedRuntimeDigest"`
	InstanceType           string `json:"instanceType"`
	VCPUs                  int    `json:"vcpus"`
	MemoryGiB              int    `json:"memoryGiB"`
	DiskGiB                int    `json:"diskGiB"`
}

// projectNightlySeed consumes the seed already extracted from the authenticated
// simulator artifact. The source asset stays unchanged. This is only the local
// test's VM sizing projection; NicoSim still owns full seed decoding/validation.
func projectNightlySeed(ctx context.Context, work string) (nightlySeedProjection, error) {
	var result nightlySeedProjection
	if ctx == nil {
		return result, e.New(e.CodeUsage, "nightly seed projection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "nightly seed projection canceled")
	}
	raw, err := boundedRegularRead(filepath.Join(work, "internal/seed/default-seed.yaml"), 64<<10)
	if err != nil {
		return result, e.Wrap(e.CodeVerifyFailed, err, "reading authenticated simulator seed")
	}
	projected, err := nightlySeedBytes(raw)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "nightly seed projection canceled")
	}
	runtimePath, err := freshDestination(filepath.Join(work, "laptop-seed.yaml"))
	if err != nil {
		return result, e.Wrap(e.CodeImmutableViolation, err, "admitting private runtime seed destination")
	}
	recordPath, err := freshDestination(filepath.Join(work, "seed-projection.json"))
	if err != nil {
		return result, e.Wrap(e.CodeImmutableViolation, err, "admitting private seed projection record")
	}
	result = nightlySeedProjection{
		RawSourceDigest: "sha256:" + plainSHA(raw), ProjectedRuntimeDigest: "sha256:" + plainSHA(projected),
		InstanceType: TenantInstanceType, VCPUs: tenantDemoVCPUs, MemoryGiB: tenantDemoMemoryGiB, DiskGiB: 60,
	}
	if err := privateWrite(runtimePath, projected); err != nil {
		return nightlySeedProjection{}, e.Wrap(e.CodeImmutableViolation, err, "writing private runtime seed")
	}
	if err := privateJSON(recordPath, result); err != nil {
		return nightlySeedProjection{}, e.Wrap(e.CodeImmutableViolation, err, "writing private seed projection record")
	}
	return result, nil
}

func nightlySeedBytes(raw []byte) ([]byte, error) {
	invalid := func(message string) ([]byte, error) { return nil, e.New(e.CodeVerifyFailed, "%s", message) }
	root, err := documentpath.Decode(raw)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding authenticated seed for nightly sizing")
	}
	instancePath, sitePath := "instance_types[id="+TenantInstanceType+"]", "sites[id="+SiteID+"]"
	selected, err := documentpath.Select(root, []string{instancePath, sitePath}, false)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "selecting one VM site and nightly instance type")
	}
	var site struct {
		ID      string `yaml:"id"`
		Backend string `yaml:"backend"`
	}
	var instance struct {
		ID        string `yaml:"id"`
		SiteID    string `yaml:"site_id"`
		GPUCount  *int   `yaml:"gpu_count"`
		CPUCount  *int   `yaml:"cpu_count"`
		MemoryGiB *int   `yaml:"memory_gib"`
		DiskGiB   *int   `yaml:"disk_gib"`
	}
	if err := selected[sitePath].Decode(&site); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading selected nightly VM site")
	}
	if err := selected[instancePath].Decode(&instance); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading selected nightly instance type")
	}
	if site.ID != SiteID || site.Backend != "vm" || instance.ID != TenantInstanceType || instance.SiteID != SiteID || instance.GPUCount == nil || *instance.GPUCount != 0 {
		return invalid("nightly sizing requires its explicit non-GPU instance type in the selected VM site")
	}
	if instance.CPUCount == nil || instance.MemoryGiB == nil || instance.DiskGiB == nil || *instance.CPUCount <= 0 || *instance.MemoryGiB <= 0 || *instance.DiskGiB <= 0 {
		return invalid("selected nightly VM instance requires positive CPU, memory and disk values")
	}
	// yaml's typed int decoder permits float coercion. Inspect the parser's tags
	// at these owned fields so sizing never truncates a floating-point value.
	for _, field := range []string{"gpu_count", "cpu_count", "memory_gib", "disk_gib"} {
		node := documentpath.Field(selected[instancePath], field)
		if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
			return invalid("selected nightly instance sizing requires explicit YAML integers")
		}
	}
	// Preserve all unknown source fields, names and unrelated rows. This changes
	// only the existing selected instance's three runtime sizing scalars.
	for _, sizing := range []struct {
		field string
		value int
	}{{"cpu_count", tenantDemoVCPUs}, {"memory_gib", tenantDemoMemoryGiB}, {"disk_gib", 60}} {
		documentpath.Field(selected[instancePath], sizing.field).Value = strconv.Itoa(sizing.value)
	}
	projected, err := yaml.Marshal(root)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding private nightly runtime seed")
	}
	return projected, nil
}
