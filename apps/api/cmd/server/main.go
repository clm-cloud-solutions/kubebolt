package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/api"
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

	// Create API Router
	router := api.NewRouter(manager, wsHub, cfg.CORSOrigins)

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
