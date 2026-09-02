package cluster

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// newUIDConnector builds the smallest Connector that EnsureClusterUID needs:
// a clientset to read through and a stopCh to be cancelled by.
func newUIDConnector(cs *fake.Clientset) *Connector {
	// uidClient is what the resolver reads through in production; wire it here so
	// the test exercises the real path and not the nil-guard fallback.
	return &Connector{clientset: cs, uidClient: cs, stopCh: make(chan struct{}), clusterName: "test"}
}

func kubeSystem(uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}
}

// The happy path costs nothing: a connector that already knows its UID must not
// start a resolver at all.
func TestEnsureClusterUID_NoopWhenAlreadyKnown(t *testing.T) {
	cs := fake.NewSimpleClientset()
	calls := 0
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, errors.New("should not be called")
	})
	c := newUIDConnector(cs)
	c.clusterUID.Store("already-known")

	resolved := make(chan string, 1)
	c.EnsureClusterUID(func(uid string) { resolved <- uid })

	time.Sleep(1500 * time.Millisecond)
	if calls != 0 {
		t.Errorf("a connector that already has its UID must not poll; got %d calls", calls)
	}
	select {
	case uid := <-resolved:
		t.Errorf("onResolved fired for an already-known UID: %q", uid)
	default:
	}
}

// The point of the whole thing: a read that failed at construction must still
// resolve, without anyone restarting the API (finding #46).
func TestEnsureClusterUID_RecoversAfterAFailedRead(t *testing.T) {
	cs := fake.NewSimpleClientset()
	attempts := 0
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		attempts++
		if attempts < 2 {
			return true, nil, errors.New("no agent connected")
		}
		return true, kubeSystem("uid-from-retry"), nil
	})
	c := newUIDConnector(cs)
	defer close(c.stopCh)

	resolved := make(chan string, 1)
	c.EnsureClusterUID(func(uid string) { resolved <- uid })

	select {
	case uid := <-resolved:
		if uid != "uid-from-retry" {
			t.Fatalf("onResolved got %q, want uid-from-retry", uid)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the resolver never recovered — the cluster would stay blind until an API restart")
	}
	if got := c.ClusterUID(); got != "uid-from-retry" {
		t.Errorf("ClusterUID() = %q, want the retried value", got)
	}
}

// It must die with the connector. A resolver that outlives its owner is a
// goroutine leak per torn-down cluster, and this one has no overall deadline.
func TestEnsureClusterUID_StopsWithTheConnector(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("always down")
	})
	c := newUIDConnector(cs)

	done := make(chan struct{})
	c.EnsureClusterUID(func(string) { close(done) })
	close(c.stopCh)

	select {
	case <-done:
		t.Fatal("onResolved fired after shutdown")
	case <-time.After(2500 * time.Millisecond):
		// Still no resolution and no panic: the goroutine returned on stopCh.
	}
}

// A Connector built without uidClient must degrade to the shared one, not panic.
// This runs in a background goroutine, so a nil dereference there would take the
// whole API process down — far out of proportion to a missing field.
func TestEnsureClusterUID_SurvivesAMissingDedicatedClient(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, kubeSystem("uid-via-shared"), nil
	})
	c := &Connector{clientset: cs, stopCh: make(chan struct{}), clusterName: "test"} // no uidClient
	defer close(c.stopCh)

	resolved := make(chan string, 1)
	c.EnsureClusterUID(func(uid string) { resolved <- uid })

	select {
	case uid := <-resolved:
		if uid != "uid-via-shared" {
			t.Fatalf("resolved %q, want uid-via-shared", uid)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the resolver never ran through the shared-client fallback")
	}
}
