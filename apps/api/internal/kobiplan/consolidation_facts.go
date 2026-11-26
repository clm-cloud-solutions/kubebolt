package kobiplan

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// ConsolidationObjects is what the cluster holds, as the informers have it.
type ConsolidationObjects struct {
	Nodes       []*corev1.Node
	Pods        []*corev1.Pod
	PDBs        []*policyv1.PodDisruptionBudget
	Deployments []*appsv1.Deployment
	// CAStatus: kube-system/cluster-autoscaler-status exists, the ConfigMap
	// the managed autoscalers (AKS, GKE, EKS with CA) write.
	CAStatus bool
}

// PodNeed is a pod's usage over the window, or its planned requests: CPU in
// millicores and memory in bytes.
type PodNeed struct {
	CPUMilli int64
	MemBytes int64
}

// PlannedContainers is what a live right-sizing plan leaves a pod's
// containers requesting, by container name; a container it does not touch
// keeps its own requests.
type PlannedContainers map[string]PodNeed

const (
	annotationSafeToEvict = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	annotationMirror      = "kubernetes.io/config.mirror"
	labelZone             = "topology.kubernetes.io/zone"
	labelZoneBeta         = "failure-domain.beta.kubernetes.io/zone"
	labelInstanceType     = "node.kubernetes.io/instance-type"
	labelInstanceTypeBeta = "beta.kubernetes.io/instance-type"
)

// ConsolidationFacts turns the cluster's objects into what the engine reads.
// A pod needs the larger of its requests (the planned ones when a live
// right-sizing plan changes them) and its usage, keyed namespace/name in both
// maps. prices is each node's monthly list price.
func ConsolidationFacts(o ConsolidationObjects, usage map[string]PodNeed, planned map[string]PlannedContainers, prices map[string]float64) ([]NodeFacts, []PodFacts, AutoscalerFacts) {
	zoneOf := map[string]string{}
	var nodes []NodeFacts
	for _, n := range o.Nodes {
		pool, source := NodePool(n.Labels)
		f := NodeFacts{
			Name: n.Name, Pool: pool, PoolSource: source,
			Zone:          firstLabel(n.Labels, labelZone, labelZoneBeta),
			InstanceType:  firstLabel(n.Labels, labelInstanceType, labelInstanceTypeBeta),
			ProviderID:    n.Spec.ProviderID,
			Labels:        n.Labels,
			Taints:        n.Spec.Taints,
			Unschedulable: n.Spec.Unschedulable,
			ControlPlane:  IsControlPlane(n.Labels),
			CPUMilli:      n.Status.Allocatable.Cpu().MilliValue(),
			MemBytes:      n.Status.Allocatable.Memory().Value(),
		}
		if p, ok := prices[n.Name]; ok {
			price := p
			f.Monthly = &price
		}
		zoneOf[n.Name] = f.Zone
		nodes = append(nodes, f)
	}

	var pods []PodFacts
	for _, p := range o.Pods {
		if p.Spec.NodeName == "" || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		key := p.Namespace + "/" + p.Name
		req := podRequests(p, planned[key])
		need := req
		if u, ok := usage[key]; ok {
			need.CPUMilli = max(need.CPUMilli, u.CPUMilli)
			need.MemBytes = max(need.MemBytes, u.MemBytes)
		}
		f := PodFacts{
			Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName,
			CPUMilli: need.CPUMilli, MemBytes: need.MemBytes,
			NodeSelector: p.Spec.NodeSelector, Tolerations: p.Spec.Tolerations,
		}
		ctrl := metav1.GetControllerOf(p)
		switch {
		case ctrl != nil && ctrl.Kind == "DaemonSet", ctrl != nil && ctrl.Kind == "Node", p.Annotations[annotationMirror] != "":
			f.DaemonSet = true
		}
		if hasPVC(p) {
			// Block storage is zonal: a pod with a claim stays in its zone.
			f.VolumeZone = zoneOf[p.Spec.NodeName]
		}
		f.Workload = blockerOwner(p, ctrl)
		unsimulated := placementFacts(p, &f)
		if !f.DaemonSet {
			f.Blocker, f.Owner = podBlocker(p, ctrl, o.PDBs)
			if f.Blocker == "" && unsimulated {
				f.Blocker, f.Owner = BlockUnsimulated, f.Workload
			}
		}
		pods = append(pods, f)
	}

	return nodes, pods, AutoscalerFacts{ClusterAutoscaler: o.CAStatus || runsClusterAutoscaler(o.Deployments)}
}

// placementFacts reads the pod's scheduling rules into f: its labels, its
// required node affinity, its required anti-affinity on the node and its host
// ports. It answers true when the pod has a hard rule the engine does not
// simulate: required pod affinity, anti-affinity on any other topology (a
// zone spans other pools) or by namespace labels, and a topology spread that
// refuses to schedule.
func placementFacts(p *corev1.Pod, f *PodFacts) bool {
	f.Labels = p.Labels
	unsimulated := false
	for _, c := range p.Spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			unsimulated = true
		}
	}
	for _, c := range p.Spec.Containers {
		for _, port := range c.Ports {
			if port.HostPort > 0 {
				proto := port.Protocol
				if proto == "" {
					proto = corev1.ProtocolTCP
				}
				f.HostPorts = append(f.HostPorts, fmt.Sprintf("%s/%d", proto, port.HostPort))
			}
		}
	}
	aff := p.Spec.Affinity
	if aff == nil {
		return unsimulated
	}
	if aff.NodeAffinity != nil {
		f.NodeAffinity = aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	}
	if aff.PodAffinity != nil && len(aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		unsimulated = true
	}
	if aff.PodAntiAffinity == nil {
		return unsimulated
	}
	for _, t := range aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		term, ok := antiAffinityTerm(p, t)
		if !ok {
			unsimulated = true
			continue
		}
		if term != nil {
			f.AntiAffinity = append(f.AntiAffinity, *term)
		}
	}
	return unsimulated
}

