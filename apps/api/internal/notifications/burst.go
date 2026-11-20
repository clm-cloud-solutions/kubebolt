package notifications

import (
	"context"
	"log"
	"log/slog"
	"time"
)

// A burst is many insights from one cluster at once: a cluster's first
// evaluation, a cluster that comes back after the episode watchdog expired its
// episodes, a broken node taking dozens of workloads down. Sent one by one they
// bury the team (hundreds of emails in seconds) and, on a shared email
// provider, exhaust the sending limit for everyone else. The Manager counts the
// insights each cluster sends to a channel that delivers one message per
// insight; past BurstConfig.Threshold within Window it holds the rest and,
// once the cluster has been quiet for Quiet (or MaxWait at the latest), sends
// ONE summary that counts and classifies them. Below the threshold nothing
// changes: every insight still goes out the moment it is detected.
//
// Channels that already batch (the email digest) are not burst-tracked: they
// get every event as before, and the digest itself turns into a summary when it
// grows past its own list limit.

// Default burst thresholds. Settings › Notifications overrides them per org;
// the KUBEBOLT_NOTIFICATIONS_BURST_* env vars set the install default.
const (
	DefaultBurstThreshold = 5
	DefaultBurstWindow    = 60 * time.Second
	DefaultBurstQuiet     = 30 * time.Second
	DefaultBurstMaxWait   = 5 * time.Minute
)

// BurstConfig decides when a run of insights from one cluster becomes a burst.
// There is no "off": a threshold below 2 resolves to the default (burstFrom).
type BurstConfig struct {
	Threshold int           // insights from one cluster within Window that start a burst
	Window    time.Duration // how far back the count looks
	Quiet     time.Duration // how long the cluster must stay quiet before the summary is sent
	MaxWait   time.Duration // longest a held insight waits; a burst still going then gets a partial summary
}

// DefaultBurstConfig is what an install gets with nothing configured.
func DefaultBurstConfig() BurstConfig {
	return BurstConfig{Threshold: DefaultBurstThreshold, Window: DefaultBurstWindow, Quiet: DefaultBurstQuiet, MaxWait: DefaultBurstMaxWait}
}

// normalized fills unset durations with the defaults and keeps Quiet ≤ MaxWait,
// so a partial config never produces a summary that waits forever.
func (c BurstConfig) normalized() BurstConfig {
	if c.Window <= 0 {
		c.Window = DefaultBurstWindow
	}
	if c.Quiet <= 0 {
		c.Quiet = DefaultBurstQuiet
	}
	if c.MaxWait <= 0 {
		c.MaxWait = DefaultBurstMaxWait
	}
	if c.Quiet > c.MaxWait {
		c.Quiet = c.MaxWait
	}
	return c
}

// SummaryItem is one insight a summary covers.
type SummaryItem struct {
	Event
	// AlsoSentAlone: it went out on its own before the burst was detected (the
	// first Threshold-1 insights), so the summary marks it instead of hiding it.
	AlsoSentAlone bool
}

// Summary is one message that stands in for a burst of insights from one cluster.
type Summary struct {
	ClusterName string
	BaseURL     string
	Items       []SummaryItem // every insight of the burst, in arrival order
	First, Last time.Time     // when the first and the last of them arrived
	Resolved    bool          // a burst of resolutions, not of new insights
	// Partial: MaxWait elapsed while the cluster was still sending; the next
	// summary continues where this one stops.
	Partial bool
	Config  BurstConfig // the thresholds that made it a burst, for the "why" line
}

// SummaryNotifier is implemented by every channel that can render a Summary.
// One that cannot gets the held insights one by one, as before bursts existed.
type SummaryNotifier interface {
	SendSummary(ctx context.Context, s Summary) error
}

// buffersEvents reports whether the notifier batches events itself (the email
// digest). Those get every event as it comes and are left out of bursts.
func buffersEvents(n Notifier) bool {
	d, ok := n.(interface{ DigestMode() string })
	if !ok {
		return false
	}
	mode := d.DigestMode()
	return mode != "" && mode != string(DigestInstant)
}

