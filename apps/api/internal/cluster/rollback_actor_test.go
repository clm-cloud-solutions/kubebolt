package cluster

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/fieldmanager"
)

// Finding #69: a rollback writes under the field manager of whoever asked for
// it, and — as before — a caller that goes away does not abort it.
func TestRollbackDeployment_WritesUnderTheCallersActor(t *testing.T) {
	fixture := func() *fake.Clientset {
		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: "shop", UID: "d1",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"},
		}}
		rs := func(rev, image string) *appsv1.ReplicaSet {
			return &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-" + rev, Namespace: "shop",
					Annotations:     map[string]string{"deployment.kubernetes.io/revision": rev},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", UID: "d1"}},
				},
				Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: image}}},
				}},
			}
		}
		return fake.NewSimpleClientset(dep, rs("1", "web:1"), rs("2", "web:2"))
	}
	updateManager := func(cs *fake.Clientset) string {
		for _, a := range cs.Actions() {
			if u, ok := a.(k8stesting.UpdateActionImpl); ok {
				return u.UpdateOptions.FieldManager
			}
		}
		t.Fatal("no update recorded")
		return ""
	}

	cs := fixture()
	c := &Connector{clientset: cs}
	ctx := fieldmanager.WithActor(context.Background(), fieldmanager.ActorAutopilot)
	if _, _, err := c.RollbackDeployment(ctx, "shop", "web", 0, false); err != nil {
		t.Fatalf("RollbackDeployment: %v", err)
	}
	if got := updateManager(cs); got != "kubebolt-autopilot" {
		t.Errorf("field manager %q, want kubebolt-autopilot", got)
	}

	cs = fixture()
	c = &Connector{clientset: cs}
	cancelled, cancel := context.WithCancel(fieldmanager.WithActor(context.Background(), fieldmanager.ActorKobi))
	cancel()
	if _, _, err := c.RollbackDeployment(cancelled, "shop", "web", 0, false); err != nil {
		t.Fatalf("RollbackDeployment with the caller gone: %v", err)
	}
	if got := updateManager(cs); got != "kubebolt-kobi" {
		t.Errorf("field manager %q, want kubebolt-kobi", got)
	}
}
