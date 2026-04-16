package main

import (
	"context"
	crypto_rand "crypto/rand"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/api"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
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

func main() {
	cfg := config.DefaultConfig()

	var host string
	var showVersion bool
	var openBrowser bool

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
			log.Println("Embedded frontend detected — serving UI and API on single port")
		}
	}

	log.Printf("KubeBolt %s starting...", version)
	log.Printf("  Kubeconfig: %s", cfg.Kubeconfig)
	log.Printf("  Listen:     %s:%d", host, cfg.Port)
	if frontendFS != nil {
		log.Printf("  Mode:       single binary (embedded frontend)")
	} else {
		log.Printf("  Mode:       API-only (frontend served separately)")
	}

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
		log.Fatalf("Failed to create cluster manager: %v", err)
	}
	defer manager.Stop()

	// Load copilot configuration from KUBEBOLT_AI_* env vars
	copilotCfg := config.LoadCopilotConfig()
	if copilotCfg.Enabled {
		log.Printf("AI Copilot enabled: provider=%s model=%s", copilotCfg.Primary.Provider, copilotCfg.Primary.Model)
		if copilotCfg.Fallback != nil {
			log.Printf("  Fallback: provider=%s model=%s", copilotCfg.Fallback.Provider, copilotCfg.Fallback.Model)
		}
	} else {
		log.Println("AI Copilot disabled (KUBEBOLT_AI_API_KEY not set)")
	}

	// Load auth configuration from KUBEBOLT_AUTH_* env vars
	authCfg := config.LoadAuthConfig()

	var authHandlers *auth.Handlers
	if authCfg.Enabled {
		log.Println("Authentication enabled")

		store, err := auth.NewStore(authCfg.DataDir)
		if err != nil {
			log.Fatalf("Failed to open auth store: %v", err)
		}
		defer store.Close()

		// Resolve JWT secret: env var > persisted in DB > generate and persist
		if !authCfg.JWTSecretFromEnv {
			if secret, err := store.GetSetting("jwt_secret"); err == nil {
				authCfg.JWTSecret = secret
				log.Println("JWT secret loaded from database")
			} else {
				secret := make([]byte, 32)
				if _, err := crypto_rand.Read(secret); err != nil {
					log.Fatalf("Failed to generate JWT secret: %v", err)
				}
				if err := store.SetSetting("jwt_secret", secret); err != nil {
					log.Fatalf("Failed to persist JWT secret: %v", err)
				}
				authCfg.JWTSecret = secret
				log.Println("JWT secret generated and persisted to database")
			}
		}

		seeded, err := store.SeedAdmin(authCfg.InitialAdminPassword)
		if err != nil {
			log.Fatalf("Failed to seed admin user: %v", err)
		}
		if seeded {
			log.Println("Default admin user created (username: admin)")
		}

		jwtSvc := auth.NewJWTService(authCfg)
		authHandlers = auth.NewHandlers(store, jwtSvc, authCfg)
	} else {
		log.Println("Authentication disabled (KUBEBOLT_AUTH_ENABLED=false)")
		authHandlers = auth.NewNoOpHandlers()
	}

	// Create API Router (with optional embedded frontend)
	router := api.NewRouter(manager, wsHub, cfg.CORSOrigins, copilotCfg, authHandlers)

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
			log.Printf("KubeBolt ready at %s", url)
		} else {
			log.Printf("KubeBolt API running at %s", url)
		}

		if openBrowser {
			openURL(url)
		}

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down KubeBolt...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	log.Println("KubeBolt stopped")
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
