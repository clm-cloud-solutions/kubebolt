package cluster

import (
	"context"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/agentversion"
)

// debt-24: the cluster list says when a cluster's agent is out of date,
// judged by its oldest live agent, and says nothing once no agent is live.
func TestManager_ListClusters_AgentUpdate(t *testing.T) {
	m := newBareManager()
	reg := channel.NewAgentRegistry()
	m.SetAgentRegistry(reg)
	if _, err := m.AddAgentProxyCluster("c-lab", "Lab"); err != nil {
		t.Fatal(err)
	}
	reg.SetHelloMeta("c-lab", "a1", channel.HelloMeta{AgentVersion: agentversion.Latest})
	a1 := channel.NewAgent("c-lab", "a1", "node-0", nil, []string{"kube-proxy"}, nil)
	reg.Register(a1)
	reg.SetHelloMeta("c-lab", "a2", channel.HelloMeta{AgentVersion: "1.3.1"})
	a2 := channel.NewAgent("c-lab", "a2", "node-1", nil, []string{"kube-proxy"}, nil)
	reg.Register(a2)

	got := m.ListClusters(context.Background())[0].AgentUpdate
	if got == nil || got.Version != "1.3.1" || got.Level != agentversion.LevelRecommended || len(got.Reasons) == 0 {
		t.Fatalf("agentUpdate = %+v, want 1.3.1 recommended with its reasons", got)
	}

	reg.Unregister(a2)
	if got := m.ListClusters(context.Background())[0].AgentUpdate; got == nil || got.Level != agentversion.LevelCurrent {
		t.Fatalf("with only the current agent: agentUpdate = %+v, want current", got)
	}

	reg.Unregister(a1)
	if got := m.ListClusters(context.Background())[0].AgentUpdate; got != nil {
		t.Fatalf("no live agent: agentUpdate = %+v, want none", got)
	}
}
