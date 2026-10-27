package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// cpu-throttle-risk: usage, the limit and the bar.
func TestCPUThrottleRisk_EvidenceRecordsUsageLimitAndBar(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()
	state := &ClusterState{
		Pods:       []*corev1.Pod{cpuLimitPod("lab", "spin", "50m")},
		PodMetrics: map[string]*models.MetricPoint{"lab/spin": {CPUUsage: 49}},
	}
	got := cpuThrottleRiskRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "CPU").Detail; d != "49m of a 50m limit, 98% (fires above 80%)" {
		t.Errorf("cpu: %q", d)
	}
}
