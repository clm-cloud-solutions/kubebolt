package cluster

import (
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func replicaSet(ns, name string, refs ...metav1.OwnerReference) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, OwnerReferences: refs}}
}

func podNames(pods []*corev1.Pod) []string {
	out := make([]string, 0, len(pods))
	for _, p := range pods {
		out = append(out, p.Namespace+"/"+p.Name)
	}
	sort.Strings(out)
	return out
}

func itemNames(items []map[string]interface{}) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		out = append(out, m["namespace"].(string)+"/"+m["name"].(string))
	}
	sort.Strings(out)
	return out
}

func equalNames(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

// A workload's pods are its own namespace's only, even when another namespace
// has a workload, ReplicaSet or pod with the same name.
func TestWorkloadPods_OnlyTheWorkloadsNamespaceAndOwner(t *testing.T) {
	c := connectorWith(t,
		[]*corev1.Pod{
			pod("shop", "web-7d9-a", ownerRef("ReplicaSet", "web-7d9")),
			pod("shop", "web-6c1-b", ownerRef("ReplicaSet", "web-6c1")), // older revision, still running
			pod("shop", "api-5f2-c", ownerRef("ReplicaSet", "api-5f2")),
			pod("staging", "web-7d9-x", ownerRef("ReplicaSet", "web-7d9")), // same RS name, other namespace
			pod("shop", "agent-n1", ownerRef("DaemonSet", "agent")),
			pod("monitoring", "agent-n1", ownerRef("DaemonSet", "agent")),
			pod("shop", "db-0", ownerRef("StatefulSet", "db")),
			pod("shop", "migrate-q", ownerRef("Job", "migrate")),
		},
		[]*appsv1.ReplicaSet{
			replicaSet("shop", "web-7d9", ownerRef("Deployment", "web")),
			replicaSet("shop", "web-6c1", ownerRef("Deployment", "web")),
			replicaSet("shop", "api-5f2", ownerRef("Deployment", "api")),
			replicaSet("staging", "web-7d9", ownerRef("Deployment", "web")),
		},
	)

	equalNames(t, "Deployment shop/web", podNames(c.deploymentPodObjects("shop", "web")), []string{"shop/web-6c1-b", "shop/web-7d9-a"})
	equalNames(t, "DaemonSet shop/agent", podNames(c.ownedPodObjects("shop", "DaemonSet", "agent")), []string{"shop/agent-n1"})
	equalNames(t, "StatefulSet shop/db", podNames(c.ownedPodObjects("shop", "StatefulSet", "db")), []string{"shop/db-0"})
	equalNames(t, "Job shop/migrate", podNames(c.ownedPodObjects("shop", "Job", "migrate")), []string{"shop/migrate-q"})

	// The public functions return the same pods, as rows.
	equalNames(t, "GetDeploymentPods", itemNames(c.GetDeploymentPods("shop", "web")), []string{"shop/web-6c1-b", "shop/web-7d9-a"})
	equalNames(t, "GetDaemonSetPods", itemNames(c.GetDaemonSetPods("monitoring", "agent")), []string{"monitoring/agent-n1"})
	equalNames(t, "GetStatefulSetPods", itemNames(c.GetStatefulSetPods("shop", "db")), []string{"shop/db-0"})
	equalNames(t, "GetJobPods", itemNames(c.GetJobPods("shop", "migrate")), []string{"shop/migrate-q"})

	if got := c.GetDeploymentPods("shop", "missing"); got != nil {
		t.Errorf("a Deployment without ReplicaSets returned %v, want nil", got)
	}
}

// livePodCount counts the same pods the Pods tab lists, without building the
// rows.
func TestWorkloadPods_CountMatchesTheList(t *testing.T) {
	c := connectorWith(t,
		[]*corev1.Pod{
			pod("shop", "web-7d9-a", ownerRef("ReplicaSet", "web-7d9")),
			pod("shop", "web-7d9-b", ownerRef("ReplicaSet", "web-7d9")),
			pod("shop", "agent-n1", ownerRef("DaemonSet", "agent")),
		},
		[]*appsv1.ReplicaSet{replicaSet("shop", "web-7d9", ownerRef("Deployment", "web"))},
	)
	if got, want := len(c.deploymentPodObjects("shop", "web")), len(c.GetDeploymentPods("shop", "web")); got != want || got != 2 {
		t.Errorf("Deployment count %d, list %d, want 2", got, want)
	}
	if got, want := len(c.ownedPodObjects("shop", "DaemonSet", "agent")), len(c.GetDaemonSetPods("shop", "agent")); got != want || got != 1 {
		t.Errorf("DaemonSet count %d, list %d, want 1", got, want)
	}
}

// Rows keep the requests and limits the Pods tab shows.
func TestWorkloadPods_RowsCarryRequestsAndLimits(t *testing.T) {
	p := pod("shop", "db-0", ownerRef("StatefulSet", "db"))
	p.Spec.Containers = []corev1.Container{
		{Name: "db", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")},
		}},
		{Name: "exporter", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
		}},
	}
	c := connectorWith(t, []*corev1.Pod{p}, nil)
	rows := c.GetStatefulSetPods("shop", "db")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	for k, want := range map[string]int64{"cpuRequest": 300, "cpuLimit": 1000, "memoryRequest": 256 << 20, "memoryLimit": 512 << 20} {
		if got := rows[0][k]; got != want {
			t.Errorf("%s = %v, want %d", k, got, want)
		}
	}
}

// A connector without the pod or ReplicaSet informer (RBAC denied it) returns
// no pods instead of panicking.
func TestWorkloadPods_NoListers(t *testing.T) {
	c := &Connector{}
	if c.deploymentPodObjects("shop", "web") != nil || c.ownedPodObjects("shop", "DaemonSet", "agent") != nil {
		t.Error("expected nil without listers")
	}
	if c.GetDeploymentPods("shop", "web") != nil || c.GetDaemonSetPods("shop", "agent") != nil {
		t.Error("expected nil rows without listers")
	}
}
