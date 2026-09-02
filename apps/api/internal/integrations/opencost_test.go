package integrations

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// openCostPod builds a pod shaped like the official chart's output.
// ready toggles the PodReady condition; labels nil drops the
// standard labels (exercises the name-prefix fallback path).
func openCostPod(name, ns string, labels map[string]string, ready bool, image string) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "opencost", Image: image},
		}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: cond},
		}},
	}
}

func chartLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":    "opencost",
		"app.kubernetes.io/version": "1.108.0",
	}
}

func TestOpenCostDetect_NotInstalled(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewOpenCost(nil, nil)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusNotInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusNotInstalled)
	}
	// Meta should still be populated so the UI can render the card.
	if snap.ID != OpenCostID || snap.Name == "" || snap.Description == "" {
		t.Error("meta fields missing on NotInstalled snapshot")
	}
}

func TestOpenCostDetect_InstalledHealthy(t *testing.T) {
	cs := fake.NewSimpleClientset(
		openCostPod("opencost-7d4f8b-a", "opencost", chartLabels(), true, "quay.io/kubecost1/kubecost-cost-model:1.108.0"),
		openCostPod("opencost-7d4f8b-b", "opencost", chartLabels(), true, "quay.io/kubecost1/kubecost-cost-model:1.108.0"),
	)
	p := NewOpenCost(nil, nil)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusInstalled)
	}
	if snap.Namespace != "opencost" {
		t.Errorf("namespace = %q, want opencost", snap.Namespace)
	}
	if snap.Version != "1.108.0" {
		t.Errorf("version = %q, want 1.108.0 (from version label)", snap.Version)
	}
	if snap.Health == nil || snap.Health.PodsReady != 2 || snap.Health.PodsDesired != 2 {
		t.Errorf("health = %+v, want 2/2", snap.Health)
	}
	if snap.Health.Message != "" {
		t.Errorf("healthy install without probe should carry no message, got %q", snap.Health.Message)
	}
	if snap.Managed {
		t.Error("helm install without managed-by=kubebolt must report Managed=false")
	}
}

func TestOpenCostDetect_Degraded(t *testing.T) {
	cs := fake.NewSimpleClientset(
		openCostPod("opencost-a", "opencost", chartLabels(), true, "opencost:1.108.0"),
		openCostPod("opencost-b", "opencost", chartLabels(), false, "opencost:1.108.0"),
	)
	p := NewOpenCost(nil, nil)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusDegraded {
		t.Errorf("status = %q, want %q", snap.Status, StatusDegraded)
	}
	if snap.Health == nil || snap.Health.PodsReady != 1 || snap.Health.PodsDesired != 2 {
		t.Errorf("health = %+v, want 1/2", snap.Health)
	}
}

func TestOpenCostDetect_FallbackByName(t *testing.T) {
	// No standard labels — found by name prefix in a conventional
	// namespace; version falls back to the image tag.
	cs := fake.NewSimpleClientset(
		openCostPod("opencost-6f9c", "kubecost", nil, true, "ghcr.io/opencost/opencost:1.113.0"),
	)
	p := NewOpenCost(nil, nil)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("status = %q, want %q", snap.Status, StatusInstalled)
	}
	if snap.Namespace != "kubecost" {
		t.Errorf("namespace = %q, want kubecost", snap.Namespace)
	}
	if snap.Version != "1.113.0" {
		t.Errorf("version = %q, want 1.113.0 (from image tag)", snap.Version)
	}
}

func TestOpenCostDetect_ListForbidden_Unknown(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pods is forbidden: RBAC denied")
	})
	p := NewOpenCost(nil, nil)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("Detect must map list errors to StatusUnknown, not return them: %v", err)
	}
	if snap.Status != StatusUnknown {
		t.Errorf("status = %q, want %q", snap.Status, StatusUnknown)
	}
	if snap.Health == nil || !strings.Contains(snap.Health.Message, "forbidden") {
		t.Errorf("health message should surface the cause, got %+v", snap.Health)
	}
}

func TestOpenCostDetect_ProbeSaysNoSamples(t *testing.T) {
	cs := fake.NewSimpleClientset(
		openCostPod("opencost-a", "opencost", chartLabels(), true, "opencost:1.108.0"),
	)
	probe := func(ctx context.Context, clusterID string) (bool, error) {
		if clusterID != "uid-123" {
			t.Errorf("probe clusterID = %q, want uid-123", clusterID)
		}
		return false, nil
	}
	p := NewOpenCost(func() string { return "uid-123" }, probe)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled {
		t.Errorf("no-samples hint must not degrade status: %q", snap.Status)
	}
	if snap.Health == nil || !strings.Contains(snap.Health.Message, "no cost samples") {
		t.Errorf("expected no-samples hint, got %+v", snap.Health)
	}
}

func TestOpenCostDetect_ProbeErrorIgnored(t *testing.T) {
	cs := fake.NewSimpleClientset(
		openCostPod("opencost-a", "opencost", chartLabels(), true, "opencost:1.108.0"),
	)
	probe := func(ctx context.Context, clusterID string) (bool, error) {
		return false, errors.New("vm unreachable")
	}
	p := NewOpenCost(func() string { return "uid-123" }, probe)

	snap, err := p.Detect(context.Background(), cs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Status != StatusInstalled || (snap.Health != nil && snap.Health.Message != "") {
		t.Errorf("probe errors are advisory and must not alter the verdict, got status=%q health=%+v", snap.Status, snap.Health)
	}
}
