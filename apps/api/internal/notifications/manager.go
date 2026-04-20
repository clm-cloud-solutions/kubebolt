package notifications

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// Manager coordinates dispatch of insight alerts to registered notifiers.
// It handles severity filtering, deduplication (same insight within a cooldown
// window is only sent once), and async delivery so the caller is never blocked.
type Manager struct {
	notifiers   []Notifier
	minSeverity int           // numeric threshold; events below this level are dropped
	cooldown    time.Duration // minimum time between sending the same insight
	baseURL     string        // optional base URL to include in notifications

	mu       sync.Mutex
	lastSent map[string]time.Time // dedup key → last sent timestamp
}

// Config holds runtime settings for the manager.
type Config struct {
	MinSeverity string
	Cooldown    time.Duration
	BaseURL     string
}

// NewManager creates a notification manager with the given notifiers and config.
// If notifiers is empty, Enqueue is a no-op. minSeverity defaults to "warning"
// if empty or invalid. Cooldown defaults to 1h if zero.
func NewManager(notifiers []Notifier, cfg Config) *Manager {
	min := severityLevel(cfg.MinSeverity)
	if min == 0 {
		min = severityLevel("warning")
	}
	cooldown := cfg.Cooldown
	if cooldown == 0 {
		cooldown = time.Hour
	}
	return &Manager{
		notifiers:   notifiers,
		minSeverity: min,
		cooldown:    cooldown,
		baseURL:     cfg.BaseURL,
		lastSent:    make(map[string]time.Time),
	}
}

// Enabled returns true if at least one notifier is configured.
func (m *Manager) Enabled() bool {
	return len(m.notifiers) > 0
}

// Notifiers returns the registered notifiers (used by handlers for /test).
func (m *Manager) Notifiers() []Notifier {
	return m.notifiers
}

// MinSeverity returns the configured minimum severity as a string.
func (m *Manager) MinSeverity() string {
	switch m.minSeverity {
	case 3:
		return "critical"
	case 2:
		return "warning"
	case 1:
		return "info"
	}
	return "warning"
}

// Cooldown returns the configured dedup window.
func (m *Manager) Cooldown() time.Duration {
	return m.cooldown
}

// Enqueue filters, dedupes, and dispatches an insight asynchronously.
// Safe to call from the insights engine on every new detection.
// Returns immediately — actual HTTP delivery happens in a goroutine.
func (m *Manager) Enqueue(clusterName string, insight models.Insight) {
	if len(m.notifiers) == 0 {
		return
	}

	// Severity filter
	if severityLevel(insight.Severity) < m.minSeverity {
		return
	}

	// Dedup: same (cluster, resource, title) within cooldown = skip
	key := clusterName + "|" + insight.Resource + "|" + insight.Title
	m.mu.Lock()
	if last, ok := m.lastSent[key]; ok && time.Since(last) < m.cooldown {
		m.mu.Unlock()
		return
	}
	m.lastSent[key] = time.Now()
	// Opportunistic cleanup: if the map grows beyond 10k entries, purge stale ones
	if len(m.lastSent) > 10000 {
		m.purgeExpiredLocked()
	}
	m.mu.Unlock()

	event := Event{
		Insight:     insight,
		ClusterName: clusterName,
		BaseURL:     m.baseURL,
	}

	// Fire-and-forget dispatch per notifier. Each notifier has its own 10s HTTP
	// timeout so one slow channel can't block others.
	for _, n := range m.notifiers {
		go func(n Notifier) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := n.Send(ctx, event); err != nil {
				log.Printf("Notification via %s failed: %v", n.Name(), err)
			}
		}(n)
	}
}

// SendTest sends a synthetic test event to one specific notifier, bypassing
// severity/dedup filters. Used by the /notifications/test endpoint.
func (m *Manager) SendTest(ctx context.Context, channelName string) error {
	for _, n := range m.notifiers {
		if n.Name() == channelName {
			event := Event{
				Insight: models.Insight{
					ID:        "test",
					Severity:  "info",
					Category:  "Test",
					Resource:  "kubebolt/test",
					Namespace: "default",
					Title:     "Test notification from KubeBolt",
					Message:   "If you can see this message, notifications via " + n.Name() + " are working correctly.",
					FirstSeen: time.Now(),
					LastSeen:  time.Now(),
				},
				ClusterName: "kubebolt-test",
				BaseURL:     m.baseURL,
			}
			return n.Send(ctx, event)
		}
	}
	return errNoSuchChannel
}

// purgeExpiredLocked removes dedup entries whose cooldown has expired.
// Must be called with m.mu held.
func (m *Manager) purgeExpiredLocked() {
	threshold := time.Now().Add(-m.cooldown)
	for k, t := range m.lastSent {
		if t.Before(threshold) {
			delete(m.lastSent, k)
		}
	}
}
