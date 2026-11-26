package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
	"github.com/kubebolt/kubebolt/apps/api/internal/opsmetrics"
)

// Plan verification (plans-07): what the cluster shows about each step of an
// exported plan, read from the plan's own cluster — the caches for the
// template, the pods and the rollout, VictoriaMetrics for the P95 since the
// step was applied and the cost after it — and written into the plan by
// kobiplan.Plan.Verify. It runs on the hourly sweep (RunKobiPlanSweep) and
// on request (POST /plans/{id}/verify, «Verify now»).

// planFacts reads the facts for every step that can move: a pending or
// applied step of an exported plan, and a step whose impact is due.
func (h *handlers) planFacts(ctx context.Context, conn *cluster.Connector, p kobiplan.Plan, now time.Time) kobiplan.PlanFacts {
	out := kobiplan.PlanFacts{Steps: map[string]kobiplan.StepFacts{}}
	var pricing *kobiplan.Pricing
	price := func() kobiplan.Pricing {
		if pricing == nil {
			pr, _ := h.planPricing(ctx, p.ClusterID)
			pricing = &pr
		}
		return *pricing
	}
	for _, s := range p.Steps {
		if s.Status == kobiplan.StepExpired || (s.Status == kobiplan.StepVerified && s.Impact.Observed != nil) {
			continue
		}
		switch s.Action {
		case kobiplan.ActionSetResources:
			out.Steps[s.ID] = h.stepFacts(ctx, conn, p, s, now, price)
		case kobiplan.ActionRemoveNodes:
			if f := poolStepFacts(conn.GetNodes(), conn.GetPods(), s, now); f != nil {
				out.Steps[s.ID] = kobiplan.StepFacts{Pool: f}
			}
		}
	}
	if last := p.LastAppliedAt(); last != nil && !now.Before(last.Add(kobiplan.ImpactAfter)) && p.Baseline != nil && p.Baseline.ClusterCost != nil {
		out.ClusterCostAfter = h.costAfter(ctx, p.ClusterID, clusterCostExpr(p.Baseline), *last, price())
	}
	return out
}

func (h *handlers) stepFacts(ctx context.Context, conn *cluster.Connector, p kobiplan.Plan, s kobiplan.Step, now time.Time, price func() kobiplan.Pricing) kobiplan.StepFacts {
	t := s.Target
	tpl, ok := templateOf(conn, t.Type, t.Namespace, t.Name)
	if !ok {
		return kobiplan.StepFacts{}
	}
	params, _ := kobiplan.DecodeSetResources(s.Params)
	f := kobiplan.StepFacts{
		Found:    true,
		SpecHash: kobiplan.SpecHash(tpl),
		Live:     kobiplan.LiveResources(tpl, params),
	}
	pods := workloadPodsOf(conn, t)
	f.Pods = len(pods)
	if s.Status == kobiplan.StepPending || s.AppliedAt == nil {
		return f
	}
	applied := *s.AppliedAt
	if s.Status == kobiplan.StepApplied && !now.Before(applied.Add(kobiplan.VerifyAfter)) {
		f.Checked = true
		f.RolloutComplete = rolloutComplete(conn, t)
		f.OOMKilled, f.Restarts = podTroubleSince(pods, applied)
		f.P95 = h.p95Since(ctx, p.ClusterID, t, f.Live, applied, now)
	}
	if s.Impact.Observed == nil && !now.Before(applied.Add(kobiplan.ImpactAfter)) {
		f.Measured = true
		f.WindowDays = int(math.Round(kobiplan.ImpactAfter.Hours() / 24))
		if q := workloadAllocationExpr(t); q != "" && s.Baseline != nil && s.Baseline.AllocatedCost != nil {
			f.AllocatedAfter = h.costAfter(ctx, p.ClusterID, q, applied, price())
		}
	}
	return f
}

// costAfter is the monthly cost of an hourly-cost expression averaged over
// the ImpactAfter window that starts when the step was applied, in the
// pricing's currency. nil without data.
func (h *handlers) costAfter(ctx context.Context, clusterID, expr string, from time.Time, pricing kobiplan.Pricing) *kobiplan.Money {
	if !pricing.Rates.Available() {
		return nil
	}
	q := scopeQueryByTenant(scopeQueryByCluster(fmt.Sprintf(`avg_over_time((%s)%s)`, expr, promWindow(kobiplan.ImpactAfter)), clusterID), metricsTenantPinCtx(ctx))
	rows, err := runInstantQueryAt(ctx, q, from.Add(kobiplan.ImpactAfter))
	if err != nil || len(rows) == 0 || math.IsNaN(rows[0].Value) {
		return nil
	}
	return &kobiplan.Money{Monthly: math.Round(rows[0].Value*hoursPerMonth*pricing.Factor*100) / 100, Currency: pricing.Currency, Source: "opencost"}
}

