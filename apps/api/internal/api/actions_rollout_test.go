package api

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// Rollout pause/resume tests focus on:
//   1. Patch SHAPE — buildRolloutPausedPatch must produce
//      `{"spec":{"paused":<bool>}}` exactly. A drifted shape (eg.
//      nesting inside spec.template) wouldn't fail validation but
//      would silently be a no-op against a real apiserver.
//   2. INVARIANCE end-to-end against fake clientset — the patch must
//      flip Deployment.spec.paused from false → true (and vice-versa)
//      WITHOUT touching anything else: replicas, selector, strategy,
//      labels, the pod template's containers list. If the patch ever
//      drifts to a Replace-like merge strategy, the invariance test
//      catches it because Strategy/Replicas/Containers all get nuked.
//   3. Response SHAPE — the {status, alreadyX, deployment} envelope
//      is what the frontend uses to drive the panel state without a
//      refetch. Renaming a key would silently fall back to the 30s
//      poll cadence.

func TestBuildRolloutPausedPatchTrue(t *testing.T) {
	got, err := buildRolloutPausedPatch(true)
	if err != nil {
		t.Fatalf("buildRolloutPausedPatch(true): %v", err)
	}
	want := `{"spec":{"paused":true}}`
	if string(got) != want {
		t.Errorf("pause patch = %s, want %s", got, want)
	}
}

func TestBuildRolloutPausedPatchFalse(t *testing.T) {
	got, err := buildRolloutPausedPatch(false)
	if err != nil {
		t.Fatalf("buildRolloutPausedPatch(false): %v", err)
	}
	want := `{"spec":{"paused":false}}`
	if string(got) != want {
		t.Errorf("resume patch = %s, want %s", got, want)
	}
}

// TestRolloutPausePatchInvariance is the load-bearing test: applying
// the patch against a real (well, fake) clientset must flip
// spec.paused AND preserve every other Deployment field — replicas,
// selector, strategy, labels, the pod template's containers list, the
// works. If the patch ever drifts to a Replace-like merge strategy,
// this test fails because Strategy and the containers list get nuked.
func TestRolloutPausePatchInvariance(t *testing.T) {
	maxSurge := intstr.FromString("25%")
	maxUnavailable := intstr.FromString("25%")
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments-api",
			Namespace: "default",
			Labels:    map[string]string{"app": "payments-api", "team": "payments"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(5),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "payments-api"},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       &maxSurge,
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "payments-api"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "acme/payments-api:v2.9.1",
						Env: []corev1.EnvVar{
							{Name: "DEBUG", Value: "true"},
						},
					}},
				},
			},
			Paused: false,
		},
	}
	cs := fake.NewSimpleClientset(dep)
	ctx := context.Background()

	patch, err := buildRolloutPausedPatch(true)
	if err != nil {
		t.Fatalf("buildRolloutPausedPatch: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Patch(
		ctx, "payments-api", types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	got, err := cs.AppsV1().Deployments("default").Get(ctx, "payments-api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !got.Spec.Paused {
		t.Error("spec.paused = false after pause patch")
	}
	// Replicas, selector, strategy, labels, the pod template
	// containers must survive. Merge patch only touches the keys it
	// names; this is the property the whole feature rests on.
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 5 {
		t.Errorf("Replicas clobbered: %v", got.Spec.Replicas)
	}
	if got.Spec.Selector == nil || got.Spec.Selector.MatchLabels["app"] != "payments-api" {
		t.Errorf("Selector clobbered: %v", got.Spec.Selector)
	}
	if got.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("Strategy type clobbered: %v", got.Spec.Strategy.Type)
	}
	if got.Spec.Strategy.RollingUpdate == nil ||
		got.Spec.Strategy.RollingUpdate.MaxSurge == nil ||
		got.Spec.Strategy.RollingUpdate.MaxSurge.StrVal != "25%" {
		t.Errorf("RollingUpdate strategy clobbered: %+v", got.Spec.Strategy.RollingUpdate)
	}
	if got.Labels["team"] != "payments" {
		t.Errorf("Labels clobbered: %v", got.Labels)
	}
	if len(got.Spec.Template.Spec.Containers) != 1 ||
		got.Spec.Template.Spec.Containers[0].Image != "acme/payments-api:v2.9.1" {
		t.Errorf("Pod template containers clobbered: %+v", got.Spec.Template.Spec.Containers)
	}
	if len(got.Spec.Template.Spec.Containers[0].Env) != 1 ||
		got.Spec.Template.Spec.Containers[0].Env[0].Name != "DEBUG" {
		t.Errorf("Container env clobbered: %+v", got.Spec.Template.Spec.Containers[0].Env)
	}
}

// TestRolloutResumePatchRoundTrip — start paused, resume flips it back.
// Catches a regression where the patch encodes false as the JSON
// default and gets dropped by the marshaller (paused field never
// serialized → no-op).
func TestRolloutResumePatchRoundTrip(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Paused: true},
	}
	cs := fake.NewSimpleClientset(dep)
	ctx := context.Background()

	patch, _ := buildRolloutPausedPatch(false)
	if _, err := cs.AppsV1().Deployments("default").Patch(
		ctx, "d1", types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got, _ := cs.AppsV1().Deployments("default").Get(ctx, "d1", metav1.GetOptions{})
	if got.Spec.Paused {
		t.Error("spec.paused = true after resume patch")
	}
}

func TestBuildRolloutResponsePause(t *testing.T) {
	dep := map[string]interface{}{"name": "d1", "namespace": "default"}

	// Real pause (alreadyPaused=false → just performed)
	got := buildRolloutResponse("rollout-pause", dep, false)
	if got["status"] != "paused" {
		t.Errorf("status = %v, want paused", got["status"])
	}
	if got["alreadyPaused"] != false {
		t.Errorf("alreadyPaused = %v, want false", got["alreadyPaused"])
	}
	if _, has := got["alreadyActive"]; has {
		t.Error("response must not include alreadyActive for pause action")
	}

	// No-op pause (alreadyPaused=true → was already in state)
	got = buildRolloutResponse("rollout-pause", dep, true)
	if got["alreadyPaused"] != true {
		t.Errorf("alreadyPaused = %v, want true", got["alreadyPaused"])
	}
}

func TestBuildRolloutResponseResume(t *testing.T) {
	dep := map[string]interface{}{"name": "d1"}
	got := buildRolloutResponse("rollout-resume", dep, false)
	if got["status"] != "resumed" {
		t.Errorf("status = %v, want resumed", got["status"])
	}
	if got["alreadyActive"] != false {
		t.Errorf("alreadyActive = %v, want false", got["alreadyActive"])
	}
	if _, has := got["alreadyPaused"]; has {
		t.Error("response must not include alreadyPaused for resume action")
	}
}

// TestRolloutPausableTypes guards the type-allowlist boundary. If a
// future PR adds StatefulSet to the allowlist without checking that
// upstream apps/v1 supports spec.paused (it doesn't, as of K8s 1.32),
// this assertion fires.
func TestRolloutPausableTypesScope(t *testing.T) {
	if !rolloutPausableTypes["deployments"] {
		t.Error("deployments should be in rolloutPausableTypes")
	}
	if rolloutPausableTypes["statefulsets"] {
		t.Error("statefulsets must not be in rolloutPausableTypes — upstream apps/v1 has no spec.paused (as of K8s 1.32). See tier2-rollout-pause-resume.md for the type-scope reasoning.")
	}
	if rolloutPausableTypes["daemonsets"] {
		t.Error("daemonsets must not be in rolloutPausableTypes — no equivalent field")
	}
}
