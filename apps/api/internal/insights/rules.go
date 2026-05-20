package insights

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/google/uuid"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// AllRules returns all Phase 1 insight rules.
func AllRules() []Rule {
	return []Rule{
		crashLoopRule(),
		oomKilledRule(),
		cpuThrottleRiskRule(),
		memoryPressureRule(),
		resourceUnderrequestRule(),
		zeroReplicasRule(),
		pvcPendingRule(),
		nodeNotReadyRule(),
		hpaMaxedOutRule(),
		frequentRestartsRule(),
		imagePullBackoffRule(),
		evictedPodsRule(),
		serviceNoEndpointsRule(),
		networkPolicyNoMatchRule(),
		namespaceWithoutNetworkPolicyRule(),
	}
}

func newInsight(severity, resource, title, message, suggestion string) models.Insight {
	now := time.Now()
	// Extract namespace from resource string like "Pod/namespace/name"
	namespace := ""
	parts := strings.SplitN(resource, "/", 3)
	if len(parts) >= 3 {
		namespace = parts[1]
	}
	// Derive category from the resource kind
	category := "workload"
	if len(parts) >= 1 {
		switch parts[0] {
		case "Node":
			category = "node"
		case "PVC":
			category = "storage"
		case "HPA":
			category = "autoscaling"
		case "Service":
			category = "networking"
		case "NetworkPolicy":
			category = "networking"
		case "Namespace":
			category = "networking" // covers the "namespace without NetworkPolicy" rule
		}
	}
	return models.Insight{
		ID:         uuid.New().String(),
		Severity:   severity,
		Category:   category,
		Resource:   resource,
		Namespace:  namespace,
		Title:      title,
		Message:    message,
		Suggestion: suggestion,
		FirstSeen:  now,
		LastSeen:   now,
	}
}

// 1. CrashLoopBackOff with restarts > 3
func crashLoopRule() Rule {
	return Rule{
		ID:       "crash-loop",
		Name:     "CrashLoopBackOff Detection",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" && cs.RestartCount > 3 {
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Pod in CrashLoopBackOff",
							fmt.Sprintf("Container %s in pod %s/%s is crash-looping with %d restarts", cs.Name, pod.Namespace, pod.Name, cs.RestartCount),
							"Check container logs with 'kubectl logs' and review the container's command/args and liveness probes.",
						))
					}
				}
			}
			return insights
		},
	}
}

// 2. OOMKilled
func oomKilledRule() Rule {
	return Rule{
		ID:       "oom-killed",
		Name:     "OOMKilled Detection",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Container OOMKilled",
							fmt.Sprintf("Container %s in pod %s/%s was terminated due to OOM (exit code 137)", cs.Name, pod.Namespace, pod.Name),
							"Increase memory limits for this container or investigate memory leaks in the application.",
						))
					}
				}
			}
			return insights
		},
	}
}

// 3. CPU throttle risk (usage > 80% of limit)
func cpuThrottleRiskRule() Rule {
	return Rule{
		ID:       "cpu-throttle-risk",
		Name:     "CPU Throttle Risk",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				metrics := state.PodMetrics[key]
				if metrics == nil {
					continue
				}
				var cpuLimit int64
				for _, c := range pod.Spec.Containers {
					cpuLimit += c.Resources.Limits.Cpu().MilliValue()
				}
				if cpuLimit > 0 && float64(metrics.CPUUsage)/float64(cpuLimit) > 0.8 {
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"CPU Throttle Risk",
						fmt.Sprintf("Pod %s/%s is using %.0f%% of CPU limit (%dm/%dm)", pod.Namespace, pod.Name, float64(metrics.CPUUsage)/float64(cpuLimit)*100, metrics.CPUUsage, cpuLimit),
						"Consider increasing CPU limits or optimizing CPU-intensive operations.",
					))
				}
			}
			return insights
		},
	}
}

