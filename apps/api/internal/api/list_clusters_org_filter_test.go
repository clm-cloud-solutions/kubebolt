package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// reqWithOrg builds a request whose context carries the resolved org, the way
// auth.ResolveTenant middleware would in production.
func reqWithOrg(org string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/clusters", nil)
	return r.WithContext(auth.WithTenantID(r.Context(), org))
}

func clusterNames(cs []cluster.ClusterInfo) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// TestFilterClustersByOrg_OSSNeutral verifies the filter is a pass-through
// whenever multi-tenancy is off OR the resolved org is the OSS sentinel — even
// for agent-proxy clusters that have no matching agent. Stock OSS must see every
// cluster unchanged.
func TestFilterClustersByOrg_OSSNeutral(t *testing.T) {
	clusters := []cluster.ClusterInfo{
		{Name: "file-1", Context: "file-1", Source: "file"},
		{Name: "proxy-1", Context: "proxy-1", Source: "agent-proxy", ClusterID: "uid-A"},
	}

	t.Run("multi-tenant disabled", func(t *testing.T) {
		prev := auth.MultiTenantEnabled
		auth.MultiTenantEnabled = false
		defer func() { auth.MultiTenantEnabled = prev }()

		h := &handlers{agentRegistry: channel.NewAgentRegistry()}
		got := h.filterClustersByOrg(reqWithOrg("some-org-uuid"), clusters)
		if len(got) != 2 {
			t.Fatalf("disabled multi-tenancy must not filter; got %v", clusterNames(got))
		}
	})

	t.Run("default tenant sentinel passes through", func(t *testing.T) {
		prev := auth.MultiTenantEnabled
		auth.MultiTenantEnabled = true
		defer func() { auth.MultiTenantEnabled = prev }()

		h := &handlers{agentRegistry: channel.NewAgentRegistry()}
		// DefaultTenantName is a NAME, not a stamped org UUID → no filtering.
		got := h.filterClustersByOrg(reqWithOrg(auth.DefaultTenantName), clusters)
		if len(got) != 2 {
			t.Fatalf("default tenant must not filter; got %v", clusterNames(got))
		}
	})
}

// TestFilterClustersByOrg_AgentProxyScoped verifies that with a real org, an
// agent-proxy cluster is kept only when the registry holds an agent for that
// cluster_id under the SAME org; non-agent clusters always pass.
func TestFilterClustersByOrg_AgentProxyScoped(t *testing.T) {
	prev := auth.MultiTenantEnabled
	auth.MultiTenantEnabled = true
	defer func() { auth.MultiTenantEnabled = prev }()

	const orgA, orgB = "org-a-uuid", "org-b-uuid"

	reg := channel.NewAgentRegistry()
	// org-A owns cluster uid-A; org-B owns cluster uid-B.
	reg.Register(channel.NewAgent("uid-A", "agent-a", "node-a",
		&auth.AgentIdentity{TenantID: orgA, Mode: auth.ModeIngestToken}, []string{"kube-proxy"}, nil))
	reg.Register(channel.NewAgent("uid-B", "agent-b", "node-b",
		&auth.AgentIdentity{TenantID: orgB, Mode: auth.ModeIngestToken}, []string{"kube-proxy"}, nil))

	clusters := []cluster.ClusterInfo{
		{Name: "file-1", Context: "file-1", Source: "file"},
		{Name: "uploaded-1", Context: "uploaded-1", Source: "uploaded"},
		{Name: "proxy-A", Context: "proxy-A", Source: "agent-proxy", ClusterID: "uid-A"},
		{Name: "proxy-B", Context: "proxy-B", Source: "agent-proxy", ClusterID: "uid-B"},
		{Name: "proxy-noagent", Context: "proxy-noagent", Source: "agent-proxy", ClusterID: "uid-Z"},
	}

	h := &handlers{agentRegistry: reg}

	gotA := clusterNames(h.filterClustersByOrg(reqWithOrg(orgA), clusters))
	// org-A keeps non-agent clusters + its own proxy, drops org-B's proxy and
	// the orphan proxy with no live agent.
	wantA := map[string]bool{"file-1": true, "uploaded-1": true, "proxy-A": true}
	if len(gotA) != len(wantA) {
		t.Fatalf("org-A filtered list = %v, want keys %v", gotA, wantA)
	}
	for _, n := range gotA {
		if !wantA[n] {
			t.Fatalf("org-A should not see %q (got %v)", n, gotA)
		}
	}

	gotB := clusterNames(h.filterClustersByOrg(reqWithOrg(orgB), clusters))
	wantB := map[string]bool{"file-1": true, "uploaded-1": true, "proxy-B": true}
	if len(gotB) != len(wantB) {
		t.Fatalf("org-B filtered list = %v, want keys %v", gotB, wantB)
	}
	for _, n := range gotB {
		if !wantB[n] {
			t.Fatalf("org-B should not see %q (got %v)", n, gotB)
		}
	}
}