func hasPerInsightChannel(notifiers []Notifier) bool {
	for _, n := range notifiers {
		if !buffersEvents(n) {
			return true
		}
	}
	return false
}

func perInsightChannels(notifiers []Notifier) []Notifier {
	var out []Notifier
	for _, n := range notifiers {
		if !buffersEvents(n) {
			out = append(out, n)
		}
	}
	return out
}

type burstKey struct {
	cluster  string
	resolved bool
}

type sentAlone struct {
	at time.Time
	ev Event
}

// burstRun is one cluster's state. Before a burst it remembers the insights it
// sent on their own inside the window (at most Threshold-1); during one it
// collects everything the summary will cover.
type burstRun struct {
	recent  []sentAlone
	active  bool
	items   []SummaryItem
	first   time.Time
	last    time.Time
	started time.Time // start of the current summary, for MaxWait
	timer   stopper
	gen     uint64 // a timer that fires after its run moved on is ignored
}

type stopper interface{ Stop() bool }

// holdForBurstLocked records an event bound for the per-insight channels and
// reports whether it must be held for a summary instead of sent now. Must be
// called with m.mu held.
func (m *Manager) holdForBurstLocked(cluster string, resolved bool, e Event, notifiers []Notifier) bool {
	cfg := m.burst
	if !hasPerInsightChannel(notifiers) {
		return false
	}
	now := m.now()
	key := burstKey{cluster: cluster, resolved: resolved}
	r := m.bursts[key]
	if r == nil {
		r = &burstRun{}
		m.bursts[key] = r
	}
	if r.active {
		if len(r.items) == 0 {
			r.first = now // first insight after a partial summary
		}
		r.items = append(r.items, SummaryItem{Event: e})
		r.last = now
		m.armBurstLocked(key, r, cfg, now)
		return true
	}

	// Not in a burst: forget what fell out of the window, then count.
	keep := r.recent[:0]
	for _, s := range r.recent {
		if now.Sub(s.at) < cfg.Window {
			keep = append(keep, s)
		}
	}
	r.recent = keep
	if len(r.recent)+1 < cfg.Threshold {
		r.recent = append(r.recent, sentAlone{at: now, ev: e})
		return false
	}

	// This one makes the burst. The summary covers the ones already sent too,
	// so it tells the whole story.
	r.active = true
	r.items = make([]SummaryItem, 0, len(r.recent)+1)
	for _, s := range r.recent {
		r.items = append(r.items, SummaryItem{Event: s.ev, AlsoSentAlone: true})
	}
	r.items = append(r.items, SummaryItem{Event: e})
	r.first = now
	if len(r.recent) > 0 {
		r.first = r.recent[0].at
	}
	r.last = now
	r.started = now
	r.recent = nil
	m.armBurstLocked(key, r, cfg, now)
	slog.Info("notification burst started: holding for one summary",
		slog.String("cluster", cluster), slog.Bool("resolved", resolved),
		slog.Int("sent_alone", len(r.items)-1), slog.Int("threshold", cfg.Threshold),
		slog.Duration("window", cfg.Window))
	return true
}

// armBurstLocked (re)schedules the flush: when the cluster has been quiet for
// Quiet, or MaxWait after the summary started, whichever comes first.
func (m *Manager) armBurstLocked(key burstKey, r *burstRun, cfg BurstConfig, now time.Time) {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.gen++
	gen := r.gen
	at := r.last.Add(cfg.Quiet)
	if max := r.started.Add(cfg.MaxWait); max.Before(at) {
		at = max
	}
	d := at.Sub(now)
	if d < 0 {
		d = 0
	}
	r.timer = m.afterFunc(d, func() { m.flushBurst(key, gen) })
}