// 4. Memory pressure (usage > 85% of limit)
func memoryPressureRule() Rule {
	return Rule{
		ID:       "memory-pressure",
		Name:     "Memory Pressure",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				metrics := state.PodMetrics[key]
				if metrics == nil {
					continue
				}
				var memLimit int64
				for _, c := range pod.Spec.Containers {
					memLimit += c.Resources.Limits.Memory().Value()
				}
				if memLimit > 0 && float64(metrics.MemUsage)/float64(memLimit) > 0.85 {
					pct := float64(metrics.MemUsage) / float64(memLimit) * 100
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Memory Pressure",
						fmt.Sprintf("Pod %s/%s is using %.0f%% of memory limit", pod.Namespace, pod.Name, pct),
						"Increase memory limits or investigate memory usage patterns to avoid OOMKill.",
					))
				}
			}
			return insights
		},
	}
}

// 5. Resource underrequest (requests < 40% of actual usage)
func resourceUnderrequestRule() Rule {
	return Rule{
		ID:       "resource-underrequest",
		Name:     "Resource Under-Request",
		Severity: "info",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				key := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				metrics := state.PodMetrics[key]
				if metrics == nil {
					continue
				}
				var cpuReq, memReq int64
				for _, c := range pod.Spec.Containers {
					cpuReq += c.Resources.Requests.Cpu().MilliValue()
					memReq += c.Resources.Requests.Memory().Value()
				}
				if cpuReq > 0 && metrics.CPUUsage > 0 && float64(cpuReq)/float64(metrics.CPUUsage) < 0.4 {
					insights = append(insights, newInsight(
						"info",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"CPU Request Too Low",
						fmt.Sprintf("Pod %s/%s CPU request (%dm) is less than 40%% of actual usage (%dm)", pod.Namespace, pod.Name, cpuReq, metrics.CPUUsage),
						"Increase CPU requests to better reflect actual usage for improved scheduling.",
					))
				}
				if memReq > 0 && metrics.MemUsage > 0 && float64(memReq)/float64(metrics.MemUsage) < 0.4 {
					insights = append(insights, newInsight(
						"info",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Memory Request Too Low",
						fmt.Sprintf("Pod %s/%s memory request is less than 40%% of actual usage", pod.Namespace, pod.Name),
						"Increase memory requests to better reflect actual usage for improved scheduling.",
					))
				}
			}
			return insights
		},
	}
}

// 6. Deployment with 0 available replicas
func zeroReplicasRule() Rule {
	return Rule{
		ID:       "zero-replicas",
		Name:     "Zero Available Replicas",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, d := range state.Deployments {
				if d.Spec.Replicas != nil && *d.Spec.Replicas > 0 && d.Status.AvailableReplicas == 0 {
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Deployment/%s/%s", d.Namespace, d.Name),
						"Zero Available Replicas",
						fmt.Sprintf("Deployment %s/%s has 0 available replicas (desired: %d)", d.Namespace, d.Name, *d.Spec.Replicas),
						"Check pod events and logs. The deployment may have failing containers or scheduling issues.",
					))
				}
			}
			return insights
		},
	}
}

// 7. PVC in Pending state
func pvcPendingRule() Rule {
	return Rule{
		ID:       "pvc-pending",
		Name:     "PVC Pending",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pvc := range state.PVCs {
				if pvc.Status.Phase == corev1.ClaimPending {
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("PVC/%s/%s", pvc.Namespace, pvc.Name),
						"PVC Pending",
						fmt.Sprintf("PersistentVolumeClaim %s/%s is in Pending state", pvc.Namespace, pvc.Name),
						"Check if a suitable PersistentVolume exists or if the StorageClass can dynamically provision one.",
					))
				}
			}
			return insights
		},
	}
}

// 8. Node not ready
func nodeNotReadyRule() Rule {
	return Rule{
		ID:       "node-not-ready",
		Name:     "Node Not Ready",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, node := range state.Nodes {
				ready := false
				for _, cond := range node.Status.Conditions {
					if cond.Type == corev1.NodeReady {
						if cond.Status == corev1.ConditionTrue {
							ready = true
						}
						break
					}
				}
				if !ready {
					insights = append(insights, newInsight(
						"critical",
						fmt.Sprintf("Node/%s", node.Name),
						"Node Not Ready",
						fmt.Sprintf("Node %s is not in Ready state", node.Name),
						"Check node conditions, kubelet logs, and ensure the node has sufficient resources.",
					))
				}
			}
			return insights
		},
	}
}

