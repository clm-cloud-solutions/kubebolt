package integrations

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Every test uses a fake clientset so we exercise the same client
// interface the production handlers do, without needing a cluster.

func TestAgentDetect_NotInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	a := NewAgent()

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
	a := NewAgent()

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

func TestAgentDetect_Installed_DevLabel(t *testing.T) {
	// Legacy dev manifest uses `app: kubebolt-agent` instead of the
	// Helm convention. Detect should fall back to it.
	cs := fake.NewSimpleClientset(agentDaemonSet("kubebolt-system", map[string]string{
		"app": "kubebolt-agent",
	}, "kubebolt-agent:dev", 1, 1, nil))
	a := NewAgent()

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
	a := NewAgent()

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
	a := NewAgent()

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
			snap, _ := NewAgent().Detect(context.Background(), cs)
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
