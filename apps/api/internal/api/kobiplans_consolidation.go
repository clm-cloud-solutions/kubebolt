package api

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// Kobi Plans, consolidation (plans-16): this file reads the cluster (nodes,
// pods, PDBs, the autoscaler), the metrics store (each pod's P95 over the
// week, each node's list price) and the live right-sizing plans (the
// requests they leave); kobiplan.ConsolidationFacts and BuildConsolidation
// decide.

const (
	consolidationReadTimeout = 20 * time.Second
	autoscalerStatusTimeout  = 3 * time.Second
)

// Each pod's P95 over the week, CPU in cores and memory in bytes, summed over
// its containers (the pause container and the pod's own cgroup left out).
const (
	podCPUP95Query = `quantile_over_time(0.95, sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]))[7d:5m])`
	podMemP95Query = `quantile_over_time(0.95, sum by (namespace, pod) (container_memory_working_set_bytes{container!="",container!="POD"})[7d:5m])`
	// How many days of the week the store has usage for.
	podUsageDaysQuery = `count_over_time(sum(container_memory_working_set_bytes{container!="",container!="POD"})[7d:1h]) / 24`
	nodePriceQuery    = `max by (node) (node_total_hourly_cost)`
)

func (s planSource) createConsolidation(ctx context.Context, conn *cluster.Connector, clusterID string, live []kobiplan.Plan, req copilot.CreatePlanRequest, now time.Time) (copilot.CreatePlanOutcome, error) {
	readCtx, cancel := context.WithTimeout(ctx, consolidationReadTimeout)
	defer cancel()

	usage, days, err := s.h.podUsageP95(readCtx, clusterID)
	if err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	if minHours := s.h.rightSizingMinHistoryHours(ctx); days*24 < float64(minHours) {
		return copilot.CreatePlanOutcome{}, fmt.Errorf("%w (it needs %d hours of usage history)", kobiplan.ErrInsufficientHistory, minHours)
	}
	pricing, notes := s.h.planPricing(readCtx, clusterID)
	prices := s.h.nodeMonthlyPrices(readCtx, clusterID, pricing.Factor)
	objects := consolidationObjects(readCtx, conn)
	planned, dependsOn := plannedRequests(func(t kobiplan.Target) []*corev1.Pod { return workloadPodsOf(conn, t) }, live, now)

	nodes, current, autoscaler := kobiplan.ConsolidationFacts(objects, usage, nil, prices)
	var after []kobiplan.PodFacts
	if len(planned) > 0 {
		_, after, _ = kobiplan.ConsolidationFacts(objects, usage, planned, prices)
	}
	currency := pricing.Currency
	if len(prices) == 0 {
		currency = ""
	}
	p, err := kobiplan.BuildConsolidation(kobiplan.ConsolidationInput{
		ID:          "pln_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		OrgID:       s.tenant,
		ClusterID:   clusterID,
		Title:       consolidationTitle(req),
		Source:      kobiplan.Source{Initiator: kobiplan.InitiatorChat, UserID: s.user, UserName: s.userName, ConversationID: s.conversation},
		Nodes:       nodes,
		Current:     current,
		Planned:     after,
		DependsOn:   dependsOn,
		Autoscaler:  autoscaler,
		Currency:    currency,
		PriceSource: "opencost",
		WindowDays:  days,
		ComputedAt:  now,
		Notes:       notes,
		Now:         now,
	})
	if err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	if len(p.Steps) == 0 {
		return copilot.CreatePlanOutcome{Empty: &p}, nil
	}
	if err := s.store.Create(p); err != nil {
		return copilot.CreatePlanOutcome{}, err
	}
	slog.Info("kobi plan created",
		slog.String("plan", p.ID), slog.String("kind", string(p.Kind)), slog.String("cluster", clusterID),
		slog.Int("steps", len(p.Steps)), slog.Int("excluded", len(p.Excluded)), slog.Int("dependsOn", len(p.DependsOn)))
	return copilot.CreatePlanOutcome{Created: &p}, nil
}

func consolidationTitle(req copilot.CreatePlanRequest) string {
	if req.Title != "" {
		return req.Title
	}
	return "Consolidation: node pools"
}

