package kobiplan

import (
	"maps"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

// The consolidation engine (plans-16): which nodes of a pool are left over
// once the pods it runs are packed onto the others. Freed requests lower the
// bill only when a node goes (26-nov), so this is what turns a right-sizing
// plan into money. Pure: the caller reads the nodes, the pods and what each
// pod really needs; this decides, the same way every time.

// ConsolidationCeiling is the share of a node's allocatable the pods packed
// onto it may request: the rest is room for a spike and for the pods of a
// node that fails. MinPoolNodes is the fewest nodes a pool is taken to.
const (
	ConsolidationCeiling = 0.80
	MinPoolNodes         = 3
)

// Pool label sources: who created the pool, read from the node's labels.
const (
	PoolEKS       = "eks"
	PoolAKS       = "aks"
	PoolGKE       = "gke"
	PoolKarpenter = "karpenter"
	// PoolNone: the nodes carry no pool label (kind, a self-managed cluster):
	// they are taken as one group, changed wherever they are created.
	PoolNone = "none"
)

// poolLabels in the order they are tried: a Karpenter node on EKS also
// carries no nodegroup label, an AKS node carries the agentpool one twice.
var poolLabels = []struct{ key, source string }{
	{"karpenter.sh/nodepool", PoolKarpenter},
	{"karpenter.sh/provisioner-name", PoolKarpenter},
	{"eks.amazonaws.com/nodegroup", PoolEKS},
	{"kubernetes.azure.com/agentpool", PoolAKS},
	{"agentpool", PoolAKS},
	{"cloud.google.com/gke-nodepool", PoolGKE},
}

// NodePool is the pool a node belongs to and who made it, from its labels.
func NodePool(labels map[string]string) (name, source string) {
	for _, l := range poolLabels {
		if v := labels[l.key]; v != "" {
			return v, l.source
		}
	}
	return "", PoolNone
}

// IsControlPlane: a control-plane node is never consolidated.
func IsControlPlane(labels map[string]string) bool {
	_, cp := labels["node-role.kubernetes.io/control-plane"]
	_, master := labels["node-role.kubernetes.io/master"]
	return cp || master
}

// Autoscaler kinds a cluster can have, from what runs in it.
const (
	AutoscalerNone              = "none"
	AutoscalerClusterAutoscaler = "cluster_autoscaler"
	AutoscalerKarpenter         = "karpenter"
)

// AutoscalerFacts is what the cluster shows of an autoscaler.
type AutoscalerFacts struct {
	// ClusterAutoscaler: a cluster-autoscaler Deployment, or the status
	// ConfigMap the managed ones (AKS, GKE, EKS with CA) write in kube-system.
	ClusterAutoscaler bool
}

// PoolAutoscaler says what removes this pool's spare nodes: Karpenter for its
// own pools, the Cluster Autoscaler when the cluster runs one, else nobody.
func PoolAutoscaler(source string, f AutoscalerFacts) string {
	switch {
	case source == PoolKarpenter:
		return AutoscalerKarpenter
	case f.ClusterAutoscaler:
		return AutoscalerClusterAutoscaler
	}
	return AutoscalerNone
}

// NodeFacts is one node as the engine reads it.
type NodeFacts struct {
	Name         string
	Pool         string
	PoolSource   string
	Zone         string
	InstanceType string
	// ProviderID is the node's spec.providerID (aws:///us-east-1c/i-0abc…):
	// the export names the instance to remove with it.
	ProviderID string
	Labels     map[string]string
	Taints     []corev1.Taint
	// Unschedulable nodes and control-plane nodes take no pods and are left
	// alone.
	Unschedulable bool
	ControlPlane  bool
	CPUMilli      int64 // allocatable
	MemBytes      int64 // allocatable
	// Monthly is the node's OpenCost list price per month, nil without it.
	Monthly *float64
}

// Pod blockers: why a pod keeps its node (the Cluster Autoscaler's own rules),
// and two of the engine's own:
//   - BlockUnsimulated: the pod has scheduling rules the engine does not
//     simulate (pod affinity, a hard topology spread, anti-affinity beyond the
//     node): moving it could leave it Pending, so its node stays;
//   - BlockNoPlace: the rules the engine does simulate (one replica per node,
//     node affinity, host ports, the node selector, taints, the volume's
//     zone) leave the pod no node among the ones that stay.
const (
	BlockNoController = "no_controller"
	BlockLocalStorage = "local_storage"
	BlockPDB          = "pdb"
	BlockNotEvictable = "safe_to_evict_false"
	BlockUnsimulated  = "unsimulated_rules"
	BlockNoPlace      = "no_place"
)

// HostnameTopology is the topology key of a rule about one node.
const HostnameTopology = "kubernetes.io/hostname"

// PodTerm is one required anti-affinity term on the node: no pod it selects,
// in its namespaces, may share the pod's node.
type PodTerm struct {
	Selector labels.Selector
	// Namespaces the term looks in; AllNamespaces for an empty namespace
	// selector.
	Namespaces    map[string]bool
	AllNamespaces bool
}

func (t PodTerm) selects(q PodFacts) bool {
	return (t.AllNamespaces || t.Namespaces[q.Namespace]) && t.Selector.Matches(labels.Set(q.Labels))
}

// PodFacts is one pod as the engine reads it: where it runs and what it
// needs. CPUMilli and MemBytes are what the caller decided it needs: the
// larger of its request (after a live right-sizing plan, when there is one)
// and its usage.
type PodFacts struct {
	Namespace string
	Name      string
	Node      string
	CPUMilli  int64
	MemBytes  int64
	// DaemonSet pods go with their node: they are not moved, and every node
	// carries its own.
	DaemonSet    bool
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	// VolumeZone: the zone of a volume it mounts; it can only move within it.
	VolumeZone string
	// Blocker, when set, keeps the pod, and so its node, where it is.
	Blocker string
	// Owner names who keeps it there, for the reason in words ("etl/loader").
	Owner string
	// Workload is the pod's workload as a person knows it ("etl/loader").
	Workload string

	// Its scheduling rules, as the scheduler reads them.
	Labels map[string]string
	// NodeAffinity is its required node affinity, nil without one.
	NodeAffinity *corev1.NodeSelector
	// AntiAffinity are its required anti-affinity terms on the node: one
	// replica per node, typically.
	AntiAffinity []PodTerm
	// HostPorts it binds on its node ("TCP/80"): two pods never share one.
	HostPorts []string
}

// NodeLoad is one node's requests before and after, as shares of its
// allocatable, for the screen that says why a node is left over.
type NodeLoad struct {
	Node      string  `json:"node"`
	Zone      string  `json:"zone,omitempty"`
	CPUBefore float64 `json:"cpuBefore"`
	MemBefore float64 `json:"memBefore"`
	CPUAfter  float64 `json:"cpuAfter"`
	MemAfter  float64 `json:"memAfter"`
	Removed   bool    `json:"removed,omitempty"`
	// Monthly is the node's list price, nil without one.
	Monthly *float64 `json:"monthly,omitempty"`
}

// NodeBlock is a node that could not go, and why.
type NodeBlock struct {
	Node   string `json:"node"`
	Reason string `json:"reason"`
	Pod    string `json:"pod,omitempty"`
	Owner  string `json:"owner,omitempty"`
}

// PoolVerdict is the engine's answer for one pool.
type PoolVerdict struct {
	Pool       string      `json:"pool"`
	PoolSource string      `json:"poolSource"`
	Autoscaler string      `json:"autoscaler"`
	Nodes      int         `json:"nodes"`
	Removable  []string    `json:"removable"`
	Blocked    []NodeBlock `json:"blocked,omitempty"`
	Loads      []NodeLoad  `json:"loads"`
	// Monthly is the list price of the removable nodes, nil when a removable
	// node has no price.
	Monthly *float64 `json:"monthly,omitempty"`
	// Why nothing can go, when nothing can: "min_nodes", "headroom",
	// "blocked".
	Kept string `json:"kept,omitempty"`
}

// Kept reasons.
const (
	KeptMinNodes = "min_nodes"
	KeptHeadroom = "headroom"
	KeptBlocked  = "blocked"
)

type podLoad struct{ cpu, mem int64 }

// SimulateConsolidation answers, pool by pool, which nodes are left over.
// For each candidate, least loaded first, it tries to move every pod it runs
// onto the nodes that stay, the largest pods first, each to the fullest node
// where it still fits under the ceiling and the scheduler's rules allow it
// (rulesAllow). A node goes only if every pod finds a place, the
// pool keeps MinPoolNodes and one node in every zone it spans, and what stays
// can still lose its largest node.
func SimulateConsolidation(nodes []NodeFacts, pods []PodFacts, autoscaler AutoscalerFacts) []PoolVerdict {
	byPool := map[string][]NodeFacts{}
	var order []string
	for _, n := range nodes {
		if n.ControlPlane || n.Unschedulable {
			continue
		}
		key := n.PoolSource + "/" + n.Pool
		if _, ok := byPool[key]; !ok {
			order = append(order, key)
		}
		byPool[key] = append(byPool[key], n)
	}
	sort.Strings(order)
	podsOn := map[string][]PodFacts{}
	for _, p := range pods {
		podsOn[p.Node] = append(podsOn[p.Node], p)
	}
	out := make([]PoolVerdict, 0, len(order))
	for _, key := range order {
		out = append(out, simulatePool(byPool[key], podsOn, autoscaler))
	}
	return out
}

func simulatePool(nodes []NodeFacts, podsOn map[string][]PodFacts, autoscaler AutoscalerFacts) PoolVerdict {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	v := PoolVerdict{
		Pool: nodes[0].Pool, PoolSource: nodes[0].PoolSource,
		Autoscaler: PoolAutoscaler(nodes[0].PoolSource, autoscaler),
		Nodes:      len(nodes), Removable: []string{},
	}
	// What each node carries: its DaemonSet pods stay with it; the rest can
	// move.
	fixed := map[string]podLoad{}
	movable := map[string]podLoad{}
	for _, n := range nodes {
		for _, p := range podsOn[n.Name] {
			if p.DaemonSet {
				fixed[n.Name] = addPod(fixed[n.Name], p)
			} else {
				movable[n.Name] = addPod(movable[n.Name], p)
			}
		}
	}
	before := map[string]podLoad{}
	// at: the pods on each node as the simulation moves them, for the rules
	// between pods (one replica per node, host ports).
	at := map[string][]PodFacts{}
	for _, n := range nodes {
		before[n.Name] = sumLoad(fixed[n.Name], movable[n.Name])
		at[n.Name] = podsOn[n.Name]
	}
	used := map[string]podLoad{}
	for k, l := range before {
		used[k] = l
	}

	// Candidates: least loaded first, the node with the fewest pods to move.
	cands := append([]NodeFacts(nil), nodes...)
	sort.SliceStable(cands, func(i, j int) bool {
		return loadShare(movable[cands[i].Name], cands[i]) < loadShare(movable[cands[j].Name], cands[j])
	})
	removed := map[string]bool{}
	zones := map[string]int{}
	for _, n := range nodes {
		zones[n.Zone]++
	}
	for _, c := range cands {
		if len(nodes)-len(removed) <= MinPoolNodes {
			if len(v.Removable) == 0 && v.Kept == "" {
				v.Kept = KeptMinNodes
			}
			break
		}
		if c.Zone != "" && zones[c.Zone] <= 1 {
			continue
		}
		if b := blockerOn(c.Name, podsOn); b != nil {
			v.Blocked = append(v.Blocked, *b)
			continue
		}
		trial := map[string]podLoad{}
		for k, l := range used {
			trial[k] = l
		}
		trialAt := maps.Clone(at)
		if ok, block := placePods(c, podsOn[c.Name], nodes, removed, trial, trialAt); !ok {
			if block != nil {
				v.Blocked = append(v.Blocked, *block)
			}
			continue
		}
		delete(trial, c.Name)
		delete(trialAt, c.Name)
		if !survivesLosingOne(nodes, removed, c.Name, trial) {
			if len(v.Removable) == 0 {
				v.Kept = KeptHeadroom
			}
			break
		}
		removed[c.Name] = true
		zones[c.Zone]--
		used, at = trial, trialAt
		v.Removable = append(v.Removable, c.Name)
	}
	if len(v.Removable) == 0 && v.Kept == "" && len(v.Blocked) > 0 {
		v.Kept = KeptBlocked
	}
	if len(v.Removable) > 0 {
		v.Kept = ""
	}

	var total float64
	priced := true
	for _, n := range nodes {
		b := before[n.Name]
		l := NodeLoad{Node: n.Name, Zone: n.Zone, CPUBefore: pctOf(b.cpu, n.CPUMilli), MemBefore: pctOf(b.mem, n.MemBytes), Monthly: n.Monthly}
		if removed[n.Name] {
			l.Removed = true
			if n.Monthly != nil {
				total += *n.Monthly
			} else {
				priced = false
			}
		} else {
			a := used[n.Name]
			l.CPUAfter, l.MemAfter = pctOf(a.cpu, n.CPUMilli), pctOf(a.mem, n.MemBytes)
		}
		v.Loads = append(v.Loads, l)
	}
	if len(v.Removable) > 0 && priced {
		m := round2(total)
		v.Monthly = &m
	}
	return v
}

// placePods moves the candidate's pods onto the nodes that stay, in trial and
// trialAt: the largest pods first, each onto the fullest node where it still
// fits and its rules allow it. A pod with no place answers false, with a
// block when some node had the room and only the rules kept the pod out.
func placePods(c NodeFacts, pods []PodFacts, nodes []NodeFacts, removed map[string]bool, trial map[string]podLoad, trialAt map[string][]PodFacts) (bool, *NodeBlock) {
	var moving []PodFacts
	for _, p := range pods {
		if !p.DaemonSet {
			moving = append(moving, p)
		}
	}
	sort.SliceStable(moving, func(i, j int) bool {
		return moving[i].CPUMilli+moving[i].MemBytes>>20 > moving[j].CPUMilli+moving[j].MemBytes>>20
	})
	for _, p := range moving {
		best := ""
		bestFree := int64(-1)
		roomOnly := false
		for _, n := range nodes {
			if n.Name == c.Name || removed[n.Name] || !roomFor(p, n, trial[n.Name]) {
				continue
			}
			if !rulesAllow(p, n, trialAt[n.Name]) {
				roomOnly = true
				continue
			}
			// Fullest first: the CPU it would have left, in millicores.
			free := int64(float64(n.CPUMilli)*ConsolidationCeiling) - trial[n.Name].cpu - p.CPUMilli
			if best == "" || free < bestFree {
				best, bestFree = n.Name, free
			}
		}
		if best == "" {
			if roomOnly {
				return false, &NodeBlock{Node: c.Name, Reason: BlockNoPlace, Pod: p.Namespace + "/" + p.Name, Owner: p.Workload}
			}
			return false, nil
		}
		trial[best] = addPod(trial[best], p)
		// Clip: a new backing array, so the slice the trial started from is
		// never written.
		trialAt[best] = append(slices.Clip(trialAt[best]), p)
	}
	return true, nil
}

// roomFor: the pod fits under the ceiling of what the node already carries.
func roomFor(p PodFacts, n NodeFacts, l podLoad) bool {
	return float64(l.cpu+p.CPUMilli) <= float64(n.CPUMilli)*ConsolidationCeiling &&
		float64(l.mem+p.MemBytes) <= float64(n.MemBytes)*ConsolidationCeiling
}

// rulesAllow: the scheduler's rules let the pod onto the node, with the pods
// it carries: the volume's zone, the node selector and node affinity, the
// taints, anti-affinity on the node both ways, and host ports.
func rulesAllow(p PodFacts, n NodeFacts, on []PodFacts) bool {
	if p.VolumeZone != "" && n.Zone != "" && p.VolumeZone != n.Zone {
		return false
	}
	for k, want := range p.NodeSelector {
		if n.Labels[k] != want {
			return false
		}
	}
	if p.NodeAffinity != nil {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n.Name, Labels: n.Labels}}
		if ok, err := nodeaffinity.NewLazyErrorNodeSelector(p.NodeAffinity).Match(node); err != nil || !ok {
			return false
		}
	}
	for _, t := range n.Taints {
		if (t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute) && !toleratesTaint(p.Tolerations, t) {
			return false
		}
	}
	for _, q := range on {
		for _, t := range p.AntiAffinity {
			if t.selects(q) {
				return false
			}
		}
		for _, t := range q.AntiAffinity {
			if t.selects(p) {
				return false
			}
		}
		for _, port := range p.HostPorts {
			if slices.Contains(q.HostPorts, port) {
				return false
			}
		}
	}
	return true
}

