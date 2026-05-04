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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent"
	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
	"github.com/kubebolt/kubebolt/apps/api/internal/api"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/integrations"
	"github.com/kubebolt/kubebolt/apps/api/internal/logging"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/notifications"
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

		jwtSvc := auth.NewJWTService(authCfg)
		authHandlers = auth.NewHandlers(store, jwtSvc, authCfg)

		// Attach cluster storage (uses the same BoltDB for persistence of
		// user-uploaded kubeconfigs and display name overrides).
		configsBucket, displayBucket := auth.ClusterBuckets()
		clusterStorage := cluster.NewStorage(store.DB(), configsBucket, displayBucket)
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

		// Persistent agent registry. Restart-survival for the
		// agent-proxy cluster list — operators expect their dashboard
		// to keep showing connected clusters across `helm upgrade` of
		// the backend, not to "blank" until each agent reconnects.
		// Bucket already created in auth.NewStore; we just wire the
		// store + registry binding here.
		agentStore = channel.NewBoltAgentStore(store.DB(), auth.AgentsBucket())

		// Build the agent authenticator. TokenReview mode is best-effort:
		// if there is no in-cluster client (KubeBolt running outside K8s,
		// e.g. via `go run`) the bundle still includes BearerIngestAuth
		// for SaaS / cross-cluster ingest tokens.
		factoryOpts := agent.LoadAuthenticatorOptionsFromEnv()
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
		tenantHandlers = auth.NewTenantHandlers(tenantsStore, bundle.AsCacheInvalidators()...)
	} else {
		slog.Info("authentication disabled (KUBEBOLT_AUTH_ENABLED=false)")
		authHandlers = auth.NewNoOpHandlers()
	}

	// Load notifications config and wire up Slack/Discord notifiers if webhooks are set
	notifCfg := config.LoadNotificationsConfig()
	var notifManager *notifications.Manager
	{
		var notifiers []notifications.Notifier
		if notifCfg.SlackWebhookURL != "" {
			notifiers = append(notifiers, notifications.NewSlackNotifier(notifCfg.SlackWebhookURL))
			slog.Info("slack notifications enabled")
		}
		if notifCfg.DiscordWebhookURL != "" {
			notifiers = append(notifiers, notifications.NewDiscordNotifier(notifCfg.DiscordWebhookURL))
			slog.Info("discord notifications enabled")
		}
		if notifCfg.Email.Enabled() {
			email := notifications.NewEmailNotifier(notifications.EmailConfig{
				Host:       notifCfg.Email.Host,
				Port:       notifCfg.Email.Port,
				Username:   notifCfg.Email.Username,
				Password:   notifCfg.Email.Password,
				From:       notifCfg.Email.From,
				To:         notifCfg.Email.To,
				DigestMode: notifications.DigestMode(notifCfg.Email.DigestMode),
			})
			notifiers = append(notifiers, email)
			slog.Info("email notifications enabled",
				slog.String("mode", notifCfg.Email.DigestMode),
				slog.Int("recipients", len(notifCfg.Email.To)),
			)
		}
		notifManager = notifications.NewManager(notifiers, notifications.Config{
			MasterEnabled:   notifCfg.MasterEnabled,
			MinSeverity:     notifCfg.MinSeverity,
			Cooldown:        notifCfg.Cooldown,
			BaseURL:         notifCfg.BaseURL,
			IncludeResolved: notifCfg.IncludeResolved,
		})
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
	integrationRegistry.Register(integrations.NewAgent())

	// Resolve the agent auth enforcement that the router will surface
	// to the UI for "refuse proxy + empty auth on enforced backend"
	// gating. This mirrors the logic applied to agentAuthCfg.Enforcement
	// further down: env says X, but if app auth is disabled (no
	// agentAuthBundle), the agent gRPC server forces "disabled" — the
	// UI must reflect the EFFECTIVE mode, not the requested one.
	resolvedEnforcement := string(agent.EnforcementDisabled)
	if v := os.Getenv("KUBEBOLT_AGENT_AUTH_MODE"); v != "" {
		if parsed, ok := agent.ParseEnforcement(v); ok {
			resolvedEnforcement = string(parsed)
		}
	}
	if agentAuthBundle == nil && resolvedEnforcement != string(agent.EnforcementDisabled) {
		resolvedEnforcement = string(agent.EnforcementDisabled)
	}

	// Create API Router (with optional embedded frontend)
	router := api.NewRouter(manager, wsHub, cfg.CORSOrigins, copilotCfg, copilotUsage, authHandlers, tenantHandlers, notifManager, integrationRegistry, resolvedEnforcement, tenantsStore)

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

	vmURL := os.Getenv("KUBEBOLT_METRICS_STORAGE_URL")
	if vmURL == "" {
		vmURL = "http://localhost:8428"
	}
	agentAddr := os.Getenv("KUBEBOLT_AGENT_GRPC_ADDR")
	if agentAddr == "" {
		agentAddr = fmt.Sprintf("%s:9090", host)
	}

	writer := agent.NewVMWriter(vmURL)
	// AgentRegistry indexes connected agents by (cluster_id, agent_id).
	// The AgentProxyTransport (Sprint A.5 commit 5) consumes it via the
	// cluster.Manager; admin handlers will too (commit 8). The manager
	// also gets a reference so AddAgentProxyCluster can later resolve
	// reachability via the live registry.
	agentRegistry := channel.NewAgentRegistry()
	manager.SetAgentRegistry(agentRegistry)

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
			for clusterID, displayName := range seen {
				if _, err := manager.AddAgentProxyCluster(clusterID, displayName); err != nil {
					slog.Warn("failed to restore agent-proxy cluster",
						slog.String("cluster_id", clusterID),
						slog.String("display_name", displayName),
						slog.String("error", err.Error()),
					)
				}
			}
			if len(seen) > 0 {
				slog.Info("restored agent-proxy clusters from persistent registry",
					slog.Int("count", len(seen)),
				)
			}
		}

		// Prune disconnected records older than the horizon. Records
		// for agents currently connected (DisconnectedAt zero) never
		// expire. The horizon is configurable so SaaS operators can
		// shorten it if they don't want stale orgs in their data set.
		pruneHorizon := 24 * time.Hour
		if v := os.Getenv("KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				pruneHorizon = d
			} else {
				slog.Warn("invalid KUBEBOLT_AGENT_REGISTRY_PRUNE_HORIZON, using default",
					slog.String("requested", v),
					slog.Duration("default", pruneHorizon),
				)
			}
		}
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-agentCtx.Done():
					return
				case <-ticker.C:
					removed, err := agentStore.Prune(time.Now().UTC().Add(-pruneHorizon))
					if err != nil {
						slog.Warn("agent registry prune failed", slog.String("error", err.Error()))
						continue
					}
					if removed > 0 {
						slog.Info("agent registry pruned", slog.Int("removed", removed))
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
	autoRegisterClusters := parseAutoRegisterFlag(os.Getenv("KUBEBOLT_AGENT_AUTOREGISTER_CLUSTERS"))
	if autoRegisterClusters {
		slog.Info("agent-proxy cluster auto-register enabled")
	}

	ingestSrv := agent.NewServer(writer,
		agent.WithRegistry(agentRegistry),
		agent.WithClusterRegistrar(manager),
		agent.WithAutoRegisterClusters(autoRegisterClusters),
	)

	// Sprint A migration window: enforcement defaults to "disabled" so
	// existing fleets without auth credentials keep working. Operators
	// flip KUBEBOLT_AGENT_AUTH_MODE=enforced to require credentials.
	agentAuthCfg := agent.AuthConfig{
		Enforcement: agent.EnforcementDisabled,
	}
	if v := os.Getenv("KUBEBOLT_AGENT_AUTH_MODE"); v != "" {
		if parsed, ok := agent.ParseEnforcement(v); ok {
			agentAuthCfg.Enforcement = parsed
		} else {
			slog.Warn("KUBEBOLT_AGENT_AUTH_MODE has unknown value, defaulting to disabled",
				slog.String("requested", v),
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

	// TLS (and optional mTLS) for the agent gRPC channel. Half-set env
	// surfaces as an error here so misconfigurations fail loud at boot.
	agentTLS, err := agent.LoadServerTLSFromEnv()
	if err != nil {
		fatal("agent TLS configuration invalid", slog.String("error", err.Error()))
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
				"app.kubernetes.io/name":      "kubebolt",
				"app.kubernetes.io/component": "auth",
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