// workloadAllocationExpr is the workload's hourly OpenCost cost, the sum the
// baseline averages (workloadAllocationQuery) before it is windowed.
func workloadAllocationExpr(t kobiplan.Target) string {
	pods := workloadPodRegex(t)
	if pods == "" || t.Namespace == "" {
		return ""
	}
	sel := fmt.Sprintf(`{namespace=%q,pod=~"%s"}`, t.Namespace, pods)
	return `sum(container_cpu_allocation` + sel + ` * on(node) group_left max by (node) (node_cpu_hourly_cost)) + ` +
		`sum((container_memory_allocation_bytes` + sel + ` / 1073741824) * on(node) group_left max by (node) (node_ram_hourly_cost))`
}

// p95Since is the P95 per pod since the step was applied, for each
// dimension that has a limit (the only ones the check reads).
func (h *handlers) p95Since(ctx context.Context, clusterID string, t kobiplan.Target, live kobiplan.SetResourcesParams, from, now time.Time) map[bool]int64 {
	out := map[bool]int64{}
	pods := workloadPodRegex(t)
	if pods == "" {
		return out
	}
	window := promWindow(now.Sub(from))
	for _, c := range live.Containers {
		if c.Limits == nil {
			continue
		}
		sel := fmt.Sprintf(`{namespace=%q,pod=~"%s",container=%q}`, t.Namespace, pods, c.Container)
		read := func(q string, scale float64) (int64, bool) {
			rows, err := runInstantQuery(ctx, scopeQueryByTenant(scopeQueryByCluster(q, clusterID), metricsTenantPinCtx(ctx)))
			if err != nil || len(rows) == 0 || math.IsNaN(rows[0].Value) {
				return 0, false
			}
			return int64(math.Round(rows[0].Value * scale)), true
		}
		if c.Limits.CPU != nil {
			if v, ok := read(`max(quantile_over_time(0.95, rate(container_cpu_usage_seconds_total`+sel+`[5m])`+window+`))`, 1000); ok && v > out[false] {
				out[false] = v
			}
		}
		if c.Limits.Memory != nil {
			if v, ok := read(`max(quantile_over_time(0.95, container_memory_working_set_bytes`+sel+window+`))`, 1); ok && v > out[true] {
				out[true] = v
			}
		}
	}
	return out
}

// promWindow is a subquery range for a duration: hourly points over a day or
// more, a minute apart below (a test stack's short windows).
func promWindow(d time.Duration) string {
	step := "1h"
	if d < 24*time.Hour {
		step = "1m"
	}
	secs := int64(d.Seconds())
	if secs < 300 {
		secs = 300
	}
	return fmt.Sprintf("[%ds:%s]", secs, step)
}

// workloadPodsOf is the pods its controller made, matched by the name it
// gives them (the same rule as the baseline's queries).
func workloadPodsOf(conn *cluster.Connector, t kobiplan.Target) []*corev1.Pod {
	var suffix string
	switch t.Type {
	case "deployments":
		suffix = `-[a-z0-9]{6,12}-[a-z0-9]{5}`
	case "statefulsets":
		suffix = `-[0-9]+`
	case "daemonsets":
		suffix = `-[a-z0-9]{5}`
	default:
		return nil
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(t.Name) + suffix + `$`)
	var out []*corev1.Pod
	for _, pod := range conn.GetPods() {
		if pod.Namespace == t.Namespace && re.MatchString(pod.Name) && pod.DeletionTimestamp == nil {
			out = append(out, pod)
		}
	}
	return out
}

// podTroubleSince says whether a container was OOMKilled since the step was
// applied, and how many containers restarted since then (their last
// termination finished after it).
func podTroubleSince(pods []*corev1.Pod, since time.Time) (oom bool, restarts int32) {
	for _, pod := range pods {
		for _, cs := range pod.Status.ContainerStatuses {
			if term := cs.LastTerminationState.Terminated; term != nil && !term.FinishedAt.Time.Before(since) {
				restarts++
				if term.Reason == "OOMKilled" {
					oom = true
				}
			}
			if term := cs.State.Terminated; term != nil && term.Reason == "OOMKilled" && !term.FinishedAt.Time.Before(since) {
				oom = true
			}
		}
	}
	return oom, restarts
}

