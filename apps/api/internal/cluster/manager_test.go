package cluster

import (
	"strings"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// newBareManager builds a Manager that the test can poke at without
// going through NewManager (which tries to load a kubeconfig and dial
// a real apiserver). Only the registration/listing surfaces need
// exercising here — the connect path is covered by the multi-cluster
// e2e in commit 11.
func newBareManager() *Manager {
	return &Manager{
		kubeConfig: &clientcmdapi.Config{
			Contexts: map[string]*clientcmdapi.Context{},
			Clusters: map[string]*clientcmdapi.Cluster{},
		},
		wsHub:           websocket.NewHub(),
		metricInterval:  30 * time.Second,
		insightInterval: 30 * time.Second,
	}
}

func TestManager_AddAgentProxyCluster_RequiresRegistry(t *testing.T) {
	m := newBareManager()
	if _, err := m.AddAgentProxyCluster("c1", "Prod"); err == nil {
		t.Fatal("expected error when AgentRegistry is unset")
	}
}

func TestManager_AddAgentProxyCluster_RegistersAndLists(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())

	got, err := m.AddAgentProxyCluster("c-prod", "Production")
	if err != nil {
		t.Fatalf("AddAgentProxyCluster err = %v", err)
	}
	if got != "agent:c-prod" {
		t.Errorf("contextName = %q, want agent:c-prod", got)
	}

	clusters := m.ListClusters()
	if len(clusters) != 1 {
		t.Fatalf("len(ListClusters) = %d, want 1", len(clusters))
	}
	c := clusters[0]
	if c.Context != "agent:c-prod" {
		t.Errorf("Context = %q", c.Context)
	}
	if c.Source != "agent-proxy" {
		t.Errorf("Source = %q, want agent-proxy", c.Source)
	}
	if !strings.HasPrefix(c.Server, "https://c-prod.agent.local") {
		t.Errorf("Server = %q, want synthetic agent.local URL", c.Server)
	}
}

func TestManager_AddAgentProxyCluster_RejectsEmptyID(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	if _, err := m.AddAgentProxyCluster("", "x"); err == nil {
		t.Fatal("empty clusterID must error")
	}
}

func TestManager_AddAgentProxyCluster_Idempotent(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())

	if _, err := m.AddAgentProxyCluster("c1", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddAgentProxyCluster("c1", "second"); err != nil {
		t.Fatal("re-adding the same clusterID must succeed")
	}
	if got := len(m.ListClusters()); got != 1 {
		t.Errorf("ListClusters count = %d, want 1 (no duplicates)", got)
	}
}

func TestManager_RemoveAgentProxyCluster(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	if _, err := m.AddAgentProxyCluster("c1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddAgentProxyCluster("c2", ""); err != nil {
		t.Fatal(err)
	}

	m.RemoveAgentProxyCluster("c1")
	clusters := m.ListClusters()
	if len(clusters) != 1 || clusters[0].Context != "agent:c2" {
		t.Errorf("after remove c1, clusters = %+v", clusters)
	}
}

func TestManager_RemoveAgentProxyCluster_UnknownIsNoop(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	m.RemoveAgentProxyCluster("nope") // must not panic
	if got := len(m.ListClusters()); got != 0 {
		t.Errorf("Count = %d, want 0", got)
	}
}

func TestManager_AccessForContext_AgentProxyWinsOverKubeconfig(t *testing.T) {
	// Pin the precedence rule: if a contextName collides between
	// an agent-proxy registration and a kubeconfig entry, the
	// agent-proxy wins. Otherwise a stale kubeconfig entry could
	// silently shadow a freshly registered cluster and the manager
	// would dial the wrong apiserver.
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	m.kubeConfig.Contexts["agent:c1"] = &clientcmdapi.Context{Cluster: "ghost"}
	m.kubeConfig.Clusters["ghost"] = &clientcmdapi.Cluster{Server: "https://ghost"}
	if _, err := m.AddAgentProxyCluster("c1", ""); err != nil {
		t.Fatal(err)
	}

	access := m.accessForContextLocked("agent:c1")
	if access == nil || access.Mode != AccessModeAgentProxy {
		t.Fatalf("access = %+v, want agent-proxy mode", access)
	}
	if access.ClusterID != "c1" {
		t.Errorf("ClusterID = %q", access.ClusterID)
	}
}

func TestManager_AccessForContext_LocalFallback(t *testing.T) {
	m := newBareManager()
	m.kubeconfigPath = "/tmp/some-kubeconfig"
	m.kubeConfig.Contexts["kind-dev"] = &clientcmdapi.Context{Cluster: "kind-dev"}
	access := m.accessForContextLocked("kind-dev")
	if access == nil || access.Mode != AccessModeLocal {
		t.Fatalf("access = %+v, want local mode", access)
	}
	if access.KubeconfigContext != "kind-dev" || access.KubeconfigPath != "/tmp/some-kubeconfig" {
		t.Errorf("local fields not populated: %+v", access)
	}
}

func TestManager_AccessForContext_UnknownReturnsNil(t *testing.T) {
	m := newBareManager()
	if got := m.accessForContextLocked("ghost"); got != nil {
		t.Errorf("unknown context should return nil access, got %+v", got)
	}
}
