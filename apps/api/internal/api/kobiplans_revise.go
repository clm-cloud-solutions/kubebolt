package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/kobiplan"
)

// PUT /plans/{id}/steps/{step} (plans-06, d85): a person sets a pending
// step's values in the plan («the engine says 10m, I'd rather keep 30m»).
// The body is the step's own change, set-resources' shape. Taken values make
// a new version; a value under the per-pod P95 is refused with that figure
// (422) and the refusal stays in the plan's notes (kobiplan.ReviseStep).
func (h *handlers) handleReviseStep(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	values, err := kobiplan.DecodeSetResources(json.RawMessage(raw))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid body: "+err.Error())
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
	if status, _ := effectiveStatus(*p, now); !status.Live() {
		respondError(w, http.StatusConflict, "this plan is no longer live")
		return
	}
	stepID := chi.URLParam(r, "step")
	step := p.StepByID(stepID)
	if step == nil {
		respondError(w, http.StatusNotFound, "step not found")
		return
	}
	by := h.exporterName(r)
	pricing, _ := h.planPricing(r.Context(), p.ClusterID)
	changed, err := p.ReviseStep(stepID, values, pricing, by, now)
	var rejected *kobiplan.ValueRejected
	switch {
	case errors.As(err, &rejected):
		// The refusal stays in the plan, with the figure that justifies it.
		p.Notes = append(p.Notes, kobiplan.Note{Version: p.Version, At: now, Text: kobiplan.RejectedNote(*step, by, rejected.Reason)})
		werr := store.Update(*p)
		auditMutationOn(r, p.ClusterID, "revise_plan_step", "kobi-plan", "", p.ID, map[string]any{"step": stepID}, err)
		if werr != nil {
			respondError(w, http.StatusInternalServerError, "could not record the refusal")
			return
		}
		respondError(w, http.StatusUnprocessableEntity, rejected.Reason)
		return
	case err != nil:
		respondError(w, http.StatusBadRequest, err.Error())
		return
	case !changed:
		respondJSON(w, http.StatusOK, h.planViewOf(r, *p, now))
		return
	}
	err = store.Revise(*p)
	auditMutationOn(r, p.ClusterID, "revise_plan_step", "kobi-plan", "", p.ID, map[string]any{"step": stepID}, err)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not save the plan")
		return
	}
	stored, err := store.Get(p.OrgID, p.ID)
	if err != nil {
		stored = p
	}
	respondJSON(w, http.StatusOK, h.planViewOf(r, *stored, now))
}

// ReviseStep is revise_plan's source (plans-06): a person's values, as Kobi
// transcribed them, for the pending step of the active cluster's live plan
// that changes that workload. Found by the workload (and the plan's title
// when several have it), never by an id. The same rules as Edit values:
// refused under the per-pod P95 (noted in the plan), a new version otherwise.
func (s planSource) ReviseStep(ctx context.Context, req copilot.ReviseStepRequest) (copilot.ReviseStepOutcome, error) {
	clusterID := s.h.activeClusterUID(ctx)
	if clusterID == "" {
		return copilot.ReviseStepOutcome{}, errNoClusterID
	}
	return s.reviseStepOn(ctx, clusterID, req)
}

