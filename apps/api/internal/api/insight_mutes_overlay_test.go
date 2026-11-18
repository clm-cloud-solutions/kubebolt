package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/websocket"
)

// listOnlyMutes is a MuteStore that only lists: what the org-wide overlay
// reads. An empty clusterID means every cluster, as the stores answer it.
type listOnlyMutes struct{ mutes []insights.Mute }

func (s listOnlyMutes) CreateMute(context.Context, string, insights.Mute) (insights.Mute, error) {
	return insights.Mute{}, nil
}
func (s listOnlyMutes) DeleteMute(context.Context, string, string, string) error { return nil }
func (s listOnlyMutes) ListMutes(_ context.Context, _ string, clusterID string) ([]insights.Mute, error) {
	var out []insights.Mute
	for _, m := range s.mutes {
		if clusterID == "" || m.ClusterID == clusterID {
			out = append(out, m)
		}
	}
	return out, nil
}

// One mute on c1: crash-loop on Pod/ns/x, and the critical zero-replicas.
var overlayMutes = listOnlyMutes{mutes: []insights.Mute{
	{ClusterID: "c1", RuleID: "crash-loop", Resource: "Pod/ns/x"},
	{ClusterID: "c1", RuleID: "zero-replicas", Resource: "Deploy/ns/y"},
}}

