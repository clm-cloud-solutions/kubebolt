package copilot

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The filesystem lane exists because of a measured miss. On 2026-09-16 Kobi was
// asked how a node's disk filled, had no tool that could answer, and invented a
// cause (a memory spike 90 minutes after the evictions). The data was there the
// whole time: node_fs_used_bytes on that node reads 53% at 02:55, 88.7% at
// 03:10 — the minute kubelet evicted for ephemeral-storage — and 39.9% at 03:15
// once the replacement node came up.

func fsBuilder() *promBuilder {
	return &promBuilder{tenantID: "org-1", clusterUID: "uid-9", kind: "Node", name: "aks-default-vmss000004"}
}

// Both branches must be present, and the fallback is not optional garnish:
// measured against production, node_filesystem_avail_bytes has ZERO series in
// all four clusters and node_fs_used_bytes has series in all four. Shipping
// only the node-exporter branch — the one the UI leads with — would have made
// this lane silently empty for every real cluster.
func TestFilesystem_KeepsBothBranchesAndGuardsTheOverlap(t *testing.T) {
	q := fsBuilder().buildFilesystem()

	for _, want := range []string{
		"node_filesystem_avail_bytes", "node_filesystem_size_bytes", // node-exporter
		"node_fs_used_bytes", "node_fs_capacity_bytes", // the agent's own
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lost %s", want)
		}
	}
	// Without the guard both branches return on a node that HAS node-exporter,
	// and the node reports a phantom extra series with no mountpoint.
	if !strings.Contains(q, "unless on(node) node_filesystem_avail_bytes") {
		t.Error("the fallback is not guarded; a node with node-exporter would double-report")
	}
}

// The org scope must be first in every selector — the invariant the builders
// are written around, so that no query can read another tenant's series.
func TestFilesystem_IsTenantScopedEverywhere(t *testing.T) {
	q := fsBuilder().buildFilesystem()
	for _, sel := range strings.Split(q, "{")[1:] {
		body := sel[:strings.Index(sel, "}")]
		if !strings.HasPrefix(body, `tenant_id="org-1",cluster_id="uid-9"`) {
			t.Errorf("a selector is not org-scoped first: {%s}", body)
		}
		if !strings.Contains(body, `node="aks-default-vmss000004"`) {
			t.Errorf("a selector lost the node: {%s}", body)
		}
	}
}

// Counting tmpfs and overlay as disk is how a node with plenty of room reports
// pressure; the kubelet's per-pod mounts are cardinality with no signal.
func TestFilesystem_ExcludesPseudoFilesystemsAndEphemeralMounts(t *testing.T) {
	q := fsBuilder().buildFilesystem()
	for _, want := range []string{"tmpfs", "overlay", "cgroup2", "^/var/lib/kubelet/.*", "^/run/.*"} {
		if !strings.Contains(q, want) {
			t.Errorf("filter lost %q", want)
		}
	}
	// Only the node-exporter branch can filter by mountpoint — the agent's
	// metric is already one coarse series per node and has no such label.
	if strings.Count(q, "fstype!~") != 2 {
		t.Errorf("fstype filter should appear on both node-exporter selectors, got %d", strings.Count(q, "fstype!~"))
	}
}

// A pod's disk usage is not the node's. Answering with the node's would have
// the model attribute a node-wide fill to one workload — which is the exact
// class of mistake this lane exists to prevent.
func TestFilesystem_RefusedForNonNodeKindsRatherThanApproximated(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "StatefulSet"} {
		b := fsBuilder()
		b.kind = kind
		_, err := runMetric(context.Background(), *b, MetricFilesystem,
			time.Now().Add(-time.Hour), time.Now(), time.Minute)
		if err == nil {
			t.Errorf("kind=%s was answered with the node's disk", kind)
			continue
		}
		if !strings.Contains(err.Error(), "kind=Node") {
			t.Errorf("kind=%s: error does not say what to do instead: %v", kind, err)
		}
	}
}

// The tool used to tell the model, in its own description, that it could not
// answer this. A model obeys that.
func TestFilesystem_IsOfferedAndNoLongerDisclaimed(t *testing.T) {
	var def *ToolDefinition
	for _, d := range ToolDefinitions() {
		if d.Name == "get_workload_metrics" {
			dd := d
			def = &dd
		}
	}
	if def == nil {
		t.Fatal("get_workload_metrics is gone")
	}
	props, _ := def.InputSchema["properties"].(map[string]interface{})
	raw, _ := props["metrics"].(map[string]interface{})
	items, _ := raw["items"].(map[string]interface{})
	enum, _ := items["enum"].([]string)
	var found bool
	for _, v := range enum {
		if v == string(MetricFilesystem) {
			found = true
		}
	}
	if !found {
		t.Fatalf("filesystem is not in the metric enum: %v", enum)
	}
	if strings.Contains(def.Description, "Disk metrics are not exposed") {
		t.Error("the description still tells the model it cannot answer disk questions")
	}
	low := strings.ToLower(def.Description)
	for _, cue := range []string{"diskpressure", "node-only", "ephemeral-storage"} {
		if !strings.Contains(low, cue) {
			t.Errorf("the description does not orient the model: missing %q", cue)
		}
	}
}
