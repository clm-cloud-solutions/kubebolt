package insights

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// image-pull-backoff: the image, what the kubelet said (not found,
// unauthorized, a network error) and the pull secrets.
func TestImagePullBackoff_EvidenceCarriesImageMessageAndSecrets(t *testing.T) {
	p := pod("lab", "broken-0")
	p.Spec.Containers = []corev1.Container{{Name: "app", Image: "nginx:1.27-alpne"}}
	p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull",
			Message: `failed to pull and unpack image "docker.io/library/nginx:1.27-alpne": not found`}},
	}}
	got := imagePullBackoffRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Image").Detail; d != "nginx:1.27-alpne" {
		t.Errorf("image: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Waiting").Detail; !strings.HasSuffix(d, ": not found") {
		t.Errorf("waiting: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Pull secrets").Detail; d != "regcred" {
		t.Errorf("pull secrets: %q", d)
	}
}
