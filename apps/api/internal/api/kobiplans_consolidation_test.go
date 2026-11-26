package api

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// What the live right-sizing plans leave each pod requesting: only pending
// steps, only live plans, only requests; and which plans they are.
func TestPlannedRequests(t *testing.T) {
	now := time.Now().UTC()
	step := func(name, status string, params string) kobiplan.Step {
		return kobiplan.Step{
			Action: kobiplan.ActionSetResources, Status: kobiplan.StepStatus(status),
			Target: kobiplan.Target{Type: "deployments", Namespace: "shop", Name: name},
			Params: json.RawMessage(params),
		}
	}
	plan := func(id string, status kobiplan.Status, kind kobiplan.Kind, steps ...kobiplan.Step) kobiplan.Plan {
		return kobiplan.Plan{ID: id, Kind: kind, Status: status, ExpiresAt: now.Add(time.Hour), Steps: steps}
	}
	live := []kobiplan.Plan{
		plan("pln_a", kobiplan.StatusExported, kobiplan.KindRightsizing,
			step("api", "pending", `{"containers":[{"container":"app","requests":{"cpu":"250m","memory":"512Mi"},"limits":{"memory":"1Gi"}}]}`),
			step("web", "applied", `{"containers":[{"container":"app","requests":{"cpu":"100m"}}]}`),
			step("job", "pending", `{"containers":[{"container":"app","limits":{"memory":"2Gi"}}]}`)),
		plan("pln_gone", kobiplan.StatusExpired, kobiplan.KindRightsizing, step("cart", "pending", `{"containers":[{"container":"app","requests":{"cpu":"50m"}}]}`)),
		plan("pln_pools", kobiplan.StatusDraft, kobiplan.KindConsolidation),
	}
	podsOf := func(t kobiplan.Target) []*corev1.Pod {
		return []*corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Namespace: t.Namespace, Name: t.Name + "-7d9f-a1"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: t.Namespace, Name: t.Name + "-7d9f-b2"}},
		}
	}
	planned, from := plannedRequests(podsOf, live, now)
	if !slices.Equal(from, []string{"pln_a"}) {
		t.Fatalf("plans = %v", from)
	}
	if len(planned) != 2 {
		t.Fatalf("planned = %v", planned)
	}
	for _, pod := range []string{"shop/api-7d9f-a1", "shop/api-7d9f-b2"} {
		if c := planned[pod]["app"]; c.CPUMilli != 250 || c.MemBytes != 512<<20 {
			t.Fatalf("%s = %+v", pod, c)
		}
	}
}

// The pool as the sweep reads it: its nodes by label (a cordoned one counts,
// a control-plane one does not), and after a week the pods that wait for
// room, were evicted for pressure on its nodes, or were OOMKilled there.
func TestPoolStepFacts(t *testing.T) {
	node := func(name, pool string, extra map[string]string) *corev1.Node {
		l := map[string]string{"eks.amazonaws.com/nodegroup": pool, "node.kubernetes.io/instance-type": "m6i.xlarge"}
		for k, v := range extra {
			l[k] = v
		}
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}}
	}
	cordoned := node("n3", "general", nil)
	cordoned.Spec.Unschedulable = true
	nodes := []*corev1.Node{node("n1", "general", nil), node("n2", "general", nil), cordoned, node("b1", "batch", nil),
		node("cp", "general", map[string]string{"node-role.kubernetes.io/control-plane": ""})}

	params, _ := json.Marshal(kobiplan.RemoveNodesParams{Pool: "general", PoolSource: kobiplan.PoolEKS, NodesBefore: 4, NodesAfter: 3,
		Nodes: []kobiplan.RemovedNode{{Name: "n4"}}})
	applied := time.Now().Add(-8 * 24 * time.Hour)
	step := kobiplan.Step{Action: kobiplan.ActionRemoveNodes, Status: kobiplan.StepApplied, AppliedAt: &applied, Params: params}

	after := metav1.NewTime(applied.Add(time.Hour))
	before := metav1.NewTime(applied.Add(-time.Hour))
	pending := func(name string, at metav1.Time, msg string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: msg, LastTransitionTime: at}}}}
	}
	evicted := func(name, node string, at metav1.Time) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, LastTransitionTime: at}}}}
	}
	oom := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "oom"}, Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{
		ContainerStatuses: []corev1.ContainerStatus{{LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: after}}}}}}
	pods := []*corev1.Pod{
		pending("p1", after, "0/3 nodes are available: 3 Insufficient cpu."),
		pending("p2", before, "0/3 nodes are available: 3 Insufficient memory."), // waiting since before
		// Whatever keeps it off every node counts: the removed node may have
		// been the one its rules allowed (26-nov: anti-affinity passed).
		pending("p3", after, "0/3 nodes are available: node(s) didn't match Pod's node affinity."),
		// The scheduler's own words on kubebolt-dev (26-nov), one replica per
		// node on five nodes.
		pending("p4", after, "0/7 nodes are available: 1 node(s) didn't match Pod's node affinity/selector, 1 node(s) had untolerated taint(s), 5 node(s) didn't match pod anti-affinity rules. no new claims to deallocate, preemption: 0/7 nodes are available: 2 Preemption is not helpful for scheduling, 5 No preemption victims found for incoming pod."),
		// A claim not bound yet is not the pool's doing.
		pending("p5", after, "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims."),
		evicted("e1", "n2", after), evicted("e2", "b1", after), evicted("e3", "n1", before),
		oom,
	}

	f := poolStepFacts(nodes, pods, step, time.Now())
	if f.Nodes != 3 || f.Hash != kobiplan.PoolHash([]kobiplan.NodeFacts{{Name: "n1", InstanceType: "m6i.xlarge"}, {Name: "n2", InstanceType: "m6i.xlarge"}, {Name: "n3", InstanceType: "m6i.xlarge"}}) {
		t.Fatalf("pool = %+v", f)
	}
	if !f.Checked || f.PendingPods != 3 || f.Evicted != 1 || !f.OOMKilled {
		t.Fatalf("checks = %+v", f)
	}

	recent := time.Now().Add(-time.Hour)
	step.AppliedAt = &recent
	if f := poolStepFacts(nodes, pods, step, time.Now()); f.Checked {
		t.Fatal("checked before the week was out")
	}
}
