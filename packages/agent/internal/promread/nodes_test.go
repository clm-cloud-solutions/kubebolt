package promread

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func makeNode(name string, internalIP string, extra ...corev1.NodeAddress) *corev1.Node {
	addrs := make([]corev1.NodeAddress, 0, 1+len(extra))
	if internalIP != "" {
		addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: internalIP})
	}
	addrs = append(addrs, extra...)
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}
}

func TestK8sNodeIndex_RefreshBuildsMap(t *testing.T) {
	client := fake.NewSimpleClientset(
		makeNode("worker-a", "10.0.0.10"),
		makeNode("worker-b", "10.0.0.11"),
	)
	idx := NewK8sNodeIndex(client, 0)
	if err := idx.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := idx.NodeByIP("10.0.0.10"); got != "worker-a" {
		t.Errorf("NodeByIP(10.0.0.10) = %q, want worker-a", got)
	}
	if got := idx.NodeByIP("10.0.0.11"); got != "worker-b" {
		t.Errorf("NodeByIP(10.0.0.11) = %q, want worker-b", got)
	}
	if idx.Size() != 2 {
		t.Errorf("Size: got %d want 2", idx.Size())
	}
}

func TestK8sNodeIndex_UnknownIPReturnsEmpty(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("worker", "10.0.0.1"))
	idx := NewK8sNodeIndex(client, 0)
	_ = idx.Refresh(context.Background())
	if got := idx.NodeByIP("192.168.99.99"); got != "" {
		t.Errorf("unknown IP should return empty, got %q", got)
	}
}

func TestK8sNodeIndex_IgnoresNonInternalIP(t *testing.T) {
	// Nodes often have ExternalIP and Hostname too — the index should
	// only key off InternalIP, otherwise lookups against the wrong
	// address type would return false matches.
	client := fake.NewSimpleClientset(makeNode("worker", "10.0.0.5",
		corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
		corev1.NodeAddress{Type: corev1.NodeHostName, Address: "worker.example.com"},
	))
	idx := NewK8sNodeIndex(client, 0)
	_ = idx.Refresh(context.Background())
	if idx.NodeByIP("203.0.113.5") != "" {
		t.Error("ExternalIP should not be in the IP→name map")
	}
	if idx.NodeByIP("worker.example.com") != "" {
		t.Error("Hostname address should not be in the IP→name map")
	}
	if idx.NodeByIP("10.0.0.5") != "worker" {
		t.Error("InternalIP should be in the map")
	}
}

func TestK8sNodeIndex_RefreshReplacesMap(t *testing.T) {
	// A node removed from the cluster should disappear from the map
	// on next Refresh — stale entries would map IPs to dead nodes.
	client := fake.NewSimpleClientset(
		makeNode("worker-a", "10.0.0.10"),
		makeNode("worker-b", "10.0.0.11"),
	)
	idx := NewK8sNodeIndex(client, 0)
	_ = idx.Refresh(context.Background())

	// Simulate worker-b removal.
	if err := client.CoreV1().Nodes().Delete(context.Background(), "worker-b", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := idx.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if idx.NodeByIP("10.0.0.11") != "" {
		t.Error("worker-b IP should be gone after refresh")
	}
	if idx.NodeByIP("10.0.0.10") != "worker-a" {
		t.Error("worker-a should still be present")
	}
}

func TestK8sNodeIndex_IsKnownNode(t *testing.T) {
	client := fake.NewSimpleClientset(
		makeNode("worker-a", "10.0.0.10"),
		makeNode("worker-b", "10.0.0.11"),
		// node with NO InternalIP — only in nameSet, not in ipToName.
		// Azure VMSS-style names sometimes don't expose InternalIP if
		// the node-exporter is host-network on a different interface;
		// IsKnownNode must still match against .metadata.name.
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "aks-nodepool1-XXX-vmss000000"}},
	)
	idx := NewK8sNodeIndex(client, 0)
	_ = idx.Refresh(context.Background())

	cases := []struct {
		name string
		want bool
	}{
		{"worker-a", true},
		{"worker-b", true},
		{"aks-nodepool1-XXX-vmss000000", true},
		{"not-a-node", false},
		{"", false},
		{"10.0.0.10", false}, // IPs are not names
	}
	for _, tc := range cases {
		if got := idx.IsKnownNode(tc.name); got != tc.want {
			t.Errorf("IsKnownNode(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestK8sNodeIndex_IsKnownNodeRefreshReplacesSet(t *testing.T) {
	// A node removed from the cluster must disappear from IsKnownNode
	// on next Refresh — symmetric to the ipToName invalidation.
	client := fake.NewSimpleClientset(
		makeNode("worker-a", "10.0.0.10"),
		makeNode("worker-b", "10.0.0.11"),
	)
	idx := NewK8sNodeIndex(client, 0)
	_ = idx.Refresh(context.Background())

	if err := client.CoreV1().Nodes().Delete(context.Background(), "worker-b", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = idx.Refresh(context.Background())

	if idx.IsKnownNode("worker-b") {
		t.Error("worker-b should be gone from IsKnownNode after refresh")
	}
	if !idx.IsKnownNode("worker-a") {
		t.Error("worker-a should still be present")
	}
}

func TestStripPort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"172.18.0.4:9100", "172.18.0.4"},
		{"10.0.0.1:80", "10.0.0.1"},
		{"no-port", "no-port"},
		{"", ""},
		{":9100", ":9100"}, // edge case — LastIndex at 0 returns unchanged
	}
	for _, tc := range cases {
		if got := StripPort(tc.in); got != tc.want {
			t.Errorf("StripPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
