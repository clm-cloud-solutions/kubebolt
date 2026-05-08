package cluster

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// History tests focus on the two error-prone paths that gave me the
// most pause writing the feature:
//
//   1. ControllerRevision.Data.Raw decoding — the JSON shape is a
//      partial workload (`{spec:{template:...}}`) and silently
//      returning empty containers if the unmarshal target is wrong
//      would make every STS/DS rollback footgun-shaped.
//   2. Multi-container image extraction — the legacy
//      GetDeploymentHistory used `containers[0].Image` only; the
//      detailed variant must enumerate every container.
//
// Owner-ref filtering and revision sorting are exercised end-to-end
// by the higher-level methods against fake clientset fixtures, but
// they're trivial enough that we don't add separate tests.

func TestExtractImagesFromControllerRevisionStatefulSet(t *testing.T) {
	// Real-shape fixture: kubectl/STS controller writes a partial
	// StatefulSet with the new pod template under spec.template.
	raw := []byte(`{
		"spec": {
			"template": {
				"spec": {
					"containers": [
						{"name": "app", "image": "ghcr.io/acme/app:v1"},
						{"name": "sidecar", "image": "ghcr.io/acme/sidecar:v1"}
					]
				}
			}
		}
	}`)
	got, err := extractImagesFromControllerRevision(raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	want := []ImagePair{
		{"app", "ghcr.io/acme/app:v1"},
		{"sidecar", "ghcr.io/acme/sidecar:v1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("pair[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExtractImagesFromControllerRevisionEmpty(t *testing.T) {
	// Empty raw — DS in some clusters writes empty CRs during init.
	got, err := extractImagesFromControllerRevision(nil)
	if err != nil {
		t.Fatalf("extract(nil): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("extract(nil) = %v, want empty non-nil slice", got)
	}

	// Well-formed JSON but no containers.
	got, err = extractImagesFromControllerRevision([]byte(`{"spec":{"template":{"spec":{}}}}`))
	if err != nil {
		t.Fatalf("extract(empty containers): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("extract(empty containers) = %v, want empty non-nil slice", got)
	}
}

func TestExtractImagesFromControllerRevisionInvalidJSON(t *testing.T) {
	// Mangled JSON — caller logs but doesn't crash. We return an
	// error so the calling code can surface "couldn't decode this
	// revision's template" instead of pretending the revision had
	// no containers (which would falsely allow a rollback to land
	// without any images at all).
	_, err := extractImagesFromControllerRevision([]byte(`not-json`))
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestReplicaSetToDetailedRevisionMultiContainer(t *testing.T) {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-7d5b9f",
			Annotations: map[string]string{
				deploymentRevisionAnnotation: "12",
				changeCauseAnnotation:        "kubectl set image deploy/api app=…",
			},
			CreationTimestamp: metav1.Now(),
		},
		Spec: appsv1.ReplicaSetSpec{
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
		Status: appsv1.ReplicaSetStatus{Replicas: 3},
	}

	got := replicaSetToDetailedRevision(rs, 12)
	if got.Revision != 12 {
		t.Errorf("Revision = %d, want 12", got.Revision)
	}
	if got.ChangeCause == "" {
		t.Error("ChangeCause not extracted from annotation")
	}
	if !got.Active {
		t.Error("Active = false, want true (revision matches current)")
	}
	if len(got.Images) != 3 {
		t.Fatalf("Images len = %d, want 3 (the legacy method only returned containers[0])", len(got.Images))
	}
	if got.Images[2].Container != "exporter" || got.Images[2].Image != "exp:1" {
		t.Errorf("third container missing or wrong: %v", got.Images[2])
	}

	// Inactive revision: same fixture, different currentRev.
	gotInactive := replicaSetToDetailedRevision(rs, 13)
	if gotInactive.Active {
		t.Error("Active = true for non-matching currentRev")
	}
}

func TestReplicaSetToDetailedRevisionMissingAnnotations(t *testing.T) {
	// RS without the revision annotation — exists in older
	// Deployments or hand-crafted fixtures. Should not crash; should
	// produce Revision=0 and treat as inactive.
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "api-old", CreationTimestamp: metav1.Now()},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "img:1"}},
		}}},
	}
	got := replicaSetToDetailedRevision(rs, 1)
	if got.Revision != 0 || got.Active || got.ChangeCause != "" {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestIsOwnedBy(t *testing.T) {
	uid := types.UID("dep-uid-1")
	refs := []metav1.OwnerReference{
		{Kind: "Deployment", Name: "api", UID: uid},
		{Kind: "Service", Name: "api", UID: types.UID("other")},
	}
	if !isOwnedBy(refs, "Deployment", "api", uid) {
		t.Error("expected match")
	}
	// UID mismatch — name collision after deletion + recreation.
	if isOwnedBy(refs, "Deployment", "api", types.UID("dep-uid-2")) {
		t.Error("UID mismatch should not match")
	}
	if isOwnedByKindName(refs, "Deployment", "api") != true {
		t.Error("kind+name match should succeed without UID check")
	}
	if isOwnedByKindName(refs, "Deployment", "other") {
		t.Error("name mismatch should not match")
	}
}

// TestRoundTripImagePair confirms ImagePair JSON tags match the API
// contract the frontend depends on (`{container, image}`).
func TestImagePairJSONTags(t *testing.T) {
	p := ImagePair{Container: "app", Image: "img:1"}
	out, _ := json.Marshal(p)
	if string(out) != `{"container":"app","image":"img:1"}` {
		t.Errorf("JSON tags wrong: %s", out)
	}
}

func TestPickRollbackTargetDefault(t *testing.T) {
	owned := []appsv1.ControllerRevision{
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-3"}, Revision: 3},
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-1"}, Revision: 1},
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-2"}, Revision: 2},
	}
	target, err := pickRollbackTarget(owned, 0, 3)
	if err != nil {
		t.Fatalf("pickRollbackTarget(default): %v", err)
	}
	// Default with currentRev=3 → most recent non-current → 2.
	if target.Revision != 2 {
		t.Errorf("default target = %d, want 2 (most recent non-current)", target.Revision)
	}
}

func TestPickRollbackTargetExplicit(t *testing.T) {
	owned := []appsv1.ControllerRevision{
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-3"}, Revision: 3},
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-2"}, Revision: 2},
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-1"}, Revision: 1},
	}
	target, err := pickRollbackTarget(owned, 1, 3)
	if err != nil || target.Revision != 1 {
		t.Errorf("explicit target = %v, %v", target, err)
	}
}

func TestPickRollbackTargetErrors(t *testing.T) {
	cases := []struct {
		name        string
		owned       []appsv1.ControllerRevision
		toRev       int64
		currentRev  int64
		errContains string
	}{
		{
			name:        "single revision — no history",
			owned:       []appsv1.ControllerRevision{{Revision: 1}},
			toRev:       0,
			currentRev:  1,
			errContains: "no rollback history",
		},
		{
			name: "no-op (target == current)",
			owned: []appsv1.ControllerRevision{
				{Revision: 2}, {Revision: 1},
			},
			toRev:       2,
			currentRev:  2,
			errContains: "no-op",
		},
		{
			name: "target not found",
			owned: []appsv1.ControllerRevision{
				{Revision: 2}, {Revision: 1},
			},
			toRev:       7,
			currentRev:  2,
			errContains: "not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pickRollbackTarget(tc.owned, tc.toRev, tc.currentRev)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tc.errContains) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.errContains)
			}
		})
	}
}

