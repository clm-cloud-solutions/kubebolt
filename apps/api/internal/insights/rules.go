package insights

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

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
