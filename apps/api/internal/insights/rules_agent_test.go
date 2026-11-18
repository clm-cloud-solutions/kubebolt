package insights

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

func agentPod(mode, limit, image string) *corev1.Pod {
	c := corev1.Container{Name: "agent", Image: image}
	if limit != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
	}
	if mode != "" {
		c.Env = []corev1.EnvVar{{Name: "KUBEBOLT_AGENT_MODE", Value: mode}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kubebolt-system", Name: "kubebolt-agent-x1",
			Labels: map[string]string{"app.kubernetes.io/name": "kubebolt-agent", "app.kubernetes.io/instance": "kubebolt-agent"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{c}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "agent",
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(time.Now()),
			}},
		}}},
	}
}

// An OOMKilled KubeBolt agent gets the command that raises its own limit
// , pinned to the chart version it runs.
func TestOOMKilled_TheAgentGetsTheHelmCommand(t *testing.T) {
	got := oomKilledRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{agentPod("daemonset", "128Mi", "ghcr.io/clm-cloud-solutions/kubebolt/kubebolt-agent:1.4.9")}})
	if len(got) != 1 {
		t.Fatalf("got %d insights", len(got))
	}
	want := "helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent -n kubebolt-system --version 1.4.9 --reuse-values --set resources.limits.memory=256Mi"
	if !strings.Contains(got[0].Suggestion, want) {
		t.Fatalf("suggestion = %q, want it to contain %q", got[0].Suggestion, want)
	}
}

// The promread pod's limit is a different value; twice 256Mi is 512Mi.
func TestOOMKilled_ThePromreadPodGetsItsOwnValuesKey(t *testing.T) {
	got := oomKilledRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{agentPod("promread", "256Mi", "ghcr.io/clm-cloud-solutions/kubebolt/kubebolt-agent:1.4.9")}})
	if len(got) != 1 || !strings.Contains(got[0].Suggestion, "--set agent.promRead.deployment.resources.limits.memory=512Mi") {
		t.Fatalf("suggestion = %+v", got)
	}
}

// Without a readable version the command resets to the latest chart's
// defaults instead of reusing stale ones.
func TestAgentMemorySuggestion_WithoutAVersion(t *testing.T) {
	s, ok := agentMemorySuggestion(agentPod("", "128Mi", "registry.local:5000/kubebolt-agent:dev"))
	if !ok || !strings.Contains(s, "--reset-then-reuse-values --set resources.limits.memory=256Mi") || strings.Contains(s, "--version") {
		t.Fatalf("suggestion = %q", s)
	}
}

// Any other pod keeps the generic text.
func TestOOMKilled_OtherPodsKeepTheGenericSuggestion(t *testing.T) {
	p := agentPod("", "128Mi", "x:1.0.0")
	p.Labels = map[string]string{"app.kubernetes.io/name": "shop"}
	got := oomKilledRule().Evaluate(&ClusterState{Pods: []*corev1.Pod{p}})
	if len(got) != 1 || strings.Contains(got[0].Suggestion, "helm upgrade") || !strings.Contains(got[0].Suggestion, "raise it") {
		t.Fatalf("suggestion = %+v", got)
	}
}

// Memory Near Limit on the agent gives the same command.
func TestMemoryNearLimit_TheAgentGetsTheHelmCommand(t *testing.T) {
	prev := sustainedMetricWindow
	sustainedMetricWindow = 0
	t.Cleanup(func() { sustainedMetricWindow = prev })
	p := agentPod("daemonset", "128Mi", "ghcr.io/clm-cloud-solutions/kubebolt/kubebolt-agent:1.4.9")
	p.Status.ContainerStatuses = nil
	state := &ClusterState{Pods: []*corev1.Pod{p}, PodMetrics: map[string]*models.MetricPoint{"kubebolt-system/kubebolt-agent-x1": {MemUsage: 120 << 20}}}
	got := memoryPressureRule().Evaluate(state)
	if len(got) != 1 || !strings.Contains(got[0].Suggestion, "--set resources.limits.memory=256Mi") {
		t.Fatalf("got %+v", got)
	}
}

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/x/kubebolt-agent:1.4.9":                "1.4.9",
		"ghcr.io/x/kubebolt-agent:v1.4.9@sha256:abcdef": "1.4.9",
		"registry:5000/kubebolt-agent":                  "",
		"kubebolt-agent:latest":                         "",
	}
	for in, want := range cases {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}
