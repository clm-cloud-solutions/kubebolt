package cluster

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The shape that misled a postmortem: Progressing has been True since an old
// rollout (transition), while its message was rewritten for the current one
// (update). Both times must survive, and must not be swapped.
func TestDeploymentConditionsToSlice_KeepsBothTimes(t *testing.T) {
	transition := time.Date(2026, 9, 23, 18, 47, 57, 0, time.UTC)
	update := time.Date(2026, 9, 23, 19, 33, 50, 0, time.UTC)
	got := deploymentConditionsToSlice([]appsv1.DeploymentCondition{{
		Type:               appsv1.DeploymentProgressing,
		Status:             corev1.ConditionTrue,
		Reason:             "ReplicaSetUpdated",
		Message:            `ReplicaSet "image-app-c4558bdb" is progressing.`,
		LastTransitionTime: metav1.NewTime(transition),
		LastUpdateTime:     metav1.NewTime(update),
	}})
	if len(got) != 1 {
		t.Fatalf("got %d conditions", len(got))
	}
	if got[0]["lastUpdateTime"] != "2026-09-23T19:33:50Z" {
		t.Errorf("lastUpdateTime = %v", got[0]["lastUpdateTime"])
	}
	if got[0]["lastTransitionTime"] != "2026-09-23T18:47:57Z" {
		t.Errorf("lastTransitionTime = %v", got[0]["lastTransitionTime"])
	}
}