// rolloutComplete: the controller has seen the latest spec and every replica
// runs it and is available.
func rolloutComplete(conn *cluster.Connector, t kobiplan.Target) bool {
	switch t.Type {
	case "deployments":
		for _, d := range conn.GetDeployments() {
			if d.Namespace == t.Namespace && d.Name == t.Name {
				want := int32(1)
				if d.Spec.Replicas != nil {
					want = *d.Spec.Replicas
				}
				st := d.Status
				return st.ObservedGeneration >= d.Generation && st.UpdatedReplicas == want && st.AvailableReplicas == want && st.Replicas == want
			}
		}
	case "statefulsets":
		for _, s := range conn.GetStatefulSets() {
			if s.Namespace == t.Namespace && s.Name == t.Name {
				want := int32(1)
				if s.Spec.Replicas != nil {
					want = *s.Spec.Replicas
				}
				st := s.Status
				return st.ObservedGeneration >= s.Generation && st.UpdatedReplicas == want && st.ReadyReplicas == want &&
					(st.UpdateRevision == "" || st.CurrentRevision == st.UpdateRevision)
			}
		}
	case "daemonsets":
		for _, d := range conn.GetDaemonSets() {
			if d.Namespace == t.Namespace && d.Name == t.Name {
				st := d.Status
				return st.ObservedGeneration >= d.Generation && st.UpdatedNumberScheduled == st.DesiredNumberScheduled && st.NumberAvailable == st.DesiredNumberScheduled
			}
		}
	}
	return false
}

