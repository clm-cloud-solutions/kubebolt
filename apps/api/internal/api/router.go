package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// NewRouter creates the chi router with all API routes.
func NewRouter(manager *cluster.Manager, wsHub *websocket.Hub, corsOrigins []string) *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.RequestID)
	r.Use(LoggingMiddleware)
	r.Use(CORSMiddleware(corsOrigins))

	h := &handlers{
		manager:   manager,
		wsHub:     wsHub,
		pfManager: NewPortForwardManager(),
	}

	// Health check endpoint
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(JSONContentType)

		// Cluster management — always available, no active connector required
		r.Get("/clusters", h.listClusters)
		r.Post("/clusters/switch", h.switchCluster)

		// All other endpoints require an active cluster connection
		r.Group(func(r chi.Router) {
			r.Use(h.requireConnector)
			r.Get("/cluster/overview", h.getClusterOverview)
			r.Get("/cluster/health", h.getClusterHealth)
			r.Get("/cluster/permissions", h.getPermissions)
			r.Get("/resources/{type}", h.getResources)
			r.Get("/resources/{type}/{namespace}/{name}", h.getResourceDetail)
			r.Get("/resources/{type}/{namespace}/{name}/yaml", h.getResourceYAML)
			r.Put("/resources/{type}/{namespace}/{name}/yaml", h.putResourceYAML)
			r.Get("/resources/{type}/{namespace}/{name}/describe", h.getResourceDescribe)
			r.Post("/resources/{type}/{namespace}/{name}/restart", h.handleRestart)
			r.Post("/resources/{type}/{namespace}/{name}/scale", h.handleScale)
			r.Delete("/resources/{type}/{namespace}/{name}", h.handleDelete)
			r.Get("/resources/pods/{namespace}/{name}/logs", h.getPodLogs)
			r.Get("/resources/pods/{namespace}/{name}/files", h.handleListFiles)
			r.Get("/resources/pods/{namespace}/{name}/files/content", h.handleFileContent)
			r.Get("/resources/pods/{namespace}/{name}/files/download", h.handleFileDownload)
			r.Get("/resources/deployments/{namespace}/{name}/pods", h.getDeploymentPods)
			r.Get("/resources/deployments/{namespace}/{name}/history", h.getDeploymentHistory)
			r.Get("/resources/statefulsets/{namespace}/{name}/pods", h.getStatefulSetPods)
			r.Get("/resources/daemonsets/{namespace}/{name}/pods", h.getDaemonSetPods)
			r.Get("/resources/jobs/{namespace}/{name}/pods", h.getJobPods)
			r.Get("/resources/cronjobs/{namespace}/{name}/jobs", h.getCronJobJobs)
			r.Get("/resources/{type}/{namespace}/{name}/history", h.getWorkloadHistory)
			r.Post("/portforward", h.handleCreatePortForward)
			r.Get("/portforward", h.handleListPortForwards)
			r.Delete("/portforward/{id}", h.handleDeletePortForward)
			r.Get("/search", h.handleSearch)
			r.Get("/topology", h.getTopology)
			r.Get("/insights", h.getInsights)
			r.Get("/events", h.getEvents)
			r.Get("/metrics/{type}/{namespace}/{name}", h.getMetrics)
		})
	})

	// WebSocket endpoints (outside JSON middleware)
	r.Get("/api/v1/ws", h.handleWebSocket)
	r.Get("/ws/exec/{namespace}/{name}", h.handleExec)

	// Port-forward reverse proxy (outside JSON middleware — proxied content has its own content-type)
	r.HandleFunc("/pf/{id}/*", h.handlePortForwardProxy)

	return r
}
