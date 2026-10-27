package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// pvc-pending: the class the claim resolves to (binding mode, provisioner, or
// why there is none) and the pods that mount it — on every branch.
func TestPVCPending_EvidenceCarriesClassAndConsumers(t *testing.T) {
	immediate := storagev1.VolumeBindingImmediate
	class := "fast"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &class},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	user := pod("db", "pg-0")
	user.Status.Phase = corev1.PodPending
	user.Spec.NodeName = "node-1"
	user.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
	state := &ClusterState{
		PVCs:           []*corev1.PersistentVolumeClaim{pvc},
		Pods:           []*corev1.Pod{user},
		StorageClasses: []*storagev1.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Provisioner: "ebs.csi.aws.com", VolumeBindingMode: &immediate}},
	}
	got := pvcPendingRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "StorageClass").Detail; d != "fast (Immediate, provisioner ebs.csi.aws.com)" {
		t.Errorf("class: %q", d)
	}
	if d := evidenceByLabel(t, got[0], "Mounted by").Detail; d != "pg-0 (Pending)" {
		t.Errorf("mounted by: %q", d)
	}

	state.StorageClasses = nil
	got = pvcPendingRule().Evaluate(state)
	if d := evidenceByLabel(t, got[0], "StorageClass").Detail; d != "fast — does not exist" {
		t.Errorf("missing class: %q", d)
	}
}
