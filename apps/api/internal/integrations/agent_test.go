package integrations

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Every test uses a fake clientset so we exercise the same client
// interface the production handlers do, without needing a cluster.

func TestAgentDetect_NotInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	a := NewAgent(nil, nil)

	snap, err := a.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusNotInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusNotInstalled)
	}
	if snap.Version != "" {
		t.Errorf("version = %q, want empty", snap.Version)
	}
	// Meta should still be populated so the UI can render the card.
	if snap.Name == "" || snap.Description == "" {
		t.Error("meta fields missing on NotInstalled snapshot")
	}
}

func TestAgentDetect_Installed_HelmLabel(t *testing.T) {
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt-system", map[string]string{
		"app.kubernetes.io/name": "kubebolt-agent",
	}, "ghcr.io/clm-cloud-solutions/kubebolt/agent:1.2.3", 3, 3, nil))
	a := NewAgent(nil, nil)

	snap, err := a.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusInstalled)
	}
	if snap.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", snap.Version)
	}
	if snap.Namespace != "kubebolt-system" {
		t.Errorf("namespace = %q, want kubebolt-system", snap.Namespace)
	}
	if snap.Health == nil || snap.Health.PodsReady != 3 || snap.Health.PodsDesired != 3 {
		t.Errorf("health = %+v, want 3/3", snap.Health)
	}
}

func TestAgentDetect_Installed_KubeboltNamespace_1_13(t *testing.T) {
	// 1.13 convention: install namespace is `kubebolt` (our docs +
	// chart NOTES use it). Pre-1.13 used `kubebolt-system`; both
	// preferred namespaces are probed via agentPreferredNamespaces.
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt", map[string]string{
		"app.kubernetes.io/name": "kubebolt-agent",
	}, "ghcr.io/clm-cloud-solutions/kubebolt/agent:1.13.0", 1, 1, nil))
	snap, err := NewAgent(nil, nil).Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusInstalled)
	}
	if snap.Namespace != "kubebolt" {
		t.Errorf("namespace = %q, want kubebolt", snap.Namespace)
	}
}

func TestAgentDetect_Installed_CustomNamespace_FallsBackToClusterWide(t *testing.T) {
	// Operators are NOT constrained to agentPreferredNamespaces — many
	// install the agent in a namespace that fits their existing
	// conventions (monitoring, observability, kb-prod, etc.). The
	// preferred list is just a fast-path short-circuit. When the
	// agent is somewhere else, the cluster-wide (NamespaceAll) attempts
	// at the end of the lookup grid catch it. Regression test for
	// the session 11-A operator concern: do not force a specific ns.
	cs := fake.NewSimpleClientset(agentDaemonSet("monitoring", map[string]string{
		"app.kubernetes.io/name": "kubebolt-agent",
	}, "agent:1.13.0", 2, 2, nil))
	snap, err := NewAgent(nil, nil).Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusInstalled)
	}
	if snap.Namespace != "monitoring" {
		t.Errorf("namespace = %q, want monitoring (cluster-wide fallback must report the actual ns)", snap.Namespace)
	}
}

func TestAgentDetect_TransientErrorOnFirstAttempt_StillFindsAgent(t *testing.T) {
	// Session 11-A regression: a 1.13 install in `kubebolt` namespace
	// over an unstable agent-proxy tunnel had its first
	// `DaemonSets("kubebolt-system").List(...)` attempt fail with a
	// transport error, and the OLD code returned that error
	// immediately, skipping the cluster-wide fallback. The agent card
	// was stuck UNKNOWN even though the DaemonSet existed and was
	// healthy. New behavior: collect errors across attempts and keep
	// going; only surface the last error if NO attempt succeeded.
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt", map[string]string{
		"app.kubernetes.io/name": "kubebolt-agent",
	}, "agent:1.13.0", 1, 1, nil))
	// Reactor injects an error on the FIRST DaemonSets.List call
	// (against the legacy `kubebolt-system` namespace), then steps
	// out of the way so subsequent calls hit the real fixture.
	first := true
	cs.PrependReactor("list", "daemonsets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		la, ok := action.(k8stesting.ListAction)
		if !ok || la.GetNamespace() != "kubebolt-system" || !first {
			return false, nil, nil
		}
		first = false
		return true, nil, errors.New("simulated transport error from agent-proxy tunnel")
	})

	snap, err := NewAgent(nil, nil).Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("Detect bubbled error to caller: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q (fallback should have found the DS)", snap.Status, StatusInstalled)
	}
	if snap.Namespace != "kubebolt" {
		t.Errorf("namespace = %q, want kubebolt (fallback should reach it)", snap.Namespace)
	}
}

