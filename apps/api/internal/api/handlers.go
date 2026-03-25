package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

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
		// "not found in kubeconfig" is a bad-request; anything else is a connection failure
		status := http.StatusServiceUnavailable
		if strings.Contains(err.Error(), "not found in kubeconfig") {
			status = http.StatusBadRequest
		}
		respondError(w, status, err.Error())
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
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	respondJSON(w, http.StatusOK, conn.GetOverview())
}

func (h *handlers) getClusterHealth(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	eng := h.manager.Engine()
	col := h.manager.Collector()
	if conn == nil || eng == nil || col == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	health := conn.GetHealth(col.IsAvailable(), eng.GetAllInsights())
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

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	result := conn.GetResources(resourceType, namespace, search, status, sortBy, order, page, limit)
	respondJSON(w, http.StatusOK, result)
}

func (h *handlers) getResourceDetail(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// "_" is used as a placeholder for cluster-scoped resources (nodes, PVs, etc.)
	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	detail, err := conn.GetResourceDetail(resourceType, namespace, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, detail)
}

func (h *handlers) getTopology(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	respondJSON(w, http.StatusOK, conn.GetTopology())
}

func (h *handlers) getInsights(w http.ResponseWriter, r *http.Request) {
	eng := h.manager.Engine()
	if eng == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	severity := r.URL.Query().Get("severity")
	resolvedStr := r.URL.Query().Get("resolved")
	resolved := resolvedStr == "true"

	items := eng.GetInsights(severity, resolved)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

func (h *handlers) getEvents(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	eventType := r.URL.Query().Get("type")
	namespace := r.URL.Query().Get("namespace")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 100
	}
	respondJSON(w, http.StatusOK, conn.GetEvents(eventType, namespace, limit))
}

func (h *handlers) getMetrics(w http.ResponseWriter, r *http.Request) {
	col := h.manager.Collector()
	if col == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	metricType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	switch metricType {
	case "pods":
		m := col.GetPodMetrics(namespace, name)
		if m == nil {
			respondError(w, http.StatusNotFound, "metrics not found")
			return
		}
		respondJSON(w, http.StatusOK, m)
	case "nodes":
		m := col.GetNodeMetrics(name)
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

// requireConnector is middleware that returns 503 when no cluster is connected.
// Used to guard all endpoints that call h.manager.Connector().
func (h *handlers) requireConnector(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.manager.Connector() == nil {
			msg := "cluster not connected"
			if err := h.manager.ConnError(); err != nil {
				msg = err.Error()
			}
			respondError(w, http.StatusServiceUnavailable, msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}