// podUsageP95 is each pod's P95 over the week, keyed namespace/name, and how
// many days of it the store holds. An unreachable store is an error: without
// usage, a pod with no requests would count as needing nothing.
func (h *handlers) podUsageP95(ctx context.Context, clusterID string) (map[string]kobiplan.PodNeed, float64, error) {
	scope := func(q string) string {
		return scopeQueryByTenant(scopeQueryByCluster(q, clusterID), metricsTenantPinCtx(ctx))
	}
	cpu, err := runInstantQuery(ctx, scope(podCPUP95Query))
	if err != nil {
		return nil, 0, fmt.Errorf("the usage history could not be read: %w", err)
	}
	mem, err := runInstantQuery(ctx, scope(podMemP95Query))
	if err != nil {
		return nil, 0, fmt.Errorf("the usage history could not be read: %w", err)
	}
	out := map[string]kobiplan.PodNeed{}
	for _, r := range cpu {
		if k := podRowKey(r); k != "" {
			n := out[k]
			n.CPUMilli = int64(math.Ceil(r.Value * 1000))
			out[k] = n
		}
	}
	for _, r := range mem {
		if k := podRowKey(r); k != "" {
			n := out[k]
			n.MemBytes = int64(math.Ceil(r.Value))
			out[k] = n
		}
	}
	days := 0.0
	if rows, err := runInstantQuery(ctx, scope(podUsageDaysQuery)); err == nil && len(rows) > 0 && !math.IsNaN(rows[0].Value) {
		days = math.Min(kobiplan.FullWindowDays, rows[0].Value)
	}
	return out, days, nil
}

func podRowKey(r vmRow) string {
	ns, pod := r.Labels["namespace"], r.Labels["pod"]
	if ns == "" || pod == "" || math.IsNaN(r.Value) || r.Value < 0 {
		return ""
	}
	return ns + "/" + pod
}

// nodeMonthlyPrices is each node's OpenCost list price a month, in the org's
// currency; empty without OpenCost.
func (h *handlers) nodeMonthlyPrices(ctx context.Context, clusterID string, factor float64) map[string]float64 {
	rows, err := runInstantQuery(ctx, scopeQueryByTenant(scopeQueryByCluster(nodePriceQuery, clusterID), metricsTenantPinCtx(ctx)))
	if err != nil {
		return nil
	}
	if factor <= 0 {
		factor = 1
	}
	out := map[string]float64{}
	for _, r := range rows {
		if n := r.Labels["node"]; n != "" && !math.IsNaN(r.Value) && r.Value > 0 {
			out[n] = math.Round(r.Value*hoursPerMonth*factor*100) / 100
		}
	}
	return out
}

// consolidationObjects reads what the engine decides with from the informers,
// and whether a managed Cluster Autoscaler left its status ConfigMap.
func consolidationObjects(ctx context.Context, conn *cluster.Connector) kobiplan.ConsolidationObjects {
	o := kobiplan.ConsolidationObjects{
		Nodes:       conn.GetNodes(),
		Pods:        conn.GetPods(),
		PDBs:        conn.GetPodDisruptionBudgets(),
		Deployments: conn.GetDeployments(),
	}
	if cs := conn.Clientset(); cs != nil {
		cctx, cancel := context.WithTimeout(ctx, autoscalerStatusTimeout)
		defer cancel()
		if _, err := cs.CoreV1().ConfigMaps("kube-system").Get(cctx, "cluster-autoscaler-status", metav1.GetOptions{}); err == nil {
			o.CAStatus = true
		}
	}
	return o
}

