package cluster

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deploymentRevisionManifestYAML powers the History-tab revision diff. It must
// render the FULL Deployment manifest (apiVersion/kind/metadata/spec) with the
// revision's template swapped in, and strip per-revision noise so the diff
// shows real changes, not metadata churn or the pod-template-hash.
func TestDeploymentRevisionManifestYAML(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "kite",
			Namespace:       "kite",
			ResourceVersion: "8146783",
			Generation:      31,
			UID:             "4c7d57e1",
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision":                "31",
				"kubectl.kubernetes.io/last-applied-configuration": "{...}",
				"keep-me": "kept",
			},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	tmpl := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "kite", "pod-template-hash": "7b9c4d"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kite", Image: "kite:0.12"}}},
	}

	y := deploymentRevisionManifestYAML(dep, tmpl)
	if y == "" {
		t.Fatal("expected non-empty manifest YAML")
	}
	// Full-object context, like the YAML tab.
	for _, want := range []string{"apiVersion: apps/v1", "kind: Deployment", "name: kite", "image: kite:0.12"} {
		if !strings.Contains(y, want) {
			t.Errorf("manifest missing %q\n%s", want, y)
		}
	}
	// Noise must be stripped.
	for _, noise := range []string{"resourceVersion", "generation:", "managedFields", "pod-template-hash", "last-applied-configuration", "deployment.kubernetes.io/revision", "status:"} {
		if strings.Contains(y, noise) {
			t.Errorf("manifest should not contain noise %q\n%s", noise, y)
		}
	}
	// Non-noise annotations survive.
	if !strings.Contains(y, "keep-me: kept") {
		t.Errorf("real annotations should survive:\n%s", y)
	}
}

// extractTemplateFromControllerRevision recovers the full pod template (STS/DS)
// from the ControllerRevision's embedded JSON, not just the images.
func TestExtractTemplateFromControllerRevision(t *testing.T) {
	raw := []byte(`{"spec":{"template":{"metadata":{"labels":{"app":"redis"}},"spec":{"containers":[{"name":"redis","image":"redis:7"}]}}}}`)
	tmpl, err := extractTemplateFromControllerRevision(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tmpl.Spec.Containers) != 1 || tmpl.Spec.Containers[0].Image != "redis:7" {
		t.Errorf("template not parsed: %+v", tmpl.Spec.Containers)
	}
	// Empty raw (some DS write empty CRs during init) → zero template, no error.
	if _, err := extractTemplateFromControllerRevision(nil); err != nil {
		t.Errorf("empty raw should not error: %v", err)
	}
}
