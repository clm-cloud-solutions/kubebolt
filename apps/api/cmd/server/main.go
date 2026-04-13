package main

import (
	"context"
	crypto_rand "crypto/rand"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/api"
	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

func main() {
	cfg := config.DefaultConfig()

	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	flag.IntVar(&cfg.Port, "port", cfg.Port, "API server port")
	flag.IntVar(&cfg.MetricInterval, "metric-interval", cfg.MetricInterval, "Metrics polling interval in seconds")
	flag.IntVar(&cfg.InsightInterval, "insight-interval", cfg.InsightInterval, "Insight evaluation interval in seconds")
	flag.Parse()

	if cfg.Kubeconfig == "" {
		if env := os.Getenv("KUBECONFIG"); env != "" {
			cfg.Kubeconfig = env
		} else {
			home, _ := os.UserHomeDir()
			cfg.Kubeconfig = home + "/.kube/config"
		}
	}

	log.Printf("KubeBolt starting...")
	log.Printf("  Kubeconfig: %s", cfg.Kubeconfig)
	log.Printf("  API Port:   %d", cfg.Port)

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

	// Create API Router
	router := api.NewRouter(manager, wsHub, cfg.CORSOrigins, copilotCfg, authHandlers)

	// Start HTTP server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	go func() {
		log.Printf("KubeBolt API running on http://localhost:%d", cfg.Port)
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