func (m *Manager) flushBurst(key burstKey, gen uint64) {
	m.mu.Lock()
	r := m.bursts[key]
	if r == nil || r.gen != gen || !r.active {
		m.mu.Unlock()
		return
	}
	cfg := m.burst
	now := m.now()
	s := m.summaryLocked(key, r, cfg)
	if now.Before(r.last.Add(cfg.Quiet)) {
		// MaxWait reached while the cluster is still sending: send what we
		// have and keep it in burst mode, so the rest lands in the next summary
		// instead of reopening a run of single alerts.
		s.Partial = true
		r.items = nil
		r.started = now
		m.armBurstLocked(key, r, cfg, now)
	} else {
		delete(m.bursts, key)
	}
	notifiers := perInsightChannels(m.notifiers)
	m.mu.Unlock()
	logSummary(s, len(notifiers))
	m.deliverSummary(notifiers, s, false)
}

func logSummary(s Summary, channels int) {
	slog.Info("notification burst summary",
		slog.String("cluster", s.ClusterName), slog.Bool("resolved", s.Resolved),
		slog.Int("insights", len(s.Items)), slog.Bool("partial", s.Partial),
		slog.Int("channels", channels))
}

func (m *Manager) summaryLocked(key burstKey, r *burstRun, cfg BurstConfig) Summary {
	return Summary{
		ClusterName: key.cluster,
		BaseURL:     m.baseURL,
		Items:       r.items,
		First:       r.first,
		Last:        r.last,
		Resolved:    key.resolved,
		Config:      cfg,
	}
}

// flushAllBurstsLocked ends every burst now and returns their summaries. Used
// by Stop, so a settings change or a shutdown never drops held insights.
func (m *Manager) flushAllBurstsLocked() []Summary {
	var out []Summary
	for key, r := range m.bursts {
		if r.timer != nil {
			r.timer.Stop()
		}
		if r.active && len(r.items) > 0 {
			out = append(out, m.summaryLocked(key, r, m.burst))
		}
	}
	m.bursts = make(map[burstKey]*burstRun)
	return out
}

// deliverSummary sends a summary to each per-insight channel. A summary of one
// insight is sent as that insight. wait=true blocks until every channel has
// answered (Stop); otherwise delivery is fire-and-forget like Enqueue.
func (m *Manager) deliverSummary(notifiers []Notifier, s Summary, wait bool) {
	if len(s.Items) == 0 || len(notifiers) == 0 {
		return
	}
	done := make(chan struct{}, len(notifiers))
	for _, n := range notifiers {
		go func(n Notifier) {
			defer func() { done <- struct{}{} }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := sendSummaryVia(ctx, n, s); err != nil {
				log.Printf("Notification summary via %s failed: %v", n.Name(), err)
			}
		}(n)
	}
	if wait {
		for range notifiers {
			<-done
		}
	}
}

func sendSummaryVia(ctx context.Context, n Notifier, s Summary) error {
	if len(s.Items) == 1 {
		if s.Items[0].AlsoSentAlone {
			return nil
		}
		return n.Send(ctx, s.Items[0].Event)
	}
	if sn, ok := n.(SummaryNotifier); ok {
		return sn.SendSummary(ctx, s)
	}
	// A channel without a summary renderer gets what it would have had
	// before: each held insight on its own.
	for _, it := range s.Items {
		if it.AlsoSentAlone {
			continue
		}
		if err := n.Send(ctx, it.Event); err != nil {
			return err
		}
	}
	return nil
}

// deliver sends one event: right away to every channel, except the
// per-insight channels when the event is held for a burst summary.
func (m *Manager) deliver(notifiers []Notifier, e Event, held bool, what string) {
	for _, n := range notifiers {
		if held && !buffersEvents(n) {
			continue
		}
		go func(n Notifier) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := n.Send(ctx, e); err != nil {
				log.Printf("%s via %s failed: %v", what, n.Name(), err)
			}
		}(n)
	}
}