// antiAffinityTerm reads one required anti-affinity term as the scheduler
// does: a nil label selector selects no pod; matchLabelKeys and
// mismatchLabelKeys add the pod's own values; no namespaces means the pod's,
// an empty namespace selector every one. ok is false for a term the engine
// cannot follow (another topology, a namespace selector with terms).
func antiAffinityTerm(p *corev1.Pod, t corev1.PodAffinityTerm) (*PodTerm, bool) {
	if t.TopologyKey != HostnameTopology {
		return nil, false
	}
	if t.LabelSelector == nil {
		return nil, true
	}
	sel, err := metav1.LabelSelectorAsSelector(t.LabelSelector)
	if err != nil {
		return nil, false
	}
	for _, k := range t.MatchLabelKeys {
		if v, ok := p.Labels[k]; ok {
			r, err := labels.NewRequirement(k, selection.In, []string{v})
			if err != nil {
				return nil, false
			}
			sel = sel.Add(*r)
		}
	}
	for _, k := range t.MismatchLabelKeys {
		if v, ok := p.Labels[k]; ok {
			r, err := labels.NewRequirement(k, selection.NotIn, []string{v})
			if err != nil {
				return nil, false
			}
			sel = sel.Add(*r)
		}
	}
	term := &PodTerm{Selector: sel}
	switch ns := t.NamespaceSelector; {
	case ns != nil && len(ns.MatchLabels) == 0 && len(ns.MatchExpressions) == 0:
		term.AllNamespaces = true
	case ns != nil:
		return nil, false
	default:
		term.Namespaces = map[string]bool{}
		for _, n := range t.Namespaces {
			term.Namespaces[n] = true
		}
		if len(term.Namespaces) == 0 {
			term.Namespaces[p.Namespace] = true
		}
	}
	return term, true
}

// podRequests sums the containers' requests, the planned ones where a plan
// sets them, and takes the largest init container when it asks for more.
func podRequests(p *corev1.Pod, planned PlannedContainers) PodNeed {
	var r PodNeed
	for _, c := range p.Spec.Containers {
		cpu, mem := c.Resources.Requests.Cpu().MilliValue(), c.Resources.Requests.Memory().Value()
		if pl, ok := planned[c.Name]; ok {
			if pl.CPUMilli > 0 {
				cpu = pl.CPUMilli
			}
			if pl.MemBytes > 0 {
				mem = pl.MemBytes
			}
		}
		r.CPUMilli += cpu
		r.MemBytes += mem
	}
	for _, c := range p.Spec.InitContainers {
		r.CPUMilli = max(r.CPUMilli, c.Resources.Requests.Cpu().MilliValue())
		r.MemBytes = max(r.MemBytes, c.Resources.Requests.Memory().Value())
	}
	return r
}

// podBlocker: the Cluster Autoscaler's own reasons a pod keeps its node. A
// pod marked safe to evict is never blocked, except by a PDB, which the
// eviction API enforces whatever the annotation says. An emptyDir is not a
// block: it is lost on every rollout too. A hostPath ties it to the node.
func podBlocker(p *corev1.Pod, ctrl *metav1.OwnerReference, pdbs []*policyv1.PodDisruptionBudget) (string, string) {
	if pdb := blockingPDB(p, pdbs); pdb != "" {
		return BlockPDB, pdb
	}
	safe := p.Annotations[annotationSafeToEvict]
	if safe == "true" {
		return "", ""
	}
	if safe == "false" {
		return BlockNotEvictable, blockerOwner(p, ctrl)
	}
	if ctrl == nil {
		return BlockNoController, p.Namespace + "/" + p.Name
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath != nil {
			return BlockLocalStorage, blockerOwner(p, ctrl)
		}
	}
	return "", ""
}

// blockingPDB names the PDB that allows no disruption of this pod now. In
// policy/v1 a nil selector selects no pod and an empty one every pod of the
// namespace.
func blockingPDB(p *corev1.Pod, pdbs []*policyv1.PodDisruptionBudget) string {
	for _, pdb := range pdbs {
		if pdb.Namespace != p.Namespace || pdb.Spec.Selector == nil || pdb.Status.DisruptionsAllowed > 0 {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		return pdb.Namespace + "/" + pdb.Name
	}
	return ""
}

// blockerOwner names the workload a person knows: a ReplicaSet's pods are
// their Deployment's (the ReplicaSet is the Deployment's name plus the pod
// template hash every one of its pods carries).
func blockerOwner(p *corev1.Pod, ctrl *metav1.OwnerReference) string {
	if ctrl == nil {
		return p.Namespace + "/" + p.Name
	}
	name := ctrl.Name
	if h := p.Labels["pod-template-hash"]; ctrl.Kind == "ReplicaSet" && h != "" {
		name = strings.TrimSuffix(name, "-"+h)
	}
	return p.Namespace + "/" + name
}

func hasPVC(p *corev1.Pod) bool {
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			return true
		}
	}
	return false
}

// runsClusterAutoscaler: a cluster-autoscaler Deployment with replicas.
func runsClusterAutoscaler(ds []*appsv1.Deployment) bool {
	for _, d := range ds {
		name := d.Name
		if d.Labels["app.kubernetes.io/name"] != "" {
			name = d.Labels["app.kubernetes.io/name"]
		}
		if strings.Contains(name, "cluster-autoscaler") && (d.Spec.Replicas == nil || *d.Spec.Replicas > 0) {
			return true
		}
	}
	return false
}

func firstLabel(l map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := l[k]; v != "" {
			return v
		}
	}
	return ""
}
