// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"fmt"
	"strings"
	"testing"
)

func TestNativeBootstrapConditionsKeepOnlyClosedObservations(t *testing.T) {
	const cluster = `{"clusters":[{"id":"PRIVATE_ID","name":"PRIVATE_NAME","conditions":{"provisioning":{"status":"False","message":"Workflow failed: failed to resolve the CNI install mode: PRIVATE_TOKEN"},"provisioned":{"status":"False"},"PRIVATE_CONDITION":{"status":"True"}}}]}`
	for _, tc := range []struct{ name, collection, raw, want string }{
		{"cluster failure", "clusters", cluster, "Bootstrap controller observation: clusters[1] provisioning=False\nBootstrap source failure context: cni-mode\nBootstrap controller observation: clusters[1] provisioned=False\n"},
		{"nodes", "nodes", `{"nodes":[{"name":"PRIVATE_NODE","conditions":{"readyToJoin":{"status":"True","message":"PRIVATE_CREDENTIAL"},"nodeAgentReady":{"status":"False"}}}]}`, "Bootstrap controller observation: nodes[1] readyToJoin=True\nBootstrap controller observation: nodes[1] nodeAgentReady=False\n"},
		{"unknown status", "clusters", strings.ReplaceAll(cluster, "False", "PRIVATE_STATUS"), ""},
		{"unknown collection", "PRIVATE_COLLECTION", cluster, ""},
		{"wrong shape", "clusters", `{"clusters":{}}`, ""},
		{"malformed", "clusters", `{`, ""},
		{"no objects", "nodes", `{"nodes":[]}`, ""},
		{"excess objects", "clusters", `{"clusters":[` + strings.TrimSuffix(strings.Repeat(`{},`, 9), ",") + `]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeBootstrapConditionSummary(tc.collection, []byte(tc.raw)); got != tc.want || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect observation: %q", got)
			}
		})
	}
	var fields []string
	for i := range 33 {
		fields = append(fields, fmt.Sprintf(`"private-%d":{"status":"True"}`, i))
	}
	if got := nativeBootstrapConditionSummary("clusters", []byte(`{"clusters":[{"conditions":{`+strings.Join(fields, ",")+`}}]}`)); got != "" {
		t.Fatal("unbounded condition map was presented")
	}
}

func TestNativeBootstrapFailureContextNeverReturnsRawDetails(t *testing.T) {
	for _, tc := range []struct{ phrase, label string }{
		{"failed to resolve the CNI install mode:", "cni-mode"},
		{"failed to install cluster networking:", "cluster-networking"},
		{"failed to initialize Kubernetes control plane:", "control-plane-init"},
		{"failed to wait for a control plane node to be ready to join:", "control-plane-ready-to-join"},
		{"failed to configure initial node KubeVip:", "initial-node-kube-vip"},
		{"failed to create CAPI resources:", "capi-resources"},
		{"failed to mark initial control plane node as joined:", "control-plane-joined-record"},
		{"failed to install kube-local-dns:", "kube-local-dns"},
		{"failed to label system namespaces for Pod Security Admission:", "system-namespace-labels"},
		{"failed to mark cluster as provisioned:", "cluster-provisioned-record"},
		{"PRIVATE_UNKNOWN_FAILURE", "unclassified"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := nativeBootstrapFailureContext("Workflow failed: " + tc.phrase + " PRIVATE_TOKEN")
			if got != tc.label || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect context: %q", got)
			}
			if got := nativeBootstrapFailureContext(tc.phrase); got != "" {
				t.Fatal("ordinary condition text became a workflow failure")
			}
		})
	}
}
