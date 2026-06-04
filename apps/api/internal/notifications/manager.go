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
//
// Hot reload: the notifier slice and the 5 global knobs (masterEnabled,
// minSeverity, cooldown, baseURL, includeResolved) are all swappable at
// runtime through SetNotifiers/SetConfig — used by the admin Settings →
// Notifications PUT handler to make a UI edit take effect without restart.
// All reads in the dispatch path take a snapshot under m.mu so a swap in
// flight can't tear a single Enqueue call across two configurations.
type Manager struct {
	mu              sync.Mutex
	notifiers       []Notifier
	masterEnabled   bool          // global kill switch; when false, Enqueue is a no-op
	minSeverity     int           // numeric threshold; events below this level are dropped
	cooldown        time.Duration // minimum time between sending the same insight
	baseURL         string        // optional base URL to include in notifications
	includeResolved bool          // also notify when an insight transitions to resolved
	lastSent        map[string]time.Time // dedup key → last sent timestamp
}

// Config holds runtime settings for the manager.
type Config struct {
	MasterEnabled   bool
	MinSeverity     string
	Cooldown        time.Duration
	BaseURL         string
	IncludeResolved bool
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
		notifiers:       notifiers,
		masterEnabled:   cfg.MasterEnabled,
		minSeverity:     min,
		cooldown:        cooldown,
		baseURL:         cfg.BaseURL,
		includeResolved: cfg.IncludeResolved,
		lastSent:        make(map[string]time.Time),
	}
}

// Enabled returns true if at least one notifier is configured AND the
// master toggle is on.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notifiers) > 0 && m.masterEnabled
}

// MasterEnabled returns the state of the global kill switch (independent
// of whether any channels are configured).
func (m *Manager) MasterEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.masterEnabled
}

// BaseURL returns the configured base URL for links in messages.
func (m *Manager) BaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseURL
}

// IncludeResolved returns true when resolved-insight notifications are enabled.
func (m *Manager) IncludeResolved() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.includeResolved
}

// Notifiers returns a snapshot of the registered notifiers (used by
// handlers for /test). The slice is a copy so callers can range over it
// without holding the manager lock.
func (m *Manager) Notifiers() []Notifier {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Notifier, len(m.notifiers))
	copy(out, m.notifiers)
	return out
}

// MinSeverity returns the configured minimum severity as a string.
func (m *Manager) MinSeverity() string {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cooldown
}

// SetConfig swaps the 5 global knobs atomically. Cooldown=0 falls back
// to 1h, invalid MinSeverity falls back to "warning" — same defaulting
// the constructor applies. Existing notifiers stay in place.
//
// Hot reload entry point: the admin Settings UI calls this after
// persisting a notifications-settings patch so the next Enqueue picks
// up the new thresholds without a process restart.
func (m *Manager) SetConfig(cfg Config) {
	min := severityLevel(cfg.MinSeverity)
	if min == 0 {
		min = severityLevel("warning")
	}
	cooldown := cfg.Cooldown
	if cooldown == 0 {
		cooldown = time.Hour
	}
	m.mu.Lock()
	m.masterEnabled = cfg.MasterEnabled
	m.minSeverity = min
	m.cooldown = cooldown
	m.baseURL = cfg.BaseURL
	m.includeResolved = cfg.IncludeResolved
	m.mu.Unlock()
}

// SetNotifiers replaces the registered notifier set. Any prior notifier
// implementing Stop() is stopped after the swap so swapping doesn't
// leak goroutines (notably the email digest flusher). Safe to call
// repeatedly; passing an empty slice disables all dispatch.
//
// Stop happens OUTSIDE the lock so a slow Stop on one notifier can't
// block other manager operations during the swap.
func (m *Manager) SetNotifiers(next []Notifier) {
	m.mu.Lock()
	old := m.notifiers
	m.notifiers = next
	m.mu.Unlock()
	for _, n := range old {
		if s, ok := n.(interface{ Stop() }); ok {
			s.Stop()
		}
	}
}

// Enqueue filters, dedupes, and dispatches an insight asynchronously.
// Safe to call from the insights engine on every new detection.
// Returns immediately — actual HTTP delivery happens in a goroutine.
//
// All config reads happen inside a single critical section so a
// concurrent SetConfig/SetNotifiers can't tear this call between two
// configurations.
// insightDedupKey identifies an insight for notification dedup. Prefers the
// stable Fingerprint (Sprint 0 — survives restarts and rule rewording);
// falls back to Resource|Title for insights emitted without a fingerprint
// (installs running with no persistent insights store).
func insightDedupKey(clusterName string, insight models.Insight) string {
	if insight.Fingerprint != "" {
		return clusterName + "|" + insight.Fingerprint
	}
	return clusterName + "|" + insight.Resource + "|" + insight.Title
}

