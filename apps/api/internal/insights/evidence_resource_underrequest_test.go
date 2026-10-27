package insights

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// resource-underrequest: the request, the usage and the bar. Thresholds are
// tunable per org, and the message used to say "40%" whatever the bar was.
func TestResourceUnderrequest_EvidenceRecordsTheBar(t *testing.T) {
	old := sustainedMetricWindow
	sustainedMetricWindow = 0
	defer func() { sustainedMetricWindow = old }()
	p := pod("agents", "agent-0")
	p.Spec.Containers = []corev1.Container{{Name: "a", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")}}}}
	state := &ClusterState{
		Pods:       []*corev1.Pod{p},
		PodMetrics: map[string]*models.MetricPoint{"agents/agent-0": {CPUUsage: 31}},
		Policies:   &PolicySnapshot{Thresholds: map[string]float64{"resource-underrequest": 0.5}},
	}
	got := resourceUnderrequestRule().Evaluate(state)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if d := evidenceByLabel(t, got[0], "CPU").Detail; d != "request 10m, usage 31m: the request is 32% of usage (fires under 50%)" {
		t.Errorf("cpu: %q", d)
	}
	if want := "less than 50% of actual usage"; !strings.Contains(got[0].Message, want) {
		t.Errorf("message %q does not carry the policy's bar", got[0].Message)
	}
}
