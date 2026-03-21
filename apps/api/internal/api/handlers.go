package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

type handlers struct {
	manager *cluster.Manager
	wsHub   *websocket.Hub
}

func (h *handlers) listClusters(w http.ResponseWriter, r *http.Request) {
	clusters := h.manager.ListClusters()
	respondJSON(w, http.StatusOK, clusters)
}

func (h *handlers) switchCluster(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Context string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Context == "" {
		respondError(w, http.StatusBadRequest, "context is required")
		return
	}

	if err := h.manager.SwitchCluster(body.Context); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Broadcast cluster switch event
	h.wsHub.Broadcast("cluster.switched", map[string]string{"context": body.Context})

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"context": body.Context,
	})
}

func (h *handlers) getClusterOverview(w http.ResponseWriter, r *http.Request) {
	overview := h.manager.Connector().GetOverview()
	respondJSON(w, http.StatusOK, overview)
}

func (h *handlers) getClusterHealth(w http.ResponseWriter, r *http.Request) {
	allInsights := h.manager.Engine().GetAllInsights()
	health := h.manager.Connector().GetHealth(h.manager.Collector().IsAvailable(), allInsights)
	respondJSON(w, http.StatusOK, health)
}

func (h *handlers) getResources(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := r.URL.Query().Get("namespace")
	search := r.URL.Query().Get("search")
	status := r.URL.Query().Get("status")
	sortBy := r.URL.Query().Get("sort")
	order := r.URL.Query().Get("order")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}

	result := h.manager.Connector().GetResources(resourceType, namespace, search, status, sortBy, order, page, limit)
	respondJSON(w, http.StatusOK, result)
}

func (h *handlers) getResourceDetail(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	detail, err := h.manager.Connector().GetResourceDetail(resourceType, namespace, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, detail)
}

func (h *handlers) getTopology(w http.ResponseWriter, r *http.Request) {
	topology := h.manager.Connector().GetTopology()
	respondJSON(w, http.StatusOK, topology)
}

func (h *handlers) getInsights(w http.ResponseWriter, r *http.Request) {
	severity := r.URL.Query().Get("severity")
	resolvedStr := r.URL.Query().Get("resolved")
	resolved := resolvedStr == "true"

	items := h.manager.Engine().GetInsights(severity, resolved)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

func (h *handlers) getEvents(w http.ResponseWriter, r *http.Request) {
	eventType := r.URL.Query().Get("type")
	namespace := r.URL.Query().Get("namespace")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 100
	}

	result := h.manager.Connector().GetEvents(eventType, namespace, limit)
	respondJSON(w, http.StatusOK, result)
}

func (h *handlers) getMetrics(w http.ResponseWriter, r *http.Request) {
	metricType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	switch metricType {
	case "pods":
		m := h.manager.Collector().GetPodMetrics(namespace, name)
		if m == nil {
			respondError(w, http.StatusNotFound, "metrics not found")
			return
		}
		respondJSON(w, http.StatusOK, m)
	case "nodes":
		m := h.manager.Collector().GetNodeMetrics(name)
		if m == nil {
			respondError(w, http.StatusNotFound, "metrics not found")
			return
		}
		respondJSON(w, http.StatusOK, m)
	default:
		respondError(w, http.StatusBadRequest, "unsupported metric type")
	}
}

func (h *handlers) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	websocket.ServeWS(h.wsHub, w, r)
}
