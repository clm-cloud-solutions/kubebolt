package notifications

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// --- fakes ---------------------------------------------------------------

// recNotifier records what it is sent. mode "" is a per-insight channel
// (Slack, Discord, instant email); "hourly" behaves like the email digest.
type recNotifier struct {
	name string
	mode string

	mu        sync.Mutex
	sent      []Event
	summaries []Summary
}

func (r *recNotifier) Name() string       { return r.name }
func (r *recNotifier) DigestMode() string { return r.mode }
func (r *recNotifier) Send(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, e)
	return nil
}
func (r *recNotifier) SendSummary(_ context.Context, s Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summaries = append(r.summaries, s)
	return nil
}
func (r *recNotifier) counts() (sent, summaries int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent), len(r.summaries)
}
func (r *recNotifier) summary(i int) Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summaries[i]
}

// plainOnly is a channel with no summary renderer.
type plainOnly struct {
	mu   sync.Mutex
	sent []Event
}

func (p *plainOnly) Name() string { return "plain" }
func (p *plainOnly) Send(_ context.Context, e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, e)
	return nil
}
func (p *plainOnly) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

// fakeClock drives the Manager's clock and timers by hand.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool {
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) stopper {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock and fires every due timer, in time order. A fired
// callback may arm new timers; those fire too if they fall inside the step.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var due []*fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.at.After(target) {
				due = append(due, t)
			}
		}
		if len(due) == 0 {
			c.now = target
			c.mu.Unlock()
			return
		}
		sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
		next := due[0]
		next.fired = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
	}
}

func newTestManager(t *testing.T, notifiers []Notifier, burst BurstConfig) (*Manager, *fakeClock) {
	t.Helper()
	m := NewManager(notifiers, Config{MasterEnabled: true, MinSeverity: "info", Cooldown: time.Hour, Burst: burst})
	clk := &fakeClock{now: time.Date(2026, 11, 17, 23, 41, 5, 0, time.UTC)}
	m.now = clk.Now
	m.afterFunc = clk.AfterFunc
	return m, clk
}

func insightN(i int, sev string) models.Insight {
	return models.Insight{
		Severity:    sev,
		Title:       "Service Has Zero Ready Endpoints",
		Resource:    fmt.Sprintf("Service/ns-%d/svc-%d", i%7, i),
		Namespace:   fmt.Sprintf("ns-%d", i%7),
		Fingerprint: fmt.Sprintf("fp-%d", i),
	}
}

// eventually polls cond (deliveries run on goroutines).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// settle gives in-flight delivery goroutines a moment, for "nothing more
// arrived" assertions.
func settle() { time.Sleep(30 * time.Millisecond) }

// --- behaviour -------------------------------------------------------------

func TestBurst_BelowTheThresholdEveryInsightGoesOutAtOnce(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m, clk := newTestManager(t, []Notifier{slack}, DefaultBurstConfig())
	for i := 0; i < 4; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
		clk.Advance(time.Second)
	}
	eventually(t, "4 sends", func() bool { s, _ := slack.counts(); return s == 4 })
	clk.Advance(10 * time.Minute)
	settle()
	if s, sum := slack.counts(); s != 4 || sum != 0 {
		t.Fatalf("sent=%d summaries=%d, want 4 and 0", s, sum)
	}
}

