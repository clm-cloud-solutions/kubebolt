package cluster

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
)

// ownerRef builds the controller reference shape the apiserver writes.
func ownerRef(kind, name string) metav1.OwnerReference {
	t := true
	return metav1.OwnerReference{Kind: kind, Name: name, Controller: &t}
}

// connectorWith wires real client-go listers over hand-built indexers, so these
// exercise the same lookup path production uses rather than a stand-in.
func connectorWith(t *testing.T, pods []*corev1.Pod, rs []*appsv1.ReplicaSet) *Connector {
	t.Helper()
	podIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, p := range pods {
		if err := podIdx.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	rsIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, r := range rs {
		if err := rsIdx.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	return &Connector{
		podLister:        corelisters.NewPodLister(podIdx),
		replicaSetLister: appslisters.NewReplicaSetLister(rsIdx),
	}
}

func pod(ns, name string, refs ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: name, OwnerReferences: refs,
	}}
}

// A pod owned by a ReplicaSet must land on the DEPLOYMENT, not stop halfway:
// the ReplicaSet name carries a pod-template-hash that moves on every rollout,
// so stopping there would only relocate the churn.
func TestPodWorkloadOwner_ChainsThroughReplicaSetToDeployment(t *testing.T) {
	c := connectorWith(t,
		[]*corev1.Pod{pod("prod", "web-7d4f8b-x9", ownerRef("ReplicaSet", "web-7d4f8b"))},
		[]*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "web-7d4f8b",
			OwnerReferences: []metav1.OwnerReference{ownerRef("Deployment", "web")},
		}}})

	kind, name, _ := c.WorkloadOwner("prod", "Pod", "web-7d4f8b-x9")
	if kind != "Deployment" || name != "web" {
		t.Errorf("got %s/%s, want Deployment/web", kind, name)
	}
}

// A DaemonSet is already the unit — collapse one level and stop.
func TestPodWorkloadOwner_CollapsesToDaemonSet(t *testing.T) {
	c := connectorWith(t,
		[]*corev1.Pod{pod("monitoring", "node-exporter-pwqzh", ownerRef("DaemonSet", "kube-prom-node-exporter"))},
		nil)

	kind, name, _ := c.WorkloadOwner("monitoring", "Pod", "node-exporter-pwqzh")
	if kind != "DaemonSet" || name != "kube-prom-node-exporter" {
		t.Errorf("got %s/%s, want DaemonSet/kube-prom-node-exporter", kind, name)
	}
}

// STATIC PODS name a NODE as owner. Collapsing on any owner would fuse
// kube-apiserver, etcd, kube-scheduler and kube-controller-manager into one row
// for the node they share, losing which component actually violates — and a Node
// is not something you edit a manifest for.
func TestPodWorkloadOwner_StaticPodsStayPods(t *testing.T) {
	for _, name := range []string{"kube-apiserver-cp", "etcd-cp", "kube-scheduler-cp"} {
		c := connectorWith(t,
			[]*corev1.Pod{pod("kube-system", name, ownerRef("Node", "kubebolt-dev-control-plane"))}, nil)
		kind, got, _ := c.WorkloadOwner("kube-system", "Pod", name)
		if kind != "Pod" || got != name {
			t.Errorf("%s collapsed to %s/%s; a Node owner is not a workload", name, kind, got)
		}
	}
}

// A bare pod is the unit. Nothing to collapse onto, and inventing one would
// point the operator at a resource that does not exist.
func TestPodWorkloadOwner_OwnerlessPodStaysPod(t *testing.T) {
	c := connectorWith(t, []*corev1.Pod{pod("default", "node-debugger-7mk2t")}, nil)
	kind, name, _ := c.WorkloadOwner("default", "Pod", "node-debugger-7mk2t")
	if kind != "Pod" || name != "node-debugger-7mk2t" {
		t.Errorf("got %s/%s, want the pod unchanged", kind, name)
	}
}

