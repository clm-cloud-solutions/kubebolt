package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// Finding #43. The WebSocket used to carry the raw informer object, so every
// Secret in the cluster was pushed to every authenticated browser in full — on
// every change and on every 30s resync. Nothing about the secret may survive
// into the broadcast payload.
func TestResourceRef_SecretContentsNeverReachTheWire(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.trivy-operator.v2",
			Namespace: "kubebolt-system",
			UID:       "uid-123",
		},
		Data: map[string][]byte{
			"release":  []byte("SUPER-SECRET-RELEASE-BLOB"),
			"password": []byte("hunter2"),
		},
		StringData: map[string]string{"token": "ANOTHER-SECRET"},
		Type:       corev1.SecretTypeOpaque,
	}

	raw, err := json.Marshal(resourceRef(secret))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := string(raw)

	for _, leak := range []string{"SUPER-SECRET-RELEASE-BLOB", "hunter2", "ANOTHER-SECRET", "stringData", "\"data\""} {
		if strings.Contains(payload, leak) {
			t.Errorf("broadcast payload leaks %q: %s", leak, payload)
		}
	}
	// And it must still carry what the client actually uses.
	for _, want := range []string{"kubebolt-system", "sh.helm.release.v1.trivy-operator.v2", "Secret"} {
		if !strings.Contains(payload, want) {
			t.Errorf("broadcast payload lost %q: %s", want, payload)
		}
	}
}

// The deployed frontend reads payload.data.metadata.namespace / .name. The shape
// must not change, or a backend-only rollout breaks every browser.
func TestResourceRef_KeepsTheShapeTheFrontendReads(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "default"}}
	ref := resourceRef(pod)

	md, ok := ref["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata missing or wrong type: %+v", ref)
	}
	if md["namespace"] != "default" || md["name"] != "api-1" {
		t.Errorf("metadata = %+v, want namespace=default name=api-1", md)
	}
	if ref["kind"] != "Pod" {
		t.Errorf("kind = %v, want Pod", ref["kind"])
	}
}

// Deletes can arrive wrapped when the watch missed the final state; the ref must
// unwrap rather than emit an empty notification.
func TestResourceRef_UnwrapsTombstone(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: "ns1"}}
	ref := resourceRef(cache.DeletedFinalStateUnknown{Key: "ns1/gone", Obj: pod})

	md := ref["metadata"].(map[string]interface{})
	if md["name"] != "gone" || md["namespace"] != "ns1" {
		t.Errorf("tombstone not unwrapped: %+v", ref)
	}
	if ref["kind"] != "Pod" {
		t.Errorf("kind = %v, want Pod", ref["kind"])
	}
}

// A non-Kubernetes object must not panic or drop the event.
func TestResourceRef_NonK8sObjectDegradesGracefully(t *testing.T) {
	ref := resourceRef("not an object")
	if ref == nil {
		t.Fatal("nil ref")
	}
	if _, ok := ref["metadata"].(map[string]interface{}); !ok {
		t.Errorf("metadata missing: %+v", ref)
	}
}
