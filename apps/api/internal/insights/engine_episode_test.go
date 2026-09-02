package insights

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// recordingSink captures the engine's episode events in order.
type recordingSink struct {
	events []string // "opened:<id>:<prev>", "flapped:<id>:<n>", "touched:<id>", "sev:<from>-><to>", "resolved:<id>:<kind>"
	// openedRecs snapshots the record CONTENT at each Opened — the sink
	// persists rec verbatim, so what arrives here is what the episode row
	// gets (in-vivo find 31-ago: brand-new identities arrived with empty
	// severity/title).
	openedRecs []InsightRecord
}

func (r *recordingSink) EpisodeOpened(rec *InsightRecord, id string, _ time.Time, prev string) {
	r.events = append(r.events, "opened:"+id+":"+prev)
	r.openedRecs = append(r.openedRecs, *rec)
}
func (r *recordingSink) EpisodeFlapped(_ *InsightRecord, id string, _ time.Time, n int) {
	r.events = append(r.events, "flapped:"+id+":"+itoaTest(n))
}
func (r *recordingSink) EpisodeTouched(_ *InsightRecord, id string, _ time.Time) {
	r.events = append(r.events, "touched:"+id)
}
func (r *recordingSink) EpisodeSeverityChanged(_ *InsightRecord, id, from, to string, _ time.Time) {
	r.events = append(r.events, "sev:"+from+"->"+to)
}
func (r *recordingSink) EpisodeResolved(_ *InsightRecord, id string, _ time.Time, kind string) {
	r.events = append(r.events, "resolved:"+id+":"+kind)
}

func itoaTest(n int) string { return string(rune('0' + n)) }

func last(events []string) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1]
}

