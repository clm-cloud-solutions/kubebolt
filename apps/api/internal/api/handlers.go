package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
	"github.com/kubebolt/kubebolt/apps/api/internal/updatecheck"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

type handlers struct {
	manager       *cluster.Manager
	wsHub         *websocket.Hub
	pfManager     *PortForwardManager
	drainManager  *drainSessionManager
	// copilotConfig holds the env-driven baseline computed once at boot.
	// Spec #09 introduced UI-edited overrides — at request time, prefer
	// settingsRuntime.Copilot() which merges BoltDB overrides onto this
	// baseline. The raw copilotConfig is still used for things bound
	// at startup (USAGE accounting cache sizing, etc.) where hot-reload
	// isn't valuable.
	copilotConfig    config.CopilotConfig
	settingsRuntime  *settings.Runtime   // nil when auth/persistence disabled — same gate as copilotUsage
	bootEnv          map[string]string   // snapshot of KUBEBOLT_* env vars captured at process start
	copilotUsage     *copilot.UsageStore // nil when auth/persistence disabled
	// copilotConversations persists Kobi chat transcripts per user so the
	// operator can refresh / re-login and resume. nil when auth/persistence
	// disabled (same gate as copilotUsage) — chat still works, just ephemeral.
	copilotConversations copilot.ConversationStore
	authHandlers     *auth.Handlers
	notifications *notifications.Manager // nil when no webhook URLs configured
	integrations  *integrations.Registry
	// agentAuthEnforcement mirrors the agent gRPC server's
	// EnforcementMode ("enforced"/"permissive"/"disabled"). The agent
	// integration handlers consult it to refuse misconfigurations
	// (e.g. proxyEnabled=true with authMode="" against an enforced
	// backend) up-front, instead of letting the agent crash-loop on
	// the welcome handshake. Empty string == disabled.
	agentAuthEnforcement string
	// tenantsStore is the source of truth for ingest tokens. The
	// agent integration's "Generate token + create Secret" flow
	// issues here, then materializes a K8s Secret in the agent
	// namespace so the operator never has to copy the plaintext.
	// nil when auth is disabled — the issue-token endpoint refuses
	// in that case.
	tenantsStore *auth.TenantsStore
	// ingestTokens validates "kb_" ingest tokens (now in their own store,
	// not inlined in the tenant record). nil when auth is disabled.
	ingestTokens auth.IngestTokenStore
	// promWriteAuthMode mirrors agentAuthEnforcement above but
	// scopes the policy to the HTTP /api/v1/prom/write receiver
	// (vmagent's ingest path). Same three values:
	//   "enforced"   bearer required AND validated, 401 otherwise
	//   "permissive" bearer optional; if present must validate, but
	//                missing/bad bearers log a WARN and pass through
	//                — same Sprint A migration semantics the gRPC
	//                channel uses
	//   "disabled"   bearer ignored entirely (Sprint A default)
	// Empty string falls back to "disabled" at parse time.
	promWriteAuthMode string
	// promRateLimiter is the per-tenant token-bucket gate added in
	// Phase 3 Day 3. nil means "no rate limit" (transitional / test
	// envs); production wires this always via NewPromRateLimiter
	// with the fleet defaults from config.LoadPromWriteLimitsConfig.
	// Bucket state is in-memory only; restart resets every tenant's
	// counter, which is the conservative right answer (slightly
	// more permissive than persisting state, no extra BoltDB writes
	// on the hot path).
	promRateLimiter *PromRateLimiter
	// promCardinality enforces the per-tenant MaxActiveSeries cap by
	// periodically querying VictoriaMetrics for the current series
	// count per tenant_id label. Day 4 of Phase 3. nil means
	// cardinality enforcement is disabled (e.g. when VM URL isn't
	// reachable at boot or in test fixtures). The refresh goroutine
	// is started by main.go alongside the HTTP server.
	promCardinality *CardinalityTracker
	// promWriteMetrics records per-tenant request outcomes,
	// accepted samples + bytes, and the active-series gauge driven
	// by the cardinality tracker's refresh loop. Exposed at /metrics
	// via promhttp. Day 5 of Phase 3. nil means observability is
	// disabled — increments become no-ops (the metrics methods
	// nil-guard). Test fixtures pass nil; production wires it.
	promWriteMetrics *PromWriteMetrics
	// agentRegistry is the in-memory directory of currently-connected
	// agents. Spec #09 V2 Item 5b — the /admin/ingest-activity panel's
	// heartbeat list reads this directly via a new admin endpoint
	// rather than going through Prometheus (which would lag by the
	// scrape interval and lose attributes like NodeName + Connected
	// timestamp that aren't worth pushing into label-cardinality).
	agentRegistry *channel.AgentRegistry
	// updateCheck polls the GitHub releases API on demand and reports
	// whether a newer stable version of KubeBolt is available. nil
	// when the binary is a dev build OR when main.go opted out of
	// wiring the service. The handler short-circuits to
	// `{"enabled": false}` in either case.
	updateCheck *updatecheck.Service
}

