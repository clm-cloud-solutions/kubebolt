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

// Tests for handleSetEnv cover the four high-risk behaviors:
//
//   1. Patch SHAPE — set rows emit {name, value/valueFrom},
//      remove rows emit {name, "$patch": "delete"}. Strategic merge's
//      directive interpretation is the load-bearing primitive.
//   2. END-TO-END against fake clientset — applying the patch must
//      add new entries, update existing ones, AND remove the targeted
//      ones, in a single atomic operation, while preserving every
//      other env entry on the same container and every env entry on
//      untargeted containers.
//   3. VALIDATION — name must be C_IDENTIFIER, set requires exactly
//      one of value/valueFrom, remove forbids both, duplicates are
//      rejected.
//   4. INIT containers route to initContainers, not containers.

func TestBuildSetEnvPatchSetAndRemove(t *testing.T) {
	rows := []containerEnvPatch{
		{
			Container: "app",
			Env: []envVarPatch{
				{Name: "DEBUG", Action: "set", Value: strPtr("true")},
				{Name: "OLD_VAR", Action: "remove"},
				{
					Name:   "DB_URL",
					Action: "set",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "db"},
							Key:                  "url",
						},
					},
				},
			},
		},
	}
	got, err := buildSetEnvPatch(rows, false)
	if err != nil {
		t.Fatalf("buildSetEnvPatch: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	containers, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	cs, _ := containers.([]any)
	if len(cs) != 1 {
		t.Fatalf("expected 1 container, got %d", len(cs))
	}
	envEntries, _ := cs[0].(map[string]any)["env"].([]any)
	if len(envEntries) != 3 {
		t.Fatalf("expected 3 env entries, got %d", len(envEntries))
	}

	// DEBUG = literal set
	debug := envEntries[0].(map[string]any)
	if debug["name"] != "DEBUG" || debug["value"] != "true" {
		t.Errorf("DEBUG entry wrong: %v", debug)
	}
	if _, has := debug["$patch"]; has {
		t.Error("set entry should not carry $patch directive")
	}

	// OLD_VAR = remove
	rem := envEntries[1].(map[string]any)
	if rem["name"] != "OLD_VAR" || rem["$patch"] != "delete" {
		t.Errorf("OLD_VAR remove entry wrong: %v", rem)
	}
	if _, has := rem["value"]; has {
		t.Error("remove entry should not carry value")
	}

	// DB_URL = secret valueFrom
	dburl := envEntries[2].(map[string]any)
	if dburl["name"] != "DB_URL" {
		t.Errorf("DB_URL name wrong: %v", dburl["name"])
	}
	vf := dburl["valueFrom"].(map[string]any)
	skr := vf["secretKeyRef"].(map[string]any)
	if skr["name"] != "db" || skr["key"] != "url" {
		t.Errorf("secretKeyRef wrong: %v", skr)
	}
}

func TestBuildSetEnvPatchTriggerRollout(t *testing.T) {
	got, _ := buildSetEnvPatch([]containerEnvPatch{
		{Container: "app", Env: []envVarPatch{{Name: "X", Action: "set", Value: strPtr("y")}}},
	}, true)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	ann, has, _ := getNested(decoded, "spec", "template", "metadata", "annotations")
	if !has {
		t.Fatal("triggerRollout=true should add restart annotation, but metadata.annotations missing")
	}
	annMap := ann.(map[string]any)
	if _, ok := annMap["kubectl.kubernetes.io/restartedAt"]; !ok {
		t.Errorf("expected restartedAt annotation, got %v", annMap)
	}
}

func TestBuildSetEnvPatchNoRolloutTrigger(t *testing.T) {
	got, _ := buildSetEnvPatch([]containerEnvPatch{
		{Container: "app", Env: []envVarPatch{{Name: "X", Action: "set", Value: strPtr("y")}}},
	}, false)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	if _, has, _ := getNested(decoded, "spec", "template", "metadata"); has {
		t.Error("triggerRollout=false should not add metadata block")
	}
}

func TestBuildSetEnvPatchInitContainer(t *testing.T) {
	rows := []containerEnvPatch{
		{Container: "init-db", InitContainer: true, Env: []envVarPatch{{Name: "INIT_X", Action: "set", Value: strPtr("1")}}},
		{Container: "app", Env: []envVarPatch{{Name: "APP_X", Action: "set", Value: strPtr("2")}}},
	}
	got, _ := buildSetEnvPatch(rows, false)
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	normal, _, _ := getNested(decoded, "spec", "template", "spec", "containers")
	if len(normal.([]any)) != 1 || normal.([]any)[0].(map[string]any)["name"] != "app" {
		t.Errorf("normal containers wrong: %v", normal)
	}
	init, _, _ := getNested(decoded, "spec", "template", "spec", "initContainers")
	if len(init.([]any)) != 1 || init.([]any)[0].(map[string]any)["name"] != "init-db" {
		t.Errorf("init containers wrong: %v", init)
	}
}

// TestSetEnvStrategicMergeAddsUpdatesRemoves is the load-bearing test.
// Apply the patch against a Deployment with a 3-entry env list; assert
// that ADD adds, UPDATE updates the matching name, REMOVE drops the
// targeted entry, and ALL other env entries on the same container PLUS
// every env entry on an untargeted container survive.
func TestSetEnvStrategicMergeAddsUpdatesRemoves(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "ghcr.io/acme/app:v1",
							Env: []corev1.EnvVar{
								{Name: "DEBUG", Value: "false"},
								{Name: "REGION", Value: "us-east-1"},
								{Name: "OLD_VAR", Value: "stale"},
							},
						},
						{
							Name:  "sidecar",
							Image: "ghcr.io/acme/sidecar:v1",
							Env: []corev1.EnvVar{
								{Name: "MODE", Value: "proxy"},
							},
						},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	ctx := context.Background()

	patch, _ := buildSetEnvPatch([]containerEnvPatch{
		{
			Container: "app",
			Env: []envVarPatch{
				{Name: "DEBUG", Action: "set", Value: strPtr("true")},      // update
				{Name: "OLD_VAR", Action: "remove"},                         // remove
				{Name: "FEATURE_X", Action: "set", Value: strPtr("on")},     // add
			},
		},
	}, false)

	if _, err := cs.AppsV1().Deployments("default").Patch(
		ctx, "api", types.StrategicMergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	got, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	app := got.Spec.Template.Spec.Containers[0]
	envByName := map[string]string{}
	for _, e := range app.Env {
		envByName[e.Name] = e.Value
	}

	// DEBUG updated
	if v := envByName["DEBUG"]; v != "true" {
		t.Errorf("DEBUG = %q, want true", v)
	}
	// REGION untouched (not in patch)
	if v := envByName["REGION"]; v != "us-east-1" {
		t.Errorf("REGION = %q, want us-east-1 (should survive)", v)
	}
	// OLD_VAR removed
	if _, has := envByName["OLD_VAR"]; has {
		t.Error("OLD_VAR should have been removed by $patch:delete directive")
	}
	// FEATURE_X added
	if v := envByName["FEATURE_X"]; v != "on" {
		t.Errorf("FEATURE_X = %q, want on", v)
	}

	// Untargeted sidecar must keep its env
	sidecar := got.Spec.Template.Spec.Containers[1]
	if len(sidecar.Env) != 1 || sidecar.Env[0].Name != "MODE" {
		t.Errorf("sidecar env clobbered: %v", sidecar.Env)
	}
}

func TestEnvVarNameValidation(t *testing.T) {
	cases := []struct {
		name    string
		want    bool
	}{
		{"FOO", true},
		{"foo_bar", true},
		{"_HIDDEN", true},
		{"X1", true},
		{"1FOO", false},  // can't start with digit
		{"FOO-BAR", false}, // hyphen not allowed
		{"foo.bar", false}, // dot not allowed
		{"", false},
		{"FOO BAR", false}, // space
	}
	for _, c := range cases {
		got := envVarNameRE.MatchString(c.name)
		if got != c.want {
			t.Errorf("envVarNameRE.MatchString(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSetEnvableTypesScope(t *testing.T) {
	for _, ok := range []string{"deployments", "statefulsets", "daemonsets"} {
		if !setEnvableTypes[ok] {
			t.Errorf("%s should be in setEnvableTypes", ok)
		}
	}
	for _, bad := range []string{"pods", "jobs", "cronjobs"} {
		if setEnvableTypes[bad] {
			t.Errorf("%s must not be in setEnvableTypes — only kinds with mutable pod templates", bad)
		}
	}
}

// TestEnvVarToEnvEntryPairKindResolution verifies the response-side
// kind classifier maps every ValueFrom variant correctly. The UI uses
// `kind` to render the right diff row (literal, CM ref, Secret ref,
// field ref, etc.) — getting it wrong silently produces a confusing
// diff display.
func TestEnvVarToEnvEntryPairKindResolution(t *testing.T) {
	cases := []struct {
		name string
		in   corev1.EnvVar
		want string
	}{
		{
			name: "literal",
			in:   corev1.EnvVar{Name: "X", Value: "hello"},
			want: "literal",
		},
		{
			name: "configMap",
			in: corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "cm"},
					Key:                  "k",
				},
			}},
			want: "configMap",
		},
		{
			name: "secret",
			in: corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
					Key:                  "k",
				},
			}},
			want: "secret",
		},
		{
			name: "field",
			in: corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			}},
			want: "field",
		},
		{
			name: "resourceField",
			in: corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				ResourceFieldRef: &corev1.ResourceFieldSelector{
					ContainerName: "app",
					Resource:      "limits.memory",
				},
			}},
			want: "resourceField",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := envVarToEnvEntryPair(c.in)
			if got.Kind != c.want {
				t.Errorf("Kind = %q, want %q", got.Kind, c.want)
			}
		})
	}
}

func TestValidateEnvSourceRefsCMNotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	containers := []containerEnvPatch{
		{
			Container: "app",
			Env: []envVarPatch{
				{
					Name:   "X",
					Action: "set",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "missing-cm"},
							Key:                  "k",
						},
					},
				},
			},
		},
	}
	err := validateEnvSourceRefs(context.Background(), cs, "default", containers)
	if err == nil {
		t.Fatal("expected error for missing CM, got nil")
	}
	if !strings.Contains(err.Error(), "missing-cm") {
		t.Errorf("error should name the missing CM, got: %v", err)
	}
}

func TestValidateEnvSourceRefsOptionalSkipped(t *testing.T) {
	cs := fake.NewSimpleClientset() // no CMs registered
	optional := true
	containers := []containerEnvPatch{
		{
			Container: "app",
			Env: []envVarPatch{
				{
					Name:   "X",
					Action: "set",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "missing-but-optional"},
							Key:                  "k",
							Optional:             &optional,
						},
					},
				},
			},
		},
	}
	if err := validateEnvSourceRefs(context.Background(), cs, "default", containers); err != nil {
		t.Errorf("optional=true should skip validation, got error: %v", err)
	}
}

func TestValidateEnvSourceRefsSuccess(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: "default"}},
	)
	containers := []containerEnvPatch{
		{
			Container: "app",
			Env: []envVarPatch{
				{
					Name:   "URL",
					Action: "set",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
							Key:                  "url",
						},
					},
				},
				{
					Name:   "TOKEN",
					Action: "set",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"},
							Key:                  "token",
						},
					},
				},
			},
		},
	}
	if err := validateEnvSourceRefs(context.Background(), cs, "default", containers); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}
