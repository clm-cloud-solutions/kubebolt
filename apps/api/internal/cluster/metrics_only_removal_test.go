package cluster

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

func newRegistryManager(t *testing.T) *Manager {
	t.Helper()
	m := newBareManager()
	m.SetStorage(newBoltStorage(t))
	m.SetAgentRegistry(channel.NewAgentRegistry())
	return m
}

// The operator's delete removes a cluster that was only ever metrics-only: its
// flag and its context go. Before, RemoveAgentProxyCluster returned early for a
// cluster with no agent-proxy entry — the delete answered 200, the cluster
// stayed listed, and a stale header for it still resolved as metrics-only.
func TestRemove_MetricsOnlyClusterIsRemoved(t *testing.T) {
	m := newRegistryManager(t)
	ctxName, err := m.AddMetricsOnlyCluster("mo-1", "monitored")
	if err != nil {
		t.Fatal(err)
	}
	m.RemoveAgentProxyCluster("mo-1")
	if m.IsMetricsOnlyContext(ctxName) {
		t.Error("the deleted cluster is still metrics-only")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.kubeConfig.Contexts[ctxName] != nil {
		t.Error("the deleted cluster's context is still registered")
	}
}

// The first-registration demotion re-checks under the lock: a cluster that is
// agent-proxy by the time it writes is left alone.
func TestAddMetricsOnlyClusterUnlessProxy(t *testing.T) {
	m := newRegistryManager(t)
	ctxName, err := m.AddAgentProxyCluster("c1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if added, err := m.AddMetricsOnlyClusterUnlessProxy("c1", "c1"); err != nil || added {
		t.Fatalf("added=%v err=%v: an agent-proxy cluster must not be demoted by the first-registration path", added, err)
	}
	if added, _ := m.AddMetricsOnlyClusterUnlessProxy("c2", "c2"); !added {
		t.Error("a cluster that is not agent-proxy must be registered metrics-only")
	}
	if m.IsMetricsOnlyContext(ctxName) {
		t.Error("c1 was demoted")
	}
}

// The deferred demotion refuses a cluster deleted during the grace window.
func TestDemoteRegisteredCluster_RefusesADeletedCluster(t *testing.T) {
	m := newRegistryManager(t)
	if _, err := m.AddAgentProxyCluster("c1", "c1"); err != nil {
		t.Fatal(err)
	}
	m.RemoveAgentProxyCluster("c1")
	if demoted, err := m.DemoteRegisteredCluster("c1", "c1"); err != nil || demoted {
		t.Fatalf("demoted=%v err=%v: a deleted cluster must not come back", demoted, err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.kubeConfig.Contexts[AgentProxyContextName("c1")] != nil {
		t.Error("the deleted cluster's context was re-created")
	}
}
