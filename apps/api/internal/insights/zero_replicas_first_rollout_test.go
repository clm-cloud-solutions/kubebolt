package insights

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// zeroDeploy is a Deployment with one desired replica and none available,
// created `age` ago. Its conditions are what each case adds.
func zeroDeploy(age time.Duration, conds ...appsv1.DeploymentCondition) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "api",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Spec: appsv1.DeploymentSpec{Replicas: i32(1)},
	}
	d.Status.Conditions = conds
	return d
}

func cond(t appsv1.DeploymentConditionType, s corev1.ConditionStatus, reason string, at time.Time) appsv1.DeploymentCondition {
	return appsv1.DeploymentCondition{
		Type: t, Status: s, Reason: reason,
		LastTransitionTime: metav1.NewTime(at), LastUpdateTime: metav1.NewTime(at),
	}
}

func zeroReplicasFires(d *appsv1.Deployment) bool {
	return len(zeroReplicasRule().Evaluate(&ClusterState{Deployments: []*appsv1.Deployment{d}})) > 0
}

// The controller's first sync writes Available=False and Progressing=True at
// the Deployment's birth. While the first pods start, zero available is not an
// outage: the insight stays closed. (KubeBolt's own install opened a critical
// one on its OpenCost Deployment, for a minute.)
func TestZeroReplicas_FirstRolloutIsNotAnOutage(t *testing.T) {
	d := zeroDeploy(40 * time.Second)
	born := d.CreationTimestamp.Time
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", born.Add(time.Second)),
		cond(appsv1.DeploymentProgressing, corev1.ConditionTrue, "ReplicaSetUpdated", born.Add(time.Second)),
	}
	if zeroReplicasFires(d) {
		t.Fatal("a Deployment whose first pods are starting fired zero-replicas")
	}
}

// Seconds old, not synced yet: no conditions at all. Same answer, until
// bornTolerance says no controller is working on it.
func TestZeroReplicas_UnsyncedDeploymentWaitsOnlyForBornTolerance(t *testing.T) {
	if zeroReplicasFires(zeroDeploy(5 * time.Second)) {
		t.Error("a seconds-old Deployment with no conditions yet fired")
	}
	if !zeroReplicasFires(zeroDeploy(10 * time.Minute)) {
		t.Error("a Deployment nobody synced for 10 minutes stayed silent")
	}
}

// The controller ends the wait: past progressDeadlineSeconds it sets
// Progressing=False and the zero is an outage again.
func TestZeroReplicas_FirstRolloutPastItsDeadlineFires(t *testing.T) {
	d := zeroDeploy(11 * time.Minute)
	born := d.CreationTimestamp.Time
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", born.Add(time.Second)),
		cond(appsv1.DeploymentProgressing, corev1.ConditionFalse, "ProgressDeadlineExceeded", born.Add(10*time.Minute)),
	}
	if !zeroReplicasFires(d) {
		t.Fatal("a first rollout past its progress deadline did not fire")
	}
}

// A quota refusing the pods is no rollout in progress: fire at once, the
// ReplicaFailure condition names the cause.
func TestZeroReplicas_FirstRolloutRefusedByQuotaFires(t *testing.T) {
	d := zeroDeploy(30 * time.Second)
	born := d.CreationTimestamp.Time
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", born.Add(time.Second)),
		cond(appsv1.DeploymentProgressing, corev1.ConditionTrue, "ReplicaSetUpdated", born.Add(time.Second)),
		cond(appsv1.DeploymentReplicaFailure, corev1.ConditionTrue, "FailedCreate", born.Add(2*time.Second)),
	}
	if !zeroReplicasFires(d) {
		t.Fatal("a first rollout refused by a quota stayed silent")
	}
}

// The case that must keep firing: a Deployment that was available for days
// and lost its only pod (a production service, its single replica
// deleted mid-rollout). Available went True→False just now, long after birth.
func TestZeroReplicas_LosingEveryPodAfterBeingAvailableFires(t *testing.T) {
	d := zeroDeploy(72 * time.Hour)
	now := time.Now()
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", now.Add(-20*time.Second)),
		cond(appsv1.DeploymentProgressing, corev1.ConditionTrue, "ReplicaSetUpdated", now.Add(-5*time.Minute)),
	}
	if !zeroReplicasFires(d) {
		t.Fatal("a Deployment that lost every pod after being available stayed silent")
	}
}

// A Deployment that completed its first rollout and lost its only pod 40 s
// after birth — inside bornTolerance. The shape is the one a kind cluster
// wrote for exactly that: Progressing keeps NewReplicaSetAvailable, and that
// is what tells it from a first rollout.
func TestZeroReplicas_YoungDeploymentThatWasAvailableFires(t *testing.T) {
	d := zeroDeploy(46 * time.Second)
	born := d.CreationTimestamp.Time
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentProgressing, corev1.ConditionTrue, "NewReplicaSetAvailable", born),
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", born.Add(40*time.Second)),
	}
	if !zeroReplicasFires(d) {
		t.Fatal("a Deployment that lost its pods 40 s after its first rollout completed stayed silent")
	}
}

// Same, with a later loss: the Available transition alone already says it.
func TestZeroReplicas_LossPastBornToleranceFires(t *testing.T) {
	d := zeroDeploy(5 * time.Minute)
	born := d.CreationTimestamp.Time
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", born.Add(4*time.Minute)),
		cond(appsv1.DeploymentProgressing, corev1.ConditionTrue, "ReplicaSetUpdated", born),
	}
	if !zeroReplicasFires(d) {
		t.Fatal("a Deployment that lost its pods four minutes after birth stayed silent")
	}
}

// A paused Deployment never progresses, so nothing would end the wait.
func TestZeroReplicas_PausedDeploymentFires(t *testing.T) {
	d := zeroDeploy(30 * time.Second)
	born := d.CreationTimestamp.Time
	d.Spec.Paused = true
	d.Status.Conditions = []appsv1.DeploymentCondition{
		cond(appsv1.DeploymentAvailable, corev1.ConditionFalse, "MinimumReplicasUnavailable", born.Add(time.Second)),
		cond(appsv1.DeploymentProgressing, corev1.ConditionUnknown, "DeploymentPaused", born.Add(time.Second)),
	}
	if !zeroReplicasFires(d) {
		t.Fatal("a paused Deployment with zero available stayed silent")
	}
}
