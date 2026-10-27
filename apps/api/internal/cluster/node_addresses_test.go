package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A dependency at 172.18.0.5 next to nodes at .2 and .3: two models answered
// "I could not check whether it is a node, the node list does not show its
// IPs". The list now carries the addresses kubectl shows.
func TestNodeToMap_CarriesTheNodeAddresses(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "worker"},
			{Type: corev1.NodeInternalIP, Address: "172.18.0.2"},
			{Type: corev1.NodeInternalIP, Address: "fc00::2"},
			{Type: corev1.NodeExternalIP, Address: "203.0.113.7"},
		}},
	}
	m := nodeToMap(node)
	if got := m["internalIP"]; got != "172.18.0.2" {
		t.Errorf("internalIP = %v, want the first InternalIP, as kubectl shows it", got)
	}
	if got := m["externalIP"]; got != "203.0.113.7" {
		t.Errorf("externalIP = %v, want 203.0.113.7", got)
	}
}

func TestNodeToMap_NoAddressIsEmptyNotMissing(t *testing.T) {
	m := nodeToMap(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "bare"}})
	for _, k := range []string{"internalIP", "externalIP"} {
		v, ok := m[k]
		if !ok || v != "" {
			t.Errorf("%s = %v (present %v), want an empty string", k, v, ok)
		}
	}
}