// liveCopilotConfig resolves the runtime Copilot config: BoltDB override
// merged onto the env baseline when settingsRuntime is wired, the raw
// env baseline otherwise. Call this at the START of every Copilot
// entry-point handler (HandleCopilotConfig, HandleCopilotChat,
// HandleCopilotCompact) so they all pick up UI changes within the
// resolver's cache — invalidated immediately on PUT, never expired
// otherwise, so the worst-case staleness is a single in-flight request
// that already snapshotted the previous value.
//
// Subsystem reads INSIDE a single handler call should resolve once into
// a local and use that throughout. Re-reading mid-handler can pick up a
// concurrent admin PUT and produce inconsistent provider/model across
// the request's logging / accounting / chat fields.
func (h *handlers) liveCopilotConfig() config.CopilotConfig {
	if h.settingsRuntime != nil {
		return h.settingsRuntime.Copilot()
	}
	return h.copilotConfig
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
	// Cancel any in-flight drains too — the previous cluster's
	// restConfig won't apply to the new cluster, and continuing to
	// evict pods on the old cluster is never the right answer.
	h.drainManager.CancelAll()

	// Broadcast cluster switch event
	h.wsHub.Broadcast("cluster.switched", map[string]string{"context": body.Context})

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"context": body.Context,
	})
}

func (h *handlers) getClusterOverview(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	overview := conn.GetOverview()
	// GetOverview()'s internal buildHealth() omits the insights count
	// (the connector doesn't have a reference to the engine), so the
	// overview's Health.Insights stays at zero even when /insights has
	// data — visible in the dashboard's Insights KPI showing "0" while
	// the page shows real items. Recompute with the engine here so the
	// overview payload matches what /cluster/health and /insights see.
	if eng := h.manager.Engine(r.Context()); eng != nil {
		col := h.manager.Collector(r.Context())
		metricsAvailable := col != nil && col.IsAvailable()
		overview.Health = conn.GetHealth(metricsAvailable, eng.GetAllInsights())
	}
	respondJSON(w, http.StatusOK, overview)
}

func (h *handlers) getClusterHealth(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector(r.Context())
	eng := h.manager.Engine(r.Context())
	col := h.manager.Collector(r.Context())
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
	node := r.URL.Query().Get("node")
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

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	result := conn.GetResources(resourceType, namespace, search, status, node, sortBy, order, page, limit)
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

	conn := h.manager.Connector(r.Context())
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
	if col := h.manager.Collector(r.Context()); col != nil {
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
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	respondJSON(w, http.StatusOK, conn.GetTopology())
}

func (h *handlers) getInsights(w http.ResponseWriter, r *http.Request) {
	eng := h.manager.Engine(r.Context())
	if eng == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	severity := r.URL.Query().Get("severity")
	resolvedStr := r.URL.Query().Get("resolved")
	resolved := resolvedStr == "true"

	// History path (Sprint 0): read persisted insight records — active +
	// resolved, surviving restarts — instead of live engine state. Opt-in
	// via ?history=true so the default behavior is unchanged.
	if r.URL.Query().Get("history") == "true" {
		q := insights.InsightQuery{Severity: severity}
		switch r.URL.Query().Get("status") {
		case "active", "resolved":
			q.Status = r.URL.Query().Get("status")
		}
		if v := r.URL.Query().Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				q.Since = t
			}
		}
		if v := r.URL.Query().Get("until"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				q.Until = t
			}
		}
		records, err := eng.ListHistory(q)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to read insight history")
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"items": records,
			"total": len(records),
		})
		return
	}

	items := eng.GetInsights(severity, resolved)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

