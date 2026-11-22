package main

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// defaultShutdownTimeout fits the pod's default 30 s grace period with room for
// what runs after the HTTP drain: the usage store's last flush and the MCP
// gateway's own 5 s.
const defaultShutdownTimeout = 20 * time.Second

// shutdownTimeout is how long the HTTP server gets to drain on SIGTERM
// (KUBEBOLT_SHUTDOWN_TIMEOUT, a Go duration). Raise it together with the pod's
// terminationGracePeriodSeconds, never past it: the kubelet's SIGKILL at the
// end of the grace period skips the usage flush that follows the drain.
func shutdownTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("KUBEBOLT_SHUTDOWN_TIMEOUT"))
	if raw == "" {
		return defaultShutdownTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("KUBEBOLT_SHUTDOWN_TIMEOUT is not a positive duration; using the default",
			slog.String("value", raw), slog.Duration("default", defaultShutdownTimeout))
		return defaultShutdownTimeout
	}
	return d
}