// plannedRequests is what the pending steps of the cluster's live right-sizing
// plans leave each pod requesting, by pod and container, and which plans those
// are. A step already applied is in the cluster's requests.
func plannedRequests(podsOf func(kobiplan.Target) []*corev1.Pod, live []kobiplan.Plan, now time.Time) (map[string]kobiplan.PlannedContainers, []string) {
	out := map[string]kobiplan.PlannedContainers{}
	var plans []string
	for _, p := range live {
		if p.Kind != kobiplan.KindRightsizing || !p.Status.Live() || (p.Status == kobiplan.StatusDraft && !p.ExpiresAt.After(now)) {
			continue
		}
		used := false
		for _, st := range p.Steps {
			if st.Status != kobiplan.StepPending || st.Action != kobiplan.ActionSetResources {
				continue
			}
			params, err := kobiplan.DecodeSetResources(st.Params)
			if err != nil {
				continue
			}
			changes := kobiplan.PlannedContainers{}
			for _, c := range params.Containers {
				if c.InitContainer || c.Requests == nil {
					continue
				}
				changes[c.Container] = kobiplan.PodNeed{CPUMilli: milliOf(c.Requests.CPU), MemBytes: bytesOf(c.Requests.Memory)}
			}
			if len(changes) == 0 {
				continue
			}
			for _, pod := range podsOf(st.Target) {
				out[pod.Namespace+"/"+pod.Name] = changes
				used = true
			}
		}
		if used {
			plans = append(plans, p.ID)
		}
	}
	return out, plans
}

func milliOf(q *string) int64 {
	if q == nil {
		return 0
	}
	v, err := resource.ParseQuantity(*q)
	if err != nil {
		return 0
	}
	return v.MilliValue()
}

func bytesOf(q *string) int64 {
	if q == nil {
		return 0
	}
	v, err := resource.ParseQuantity(*q)
	if err != nil {
		return 0
	}
	return v.Value()
}

// poolStepFacts reads a consolidation step's pool as it is now: its nodes
// (control-plane ones aside; a cordoned node counts, it is still paid for)
// and, once the step has been applied PoolVerifyAfter, what its checks read.
func poolStepFacts(nodes []*corev1.Node, pods []*corev1.Pod, s kobiplan.Step, now time.Time) *kobiplan.PoolFacts {
	params, err := kobiplan.DecodeRemoveNodes(s.Params)
	if err != nil {
		return nil
	}
	var in []kobiplan.NodeFacts
	var cordoned []string
	onPool := map[string]bool{}
	for _, n := range nodes {
		name, source := kobiplan.NodePool(n.Labels)
		if kobiplan.PoolName(name) != params.Pool || source != params.PoolSource || kobiplan.IsControlPlane(n.Labels) {
			continue
		}
		in = append(in, kobiplan.NodeFacts{Name: n.Name, InstanceType: n.Labels["node.kubernetes.io/instance-type"]})
		onPool[n.Name] = true
		if n.Spec.Unschedulable {
			cordoned = append(cordoned, n.Name)
		}
	}
	f := &kobiplan.PoolFacts{Nodes: len(in), Hash: kobiplan.PoolHash(in), Present: in, Cordoned: cordoned}
	if s.Status != kobiplan.StepApplied || s.AppliedAt == nil || now.Before(s.AppliedAt.Add(kobiplan.PoolVerifyAfter())) {
		return f
	}
	applied := *s.AppliedAt
	f.Checked = true
	var mine []*corev1.Pod
	for _, p := range pods {
		if waitsForANode(p, applied) {
			f.PendingPods++
		}
		if !onPool[p.Spec.NodeName] {
			continue
		}
		mine = append(mine, p)
		if evictedSince(p, applied) {
			f.Evicted++
		}
	}
	f.OOMKilled, _ = podTroubleSince(mine, applied)
	return f
}

// waitsForANode: the scheduler found no node for the pod since the step was
// applied, whatever the reason: no room, or no node its rules allow (one
// replica per node, a topology spread, node affinity). Only a claim not yet
// bound is not the pool's doing. Counting «Insufficient» alone let a replica
// left Pending by anti-affinity pass the check (26-nov).
func waitsForANode(p *corev1.Pod, since time.Time) bool {
	if p.Status.Phase != corev1.PodPending || p.Spec.NodeName != "" {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable &&
			!strings.Contains(c.Message, "unbound immediate PersistentVolumeClaims") && !c.LastTransitionTime.Time.Before(since) {
			return true
		}
	}
	return false
}

// evictedSince: the kubelet evicted the pod for node pressure after since.
func evictedSince(p *corev1.Pod, since time.Time) bool {
	if p.Status.Reason != "Evicted" {
		return false
	}
	for _, c := range p.Status.Conditions {
		if !c.LastTransitionTime.Time.Before(since) {
			return true
		}
	}
	return false
}