// Unknown pod (informer gap, pod already gone): return the input rather than
// guess. Same stance as the ReplicaSet path.
func TestPodWorkloadOwner_UnknownPodIsLeftAlone(t *testing.T) {
	c := connectorWith(t, nil, nil)
	kind, name, _ := c.WorkloadOwner("prod", "Pod", "vanished")
	if kind != "Pod" || name != "vanished" {
		t.Errorf("got %s/%s, want the input unchanged", kind, name)
	}
}

// A Job's pods collapse to the Job and STOP there. A CronJob's runs are things
// an operator compares against each other; folding a Job into its CronJob would
// erase which run failed.
func TestPodWorkloadOwner_StopsAtTheJob(t *testing.T) {
	c := connectorWith(t,
		[]*corev1.Pod{pod("batch", "nightly-29384-abc", ownerRef("Job", "nightly-29384"))}, nil)
	kind, name, _ := c.WorkloadOwner("batch", "Pod", "nightly-29384-abc")
	if kind != "Job" || name != "nightly-29384" {
		t.Errorf("got %s/%s, want Job/nightly-29384", kind, name)
	}
}

// The pre-existing ReplicaSet behaviour must be untouched by the Pod branch.
func TestWorkloadOwner_ReplicaSetPathUnchanged(t *testing.T) {
	c := connectorWith(t, nil, []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{
		Namespace: "prod", Name: "api-5f6",
		OwnerReferences: []metav1.OwnerReference{ownerRef("Deployment", "api")},
	}}})
	if kind, name, _ := c.WorkloadOwner("prod", "ReplicaSet", "api-5f6"); kind != "Deployment" || name != "api" {
		t.Errorf("got %s/%s, want Deployment/api", kind, name)
	}
	// A kind that is neither Pod nor ReplicaSet passes straight through.
	if kind, name, _ := c.WorkloadOwner("prod", "StatefulSet", "db"); kind != "StatefulSet" || name != "db" {
		t.Errorf("got %s/%s, want StatefulSet/db", kind, name)
	}
}

// The settled flag is what stops the findings sweep from minting a second
// identity for a finding it already has. It must be false ONLY when a later
// pass would answer differently — i.e. the cache could not be consulted — and
// true for every legitimately-degraded shape, because those never improve and
// waiting on them would block ingestion forever.
func TestWorkloadOwner_SettledSeparatesTransientFromFinal(t *testing.T) {
	bare := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "orphan-77c"}}
	owned := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: "prod", Name: "api-5f6",
		OwnerReferences: []metav1.OwnerReference{ownerRef("Deployment", "api")},
	}}
	c := connectorWith(t, []*corev1.Pod{pod("prod", "api-5f6-x1", ownerRef("ReplicaSet", "api-5f6"))},
		[]*appsv1.ReplicaSet{bare, owned})

	cases := []struct {
		name        string
		kind, input string
		wantSettled bool
	}{
		{"collapsed to its Deployment", "ReplicaSet", "api-5f6", true},
		{"bare ReplicaSet is a final answer", "ReplicaSet", "orphan-77c", true},
		{"nothing to collapse", "StatefulSet", "db", true},
		{"pod chains through to the Deployment", "Pod", "api-5f6-x1", true},
		// The one that matters: the cache has ReplicaSets, just not this one.
		// That is the agent-proxy incomplete-cache case, and it self-heals.
		{"ReplicaSet absent from a populated cache", "ReplicaSet", "web-9d2", false},
		{"pod absent from a populated cache", "Pod", "vanished", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, settled := c.WorkloadOwner("prod", tc.kind, tc.input); settled != tc.wantSettled {
				t.Errorf("settled = %v, want %v", settled, tc.wantSettled)
			}
		})
	}
}

// A permission-gated ServiceAccount has no ReplicaSet informer at all. That is
// permanent, so the answer is settled and degraded — blocking here would mean a
// namespace-scoped install never records a single finding.
func TestWorkloadOwner_NoInformerIsSettled(t *testing.T) {
	c := &Connector{}
	kind, name, settled := c.WorkloadOwner("prod", "ReplicaSet", "api-5f6")
	if kind != "ReplicaSet" || name != "api-5f6" || !settled {
		t.Errorf("got %s/%s settled=%v, want the input unchanged and settled", kind, name, settled)
	}
}
