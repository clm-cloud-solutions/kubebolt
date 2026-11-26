package kobiplan

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func factsNode(name, zone string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"eks.amazonaws.com/nodegroup": "general", labelZone: zone, labelInstanceType: "m6i.xlarge",
		}},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("3920m"), corev1.ResourceMemory: resource.MustParse("15Gi"),
		}},
	}
}

func factsPod(name, node string, ctrl *metav1.OwnerReference, containers ...corev1.Container) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, Labels: map[string]string{"app": "api", "pod-template-hash": "7d9f"}},
		Spec:       corev1.PodSpec{NodeName: node, Containers: containers},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if ctrl != nil {
		ctrl.Controller = ptrTo(true)
		p.OwnerReferences = []metav1.OwnerReference{*ctrl}
	}
	return p
}

func ctr(name, cpu, mem string) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
	}}}
}

func rs() *metav1.OwnerReference { return &metav1.OwnerReference{Kind: "ReplicaSet", Name: "api-7d9f"} }

func ptrTo[T any](v T) *T { return &v }

func podFact(t *testing.T, pods []PodFacts, name string) PodFacts {
	t.Helper()
	for _, p := range pods {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no facts for %s in %+v", name, pods)
	return PodFacts{}
}

// A node reads its pool, zone, shape, allocatable and list price.
func TestConsolidationFacts_Nodes(t *testing.T) {
	cp := factsNode("cp-1", "us-east-1a")
	cp.Labels["node-role.kubernetes.io/control-plane"] = ""
	cordoned := factsNode("node-2", "us-east-1b")
	cordoned.Spec.Unschedulable = true
	nodes, _, _ := ConsolidationFacts(ConsolidationObjects{Nodes: []*corev1.Node{factsNode("node-1", "us-east-1a"), cordoned, cp}}, nil, nil, map[string]float64{"node-1": 129.40})
	n := nodes[0]
	if n.Pool != "general" || n.PoolSource != PoolEKS || n.Zone != "us-east-1a" || n.InstanceType != "m6i.xlarge" || n.CPUMilli != 3920 || n.MemBytes != 15*gib {
		t.Fatalf("node = %+v", n)
	}
	if n.Monthly == nil || *n.Monthly != 129.40 || nodes[1].Monthly != nil {
		t.Fatal("a node's price is its own, and a node without one has none")
	}
	if !nodes[1].Unschedulable || !nodes[2].ControlPlane {
		t.Fatalf("cordoned / control plane: %+v %+v", nodes[1], nodes[2])
	}
}

// A pod needs the larger of what it asks for (the plan's figure when a live
// right-sizing plan changes a container) and what it used.
func TestConsolidationFacts_PodNeed(t *testing.T) {
	init := factsPod("migrate", "node-1", rs(), ctr("app", "100m", "128Mi"))
	init.Spec.InitContainers = []corev1.Container{ctr("schema", "500m", "64Mi")}
	o := ConsolidationObjects{Nodes: []*corev1.Node{factsNode("node-1", "us-east-1a")}, Pods: []*corev1.Pod{
		factsPod("api-1", "node-1", rs(), ctr("app", "1", "2Gi"), ctr("proxy", "100m", "128Mi")),
		factsPod("api-2", "node-1", rs(), ctr("app", "1", "2Gi"), ctr("proxy", "100m", "128Mi")),
		init,
	}}
	usage := map[string]PodNeed{"shop/api-2": {CPUMilli: 1600, MemBytes: gib}}
	planned := map[string]PlannedContainers{"shop/api-1": {"app": {CPUMilli: 400, MemBytes: gib}}}
	_, pods, _ := ConsolidationFacts(o, usage, planned, nil)

	if p := podFact(t, pods, "api-1"); p.CPUMilli != 500 || p.MemBytes != gib+128<<20 {
		t.Fatalf("planned: %d m, %d B", p.CPUMilli, p.MemBytes)
	}
	if p := podFact(t, pods, "api-2"); p.CPUMilli != 1600 || p.MemBytes != 2*gib+128<<20 {
		t.Fatalf("usage over requests: %d m, %d B", p.CPUMilli, p.MemBytes)
	}
	if p := podFact(t, pods, "migrate"); p.CPUMilli != 500 || p.MemBytes != 128<<20 {
		t.Fatalf("init container: %d m, %d B", p.CPUMilli, p.MemBytes)
	}
}

// Pods that left, finished or wait for a node take no room anywhere.
func TestConsolidationFacts_SkipsPodsThatTakeNoRoom(t *testing.T) {
	done := factsPod("job-1", "node-1", nil)
	done.Status.Phase = corev1.PodSucceeded
	going := factsPod("api-9", "node-1", rs())
	going.DeletionTimestamp = &metav1.Time{}
	_, pods, _ := ConsolidationFacts(ConsolidationObjects{Pods: []*corev1.Pod{done, going, factsPod("pending", "", rs())}}, nil, nil, nil)
	if len(pods) != 0 {
		t.Fatalf("pods = %+v", pods)
	}
}

// The Cluster Autoscaler's reasons a pod keeps its node, named after the
// workload a person knows.
func TestConsolidationFacts_Blockers(t *testing.T) {
	hostPath := factsPod("collector", "node-1", rs())
	hostPath.Spec.Volumes = []corev1.Volume{{Name: "logs", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/log"}}}}
	scratch := factsPod("cache", "node-1", rs())
	scratch.Spec.Volumes = []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	pinned := factsPod("batch", "node-1", rs())
	pinned.Annotations = map[string]string{annotationSafeToEvict: "false"}
	safe := factsPod("debug", "node-1", nil)
	safe.Annotations = map[string]string{annotationSafeToEvict: "true"}
	ds := factsPod("agent", "node-1", &metav1.OwnerReference{Kind: "DaemonSet", Name: "agent"})
	ds.Spec.Volumes = hostPath.Spec.Volumes
	mirror := factsPod("kube-proxy", "node-1", nil)
	mirror.Annotations = map[string]string{annotationMirror: "abc"}

	o := ConsolidationObjects{Pods: []*corev1.Pod{factsPod("bare", "node-1", nil), hostPath, scratch, pinned, safe, ds, mirror}}
	_, pods, _ := ConsolidationFacts(o, nil, nil, nil)

	for _, c := range []struct{ pod, blocker, owner string }{
		{"bare", BlockNoController, "shop/bare"},
		{"collector", BlockLocalStorage, "shop/api"},
		{"cache", "", ""},
		{"batch", BlockNotEvictable, "shop/api"},
		{"debug", "", ""},
		{"agent", "", ""},
		{"kube-proxy", "", ""},
	} {
		if p := podFact(t, pods, c.pod); p.Blocker != c.blocker || p.Owner != c.owner {
			t.Errorf("%s: blocker %q owner %q, want %q %q", c.pod, p.Blocker, p.Owner, c.blocker, c.owner)
		}
	}
	if !podFact(t, pods, "agent").DaemonSet || !podFact(t, pods, "kube-proxy").DaemonSet {
		t.Error("DaemonSet and static pods stay with their node")
	}
}

// A PDB that allows no disruption now blocks even a pod marked safe to evict;
// one that allows a disruption, or selects other pods, does not.
func TestConsolidationFacts_PDB(t *testing.T) {
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	pdb := func(name string, allowed int32, s *metav1.LabelSelector) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name},
			Spec:       policyv1.PodDisruptionBudgetSpec{Selector: s},
			Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: allowed},
		}
	}
	safe := factsPod("api-1", "node-1", rs())
	safe.Annotations = map[string]string{annotationSafeToEvict: "true"}

	_, pods, _ := ConsolidationFacts(ConsolidationObjects{Pods: []*corev1.Pod{safe}, PDBs: []*policyv1.PodDisruptionBudget{
		pdb("roomy", 1, sel), pdb("other", 0, &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}), pdb("none", 0, nil), pdb("api", 0, sel),
	}}, nil, nil, nil)
	if p := pods[0]; p.Blocker != BlockPDB || p.Owner != "shop/api" {
		t.Fatalf("pod = %+v", p)
	}
	// policy/v1: an empty selector selects every pod of the namespace.
	_, pods, _ = ConsolidationFacts(ConsolidationObjects{Pods: []*corev1.Pod{safe}, PDBs: []*policyv1.PodDisruptionBudget{pdb("all", 0, &metav1.LabelSelector{})}}, nil, nil, nil)
	if p := pods[0]; p.Blocker != BlockPDB || p.Owner != "shop/all" {
		t.Fatalf("empty selector: %+v", p)
	}
}

