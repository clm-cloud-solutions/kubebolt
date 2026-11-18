package insights

import (
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// agentChart is where the KubeBolt agent's Helm chart is published.
const agentChart = "oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent"

var semverTag = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// kubeboltAgent recognises a KubeBolt agent pod by its chart's selector
// labels and returns its Helm release and the values key of its memory limit:
// the DaemonSet's resources, or the promread Deployment's own.
func kubeboltAgent(pod *corev1.Pod) (release, valuesKey string, agent *corev1.Container, ok bool) {
	if pod == nil || pod.Labels["app.kubernetes.io/name"] != "kubebolt-agent" {
		return "", "", nil, false
	}
	release = pod.Labels["app.kubernetes.io/instance"]
	if release == "" {
		return "", "", nil, false
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == "agent" {
			agent = &pod.Spec.Containers[i]
		}
	}
	if agent == nil {
		return "", "", nil, false
	}
	valuesKey = "resources.limits.memory"
	for _, e := range agent.Env {
		if e.Name == "KUBEBOLT_AGENT_MODE" && e.Value == "promread" {
			valuesKey = "agent.promRead.deployment.resources.limits.memory"
		}
	}
	return release, valuesKey, agent, true
}

// agentMemorySuggestion is the remediation for a KubeBolt agent that ran out
// of memory or is close to it: the helm command that raises its limit, at
// twice the current one and never below the chart's 256Mi. The chart version
// is the agent image's tag (they are published together) so the command
// changes only the memory; without a readable tag it resets to the latest
// chart's defaults and reuses the release's values.
func agentMemorySuggestion(pod *corev1.Pod) (string, bool) {
	release, key, agent, ok := kubeboltAgent(pod)
	if !ok {
		return "", false
	}
	next := int64(256)
	if q, has := agent.Resources.Limits[corev1.ResourceMemory]; has {
		if mi := q.Value() / (1 << 20); 2*mi > next {
			next = 2 * mi
		}
	}
	target := resource.MustParse(fmt.Sprintf("%dMi", next))
	cmd := fmt.Sprintf("helm upgrade %s %s -n %s", release, agentChart, pod.Namespace)
	if tag := imageTag(agent.Image); tag != "" {
		cmd += fmt.Sprintf(" --version %s --reuse-values", tag)
	} else {
		cmd += " --reset-then-reuse-values"
	}
	cmd += fmt.Sprintf(" --set %s=%s", key, target.String())
	return "This is the KubeBolt agent. It relays the API server's responses for this cluster and holds each one whole, so a large cluster needs more than the default. Raise its memory limit on its Helm release: " +
		cmd + ". The agent caps one relayed response at a quarter of its limit and answers with an error past it.", true
}

// imageTag returns the semver tag of an image reference, "" otherwise.
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	i := strings.LastIndex(image, ":")
	if i < 0 || strings.Contains(image[i:], "/") {
		return ""
	}
	if m := semverTag.FindStringSubmatch(image[i+1:]); m != nil {
		return m[1]
	}
	return ""
}
