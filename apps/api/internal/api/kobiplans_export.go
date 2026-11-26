package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// Exporting a plan (plans-05, d84: a button first, Kobi later). GET renders
// the text for the screen; POST is the person taking it (Copy or Download):
// the export is recorded, the plan becomes exported, and the first time its
// baseline is measured (P20). Both sit next to the plan reads, outside
// requireConnector: the text comes from the stored plan. The cluster is read
// only for what makes the text exact (the Helm release values) and is
// optional: without it a Helm change is said in words.

const (
	planBaselineDays    = 7
	planBaselineTimeout = 15 * time.Second
	planReleaseTimeout  = 5 * time.Second
	hoursPerMonth       = 730
)

// planExportLocks serializes exports of one plan in this process: an export
// reads the plan, adds its record and writes it back.
var planExportLocks sync.Map

func planExportLock(id string) *sync.Mutex {
	m, _ := planExportLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

type planExportResponse struct {
	Plan   *planView         `json:"plan,omitempty"`
	Export kobiplan.Rendered `json:"export"`
}

func parseExportFormat(s string) (kobiplan.ExportFormat, bool) {
	switch f := kobiplan.ExportFormat(s); f {
	case kobiplan.ExportCommands, kobiplan.ExportDiff:
		return f, true
	}
	return "", false
}

// readablePlan loads a plan the caller may read, answering the request
// itself when it cannot.
func (h *handlers) readablePlan(w http.ResponseWriter, r *http.Request) (copilot.PlanStore, *kobiplan.Plan, bool) {
	store := kobiPlanStore()
	if store == nil {
		respondError(w, http.StatusServiceUnavailable, "plans are not enabled on this install")
		return nil, nil, false
	}
	p, err := store.Get(auth.ContextTenantID(r), chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, copilot.ErrPlanNotFound):
		respondError(w, http.StatusNotFound, "plan not found")
		return nil, nil, false
	case err != nil:
		respondError(w, http.StatusInternalServerError, "could not read the plan")
		return nil, nil, false
	}
	if !ClusterScopeFrom(r.Context()).May(p.ClusterID) {
		respondError(w, http.StatusNotFound, "plan not found")
		return nil, nil, false
	}
	return store, p, true
}

// GET /plans/{id}/export?format=commands|diff
func (h *handlers) handlePreviewPlanExport(w http.ResponseWriter, r *http.Request) {
	format, ok := parseExportFormat(r.URL.Query().Get("format"))
	if !ok {
		respondError(w, http.StatusBadRequest, "format must be commands or diff")
		return
	}
	_, p, ok := h.readablePlan(w, r)
	if !ok {
		return
	}
	out, err := h.renderPlanExport(r, *p, format, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not render the plan")
		return
	}
	respondJSON(w, http.StatusOK, planExportResponse{Export: out})
}

