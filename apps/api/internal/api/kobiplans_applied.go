package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// A plan step applied from KubeBolt (plans-15, d85). When a resources change
// on a workload touches a pending step of a live plan (same workload,
// container and dimension, whatever the value), the plan records it: the
// step is applied, the plan becomes exported through the apply channel
// (so it stops expiring and its baseline is measured), and values the person
// chose replace the step's in a new version that keeps the engine's. Matched
// on the server, by what the change is: no plan id travels through Kobi or
// the request. Only steps applied in the cluster (no owner, or none
// identified) and only while the workload is as the plan saw it.

// planStepLink is one pending step a change touches.
type planStepLink struct {
	planID, stepID string
	title          string
	order          int
}

// templateOf reads a workload's pod template from the connector's caches.
func templateOf(conn *cluster.Connector, typ, namespace, name string) (corev1.PodSpec, bool) {
	switch typ {
	case "deployments":
		for _, d := range conn.GetDeployments() {
			if d.Namespace == namespace && d.Name == name {
				return d.Spec.Template.Spec, true
			}
		}
	case "statefulsets":
		for _, s := range conn.GetStatefulSets() {
			if s.Namespace == namespace && s.Name == name {
				return s.Spec.Template.Spec, true
			}
		}
	case "daemonsets":
		for _, d := range conn.GetDaemonSets() {
			if d.Namespace == namespace && d.Name == name {
				return d.Spec.Template.Spec, true
			}
		}
	}
	return corev1.PodSpec{}, false
}

// liveSpecHash is the SpecHash of a workload's template as the caches hold
// it now: a step applies only while it equals the step's precondition.
func liveSpecHash(conn *cluster.Connector, typ, namespace, name string) string {
	if conn == nil {
		return ""
	}
	tpl, ok := templateOf(conn, typ, namespace, name)
	if !ok {
		return ""
	}
	return kobiplan.SpecHash(tpl)
}

// touchedPlanSteps finds the pending steps of the cluster's live plans that a
// change touches, most recent plan first. liveHash is the workload's
// liveSpecHash; empty matches nothing.
func touchedPlanSteps(store copilot.PlanStore, tenant, clusterID, liveHash, typ, namespace, name string, applied kobiplan.SetResourcesParams, now time.Time) []planStepLink {
	if store == nil || clusterID == "" || liveHash == "" {
		return nil
	}
	live, err := store.LiveForCluster(tenant, clusterID)
	if err != nil || len(live) == 0 {
		return nil
	}
	hash := liveHash
	sort.SliceStable(live, func(i, j int) bool { return live[i].CreatedAt.After(live[j].CreatedAt) })
	var out []planStepLink
	for _, p := range live {
		if status, _ := effectiveStatus(p, now); status == kobiplan.StatusExpired {
			continue
		}
		for _, s := range p.Steps {
			if s.Status != kobiplan.StepPending || !s.AppliedInCluster() || s.Precondition.SpecHash != hash {
				continue
			}
			if s.Touches(typ, namespace, name, applied) {
				out = append(out, planStepLink{planID: p.ID, stepID: s.ID, title: p.Title, order: s.Order})
			}
		}
	}
	return out
}

// appliedPlanStep is what the set-resources answer says about a plan step
// the change applied.
type appliedPlanStep struct {
	Plan       string `json:"plan"`
	Step       int    `json:"step"`
	AsProposed bool   `json:"asProposed"`
}

// recordAppliedPlanSteps writes into each plan that the change applied its
// step. Called after the change succeeded; a failure here is logged and never
// turns a change that worked into an error.
func (h *handlers) recordAppliedPlanSteps(r *http.Request, clusterID string, links []planStepLink, applied kobiplan.SetResourcesParams, baselines map[string]planBaselines) []appliedPlanStep {
	store := kobiPlanStore()
	if store == nil || len(links) == 0 {
		return nil
	}
	tenant := auth.ContextTenantID(r)
	by := h.exporterName(r)
	now := time.Now().UTC()
	var pricing *kobiplan.Pricing
	var out []appliedPlanStep
	for _, l := range links {
		res, err := h.recordAppliedStep(r.Context(), store, tenant, clusterID, l, applied, baselines[l.planID], by, now, &pricing)
		if err != nil {
			slog.Warn("kobi plan: could not record an applied step", slog.String("plan", l.planID), slog.String("step", l.stepID), slog.Any("error", err))
			continue
		}
		out = append(out, res)
	}
	return out
}

type planBaselines struct {
	plan  *kobiplan.Baseline
	steps map[string]*kobiplan.Baseline
}

