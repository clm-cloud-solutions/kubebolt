package api

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// Tests for handleSetResources cover:
//
//   1. Patch SHAPE — buildSetResourcesPatch must produce strategic-
//      merge bytes that include only the fields the operator named,
//      and must split normal vs init containers into the right arrays.
//   2. Patch INVARIANCE — applying the patch against a multi-container
//      Deployment with env/ports/volumes must touch ONLY the targeted
//      containers' resources sub-object. Every other field on every
//      container must survive, including untargeted containers'
//      existing resources blocks.
//   3. VALIDATION — quantity-string parsing rejects bad inputs;
//      limit-vs-request enforcement catches inverted bounds before
//      the apiserver does.
//   4. INIT containers route to the initContainers array, not
//      containers, when the InitContainer flag is set.

func strPtr(s string) *string { return &s }

func TestBuildSetResourcesPatchShape(t *testing.T) {
	body := []containerResourcesPatch{
		{
			Container: "app",
			Requests:  &resourceQuantity{CPU: strPtr("200m"), Memory: strPtr("384Mi")},
			Limits:    &resourceQuantity{CPU: strPtr("500m"), Memory: strPtr("768Mi")},
		},
	}
	got, err := buildSetResourcesPatch(body)
	if err != nil {
		t.Fatalf("buildSetResourcesPatch: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	containers, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	cs, ok := containers.([]any)
	if !ok || len(cs) != 1 {
		t.Fatalf("expected 1 container in patch, got %v", containers)
	}
	c := cs[0].(map[string]any)
	if c["name"] != "app" {
		t.Errorf("name = %v, want app", c["name"])
	}
	res := c["resources"].(map[string]any)
	reqs := res["requests"].(map[string]any)
	if reqs["cpu"] != "200m" || reqs["memory"] != "384Mi" {
		t.Errorf("requests wrong: %v", reqs)
	}
	lims := res["limits"].(map[string]any)
	if lims["cpu"] != "500m" || lims["memory"] != "768Mi" {
		t.Errorf("limits wrong: %v", lims)
	}
	// Patch MUST NOT include initContainers when no init row was
	// provided — otherwise strategic merge would emit an empty array
	// and clobber existing init containers.
	if _, has, _ := getNested(decoded, "spec", "template", "spec", "initContainers"); has {
		t.Error("initContainers must be absent when no init row was provided")
	}
}

// TestBuildSetResourcesPatchPartialDimensions — operator only bumps
// memory limit, leaves cpu request alone. The patch must include only
// memory, NOT a phantom empty cpu key (which the apiserver would
// interpret as "delete cpu" via JSON-merge semantics on some shapes).
func TestBuildSetResourcesPatchPartialDimensions(t *testing.T) {
	body := []containerResourcesPatch{
		{
			Container: "app",
			Limits:    &resourceQuantity{Memory: strPtr("768Mi")},
		},
	}
	got, _ := buildSetResourcesPatch(body)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	containers, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	c := containers.([]any)[0].(map[string]any)
	res := c["resources"].(map[string]any)
	if _, has := res["requests"]; has {
		t.Error("requests must be absent when no request dimension was provided")
	}
	lims := res["limits"].(map[string]any)
	if _, has := lims["cpu"]; has {
		t.Error("limits.cpu must be absent when only memory was provided")
	}
	if lims["memory"] != "768Mi" {
		t.Errorf("limits.memory wrong: %v", lims["memory"])
	}
}

// TestBuildSetResourcesPatchEmptyStringSkipped — empty strings are
// treated as field-absent in v1 (see actions_resources.go file-level
// comment). They MUST NOT appear in the patch with empty values, and
// MUST NOT trigger any "remove" behavior either.
func TestBuildSetResourcesPatchEmptyStringSkipped(t *testing.T) {
	body := []containerResourcesPatch{
		{
			Container: "app",
			Requests:  &resourceQuantity{CPU: strPtr(""), Memory: strPtr("256Mi")},
		},
	}
	got, _ := buildSetResourcesPatch(body)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	containers, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	c := containers.([]any)[0].(map[string]any)
	reqs := c["resources"].(map[string]any)["requests"].(map[string]any)
	if _, has := reqs["cpu"]; has {
		t.Errorf("empty-string cpu should be skipped, got %v", reqs)
	}
	if reqs["memory"] != "256Mi" {
		t.Errorf("memory wrong: %v", reqs["memory"])
	}
}

// TestBuildSetResourcesPatchInitContainer — init container row goes
// into the initContainers array, not containers.
func TestBuildSetResourcesPatchInitContainer(t *testing.T) {
	body := []containerResourcesPatch{
		{
			Container:     "init-db",
			InitContainer: true,
			Limits:        &resourceQuantity{Memory: strPtr("128Mi")},
		},
		{
			Container: "app",
			Limits:    &resourceQuantity{Memory: strPtr("512Mi")},
		},
	}
	got, _ := buildSetResourcesPatch(body)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)

	normal, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	if len(normal.([]any)) != 1 {
		t.Errorf("normal containers count = %d, want 1", len(normal.([]any)))
	}
	if normal.([]any)[0].(map[string]any)["name"] != "app" {
		t.Errorf("normal container name wrong: %v", normal.([]any)[0])
	}

	init, _, _ := getNested(decoded, "spec", "template", "spec", "initContainers")
	if len(init.([]any)) != 1 {
		t.Errorf("init containers count = %d, want 1", len(init.([]any)))
	}
	if init.([]any)[0].(map[string]any)["name"] != "init-db" {
		t.Errorf("init container name wrong: %v", init.([]any)[0])
	}
}

// TestSetResourcesStrategicMergeInvariance is the load-bearing test:
// applying the patch against a real (well, fake) clientset must
// preserve every non-resources field on every container (including
// untargeted containers' existing resources). If strategic merge ever
// got swapped to MergePatchType, this test fails because the
// containers array gets replaced wholesale instead of merged by name.
func TestSetResourcesStrategicMergeInvariance(t *testing.T) {
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
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("300m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
						{
							Name:  "sidecar",
							Image: "ghcr.io/acme/sidecar:v1",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	ctx := context.Background()

	// Patch only `app`'s memory limit. Everything else must survive.
	patch, err := buildSetResourcesPatch([]containerResourcesPatch{
		{Container: "app", Limits: &resourceQuantity{Memory: strPtr("768Mi")}},
	})
	if err != nil {
		t.Fatalf("buildSetResourcesPatch: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Patch(
		ctx, "api", types.StrategicMergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	got, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})

	app := got.Spec.Template.Spec.Containers[0]
	if app.Resources.Limits.Memory().String() != "768Mi" {
		t.Errorf("app.limits.memory = %s, want 768Mi", app.Resources.Limits.Memory().String())
	}
	// Other dimensions on `app` must survive.
	if app.Resources.Limits.Cpu().String() != "300m" {
		t.Errorf("app.limits.cpu clobbered: %s", app.Resources.Limits.Cpu().String())
	}
	if app.Resources.Requests.Memory().String() != "256Mi" {
		t.Errorf("app.requests.memory clobbered: %s", app.Resources.Requests.Memory().String())
	}
	if app.Resources.Requests.Cpu().String() != "100m" {
		t.Errorf("app.requests.cpu clobbered: %s", app.Resources.Requests.Cpu().String())
	}
	// Non-resources fields on `app` must survive.
	if app.Image != "ghcr.io/acme/app:v1" {
		t.Errorf("image clobbered: %s", app.Image)
	}
	if len(app.Ports) != 1 || app.Ports[0].ContainerPort != 8080 {
		t.Errorf("ports clobbered: %v", app.Ports)
	}
	if len(app.Env) != 1 || app.Env[0].Name != "LOG_LEVEL" {
		t.Errorf("env clobbered: %v", app.Env)
	}

	// Untargeted `sidecar` must keep its existing resources unchanged.
	sidecar := got.Spec.Template.Spec.Containers[1]
	if sidecar.Image != "ghcr.io/acme/sidecar:v1" {
		t.Errorf("sidecar image clobbered: %s", sidecar.Image)
	}
	if sidecar.Resources.Requests.Memory().String() != "64Mi" {
		t.Errorf("sidecar.requests.memory clobbered: %s", sidecar.Resources.Requests.Memory().String())
	}
	if !sidecar.Resources.Limits.Memory().IsZero() {
		t.Errorf("sidecar.limits.memory created from nothing: %s", sidecar.Resources.Limits.Memory().String())
	}
}

func TestValidateResourceQuantityRejectsBadStrings(t *testing.T) {
	cases := []struct {
		name  string
		input *resourceQuantity
	}{
		{"cpu lowercase mb", &resourceQuantity{CPU: strPtr("0.5cpu")}},
		{"memory mb instead of Mi", &resourceQuantity{Memory: strPtr("512mb")}},
		{"garbage", &resourceQuantity{CPU: strPtr("not-a-number")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateResourceQuantity(0, "requests", c.input); err == nil {
				t.Errorf("validateResourceQuantity(%v) returned nil; want error", c.input)
			}
		})
	}
}

func TestValidateResourceQuantityAcceptsValidStrings(t *testing.T) {
	cases := []struct {
		name  string
		input *resourceQuantity
	}{
		{"cpu millicores", &resourceQuantity{CPU: strPtr("200m")}},
		{"cpu fractional", &resourceQuantity{CPU: strPtr("0.5")}},
		{"cpu integer", &resourceQuantity{CPU: strPtr("2")}},
		{"memory Mi", &resourceQuantity{Memory: strPtr("512Mi")}},
		{"memory Gi", &resourceQuantity{Memory: strPtr("2Gi")}},
		{"memory M", &resourceQuantity{Memory: strPtr("500M")}},
		{"empty cpu skipped", &resourceQuantity{CPU: strPtr("")}},
		{"nil quantity", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateResourceQuantity(0, "requests", c.input); err != nil {
				t.Errorf("validateResourceQuantity(%v) = %v; want nil", c.input, err)
			}
		})
	}
}

func TestValidateLimitGteRequest(t *testing.T) {
	cases := []struct {
		name    string
		row     containerResourcesPatch
		wantErr bool
	}{
		{
			name: "valid: cpu limit > request",
			row: containerResourcesPatch{
				Container: "app",
				Requests:  &resourceQuantity{CPU: strPtr("100m")},
				Limits:    &resourceQuantity{CPU: strPtr("500m")},
			},
		},
		{
			name: "invalid: cpu limit < request",
			row: containerResourcesPatch{
				Container: "app",
				Requests:  &resourceQuantity{CPU: strPtr("500m")},
				Limits:    &resourceQuantity{CPU: strPtr("100m")},
			},
			wantErr: true,
		},
		{
			name: "valid: memory limit equals request",
			row: containerResourcesPatch{
				Container: "app",
				Requests:  &resourceQuantity{Memory: strPtr("256Mi")},
				Limits:    &resourceQuantity{Memory: strPtr("256Mi")},
			},
		},
		{
			name: "invalid: memory limit < request (cross-unit)",
			row: containerResourcesPatch{
				Container: "app",
				Requests:  &resourceQuantity{Memory: strPtr("1Gi")},
				Limits:    &resourceQuantity{Memory: strPtr("512Mi")},
			},
			wantErr: true,
		},
		{
			name: "valid: only requests provided",
			row: containerResourcesPatch{
				Container: "app",
				Requests:  &resourceQuantity{CPU: strPtr("500m")},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateLimitGteRequest(0, c.row)
			if c.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestSetResourceableTypesScope guards the type-allowlist boundary.
// If a future PR adds e.g. pods to the allowlist (Pod resources are
// immutable and the apiserver would reject the patch), this test
// fires.
func TestSetResourceableTypesScope(t *testing.T) {
	for _, ok := range []string{"deployments", "statefulsets", "daemonsets"} {
		if !setResourceableTypes[ok] {
			t.Errorf("%s should be in setResourceableTypes", ok)
		}
	}
	for _, bad := range []string{"pods", "jobs", "cronjobs", "replicasets"} {
		if setResourceableTypes[bad] {
			t.Errorf("%s must not be in setResourceableTypes — only workload kinds with mutable pod templates", bad)
		}
	}
}
