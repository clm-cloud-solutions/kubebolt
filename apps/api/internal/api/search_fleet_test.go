package api

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

func TestSearchableCluster(t *testing.T) {
	cases := []struct {
		name string
		ci   cluster.ClusterInfo
		want bool
	}{
		{"connected kubeconfig", cluster.ClusterInfo{Status: "connected"}, true},
		{"agent live but not active", cluster.ClusterInfo{Status: "disconnected", AgentConnected: true}, true},
		{"disconnected, no agent", cluster.ClusterInfo{Status: "disconnected"}, false},
		{"metrics-only never searchable", cluster.ClusterInfo{Status: "connected", Mode: "metrics-only"}, false},
		{"error state", cluster.ClusterInfo{Status: "error"}, false},
	}
	for _, tc := range cases {
		if got := searchableCluster(tc.ci); got != tc.want {
			t.Errorf("%s: searchable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClusterLabel(t *testing.T) {
	if got := clusterLabel(cluster.ClusterInfo{Context: "ctx-1", DisplayName: "prod-us"}); got != "prod-us" {
		t.Errorf("display name must win, got %q", got)
	}
	if got := clusterLabel(cluster.ClusterInfo{Context: "ctx-1"}); got != "ctx-1" {
		t.Errorf("context fallback, got %q", got)
	}
}

func TestMergeFleetResults(t *testing.T) {
	per := map[string][]searchResult{
		"prod-us": {
			{Name: "payments-api", ResourceType: "deployments"},
			{Name: "auth-svc", ResourceType: "deployments"},
		},
		"eu-1": {
			{Name: "payments-api", ResourceType: "deployments"},
		},
	}
	merged := mergeFleetResults(per, 50)
	if len(merged) != 3 {
		t.Fatalf("merged = %d, want 3", len(merged))
	}
	// Stable order: cluster label asc, then name asc — and every hit
	// carries its cluster.
	if merged[0].Cluster != "eu-1" || merged[0].Name != "payments-api" {
		t.Errorf("first hit = %+v, want eu-1/payments-api", merged[0])
	}
	if merged[1].Cluster != "prod-us" || merged[1].Name != "auth-svc" {
		t.Errorf("second hit = %+v, want prod-us/auth-svc", merged[1])
	}
	for _, m := range merged {
		if m.Cluster == "" {
			t.Errorf("hit without cluster label: %+v", m)
		}
	}

	// Global cap truncates deterministically.
	if got := mergeFleetResults(per, 2); len(got) != 2 {
		t.Errorf("capped merge = %d, want 2", len(got))
	}
}
