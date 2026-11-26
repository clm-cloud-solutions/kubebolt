package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// POST /plans/{id}/discard (plans-06, P21): a person ends a live plan. It
// stops being live — Free's one plan per cluster is free again and its
// workloads can go into a new plan — and nothing changes in the cluster.
func (h *handlers) handleDiscardPlan(w http.ResponseWriter, r *http.Request) {
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
	if err := p.Discard(h.exporterName(r), now); err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	err := store.Update(*p)
	auditMutationOn(r, p.ClusterID, "discard_plan", "kobi-plan", "", p.ID, nil, err)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not discard the plan")
		return
	}
	respondJSON(w, http.StatusOK, h.planViewOf(r, *p, now))
}
