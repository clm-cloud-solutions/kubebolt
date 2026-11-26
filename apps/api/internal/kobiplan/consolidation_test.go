package kobiplan

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const gib = int64(1) << 30

// A pool of m6i.xlarge-shaped nodes (4 cores, 16 GiB), 129,40 a month each,
// spread over three zones.
func pool(n int) []NodeFacts {
	zones := []string{"us-east-1a", "us-east-1a", "us-east-1b", "us-east-1c", "us-east-1c", "us-east-1b"}
	price := 129.40
	out := make([]NodeFacts, n)
	for i := range out {
		out[i] = NodeFacts{
			Name: fmt.Sprintf("node-%d", i+1), Pool: "general", PoolSource: PoolEKS, Zone: zones[i%len(zones)],
			InstanceType: "m6i.xlarge", CPUMilli: 4000, MemBytes: 16 * gib, Monthly: &price,
			Labels: map[string]string{"eks.amazonaws.com/nodegroup": "general"},
		}
	}
	return out
}

func pod(node, name string, cpu int64, memGiB float64) PodFacts {
	return PodFacts{Namespace: "shop", Name: name, Node: node, CPUMilli: cpu, MemBytes: int64(memGiB * float64(gib))}
}

// Every node runs a DaemonSet pod that stays with it.
func withDaemons(nodes []NodeFacts, pods []PodFacts) []PodFacts {
	for _, n := range nodes {
		pods = append(pods, PodFacts{Namespace: "kube-system", Name: "agent-" + n.Name, Node: n.Name, CPUMilli: 100, MemBytes: gib / 4, DaemonSet: true})
	}
	return pods
}

// The Sucal shape: five nodes half used and one nearly idle. The idle one
// goes, its pods land on the others under the ceiling, and its list price is
// the figure.
func TestConsolidation_OneNodeLeftOver(t *testing.T) {
	nodes := pool(5)
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "api-1", 2000, 8), pod("node-2", "api-2", 1900, 7), pod("node-3", "web-1", 2100, 8),
		pod("node-4", "web-2", 1800, 8), pod("node-5", "cron-1", 600, 2), pod("node-5", "cron-2", 400, 2),
	})
	v := SimulateConsolidation(nodes, pods, AutoscalerFacts{})
	if len(v) != 1 {
		t.Fatalf("pools = %+v", v)
	}
	p := v[0]
	if !slices.Equal(p.Removable, []string{"node-5"}) || p.Monthly == nil || *p.Monthly != 129.40 || p.Kept != "" {
		t.Fatalf("verdict = %+v", p)
	}
	if p.Autoscaler != AutoscalerNone || p.Pool != "general" || p.PoolSource != PoolEKS || p.Nodes != 5 {
		t.Fatalf("pool = %+v", p)
	}
	for _, l := range p.Loads {
		if !l.Removed && (l.CPUAfter > 80 || l.MemAfter > 80) {
			t.Fatalf("%s left over the ceiling: %+v", l.Node, l)
		}
		if l.Removed && l.Node != "node-5" {
			t.Fatalf("removed %s", l.Node)
		}
	}
}

// A pool at the minimum keeps every node.
func TestConsolidation_MinNodes(t *testing.T) {
	nodes := pool(3)
	v := SimulateConsolidation(nodes, withDaemons(nodes, nil), AutoscalerFacts{ClusterAutoscaler: true})[0]
	if len(v.Removable) != 0 || v.Kept != KeptMinNodes || v.Autoscaler != AutoscalerClusterAutoscaler {
		t.Fatalf("verdict = %+v", v)
	}
}

// A pod a PDB keeps in place keeps its node; the next node is tried.
func TestConsolidation_BlockedNode(t *testing.T) {
	nodes := pool(5)
	loader := pod("node-5", "loader", 200, 1)
	loader.Blocker, loader.Owner = BlockPDB, "etl/loader"
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 1800, 6), pod("node-2", "b", 1800, 6), pod("node-3", "c", 1800, 6), pod("node-4", "d", 800, 3), loader,
	})
	v := SimulateConsolidation(nodes, pods, AutoscalerFacts{})[0]
	if len(v.Blocked) != 1 || v.Blocked[0].Node != "node-5" || v.Blocked[0].Reason != BlockPDB || v.Blocked[0].Owner != "etl/loader" {
		t.Fatalf("blocked = %+v", v.Blocked)
	}
	// node-4, the next least loaded, goes; then node-1 too, whose pod fits on
	// the blocked node, which stays. Never node-5, and never below 3 nodes.
	if !slices.Equal(v.Removable, []string{"node-4", "node-1"}) || slices.Contains(v.Removable, "node-5") {
		t.Fatalf("removable = %v", v.Removable)
	}
}