func TestBurst_TheFifthWithinAMinuteStartsABurstAndOneSummaryCoversAll(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m, clk := newTestManager(t, []Notifier{slack}, DefaultBurstConfig())
	m.Enqueue("acme-dev", insightN(0, "critical"))
	for i := 1; i < 598; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	eventually(t, "the first 4 sent alone", func() bool { s, _ := slack.counts(); return s == 4 })

	clk.Advance(29 * time.Second) // not quiet long enough yet
	settle()
	if _, sum := slack.counts(); sum != 0 {
		t.Fatalf("summary sent before the cluster was quiet for 30s")
	}
	clk.Advance(time.Second)
	eventually(t, "one summary", func() bool { _, sum := slack.counts(); return sum == 1 })

	s := slack.summary(0)
	if len(s.Items) != 598 || s.ClusterName != "acme-dev" || s.Partial || s.Resolved {
		t.Fatalf("summary = %d items, cluster %q, partial %v, resolved %v; want 598, acme-dev, false, false",
			len(s.Items), s.ClusterName, s.Partial, s.Resolved)
	}
	for i, it := range s.Items {
		if want := i < 4; it.AlsoSentAlone != want {
			t.Fatalf("item %d AlsoSentAlone = %v, want %v", i, it.AlsoSentAlone, want)
		}
	}
	if sent, _ := slack.counts(); sent != 4 {
		t.Fatalf("sent alone = %d, want 4 (the rest only in the summary)", sent)
	}
}

func TestBurst_InsightsOutsideTheWindowDoNotAddUp(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m, clk := newTestManager(t, []Notifier{slack}, DefaultBurstConfig())
	for i := 0; i < 4; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	clk.Advance(61 * time.Second)
	for i := 4; i < 8; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	eventually(t, "8 sends", func() bool { s, _ := slack.counts(); return s == 8 })
	clk.Advance(10 * time.Minute)
	settle()
	if _, sum := slack.counts(); sum != 0 {
		t.Fatalf("a summary was sent for 4 + 4 insights a minute apart")
	}
}

func TestBurst_IsCountedPerCluster(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m, _ := newTestManager(t, []Notifier{slack}, DefaultBurstConfig())
	for i := 0; i < 20; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	m.Enqueue("acme-prod", insightN(100, "critical"))
	eventually(t, "4 from acme-dev + 1 from acme-prod", func() bool { s, _ := slack.counts(); return s == 5 })
	slack.mu.Lock()
	defer slack.mu.Unlock()
	for _, e := range slack.sent {
		if e.ClusterName == "acme-prod" {
			return
		}
	}
	t.Fatal("acme-prod's insight was held by acme-dev's burst")
}

func TestBurst_AClusterThatNeverSettlesGetsAPartialSummaryEveryMaxWait(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m, clk := newTestManager(t, []Notifier{slack}, DefaultBurstConfig())
	i := 0
	for ; i < 5; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	// One more every 20 s (never quiet for 30 s) for 6 minutes.
	for step := 0; step < 18; step++ {
		clk.Advance(20 * time.Second)
		m.Enqueue("acme-dev", insightN(i, "warning"))
		i++
	}
	eventually(t, "a partial summary at 5 min", func() bool { _, sum := slack.counts(); return sum == 1 })
	first := slack.summary(0)
	if !first.Partial {
		t.Fatal("the summary at MaxWait is not marked partial")
	}
	// Still in burst mode: the next insights do not go out alone.
	if sent, _ := slack.counts(); sent != 4 {
		t.Fatalf("sent alone = %d, want 4 (the burst continued, nothing reopened)", sent)
	}
	clk.Advance(30 * time.Second) // now it settles
	eventually(t, "the closing summary", func() bool { _, sum := slack.counts(); return sum == 2 })
	second := slack.summary(1)
	if second.Partial {
		t.Fatal("the closing summary is marked partial")
	}
	if got := len(first.Items) + len(second.Items); got != i {
		t.Fatalf("the two summaries cover %d insights, want %d", got, i)
	}
	if second.First.IsZero() || second.First.Before(first.Last) {
		t.Fatalf("second summary starts at %v, before the first ended at %v", second.First, first.Last)
	}

	// Settled: the next insight is a lone one again.
	clk.Advance(2 * time.Minute)
	m.Enqueue("acme-dev", insightN(999, "critical"))
	eventually(t, "a lone insight after the burst", func() bool { s, _ := slack.counts(); return s == 5 })
}