func (m *Manager) Enqueue(clusterName string, insight models.Insight) {
	key := insightDedupKey(clusterName, insight)

	m.mu.Lock()
	if len(m.notifiers) == 0 || !m.masterEnabled {
		m.mu.Unlock()
		return
	}
	if severityLevel(insight.Severity) < m.minSeverity {
		m.mu.Unlock()
		return
	}
	if last, ok := m.lastSent[key]; ok && time.Since(last) < m.cooldown {
		m.mu.Unlock()
		return
	}
	m.lastSent[key] = time.Now()
	// Opportunistic cleanup: if the map grows beyond 10k entries, purge stale ones
	if len(m.lastSent) > 10000 {
		m.purgeExpiredLocked()
	}
	// Snapshot what we need for dispatch.
	baseURL := m.baseURL
	notifiers := make([]Notifier, len(m.notifiers))
	copy(notifiers, m.notifiers)
	m.mu.Unlock()

	event := Event{
		Insight:     insight,
		ClusterName: clusterName,
		BaseURL:     baseURL,
	}

	// Fire-and-forget dispatch per notifier. Each notifier has its own 10s HTTP
	// timeout so one slow channel can't block others.
	for _, n := range notifiers {
		go func(n Notifier) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := n.Send(ctx, event); err != nil {
				log.Printf("Notification via %s failed: %v", n.Name(), err)
			}
		}(n)
	}
}

// EnqueueResolved dispatches a notification when an insight transitions to
// resolved. No-op unless both the master toggle and includeResolved are on.
// Uses a different dedup key than Enqueue so a resolution right after a new
// notification is not deduplicated.
func (m *Manager) EnqueueResolved(clusterName string, insight models.Insight) {
	key := "resolved|" + insightDedupKey(clusterName, insight)

	m.mu.Lock()
	if len(m.notifiers) == 0 || !m.masterEnabled || !m.includeResolved {
		m.mu.Unlock()
		return
	}
	if severityLevel(insight.Severity) < m.minSeverity {
		m.mu.Unlock()
		return
	}
	if last, ok := m.lastSent[key]; ok && time.Since(last) < m.cooldown {
		m.mu.Unlock()
		return
	}
	m.lastSent[key] = time.Now()
	baseURL := m.baseURL
	notifiers := make([]Notifier, len(m.notifiers))
	copy(notifiers, m.notifiers)
	m.mu.Unlock()

	// Prefix the title so existing notifiers visually distinguish the event
	// without needing a template refactor. Preserves all other fields.
	annotated := insight
	annotated.Title = "[Resolved] " + insight.Title
	annotated.Resolved = true

	event := Event{
		Insight:     annotated,
		ClusterName: clusterName,
		BaseURL:     baseURL,
	}

	for _, n := range notifiers {
		go func(n Notifier) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := n.Send(ctx, event); err != nil {
				log.Printf("Resolved notification via %s failed: %v", n.Name(), err)
			}
		}(n)
	}
}

// SendTest sends a synthetic test event to one specific notifier, bypassing
// severity/dedup filters. Used by the /notifications/test endpoint.
func (m *Manager) SendTest(ctx context.Context, channelName string) error {
	m.mu.Lock()
	baseURL := m.baseURL
	var target Notifier
	for _, n := range m.notifiers {
		if n.Name() == channelName {
			target = n
			break
		}
	}
	m.mu.Unlock()
	if target == nil {
		return errNoSuchChannel
	}
	event := Event{
		Insight: models.Insight{
			ID:        "test",
			Severity:  "info",
			Category:  "Test",
			Resource:  "kubebolt/test",
			Namespace: "default",
			Title:     "Test notification from KubeBolt",
			Message:   "If you can see this message, notifications via " + target.Name() + " are working correctly.",
			FirstSeen: time.Now(),
			LastSeen:  time.Now(),
		},
		ClusterName: "kubebolt-test",
		BaseURL:     baseURL,
	}
	return target.Send(ctx, event)
}

// Stop shuts down any notifiers that need cleanup (e.g. email digest flusher).
// Safe to call at any time; idempotent.
func (m *Manager) Stop() {
	m.mu.Lock()
	notifiers := make([]Notifier, len(m.notifiers))
	copy(notifiers, m.notifiers)
	m.mu.Unlock()
	for _, n := range notifiers {
		if s, ok := n.(interface{ Stop() }); ok {
			s.Stop()
		}
	}
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