func (h *handlers) recordAppliedStep(ctx context.Context, store copilot.PlanStore, tenant, clusterID string, l planStepLink, applied kobiplan.SetResourcesParams, base planBaselines, by string, now time.Time, pricing **kobiplan.Pricing) (appliedPlanStep, error) {
	lock := planExportLock(l.planID)
	lock.Lock()
	defer lock.Unlock()
	p, err := store.Get(tenant, l.planID)
	if err != nil {
		return appliedPlanStep{}, err
	}
	pending := false
	for _, s := range p.Steps {
		if s.ID == l.stepID && s.Status == kobiplan.StepPending {
			pending = true
		}
	}
	if !pending {
		return appliedPlanStep{}, errors.New("the step is no longer pending")
	}
	if *pricing == nil {
		pr, _ := h.planPricing(ctx, clusterID)
		*pricing = &pr
	}
	// The export first: it keeps the baselines of the steps still pending,
	// this one included (measured before the change).
	p.RecordExport(kobiplan.ExportApply, by, now, base.plan, base.steps)
	changed, err := p.ApplyStep(l.stepID, applied, **pricing, by, now)
	if err != nil {
		return appliedPlanStep{}, err
	}
	if !changed {
		p.Notes = append(p.Notes, kobiplan.Note{Version: p.Version, At: now, Text: appliedAsProposedNote(l, by)})
	}
	if changed {
		err = store.Revise(*p)
	} else {
		err = store.Update(*p)
	}
	if err != nil {
		return appliedPlanStep{}, err
	}
	return appliedPlanStep{Plan: l.title, Step: l.order, AsProposed: !changed}, nil
}

func appliedAsProposedNote(l planStepLink, by string) string {
	if by == "" {
		return "Step " + strconv.Itoa(l.order) + " applied as proposed."
	}
	return "Step " + strconv.Itoa(l.order) + " applied as proposed by " + by + "."
}

// measureLinkedBaselines measures, before the change, the baseline of every
// linked plan that has none yet (P20: the cost before anything is applied).
func (h *handlers) measureLinkedBaselines(ctx context.Context, store copilot.PlanStore, tenant string, links []planStepLink) map[string]planBaselines {
	out := map[string]planBaselines{}
	for _, l := range links {
		if _, done := out[l.planID]; done {
			continue
		}
		p, err := store.Get(tenant, l.planID)
		if err != nil || p.Baseline != nil {
			out[l.planID] = planBaselines{}
			continue
		}
		plan, steps := h.measurePlanBaseline(ctx, *p, time.Now().UTC())
		out[l.planID] = planBaselines{plan: plan, steps: steps}
	}
	return out
}

// setResourcesChange is a set-resources request as a plan reads it.
func setResourcesChange(rows []containerResourcesPatch) kobiplan.SetResourcesParams {
	raw, _ := json.Marshal(setResourcesRequest{Containers: rows})
	var out kobiplan.SetResourcesParams
	_ = json.Unmarshal(raw, &out)
	// An empty value means "leave it alone" in a set-resources request.
	for i := range out.Containers {
		c := &out.Containers[i]
		c.Requests = dropEmpty(c.Requests)
		c.Limits = dropEmpty(c.Limits)
	}
	return out
}

func dropEmpty(r *kobiplan.Resources) *kobiplan.Resources {
	if r == nil {
		return nil
	}
	if r.CPU != nil && *r.CPU == "" {
		r.CPU = nil
	}
	if r.Memory != nil && *r.Memory == "" {
		r.Memory = nil
	}
	if r.CPU == nil && r.Memory == nil {
		return nil
	}
	return r
}

// MatchStep is create_plan's source answering, for a proposed resources
// change, which plan step it would apply (the card's line and the plan's
// risk). The same match the set-resources handler makes when it runs.
// ReadPlan: one of the active cluster's plans, by id (the plan the person is
// looking at).
func (s planSource) ReadPlan(ctx context.Context, id string) (*kobiplan.Plan, error) {
	p, err := s.store.Get(s.tenant, id)
	if err != nil {
		return nil, err
	}
	if clusterID := s.h.activeClusterUID(ctx); clusterID == "" || p.ClusterID != clusterID {
		return nil, copilot.ErrPlanOtherCluster
	}
	return p, nil
}

func (s planSource) MatchStep(ctx context.Context, typ, namespace, name string, containers []map[string]interface{}) *copilot.PlanStepMatch {
	raw, err := json.Marshal(map[string]any{"containers": containers})
	if err != nil {
		return nil
	}
	change, err := kobiplan.DecodeSetResources(raw)
	if err != nil {
		return nil
	}
	hash := liveSpecHash(s.h.manager.Connector(ctx), typ, namespace, name)
	links := touchedPlanSteps(s.store, s.tenant, s.h.activeClusterUID(ctx), hash, typ, namespace, name, change, time.Now().UTC())
	if len(links) == 0 {
		return nil
	}
	l := links[0]
	p, err := s.store.Get(s.tenant, l.planID)
	if err != nil {
		return nil
	}
	for _, st := range p.Steps {
		if st.ID != l.stepID {
			continue
		}
		risk, _, deviates, below := st.AppliedRisk(change)
		return &copilot.PlanStepMatch{Order: st.Order, PlanTitle: p.Title, Risk: string(risk), Deviates: deviates, BelowEngine: below}
	}
	return nil
}
