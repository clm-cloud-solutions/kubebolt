package api

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// Cordon/uncordon tests focus on:
//   1. Patch SHAPE — buildSchedulabilityPatch must produce the exact
//      `{"spec":{"unschedulable":<bool>}}` body. A drifted shape
//      (eg. nesting one level deeper) wouldn't fail validation but
//      would silently be a no-op against a real apiserver.
//   2. END-TO-END against fake clientset — the patch should flip
//      Node.spec.unschedulable from false → true (cordon) and back
//      (uncordon), without touching any other field.
//   3. Response SHAPE — the {status, alreadyX, node} envelope is
//      what the frontend setQueryData expects; renaming a key would
//      cause the UI to silently fall back to refetch instead of
//      optimistic update.

func TestBuildSchedulabilityPatchCordon(t *testing.T) {
	got, err := buildSchedulabilityPatch(true)
	if err != nil {
		t.Fatalf("buildSchedulabilityPatch(true): %v", err)
	}
	want := `{"spec":{"unschedulable":true}}`
	if string(got) != want {
		t.Errorf("cordon patch = %s, want %s", got, want)
	}
}

func TestBuildSchedulabilityPatchUncordon(t *testing.T) {
	got, err := buildSchedulabilityPatch(false)
	if err != nil {
		t.Fatalf("buildSchedulabilityPatch(false): %v", err)
	}
	want := `{"spec":{"unschedulable":false}}`
	if string(got) != want {
		t.Errorf("uncordon patch = %s, want %s", got, want)
	}
}

// TestCordonPatchInvariance is the load-bearing test: applying the
// patch against a real (well, fake) clientset must flip
// spec.unschedulable AND preserve every other Node field — taints,
// labels, capacity, conditions, the works. If the patch ever drifts
// to a Replace-like merge strategy, this test fails because Taints
// gets nuked.
func TestCordonPatchInvariance(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ip-10-0-3-22",
			Labels: map[string]string{"role": "worker", "topology.kubernetes.io/zone": "us-west-2a"},
		},
		Spec: corev1.NodeSpec{
			PodCIDR: "10.244.0.0/24",
			Taints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
			Unschedulable: false,
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
			},
		},
	}
	cs := fake.NewSimpleClientset(node)
	ctx := context.Background()

	patch, err := buildSchedulabilityPatch(true)
	if err != nil {
		t.Fatalf("buildSchedulabilityPatch: %v", err)
	}
	if _, err := cs.CoreV1().Nodes().Patch(
		ctx, "ip-10-0-3-22", types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	got, err := cs.CoreV1().Nodes().Get(ctx, "ip-10-0-3-22", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !got.Spec.Unschedulable {
		t.Error("spec.unschedulable = false after cordon patch")
	}
	// Taints, labels, PodCIDR, capacity must survive. Merge patch
	// only touches the keys it names; this is the property the whole
	// feature rests on.
	if len(got.Spec.Taints) != 1 || got.Spec.Taints[0].Key != "dedicated" {
		t.Errorf("Taints clobbered by cordon patch: %v", got.Spec.Taints)
	}
	if got.Spec.PodCIDR != "10.244.0.0/24" {
		t.Errorf("PodCIDR clobbered: %q", got.Spec.PodCIDR)
	}
	if got.Labels["role"] != "worker" {
		t.Errorf("Labels clobbered: %v", got.Labels)
	}
	if cpu := got.Status.Capacity.Cpu(); cpu == nil || cpu.String() != "8" {
		t.Errorf("Capacity clobbered: %v", got.Status.Capacity)
	}
}

func TestUncordonPatchRoundTrip(t *testing.T) {
	// Start cordoned; uncordon flips it back.
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	cs := fake.NewSimpleClientset(node)
	ctx := context.Background()

	patch, _ := buildSchedulabilityPatch(false)
	if _, err := cs.CoreV1().Nodes().Patch(
		ctx, "n1", types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got, _ := cs.CoreV1().Nodes().Get(ctx, "n1", metav1.GetOptions{})
	if got.Spec.Unschedulable {
		t.Error("spec.unschedulable = true after uncordon patch")
	}
}

func TestBuildSchedulabilityResponseCordon(t *testing.T) {
	node := map[string]interface{}{"name": "n1", "status": "Ready"}

	// Real cordon (alreadyCordoned=false → just performed)
	got := buildSchedulabilityResponse("cordon", node, false)
	if got["status"] != "cordoned" {
		t.Errorf("status = %v, want cordoned", got["status"])
	}
	if got["alreadyCordoned"] != false {
		t.Errorf("alreadyCordoned = %v, want false", got["alreadyCordoned"])
	}
	if _, has := got["alreadyUncordoned"]; has {
		t.Error("response must not include alreadyUncordoned for cordon action")
	}

	// No-op cordon (alreadyCordoned=true → was already in state)
	got = buildSchedulabilityResponse("cordon", node, true)
	if got["alreadyCordoned"] != true {
		t.Errorf("alreadyCordoned = %v, want true", got["alreadyCordoned"])
	}
}

func TestBuildSchedulabilityResponseUncordon(t *testing.T) {
	node := map[string]interface{}{"name": "n1"}
	got := buildSchedulabilityResponse("uncordon", node, false)
	if got["status"] != "uncordoned" {
		t.Errorf("status = %v, want uncordoned", got["status"])
	}
	if got["alreadyUncordoned"] != false {
		t.Errorf("alreadyUncordoned = %v, want false", got["alreadyUncordoned"])
	}
	if _, has := got["alreadyCordoned"]; has {
		t.Error("response must not include alreadyCordoned for uncordon action")
	}
}

