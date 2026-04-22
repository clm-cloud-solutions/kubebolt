package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// NewRouter creates the chi router with all API routes.
func NewRouter(
	manager *cluster.Manager,
	wsHub *websocket.Hub,
	corsOrigins []string,
	copilotCfg config.CopilotConfig,
	copilotUsage *copilot.UsageStore,
	authHandlers *auth.Handlers,
	notifManager *notifications.Manager,
) *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.RequestID)
	r.Use(LoggingMiddleware)
	r.Use(CORSMiddleware(corsOrigins))

	h := &handlers{
		manager:       manager,
		wsHub:         wsHub,
		pfManager:     NewPortForwardManager(),
		copilotConfig: copilotCfg,
		copilotUsage:  copilotUsage,
		authHandlers:  authHandlers,
		notifications: notifManager,
	}

	// Health check endpoint
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(JSONContentType)

		// --- Public routes (no JWT required) ---
		r.Get("/auth/config", authHandlers.GetAuthConfig)
		r.Post("/auth/login", authHandlers.Login)
		r.Post("/auth/refresh", authHandlers.Refresh)
		// Copilot config is public — no API keys exposed, frontend needs it before auth to decide whether to render the chat panel
		r.Get("/copilot/config", h.HandleCopilotConfig)

		// --- All routes below require auth (when enabled) ---
		r.Group(func(r chi.Router) {
			r.Use(authHandlers.RequireAuth)

			// Auth-protected user routes
			r.Post("/auth/logout", authHandlers.Logout)
			r.Get("/auth/me", authHandlers.GetMe)
			r.Put("/auth/me/password", authHandlers.ChangePassword)

			// User management — admin only
			r.Route("/users", func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/", authHandlers.ListUsers)
				r.Post("/", authHandlers.CreateUser)
				r.Get("/{id}", authHandlers.GetUser)
				r.Put("/{id}", authHandlers.UpdateUser)
				r.Put("/{id}/password", authHandlers.ResetPassword)
				r.Delete("/{id}", authHandlers.DeleteUser)
			})

			// Cluster management — always available, no active connector required
			r.Get("/clusters", h.listClusters)
			r.Post("/clusters/switch", h.switchCluster)

			// Cluster CRUD — admin only (add/remove/rename clusters from UI)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Post("/clusters", h.handleAddCluster)
				r.Delete("/clusters/{context}", h.handleDeleteCluster)
				r.Put("/clusters/{context}/rename", h.handleRenameCluster)
			})

			// Notifications — admin only (config read + test send)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/notifications/config", h.handleNotificationsConfig)
				r.Post("/notifications/test/{channel}", h.handleNotificationsTest)
			})

			// Copilot usage analytics — admin only
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/admin/copilot/usage/summary", h.handleCopilotUsageSummary)
				r.Get("/admin/copilot/usage/timeseries", h.handleCopilotUsageTimeseries)
				r.Get("/admin/copilot/usage/sessions", h.handleCopilotUsageSessions)
			})

			// All other endpoints require an active cluster connection
			r.Group(func(r chi.Router) {
				r.Use(h.requireConnector)

				// Read endpoints — any authenticated role (Viewer+)
				r.Get("/cluster/overview", h.getClusterOverview)
				r.Get("/cluster/health", h.getClusterHealth)
				r.Get("/cluster/permissions", h.getPermissions)
				r.Get("/resources/{type}", h.getResources)
				r.Get("/resources/{type}/{namespace}/{name}", h.getResourceDetail)
				r.Get("/resources/{type}/{namespace}/{name}/yaml", h.getResourceYAML)
				r.Get("/resources/{type}/{namespace}/{name}/describe", h.getResourceDescribe)
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
				r.Get("/portforward", h.handleListPortForwards)
				r.Get("/search", h.handleSearch)
				r.Get("/topology", h.getTopology)
				r.Get("/insights", h.getInsights)
				r.Get("/events", h.getEvents)
				r.Get("/metrics/{type}/{namespace}/{name}", h.getMetrics)

				// Copilot chat — any role can ask questions
				r.Post("/copilot/chat", h.HandleCopilotChat)
				r.Post("/copilot/compact", h.HandleCopilotCompact)

				// Write endpoints — Editor+ role required
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleEditor))
					r.Put("/resources/{type}/{namespace}/{name}/yaml", h.putResourceYAML)
					r.Post("/resources/{type}/{namespace}/{name}/restart", h.handleRestart)
					r.Post("/resources/{type}/{namespace}/{name}/scale", h.handleScale)
					r.Post("/portforward", h.handleCreatePortForward)
					r.Delete("/portforward/{id}", h.handleDeletePortForward)
				})

				// Destructive endpoints — Admin role required
				r.With(auth.RequireRole(auth.RoleAdmin)).Delete("/resources/{type}/{namespace}/{name}", h.handleDelete)
			})
		})
	})

	// WebSocket endpoints (outside JSON middleware)
	// When auth is enabled, token is validated via query param ?token=
	r.Get("/api/v1/ws", h.handleWebSocket)
	r.Get("/ws/exec/{namespace}/{name}", h.handleExec)

	// Port-forward reverse proxy (outside JSON middleware — proxied content has its own content-type)
	r.HandleFunc("/pf/{id}/*", h.handlePortForwardProxy)

	return r
}
