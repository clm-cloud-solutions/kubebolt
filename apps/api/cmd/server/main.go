package main

import (
	"context"
	crypto_rand "crypto/rand"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent"
	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/api"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/logging"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
	"github.com/kubebolt/kubebolt/apps/api/internal/updatecheck"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// version is set at build time via -ldflags.
var version = "dev"

// frontendFS embeds the production-built React frontend.
// When building the single binary, copy apps/web/dist/ to apps/api/cmd/server/web/dist/
// before running go build. If the directory doesn't exist, the binary works in API-only mode.
//
//go:embed all:web/dist
var embeddedFS embed.FS

const helpText = `KubeBolt %s — Instant Kubernetes Monitoring & Management

USAGE:
  kubebolt [flags]

FLAGS:
  --kubeconfig PATH         Path to kubeconfig file (default: $KUBECONFIG or ~/.kube/config)
  --port N                  HTTP server port (default: 8080)
  --host ADDR               Bind address (default: 0.0.0.0)
  --open                    Auto-open browser on start
  --metric-interval N       Metrics polling interval in seconds (default: 30)
  --insight-interval N      Insight evaluation interval in seconds (default: 60)
  --version                 Print version and exit
  --help                    Show this help message

ENVIRONMENT VARIABLES:
  Configuration is also loaded from a '.env' file in the current directory.
  CLI flags > system env vars > .env file > defaults.

  Logging:
    KUBEBOLT_LOG_LEVEL              debug | info (default) | warn | error
    KUBEBOLT_LOG_FORMAT             text (default) | json
    KUBEBOLT_LOG_DIR                When set, tees logs to $DIR/kubebolt.log
    KUBEBOLT_AI_DEBUG               legacy; "1" forces LOG_LEVEL=debug

  Authentication:
    KUBEBOLT_AUTH_ENABLED           Enable login (default: true)
    KUBEBOLT_ADMIN_PASSWORD         Initial admin password (generated if unset)
    KUBEBOLT_JWT_SECRET             JWT signing secret (persisted in DB if unset)
    KUBEBOLT_DATA_DIR               Directory for user database (default: ./data)

  AI Copilot (optional, BYO key):
    KUBEBOLT_AI_PROVIDER            anthropic | openai | custom
    KUBEBOLT_AI_API_KEY             Provider API key (enables copilot)
    KUBEBOLT_AI_MODEL               Model name (uses provider default if unset)
    KUBEBOLT_AI_BASE_URL            Custom endpoint for self-hosted providers

EXAMPLES:
  # Connect to current kubeconfig context
  kubebolt

  # Specific kubeconfig and port
  kubebolt --kubeconfig ~/.kube/prod.yaml --port 3000

  # Auto-open browser on start
  kubebolt --open

  # Use a .env file in the current directory for configuration
  echo "KUBEBOLT_ADMIN_PASSWORD=MyPass123" > .env
  kubebolt

LINKS:
  Website:       https://clm-cloud-solutions.github.io/kubebolt/
  Documentation: https://clm-cloud-solutions.github.io/kubebolt/docs.html
  GitHub:        https://github.com/clm-cloud-solutions/kubebolt
  License:       MIT
`

// fatal logs at error level with the given message/attrs and exits with code 1.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func main() {
	// Capture every KUBEBOLT_* env var as the FIRST thing main() does,
	// before any flag parsing or subsystem init can mutate the process
	// environment. Surfaced via /admin/settings/booted-with so operators
	// can answer "what did Helm wire into this container?" without
	// kubectl-exec to inspect /proc/1/environ.
	bootEnv := api.SnapshotKubeboltEnv(os.Environ())

	cfg := config.DefaultConfig()

	var host string
	var showVersion bool
	var openBrowser bool
	var resetAdminPassword string

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, helpText, version)
	}

	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	flag.IntVar(&cfg.Port, "port", cfg.Port, "HTTP server port")
	flag.StringVar(&host, "host", "0.0.0.0", "Bind address")
	flag.IntVar(&cfg.MetricInterval, "metric-interval", cfg.MetricInterval, "Metrics polling interval in seconds")
	flag.IntVar(&cfg.InsightInterval, "insight-interval", cfg.InsightInterval, "Insight evaluation interval in seconds")
	flag.BoolVar(&openBrowser, "open", false, "Auto-open browser on start")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.StringVar(&resetAdminPassword, "reset-admin-password", "", "Reset the admin user's password to the given value, then exit. For helm: kubectl exec deploy/kubebolt-api -- kubebolt --reset-admin-password=NEW")
	flag.Parse()

	if showVersion {
		fmt.Printf("KubeBolt %s\n", version)
		os.Exit(0)
	}

	// Reset-admin-password mode: open the auth DB, change the admin
	// user's password hash, exit. Bypasses the rest of the boot path
	// because we don't want to spin up a server while we're rotating
	// credentials. Mirrors `grafana-cli admin reset-admin-password`.
	if resetAdminPassword != "" {
		if err := runResetAdminPassword(resetAdminPassword); err != nil {
			fmt.Fprintf(os.Stderr, "reset-admin-password: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "admin password reset OK")
		os.Exit(0)
	}
	// Env-var path for the helm upgrade flow: helm sets
	// KUBEBOLT_RESET_ADMIN_PASSWORD on the deployment, the new pod
	// (with strategy=Recreate the old one is already gone, so the
	// BoltDB lock is free) runs the reset and CONTINUES normal boot
	// — server starts as usual with the new password active. The
	// operator removes the value on the next helm upgrade.
	if v := strings.TrimSpace(os.Getenv("KUBEBOLT_RESET_ADMIN_PASSWORD")); v != "" {
		if err := runResetAdminPassword(v); err != nil {
			fatal("reset-admin-password env failed", slog.String("error", err.Error()))
		}
		slog.Info("admin password reset via KUBEBOLT_RESET_ADMIN_PASSWORD; remove this env var on the next upgrade")
	}

	// Load .env file (if present) before reading any config.
	// System env vars take precedence — .env only fills in gaps.
	config.LoadDotEnv(".env")

	// Install the structured logger as early as possible, after .env is loaded
	// so KUBEBOLT_LOG_* vars from the file are honored.
	logging.Setup(logging.LoadOptionsFromEnv())

	if cfg.Kubeconfig == "" {
		if env := os.Getenv("KUBECONFIG"); env != "" {
			cfg.Kubeconfig = env
		} else {
			home, _ := os.UserHomeDir()
			cfg.Kubeconfig = home + "/.kube/config"
		}
	}

	// Check if frontend is embedded
	var frontendFS fs.FS
	if dir, err := fs.Sub(embeddedFS, "web/dist"); err == nil {
		if _, err := fs.Stat(dir, "index.html"); err == nil {
			frontendFS = dir
			slog.Info("embedded frontend detected")
		}
	}

	mode := "api-only"
	if frontendFS != nil {
		mode = "single-binary"
	}
	slog.Info("kubebolt starting",
		slog.String("version", version),
		slog.String("kubeconfig", cfg.Kubeconfig),
		slog.String("listen", fmt.Sprintf("%s:%d", host, cfg.Port)),
		slog.String("mode", mode),
	)

	// Create WebSocket hub
	wsHub := websocket.NewHub()
	go wsHub.Run()

	// Create Cluster Manager (handles connector, collector, engine lifecycle)
	manager, err := cluster.NewManager(
		cfg.Kubeconfig,
		wsHub,
		time.Duration(cfg.MetricInterval)*time.Second,
		time.Duration(cfg.InsightInterval)*time.Second,
	)
	if err != nil {
		fatal("failed to create cluster manager", slog.String("error", err.Error()))
	}
	defer manager.Stop()

	// Load copilot configuration from KUBEBOLT_AI_* env vars
	copilotCfg := config.LoadCopilotConfig()
	if copilotCfg.Enabled {
		attrs := []any{
			slog.String("provider", copilotCfg.Primary.Provider),
			slog.String("model", copilotCfg.Primary.Model),
		}
		if copilotCfg.Fallback != nil {
			attrs = append(attrs,
				slog.String("fallbackProvider", copilotCfg.Fallback.Provider),
				slog.String("fallbackModel", copilotCfg.Fallback.Model),
			)
		}
		slog.Info("AI copilot enabled", attrs...)
	} else {
		slog.Info("AI copilot disabled (KUBEBOLT_AI_API_KEY not set)")
	}

	// Load auth configuration from KUBEBOLT_AUTH_* env vars
	authCfg := config.LoadAuthConfig()

	var authHandlers *auth.Handlers
	var tenantHandlers *auth.TenantHandlers
	var agentAuthBundle *agent.AuthenticatorBundle
	var copilotUsage *copilot.UsageStore
	// settingsRuntime backs UI-editable config (spec #09). Nil when auth
	// is disabled — same gate as the rest of the admin surface, since
	// persistence requires BoltDB to be open. Constructed inside the
	// authCfg.Enabled block where `store` and the JWT secret exist.
	var settingsRuntime *settings.Runtime
	// Hoisted out of the auth-enabled scope so the API router can see
	// it for the agent integration's "issue token + create Secret"
	// flow (router-level handler talks to the same store the agent
	// gRPC interceptor validates against).
	var tenantsStore *auth.TenantsStore
	// Persistent agent registry — only enabled when auth is on (the
	// BoltDB file only exists in that path). When nil, the in-memory
	// registry survives a backend restart by losing all state, same
	// as pre-Sprint-A.5 behavior.
	var agentStore channel.AgentStore
	// Persistent insights store (Sprint 0) — same BoltDB-only gating as
	// agentStore. nil → engines run in-memory-only (pre-Sprint-0 behavior).
	var insightStore insights.InsightStore
	// Durable mutation-audit store (Sprint 1) — same BoltDB-only gating.
	// nil → mutations are slog-audited only (pre-Sprint-1 behavior).
	var actionAuditStore audit.Store

	// Fleet-wide Prom remote_write limit defaults (Phase 3 Day 1-3).
	// Loaded once at startup from KUBEBOLT_PROM_WRITE_DEFAULT_* env
	// vars and shared between two consumers:
	//   - TenantHandlers (renders the "defaults" view of /admin/tenants/:id/limits)
	//   - PromRateLimiter (enforces these when a tenant has no override)
	// Loading outside the auth block means rate limiting works even
	// when auth is disabled — operators still want the bucket gate
	// against a misbehaving Prom in single-tenant deployments.
	promLimitsCfg := config.LoadPromWriteLimitsConfig()
	promLimitsEffective := auth.EffectiveLimits{
		WriteSamplesPerSec: promLimitsCfg.WriteSamplesPerSec,
		WriteBurstSamples:  promLimitsCfg.WriteBurstSamples,
		MaxActiveSeries:    promLimitsCfg.MaxActiveSeries,
	}

	// Notifications env baseline. Loaded here (before authCfg.Enabled
	// block) so both the settings runtime and the boot-time notifier
	// wiring below see the same config snapshot. Hot-reload via the
	// admin Settings PUT handler swaps the live manager state at runtime;
	// this remains the fallback layer for any field not overridden.
	envNotifCfg := config.LoadNotificationsConfig()

	// General settings env baseline (display name, default refresh
	// interval). Trivially hot-reloadable — no live subsystem caches
	// these values; the UI reads them per request.
	envGeneralCfg := config.LoadGeneralConfig()

	if authCfg.Enabled {
		slog.Info("authentication enabled")

		store, err := auth.NewStore(authCfg.DataDir)
		if err != nil {
			fatal("failed to open auth store", slog.String("error", err.Error()))
		}
		defer store.Close()

		// Resolve JWT secret: env var > persisted in DB > generate and persist
		if !authCfg.JWTSecretFromEnv {
			if secret, err := store.GetSetting("jwt_secret"); err == nil {
				authCfg.JWTSecret = secret
				slog.Info("JWT secret loaded from database")
			} else {
				secret := make([]byte, 32)
				if _, err := crypto_rand.Read(secret); err != nil {
					fatal("failed to generate JWT secret", slog.String("error", err.Error()))
				}
				if err := store.SetSetting("jwt_secret", secret); err != nil {
					fatal("failed to persist JWT secret", slog.String("error", err.Error()))
				}
				authCfg.JWTSecret = secret
				slog.Info("JWT secret generated and persisted to database")
			}
		}

		seeded, err := store.SeedAdmin(authCfg.InitialAdminPassword)
		if err != nil {
			fatal("failed to seed admin user", slog.String("error", err.Error()))
		}
		// Print the admin password banner ONLY when we actually created
		// the admin user AND it was a generated password (env-supplied
		// passwords are already known to the operator). On subsequent
		// restarts this branch is skipped — the password printed at first
		// boot is the only one that's valid.
		//
		// In-cluster (helm install) we ALSO persist the generated password
		// to a Secret in the backend's own namespace so operators can
		// recover it even after rotating logs. Only on first boot, only
		// when not env-supplied — env-supplied passwords are the operator's
		// responsibility to track.
		if seeded {
			slog.Info("default admin user created", slog.String("username", "admin"))
			if !authCfg.AdminPasswordFromEnv {
				config.PrintAdminPasswordBanner(authCfg.InitialAdminPassword)
				if err := persistAdminPasswordSecret(authCfg.InitialAdminPassword); err != nil {
					slog.Warn("could not persist admin password to a Secret (the printed banner is the only copy)",
						slog.String("error", err.Error()),
					)
				}
			}
		}

		// Settings runtime is built HERE — before the JWT service —
		// because the resolved Auth() config (env baseline + BoltDB
		// override) feeds the JWT service's TTLs. Without this order
		// the JWT service would always use env TTLs and ignore any
		// previously-saved UI override across restarts.
		// Spec #09 V2 — the IngestChannel domain centralizes the env
		// baseline for the agent ↔ backend communication plane (auth
		// mode, rate limits, auto-registration, remote_write receiver,
		// tunnel timeouts). Consumer subsystems read live values via
		// settingsRuntime.IngestChannel() instead of os.Getenv directly.
		envIngestChannelCfg := config.LoadIngestChannelConfig()

		if rt, err := settings.NewRuntime(store, copilotCfg, envNotifCfg, authCfg, envGeneralCfg, envIngestChannelCfg, authCfg.JWTSecret); err != nil {
			slog.Warn("settings runtime disabled — admin /settings endpoints unavailable",
				slog.String("error", err.Error()))
		} else {
			settingsRuntime = rt
			// Apply persisted Auth overrides onto the live authCfg so the
			// JWT service + handlers below pick up the resolved TTLs.
			// JWTSecret stays from the env/DB path above; only the
			// UI-editable subset of AuthConfig gets merged.
			resolvedAuth := rt.Auth()
			authCfg.AccessTokenExpiry = resolvedAuth.AccessTokenExpiry
			authCfg.RefreshTokenExpiry = resolvedAuth.RefreshTokenExpiry
			slog.Info("settings runtime initialised — admin UI can override env config")
		}

		jwtSvc := auth.NewJWTService(authCfg)
		authHandlers = auth.NewHandlers(store, jwtSvc, authCfg)

		// Attach cluster storage (uses the same BoltDB for persistence of
		// user-uploaded kubeconfigs and display name overrides).
		configsBucket, displayBucket, uidBucket := auth.ClusterBuckets()
		clusterStorage := cluster.NewStorage(store.DB(), configsBucket, displayBucket, uidBucket)
		if err := manager.SetStorage(clusterStorage); err != nil {
			slog.Warn("failed to attach cluster storage, uploaded clusters won't persist",
				slog.String("error", err.Error()))
		}

		// Attach the copilot usage store so admin analytics survive restarts.
		// Shares the same BoltDB file; bucket created by auth.NewStore.
		copilotUsage = copilot.NewUsageStore(store.DB(), auth.CopilotSessionsBucket())

		// Tenants + ingest tokens (Sprint A). Auto-seeds the "default"
		// tenant on first boot; the admin REST surface lets operators
		// create more, issue tokens, and rotate / revoke them.
		ts, err := auth.NewTenantsStore(store.DB())
		if err != nil {
			fatal("failed to open tenants store", slog.String("error", err.Error()))
		}
		tenantsStore = ts

		// REST API tokens — long-lived bearer tokens for non-interactive
		// callers (service tokens kbs_ for Autopilot / EE; customer keys
		// kbk_ later). Shares the same BoltDB file. Wiring it makes
		// RequireAuth accept these in addition to the user-session JWT.
		apiTokenStore, err := auth.NewAPITokenStore(store.DB())
		if err != nil {
			fatal("failed to open api token store", slog.String("error", err.Error()))
		}
		authHandlers.SetAPITokenStore(apiTokenStore)

		// Persistent agent registry. Restart-survival for the
		// agent-proxy cluster list — operators expect their dashboard
		// to keep showing connected clusters across `helm upgrade` of
		// the backend, not to "blank" until each agent reconnects.
		// Bucket already created in auth.NewStore; we just wire the
		// store + registry binding here.
		agentStore = channel.NewBoltAgentStore(store.DB(), auth.AgentsBucket())

		// Persistent insights store (Sprint 0). Insight identities survive
		// restarts, scoped by tenant + cluster, feeding history + restart-safe
		// notification dedup + Kobi/Autopilot provenance. Bucket created in
		// auth.NewStore. tenantID = the auto-seeded "default" tenant in OSS.
		insightStore = insights.NewBoltInsightStore(store.DB(), auth.InsightsBucket())
		insightTenantID := auth.DefaultTenantName
		if dt, err := tenantsStore.GetDefaultTenant(); err == nil && dt != nil {
			insightTenantID = dt.ID
		}
		manager.SetInsightStore(insightStore, insightTenantID)

		// Durable mutation-audit store (Sprint 1). Persists every cluster
		// mutation (UI + Kobi-proposed) to the kobi_actions bucket so the
		// admin action-history view survives restarts. The cluster-id
		// resolver stamps the active cluster onto each record.
		actionAuditStore = audit.NewBoltStore(store.DB(), auth.KobiActionsBucket())
		api.SetAuditStore(actionAuditStore, func() string {
			if c := manager.Connector(); c != nil {
				return c.ClusterUID()
			}
			return ""
		})

		// Build the agent authenticator. TokenReview mode is best-effort:
		// if there is no in-cluster client (KubeBolt running outside K8s,
		// e.g. via `go run`) the bundle still includes BearerIngestAuth
		// for SaaS / cross-cluster ingest tokens.
		factoryOpts := agent.LoadAuthenticatorOptionsFromEnv()
		// Spec #09 V2 — BoltDB override wins over the env-derived value
		// the Load function picked up. Restart-required field, so
		// pendingRestart will surface in the UI if this diverges from
		// what's been captured in the boot snapshot.
		if settingsRuntime != nil {
			factoryOpts.TokenReviewAudience = settingsRuntime.IngestChannel().AgentTokenAudience
		}
		factoryOpts.TenantsStore = tenantsStore
		if c, err := agent.NewInClusterKubeClient(); err == nil {
			factoryOpts.KubeClient = c
		} else {
			slog.Info("agent auth: in-cluster client unavailable; TokenReview mode skipped",
				slog.String("error", err.Error()),
			)
		}
		bundle, err := agent.BuildAuthenticator(context.Background(), factoryOpts)
		if err != nil {
			fatal("agent auth bundle", slog.String("error", err.Error()))
		}
		agentAuthBundle = bundle
		tenantHandlers = auth.NewTenantHandlers(tenantsStore, promLimitsEffective, bundle.AsCacheInvalidators()...)

		// Now that the JWT service + admin handlers are wired with the
		// resolved authCfg, snapshot what the running process was built
		// from. Subsequent PUTs to /admin/settings/auth compare against
		// this baseline to compute pendingRestart.
		if settingsRuntime != nil {
			settingsRuntime.CaptureAuthBootSnapshot()
		}
	} else {
		slog.Info("authentication disabled (KUBEBOLT_AUTH_ENABLED=false)")
		authHandlers = auth.NewNoOpHandlers()
	}

	// Wire up Slack/Discord/Email notifiers from the env baseline loaded
	// above. BuildNotifiers + ConfigFromNotifications are shared with the
	// admin Settings → Notifications PUT handler so boot-time and hot-
	// reload produce the same notifier composition.
	var notifManager *notifications.Manager
	{
		// Resolved config = env baseline + any persisted UI overrides.
		// At boot we read settingsRuntime so admins keep their saved
		// notifications config across process restarts. When auth is
		// disabled (no settingsRuntime), env is the only layer.
		bootNotifCfg := envNotifCfg
		if settingsRuntime != nil {
			bootNotifCfg = settingsRuntime.Notifications()
		}
		notifiers := notifications.BuildNotifiers(bootNotifCfg)
		if bootNotifCfg.SlackWebhookURL != "" {
			slog.Info("slack notifications enabled")
		}
		if bootNotifCfg.DiscordWebhookURL != "" {
			slog.Info("discord notifications enabled")
		}
		if bootNotifCfg.Email.Enabled() {
			slog.Info("email notifications enabled",
				slog.String("mode", bootNotifCfg.Email.DigestMode),
				slog.Int("recipients", len(bootNotifCfg.Email.To)),
			)
		}
		notifManager = notifications.NewManager(notifiers, notifications.ConfigFromNotifications(bootNotifCfg))
		switch {
		case !notifManager.MasterEnabled():
			slog.Info("notifications master-disabled (KUBEBOLT_NOTIFICATIONS_ENABLED=false)")
		case notifManager.Enabled():
			slog.Info("notifications configured",
				slog.String("minSeverity", notifManager.MinSeverity()),
				slog.Duration("cooldown", notifManager.Cooldown()),
				slog.Bool("includeResolved", notifManager.IncludeResolved()),
			)
			manager.SetOnNewInsight(func(clusterContext string, insight models.Insight) {
				notifManager.Enqueue(clusterContext, insight)
			})
			if notifManager.IncludeResolved() {
				manager.SetOnResolvedInsight(func(clusterContext string, insight models.Insight) {
					notifManager.EnqueueResolved(clusterContext, insight)
				})
			}
		default:
			slog.Info("notifications disabled (no channels configured)")
		}
	}
	// Ensure the email digest flusher (if any) drains on shutdown
	defer notifManager.Stop()

	// Integrations registry. Populated here so adding a new adapter
	// is one line — the handlers pick it up automatically.
	integrationRegistry := integrations.NewRegistry()

	// VM probe client + active-cluster-UID closure — shared by every
	// integration card that needs to verify "samples are reaching THIS
	// backend" rather than just "the resource exists in the cluster".
	// Lifted out of the per-integration blocks so the Agent card (which
	// is registered unconditionally, regardless of auth) can use the
	// same probe surface as the Prometheus / Prometheus-read cards.
	//
	// Note on UID source: `ActiveAgentProxyClusterID` alone is
	// insufficient — it returns "" for direct-kubeconfig clusters
	// (EKS, GKE, AKS reached via the operator's local kubeconfig,
	// never through the agent proxy). Those clusters DO have a UID
	// (kube-system namespace UUID) but only the active Connector
	// knows it. We prefer the agent-proxy value when set (it's
	// available at boot before the Connector finishes) and fall
	// back to the Connector's value otherwise. Same selection the
	// handlers' `activeClusterUID()` helper uses.
	vmProbeURL := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL")
	if vmProbeURL == "" {
		vmProbeURL = "http://localhost:8428"
	}
	vmProbeClient := integrations.NewVMProbeClient(vmProbeURL, nil)
	activeClusterUID := func() string {
		if uid := manager.ActiveAgentProxyClusterID(); uid != "" {
			return uid
		}
		if conn := manager.Connector(); conn != nil {
			return conn.ClusterUID()
		}
		return ""
	}

	// Agent integration — registered unconditionally. The samplesProbe
	// closes the cross-backend false-positive (Fix #12 session 11-A
	// v3): without it, an operator's local kubebolt that has another
	// cluster's kubeconfig context would falsely report "Agent
	// installed" because the DaemonSet IS visible via kubeconfig, but
	// the agent itself is configured to ship to a DIFFERENT
	// KUBEBOLT_BACKEND_URL. The probe asks VM whether kubebolt_agent_info
	// samples tagged with this cluster's UID are arriving here.
	integrationRegistry.Register(integrations.NewAgent(activeClusterUID, vmProbeClient.AgentSamplesForCluster))

	// Prometheus integrations are gated on the TenantsStore: detection
	// reads ingest-token usage from there as the heartbeat signal.
	// When auth is disabled the store isn't wired and the receiver
	// stamps every sample as "anonymous", so a per-tenant integration
	// card has no signal to read — skip registration entirely.
	if tenantsStore != nil {
		// Sample-presence probe (Fix #8 session 11-A v3): probes for
		// `prometheus_build_info` (Prom self-metric that the
		// kubebolt-agent never emits, even with Mode C broad
		// matchers) rather than `up` (which Mode C does ship from
		// GMP/AMP/AMW and would collide with the discriminator).
		integrationRegistry.Register(integrations.NewPrometheus(
			tenantsStore,
			activeClusterUID,
			vmProbeClient.PromSamplesForCluster,
		))
		// Prometheus (read) — Mode C. Same vmProbeClient + same active
		// cluster UID closure; the only difference is the underlying
		// query (`kubebolt_promread_leader == 1` vs Prom-only metric).
		// Inside the same `tenantsStore != nil` block as Prometheus
		// because both depend on cluster_id stamping at the receiver.
		integrationRegistry.Register(integrations.NewPrometheusRead(
			activeClusterUID,
			vmProbeClient.PromreadActiveForCluster,
		))
	}

	// Resolve the agent auth enforcement that the router will surface
	// to the UI for "refuse proxy + empty auth on enforced backend"
	// gating. This mirrors the logic applied to agentAuthCfg.Enforcement
	// further down: env says X, but if app auth is disabled (no
	// agentAuthBundle), the agent gRPC server forces "disabled" — the
	// UI must reflect the EFFECTIVE mode, not the requested one.
	//
	// Spec #09 V2 — read the resolved value (env + BoltDB override)
	// via the settings runtime so UI changes take effect on next boot.
	// Falls back to env-only when the runtime isn't available (auth
	// disabled at app level → no persistent store).
	resolvedEnforcement := string(agent.EnforcementDisabled)
	authModeSource := os.Getenv("KUBEBOLT_AGENT_AUTH_MODE")
	if settingsRuntime != nil {
		authModeSource = settingsRuntime.IngestChannel().AgentAuthMode
	}
	if authModeSource != "" {
		if parsed, ok := agent.ParseEnforcement(authModeSource); ok {
			resolvedEnforcement = string(parsed)
		}
	}
	if agentAuthBundle == nil && resolvedEnforcement != string(agent.EnforcementDisabled) {
		resolvedEnforcement = string(agent.EnforcementDisabled)
	}

	// Receiver auth mode (separate from gRPC channel — operators can run
	// the agent gRPC enforced while keeping remote_write disabled, or
	// vice-versa). Same three-tier semantics. Default mirrors the gRPC
	// channel: disabled for Sprint A migration.
	// Spec #09 V2 — read via the settings runtime so BoltDB override
	// wins over env baseline. Captured at boot for the router (the
	// h.promWriteAuthMode field). Note: this is currently a boot-time
	// capture; flipping the mode in the UI requires a restart for the
	// handler to pick up the change. A future iteration could move the
	// mode read into the handler per-request like the enabled flag —
	// for now V2 ships the simpler "set at boot" semantic, surfaced
	// via pendingRestart in the masked render.
	resolvedPromWriteEnforcement := string(agent.EnforcementDisabled)
	promWriteAuthModeSource := os.Getenv("KUBEBOLT_REMOTE_WRITE_AUTH_MODE")
	if settingsRuntime != nil {
		promWriteAuthModeSource = settingsRuntime.IngestChannel().RemoteWriteAuthMode
	}
	if promWriteAuthModeSource != "" {
		if parsed, ok := agent.ParseEnforcement(promWriteAuthModeSource); ok {
			resolvedPromWriteEnforcement = string(parsed)
		} else {
			slog.Warn("remote_write auth mode has unknown value, defaulting to disabled",
				slog.String("value", promWriteAuthModeSource))
		}
	}
	// Same defense-in-depth as the gRPC path: if there's no
	// TenantsStore, enforced is impossible — downgrade to disabled
	// (with a loud WARN). The router-side handler also defends, so a
	// future code path that sets enforced via a different config
	// surface still fails closed.
	if tenantsStore == nil && resolvedPromWriteEnforcement == string(agent.EnforcementEnforced) {
		slog.Warn("KUBEBOLT_REMOTE_WRITE_AUTH_MODE=enforced but TenantsStore not wired — falling back to disabled",
			slog.String("hint", "set KUBEBOLT_AUTH_ENABLED=true to enable token validation"))
		resolvedPromWriteEnforcement = string(agent.EnforcementDisabled)
	}

	// Per-tenant Prom remote_write rate limiter (Phase 3 Day 3).
	// Reuses the same promLimitsEffective the tenant admin API uses
	// for "defaults" rendering — single source of truth means an
	// operator changing the env var moves both the admin UI's
	// "Defaults" view AND the enforcement layer in lockstep.
	promRateLimiter := api.NewPromRateLimiter(promLimitsEffective)

	// Per-tenant observability metrics (Phase 3 Day 5). Exposed at
	// /metrics in the standard Prom text-exposition format. The
	// global prometheus.DefaultRegisterer is fine for production —
	// tests pass a fresh registry to isolate (see prom_write_metrics
	// _test.go).
	promWriteMetrics := api.NewPromWriteMetrics(prometheus.DefaultRegisterer)
	// Spec #09 V2 Item 5b — gRPC ingest counters powering the
	// /admin/ingest-activity panel. Same registry as promWriteMetrics
	// so the `/metrics` endpoint surfaces both ingest paths uniformly
	// (the panel queries them via PromQL after they're scraped into VM).
	agentGrpcMetrics := agent.NewGRPCIngestMetrics(prometheus.DefaultRegisterer)

	// Per-tenant cardinality tracker (Phase 3 Day 4). Background
	// goroutine polls VM every 30s for `count by (tenant_id)
	// ({tenant_id!=""})` and caches the result. Pre-forward gate
	// uses the cache + per-tenant MaxActiveSeries cap to reject 413
	// when exceeded. nil only if VM URL is empty (shouldn't happen
	// in production — bundled chart wires it always).
	var promCardinality *api.CardinalityTracker
	vmURL := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL")
	if vmURL != "" {
		promCardinality = api.NewCardinalityTracker(vmURL, promLimitsEffective, nil, 30*time.Second)
		// Hook the metrics gauge to the cardinality tracker so each
		// successful refresh updates kubebolt_prom_write_active_series.
		// The tracker doesn't reference the metrics package directly;
		// this callback is the bridge.
		promCardinality.OnSnapshot = promWriteMetrics.SetActiveSeries
		// Start the background refresh goroutine. It's tied to the
		// process lifetime via context.Background — the process
		// shuts down via SIGTERM, dragging the goroutine with it.
		go promCardinality.RunRefreshLoop(context.Background())
	}

	// Create API Router (with optional embedded frontend)
	// AgentRegistry is hoisted ABOVE NewRouter (vs the original
	// position after VMWriter creation below) because spec #09 V2
	// Item 5b adds /admin/agents which needs the registry threaded
	// through the router. The registry has no init-order dependencies
	// — it's a plain in-memory directory — so creating it early is
	// safe. SetAgentRegistry + SetStore wiring still runs further
	// down where the manager + Bolt store are available.
	agentRegistry := channel.NewAgentRegistry()

	// updateCheckSvc drives the "new KubeBolt version available" chip.
	// Cache TTL 6h keeps GitHub API traffic well under the unauth
	// rate limit (60/h per IP) even with multiple backends sharing
	// egress NAT. Dev builds short-circuit inside the service — no
	// GitHub call ever leaves the process.
	updateCheckSvc := updatecheck.New(version, updatecheck.DefaultRepo, updatecheck.DefaultCacheTTL)

	router := api.NewRouter(manager, wsHub, cfg.CORSOrigins, copilotCfg, copilotUsage, authHandlers, tenantHandlers, notifManager, integrationRegistry, resolvedEnforcement, tenantsStore, resolvedPromWriteEnforcement, promRateLimiter, promCardinality, promWriteMetrics, settingsRuntime, bootEnv, agentRegistry, updateCheckSvc)

	// Spec #09 V2 Item 5b — push the backend's own Prometheus
	// counters into VM every 30s so the /admin/ingest-activity panel
	// can query them via PromQL like every other dashboard in the
	// app. API and VM are co-located in every production topology
	// (same Helm release / same private network), so the write is
	// local — sub-millisecond latency, no firewall punchholes.
	// Reuses the same vmURL the cardinality tracker reads above
	// (constant once main has resolved KUBEBOLT_METRICS_STORAGE_URL).
	// Goroutine tied to process-lifetime context.Background — exits
	// when the process exits.
	if vmURL != "" {
		go api.SelfWriteMetricsToVM(context.Background(), prometheus.DefaultGatherer, vmURL)
	}

	// Mount embedded frontend if available
	if frontendFS != nil {
		api.MountFrontend(router, frontendFS)
	}

	// Start HTTP server
	addr := fmt.Sprintf("%s:%d", host, cfg.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	go func() {
		url := fmt.Sprintf("http://localhost:%d", cfg.Port)
		if frontendFS != nil {
			slog.Info("kubebolt ready", slog.String("url", url))
		} else {
			slog.Info("kubebolt API running", slog.String("url", url))
		}

		if openBrowser {
			openURL(url)
		}

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("HTTP server error", slog.String("error", err.Error()))
		}
	}()

	// Start agent ingest gRPC server (Phase 2 walking skeleton).
	// Listens on port 9090 and forwards samples to VictoriaMetrics.
	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()

	if vmURL == "" {
		vmURL = "http://localhost:8428"
	}
	agentAddr := os.Getenv("KUBEBOLT_AGENT_GRPC_ADDR")
	if agentAddr == "" {
		agentAddr = fmt.Sprintf("%s:9090", host)
	}

	writer := agent.NewVMWriter(vmURL)
	// AgentRegistry was created above NewRouter; wire it into the
	// cluster.Manager here so AddAgentProxyCluster can resolve
	// reachability via the live registry. Same effect as the
	// previous "create + wire" pair, just split across two sites
	// to accommodate the /admin/agents endpoint added in V2 Item 5b.
	manager.SetAgentRegistry(agentRegistry)

	// Tunnel idle timeout — set the package-level default so every
	// AgentProxyTransport spawned later (per-cluster, on connect)
	// inherits the operator-chosen value. Default 5m; 0 disables the
	// watchdog.
	if v := os.Getenv("KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			channel.DefaultTunnelIdleTimeout = d
			slog.Info("agent-proxy tunnel idle timeout configured",
				slog.Duration("timeout", d),
			)
		} else {
			slog.Warn("invalid KUBEBOLT_AGENT_TUNNEL_IDLE_TIMEOUT, using default",
				slog.String("requested", v),
				slog.Duration("default", channel.DefaultTunnelIdleTimeout),
			)
		}
	}
	// Spec #09 V2 — hot-reload the tunnel idle timeout from the
	// settings runtime. A 30s ticker re-reads the value and rewrites
	// the package-level default; subsequent tunnel constructions pick
	// up the new value, in-flight tunnels keep their captured value
	// (you can't retroactively shorten the watchdog of a running
	// session). Race-tolerant — concurrent reads of a Duration are
	// safe on the platforms KubeBolt supports (atomic word write),
	// and either old/new value is a valid config. Tied to agentCtx so
	// the ticker stops cleanly on shutdown alongside the gRPC server.
	if settingsRuntime != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-agentCtx.Done():
					return
				case <-ticker.C:
					channel.DefaultTunnelIdleTimeout = settingsRuntime.IngestChannel().AgentTunnelIdleTimeout
				}
			}
		}()
	}

	// Discover the cluster_id the backend itself runs in (the
	// kube-system namespace UID), best-effort. Hoisted ABOVE the
	// boot-restore loop so the loop's self-skip guard (BUG-3
	// regression below) has the value. Out-of-cluster dev runs
	// leave selfClusterID empty — the self-skip gates off in both
	// the boot-restore path AND the live-connect path that uses
	// agent.WithSelfClusterID(...) further down.
	selfClusterID := ""
	if kc, err := agent.NewInClusterKubeClient(); err == nil {
		if id, err := agent.DiscoverClusterID(context.Background(), kc); err == nil {
			selfClusterID = id
			slog.Info("backend self cluster_id discovered",
				slog.String("cluster_id", selfClusterID),
			)
		} else {
			slog.Info("backend self cluster_id discovery failed; agent-proxy self-skip disabled",
				slog.String("error", err.Error()),
			)
		}
	}

	// Persistence wiring + boot-time restore. When auth is disabled
	// (agentStore == nil) all of this is skipped and the registry
	// stays in-memory — same as pre-Sprint-A.5 behavior.
	if agentStore != nil {
		agentRegistry.SetStore(agentStore)

		// Replay persisted records so cluster.Manager's
		// agentProxyContexts is populated BEFORE the agent gRPC
		// server accepts traffic. Result: the cluster selector keeps
		// showing every previously-connected agent-proxy cluster from
		// the moment the backend boots, instead of "no clusters" for
		// the ~30s window each agent takes to reconnect.
		//
		// We only restore clusters whose agents advertised the
		// `kube-proxy` capability — a metrics-only agent (rbac.mode=
		// metrics) doesn't need an agent-proxy entry in the selector,
		// since it doesn't surface inventory through the tunnel.
		//
		// The display name uses the most-recent record per cluster
		// (sort by LastSeen descending). Multi-agent DaemonSets
		// converge on the same cluster name from KUBEBOLT_AGENT_
		// CLUSTER_NAME, so this picks any of them.
		records, err := agentStore.List()
		if err != nil {
			slog.Warn("failed to list persisted agent records on boot", slog.String("error", err.Error()))
		} else {
			seen := make(map[string]string) // cluster_id → display name
			for i := range records {
				rec := &records[i]
				if !rec.HasKubeProxy() {
					continue
				}
				// Last-write-wins on display name — records are
				// sorted (cluster_id, agent_id) so any of the
				// equivalent values land here. Good enough.
				if rec.DisplayName != "" {
					seen[rec.ClusterID] = rec.DisplayName
				} else if _, ok := seen[rec.ClusterID]; !ok {
					seen[rec.ClusterID] = ""
				}
			}
			restored := 0
			skipped := 0
			for clusterID, displayName := range seen {
				// BUG-3 regression guard: never restore an agent-proxy
				// row for the backend's own cluster — already exposed
				// via in-cluster context. The live-connect path uses
				// the same agent.IsSelfCluster helper inside
				// maybeAutoRegisterCluster; without this check the
				// boot-restore path silently re-creates the duplicate
				// row every restart (cluster-validation BUG-3).
				if agent.IsSelfCluster(clusterID, selfClusterID) {
					slog.Debug("boot restore skipped: agent record matches backend's own cluster_id (already in-cluster)",
						slog.String("cluster_id", clusterID),
					)
					skipped++
					continue
				}
				if _, err := manager.AddAgentProxyCluster(clusterID, displayName); err != nil {
					slog.Warn("failed to restore agent-proxy cluster",
						slog.String("cluster_id", clusterID),
						slog.String("display_name", displayName),
						slog.String("error", err.Error()),
					)
					continue
				}
				restored++
			}
			if restored > 0 || skipped > 0 {
				slog.Info("processed persisted agent-proxy records on boot",
					slog.Int("restored", restored),
					slog.Int("skipped_self_cluster", skipped),
				)
			}
		}

		// Prune disconnected records older than the horizon. Records
		// for agents currently connected (DisconnectedAt zero) never
		// expire. The horizon is configurable so SaaS operators can
		// shorten it if they don't want stale orgs in their data set.
		//
		// Spec #09 V2 — the horizon is hot-reloadable. The ticker
		// re-reads `settingsRuntime.IngestChannel().AgentRegistryPruneHorizon`
		// on every tick, so UI changes take effect on the next hourly
		// pass without a restart. Falls back to the env-only baseline
		// when the runtime isn't available (auth-disabled mode).
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			fallbackHorizon := config.DefaultAgentRegistryPruneHorizon
			if v := os.Getenv("KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON"); v != "" {
				if d, err := time.ParseDuration(v); err == nil && d > 0 {
					fallbackHorizon = d
				}
			}
			for {
				select {
				case <-agentCtx.Done():
					return
				case <-ticker.C:
					horizon := fallbackHorizon
					if settingsRuntime != nil {
						horizon = settingsRuntime.IngestChannel().AgentRegistryPruneHorizon
					}
					removed, err := agentStore.Prune(time.Now().UTC().Add(-horizon))
					if err != nil {
						slog.Warn("agent registry prune failed", slog.String("error", err.Error()))
						continue
					}
					if removed > 0 {
						slog.Info("agent registry pruned",
							slog.Int("removed", removed),
							slog.Duration("horizon", horizon),
						)
					}
				}
			}
		}()
	}

	// Insights retention (Sprint 0). Hourly prune of RESOLVED insight
	// records older than the horizon; active insights never expire. The
	// horizon is read from KUBEBOLT_INSIGHTS_RETENTION_HORIZON (default 7d)
	// each tick, so a restart picks up a change without a code edit.
	if insightStore != nil {
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-agentCtx.Done():
					return
				case <-ticker.C:
					horizon := 7 * 24 * time.Hour
					if v := os.Getenv("KUBEBOLT_INSIGHTS_RETENTION_HORIZON"); v != "" {
						if d, err := time.ParseDuration(v); err == nil && d > 0 {
							horizon = d
						}
					}
					removed, err := insightStore.Prune(time.Now().UTC().Add(-horizon))
					if err != nil {
						slog.Warn("insights prune failed", slog.String("error", err.Error()))
						continue
					}
					if removed > 0 {
						slog.Info("insights pruned",
							slog.Int("removed", removed),
							slog.Duration("horizon", horizon),
						)
					}
				}
			}
		}()
	}

	// Action-audit retention (Sprint 1). Hourly prune of audit records older
	// than KUBEBOLT_AUDIT_RETENTION_HORIZON (default 90d) — long enough for a
	// quarter of compliance history without unbounded growth.
	if actionAuditStore != nil {
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-agentCtx.Done():
					return
				case <-ticker.C:
					horizon := 90 * 24 * time.Hour
					if v := os.Getenv("KUBEBOLT_AUDIT_RETENTION_HORIZON"); v != "" {
						if d, err := time.ParseDuration(v); err == nil && d > 0 {
							horizon = d
						}
					}
					removed, err := actionAuditStore.Prune(time.Now().UTC().Add(-horizon))
					if err != nil {
						slog.Warn("audit prune failed", slog.String("error", err.Error()))
						continue
					}
					if removed > 0 {
						slog.Info("audit records pruned",
							slog.Int("removed", removed),
							slog.Duration("horizon", horizon),
						)
					}
				}
			}
		}()
	}

	// Auto-register agent-proxy clusters: when an agent advertises the
	// kube-proxy capability AND this flag is on, its cluster shows up
	// in ListClusters automatically. Off by default — single-cluster
	// self-hosted setups don't need it, and surprise discovery would
	// be hard to undo. Multi-cluster SaaS / fleet operators flip it on.
	// Spec #09 V2 — wrapped in a closure so the agent server reads the
	// live value on every registration. UI flips the toggle without
	// needing a restart: the next agent that connects sees the new
	// posture (currently-connected agents stay registered until they
	// reconnect). Falls back to the env-only value when the settings
	// runtime isn't wired (auth-disabled boot path).
	envAutoRegister := parseAutoRegisterFlag(os.Getenv("KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS"))
	autoRegisterFn := func() bool {
		if settingsRuntime != nil {
			return settingsRuntime.IngestChannel().AgentAutoRegisterClusters
		}
		return envAutoRegister
	}
	// Capture once for any boot-time decisions that depend on the
	// resolved value at startup (e.g., the boot-restore log line below
	// that mentions "autoregister=true").
	autoRegisterClusters := autoRegisterFn()
	if autoRegisterClusters {
		slog.Info("agent-proxy cluster auto-register enabled")
	}

	// selfClusterID already discovered above (before the boot-restore
	// loop) so both the boot path (BUG-3 fix) and the live-connect
	// path (BUG-2 fix) share the same value.
	ingestSrv := agent.NewServer(writer,
		agent.WithRegistry(agentRegistry),
		agent.WithClusterRegistrar(manager),
		agent.WithAutoRegisterClustersFunc(autoRegisterFn),
		agent.WithGRPCIngestMetrics(agentGrpcMetrics),
		agent.WithSelfClusterID(selfClusterID),
	)

	// Sprint A migration window: enforcement defaults to "disabled" so
	// existing fleets without auth credentials keep working. Operators
	// flip KUBEBOLT_AGENT_AUTH_MODE=enforced to require credentials.
	//
	// Spec #09 V2 — read the resolved auth mode (env + BoltDB) via the
	// settings runtime so UI overrides win. Restart-required: the
	// running interceptor is wired with whatever this resolves to at
	// boot; subsequent UI changes flip pendingRestart in the masked
	// render and apply on the next restart.
	agentAuthCfg := agent.AuthConfig{
		Enforcement: agent.EnforcementDisabled,
	}
	agentAuthModeRaw := os.Getenv("KUBEBOLT_AGENT_AUTH_MODE")
	if settingsRuntime != nil {
		agentAuthModeRaw = settingsRuntime.IngestChannel().AgentAuthMode
	}
	if agentAuthModeRaw != "" {
		if parsed, ok := agent.ParseEnforcement(agentAuthModeRaw); ok {
			agentAuthCfg.Enforcement = parsed
		} else {
			slog.Warn("agent auth mode has unknown value, defaulting to disabled",
				slog.String("requested", agentAuthModeRaw),
			)
		}
	}
	// Plug the composite authenticator into the interceptor. Available
	// only when auth is enabled at the application level — without it,
	// there is no tenants store, so enforced/permissive cannot validate
	// anything. In that combination main.go below logs a warning and
	// the agent channel keeps running disabled.
	if agentAuthBundle != nil {
		agentAuthCfg.Authenticator = agentAuthBundle.Composite
	} else if agentAuthCfg.Enforcement != agent.EnforcementDisabled {
		slog.Warn("agent auth enforcement requested but app auth is disabled — falling back to disabled mode",
			slog.String("requested", string(agentAuthCfg.Enforcement)),
		)
		agentAuthCfg.Enforcement = agent.EnforcementDisabled
	}

	// Resolve the install's default tenant once at startup so the gRPC
	// interceptor's disabled / permissive-fallback paths stamp it on
	// AgentIdentity instead of leaving TenantID empty (which makes the
	// rate limiter bypass on the empty-key branch). Same purpose as the
	// HTTP path's resolveDefaultIngestTenant — keep all unauthenticated
	// flows bucketed against the operator's custom overrides on the
	// default tenant.
	//
	// Empty string here is intentional when tenantsStore is nil (auth
	// disabled at the app layer) — preserves the legacy bypass.
	if tenantsStore != nil {
		if dt, err := tenantsStore.GetDefaultTenant(); err == nil && dt != nil {
			agentAuthCfg.DefaultTenantID = dt.ID
		} else if err != nil {
			slog.Warn("could not resolve default tenant at startup for agent auth fallback",
				slog.String("error", err.Error()))
		}
	}

	// TLS (and optional mTLS) for the agent gRPC channel. Half-set env
	// surfaces as an error here so misconfigurations fail loud at boot.
	//
	// Spec #09 V2 — cert / key / clientCA file paths stay in env
	// (Bucket A — filesystem paths read at boot, not safely editable
	// from UI). RequireMTLS is the one knob in this section that the
	// UI can flip; we override the env-derived value with whatever the
	// settings runtime resolves (BoltDB wins over env). The CA bundle
	// must still be present in the filesystem for mTLS to make sense;
	// if RequireMTLS=true and no clientCA is loaded, LoadServerTLSFromEnv
	// already fails loud earlier.
	agentTLS, err := agent.LoadServerTLSFromEnv()
	if err != nil {
		fatal("agent TLS configuration invalid", slog.String("error", err.Error()))
	}
	if settingsRuntime != nil && agentTLS != nil {
		agentTLS.RequireMTLS = settingsRuntime.IngestChannel().AgentRequireMTLS
	}
	if agentTLS != nil && agentTLS.RequireMTLS {
		// Mirror to the auth interceptor so identity.TLSVerified is
		// re-checked post-auth, even though tls.RequireAndVerifyClientCert
		// already gates the handshake.
		agentAuthCfg.RequireMTLS = true
	}

	// Per-tenant rate limiter (ENTERPRISE-CANDIDATE). Off by default;
	// operator opts in via KUBEBOLT_AGENT_RATE_LIMIT_ENABLED=true. With
	// the OSS edition every tenant gets the same global config; the
	// SaaS edition will swap in plan-aware lookups.
	rateLimitCfg := auth.LoadRateLimitConfigFromEnv()
	if rateLimitCfg.Enabled {
		agentAuthCfg.RateLimiter = auth.NewRateLimiter(rateLimitCfg)
		slog.Info("agent ingest rate limit enabled",
			slog.Float64("requests_per_sec", rateLimitCfg.RequestsPerSec),
			slog.Float64("burst", rateLimitCfg.Burst),
		)
	}

	// Spec #09 V2 — capture the IngestChannel boot snapshot now that the
	// gRPC interceptor + TLS are wired with their resolved values.
	// Subsequent PUTs to /admin/settings/ingest-channel compare against
	// this baseline to compute pendingRestart for the three
	// restart-required fields (AgentAuthMode, AgentTokenAudience,
	// AgentRequireMTLS).
	if settingsRuntime != nil {
		settingsRuntime.CaptureIngestChannelBootSnapshot()
	}

	go func() {
		if err := agent.Listen(agentCtx, agentAddr, ingestSrv, agent.ListenOptions{
			Auth: agentAuthCfg,
			TLS:  agentTLS,
		}); err != nil {
			slog.Error("agent gRPC server error", slog.String("error", err.Error()))
		}
	}()

	// Flow data (pod_flow_events_total, etc.) arrives from the agent as
	// regular samples via StreamMetrics — no backend-side collector
	// lives here anymore. The agent is a better bridge to cluster-
	// internal sources like Hubble Relay because it works unchanged in
	// SaaS deployments where the backend is outside the customer's
	// cluster. See packages/agent/internal/flows/.

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")

	agentCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", slog.String("error", err.Error()))
	}
	slog.Info("kubebolt stopped")
}