// verifyPlan reads one plan again under its lock, marks a draft past its
// expiry, verifies an exported plan (or measures a verified one) and stores
// what moved. It returns the plan as stored.
func (h *handlers) verifyPlan(ctx context.Context, store copilot.PlanStore, tenant string, conn *cluster.Connector, id string, now time.Time) (*kobiplan.Plan, error) {
	lock := planExportLock(id)
	lock.Lock()
	defer lock.Unlock()
	p, err := store.Get(tenant, id)
	if err != nil {
		return nil, err
	}
	if p.Status == kobiplan.StatusDraft && !p.ExpiresAt.After(now) {
		p.Status, p.ExpiredReason = kobiplan.StatusExpired, kobiplan.ExpiredUnexported
		p.Notes = append(p.Notes, kobiplan.Note{Version: p.Version, At: now, Text: "Expired: nobody exported it within 7 days."})
		return p, store.Update(*p)
	}
	if p.Status != kobiplan.StatusExported && !(p.Status == kobiplan.StatusVerified && p.Impact.Observed == nil) {
		return p, nil
	}
	if conn == nil {
		return p, errPlanClusterOffline
	}
	pricing, _ := h.planPricing(ctx, p.ClusterID)
	changed, content := p.Verify(h.planFacts(ctx, conn, *p, now), pricing, now)
	switch {
	case content:
		err = store.Revise(*p)
	case changed:
		err = store.Update(*p)
	default:
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return store.Get(tenant, id)
}

var errPlanClusterOffline = errors.New("the plan's cluster is not connected")

// connectorForPlan is the plan's own cluster's connector: the request's when
// it is that cluster, else any live runtime of the org on it.
func (h *handlers) connectorForPlan(ctx context.Context, tenant, clusterID string) *cluster.Connector {
	if h.manager == nil {
		return nil
	}
	if conn := h.manager.Connector(ctx); conn != nil && h.activeClusterUID(ctx) == clusterID {
		return conn
	}
	var found *cluster.Connector
	h.manager.ForEachLiveConnector(func(t, _ string, conn *cluster.Connector) {
		if found == nil && conn.ClusterUID() == clusterID && sweepTenant(t) == tenant {
			found = conn
		}
	})
	return found
}

// POST /plans/{id}/verify («Verify now»): the same as the hourly sweep, for
// one plan, now. Editor; it changes nothing in the cluster.
func (h *handlers) handleVerifyPlan(w http.ResponseWriter, r *http.Request) {
	store, p, ok := h.readablePlan(w, r)
	if !ok {
		return
	}
	tenant := auth.ContextTenantID(r)
	now := time.Now().UTC()
	conn := h.connectorForPlan(r.Context(), tenant, p.ClusterID)
	stored, err := h.verifyPlan(r.Context(), store, tenant, conn, chi.URLParam(r, "id"), now)
	switch {
	case errors.Is(err, errPlanClusterOffline):
		respondError(w, http.StatusServiceUnavailable, "the plan's cluster is not connected, so it cannot be verified now")
		return
	case err != nil:
		respondError(w, http.StatusInternalServerError, "could not verify the plan")
		return
	}
	respondJSON(w, http.StatusOK, h.planViewOf(r, *stored, now))
}

// The hourly sweep. NewRouter leaves its handlers here; main starts it.
var planSweepHandlers atomic.Pointer[handlers]

var planSweepJob *opsmetrics.Job

// ConfigureKobiPlans reads the plan timings a test stack may shorten (main,
// before the server and the sweep start): when a step is checked
// (KUBEBOLT_KOBI_PLAN_VERIFY_AFTER, 24 h), when its impact is measured
// (KUBEBOLT_KOBI_PLAN_IMPACT_AFTER, 7 days) and how often the sweep runs
// (KUBEBOLT_KOBI_PLAN_SWEEP_INTERVAL, 1 h).
func ConfigureKobiPlans() {
	kobiplan.VerifyAfter = envDuration("KUBEBOLT_KOBI_PLAN_VERIFY_AFTER", kobiplan.VerifyAfter)
	kobiplan.ImpactAfter = envDuration("KUBEBOLT_KOBI_PLAN_IMPACT_AFTER", kobiplan.ImpactAfter)
	planSweepInterval = envDuration("KUBEBOLT_KOBI_PLAN_SWEEP_INTERVAL", time.Hour)
}

var planSweepInterval = time.Hour

// RunKobiPlanSweep verifies, every interval, the exported plans of every
// live cluster (and measures the verified ones whose impact is due), and
// marks drafts past their expiry. Stops with ctx.
func RunKobiPlanSweep(ctx context.Context) {
	interval := planSweepInterval
	planSweepJob = opsmetrics.NewJob("kobi_plan_sweep", interval)
	first := time.NewTimer(min(2*time.Minute, interval))
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		sweepPlans(ctx, time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sweepPlans(ctx context.Context, now time.Time) {
	h, store := planSweepHandlers.Load(), kobiPlanStore()
	if h == nil || store == nil || h.manager == nil {
		return
	}
	run := planSweepJob.Start()
	defer run.End()
	verified, moved := 0, 0
	seen := map[string]bool{}
	h.manager.ForEachLiveConnector(func(runtimeTenant, _ string, conn *cluster.Connector) {
		uid := conn.ClusterUID()
		tenant := sweepTenant(runtimeTenant)
		// The active slot and a parked runtime can hold the same cluster.
		if uid == "" || seen[tenant+"|"+uid] {
			return
		}
		seen[tenant+"|"+uid] = true
		tctx := auth.WithTenantID(ctx, tenant)
		plans, err := store.List(tenant, copilot.PlanFilter{ClusterID: uid, Statuses: []kobiplan.Status{kobiplan.StatusDraft, kobiplan.StatusExported, kobiplan.StatusVerified}})
		if err != nil {
			slog.Warn("kobi plan sweep: could not list plans", slog.String("cluster", uid), slog.Any("error", err))
			return
		}
		for _, p := range plans {
			if p.Status == kobiplan.StatusVerified && p.Impact.Observed != nil {
				continue
			}
			before := p.UpdatedAt
			got, err := h.verifyPlan(tctx, store, tenant, conn, p.ID, now)
			if err != nil {
				slog.Warn("kobi plan sweep: could not verify a plan", slog.String("plan", p.ID), slog.Any("error", err))
				continue
			}
			verified++
			if got != nil && got.UpdatedAt.After(before) {
				moved++
			}
		}
	})
	run.OK()
	if moved > 0 {
		slog.Info("kobi plan sweep", slog.Int("plans", verified), slog.Int("moved", moved))
	}
}

// sweepTenant is the org a runtime belongs to as the plan store keys it: a
// single-tenant install has no org on its runtimes and keeps its plans under
// the default one.
func sweepTenant(runtimeTenant string) string {
	if runtimeTenant == "" {
		return auth.DefaultTenantName
	}
	return runtimeTenant
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		slog.Warn("ignoring an invalid duration", slog.String("env", name), slog.String("value", v))
	}
	return def
}
