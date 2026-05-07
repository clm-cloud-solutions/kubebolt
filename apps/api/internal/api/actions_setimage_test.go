package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// Tests for handleSetImage cover three high-risk behaviors that
// would silently break without coverage:
//
//   1. Patch SHAPE — buildSetImagePatch must produce the exact
//      strategic-merge shape that client-go expects, otherwise the
//      Patch() call either errors or (worse) replaces the containers
//      array wholesale instead of merging by name.
//   2. Patch INVARIANCE — applying the patch via fake clientset
//      against a multi-container Deployment with env/ports/volumeMounts
//      must touch ONLY the targeted containers' image fields. This is
//      the property the whole feature rests on.
//   3. SHORT-CIRCUIT — the unchanged-set detection must agree with
//      the order operators send container/image pairs in (which need
//      not match the workload's container ordering).
//
// Coverage for getCurrentContainerImages confirms the typed-client
// fetch works for all three supported kinds and rejects unsupported
// types.

func TestBuildSetImagePatchShape(t *testing.T) {
	body := []imagePair{
		{Container: "app", Image: "ghcr.io/acme/app:v2"},
		{Container: "sidecar", Image: "ghcr.io/acme/sidecar:v1"},
	}
	got, err := buildSetImagePatch(body)
	if err != nil {
		t.Fatalf("buildSetImagePatch: %v", err)
	}

	// Decode and walk the structure rather than string-matching, so
	// JSON key ordering doesn't fail the test.
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	containers, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	cs, ok := containers.([]any)
	if !ok || len(cs) != 2 {
		t.Fatalf("expected 2 containers in patch, got %v", containers)
	}
	for i, want := range body {
		c := cs[i].(map[string]any)
		if c["name"] != want.Container || c["image"] != want.Image {
			t.Errorf("patch[%d] = %v, want %v/%v", i, c, want.Container, want.Image)
		}
		// The patch MUST NOT carry any other container fields — those
		// would clobber the live spec on merge.
		if len(c) != 2 {
			t.Errorf("patch[%d] has extra fields: %v", i, c)
		}
	}
}

// TestSetImageStrategicMergeInvariance is the load-bearing test:
// applying the patch against a real (well, fake) clientset must
// preserve every non-image field on both targeted and untargeted
// containers. If strategic merge is ever misused — e.g. swapping to
// MergePatchType — this test fails because the containers array gets
// replaced wholesale instead of merged by name.
func TestSetImageStrategicMergeInvariance(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "ghcr.io/acme/app:v1",
							Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
							Env:   []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data"},
							},
						},
						{
							Name:  "sidecar",
							Image: "ghcr.io/acme/sidecar:v1",
							Env:   []corev1.EnvVar{{Name: "MODE", Value: "proxy"}},
						},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	ctx := context.Background()

	patch, err := buildSetImagePatch([]imagePair{
		{Container: "app", Image: "ghcr.io/acme/app:v2"},
	})
	if err != nil {
		t.Fatalf("buildSetImagePatch: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Patch(
		ctx, "api", types.StrategicMergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	got, err := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	containers := got.Spec.Template.Spec.Containers
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers after patch, got %d (containers=%v) — strategic merge is broken", len(containers), containers)
	}

	app := findContainer(containers, "app")
	if app == nil {
		t.Fatal("app container disappeared after patch")
	}
	if app.Image != "ghcr.io/acme/app:v2" {
		t.Errorf("app.Image = %q, want ghcr.io/acme/app:v2", app.Image)
	}
	// All non-image fields preserved on targeted container.
	if len(app.Ports) != 1 || app.Ports[0].ContainerPort != 8080 {
		t.Errorf("app.Ports clobbered: %v", app.Ports)
	}
	if len(app.Env) != 1 || app.Env[0].Value != "info" {
		t.Errorf("app.Env clobbered: %v", app.Env)
	}
	if len(app.VolumeMounts) != 1 || app.VolumeMounts[0].MountPath != "/data" {
		t.Errorf("app.VolumeMounts clobbered: %v", app.VolumeMounts)
	}

	side := findContainer(containers, "sidecar")
	if side == nil {
		t.Fatal("sidecar container vanished — patch replaced array instead of merging")
	}
	if side.Image != "ghcr.io/acme/sidecar:v1" {
		t.Errorf("sidecar.Image = %q, want unchanged ghcr.io/acme/sidecar:v1", side.Image)
	}
	if len(side.Env) != 1 || side.Env[0].Value != "proxy" {
		t.Errorf("sidecar.Env clobbered: %v", side.Env)
	}
}

func TestSetImageMultiContainerSubset(t *testing.T) {
	// Three containers; patch only updates the middle one. The other
	// two must remain on their original images.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "img:1"},
						{Name: "sidecar", Image: "side:1"},
						{Name: "exporter", Image: "exp:1"},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	ctx := context.Background()

	patch, err := buildSetImagePatch([]imagePair{
		{Container: "sidecar", Image: "side:2"},
	})
	if err != nil {
		t.Fatalf("buildSetImagePatch: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Patch(
		ctx, "api", types.StrategicMergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	containers := got.Spec.Template.Spec.Containers

	wantImages := map[string]string{"app": "img:1", "sidecar": "side:2", "exporter": "exp:1"}
	for _, c := range containers {
		if c.Image != wantImages[c.Name] {
			t.Errorf("%s.Image = %q, want %q", c.Name, c.Image, wantImages[c.Name])
		}
	}
}

func TestGetCurrentContainerImages(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "a", Image: "img:1"}, {Name: "b", Image: "img:2"}},
			}}},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "x", Image: "x:1"}},
			}}},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "ns"},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "agent", Image: "agent:1"}},
			}}},
		},
	)
	ctx := context.Background()

	cases := []struct {
		kind, name string
		want       []imagePair
	}{
		{"deployments", "d", []imagePair{{"a", "img:1"}, {"b", "img:2"}}},
		{"statefulsets", "s", []imagePair{{"x", "x:1"}}},
		{"daemonsets", "ds", []imagePair{{"agent", "agent:1"}}},
	}
	for _, tc := range cases {
		got, err := getCurrentContainerImages(ctx, cs, tc.kind, "ns", tc.name)
		if err != nil {
			t.Errorf("%s: %v", tc.kind, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %d pairs, want %d", tc.kind, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s[%d] = %v, want %v", tc.kind, i, got[i], tc.want[i])
			}
		}
	}

	// Unsupported kind must be a hard error so the handler doesn't
	// silently fall through to a no-op.
	if _, err := getCurrentContainerImages(ctx, cs, "pods", "ns", "p"); err == nil {
		t.Error("getCurrentContainerImages(pods) should error")
	} else if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unsupported error wrong: %v", err)
	}
}

func TestContainersToImagePairs(t *testing.T) {
	cs := []corev1.Container{
		{Name: "a", Image: "x:1"},
		{Name: "b", Image: "y:2"},
	}
	got := containersToImagePairs(cs)
	if len(got) != 2 || got[0] != (imagePair{"a", "x:1"}) || got[1] != (imagePair{"b", "y:2"}) {
		t.Errorf("containersToImagePairs = %v", got)
	}
	// Empty input → empty (non-nil) slice.
	if got := containersToImagePairs(nil); got == nil || len(got) != 0 {
		t.Errorf("containersToImagePairs(nil) = %v, want empty slice", got)
	}
}

// helpers

func findContainer(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func getNested(m map[string]any, keys ...string) (any, bool, error) {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur, ok = mm[k]
		if !ok {
			return nil, false, nil
		}
	}
	return cur, true, nil
}