// 9. HPA maxed out (current == max replicas)
func hpaMaxedOutRule() Rule {
	return Rule{
		ID:       "hpa-maxed-out",
		Name:     "HPA Maxed Out",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, hpa := range state.HPAs {
				if hpa.Status.CurrentReplicas >= hpa.Spec.MaxReplicas && hpa.Status.CurrentReplicas > 0 {
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("HPA/%s/%s", hpa.Namespace, hpa.Name),
						"HPA at Maximum Replicas",
						fmt.Sprintf("HPA %s/%s is at maximum replicas (%d/%d)", hpa.Namespace, hpa.Name, hpa.Status.CurrentReplicas, hpa.Spec.MaxReplicas),
						"Consider increasing maxReplicas or optimizing the target workload to reduce resource demand.",
					))
				}
			}
			return insights
		},
	}
}

// 10. Frequent restarts (> 5)
func frequentRestartsRule() Rule {
	return Rule{
		ID:       "frequent-restarts",
		Name:     "Frequent Restarts",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.RestartCount > 5 {
						insights = append(insights, newInsight(
							"warning",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Frequent Container Restarts",
							fmt.Sprintf("Container %s in pod %s/%s has restarted %d times", cs.Name, pod.Namespace, pod.Name, cs.RestartCount),
							"Check container logs and health probes. Frequent restarts indicate instability.",
						))
					}
				}
			}
			return insights
		},
	}
}

// 11. ImagePullBackOff
func imagePullBackoffRule() Rule {
	return Rule{
		ID:       "image-pull-backoff",
		Name:     "Image Pull BackOff",
		Severity: "critical",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "ImagePullBackOff" || cs.State.Waiting.Reason == "ErrImagePull") {
						insights = append(insights, newInsight(
							"critical",
							fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
							"Image Pull Failed",
							fmt.Sprintf("Container %s in pod %s/%s cannot pull image: %s", cs.Name, pod.Namespace, pod.Name, cs.State.Waiting.Reason),
							"Verify the image name/tag exists, check registry credentials, and ensure network access to the registry.",
						))
					}
				}
			}
			return insights
		},
	}
}