func hasPrefixEvent(events []string, prefix string) bool {
	for _, e := range events {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// fakeRule emits one insight while `firing` is true.
func testEngineWithFakeRule(t *testing.T, firing *bool, severity *string) (*Engine, *recordingSink, *int) {
	t.Helper()
	sink := &recordingSink{}
	SetEpisodeSink(sink)
	t.Cleanup(func() { SetEpisodeSink(nil); SetPolicySource(nil) })

	notified := 0
	e := NewEngine(websocket.NewHub(), NewMemoryInsightStore(), "cl-1", "org-1")
	e.SetOnNewInsight(func(models.Insight) { notified++ })
	e.rules = []Rule{{ID: "fake-rule", Name: "Fake", Severity: "warning",
		Evaluate: func(*ClusterState) []models.Insight {
			if !*firing {
				return nil
			}
			return []models.Insight{{Severity: *severity, Resource: "ns/x", Title: "t", Message: "m"}}
		}}}
	return e, sink, &notified
}

// TestEpisodeLifecycle_FlapCooldownAndReopen pins A1 end to end at the
// engine level: resolve→re-fire inside the cooldown is the SAME episode
// (flapped, NO new notification); outside the cooldown a NEW episode opens
// (and notifies).
func TestEpisodeLifecycle_FlapCooldownAndReopen(t *testing.T) {
	firing := true
	sev := "warning"
	e, sink, notified := testEngineWithFakeRule(t, &firing, &sev)
	state := &ClusterState{Pods: []*corev1.Pod{}}

	e.Evaluate(state) // opens
	if !hasPrefixEvent(sink.events, "opened:") || *notified != 1 {
		t.Fatalf("open: events=%v notified=%d", sink.events, *notified)
	}
	firstID := e.GetAllInsights()[0].ID

	firing = false
	e.Evaluate(state) // resolves
	if last(sink.events) != "resolved:"+firstID+":auto_recovered" {
		t.Fatalf("resolve: %v", sink.events)
	}

	// Re-fire INSIDE the cooldown → flap, same episode, no new notification.
	firing = true
	e.Evaluate(state)
	if last(sink.events) != "flapped:"+firstID+":1" {
		t.Fatalf("flap: %v", sink.events)
	}
	if *notified != 1 {
		t.Fatalf("a flap must not re-notify; notified=%d", *notified)
	}
	if got := e.GetAllInsights()[0].ID; got != firstID {
		t.Fatalf("flap reused id: got %s want %s", got, firstID)
	}

	// Resolve again, then re-fire OUTSIDE the cooldown → new episode + notify.
	firing = false
	e.Evaluate(state)
	prev := ReopenCooldown
	ReopenCooldown = 0 // everything is now "outside the window"
	defer func() { ReopenCooldown = prev }()
	firing = true
	e.Evaluate(state)
	newID := e.GetAllInsights()[0].ID
	if newID == firstID {
		t.Fatal("outside the cooldown a NEW episode id must open")
	}
	if last(sink.events) != "opened:"+newID+":" {
		t.Fatalf("reopen: %v", sink.events)
	}
	if *notified != 2 {
		t.Fatalf("a genuine reopen must notify; notified=%d", *notified)
	}
}

// TestEpisodeLifecycle_RuleChangedKind: an active cleared because its rule
// was policy-switched off resolves as rule_changed, never auto_recovered —
// the narrative must not claim a recovery nobody observed.
func TestEpisodeLifecycle_RuleChangedKind(t *testing.T) {
	firing := true
	sev := "warning"
	e, sink, _ := testEngineWithFakeRule(t, &firing, &sev)
	state := &ClusterState{}

	e.Evaluate(state)
	id := e.GetAllInsights()[0].ID
	SetPolicySource(func(string, string) PolicySnapshot {
		return PolicySnapshot{Severities: map[string]string{"fake-rule": SeverityOff}}
	})
	e.Evaluate(state)
	if last(sink.events) != "resolved:"+id+":rule_changed" {
		t.Fatalf("off-resolution kind: %v", sink.events)
	}
}

// TestEpisodeLifecycle_SeverityChangeAndExpiredLink: an escalation while
// firing emits the severity event (what later pierces mutes), and a re-fire
// over an `expired` head opens a NEW episode LINKED to the expired one (A2).
func TestEpisodeLifecycle_SeverityChangeAndExpiredLink(t *testing.T) {
	firing := true
	sev := "warning"
	e, sink, notified := testEngineWithFakeRule(t, &firing, &sev)
	state := &ClusterState{}

	e.Evaluate(state)
	id := e.GetAllInsights()[0].ID
	sev = "critical"
	e.Evaluate(state)
	if !hasPrefixEvent(sink.events, "sev:warning->critical") {
		t.Fatalf("escalation event missing: %v", sink.events)
	}

	// Simulate the watchdog: head goes expired while the engine forgets it
	// (a dead runtime that later reconnects starts with fresh memory).
	fp := e.GetAllInsights()[0].Fingerprint
	rec, _, _ := e.store.Get("org-1", "cl-1", fp)
	rec.Status = EpisodeExpired
	if err := e.store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.insights = nil // fresh memory after "reconnect"
	e.mu.Unlock()

	before := *notified
	e.Evaluate(state)
	ev := last(sink.events)
	want := "opened:" + e.GetAllInsights()[0].ID + ":" + id
	if ev != want {
		t.Fatalf("A2 link: got %q want %q (events %v)", ev, want, sink.events)
	}
	if *notified != before+1 {
		t.Fatal("a reopen after an observation gap must notify")
	}
}

// TestEpisodeOpened_BrandNewIdentityCarriesContent — the in-vivo 31-ago bug:
// a fingerprint firing for the FIRST time ever reached the sink before
// admitNew stamped content onto the record, so its episode row was born with
// empty severity/title (blank MAX SEVERITY chip in the detail). Known
// identities dodged it by inheriting the head's previous stamp.
func TestEpisodeOpened_BrandNewIdentityCarriesContent(t *testing.T) {
	firing := true
	sev := "critical"
	e, sink, _ := testEngineWithFakeRule(t, &firing, &sev)
	e.Evaluate(&ClusterState{})

	if len(sink.openedRecs) != 1 {
		t.Fatalf("openedRecs = %d, want 1", len(sink.openedRecs))
	}
	rec := sink.openedRecs[0]
	if rec.Severity != "critical" || rec.Title == "" {
		t.Fatalf("Opened carried severity=%q title=%q — the episode row is born from these", rec.Severity, rec.Title)
	}
}

// TestHiddenByProfile — Pantalla 4's contract: a rule switched off by policy
// still RUNS, but only to count. Nothing it finds becomes an insight; the
// count is what keeps the silence visible.
func TestHiddenByProfile(t *testing.T) {
	firing := true
	sev := "warning"
	e, _, notified := testEngineWithFakeRule(t, &firing, &sev)
	state := &ClusterState{}

	e.Evaluate(state)
	if len(e.GetAllInsights()) != 1 {
		t.Fatal("baseline: the rule should produce while on")
	}

	SetPolicySource(func(string, string) PolicySnapshot {
		return PolicySnapshot{Severities: map[string]string{"fake-rule": SeverityOff}}
	})
	before := *notified
	e.Evaluate(state)

	if got := e.HiddenByProfile(); got["fake-rule"] != 1 {
		t.Fatalf("hiddenByProfile = %v, want fake-rule:1 — the silence must stay countable", got)
	}
	if n := len(e.GetAllInsights()); n != 0 {
		t.Fatalf("off rule still produced %d insights — counting must not emit", n)
	}
	if *notified != before {
		t.Fatal("counting an off rule must not notify")
	}

	// Back on: the counter clears with the next evaluation.
	SetPolicySource(nil)
	e.Evaluate(state)
	if got := e.HiddenByProfile(); len(got) != 0 {
		t.Fatalf("hiddenByProfile after re-enable = %v, want empty", got)
	}
}