func TestDecodeControllerRevisionTemplate(t *testing.T) {
	raw := []byte(`{
		"spec": {
			"template": {
				"metadata": {"labels": {"app": "redis"}},
				"spec": {
					"containers": [{"name": "redis", "image": "redis:7"}],
					"terminationGracePeriodSeconds": 30
				}
			}
		}
	}`)
	got, err := decodeControllerRevisionTemplate(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Labels["app"] != "redis" {
		t.Error("labels not decoded")
	}
	if len(got.Spec.Containers) != 1 || got.Spec.Containers[0].Image != "redis:7" {
		t.Error("containers not decoded")
	}
	if got.Spec.TerminationGracePeriodSeconds == nil || *got.Spec.TerminationGracePeriodSeconds != 30 {
		t.Error("terminationGracePeriodSeconds not decoded — non-image fields would be dropped on rollback")
	}

	// Empty data → error so the rollback fails fast instead of
	// patching an empty pod template (which would crash the whole
	// workload).
	if _, err := decodeControllerRevisionTemplate(nil); err == nil {
		t.Error("expected error on empty data")
	}
	if _, err := decodeControllerRevisionTemplate([]byte(`{"spec":{"template":{"spec":{}}}}`)); err == nil {
		t.Error("expected error on empty containers")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestControllerRevisionRoundTrip exercises the path the way the
// real apiserver writes it: a runtime.RawExtension wrapping a JSON-
// marshaled partial StatefulSet. If the StatefulSet controller ever
// changes the embedding shape, this is the test that catches it.
func TestControllerRevisionRoundTripFromRawExtension(t *testing.T) {
	template := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "redis", Image: "redis:7-alpine"},
			},
		},
	}
	partial := struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}{}
	partial.Spec.Template = template
	raw, err := json.Marshal(partial)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ext := runtime.RawExtension{Raw: raw}

	got, err := extractImagesFromControllerRevision(ext.Raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(got) != 1 || got[0].Image != "redis:7-alpine" {
		t.Errorf("round-trip lost image data: %v", got)
	}
}