// Nothing fits elsewhere without passing the ceiling, or losing a node would
// no longer be survivable: the pool keeps its nodes and says why.
func TestConsolidation_Headroom(t *testing.T) {
	nodes := pool(4)
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 2600, 10), pod("node-2", "b", 2600, 10), pod("node-3", "c", 2600, 10), pod("node-4", "d", 2000, 8),
	})
	v := SimulateConsolidation(nodes, pods, AutoscalerFacts{})[0]
	if len(v.Removable) != 0 {
		t.Fatalf("removed %v from a full pool", v.Removable)
	}
}

// The last node of a zone stays, and a pod bound to a zone's volume only
// moves within it.
func TestConsolidation_Zones(t *testing.T) {
	nodes := pool(4) // a, a, b, c
	pods := withDaemons(nodes, []PodFacts{
		pod("node-1", "a", 1000, 4), pod("node-2", "b", 1000, 4), pod("node-3", "c", 100, 1), pod("node-4", "d", 100, 1),
	})
	v := SimulateConsolidation(nodes, pods, AutoscalerFacts{})[0]
	// node-3 and node-4 are the least loaded, but each is the only node of
	// its zone; node-1 goes instead, and the pool stops at three.
	if !slices.Equal(v.Removable, []string{"node-1"}) {
		t.Fatalf("removable = %v", v.Removable)
	}
	bound := pod("node-2", "db", 100, 1)
	bound.VolumeZone = "us-east-1b"
	if !rulesAllow(bound, nodes[2], nil) || rulesAllow(bound, nodes[0], nil) {
		t.Fatal("a pod bound to us-east-1b must fit only there")
	}
}

// A pod's node selector and the node's taints decide where it can go.
func TestConsolidation_SelectorsAndTaints(t *testing.T) {
	n := pool(1)[0]
	sel := pod("x", "gpu-job", 100, 1)
	sel.NodeSelector = map[string]string{"accelerator": "nvidia"}
	if rulesAllow(sel, n, nil) {
		t.Fatal("a pod that asks for a label the node lacks must not fit")
	}
	n.Taints = []corev1.Taint{{Key: "dedicated", Value: "batch", Effect: corev1.TaintEffectNoSchedule}}
	plain := pod("x", "web", 100, 1)
	if rulesAllow(plain, n, nil) {
		t.Fatal("a tainted node must refuse a pod without the toleration")
	}
	plain.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "batch", Effect: corev1.TaintEffectNoSchedule}}
	if !rulesAllow(plain, n, nil) {
		t.Fatal("the toleration lets it in")
	}
	plain.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	if !rulesAllow(plain, n, nil) {
		t.Fatal("Exists with no key tolerates every taint")
	}
}

// One replica per node: a Deployment of four whose pods refuse to share a
// node keeps four nodes. Without the rule the pool would go from 5 to 3.
func TestConsolidation_OneReplicaPerNode(t *testing.T) {
	nodes := pool(5)
	spread := func(node, name string) PodFacts {
		p := pod(node, name, 300, 1)
		p.Workload, p.Labels = "shop/cache", map[string]string{"app": "cache"}
		p.AntiAffinity = []PodTerm{{Selector: labels.SelectorFromSet(labels.Set{"app": "cache"}), Namespaces: map[string]bool{"shop": true}}}
		return p
	}
	pods := withDaemons(nodes, []PodFacts{spread("node-1", "cache-1"), spread("node-2", "cache-2"), spread("node-3", "cache-3"), spread("node-4", "cache-4")})

	free := SimulateConsolidation(nodes, stripRules(pods), AutoscalerFacts{})[0]
	if len(free.Removable) != 2 {
		t.Fatalf("without the rule = %+v", free)
	}
	v := SimulateConsolidation(nodes, pods, AutoscalerFacts{})[0]
	if !slices.Equal(v.Removable, []string{"node-5"}) {
		t.Fatalf("removable = %v", v.Removable)
	}
	if len(v.Blocked) == 0 || v.Blocked[0].Reason != BlockNoPlace || v.Blocked[0].Owner != "shop/cache" {
		t.Fatalf("blocked = %+v", v.Blocked)
	}
	if w := BlockWords(v.Blocked[0]); !strings.Contains(w, "a pod of shop/cache has nowhere to go") {
		t.Fatalf("words = %q", w)
	}

	// The rule binds both ways: a pod that carries none still cannot join a
	// replica whose term selects it.
	plain := pod("node-5", "cache-5", 300, 1)
	plain.Labels = map[string]string{"app": "cache"}
	if rulesAllow(plain, nodes[0], []PodFacts{pods[0]}) {
		t.Fatal("a replica's anti-affinity must keep a matching pod off its node")
	}
	other := pod("node-5", "web", 300, 1)
	other.Labels = map[string]string{"app": "web"}
	if !rulesAllow(other, nodes[0], []PodFacts{pods[0]}) {
		t.Fatal("a pod the term does not select may share the node")
	}
	elsewhere := plain
	elsewhere.Namespace = "billing"
	if !rulesAllow(elsewhere, nodes[0], []PodFacts{pods[0]}) {
		t.Fatal("the term looks only in its namespaces")
	}
}