// Finding #80: Fleet and Home read every cluster's health from
// /insights/summary, which never applied the mute overlay — a silenced insight
// kept painting its cluster amber there while the Overview said healthy.
func TestInsightsSummary_LeavesOutSilencedInsights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	empty := "apiVersion: v1\nkind: Config\nclusters: []\ncontexts: []\nusers: []\ncurrent-context: \"\"\n"
	if err := os.WriteFile(path, []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := cluster.NewManager(path, websocket.NewHub(), time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)

	store := insights.NewMemoryInsightStore()
	now := time.Now().UTC()
	for _, rec := range []insights.InsightRecord{
		{Fingerprint: "a", ClusterID: "c1", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning"},        // muted
		{Fingerprint: "b", ClusterID: "c1", RuleID: "oom-killed", Resource: "Pod/ns/x", Severity: "warning"},        // other rule
		{Fingerprint: "c", ClusterID: "c2", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning"},        // other cluster
		{Fingerprint: "d", ClusterID: "c1", RuleID: "zero-replicas", Resource: "Deploy/ns/y", Severity: "critical"}, // pierces
	} {
		rec := rec
		rec.Status, rec.FirstSeen, rec.LastSeen = "active", now, now
		if err := store.Upsert(&rec); err != nil {
			t.Fatal(err)
		}
	}
	m.SetInsightStore(store, "")

	read := func(t *testing.T, h *handlers) insightSummaryResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		h.handleInsightsSummary(rec, httptest.NewRequest(http.MethodGet, "/api/v1/insights/summary", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got insightSummaryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := read(t, &handlers{manager: m, mutes: overlayMutes})
	if n := got.BySeverityCluster["c1"]["warning"]; n != 1 {
		t.Errorf("c1 warnings = %d, want 1 (the muted one left out, the other rule kept)", n)
	}
	if n := got.BySeverityCluster["c1"]["critical"]; n != 1 {
		t.Errorf("c1 criticals = %d, want 1 — a critical pierces the silence", n)
	}
	if n := got.BySeverityCluster["c2"]["warning"]; n != 1 {
		t.Errorf("c2 warnings = %d, want 1 — a mute on c1 says nothing about c2", n)
	}
	if got.BySeverity["warning"] != 2 || got.BySeverity["critical"] != 1 {
		t.Errorf("bySeverity = %v, want warning 2, critical 1", got.BySeverity)
	}

	// No mute store (an install without the lifecycle store): everything counts.
	all := read(t, &handlers{manager: m})
	if all.BySeverity["warning"] != 3 || all.BySeverity["critical"] != 1 {
		t.Errorf("without mutes bySeverity = %v, want warning 3, critical 1", all.BySeverity)
	}
}

// The episode list marks what the overlay hides, so Home's «While you were
// away» can rank a silenced episode last and say so instead of «Review →».
func TestMarkSilencedEpisodes(t *testing.T) {
	eps := []insights.Episode{
		{ID: "muted", ClusterID: "c1", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning", MaxSeverity: "warning"},
		{ID: "peaked-critical", ClusterID: "c1", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning", MaxSeverity: "critical"},
		{ID: "critical", ClusterID: "c1", RuleID: "zero-replicas", Resource: "Deploy/ns/y", Severity: "critical", MaxSeverity: "critical"},
		{ID: "other-cluster", ClusterID: "c2", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning", MaxSeverity: "warning"},
		{ID: "other-rule", ClusterID: "c1", RuleID: "oom-killed", Resource: "Pod/ns/x", Severity: "warning", MaxSeverity: "warning"},
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/insights/episodes", nil)
	(&handlers{mutes: overlayMutes}).markSilencedEpisodes(r, eps)

	want := map[string]bool{"muted": true}
	for _, ep := range eps {
		if ep.Muted != want[ep.ID] {
			t.Errorf("%s: muted = %v, want %v", ep.ID, ep.Muted, want[ep.ID])
		}
	}

	// No mute store: nothing is marked.
	fresh := []insights.Episode{{ID: "x", ClusterID: "c1", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning"}}
	(&handlers{}).markSilencedEpisodes(r, fresh)
	if fresh[0].Muted {
		t.Error("marked muted with no mute store")
	}
}

type noPresence struct{}

func (noPresence) MarkDashboardRendered(context.Context, string, string, time.Time) error { return nil }
func (noPresence) DashboardLastSeen(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

type worstOnly struct{ worst *insights.ShiftWorstEpisode }

func (worstOnly) WindowStats(context.Context, string, time.Time, time.Time) (insights.ShiftEpisodeStats, error) {
	return insights.ShiftEpisodeStats{}, nil
}
func (s worstOnly) WorstEpisode(context.Context, string, time.Time, time.Time) (*insights.ShiftWorstEpisode, error) {
	if s.worst == nil {
		return nil, nil
	}
	cp := *s.worst
	return &cp, nil
}
func (worstOnly) MuteStats(context.Context, string, time.Time) (insights.ShiftMuteStats, error) {
	return insights.ShiftMuteStats{}, nil
}

// The report's lead sentence («The longest, X, has been down …») names the
// window's worst episode. When a mute silences it, the sentence goes: it would
// lead the report with what the person asked not to hear about (finding #80).
func TestShiftReport_WorstEpisodeLeavesOutASilencedOne(t *testing.T) {
	eps := episodesByID{
		"silenced": {ID: "silenced", ClusterID: "c1", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning", MaxSeverity: "warning"},
		"loud":     {ID: "loud", ClusterID: "c2", RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning", MaxSeverity: "warning"},
	}
	worstOf := func(t *testing.T, id string) *insights.ShiftWorstEpisode {
		t.Helper()
		h := &handlers{
			presence:   noPresence{},
			shiftStats: worstOnly{worst: &insights.ShiftWorstEpisode{ID: id, Resource: "Pod/ns/x", Status: "firing", Seconds: 600}},
			episodes:   eps,
			mutes:      overlayMutes,
		}
		rec := httptest.NewRecorder()
		h.handleShiftReport(rec, httptest.NewRequest(http.MethodGet, "/api/v1/insights/shift-report", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var got shiftReportResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Worst
	}
	if w := worstOf(t, "silenced"); w != nil {
		t.Errorf("worst = %+v, want none — a mute silences it", w)
	}
	if w := worstOf(t, "loud"); w == nil || w.ID != "loud" {
		t.Errorf("worst = %+v, want the unsilenced episode", w)
	}
}
