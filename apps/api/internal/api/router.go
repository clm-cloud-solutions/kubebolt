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
		manager: manager,
		wsHub:   wsHub,
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
			r.Get("/resources/{type}", h.getResources)
			r.Get("/resources/{type}/{namespace}/{name}", h.getResourceDetail)
			r.Get("/resources/{type}/{namespace}/{name}/yaml", h.getResourceYAML)
			r.Get("/resources/pods/{namespace}/{name}/logs", h.getPodLogs)
			r.Get("/topology", h.getTopology)
			r.Get("/insights", h.getInsights)
			r.Get("/events", h.getEvents)
			r.Get("/metrics/{type}/{namespace}/{name}", h.getMetrics)
		})
	})

	// WebSocket endpoint (outside JSON middleware)
	r.Get("/api/v1/ws", h.handleWebSocket)

	return r
}
