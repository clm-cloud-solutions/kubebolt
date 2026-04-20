// Package notifications sends insight alerts to external channels (Slack, Discord).
//
// Design:
//   - Notifier is the interface every channel implements
//   - Manager coordinates dispatch, severity filtering, and deduplication
//   - Delivery is asynchronous — never blocks the insights engine
//   - Webhooks are configured via env vars; Manager is created at startup
package notifications

import (
	"context"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Event is everything a notifier needs to format a message.
// Wraps an Insight plus contextual information about where it came from.
type Event struct {
	Insight     models.Insight
	ClusterName string // human-readable cluster name (display name or context)
	BaseURL     string // optional link back to KubeBolt (e.g. https://kubebolt.example.com)
}

// Notifier is the interface every external channel implements.
// Implementations MUST be safe for concurrent use.
type Notifier interface {
	// Name returns a short identifier for logging (e.g. "slack", "discord").
	Name() string

	// Send delivers the event to the external channel. Returns an error on
	// transport or protocol failures. Implementations SHOULD respect the
	// provided context for cancellation.
	Send(ctx context.Context, e Event) error
}

// severityLevel maps severity strings to numeric levels for threshold comparison.
// Higher number = more severe.
func severityLevel(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}