// stripRules: the same pods without their scheduling rules.
func stripRules(pods []PodFacts) []PodFacts {
	out := slices.Clone(pods)
	for i := range out {
		out[i].AntiAffinity, out[i].NodeAffinity, out[i].HostPorts = nil, nil, nil
	}
	return out
}

// Node affinity and host ports, as the scheduler reads them.
func TestConsolidation_NodeAffinityAndHostPorts(t *testing.T) {
	nodes := pool(3)
	nodes[0].Labels = map[string]string{"topology.kubernetes.io/zone": "us-east-1a"}
	nodes[2].Labels = map[string]string{"topology.kubernetes.io/zone": "us-east-1b"}
	pinned := pod("x", "ledger", 100, 1)
	pinned.NodeAffinity = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1a"}},
	}}}}
	if !rulesAllow(pinned, nodes[0], nil) || rulesAllow(pinned, nodes[2], nil) {
		t.Fatal("required node affinity must allow us-east-1a only")
	}
	byName := pod("x", "local", 100, 1)
	byName.NodeAffinity = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{
		{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-2"}},
	}}}}
	if rulesAllow(byName, nodes[0], nil) || !rulesAllow(byName, nodes[1], nil) {
		t.Fatal("a pod pinned to a node by name fits that node only")
	}

	ingress := pod("x", "ingress-2", 100, 1)
	ingress.HostPorts = []string{"TCP/443"}
	on := []PodFacts{{Namespace: "shop", Name: "ingress-1", HostPorts: []string{"TCP/443"}}}
	if rulesAllow(ingress, nodes[0], on) {
		t.Fatal("two pods never share a host port")
	}
	ingress.HostPorts = []string{"UDP/443"}
	if !rulesAllow(ingress, nodes[0], on) {
		t.Fatal("another protocol is another port")
	}
}

// Control-plane and cordoned nodes are not part of any pool.
func TestConsolidation_SkipsControlPlaneAndCordoned(t *testing.T) {
	nodes := pool(5)
	nodes[0].ControlPlane = true
	nodes[1].Unschedulable = true
	v := SimulateConsolidation(nodes, nil, AutoscalerFacts{})[0]
	if v.Nodes != 3 || v.Kept != KeptMinNodes {
		t.Fatalf("verdict = %+v", v)
	}
}

// The pool comes from the labels each provider sets.
func TestNodePool(t *testing.T) {
	for _, c := range []struct {
		labels       map[string]string
		name, source string
	}{
		{map[string]string{"eks.amazonaws.com/nodegroup": "general"}, "general", PoolEKS},
		{map[string]string{"kubernetes.azure.com/agentpool": "default", "agentpool": "default"}, "default", PoolAKS},
		{map[string]string{"cloud.google.com/gke-nodepool": "pool-1"}, "pool-1", PoolGKE},
		{map[string]string{"karpenter.sh/nodepool": "default", "eks.amazonaws.com/nodegroup": "x"}, "default", PoolKarpenter},
		{map[string]string{"kubernetes.io/hostname": "kind-worker"}, "", PoolNone},
	} {
		if n, s := NodePool(c.labels); n != c.name || s != c.source {
			t.Errorf("%v → %q %q", c.labels, n, s)
		}
	}
	if !IsControlPlane(map[string]string{"node-role.kubernetes.io/control-plane": ""}) || IsControlPlane(map[string]string{}) {
		t.Error("control plane")
	}
	if PoolAutoscaler(PoolKarpenter, AutoscalerFacts{}) != AutoscalerKarpenter || PoolAutoscaler(PoolEKS, AutoscalerFacts{}) != AutoscalerNone {
		t.Error("autoscaler")
	}
}