// 13. Service has zero ready endpoints (P25-05).
//
// Fires when a Service that's expected to back a workload (has a
// selector, is not Headless, is not ExternalName) has zero
// EndpointSlice addresses with `ready=true`. The signal answers
// "is this Service actually serving?" — a common failure mode is
// a Service whose selector drifted away from its pods (mismatched
// labels after a rename, deleted Deployment leaving the Service
// behind), and traffic to ClusterIP black-holes silently. This
// rule surfaces it before users hit a 503.
//
// Skip rules:
//   - ExternalName services have no endpoints by design.
//   - Headless services (clusterIP == "None") are DNS-only and
//     legitimately can have zero ready endpoints during scale-to-
//     zero of operator workloads (think StatefulSet with replicas=0).
//   - Services without a selector are manually managed — the operator
//     wires Endpoints by hand, and that's intentional.
//
// EndpointSlice <-> Service binding uses the canonical
// `kubernetes.io/service-name` label, which the EndpointSlice
// controller writes for every slice it produces.
func serviceNoEndpointsRule() Rule {
	return Rule{
		ID:       "service-no-endpoints",
		Name:     "Service Has Zero Ready Endpoints",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight

			// Index slices by (namespace, service-name) for O(1) lookup
			// per service. A single Service can have multiple slices
			// (sharded by IP family or address count), so the value is
			// a list.
			slicesByService := map[string][]int{}
			for i, slice := range state.EndpointSlices {
				name := slice.Labels["kubernetes.io/service-name"]
				if name == "" {
					continue
				}
				key := slice.Namespace + "/" + name
				slicesByService[key] = append(slicesByService[key], i)
			}

			for _, svc := range state.Services {
				if svc.Spec.Type == corev1.ServiceTypeExternalName {
					continue
				}
				if svc.Spec.ClusterIP == corev1.ClusterIPNone {
					continue
				}
				if len(svc.Spec.Selector) == 0 {
					continue
				}

				readyCount := 0
				totalCount := 0
				key := svc.Namespace + "/" + svc.Name
				for _, idx := range slicesByService[key] {
					slice := state.EndpointSlices[idx]
					for _, ep := range slice.Endpoints {
						totalCount++
						if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
							readyCount++
						}
					}
				}

				if readyCount > 0 {
					continue
				}

				suggestion := "Verify the Service selector matches pod labels and that pods are running and ready. " +
					"Try: kubectl get endpoints " + svc.Name + " -n " + svc.Namespace
				message := fmt.Sprintf("Service %s/%s has no ready endpoints", svc.Namespace, svc.Name)
				if totalCount > 0 {
					// Distinguishes "selector matches pods, but they're not ready"
					// (workload is unhealthy) from "selector matches nothing"
					// (configuration error). Different operator response.
					message = fmt.Sprintf(
						"Service %s/%s has %d endpoint(s) but none are ready",
						svc.Namespace, svc.Name, totalCount,
					)
					suggestion = "Pods backing this Service exist but aren't passing readiness. " +
						"Check pod readiness probes and container logs."
				}

				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("Service/%s/%s", svc.Namespace, svc.Name),
					"Service Has Zero Ready Endpoints",
					message,
					suggestion,
				))
			}
			return insights
		},
	}
}

// 12. Evicted pods
func evictedPodsRule() Rule {
	return Rule{
		ID:       "evicted-pods",
		Name:     "Evicted Pods",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			for _, pod := range state.Pods {
				if pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted" {
					insights = append(insights, newInsight(
						"warning",
						fmt.Sprintf("Pod/%s/%s", pod.Namespace, pod.Name),
						"Pod Evicted",
						fmt.Sprintf("Pod %s/%s was evicted: %s", pod.Namespace, pod.Name, pod.Status.Message),
						"Check node resource pressure. Evicted pods indicate the node ran out of resources.",
					))
				}
			}
			return insights
		},
	}
}

// networkPolicyNoMatchRule flags NetworkPolicies whose podSelector
// matches zero pods in their namespace. The canonical case for this
// is a typo in matchLabels (the policy was supposed to gate the
// `tier=db` pods but actually says `tier=database`), but it also
// catches policies whose target workload was renamed or deleted
// without cleaning up the matching policy.
//
// Empty selector ({}) is NOT a no-match — per NetworkPolicy v1
// spec it matches EVERY pod in the namespace. We skip those
// explicitly so the rule doesn't false-positive on catch-all
// policies (which are intentional in many security postures).
func networkPolicyNoMatchRule() Rule {
	return Rule{
		ID:       "policy-no-match",
		Name:     "NetworkPolicy podSelector matches no pods",
		Severity: "warning",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			// Index pods by namespace once — N policies × M pods
			// without the index would be O(N×M) per evaluation tick.
			podsByNS := map[string][]*corev1.Pod{}
			for _, p := range state.Pods {
				podsByNS[p.Namespace] = append(podsByNS[p.Namespace], p)
			}
			for _, np := range state.NetworkPolicies {
				// Empty selector = catch-all (matches everything).
				// Intentional in deny-all defaults — don't flag.
				if len(np.Spec.PodSelector.MatchLabels) == 0 && len(np.Spec.PodSelector.MatchExpressions) == 0 {
					continue
				}
				sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
				if err != nil {
					// Invalid selector — different signal than no-match;
					// the policy itself is malformed. Skip here, the
					// apiserver should have rejected it on create anyway.
					continue
				}
				matched := 0
				for _, p := range podsByNS[np.Namespace] {
					if sel.Matches(labels.Set(p.Labels)) {
						matched++
					}
				}
				if matched > 0 {
					continue
				}
				insights = append(insights, newInsight(
					"warning",
					fmt.Sprintf("NetworkPolicy/%s/%s", np.Namespace, np.Name),
					"NetworkPolicy podSelector matches no pods",
					fmt.Sprintf(
						"NetworkPolicy %s/%s declares a podSelector but no pod in its namespace matches. "+
							"Traffic policy intent is unverifiable — either the selector has a typo or "+
							"the target workload was renamed / deleted.",
						np.Namespace, np.Name,
					),
					"Verify the podSelector matchLabels against the pods you expect this policy to gate. "+
						"kubectl get pods -n "+np.Namespace+" --show-labels",
				))
			}
			return insights
		},
	}
}