// POST /plans/{id}/export {"format": "commands"|"diff"}
func (h *handlers) handleExportPlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Format string `json:"format"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	format, ok := parseExportFormat(body.Format)
	if !ok {
		respondError(w, http.StatusBadRequest, "format must be commands or diff")
		return
	}
	lock := planExportLock(chi.URLParam(r, "id"))
	lock.Lock()
	defer lock.Unlock()

	store, p, ok := h.readablePlan(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	if status, _ := effectiveStatus(*p, now); status == kobiplan.StatusExpired {
		respondError(w, http.StatusConflict, "this plan expired: ask Kobi for a new one")
		return
	}
	if err := p.Exportable(); err != nil {
		respondError(w, http.StatusConflict, "this plan cannot be exported: "+err.Error())
		return
	}
	out, err := h.renderPlanExport(r, *p, format, now)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not render the plan")
		return
	}
	by := h.exporterName(r)
	var planBase *kobiplan.Baseline
	var stepBase map[string]*kobiplan.Baseline
	if p.Baseline == nil {
		planBase, stepBase = h.measurePlanBaseline(r.Context(), *p, now)
	}
	p.RecordExport(format, by, now, planBase, stepBase)
	p.Notes = append(p.Notes, kobiplan.Note{Version: p.Version, At: now, Text: exportNote(format, by)})
	err = store.Update(*p)
	auditMutationOn(r, p.ClusterID, "export_plan", "kobi-plan", "", p.ID, map[string]any{"format": string(format)}, err)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not record the export")
		return
	}
	view := h.planViewOf(r, *p, now)
	respondJSON(w, http.StatusOK, planExportResponse{Plan: &view, Export: out})
}

func exportNote(format kobiplan.ExportFormat, by string) string {
	what := "commands"
	if format == kobiplan.ExportDiff {
		what = "a diff"
	}
	if by == "" {
		return "Exported as " + what + "."
	}
	return "Exported as " + what + " by " + by + "."
}

// exporterName is the name an export is recorded under: the person's
// display name, else their username.
func (h *handlers) exporterName(r *http.Request) string {
	if h.authHandlers != nil {
		if n := h.authHandlers.UserDisplayName(r.Context(), auth.ContextUserID(r)); n != "" {
			return n
		}
	}
	return requestUserName(r)
}

func (h *handlers) renderPlanExport(r *http.Request, p kobiplan.Plan, format kobiplan.ExportFormat, at time.Time) (kobiplan.Rendered, error) {
	return kobiplan.RenderExport(kobiplan.ExportInput{
		Plan: p, Format: format, At: at, By: h.exporterName(r),
		ClusterName: h.clusterNames(r)(p.ClusterID),
		Releases:    h.planReleaseValues(r, p),
	})
}

// planReleaseValues reads the values of every Helm release the plan's steps
// belong to, from the plan's own cluster (not the one selected in the
// screen). A release that cannot be read is left out: its changes are then
// said in words.
func (h *handlers) planReleaseValues(r *http.Request, p kobiplan.Plan) map[string]*kobiplan.ReleaseValues {
	type rel struct{ ns, name string }
	var wanted []rel
	seen := map[rel]bool{}
	for _, s := range p.Steps {
		o := s.Target.Owner
		if o == nil || o.Kind != kobiplan.OwnerHelm || o.Ref == "" {
			continue
		}
		ns := o.Namespace
		if ns == "" {
			ns = s.Target.Namespace
		}
		k := rel{ns, o.Ref}
		if !seen[k] {
			seen[k] = true
			wanted = append(wanted, k)
		}
	}
	if len(wanted) == 0 || h.manager == nil {
		return nil
	}
	ctx := r.Context()
	if name := h.manager.ContextNameForClusterID(auth.ContextTenantID(r), p.ClusterID); name != "" {
		key := cluster.RuntimeKeyFromContext(ctx)
		key.Cluster = name
		ctx = cluster.WithRuntimeKey(ctx, key)
	}
	conn := h.manager.Connector(ctx)
	if conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, planReleaseTimeout)
	defer cancel()
	out := map[string]*kobiplan.ReleaseValues{}
	for _, w := range wanted {
		secrets, err := conn.ListHelmReleaseSecretsForRelease(ctx, w.ns, w.name)
		if err != nil {
			continue
		}
		ev, err := helm.DecodeEffectiveValues(w.ns, w.name, secrets)
		if err != nil {
			continue
		}
		out[w.ns+"/"+w.name] = &kobiplan.ReleaseValues{
			Chart: ev.Chart, ChartVersion: ev.ChartVersion, Revision: ev.Revision, Values: ev.Values,
		}
	}
	return out
}

// measurePlanBaseline is the cost before anything is applied (P20): what
// OpenCost allocates to each step's workload, and the cluster's cost, as the
// average of the last 7 days in monthly terms and the org's currency.
// Without OpenCost the baseline holds no money: the impact is then measured
// in resources only, from each step's values before.
func (h *handlers) measurePlanBaseline(ctx context.Context, p kobiplan.Plan, now time.Time) (*kobiplan.Baseline, map[string]*kobiplan.Baseline) {
	ctx, cancel := context.WithTimeout(ctx, planBaselineTimeout)
	defer cancel()
	base := func() *kobiplan.Baseline {
		return &kobiplan.Baseline{CapturedAt: now}
	}
	plan := base()
	steps := map[string]*kobiplan.Baseline{}
	for _, s := range p.Steps {
		steps[s.ID] = base()
	}
	pricing, _ := h.planPricing(ctx, p.ClusterID)
	if !pricing.Rates.Available() {
		return plan, steps
	}
	scope := func(q string) string {
		return scopeQueryByTenant(scopeQueryByCluster(q, p.ClusterID), metricsTenantPinCtx(ctx))
	}
	money := func(q string) *kobiplan.Money {
		rows, err := runInstantQuery(ctx, scope(q))
		if err != nil || len(rows) == 0 || math.IsNaN(rows[0].Value) {
			return nil
		}
		monthly := math.Round(rows[0].Value*hoursPerMonth*pricing.Factor*100) / 100
		return &kobiplan.Money{Monthly: monthly, Currency: pricing.Currency, Source: "opencost"}
	}
	// The hours the window has data for: an average is only over those, so
	// the baseline says how many days it covers, never a flat 7.
	days := 0.0
	if rows, err := runInstantQuery(ctx, scope(fmt.Sprintf(`count_over_time(sum(node_total_hourly_cost)[%dd:1h])`, planBaselineDays))); err == nil && len(rows) > 0 && !math.IsNaN(rows[0].Value) {
		days = math.Min(planBaselineDays, math.Round(rows[0].Value/24*10)/10)
	}
	if plan.ClusterCost = money(fmt.Sprintf(`avg_over_time((%s)[%dd:1h])`, clusterBillExpr, planBaselineDays)); plan.ClusterCost != nil {
		plan.WindowDays = days
		plan.ClusterCostCovers = kobiplan.CoversBill
	}
	for _, s := range p.Steps {
		if q := workloadAllocationQuery(s.Target); q != "" {
			if steps[s.ID].AllocatedCost = money(q); steps[s.ID].AllocatedCost != nil {
				steps[s.ID].WindowDays = days
			}
		}
	}
	return plan, steps
}

// clusterBillExpr is what the cluster costs per hour at OpenCost's list
// prices: its nodes, its disks (pv_hourly_cost is a price per GiB-hour, times
// each volume's size), its load balancers and the provider's management fee.
// Those are what the bill falls by when something is removed; freed requests
// alone only move cost into idle. Network egress is left out: OpenCost prices
// it per GiB and nobody collects the bytes. A part OpenCost does not send
// counts as zero; without the nodes there is no figure. The Cost tab's
// run-rate card lists the same parts (useClusterCost.ts).
const clusterBillExpr = `sum(node_total_hourly_cost)` +
	` + (sum(max by (persistentvolume) (pv_hourly_cost) * on (persistentvolume) (max by (persistentvolume) (kube_persistentvolume_capacity_bytes) or max by (persistentvolume) (pod_pvc_allocation)) / 1073741824) or vector(0))` +
	` + (sum(max by (namespace, service_name) (kubecost_load_balancer_cost)) or vector(0))` +
	` + (max(kubecost_cluster_management_cost) or vector(0))`

// clusterCostExpr is the expression a plan's cluster cost is measured on,
// after as before: the bill, or the nodes alone for a plan whose baseline
// was taken before the bill (26-nov).
func clusterCostExpr(b *kobiplan.Baseline) string {
	if b != nil && b.ClusterCostCovers == kobiplan.CoversBill {
		return clusterBillExpr
	}
	return `sum(node_total_hourly_cost)`
}

// workloadAllocationQuery is the hourly cost OpenCost allocates to one
// workload's pods, averaged over the baseline window: the Cost tab's
// attribution (allocation × the node's price), with the pods matched by the
// name their controller gives them.
func workloadAllocationQuery(t kobiplan.Target) string {
	pods := workloadPodRegex(t)
	if pods == "" || t.Namespace == "" {
		return ""
	}
	sel := fmt.Sprintf(`{namespace=%q,pod=~"%s"}`, t.Namespace, pods)
	cpu := `sum(container_cpu_allocation` + sel + ` * on(node) group_left max by (node) (node_cpu_hourly_cost))`
	mem := `sum((container_memory_allocation_bytes` + sel + ` / 1073741824) * on(node) group_left max by (node) (node_ram_hourly_cost))`
	// Each part averages only the hours it has data for. A zero for the
	// hours without data (`or vector(0)` inside the window) halved the
	// cost of every workload on a cluster with half a window of history.
	return fmt.Sprintf(`avg_over_time(%s[%dd:1h]) + avg_over_time(%s[%dd:1h])`, cpu, planBaselineDays, mem, planBaselineDays)
}

// workloadPodRegex matches the pods a controller names after its workload,
// as a PromQL string body (backslashes doubled).
func workloadPodRegex(t kobiplan.Target) string {
	name := strings.ReplaceAll(t.Name, ".", `\\.`)
	switch t.Type {
	case "deployments":
		return name + `-[a-z0-9]{6,12}-[a-z0-9]{5}`
	case "statefulsets":
		return name + `-[0-9]+`
	case "daemonsets":
		return name + `-[a-z0-9]{5}`
	}
	return ""
}