func TestBurst_TheDigestGetsEveryEventAndNoSummary(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	digest := &recNotifier{name: "email", mode: "hourly"}
	m, clk := newTestManager(t, []Notifier{slack, digest}, DefaultBurstConfig())
	for i := 0; i < 50; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	clk.Advance(time.Minute)
	eventually(t, "digest got all 50", func() bool { s, _ := digest.counts(); return s == 50 })
	eventually(t, "slack summary", func() bool { _, sum := slack.counts(); return sum == 1 })
	if _, sum := digest.counts(); sum != 0 {
		t.Fatal("the digest received a burst summary")
	}
}

func TestBurst_StopSendsTheHeldSummaryBeforeReturning(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m, _ := newTestManager(t, []Notifier{slack}, DefaultBurstConfig())
	for i := 0; i < 30; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	m.Stop()
	if _, sum := slack.counts(); sum != 1 {
		t.Fatalf("summaries after Stop = %d, want 1 (held insights must not be lost on a settings change)", sum)
	}
	if got := len(slack.summary(0).Items); got != 30 {
		t.Fatalf("summary covers %d insights, want 30", got)
	}
}

func TestBurst_ResolutionsAreABurstOfTheirOwn(t *testing.T) {
	slack := &recNotifier{name: "slack"}
	m := NewManager([]Notifier{slack}, Config{MasterEnabled: true, MinSeverity: "info", Cooldown: time.Hour, IncludeResolved: true})
	clk := &fakeClock{now: time.Date(2026, 11, 17, 23, 41, 5, 0, time.UTC)}
	m.now, m.afterFunc = clk.Now, clk.AfterFunc
	for i := 0; i < 3; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	for i := 10; i < 40; i++ {
		m.EnqueueResolved("acme-dev", insightN(i, "warning"))
	}
	clk.Advance(time.Minute)
	eventually(t, "resolved summary", func() bool { _, sum := slack.counts(); return sum == 1 })
	s := slack.summary(0)
	if !s.Resolved || len(s.Items) != 30 {
		t.Fatalf("summary resolved=%v items=%d, want true and 30", s.Resolved, len(s.Items))
	}
	if sent, _ := slack.counts(); sent != 3+4 {
		t.Fatalf("sent alone = %d, want 7 (3 new under their own count + the first 4 resolutions)", sent)
	}
}

func TestBurst_AChannelWithoutSummariesGetsTheHeldInsightsOneByOne(t *testing.T) {
	plain := &plainOnly{}
	m, clk := newTestManager(t, []Notifier{plain}, DefaultBurstConfig())
	for i := 0; i < 12; i++ {
		m.Enqueue("acme-dev", insightN(i, "warning"))
	}
	clk.Advance(time.Minute)
	eventually(t, "12 sends, none twice", func() bool { return plain.count() == 12 })
	settle()
	if n := plain.count(); n != 12 {
		t.Fatalf("sends = %d, want 12 (the first 4 must not be sent again)", n)
	}
}

func TestBurstConfig_FromConfig(t *testing.T) {
	if got := burstFrom(BurstConfig{}); got != DefaultBurstConfig() {
		t.Fatalf("zero config = %+v, want the defaults", got)
	}
	// There is no "off": what used to mean it resolves to the default.
	for _, th := range []int{-1, 0, 1} {
		if got := burstFrom(BurstConfig{Threshold: th}); got.Threshold != DefaultBurstThreshold {
			t.Fatalf("Threshold %d resolved to %d, want the default %d", th, got.Threshold, DefaultBurstThreshold)
		}
	}
	got := burstFrom(BurstConfig{Threshold: 8, Quiet: 10 * time.Minute, MaxWait: time.Minute})
	if got.Quiet != time.Minute || got.Window != DefaultBurstWindow {
		t.Fatalf("normalized = %+v, want Quiet capped at MaxWait and Window defaulted", got)
	}
}
