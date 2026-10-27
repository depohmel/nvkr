// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This diagnostic uses the nightly's existing exact-byte seed projection. It
// prepares no publication receipt and starts no guests during ordinary tests.
func TestHostedVMMeasurementSeed(t *testing.T) {
	root := os.Getenv("NVKR_MEASURE_SEED_ROOT")
	if root == "" {
		t.Skip("set NVKR_MEASURE_SEED_ROOT for the explicit hosted workload experiment")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || !filepath.IsAbs(root) {
		t.Fatal("measurement requires native Linux AMD64 and an absolute source seed root")
	}
	projection, err := projectSeed(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nightly guest shape: %d vCPUs, %d GiB RAM, %d GiB disk per selected instance; projected seed %s",
		projection.VCPUs, projection.MemoryGiB, projection.DiskGiB, projection.RuntimeDigest)
}