// parseAutoRegisterFlag interprets KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS.
// Empty string defaults to false. Accepts the same ergonomic spellings
// the agent's bool env vars use (1/0, true/false, yes/no, on/off,
// case-insensitive).
func parseAutoRegisterFlag(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	}
	return false
}

// persistAdminPasswordSecret writes the auto-generated admin password to
// a Kubernetes Secret in the backend's own namespace, on first boot only.
// Only meaningful in-cluster — outside (desktop binary, docker-compose),
// rest.InClusterConfig() fails and we silently skip.
//
// The Secret name is hardcoded "kubebolt-admin-password". We never
// overwrite an existing one — if it's already there, the operator
// already managed the password explicitly via auth.existingSecret on
// the chart and we shouldn't fight that.
func persistAdminPasswordSecret(password string) error {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Not in-cluster — nothing to persist to.
		return nil
	}
	nsBytes, _ := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	ns := strings.TrimSpace(string(nsBytes))
	if ns == "" {
		return fmt.Errorf("could not determine self namespace")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const secretName = "kubebolt-admin-password"
	_, err = client.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		// Already exists — operator manages it themselves, leave alone.
		slog.Info("admin password Secret already exists, leaving untouched",
			slog.String("secret", secretName),
			slog.String("namespace", ns),
		)
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check existing Secret: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kubebolt",
				"app.kubernetes.io/component":  "auth",
				"app.kubernetes.io/managed-by": "kubebolt-api",
			},
			Annotations: map[string]string{
				"kubebolt.io/generated-at": time.Now().UTC().Format(time.RFC3339),
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"password": password,
			"username": "admin",
		},
	}
	if _, err := client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create Secret %s/%s: %w", ns, secretName, err)
	}
	slog.Info("admin password persisted to Kubernetes Secret",
		slog.String("secret", secretName),
		slog.String("namespace", ns),
		slog.String("retrieve_with", fmt.Sprintf("kubectl -n %s get secret %s -o jsonpath='{.data.password}' | base64 -d", ns, secretName)),
	)
	return nil
}

// runResetAdminPassword opens the auth BoltDB at the configured
// DataDir, looks up the admin user, and replaces the password hash
// with bcrypt(newPassword). Exits non-zero on any failure (db locked
// by a running api, admin user missing, etc.). Server intentionally
// not started — this is a one-shot maintenance command meant to run
// via `kubectl exec` against a temporarily-stopped pod (or a Job).
func runResetAdminPassword(newPassword string) error {
	if len(newPassword) < 8 {
		return fmt.Errorf("password too short (min 8 chars)")
	}
	authCfg := config.LoadAuthConfig()
	if authCfg.DataDir == "" {
		authCfg.DataDir = "./data"
	}
	store, err := auth.NewStore(authCfg.DataDir)
	if err != nil {
		return fmt.Errorf("open auth store: %w (is another kubebolt-api process holding the DB? scale the deployment to 0 or run this from a Job)", err)
	}
	defer store.Close()
	user, err := store.GetUserByUsername("admin")
	if err != nil {
		return fmt.Errorf("admin user not found: %w (was the database ever seeded?)", err)
	}
	if err := store.UpdatePassword(user.ID, newPassword); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// openURL opens the given URL in the default browser.
func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Start()
}
