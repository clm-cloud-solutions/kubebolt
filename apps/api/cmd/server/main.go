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
	"syscall"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/api"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
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
	flag.Parse()

	if showVersion {
		fmt.Printf("KubeBolt %s\n", version)
		os.Exit(0)
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
	var copilotUsage *copilot.UsageStore
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
		if seeded {
			slog.Info("default admin user created", slog.String("username", "admin"))
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

	// Create API Router (with optional embedded frontend)
	router := api.NewRouter(manager, wsHub, cfg.CORSOrigins, copilotCfg, copilotUsage, authHandlers, notifManager)

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

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", slog.String("error", err.Error()))
	}
	slog.Info("kubebolt stopped")
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
