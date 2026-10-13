package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	corelisters "k8s.io/client-go/listers/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// singleNodeConnector is a single-node cluster as the cache kept it after its
// node went down: one node Ready, one pod Running, the API server last seen
// answering.
func singleNodeConnector(t *testing.T) *Connector {
	t.Helper()
	nodeIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	if err := nodeIdx.Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "aks-node-0"},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}); err != nil {
		t.Fatal(err)
	}
	podIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if err := podIdx.Add(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-0"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}); err != nil {
		t.Fatal(err)
	}
	return &Connector{
		nodeLister: corelisters.NewNodeLister(nodeIdx),
		podLister:  corelisters.NewPodLister(podIdx),
		// Checked a moment ago, so buildHealth does not go fetch it.
		apiServerReachable: true,
		apiServerCheckedAt: time.Now(),
	}
}

func checkByName(h models.ClusterHealth, name string) models.HealthCheck {
	for _, c := range h.Checks {
		if c.Name == name {
			return c
		}
	}
	return models.HealthCheck{}
}

func TestHealth_InContactGradesTheCache(t *testing.T) {
	c := singleNodeConnector(t)
	h := c.buildHealth()
	if got := checkByName(h, "nodes"); got.Status != "pass" {
		t.Fatalf("nodes = %+v, want pass", got)
	}
	if h.Status == "unknown" {
		t.Fatal("a connector in contact grades its cache")
	}
	if c.staleState() != nil {
		t.Error("a direct connection is never stale")
	}
}

func TestHealth_OutOfContactIsLastKnownNotHealthy(t *testing.T) {
	// The agent left with the node: the cache still says 1/1 Ready. That must
	// read as the last known state, never as a pass.
	c := singleNodeConnector(t)
	lostAt := time.Date(2026, 10, 12, 23, 2, 8, 0, time.UTC)
	c.SetContactLost(func() (time.Time, bool) { return lostAt, true })

	h := c.buildHealth()
	if h.Status != "unknown" || h.Score != 0 {
		t.Fatalf("status/score = %s/%d, want unknown/0", h.Status, h.Score)
	}
	for _, ck := range h.Checks {
		if ck.Status != "unknown" {
			t.Errorf("check %s = %s, want unknown", ck.Name, ck.Status)
		}
	}
	if got := checkByName(h, "nodes").Message; !strings.HasPrefix(got, "Last known: ") || !strings.Contains(got, "1 nodes are ready") {
		t.Errorf("nodes message = %q, want the last known state, labelled", got)
	}
	if got := checkByName(h, "pods").Message; !strings.HasPrefix(got, "Last known: ") {
		t.Errorf("pods message = %q, want the last known state, labelled", got)
	}
	if got := checkByName(h, "api-server").Message; strings.Contains(got, "responsive") {
		t.Errorf("api-server message = %q: it cannot be checked without the agent", got)
	}

	// Insights are counted, but they grade nothing.
	full := c.GetHealth(true, []models.Insight{{Severity: "critical"}})
	if full.Status != "unknown" || full.Score != 0 || full.Insights.Critical != 1 {
		t.Errorf("GetHealth = %s/%d with %d critical, want unknown/0 with 1", full.Status, full.Score, full.Insights.Critical)
	}

	st := c.staleState()
	if st == nil || st.Since == nil || !st.Since.Equal(lostAt) {
		t.Fatalf("stale = %+v, want since %v", st, lostAt)
	}
}

func TestStaleState_LossNotWitnessedHasNoTime(t *testing.T) {
	c := singleNodeConnector(t)
	c.SetContactLost(func() (time.Time, bool) { return time.Time{}, true })
	st := c.staleState()
	if st == nil || st.Since != nil {
		t.Fatalf("stale = %+v, want stale with no time", st)
	}
}

// The cluster list's "last seen" for a cluster that went dark is when its last
// agent left, not when that agent had registered (the durable record's stamp,
// which can be days older). The cluster stays listed: the registration is
// durable.
func TestManager_ListClusters_LastSeenIsWhenTheAgentLeft(t *testing.T) {
	m := newBareManager()
	reg := channel.NewAgentRegistry()
	m.SetAgentRegistry(reg)
	if _, err := m.AddAgentProxyCluster("c-lab", "Lab"); err != nil {
		t.Fatal(err)
	}
	a := channel.NewAgent("c-lab", "a1", "aks-node-0", nil, []string{"kube-proxy"}, nil)
	reg.Register(a)
	if got := m.ListClusters(context.Background())[0]; !got.AgentConnected || got.LastSeen != nil {
		t.Fatalf("connected row = connected %v, lastSeen %v; want connected, no last-seen", got.AgentConnected, got.LastSeen)
	}

	reg.Unregister(a)
	since, _ := reg.LostSince("c-lab")
	list := m.ListClusters(context.Background())
	if len(list) != 1 {
		t.Fatalf("clusters = %d, want the cluster still listed", len(list))
	}
	got := list[0]
	if got.AgentConnected {
		t.Fatal("the agent left")
	}
	if got.LastSeen == nil || !got.LastSeen.Equal(since) {
		t.Fatalf("lastSeen = %v, want %v (when the agent left)", got.LastSeen, since)
	}
}
