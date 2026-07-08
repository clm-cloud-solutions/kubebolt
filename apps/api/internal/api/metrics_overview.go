package api

import (
	"context"
	"fmt"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// buildMetricsOnlyOverview assembles a cluster Overview for a metrics-only cluster —
// one whose agent ships metrics but has no kube-proxy connector, so there's no live API
// to count resources. It derives counts + pod health + CPU/Mem from kube-state-metrics
// (`kube_*`) and node/pod metrics already in VictoriaMetrics, via cluster-scoped instant
// queries. Fields KSM can't provide (events, namespace workloads, k8s version) stay
// empty; a count whose series is absent (KSM coverage varies by cluster) stays 0.
func (h *handlers) buildMetricsOnlyOverview(ctx context.Context, uid string) models.ClusterOverview {
	now := time.Now()
	// fq runs a cluster-scoped instant query and returns its scalar (0 when the series
	// is absent or VM errs — best-effort, never fails the overview).
	fq := func(promQL string) float64 {
		v, ok, err := copilot.QueryInstant(ctx, scopeQueryByCluster(promQL, uid), now)
		if err != nil || !ok {
			return 0
		}
		return v
	}
	// rc is a count-only ResourceCount: Ready mirrors Total. KSM tells us a resource
	// exists, not whether it's "unready", so we don't surface a false NotReady.
	rc := func(promQL string) models.ResourceCount {
		t := int(fq(promQL))
		return models.ResourceCount{Total: t, Ready: t}
	}

	ov := models.ClusterOverview{
		ClusterUID:           uid,
		Nodes:                rc("count(kube_node_info)"),
		Namespaces:           rc("count(kube_namespace_created)"),
		Deployments:          rc("count(kube_deployment_created)"),
		StatefulSets:         rc("count(kube_statefulset_created)"),
		DaemonSets:           rc("count(kube_daemonset_created)"),
		Jobs:                 rc("count(kube_job_owner)"),
		CronJobs:             rc("count(kube_cronjob_created)"),
		Services:             rc("count(kube_service_info)"),
		ConfigMaps:           rc("count(kube_configmap_info)"),
		Secrets:              rc("count(kube_secret_info)"),
		Ingresses:            rc("count(kube_ingress_info)"),
		PVCs:                 rc("count(kube_persistentvolumeclaim_info)"),
		PVs:                  rc("count(kube_persistentvolume_info)"),
		HPAs:                 rc("count(kube_horizontalpodautoscaler_info)"),
		ServiceAccounts:      rc("count(kube_serviceaccount_info)"),
		PodDisruptionBudgets: rc("count(kube_poddisruptionbudget_created)"),
		NetworkPolicies:      rc("count(kube_networkpolicy_created)"),
		Endpoints:            rc("count(kube_endpoint_info)"),
		Events:               []models.KubeEvent{},
	}

	// Pods with a real phase breakdown (kube_pod_status_phase is 1 for a pod's current
	// phase, 0 otherwise — sum gives the count per phase).
	podsTotal := int(fq("count(kube_pod_info)"))
	running := int(fq(`sum(kube_pod_status_phase{phase="Running"})`))
	failed := int(fq(`sum(kube_pod_status_phase{phase="Failed"})`))
	pending := int(fq(`sum(kube_pod_status_phase{phase="Pending"})`))
	ov.Pods = models.ResourceCount{Total: podsTotal, Ready: running, NotReady: failed + pending, Warning: pending}

	// CPU / Memory — best-effort cluster usage vs allocatable. Usage from the cadvisor
	// series the agent ships; allocatable from KSM. CPU in millicores, memory in bytes,
	// matching the connector-backed overview's units.
	cpuUsed := fq(`sum(rate(container_cpu_usage_seconds_total[5m]))`) * 1000
	cpuAlloc := fq(`sum(kube_node_status_allocatable{resource="cpu"})`) * 1000
	ov.CPU = models.ResourceUsage{Used: int64(cpuUsed), Allocatable: int64(cpuAlloc)}
	if cpuAlloc > 0 {
		ov.CPU.PercentUsed = cpuUsed / cpuAlloc * 100
	}
	memUsed := fq(`sum(container_memory_working_set_bytes)`)
	memAlloc := fq(`sum(kube_node_status_allocatable{resource="memory"})`)
	ov.Memory = models.ResourceUsage{Used: int64(memUsed), Allocatable: int64(memAlloc)}
	if memAlloc > 0 {
		ov.Memory.PercentUsed = memUsed / memAlloc * 100
	}

	ov.Health = metricsOnlyHealth(podsTotal, running, failed, pending)
	return ov
}

// metricsOnlyHealth synthesizes cluster health from pod phases. The connector-backed
// path uses live resources + the insights engine; here KSM phases are what we have. The
// "metrics" check is always pass — the agent is shipping metrics by definition — so the
// dashboard's metrics-availability gate stays green.
func metricsOnlyHealth(podsTotal, running, failed, pending int) models.ClusterHealth {
	status, score := "healthy", 100
	if failed > 0 || pending > 0 {
		status, score = "warning", 85
	}
	if podsTotal > 0 && failed*100/podsTotal >= 10 {
		status, score = "critical", 55
	}
	podStatus := "pass"
	if failed > 0 {
		podStatus = "fail"
	} else if pending > 0 {
		podStatus = "warn"
	}
	return models.ClusterHealth{
		Status: status,
		Score:  score,
		Checks: []models.HealthCheck{
			{Name: "metrics", Status: "pass", Message: "Metrics are flowing from the agent"},
			{Name: "pods", Status: podStatus, Message: fmt.Sprintf("%d running · %d failed · %d pending", running, failed, pending)},
		},
	}
}