// A pod with a claim stays in its node's zone; one without a claim goes anywhere.
func TestConsolidationFacts_VolumeZone(t *testing.T) {
	db := factsPod("db-0", "node-2", &metav1.OwnerReference{Kind: "StatefulSet", Name: "db"})
	db.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-db-0"}}}}
	o := ConsolidationObjects{Nodes: []*corev1.Node{factsNode("node-1", "us-east-1a"), factsNode("node-2", "us-east-1b")}, Pods: []*corev1.Pod{db, factsPod("api-1", "node-2", rs())}}
	_, pods, _ := ConsolidationFacts(o, nil, nil, nil)
	if podFact(t, pods, "db-0").VolumeZone != "us-east-1b" || podFact(t, pods, "api-1").VolumeZone != "" {
		t.Fatalf("pods = %+v", pods)
	}
}

// The Cluster Autoscaler shows as its status ConfigMap or its Deployment; one
// scaled to zero is not running.
func TestConsolidationFacts_Autoscaler(t *testing.T) {
	dep := func(name string, replicas int32, label string) *appsv1.Deployment {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
		if label != "" {
			d.Labels["app.kubernetes.io/name"] = label
		}
		return d
	}
	for _, c := range []struct {
		o    ConsolidationObjects
		want bool
	}{
		{ConsolidationObjects{CAStatus: true}, true},
		{ConsolidationObjects{Deployments: []*appsv1.Deployment{dep("cluster-autoscaler-aws", 1, "")}}, true},
		{ConsolidationObjects{Deployments: []*appsv1.Deployment{dep("ca", 1, "aws-cluster-autoscaler")}}, true},
		{ConsolidationObjects{Deployments: []*appsv1.Deployment{dep("cluster-autoscaler", 0, "")}}, false},
		{ConsolidationObjects{Deployments: []*appsv1.Deployment{dep("api", 3, "")}}, false},
	} {
		if _, _, a := ConsolidationFacts(c.o, nil, nil, nil); a.ClusterAutoscaler != c.want {
			t.Errorf("%+v → %v", c.o, a.ClusterAutoscaler)
		}
	}
}