func (s planSource) reviseStepOn(ctx context.Context, clusterID string, req copilot.ReviseStepRequest) (copilot.ReviseStepOutcome, error) {
	live, err := s.store.LiveForCluster(s.tenant, clusterID)
	if err != nil {
		return copilot.ReviseStepOutcome{}, err
	}
	now := time.Now().UTC()
	type hit struct{ planID, stepID, title string }
	var hits []hit
	for _, p := range live {
		if st, _ := effectiveStatus(p, now); !st.Live() {
			continue
		}
		if req.Plan != "" && !strings.EqualFold(strings.TrimSpace(p.Title), req.Plan) {
			continue
		}
		for _, st := range p.Steps {
			if st.Status == kobiplan.StepPending && workloadIs(st.Target, req.Workload) {
				hits = append(hits, hit{p.ID, st.ID, p.Title})
			}
		}
	}
	switch len(hits) {
	case 0:
		return copilot.ReviseStepOutcome{}, fmt.Errorf("no live plan of this cluster has a pending step for %s", req.Workload)
	case 1:
	default:
		titles := make([]string, 0, len(hits))
		for _, h := range hits {
			titles = append(titles, "«"+h.title+"»")
		}
		return copilot.ReviseStepOutcome{}, fmt.Errorf("several live plans have a pending step for %s (%s): ask the person which", req.Workload, strings.Join(titles, ", "))
	}
	h := hits[0]
	lock := planExportLock(h.planID)
	lock.Lock()
	defer lock.Unlock()
	p, err := s.store.Get(s.tenant, h.planID)
	if err != nil {
		return copilot.ReviseStepOutcome{}, err
	}
	step := p.StepByID(h.stepID)
	if step == nil || step.Status != kobiplan.StepPending {
		return copilot.ReviseStepOutcome{}, fmt.Errorf("the step for %s is no longer pending", req.Workload)
	}
	values, err := personsValues(*step, req)
	if err != nil {
		return copilot.ReviseStepOutcome{}, err
	}
	by := s.userName
	if s.h.authHandlers != nil {
		if n := s.h.authHandlers.UserDisplayName(ctx, s.user); n != "" {
			by = n
		}
	}
	pricing, _ := s.h.planPricing(ctx, clusterID)
	changed, err := p.ReviseStep(h.stepID, values, pricing, by, now)
	var rejected *kobiplan.ValueRejected
	switch {
	case errors.As(err, &rejected):
		p.Notes = append(p.Notes, kobiplan.Note{Version: p.Version, At: now, Text: kobiplan.RejectedNote(*step, by, rejected.Reason)})
		if err := s.store.Update(*p); err != nil {
			return copilot.ReviseStepOutcome{}, err
		}
		return copilot.ReviseStepOutcome{Plan: p, Order: step.Order, Refused: rejected.Reason}, nil
	case err != nil:
		return copilot.ReviseStepOutcome{}, err
	case !changed:
		return copilot.ReviseStepOutcome{Plan: p, Order: step.Order}, nil
	}
	if err := s.store.Revise(*p); err != nil {
		return copilot.ReviseStepOutcome{}, err
	}
	stored, err := s.store.Get(s.tenant, h.planID)
	if err != nil {
		stored = p
	}
	return copilot.ReviseStepOutcome{Plan: stored, Order: step.Order, Changed: true}, nil
}

// workloadIs matches a step's target against namespace/kind/name or
// namespace/name; the kind may be singular, plural or capitalized.
func workloadIs(t kobiplan.Target, w string) bool {
	parts := strings.Split(strings.TrimSpace(w), "/")
	switch len(parts) {
	case 2:
		return parts[0] == t.Namespace && parts[1] == t.Name
	case 3:
		kind := strings.ToLower(parts[1])
		if !strings.HasSuffix(kind, "s") {
			kind += "s"
		}
		return parts[0] == t.Namespace && kind == t.Type && parts[2] == t.Name
	}
	return false
}

// personsValues is the step's change with the values the person gave: one
// container (the step's own when it has one), the step's other values kept.
// ReviseStep refuses a dimension the step does not change.
func personsValues(step kobiplan.Step, req copilot.ReviseStepRequest) (kobiplan.SetResourcesParams, error) {
	params, err := kobiplan.DecodeSetResources(step.Params)
	if err != nil {
		return params, err
	}
	name := req.Container
	if name == "" {
		if len(params.Containers) != 1 {
			return params, fmt.Errorf("the step changes several containers: say which")
		}
		name = params.Containers[0].Container
	}
	found := false
	for i := range params.Containers {
		c := &params.Containers[i]
		if c.Container != name {
			continue
		}
		found = true
		c.Requests = withValues(c.Requests, req.Requests)
		c.Limits = withValues(c.Limits, req.Limits)
	}
	if !found {
		return params, fmt.Errorf("the step does not change container %s", name)
	}
	return params, nil
}

func withValues(r *kobiplan.Resources, v map[string]string) *kobiplan.Resources {
	if len(v) == 0 {
		return r
	}
	out := kobiplan.Resources{}
	if r != nil {
		out = *r
	}
	if cpu, ok := v["cpu"]; ok {
		out.CPU = &cpu
	}
	if mem, ok := v["memory"]; ok {
		out.Memory = &mem
	}
	return &out
}
