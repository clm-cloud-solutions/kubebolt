package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// missing-config-dependency: what the kubelet reported, and the reference it
// names — an object that does not exist, or a key missing from one that does.
func TestMissingConfigDependency_EvidenceNamesTheReference(t *testing.T) {
	cases := map[string]string{
		`configmap "pricing-settings" not found`:                         "ConfigMap pricing-settings — does not exist",
		`couldn't find key DATABASE_URL in Secret shop/db`:               "key DATABASE_URL in Secret shop/db — the object exists, the key does not",
		`container has runAsNonRoot and image will run as root (pod: x)`: "",
	}
	for msg, want := range cases {
		got := missingConfigDependencyRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{configErrorPod(msg)}})
		if len(got) != 1 {
			t.Fatalf("%q: %+v", msg, got)
		}
		var ref string
		for _, e := range got[0].Evidence {
			if e.Label == "Reference" {
				ref = e.Detail
			}
		}
		if ref != want {
			t.Errorf("%q: reference %q, want %q", msg, ref, want)
		}
	}
}