// A pod's scheduling rules: the engine follows one replica per node, node
// affinity and host ports; pod affinity, a hard spread and anti-affinity on
// a zone keep the node.
func TestConsolidationFacts_PlacementRules(t *testing.T) {
	anti := func(name string, key string, sel *metav1.LabelSelector) *corev1.Pod {
		p := factsPod(name, "node-1", rs())
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
			{TopologyKey: key, LabelSelector: sel},
		}}}
		return p
	}
	app := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	perNode := anti("per-node", HostnameTopology, app)
	perZone := anti("per-zone", labelZone, app)
	noSelector := anti("no-selector", HostnameTopology, nil)
	keyed := anti("keyed", HostnameTopology, app)
	keyed.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].MatchLabelKeys = []string{"pod-template-hash"}
	everywhere := anti("everywhere", HostnameTopology, app)
	everywhere.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].NamespaceSelector = &metav1.LabelSelector{}
	byNsLabels := anti("by-ns-labels", HostnameTopology, app)
	byNsLabels.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}

	together := factsPod("together", "node-1", rs())
	together.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
		{TopologyKey: HostnameTopology, LabelSelector: app},
	}}}
	hard := factsPod("hard-spread", "node-1", rs())
	hard.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: labelZone, WhenUnsatisfiable: corev1.DoNotSchedule}}
	soft := factsPod("soft-spread", "node-1", rs())
	soft.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: labelZone, WhenUnsatisfiable: corev1.ScheduleAnyway}}
	ported := factsPod("ingress", "node-1", rs(), corev1.Container{Name: "nginx", Ports: []corev1.ContainerPort{{ContainerPort: 443, HostPort: 443}, {ContainerPort: 8080}}})
	zoned := factsPod("zoned", "node-1", rs())
	zoned.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: labelZone, Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1a"}}}}},
	}}}
	pdbFirst := anti("pdb-first", labelZone, app)
	pdbFirst.Labels = map[string]string{"app": "guarded"}

	o := ConsolidationObjects{
		Pods: []*corev1.Pod{perNode, perZone, noSelector, keyed, everywhere, byNsLabels, together, hard, soft, ported, zoned, pdbFirst},
		PDBs: []*policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "guarded"},
			Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guarded"}}}}},
	}
	_, pods, _ := ConsolidationFacts(o, nil, nil, nil)

	for _, c := range []struct{ pod, blocker string }{
		{"per-node", ""}, {"no-selector", ""}, {"keyed", ""}, {"everywhere", ""}, {"soft-spread", ""}, {"ingress", ""}, {"zoned", ""},
		{"per-zone", BlockUnsimulated}, {"by-ns-labels", BlockUnsimulated}, {"together", BlockUnsimulated}, {"hard-spread", BlockUnsimulated},
		// A PDB says it first.
		{"pdb-first", BlockPDB},
	} {
		if p := podFact(t, pods, c.pod); p.Blocker != c.blocker {
			t.Errorf("%s: blocker %q, want %q", c.pod, p.Blocker, c.blocker)
		}
	}
	if p := podFact(t, pods, "per-zone"); p.Owner != "shop/api" || p.Workload != "shop/api" {
		t.Errorf("per-zone names its workload: %+v", p)
	}

	replica := PodFacts{Namespace: "shop", Labels: map[string]string{"app": "api", "pod-template-hash": "7d9f"}}
	if a := podFact(t, pods, "per-node").AntiAffinity; len(a) != 1 || !a[0].selects(replica) || a[0].selects(PodFacts{Namespace: "billing", Labels: replica.Labels}) {
		t.Errorf("per-node term = %+v", a)
	}
	if a := podFact(t, pods, "no-selector").AntiAffinity; len(a) != 0 {
		t.Errorf("a nil selector selects no pod: %+v", a)
	}
	if a := podFact(t, pods, "keyed").AntiAffinity; len(a) != 1 || a[0].selects(PodFacts{Namespace: "shop", Labels: map[string]string{"app": "api", "pod-template-hash": "other"}}) || !a[0].selects(replica) {
		t.Errorf("matchLabelKeys narrows to the pod's own revision: %+v", a)
	}
	if a := podFact(t, pods, "everywhere").AntiAffinity; len(a) != 1 || !a[0].selects(PodFacts{Namespace: "billing", Labels: replica.Labels}) {
		t.Errorf("an empty namespace selector looks everywhere: %+v", a)
	}
	if hp := podFact(t, pods, "ingress").HostPorts; !slices.Equal(hp, []string{"TCP/443"}) {
		t.Errorf("host ports = %v", hp)
	}
	if podFact(t, pods, "zoned").NodeAffinity == nil {
		t.Error("required node affinity is read")
	}
}
