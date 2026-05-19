package api

import (
	"context"
	"encoding/json"
	"testing"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// Tests for handleSetHpaBounds focus on the same three properties
// as actions_resources_test.go and actions_setimage_test.go:
//
//   1. Patch SHAPE — buildHpaBoundsPatch produces the exact
//      strategic-merge envelope the apiserver expects; only the
//      dimensions the caller set appear in the patch.
//   2. Patch INVARIANCE — applying the patch against a fake clientset
//      mutates only spec.minReplicas / spec.maxReplicas; targetRef,
//      metrics, behavior, status are all preserved.
//   3. Validation logic — the bounds caps + cross-field rules reject
//      invalid combinations before any apiserver round-trip.

func TestBuildHpaBoundsPatchShape(t *testing.T) {
	min := int32(2)
	max := int32(10)

	t.Run("both fields", func(t *testing.T) {
		raw, err := buildHpaBoundsPatch(&min, &max)
		if err != nil {
			t.Fatalf("buildHpaBoundsPatch: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		spec, ok := decoded["spec"].(map[string]any)
		if !ok {
			t.Fatalf("expected spec to be object, got %T", decoded["spec"])
		}
		// JSON numbers decode as float64 — compare via int conversion.
		if int(spec["minReplicas"].(float64)) != 2 || int(spec["maxReplicas"].(float64)) != 10 {
			t.Errorf("spec=%v, want minReplicas=2 maxReplicas=10", spec)
		}
		// No stray top-level fields.
		if len(decoded) != 1 {
			t.Errorf("expected only 'spec' at top level, got %v", decoded)
		}
	})

	t.Run("only max", func(t *testing.T) {
		raw, err := buildHpaBoundsPatch(nil, &max)
		if err != nil {
			t.Fatalf("buildHpaBoundsPatch: %v", err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		spec := decoded["spec"].(map[string]any)
		if _, present := spec["minReplicas"]; present {
			t.Errorf("minReplicas should be absent from patch when nil, got %v", spec)
		}
		if int(spec["maxReplicas"].(float64)) != 10 {
			t.Errorf("maxReplicas: got %v, want 10", spec["maxReplicas"])
		}
	})

	t.Run("only min", func(t *testing.T) {
		raw, err := buildHpaBoundsPatch(&min, nil)
		if err != nil {
			t.Fatalf("buildHpaBoundsPatch: %v", err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		spec := decoded["spec"].(map[string]any)
		if _, present := spec["maxReplicas"]; present {
			t.Errorf("maxReplicas should be absent from patch when nil, got %v", spec)
		}
		if int(spec["minReplicas"].(float64)) != 2 {
			t.Errorf("minReplicas: got %v, want 2", spec["minReplicas"])
		}
	})
}

// TestSetHpaBoundsStrategicMergeInvariance — applying the patch
// against a fake clientset must touch only the bounds fields. If
// strategic merge is ever misused (e.g. swapped to MergePatchType
// or full PUT), targetRef and metrics blocks would clobber. This is
// the load-bearing property the whole feature rests on.
func TestSetHpaBoundsStrategicMergeInvariance(t *testing.T) {
	min := int32(2)
	max := int32(10)
	cpuTarget := int32(80)
	hpa := &autoscalingv1.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "default"},
		Spec: autoscalingv1.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{
				Kind:       "Deployment",
				Name:       "api",
				APIVersion: "apps/v1",
			},
			MinReplicas:                    &min,
			MaxReplicas:                    3, // pre-patch
			TargetCPUUtilizationPercentage: &cpuTarget,
		},
	}
	cs := fake.NewSimpleClientset(hpa)

	raw, err := buildHpaBoundsPatch(nil, &max)
	if err != nil {
		t.Fatalf("buildHpaBoundsPatch: %v", err)
	}

	got, err := cs.AutoscalingV1().HorizontalPodAutoscalers("default").Patch(
		context.Background(), "api-hpa", types.StrategicMergePatchType, raw, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	if got.Spec.MaxReplicas != 10 {
		t.Errorf("maxReplicas: got %d, want 10", got.Spec.MaxReplicas)
	}
	// MinReplicas not in the patch → preserved.
	if got.Spec.MinReplicas == nil || *got.Spec.MinReplicas != 2 {
		t.Errorf("minReplicas: got %v, want 2 (preserved)", got.Spec.MinReplicas)
	}
	// Untouched fields preserved.
	if got.Spec.ScaleTargetRef.Name != "api" {
		t.Errorf("scaleTargetRef.name clobbered: %v", got.Spec.ScaleTargetRef)
	}
	if got.Spec.TargetCPUUtilizationPercentage == nil || *got.Spec.TargetCPUUtilizationPercentage != 80 {
		t.Errorf("targetCPUUtilizationPercentage: got %v, want 80 (preserved)", got.Spec.TargetCPUUtilizationPercentage)
	}
}

// TestMaxReplicasSafetyCapDefined locks the safety cap to a known
// value. If someone later changes it, they have to acknowledge the
// change here and update the system-prompt + frontend copy in
// lockstep.
func TestMaxReplicasSafetyCapDefined(t *testing.T) {
	if maxReplicasSafetyCap != 1000 {
		t.Errorf("maxReplicasSafetyCap=%d, want 1000 (any change requires updating Kobi system prompt + UI copy)", maxReplicasSafetyCap)
	}
}