func (h *handlers) getEvents(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector(r.Context())
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

	var tailLines int64
	explicitTail := false
	if tl := r.URL.Query().Get("tailLines"); tl != "" {
		if v, err := strconv.ParseInt(tl, 10, 64); err == nil && v > 0 {
			tailLines = v
			explicitTail = true
		}
	}

	q := cluster.LogQuery{
		Container:  container,
		Previous:   r.URL.Query().Get("previous") == "true",
		Timestamps: r.URL.Query().Get("timestamps") == "true",
	}
	if s := r.URL.Query().Get("since"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			q.SinceSeconds = int64(d.Seconds())
		}
	}
	if st := r.URL.Query().Get("sinceTime"); st != "" {
		if t, err := time.Parse(time.RFC3339, st); err == nil {
			q.SinceTime = t
		}
	}
	if et := r.URL.Query().Get("endTime"); et != "" {
		if t, err := time.Parse(time.RFC3339, et); err == nil {
			q.EndTime = t
		}
	}

	// Apply the tail bound. Default to 100 lines only when no absolute
	// time window is set; with a window, the 10 MiB hardcap in
	// cluster.GetPodLogs is the only bound — slicing to the last 100
	// lines first would silently drop older lines inside the window.
	switch {
	case explicitTail:
		q.TailLines = tailLines
	case q.SinceTime.IsZero() && q.EndTime.IsZero():
		q.TailLines = 100
	}

	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	logs, err := conn.GetPodLogs(namespace, name, q)
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

	conn := h.manager.Connector(r.Context())
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

	conn := h.manager.Connector(r.Context())
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
	col := h.manager.Collector(r.Context())
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
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	respondJSON(w, http.StatusOK, conn.Permissions())
}

// requireConnector is middleware that returns 503 when no cluster is connected.
// Used to guard all endpoints that call h.manager.Connector(r.Context()).
func (h *handlers) requireConnector(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.manager.Connector(r.Context()) == nil {
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
	conn := h.manager.Connector(r.Context())
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
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	// `?detailed=true` returns the rich rollout-history payload
	// (multi-container images, change-cause, current-revision marker).
	// The legacy shape stays the default to keep the existing
	// History tab working until the revision-picker UI cuts over.
	if r.URL.Query().Get("detailed") == "true" {
		resp, err := conn.GetDeploymentHistoryDetailed(namespace, name)
		if err != nil {
			respondMutationError(w, err)
			return
		}
		respondJSON(w, http.StatusOK, resp)
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
	conn := h.manager.Connector(r.Context())
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
	conn := h.manager.Connector(r.Context())
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
	conn := h.manager.Connector(r.Context())
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
	conn := h.manager.Connector(r.Context())
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}
	// Detailed STS/DS history: unmarshals each ControllerRevision's
	// embedded pod template to expose container images alongside
	// the revision/timestamp the legacy shape provides.
	if r.URL.Query().Get("detailed") == "true" {
		resp, err := conn.GetWorkloadHistoryDetailed(resourceType, namespace, name)
		if err != nil {
			respondMutationError(w, err)
			return
		}
		respondJSON(w, http.StatusOK, resp)
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
	conn := h.manager.Connector(r.Context())
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