// toleratesTaint: the scheduler's own rule, without the comparison operators
// (a toleration with no key and Exists tolerates every taint).
func toleratesTaint(tols []corev1.Toleration, t corev1.Taint) bool {
	for _, tol := range tols {
		if tol.Effect != "" && tol.Effect != t.Effect {
			continue
		}
		switch tol.Operator {
		case corev1.TolerationOpExists:
			if tol.Key == "" || tol.Key == t.Key {
				return true
			}
		case corev1.TolerationOpEqual, "":
			if tol.Key == t.Key && tol.Value == t.Value {
				return true
			}
		}
	}
	return false
}

// survivesLosingOne: with the candidate gone, the nodes that stay can still
// take every pod of their largest node, up to their whole allocatable.
func survivesLosingOne(nodes []NodeFacts, removed map[string]bool, gone string, used map[string]podLoad) bool {
	var stay []NodeFacts
	for _, n := range nodes {
		if !removed[n.Name] && n.Name != gone {
			stay = append(stay, n)
		}
	}
	if len(stay) < 2 {
		return false
	}
	var cpu, mem, capCPU, capMem, bigCPU, bigMem int64
	for _, n := range stay {
		cpu += used[n.Name].cpu
		mem += used[n.Name].mem
		capCPU += n.CPUMilli
		capMem += n.MemBytes
		if n.CPUMilli > bigCPU {
			bigCPU = n.CPUMilli
		}
		if n.MemBytes > bigMem {
			bigMem = n.MemBytes
		}
	}
	return cpu <= capCPU-bigCPU && mem <= capMem-bigMem
}

func blockerOn(node string, podsOn map[string][]PodFacts) *NodeBlock {
	for _, p := range podsOn[node] {
		if p.Blocker != "" && !p.DaemonSet {
			return &NodeBlock{Node: node, Reason: p.Blocker, Pod: p.Namespace + "/" + p.Name, Owner: p.Owner}
		}
	}
	return nil
}

func addPod(l podLoad, p PodFacts) podLoad { return podLoad{l.cpu + p.CPUMilli, l.mem + p.MemBytes} }
func sumLoad(a, b podLoad) podLoad         { return podLoad{a.cpu + b.cpu, a.mem + b.mem} }

func loadShare(l podLoad, n NodeFacts) float64 {
	c, m := pctOf(l.cpu, n.CPUMilli), pctOf(l.mem, n.MemBytes)
	if c > m {
		return c
	}
	return m
}

func pctOf(v, of int64) float64 {
	if of <= 0 {
		return 0
	}
	return round2(float64(v) / float64(of) * 100)
}
