package insights

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The BoltDB twin of the Enterprise store's contract tests: the sink
// protocol must leave the same rows and the same append-only transitions
// behind, the watchdog must expire the stale and spare the fresh, and the
// mute overlay must narrate itself in the episode timeline.

func testBoltEpisodes(t *testing.T) (*BoltEpisodeStore, *BoltInsightStore) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "episodes.db"), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	names := [][]byte{[]byte("heads"), []byte("ep"), []byte("tr"), []byte("mu"), []byte("mk"), []byte("pr"), []byte("op")}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, n := range names {
			if _, err := tx.CreateBucketIfNotExists(n); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	heads := NewBoltInsightStore(db, []byte("heads"))
	store := NewBoltEpisodeStore(db, heads, BoltEpisodeBuckets{
		Episodes: []byte("ep"), Transitions: []byte("tr"), Mutes: []byte("mu"),
		MuteKeys: []byte("mk"), Presence: []byte("pr"), Operational: []byte("op"),
	})
	return store, heads
}

func epRec() *InsightRecord {
	return &InsightRecord{
		Fingerprint: "fp-1", TenantID: "org-1", ClusterID: "cl-1",
		RuleID: "crash-loop", Resource: "Pod/ns/x", Namespace: "ns",
		Title: "Crash", Severity: "warning",
	}
}

// TestBoltEpisodeStore_SinkSequence drives the full sink protocol and
// asserts the rows + append-only transitions it must leave behind.
func TestBoltEpisodeStore_SinkSequence(t *testing.T) {
	store, _ := testBoltEpisodes(t)
	ctx := context.Background()
	rec := epRec()
	t0 := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Second)

	store.EpisodeOpened(rec, "ep-1", t0, "")
	store.EpisodeTouched(rec, "ep-1", t0.Add(time.Minute))    // persists (first)
	store.EpisodeTouched(rec, "ep-1", t0.Add(61*time.Second)) // throttled (<1min after previous)
	store.EpisodeSeverityChanged(rec, "ep-1", "warning", "critical", t0.Add(2*time.Minute))
	store.EpisodeResolved(rec, "ep-1", t0.Add(3*time.Minute), ResolutionAutoRecovered)
	store.EpisodeFlapped(rec, "ep-1", t0.Add(4*time.Minute), 1)
	store.EpisodeResolved(rec, "ep-1", t0.Add(5*time.Minute), ResolutionAutoRecovered)

	ep, trs, err := store.Episode(ctx, "org-1", "ep-1")
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != EpisodeResolved || ep.ResolutionKind != ResolutionAutoRecovered {
		t.Fatalf("final state: %+v", ep)
	}
	if ep.MaxSeverity != "critical" {
		t.Fatalf("max_severity = %q, want critical (the escalation must stay)", ep.MaxSeverity)
	}
	if ep.FlapCount != 1 {
		t.Fatalf("flap_count = %d", ep.FlapCount)
	}
	if !ep.FirstSeen.Equal(t0) {
		t.Fatalf("first_seen moved by the flap: %v vs %v", ep.FirstSeen, t0)
	}
	// Transitions: open, sev, resolved, flap, resolved = 5 (touches are NOT history)
	if len(trs) != 5 {
		t.Fatalf("transitions = %d (%+v), want 5", len(trs), trs)
	}
	wantStates := [][2]string{{"", "firing"}, {"firing", "firing"}, {"firing", "resolved"}, {"resolved", "firing"}, {"firing", "resolved"}}
	for i, w := range wantStates {
		if trs[i].FromState != w[0] || trs[i].ToState != w[1] {
			t.Fatalf("transition %d = %s→%s, want %s→%s", i, trs[i].FromState, trs[i].ToState, w[0], w[1])
		}
	}
	// Reasons are the timeline copy (user-facing) — persisted in English.
	if trs[3].Reason != "re-fired within the reopen cooldown (flap 1)" {
		t.Fatalf("flap reason = %q", trs[3].Reason)
	}
	if trs[1].Reason != "severity warning → critical" {
		t.Fatalf("severity reason = %q", trs[1].Reason)
	}

	// Window (overlap) and ByFingerprint find it.
	eps, err := store.Window(ctx, "org-1", EpisodeQuery{Since: t0.Add(-time.Hour), Until: time.Now()})
	if err != nil || len(eps) != 1 {
		t.Fatalf("Window: %v / %+v", err, eps)
	}
	if eps, _ := store.Window(ctx, "org-1", EpisodeQuery{Since: t0.Add(-time.Hour), Until: time.Now(), Severity: "critical"}); len(eps) != 1 {
		t.Fatalf("Window(severity=critical) should match on MAX severity, got %d", len(eps))
	}
	byFp, err := store.ByFingerprint(ctx, "org-1", "fp-1", 5)
	if err != nil || len(byFp) != 1 {
		t.Fatalf("ByFingerprint: %v / %+v", err, byFp)
	}
	// IgnoredRate: closed without ack or action → 1/1 ignored.
	rates, err := store.IgnoredRate(ctx, "org-1", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r := rates["crash-loop"]; r[0] != 1 || r[1] != 1 {
		t.Fatalf("IgnoredRate = %+v", rates)
	}
	// The shift stats read the same transitions: one open, two auto-recoveries.
	st, err := store.WindowStats(ctx, "org-1", t0.Add(-time.Hour), time.Now())
	if err != nil || st.Opened != 1 || st.AutoRecovered != 2 || st.Criticals != 1 || st.StillFiring != 0 {
		t.Fatalf("WindowStats = %+v (%v)", st, err)
	}
	worst, err := store.WorstEpisode(ctx, "org-1", t0.Add(-time.Hour), time.Now())
	if err != nil || worst == nil || worst.ID != "ep-1" {
		t.Fatalf("WorstEpisode = %+v (%v)", worst, err)
	}
}