func TestAgentDetect_AllAttemptsFail_SurfacesError(t *testing.T) {
	// When the API is genuinely unreachable (every attempt errors), we
	// MUST surface that as StatusUnknown rather than NotInstalled —
	// "couldn't ask" is a different operator signal than "asked and
	// got no DaemonSet".
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "daemonsets", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unreachable")
	})

	snap, err := NewAgent(nil, nil).Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("Detect returned err to caller (should encode in snap.Status): %v", err)
	}
	if snap.Status != StatusUnknown {
		t.Errorf("status = %q, want %q (all attempts errored)", snap.Status, StatusUnknown)
	}
	if snap.Health == nil || snap.Health.Message == "" {
		t.Errorf("health message missing — operator needs the reason")
	}
}

func TestAgentDetect_Installed_DevLabel(t *testing.T) {
	// Legacy dev manifest uses `app: kubebolt-agent` instead of the
	// Helm convention. Detect should fall back to it.
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt-system", map[string]string{
		"app": "kubebolt-agent",
	}, "kubebolt-agent:dev", 1, 1, nil))
	a := NewAgent(nil, nil)

	snap, err := a.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusInstalled)
	}
	if snap.Version != "dev" {
		t.Errorf("version = %q, want dev", snap.Version)
	}
}

func TestAgentDetect_Degraded_PodsNotReady(t *testing.T) {
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt-system", map[string]string{
		"app.kubernetes.io/name": "kubebolt-agent",
	}, "ghcr.io/x/agent:1.0.0", 3, 1, nil))
	a := NewAgent(nil, nil)

	snap, err := a.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusDegraded {
		t.Errorf("status = %q, want %q", snap.Status, StatusDegraded)
	}
	if snap.Health == nil || snap.Health.Message == "" {
		t.Error("degraded health should include a Message")
	}
}

func TestAgentDetect_Degraded_ZeroScheduled(t *testing.T) {
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt-system", map[string]string{
		"app.kubernetes.io/name": "kubebolt-agent",
	}, "x:y", 0, 0, nil))
	a := NewAgent(nil, nil)

	snap, _ := a.Detect(context.Background(), cs)
	if snap.Status != StatusDegraded {
		t.Errorf("status = %q, want %q (zero scheduled)", snap.Status, StatusDegraded)
	}
}

func TestAgentDetect_FeatureFlags(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		wantHubble bool
	}{
		{"default_unset_is_on", nil, true},
		{"explicit_true", map[string]string{"KUBEBOLT_HUBBLE_ENABLED": "true"}, true},
		{"explicit_false", map[string]string{"KUBEBOLT_HUBBLE_ENABLED": "false"}, false},
		{"yes_is_true", map[string]string{"KUBEBOLT_HUBBLE_ENABLED": "yes"}, true},
		{"0_is_false", map[string]string{"KUBEBOLT_HUBBLE_ENABLED": "0"}, false},
		{"garbage_falls_back_to_default_on", map[string]string{"KUBEBOLT_HUBBLE_ENABLED": "maybe"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt-system", map[string]string{
				"app.kubernetes.io/name": "kubebolt-agent",
			}, "x:y", 1, 1, tc.env))
			snap, _ := NewAgent(nil, nil).Detect(context.Background(), cs)
			var got *FeatureFlag
			for i := range snap.Features {
				if snap.Features[i].Key == "hubble" {
					got = &snap.Features[i]
					break
				}
			}
			if got == nil {
				t.Fatal("hubble feature flag missing from snapshot")
			}
			if got.Enabled != tc.wantHubble {
				t.Errorf("hubble.Enabled = %v, want %v", got.Enabled, tc.wantHubble)
			}
		})
	}
}

// agentDaemonSet is a tiny factory for DaemonSet fixtures. Inline
// struct initialization gets noisy fast; this centralizes the boring
// parts so test bodies read like specs.
func agentDaemonSet(ns string, labels map[string]string, image string, desired, ready int32, env map[string]string) *appsv1.DaemonSet {
	var envVars []corev1.EnvVar
	for k, v := range env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubebolt-agent",
			Namespace: ns,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "agent",
						Image: image,
						Env:   envVars,
					}},
				},
			},
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired,
			NumberReady:            ready,
		},
	}
}
