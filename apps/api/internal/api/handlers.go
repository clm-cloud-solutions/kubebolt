package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

type handlers struct {
	manager       *cluster.Manager
	wsHub         *websocket.Hub
	pfManager     *PortForwardManager
	copilotConfig config.CopilotConfig
	authHandlers  *auth.Handlers
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

	// Stop any active port-forwards from previous cluster
	h.pfManager.StopAll()

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
	if result.Forbidden {
		respondError(w, http.StatusForbidden, "insufficient permissions to access "+resourceType)
		return
	}
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
		if _, ok := err.(*cluster.PermissionDeniedError); ok {
			respondError(w, http.StatusForbidden, err.Error())
			return
		}
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	// Inject metrics from collector if available
	if col := h.manager.Collector(); col != nil {
		switch resourceType {
		case "pods":
			if pm := col.GetPodMetrics(namespace, name); pm != nil {
				detail["cpuUsage"] = pm.CPUUsage
				detail["memoryUsage"] = pm.MemUsage
				// Aggregate limits/requests from containers
				var cpuReq, cpuLim, memReq, memLim int64
				if containers, ok := detail["containers"].([]map[string]interface{}); ok {
					for _, c := range containers {
						if res, ok := c["resources"].(map[string]interface{}); ok {
							if v, ok := res["cpuRequest"].(int64); ok { cpuReq += v }
							if v, ok := res["cpuLimit"].(int64); ok { cpuLim += v }
							if v, ok := res["memoryRequest"].(int64); ok { memReq += v }
							if v, ok := res["memoryLimit"].(int64); ok { memLim += v }
						}
					}
				}
				if cpuLim > 0 {
					detail["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuLim) * 100
				} else if cpuReq > 0 {
					detail["cpuPercent"] = float64(pm.CPUUsage) / float64(cpuReq) * 100
				}
				if memLim > 0 {
					detail["memoryPercent"] = float64(pm.MemUsage) / float64(memLim) * 100
				} else if memReq > 0 {
					detail["memoryPercent"] = float64(pm.MemUsage) / float64(memReq) * 100
				}
			}
		case "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs":
			if wm := conn.AggregateWorkloadMetrics(resourceType, namespace, name, col); wm != nil {
				detail["cpuUsage"] = wm["cpuUsage"]
				detail["memoryUsage"] = wm["memoryUsage"]
				if v, ok := wm["cpuPercent"]; ok {
					detail["cpuPercent"] = v
				}
				if v, ok := wm["memoryPercent"]; ok {
					detail["memoryPercent"] = v
				}
			}
		case "nodes":
			if nm := col.GetNodeMetrics(name); nm != nil {
				detail["cpuUsage"] = nm.CPUUsage
				detail["memoryUsage"] = nm.MemUsage
				if alloc, ok := detail["cpuAllocatable"].(int64); ok && alloc > 0 {
					detail["cpuPercent"] = float64(nm.CPUUsage) / float64(alloc) * 100
				}
				if alloc, ok := detail["memoryAllocatable"].(int64); ok && alloc > 0 {
					detail["memoryPercent"] = float64(nm.MemUsage) / float64(alloc) * 100
				}
			}
		}
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
	involvedKind := r.URL.Query().Get("involvedKind")
	involvedName := r.URL.Query().Get("involvedName")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 100
	}
	respondJSON(w, http.StatusOK, conn.GetEvents(eventType, namespace, involvedKind, involvedName, limit))
}

func (h *handlers) getPodLogs(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")

	if namespace == "_" {
		namespace = ""
	}

	tailLines := int64(100)
	if tl := r.URL.Query().Get("tailLines"); tl != "" {
		if v, err := strconv.ParseInt(tl, 10, 64); err == nil && v > 0 {
			tailLines = v
		}
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	logs, err := conn.GetPodLogs(namespace, name, container, tailLines)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(logs))
}

func (h *handlers) putResourceYAML(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	if err := conn.ApplyResourceYAML(resourceType, namespace, name, body); err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "forbidden") || strings.Contains(errMsg, "Forbidden") {
			respondError(w, http.StatusForbidden, errMsg)
		} else if strings.Contains(errMsg, "invalid") || strings.Contains(errMsg, "Invalid") {
			respondError(w, http.StatusBadRequest, errMsg)
		} else if strings.Contains(errMsg, "conflict") || strings.Contains(errMsg, "Conflict") {
			respondError(w, http.StatusConflict, errMsg)
		} else {
			respondError(w, http.StatusInternalServerError, errMsg)
		}
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

func (h *handlers) getResourceYAML(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if namespace == "_" {
		namespace = ""
	}

	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	yamlBytes, err := conn.GetResourceYAML(resourceType, namespace, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/yaml")
	w.WriteHeader(http.StatusOK)
	w.Write(yamlBytes)
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
	// Validate auth for WebSocket connections (token via query param)
	if h.authHandlers != nil && h.authHandlers.IsEnabled() {
		token := r.URL.Query().Get("token")
		if h.authHandlers.ValidateWSToken(token) == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
	}
	websocket.ServeWS(h.wsHub, w, r)
}

func (h *handlers) getPermissions(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	respondJSON(w, http.StatusOK, conn.Permissions())
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

func (h *handlers) getDeploymentPods(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	pods := conn.GetDeploymentPods(namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kind":  "pods",
		"items": pods,
		"total": len(pods),
	})
}

func (h *handlers) getDeploymentHistory(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	history := conn.GetDeploymentHistory(namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": history,
		"total": len(history),
	})
}

func (h *handlers) getStatefulSetPods(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	pods := conn.GetStatefulSetPods(namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kind":  "pods",
		"items": pods,
		"total": len(pods),
	})
}

func (h *handlers) getDaemonSetPods(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	pods := conn.GetDaemonSetPods(namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kind":  "pods",
		"items": pods,
		"total": len(pods),
	})
}

func (h *handlers) getJobPods(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	pods := conn.GetJobPods(namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kind":  "pods",
		"items": pods,
		"total": len(pods),
	})
}

func (h *handlers) getWorkloadHistory(w http.ResponseWriter, r *http.Request) {
	resourceType := chi.URLParam(r, "type")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	history := conn.GetWorkloadHistory(resourceType, namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": history,
		"total": len(history),
	})
}

func (h *handlers) getCronJobJobs(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	jobs := conn.GetCronJobJobs(namespace, name)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kind":  "jobs",
		"items": jobs,
		"total": len(jobs),
	})
}
