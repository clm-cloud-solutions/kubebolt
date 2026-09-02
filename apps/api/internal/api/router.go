package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/findings"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcp"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
	"github.com/kubebolt/kubebolt/apps/api/internal/updatecheck"
	"github.com/kubebolt/kubebolt/apps/api/internal/usage"
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
	copilotUsage copilot.SessionStore,
	// copilotConversations persists per-user Kobi transcripts for history +
	// resume. Optional — nil when auth/persistence is disabled (chat stays
	// ephemeral, the /copilot/conversations endpoints 503).
	copilotConversations copilot.ConversationStore,
	authHandlers *auth.Handlers,
	tenantHandlers *auth.TenantHandlers,
	notifManager *notifications.Manager,
	integrationRegistry *integrations.Registry,
	agentAuthEnforcement string,
	tenantsStore auth.TenantStore,
	ingestTokens auth.IngestTokenStore,
	promWriteAuthMode string,
	promRateLimiter *PromRateLimiter,
	promCardinality *CardinalityTracker,
	promNameFilter *PromNameFilter,
	promWriteMetrics *PromWriteMetrics,
	// usageStore is the W1 metering seam. Never nil — OSS passes the no-op,
	// EE passes a Postgres-backed impl. Call sites (e.g. prom_write accepted
	// samples) record billable usage through it unconditionally.
	usageStore usage.UsageStore,
	// findingsStore persists normalized security findings (E2 SEC-C).
	// Optional — nil when persistence is disabled; /findings 503s.
	findingsStore findings.Store,
	// eventStore persists runtime security events (Falco ingest). Optional.
	eventStore findings.EventStore,
	// settingsRuntime is the BoltDB-first config resolver introduced by
	// spec #09. Optional — nil when auth/persistence is disabled (the
	// /settings/* admin endpoints simply 503 in that mode, and the
	// Copilot chat handler keeps reading env-only copilotCfg).
	settingsRuntime *settings.Runtime,
	// bootEnv is the snapshot of KUBEBOLT_* env vars captured at
	// process start (via SnapshotKubeboltEnv from main.go). Exposed
	// read-only via /admin/settings/booted-with so operators can
	// see what the Helm/env baseline actually was.
	bootEnv map[string]string,
	// agentRegistry is the in-memory agent directory. nil-safe — when
	// no agents are wired (test fixtures, sub-1.0 deployments), the
	// /admin/agents endpoint returns an empty list rather than 500.
	agentRegistry *channel.AgentRegistry,
	// updateCheck reports whether a newer stable KubeBolt version is
	// available on GitHub. nil-safe — passing nil disables the
	// /update-check endpoint (returns {"enabled": false}). Wired in
	// main.go from `updatecheck.New(version, ...)`.
	updateCheck *updatecheck.Service,
	// insightPolicySvc is the rule-policy service (#44 step 1). nil-safe —
	// nil makes /admin/insight-policies 503.
	insightPolicySvc *InsightPolicyService,
	// episodeReader serves the episode history (2.1.0). nil-safe — nil makes
	// /insights/episodes 503. The same store carries the presence, mute,
	// operational and shift-stat surfaces, recovered by type assertion below.
	episodeReader insights.EpisodeReader,
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
		settingsRuntime:      settingsRuntime,
		bootEnv:              bootEnv,
		copilotUsage:         copilotUsage,
		copilotConversations: copilotConversations,
		authHandlers:         authHandlers,
		notifications:        notifManager,
		integrations:         integrationRegistry,
		agentAuthEnforcement: agentAuthEnforcement,
		tenantsStore:         tenantsStore,
		ingestTokens:         ingestTokens,
		promWriteAuthMode:    promWriteAuthMode,
		promRateLimiter:      promRateLimiter,
		promCardinality:      promCardinality,
		promNameFilter:       promNameFilter,
		promWriteMetrics:     promWriteMetrics,
		usage:                usageStore,
		findingsStore:        findingsStore,
		eventStore:           eventStore,
		agentRegistry:        agentRegistry,
		updateCheck:          updateCheck,
		insightPolicies:      insightPolicySvc,
		episodes:             episodeReader,
	}
	// The episode store also carries the presence + mute + operational +
	// shift-stat surfaces; recovering them here keeps the NewRouter signature
	// stable across editions.
	h.presence, _ = episodeReader.(insights.PresenceStore)
	h.mutes, _ = episodeReader.(insights.MuteStore)
	h.operational, _ = episodeReader.(insights.OperationalReader)
	h.shiftStats, _ = episodeReader.(insights.ShiftStatsReader)

	// Kobi MCP server (read-only). Built once — the executor is stateless
	// (it only wraps the manager) and the read-only tool catalogue is
	// static. Registered as a route inside the authenticated group below.
	mcpServer := mcp.NewServer(
		mcp.ServerInfo{Name: "kubebolt-kobi", Version: "1"},
		// Same per-org metrics-retention clamp as the chat handler. This door
		// serves the identical get_workload_metrics tool to an external MCP host,
		// so wiring the entitlement on only one of them would mean a plan limit
		// that holds in the product and not through the integration.
		mcp.NewExecutorToolProvider(copilot.NewExecutor(manager).WithMetricsRetention(h.metricsRetentionFor)),
		mcp.NewKobiPromptProvider(),
	)

	// Health check endpoint
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Prometheus scrape endpoint (Phase 3 Day 5). Exposes the
	// kubebolt_prom_write_* observability metrics in the standard
	// text-exposition format. No auth — operators firewall this
	// port at the LB / NetworkPolicy layer. Production SaaS setups
	// should add a ServiceMonitor or scrape config to pull this
	// into their VM / external Prom.
	if promWriteMetrics != nil {
		r.Method(http.MethodGet, "/metrics", PromHTTPHandler(prometheus.DefaultGatherer))
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(JSONContentType)

		// --- Public routes (no JWT required) ---
		r.Get("/auth/config", authHandlers.GetAuthConfig)
		r.Post("/auth/login", authHandlers.Login)
		// Self-service org signup (multi-org / EE). Public, next to login.
		// Returns 409 requires_ee in OSS (single auto-seeded org).
		r.Post("/auth/signup", authHandlers.Signup)
		r.Post("/auth/refresh", authHandlers.Refresh)
		// Copilot config is public — no API keys exposed, the frontend needs
		// it before auth to decide whether to render the chat panel. But when
		// the caller IS authenticated (the chat panel re-fetches it from inside
		// an active session), resolve their tenant best-effort so the panel
		// title reflects the resolved Copilot config instead of the process
		// baseline. OptionalAuth never rejects, so the pre-auth login-page fetch
		// still succeeds anonymously and falls back to the baseline.
		r.With(authHandlers.OptionalAuth, authHandlers.ResolveTenant).
			Get("/copilot/config", h.HandleCopilotConfig)

		// UI config (display name, default refresh interval) is public —
		// the login page renders the display name, and the
		// RefreshContext seeds itself before any authenticated query
		// fires. No secrets here, just chrome / UX defaults.
		r.Get("/config/ui", h.handleGetUIConfig)

		// Prom remote_write receiver. PUBLIC because vmagent doesn't
		// carry a JWT; gating is via the dedicated
		// KUBEBOLT_REMOTE_WRITE_ENABLED env var (default false). The
		// handler itself returns 404 with a hint when the var is off.
		// Phase 3 will add a bearer-token middleware specific to this
		// path (separate from the user-session JWT auth) and remove
		// the env-var gate.
		r.Post("/prom/write", h.handlePromWrite)

		// Falco runtime-event ingest (E2 SEC-E). PUBLIC route, bearer
		// INGEST-token auth inside the handler (strict — no permissive fallback
		// for security events, and the token must be scoped to a cluster).
		r.Post("/ingest/falco", h.handleFalcoIngest)

		// EE extension point — register edition-specific unauthenticated
		// routes (e.g. an internal service dispatch endpoint). No-op in OSS;
		// the `ee` build tag supplies the real implementation.
		registerEERoutes(r, h)

		// --- All routes below require auth (when enabled) ---
		r.Group(func(r chi.Router) {
			r.Use(authHandlers.RequireAuth)
			// Restrict REST API-token callers (kbs_/kbk_) to their granted
			// path scopes. No-op for user-session JWT callers. Must run
			// after RequireAuth (which establishes the principal) — fail fast
			// on out-of-scope paths before tenant/cluster resolution.
			r.Use(authHandlers.EnforceAPITokenScope)
			// Resolve the request's tenant (org) once, after auth, and
			// stash it in context. OSS: always DefaultTenantName. EE swaps
			// the resolver for real multi-tenant resolution. See
			// auth.TenantResolver.
			r.Use(authHandlers.ResolveTenant)
			// Meter one api_requests per authenticated call against the resolved
			// org. Real in EE (records through the usage seam); identity
			// pass-through in OSS (see usage_meter_*.go).
			r.Use(h.meterAPIRequest)
			// Stash the request's (tenant, cluster) RuntimeKey for the
			// connector pool (W2). Behavior-neutral until the pool reads
			// it; threading it now keeps the pool additive to handlers.
			r.Use(h.resolveCluster)

			// Auth-protected user routes
			r.Post("/auth/logout", authHandlers.Logout)
			r.Get("/auth/me", authHandlers.GetMe)
			r.Put("/auth/me/password", authHandlers.ChangePassword)

			// Account — the requesting org's plan + metering summary.
			// Any authenticated role; scoped to the caller's org via the
			// ResolveTenant-stamped context. No active cluster required.
			r.Get("/account/plan", h.handleAccountPlan)
			// Capability states (#50): the one OSS axis is the active-series cap,
			// live from the ingest gate; the Overview's truncation banner reads it.
			r.Get("/account/capabilities", h.handleAccountCapabilities)
			r.Get("/account/usage", h.handleAccountUsage)

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

			// Teams — read-only view of the org → team → user hierarchy.
			// OSS serves the single default team; POST is gated 409
			// requires_ee. List/detail are open to any authenticated user
			// (their own team); the member list is admin-only, matching the
			// sensitivity of /users.
			r.Route("/teams", func(r chi.Router) {
				r.Get("/", authHandlers.ListTeams)
				r.Get("/{id}", authHandlers.GetTeam)
				r.Post("/", authHandlers.CreateTeam)
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					r.Get("/{id}/members", authHandlers.ListTeamMembers)
				})
			})

			// Cluster management — always available, no active connector required
			r.Get("/clusters", h.listClusters)
			// Nombres de clusters, incluidos los dados de baja. Fuera de
			// requireConnector como /clusters, y con el alcance de cluster puesto
			// para que la etiqueta siga el mismo criterio que el dato que rotula.
			r.With(h.WithClusterScope).Get("/clusters/names", h.handleClusterNames)
			r.Post("/clusters/switch", h.switchCluster)

			// Update check — reports the latest stable KubeBolt release
			// on GitHub. Any logged-in role can read; admin toggles the
			// underlying poller via Settings → General. Returns
			// {"enabled": false} when disabled at env or runtime.
			r.Get("/update-check", h.handleUpdateCheck)

			// Kobi conversation history — per-user, no active connector
			// required (reads BoltDB) so operators can browse / resume past
			// conversations even when the cluster is unreachable. Any logged-in
			// role; every handler enforces (tenant, user) ownership so a user
			// only ever sees their own. Writes happen inside /copilot/chat.
			r.Get("/copilot/conversations", h.handleListConversations)
			r.Get("/copilot/conversations/{id}", h.handleGetConversation)
			r.Patch("/copilot/conversations/{id}", h.handlePatchConversation)
			r.Delete("/copilot/conversations/{id}", h.handleDeleteConversation)

			// Kobi MCP server (read-only) — exposes Kobi's read-only tool
			// catalogue + guidance prompt to external MCP hosts (Claude Code,
			// Cursor, CI/CD) over the Streamable HTTP transport. Mounted in the
			// authenticated group but OUTSIDE requireConnector on purpose:
			// initialize / tools/list must work even when the cluster is
			// momentarily disconnected, and a tools/call then degrades to a
			// graceful isError result. The request context already carries the
			// (tenant, cluster) RuntimeKey from ResolveTenant + resolveCluster,
			// so this one endpoint serves every tenant/cluster the API token is
			// authorized for — single "default" tenant in OSS, many in EE/SaaS.
			// Auth reuses the standard API tokens (kb_...).
			r.Handle("/mcp", mcp.Handler(mcpServer))

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
				// Not cluster scoped: the target IS the cluster and it is named
				// in the record, so stamping the *active* one would point at a
				// different cluster than the one being changed.
				r.Use(h.auditAdminRoutes("cluster", false))
				r.Post("/clusters", h.handleAddCluster)
				r.Delete("/clusters/{context}", h.handleDeleteCluster)
				r.Put("/clusters/{context}/rename", h.handleRenameCluster)
				// Delete an agent-proxy cluster by cluster_id (durable clusters
				// need an explicit operator delete). Static "by-id" prefix avoids
				// colliding with the context-name DELETE above.
				r.Delete("/clusters/by-id/{clusterId}", h.handleDeleteAgentProxyCluster)
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
				// WithClusterScope no estrecha nada mientras el guard de arriba sea
				// admin-de-organización: un admin lee su org entera por diseño.
				// Va montado para que abrir esta vista a un rol más estrecho no
				// abra también los datos, en silencio. Ver queryUsageInScope.
				r.Use(h.WithClusterScope)
				r.Get("/admin/copilot/usage/summary", h.handleCopilotUsageSummary)
				r.Get("/admin/copilot/usage/timeseries", h.handleCopilotUsageTimeseries)
				r.Get("/admin/copilot/usage/sessions", h.handleCopilotUsageSessions)
			})

			// Action audit history — admin only (Sprint 1). Durable record
			// of every cluster mutation (UI + Kobi-proposed) for the admin
			// action-history view.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/admin/actions", h.handleListActions)
			})

			// REST API tokens — service tokens (kbs_) for Autopilot / EE
			// machine callers, and customer keys (kbk_) later. Admin only.
			// Plaintext is returned once at creation.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/admin/api-tokens", authHandlers.ListAPITokens)
				r.Post("/admin/api-tokens", authHandlers.CreateAPIToken)
				r.Delete("/admin/api-tokens/{id}", authHandlers.DeleteAPIToken)
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

			// Runtime settings — admin only. Spec #09 introduces UI-edited
			// overrides of what was previously env-only config. The
			// settingsRuntime gate is the same auth/persistence gate the
			// rest of the admin surface uses (when BoltDB is disabled the
			// whole admin surface is unavailable anyway).
			if settingsRuntime != nil {
				r.Route("/admin/settings", func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					// Subtree-wide, so a settings endpoint added later is
					// audited without anyone having to remember. Not cluster
					// scoped: settings belong to the install, not to whichever
					// cluster the operator happened to have selected.
					r.Use(h.auditAdminRoutes("settings", false))
					r.Get("/copilot", h.handleGetSettingsCopilot)
					r.Put("/copilot", h.handlePutSettingsCopilot)
					r.Post("/copilot/reset", h.handleResetSettingsCopilot)
					r.Get("/notifications", h.handleGetSettingsNotifications)
					r.Put("/notifications", h.handlePutSettingsNotifications)
					r.Post("/notifications/reset", h.handleResetSettingsNotifications)
					r.Get("/auth", h.handleGetSettingsAuth)
					r.Put("/auth", h.handlePutSettingsAuth)
					r.Post("/auth/reset", h.handleResetSettingsAuth)
					r.Get("/general", h.handleGetSettingsGeneral)
					r.Put("/general", h.handlePutSettingsGeneral)
					r.Post("/general/reset", h.handleResetSettingsGeneral)
					// Spec #09 V2 — ingest-channel covers the
					// kubebolt-agent ↔ kubebolt comms plane (auth
					// modes, rate limits, autoregister, remote_write,
					// tunnels). Restart-required for the auth subset;
					// hot-reload for the rest.
					r.Get("/ingest-channel", h.handleGetSettingsIngestChannel)
					r.Put("/ingest-channel", h.handlePutSettingsIngestChannel)
					r.Post("/ingest-channel/reset", h.handleResetSettingsIngestChannel)
					r.Get("/booted-with", h.handleGetBootedWith)
				})
				// First-login wizard status. Separate from /settings/*
				// because it's not a domain — just a one-bit flag tracking
				// whether the wizard has been run at least once.
				r.Route("/admin/setup", func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					r.Get("/status", h.handleGetSetupStatus)
					r.Post("/complete", h.handlePostSetupComplete)
				})
				// /admin/system — destructive process-level actions that
				// don't belong under /settings. Restart is admin-only via
				// the route group below; same gating as Settings.
				r.Route("/admin/system", func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					r.Post("/restart", h.handleSystemRestart)
				})
			}

			// Spec #09 V2 Item 5b — /admin/agents reads the live agent
			// registry (in-memory directory of currently-connected
			// gRPC streams). Powers the heartbeat list in the
			// /admin/ingest-activity panel. Lives OUTSIDE the
			// settingsRuntime gate because the registry is wired
			// independently of BoltDB persistence — even in
			// auth-disabled mode, agents can still connect via the
			// disabled-auth path, and operators want to see them.
			r.Route("/admin/agents", func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/", h.handleAdminListAgents)
			})

			// Spec #09 V2 Item 5b — admin PromQL pass-through that
			// BYPASSES scopeQueryByCluster. Required for tenant-scoped
			// observability metrics (kubebolt_agent_grpc_*,
			// kubebolt_prom_write_*) which don't carry a cluster_id
			// label — applying cluster scoping returns 0 series. The
			// /admin/ingest-activity page uses these instead of the
			// public /metrics/query{,_range} routes.
			r.Route("/admin/metrics", func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/query", h.handleAdminMetricsQuery)
				r.Get("/query_range", h.handleAdminMetricsQueryRange)
			})

			// Integrations catalog — list + read are deliberately OUTSIDE
			// requireConnector so the page works on a fresh install with
			// no clusters yet. The handlers degrade to metadata-only
			// (StatusNotInstalled) when conn==nil; install/configure
			// stay inside the cluster-required group below.
			r.Get("/integrations", h.handleListIntegrations)
			r.Get("/integrations/{id}", h.handleGetIntegration)

			// Agent onboarding helpers — surface backend auth posture,
			// deployment/ingest defaults, and let the dialog issue an ingest
			// token. OUTSIDE requireConnector on purpose: the add-cluster
			// wizard registers a NEW remote cluster, so it must work even when
			// the ACTIVE cluster is down (e.g. a disconnected agent-proxy).
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/integrations/agent/auth-info", h.handleAgentAuthInfo)
				r.Get("/integrations/agent/install-defaults", h.handleAgentInstallDefaults)
				r.Post("/integrations/agent/issue-token", h.handleAgentIssueToken)
			})

			// Cluster overview lives OUTSIDE requireConnector: for a metrics-only cluster
			// it serves a KSM-derived overview (no connector needed) and owns its own
			// genuine no-connector 503.
			r.Get("/cluster/overview", h.getClusterOverview)

			// Datos POR CLUSTER a nivel de ORGANIZACIÓN, fuera de requireConnector.
			// Todo lo que va aquí dentro llega con el alcance de cluster ya resuelto
			// en el contexto (cluster_scope.go): en OSS no estrecha nada, pero un
			// handler nuevo hereda el filtro en vez de tener que acordarse de pedirlo.
			r.Group(func(r chi.Router) {
				r.Use(h.WithClusterScope)

				// Estado REAL de cada cluster para la flota. Lee el store de
				// insights —que no necesita connector— en vez del engine, que
				// sí. Ver insights_summary.go.
				// Security findings live OUTSIDE requireConnector too: they are
				// PERSISTED by the sweep, so serving them needs no live connection —
				// and they span every cluster, so gating them on the ACTIVE cluster's
				// connector would 503 the whole dashboard because one unrelated cluster
				// went away. Both handlers own their genuine 503 when persistence is
				// disabled (nil store).
				r.Get("/findings", h.handleListFindings)
				// Workload-first aggregation. Registered BEFORE the {fingerprint} route
				// so "workloads" is not swallowed as a fingerprint.
				r.Get("/findings/workloads", h.handleListFindingWorkloads)
				// Per-row drill-down: the stored record comes back even when the
				// cluster is gone; the handler degrades to live:false rather than 503.
				r.Get("/findings/{fingerprint}", h.handleFindingDetail)
				r.Get("/runtime-events", h.handleListRuntimeEvents)

				r.Get("/insights/summary", h.handleInsightsSummary)

				// Episode history (2.1.0) — OUTSIDE requireConnector on purpose:
				// history must answer for DEAD clusters, which is exactly when the
				// question arrives.
				r.Get("/insights/episodes", h.handleListEpisodes)
				r.Get("/insights/episodes/{id}", h.handleGetEpisode)
				// Operational episodes: the org's bursts — cross-cluster, so
				// outside requireConnector like the rest of history.
				r.Get("/insights/operational-episodes", h.handleOperationalEpisodes)
				// The shift report: «while you were away», hung from the
				// requesting user's presence anchor.
				r.Get("/insights/shift-report", h.handleShiftReport)
				// Presence beacon: Home renders → the user "saw" the dashboard.
				// Any role; 204-noop where presence doesn't exist.
				r.Post("/account/dashboard-seen", h.handleMarkDashboardSeen)
				// Mutes (#54) — display state, no cluster required (a mute on a
				// dead cluster must still be listable and removable). Reading is
				// for everyone; mutating takes editor and leaves an audit record
				// with what / why / until when (auditMutation in the handlers).
				r.Route("/insights/mutes", func(r chi.Router) {
					r.Get("/", h.handleListMutes)
					r.Group(func(r chi.Router) {
						r.Use(auth.RequireRole(auth.RoleEditor))
						r.Post("/", h.handleCreateMute)
						r.Delete("/{id}", h.handleDeleteMute)
					})
				})
				// Rule policies (#44 step 1) — admin, OUTSIDE requireConnector
				// (policy is install state, no cluster needed). Mutations are
				// audited: a threshold / severity change alters what the operator
				// gets told.
				r.Route("/admin/insight-policies", func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					r.Use(h.auditAdminRoutes("insight-policy", false))
					r.Get("/", h.handleListInsightPolicies)
					r.Put("/{rule}", h.handlePutInsightPolicy)
					r.Delete("/{rule}", h.handleDeleteInsightPolicy)
				})
			})

			// Search lives OUTSIDE requireConnector for the same reason. Its fleet
			// fan-out (?scope=fleet) searches every cluster the caller may see, so
			// gating it on the ACTIVE cluster's connector meant ⌘K went 503 exactly
			// when one cluster died — the moment you most want to look across the
			// fleet. The single-cluster path owns its own no-connector 503 already
			// (search.go), so nothing loses its guard by moving out.
			r.Get("/search", h.handleSearch)

			// Copilot chat — any role can ask questions. OUTSIDE requireConnector
			// on purpose: with no cluster (or a metrics-only one) Kobi still
			// answers about itself, KubeBolt and Kubernetes — the handler injects
			// the no-cluster session note and the executor gates tools per-tool.
			// The middleware here was why "Hola" got a bare 503 before the model
			// ever saw it. Kobi/AI stays gated until the org's email is verified
			// (cost control).
			r.Post("/copilot/chat", h.HandleCopilotChat)
			// Sec #9: compact is part of the same Kobi surface — gate it too,
			// else an unverified org can drive AI spend via /compact. It never
			// touches the connector (it summarizes the transcript), so it needs
			// no cluster either.
			r.Post("/copilot/compact", h.HandleCopilotCompact)

			// All other endpoints require an active cluster connection
			r.Group(func(r chi.Router) {
				r.Use(h.requireConnector)

				// Read endpoints — any authenticated role (Viewer+)
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
				r.Get("/topology", h.getTopology)
				r.Get("/insights", h.getInsights)
				r.Get("/events", h.getEvents)
				// Helm releases — read-only first-class (Sprint 4).
				r.Get("/helm/releases", h.handleListHelmReleases)
				r.Get("/helm/releases/{namespace}/{name}", h.handleGetHelmRelease)
				r.Get("/deploys", h.handleDeploys)
				r.Get("/metrics/{type}/{namespace}/{name}", h.getMetrics)

				// Integrations — install / configure / uninstall mutate
				// the cluster and require Admin. List + get live
				// outside this group (above) so the catalog renders
				// even with no cluster connected.
				r.Group(func(r chi.Router) {
					r.Use(auth.RequireRole(auth.RoleAdmin))
					// Cluster scoped: installing an integration happens ON the
					// connected cluster, so the stamp is real information.
					r.Use(h.auditAdminRoutes("integration", true))
					r.Post("/integrations/{id}/install", h.handleInstallIntegration)
					r.Get("/integrations/{id}/config", h.handleGetIntegrationConfig)
					r.Put("/integrations/{id}/config", h.handlePutIntegrationConfig)
					r.Delete("/integrations/{id}", h.handleUninstallIntegration)
				})

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
					// Set HPA bounds — strategic merge patch on
					// spec.minReplicas / spec.maxReplicas. Scoped to
					// hpas / horizontalpodautoscalers (autoscaling/v1).
					// Server-side cap at maxReplicas=1000. See
					// internal/copilot-execution-capacity/06-insight-rule-coverage.md.
					r.Post("/resources/{type}/{namespace}/{name}/set-bounds", h.handleSetHpaBounds)
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
					// Evict pod — single-pod version of drain. Uses the
					// policy/v1 Eviction API which respects any PDB
					// protecting the pod; returns 429 (pdbBlocked) when
					// blocked. Editor+ — same gate as workload Restart
					// since evict is the graceful cousin of delete that
					// operators reach for during maintenance windows.
					// Bulk eviction (drain) stays Admin below; single-pod
					// evict is mid-risk because the PDB-respect makes it
					// safer than force-delete.
					r.Post("/resources/{type}/{namespace}/{name}/evict", h.handleEvictPod)
					// Debug — inject an ephemeral container into a
					// running pod. Item 4 / C1 from the pod-actions
					// audit. Editor+ matches Terminal tab's exec
					// gate; both expose process-level access to
					// running containers, the ephemeral-container
					// variant just works on distroless/scratch where
					// `kubectl exec` can't find a shell. Returns the
					// auto-generated container name so the UI can
					// jump to the Terminal tab pre-selected.
					r.Post("/resources/{type}/{namespace}/{name}/debug", h.handleDebugPod)
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