// TestBoltEpisodeStore_WatchdogExpires: a firing episode with no signal
// beyond the TTL flips to expired, its head row follows, and the watchdog's
// transition is recorded. A fresh one is left alone.
func TestBoltEpisodeStore_WatchdogExpires(t *testing.T) {
	store, heads := testBoltEpisodes(t)
	ctx := context.Background()
	rec := epRec()

	stale := time.Now().Add(-2 * time.Hour)
	store.EpisodeOpened(rec, "ep-stale", stale, "")
	fresh := time.Now().Add(-time.Minute)
	rec2 := epRec()
	rec2.Fingerprint = "fp-2"
	store.EpisodeOpened(rec2, "ep-fresh", fresh, "")

	// Active head for fp-1 (the one that must flip to expired).
	head := epRec()
	head.Status = "active"
	head.FirstSeen, head.LastSeen = stale, stale
	head.CurrentOccurrenceID = "ep-stale"
	if err := heads.Upsert(head); err != nil {
		t.Fatal(err)
	}

	n, err := store.ExpireStaleForOrg(ctx, "org-1", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired = %d, want 1 (only the stale one)", n)
	}
	ep, trs, _ := store.Episode(ctx, "org-1", "ep-stale")
	if ep.Status != EpisodeExpired {
		t.Fatalf("stale = %+v", ep)
	}
	if lastT := trs[len(trs)-1]; lastT.ToState != EpisodeExpired || lastT.Actor != "watchdog" ||
		lastT.Reason != "no data from the cluster beyond the TTL — the condition stopped being verifiable" {
		t.Fatalf("watchdog transition: %+v", lastT)
	}
	got, ok, err := heads.Get("org-1", "cl-1", "fp-1")
	if err != nil || !ok {
		t.Fatalf("head: %v / %v", ok, err)
	}
	if got.Status != EpisodeExpired || got.CurrentOccurrenceID != "" {
		t.Fatalf("head = %q / %q, want expired with no open occurrence", got.Status, got.CurrentOccurrenceID)
	}
	if epF, _, _ := store.Episode(ctx, "org-1", "ep-fresh"); epF.Status != EpisodeFiring {
		t.Fatalf("the fresh one must not be touched: %+v", epF)
	}
	// Expired history is prunable; firing history never is.
	if n, err := store.PruneOrg("org-1", time.Now()); err != nil || n != 1 {
		t.Fatalf("PruneOrg = %d (%v), want 1", n, err)
	}
	if _, _, err := store.Episode(ctx, "org-1", "ep-fresh"); err != nil {
		t.Fatalf("firing episode pruned: %v", err)
	}
}

