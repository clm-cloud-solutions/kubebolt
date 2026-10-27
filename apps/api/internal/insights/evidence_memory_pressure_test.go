package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// memory-pressure ("Memory Near Limit"): usage, the pod's own limit and the bar.
func TestMemoryNearLimit_EvidenceRecordsUsageLimitAndBar(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()
	state := &ClusterState{
		Pods:       []*corev1.Pod{limitedPod("shop", "api", "100Mi", "100Mi")},
		PodMetrics: map[string]*models.MetricPoint{"shop/api": {MemUsage: 190 << 20}},
	}
	got := memoryPressureRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "Memory").Detail; d != "190Mi of a 200Mi limit, 95% (fires above 85%)" {
		t.Errorf("memory: %q", d)
	}
}
