package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// NewRouter creates the chi router with all API routes.
//
// tenantHandlers is optional — pass nil to skip the /admin/tenants
// surface. Self-hosted single-cluster builds without auth wired can
// keep skipping it; the agent gRPC channel still works in disabled
// enforcement mode.
func NewRouter(
	manager *cluster.Manager,
	wsHub *websocket.Hub,
	corsOrigins []string,
	copilotCfg config.CopilotConfig,
	copilotUsage *copilot.UsageStore,
	authHandlers *auth.Handlers,
	tenantHandlers *auth.TenantHandlers,
	notifManager *notifications.Manager,
	integrationRegistry *integrations.Registry,
	agentAuthEnforcement string,
	tenantsStore *auth.TenantsStore,
	promWriteAuthMode string,
	promRateLimiter *PromRateLimiter,
) *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.RequestID)
	r.Use(LoggingMiddleware)
	r.Use(CORSMiddleware(corsOrigins))

	h := &handlers{
		manager:              manager,
		wsHub:                wsHub,
		pfManager:            NewPortForwardManager(),
		drainManager:         newDrainSessionManager(),
		copilotConfig:        copilotCfg,
		copilotUsage:         copilotUsage,
		authHandlers:         authHandlers,
		notifications:        notifManager,
		integrations:         integrationRegistry,
		agentAuthEnforcement: agentAuthEnforcement,
		tenantsStore:         tenantsStore,
		promWriteAuthMode:    promWriteAuthMode,
		promRateLimiter:      promRateLimiter,
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

		// Prom remote_write receiver. PUBLIC because vmagent doesn't
		// carry a JWT; gating is via the dedicated
		// KUBEBOLT_REMOTE_WRITE_ENABLED env var (default false). The
		// handler itself returns 404 with a hint when the var is off.
		// Phase 3 will add a bearer-token middleware specific to this
		// path (separate from the user-session JWT auth) and remove
		// the env-var gate.
		r.Post("/prom/write", h.handlePromWrite)

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

			// Metrics storage (VictoriaMetrics) PromQL pass-through — no cluster
			// connection required. Data is queried from the TSDB directly.
			r.Get("/metrics/query", h.handleMetricsQuery)
			r.Get("/metrics/query_range", h.handleMetricsQueryRange)

			// Traffic flow edges derived from pod_flow_events_total. Reads
			// from the same TSDB. Empty response when Hubble / other
			// traffic observability source hasn't produced any data yet.
			r.Get("/flows/edges", h.handleFlowEdges)

			// Coverage banner — which observability sources are
			// actively shipping samples to VM for the current cluster.
			// Cheap (4 instant queries), poll-friendly from the UI.
			r.Get("/coverage", h.handleCoverage)

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

			// Tenant + ingest token administration — global admin only.
			// Sprint A model: one global admin manages every tenant's
			// tokens. Per-tenant self-service requires User.TenantID
			// (Sprint B+). See auth/tenant_handlers.go for context.
			if tenantHandlers != nil {
				r.Route("/admin/tenants", func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					tenantHandlers.RegisterRoutes(r)
				})
			}

			// Integrations catalog — list + read are deliberately OUTSIDE
			// requireConnector so the page works on a fresh install with
			// no clusters yet. The handlers degrade to metadata-only
			// (StatusNotInstalled) when conn==nil; install/configure
			// stay inside the cluster-required group below.
			r.Get("/integrations", h.handleListIntegrations)
			r.Get("/integrations/{id}", h.handleGetIntegration)

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
				r.Get("/deploys", h.handleDeploys)
				r.Get("/metrics/{type}/{namespace}/{name}", h.getMetrics)

				// Integrations — install / configure / uninstall mutate
				// the cluster and require Admin. List + get live
				// outside this group (above) so the catalog renders
				// even with no cluster connected.
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					r.Post("/integrations/{id}/install", h.handleInstallIntegration)
					r.Get("/integrations/{id}/config", h.handleGetIntegrationConfig)
					r.Put("/integrations/{id}/config", h.handlePutIntegrationConfig)
					r.Delete("/integrations/{id}", h.handleUninstallIntegration)
					// Agent-specific helpers — surface backend auth
					// posture and let the dialog issue an ingest token
					// + materialize the Secret in one click. Hard-coded
					// to /integrations/agent/* (not parameterized) so
					// other integrations don't accidentally inherit
					// the tenants-store-backed flow.
					r.Get("/integrations/agent/auth-info", h.handleAgentAuthInfo)
					r.Get("/integrations/agent/install-defaults", h.handleAgentInstallDefaults)
					r.Post("/integrations/agent/issue-token", h.handleAgentIssueToken)
				})

				// Copilot chat — any role can ask questions
				r.Post("/copilot/chat", h.HandleCopilotChat)
				r.Post("/copilot/compact", h.HandleCopilotCompact)

				// Write endpoints — Editor+ role required
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleEditor))
					r.Put("/resources/{type}/{namespace}/{name}/yaml", h.putResourceYAML)
					// Create new resource from manifest — kubectl create -f
					// equivalent. URL is /resources/:type/:ns (no :name —
					// the name lives in the body's metadata.name). Single-
					// document YAML or JSON bodies. Tier 2 #10, see
					// internal/k8s-operations/tier2-apply-new-manifest.md.
					r.Post("/resources/{type}/{namespace}", h.handleCreateResource)
					r.Post("/resources/{type}/{namespace}/{name}/restart", h.handleRestart)
					r.Post("/resources/{type}/{namespace}/{name}/scale", h.handleScale)
					r.Post("/resources/{type}/{namespace}/{name}/rollback", h.handleRollback)
					r.Post("/resources/{type}/{namespace}/{name}/set-image", h.handleSetImage)
					// Set resources — kubectl set resources. Strategic
					// merge patch on container resource requests / limits
					// without going through the YAML editor. Tier 2 #6,
					// see internal/k8s-operations/tier2-set-resources.md.
					r.Post("/resources/{type}/{namespace}/{name}/set-resources", h.handleSetResources)
					// Set env — kubectl set env. Strategic merge patch
					// on container env arrays, supporting set/remove via
					// the `$patch: delete` directive. Tier 2 #7, see
					// internal/k8s-operations/tier2-set-env.md.
					r.Post("/resources/{type}/{namespace}/{name}/set-env", h.handleSetEnv)
					// Edit metadata — kubectl label / kubectl annotate
					// equivalents. JSON merge patch on metadata.labels +
					// metadata.annotations via the dynamic client; works
					// on every kind. Tier 2 #8, see
					// internal/k8s-operations/tier2-edit-labels-annotations.md.
					r.Post("/resources/{type}/{namespace}/{name}/edit-metadata", h.handleEditMetadata)
					// Secret reveal — decode and return Secret values.
					// Editor+ at the route level; the handler escalates
					// to Admin internally for production-pattern
					// namespaces. Tier 2 #9, see
					// internal/k8s-operations/tier2-secret-reveal.md.
					r.Post("/resources/{type}/{namespace}/{name}/reveal", h.handleSecretReveal)
					r.Post("/resources/{type}/{namespace}/{name}/cordon", h.handleCordon)
					r.Post("/resources/{type}/{namespace}/{name}/uncordon", h.handleUncordon)
					// Rollout pause/resume. Deployment-only — flips
					// spec.paused so the deployment controller stops
					// reconciling without touching pods. The
					// `rollout-` prefix avoids colliding with CronJob
					// /resume below; full reasoning in
					// internal/k8s-operations/tier2-rollout-pause-resume.md.
					r.Post("/resources/{type}/{namespace}/{name}/rollout-pause", h.handleRolloutPause)
					r.Post("/resources/{type}/{namespace}/{name}/rollout-resume", h.handleRolloutResume)
					// CronJob ergonomics. Suspend/resume flip
					// spec.suspend; trigger creates a one-off Job
					// from the CronJob's jobTemplate (kubectl
					// create job --from=cronjob/X).
					r.Post("/resources/{type}/{namespace}/{name}/suspend", h.handleCronJobSuspend)
					r.Post("/resources/{type}/{namespace}/{name}/resume", h.handleCronJobResume)
					r.Post("/resources/{type}/{namespace}/{name}/trigger", h.handleCronJobTrigger)
					r.Post("/portforward", h.handleCreatePortForward)
					r.Delete("/portforward/{id}", h.handleDeletePortForward)
				})

				// Destructive endpoints — Admin role required.
				// Drain joins delete here because evicting every pod
				// on a node is high-impact: it can violate PDBs,
				// degrade cluster capacity, and disrupt running
				// workloads. Cordon/uncordon stay Editor+ since they
				// only flip a schedule flag.
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					r.Delete("/resources/{type}/{namespace}/{name}", h.handleDelete)
					r.Post("/resources/{type}/{namespace}/{name}/drain", h.handleDrain)
					// GET re-attaches to an in-flight drain (SSE);
					// DELETE cancels. Same Admin gate because
					// inspecting the drain stream effectively shows
					// what pods are being evicted across namespaces.
					r.Get("/resources/{type}/{namespace}/{name}/drain", h.handleDrainSession)
					r.Delete("/resources/{type}/{namespace}/{name}/drain", h.handleDrainCancel)
				})
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