// namespaceWithoutNetworkPolicyRule flags namespaces that have
// running pods but zero NetworkPolicies attached. Informational
// severity (not warning) — many clusters legitimately run without
// NetworkPolicies (single-tenant trust boundary), so this is a
// nudge for operators who expected policy coverage rather than a
// "fix this now" alert.
//
// System namespaces (kube-*, gke-*, etc.) are skipped — they're
// often managed by the platform and the operator has no policy
// authority over them.
func namespaceWithoutNetworkPolicyRule() Rule {
	return Rule{
		ID:       "policy-orphan",
		Name:     "Namespace has running pods but no NetworkPolicy",
		Severity: "info",
		Evaluate: func(state *ClusterState) []models.Insight {
			var insights []models.Insight
			// Index policies by namespace once.
			policiesByNS := map[string]int{}
			for _, np := range state.NetworkPolicies {
				policiesByNS[np.Namespace]++
			}
			// Group pods by namespace to surface coverage gaps
			// where workloads exist. We only flag namespaces that
			// have at least one pod — empty namespaces aren't
			// actionable from a security posture standpoint.
			runningPodsByNS := map[string]int{}
			for _, p := range state.Pods {
				if isSystemNamespace(p.Namespace) {
					continue
				}
				if p.Status.Phase == corev1.PodRunning {
					runningPodsByNS[p.Namespace]++
				}
			}
			for ns, podCount := range runningPodsByNS {
				if policiesByNS[ns] > 0 {
					continue
				}
				insights = append(insights, newInsight(
					"info",
					fmt.Sprintf("Namespace/%s/%s", ns, ns),
					"Namespace has running pods but no NetworkPolicy",
					fmt.Sprintf(
						"Namespace %q has %d running pod(s) and zero NetworkPolicies. "+
							"All pod-to-pod and pod-to-external traffic in this namespace "+
							"is allowed by default — review whether that matches your "+
							"intended security posture.",
						ns, podCount,
					),
					"If this namespace should be isolated, start with a default-deny ingress "+
						"policy: https://kubernetes.io/docs/concepts/services-networking/network-policies/#default-deny-all-ingress-traffic",
				))
			}
			return insights
		},
	}
}

// isSystemNamespace returns true for namespaces operators don't
// typically have policy authority over — built-in Kubernetes
// system namespaces and the common managed-platform prefixes.
// Skipping them prevents the policy-orphan rule from generating
// false-positive nudges the operator can't action.
func isSystemNamespace(ns string) bool {
	switch {
	case ns == "kubebolt", ns == "kubebolt-system", ns == "kubebolt-agent":
		return true
	}
	prefixes := []string{
		"kube-",   // kube-system, kube-public, kube-node-lease, kube-prom-stack, etc.
		"gke-",    // gke-managed-*, gke-system-*
		"gmp-",    // GCP Managed Prometheus
		"aks-",    // AKS-managed
		"eks-",    // EKS-managed
		"cilium-", // Cilium control plane (when split out)
		"istio-",  // istio-system if treated as platform
	}
	for _, p := range prefixes {
		if strings.HasPrefix(ns, p) {
			return true
		}
	}
	return false
}

