// Command kubebolt-mcp runs KubeBolt's Kobi capabilities as a read-only MCP
// server over stdio, for local / single-operator use (Claude Code, Cursor, or
// a CI step running on the same machine as the kubeconfig).
//
// It is deliberately minimal: it builds only the cluster manager + the
// read-only tool executor — no auth, no HTTP, no agent registry — because a
// local stdio MCP server talks to one cluster as the operator who launched it.
// For remote / multi-tenant use, the same capabilities are exposed by the main
// `kubebolt` server at POST /api/v1/mcp (authenticated with an API token).
//
// Protocol stream is on stdin/stdout; all logging goes to stderr so it never
// corrupts the JSON-RPC stream.
//
// Example (Claude Code mcp config):
//
//	{
//	  "command": "kubebolt-mcp",
//	  "args": ["--kubeconfig", "/home/me/.kube/config"]
//	}
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/config"
	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/logging"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcp"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// version is set at build time via -ldflags, matching the main server.
var version = "dev"

func main() {
	cfg := config.DefaultConfig()

	var showVersion bool
	var connectWaitSeconds int
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	flag.IntVar(&cfg.MetricInterval, "metric-interval", cfg.MetricInterval, "Metrics polling interval in seconds")
	flag.IntVar(&cfg.InsightInterval, "insight-interval", cfg.InsightInterval, "Insight evaluation interval in seconds")
	flag.IntVar(&connectWaitSeconds, "connect-wait", 10, "Seconds to wait for the initial cluster connection before serving (0 = don't wait)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("kubebolt-mcp %s\n", version)
		os.Exit(0)
	}

	// Load .env (if present) before reading config, mirroring the main server.
	config.LoadDotEnv(".env")
	// Logging to stderr (logging.Setup defaults to os.Stderr) — must NOT go to
	// stdout, which carries the MCP protocol stream.
	logging.Setup(logging.LoadOptionsFromEnv())

	if cfg.Kubeconfig == "" {
		if env := os.Getenv("KUBECONFIG"); env != "" {
			cfg.Kubeconfig = env
		} else if home, err := os.UserHomeDir(); err == nil {
			cfg.Kubeconfig = home + "/.kube/config"
		}
	}

	slog.Info("kubebolt-mcp starting (read-only stdio MCP server)",
		slog.String("version", version),
		slog.String("kubeconfig", cfg.Kubeconfig),
	)

	// WebSocket hub is a manager dependency (it broadcasts resource changes);
	// for the MCP server nothing consumes its events, but the manager needs a
	// non-nil hub.
	wsHub := websocket.NewHub()
	go wsHub.Run()

	manager, err := cluster.NewManager(
		cfg.Kubeconfig,
		wsHub,
		time.Duration(cfg.MetricInterval)*time.Second,
		time.Duration(cfg.InsightInterval)*time.Second,
	)
	if err != nil {
		slog.Error("failed to create cluster manager", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer manager.Stop()

	// Cancel on SIGINT/SIGTERM so a host that signals us shuts down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Best-effort wait for the initial connection so the first tools/call has
	// a live connector instead of returning "cluster not connected". The
	// manager connects asynchronously; we poll until ready or the budget runs
	// out. Tools still work after this — they just report not-connected until
	// the connector comes up.
	if connectWaitSeconds > 0 {
		waitForConnector(ctx, manager, time.Duration(connectWaitSeconds)*time.Second)
	}

	srv := mcp.NewServer(
		mcp.ServerInfo{Name: "kubebolt-kobi", Version: version},
		mcp.NewExecutorToolProvider(copilot.NewExecutor(manager)),
		mcp.NewKobiPromptProvider(),
	)

	slog.Info("kubebolt-mcp ready — serving MCP over stdio")
	if err := mcp.ServeStdio(ctx, srv, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		slog.Error("stdio server stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// waitForConnector polls until the active connector is non-nil or the deadline
// passes. Uses an empty context for resolution so it targets the default
// tenant + active cluster (the only runtime in this single-operator binary).
func waitForConnector(ctx context.Context, manager *cluster.Manager, budget time.Duration) {
	deadline := time.Now().Add(budget)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if manager.Connector(context.Background()) != nil {
			slog.Info("cluster connector ready")
			return
		}
		if time.Now().After(deadline) {
			if err := manager.ConnError(); err != nil {
				slog.Warn("serving without an initial cluster connection",
					slog.String("error", err.Error()))
			} else {
				slog.Warn("serving without an initial cluster connection (still connecting)")
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