// TestBoltEpisodeStore_Mutes: upsert on the key, active-only listing, the
// timeline narration, until-resolved consumption and the stats.
func TestBoltEpisodeStore_Mutes(t *testing.T) {
	store, _ := testBoltEpisodes(t)
	ctx := context.Background()
	rec := epRec()
	t0 := time.Now().Add(-10 * time.Minute).UTC()
	store.EpisodeOpened(rec, "ep-1", t0, "")

	m, err := store.CreateMute(ctx, "org-1", Mute{ClusterID: "cl-1", RuleID: "crash-loop", Resource: "Pod/ns/x", UntilResolved: true, CreatedBy: "ana"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.CreateMute(ctx, "org-1", Mute{ClusterID: "cl-1", RuleID: "crash-loop", Resource: "Pod/ns/x", Reason: "known flapper", CreatedBy: "ana"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != m.ID {
		t.Fatalf("re-muting the same key must update, not stack: %s vs %s", again.ID, m.ID)
	}
	list, _ := store.ListMutes(ctx, "org-1", "cl-1")
	if len(list) != 1 || list[0].Reason != "known flapper" || list[0].UntilResolved {
		t.Fatalf("ListMutes = %+v", list)
	}
	if other, _ := store.ListMutes(ctx, "org-1", "cl-other"); len(other) != 0 {
		t.Fatalf("cluster filter ignored: %+v", other)
	}
	_, trs, _ := store.Episode(ctx, "org-1", "ep-1")
	if last := trs[len(trs)-1]; last.ToState != "muted" || last.Actor != "user:ana" {
		t.Fatalf("mute must narrate itself in the episode timeline: %+v", last)
	}
	if err := store.DeleteMute(ctx, "org-1", m.ID, "ana"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMute(ctx, "org-1", m.ID, "ana"); err != ErrMuteNotFound {
		t.Fatalf("second delete = %v, want ErrMuteNotFound", err)
	}
	// Until-resolved is consumed by the resolution of its key.
	if _, err := store.CreateMute(ctx, "org-1", Mute{ClusterID: "cl-1", RuleID: "crash-loop", Resource: "Pod/ns/x", UntilResolved: true}); err != nil {
		t.Fatal(err)
	}
	store.EpisodeResolved(rec, "ep-1", time.Now(), ResolutionAutoRecovered)
	if list, _ := store.ListMutes(ctx, "org-1", ""); len(list) != 0 {
		t.Fatalf("until-resolved mute must be consumed on resolution: %+v", list)
	}
	ms, _ := store.MuteStats(ctx, "org-1", t0)
	if ms.ActiveNow != 0 {
		t.Fatalf("MuteStats = %+v", ms)
	}
}

// TestBoltEpisodeStore_PresenceAndBursts: the presence anchor round-trips and
// the operational clusterer persists convergent ids.
func TestBoltEpisodeStore_PresenceAndBursts(t *testing.T) {
	store, _ := testBoltEpisodes(t)
	ctx := context.Background()

	if _, has, err := store.DashboardLastSeen(ctx, "org-1", "u1"); err != nil || has {
		t.Fatalf("first shift: has=%v err=%v", has, err)
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := store.MarkDashboardRendered(ctx, "org-1", "u1", at); err != nil {
		t.Fatal(err)
	}
	if seen, has, _ := store.DashboardLastSeen(ctx, "org-1", "u1"); !has || !seen.Equal(at) {
		t.Fatalf("presence = %v/%v", seen, has)
	}

	prevMin := MinBurst
	MinBurst = 3
	t.Cleanup(func() { MinBurst = prevMin })
	base := time.Now().Add(-30 * time.Minute).UTC()
	for i := 0; i < 3; i++ {
		rec := epRec()
		rec.Fingerprint = "fp-node-" + string(rune('a'+i))
		rec.RuleID = "node-not-ready"
		rec.Resource = "Node/n" + string(rune('a'+i))
		store.EpisodeOpened(rec, "ep-node-"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Minute), "")
	}
	ops, err := store.ClusterAndStore(ctx, "org-1", base.Add(-time.Hour), time.Now())
	if err != nil || len(ops) != 1 || ops[0].Kind != OpKindNodeRotation {
		t.Fatalf("ClusterAndStore = %+v (%v)", ops, err)
	}
	again, _ := store.ClusterAndStore(ctx, "org-1", base.Add(-time.Hour), time.Now())
	if len(again) != 1 || again[0].ID != ops[0].ID {
		t.Fatalf("recomputing the window must converge on the same id: %+v vs %+v", again, ops)
	}
}
