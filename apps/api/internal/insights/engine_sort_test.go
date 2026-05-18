package insights

import (
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// TestGetInsights_Sorting pins the contract that GetInsights returns
// (severity rank ASC, FirstSeen DESC). Before this sort landed, the
// engine returned insights in FIFO of detection — and because the
// detection pass iterates Go maps (ClusterState.Pods etc.) whose
// order is non-deterministic, the same cluster with the same problems
// ranked differently between API restarts. A critical insight could
// be buried below a stale info, which broke triage.
func TestGetInsights_Sorting(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	e := &Engine{
		// Insert deliberately out-of-order: a stale info first (the
		// kind of thing that used to dominate the top of the list on
		// long-running APIs), then a fresh critical and a fresh
		// warning interleaved with another older critical and warning.
		insights: []models.Insight{
			{ID: "i-old", Severity: "info", FirstSeen: now.Add(-72 * time.Hour)},
			{ID: "c-new", Severity: "critical", FirstSeen: now.Add(-5 * time.Minute)},
			{ID: "w-old", Severity: "warning", FirstSeen: now.Add(-24 * time.Hour)},
			{ID: "c-old", Severity: "critical", FirstSeen: now.Add(-12 * time.Hour)},
			{ID: "w-new", Severity: "warning", FirstSeen: now.Add(-1 * time.Hour)},
			{ID: "i-new", Severity: "info", FirstSeen: now.Add(-10 * time.Minute)},
		},
	}

	got := e.GetInsights("", false)
	wantOrder := []string{"c-new", "c-old", "w-new", "w-old", "i-new", "i-old"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d insights, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("position %d: got %q, want %q", i, got[i].ID, want)
		}
	}
}

// TestGetInsights_UnknownSeverity verifies that an insight with a severity
// the rank table doesn't recognise sinks to the bottom rather than
// floating to the top accidentally (which would happen if the rank
// lookup defaulted to 0 instead of a high number).
func TestGetInsights_UnknownSeverity(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	e := &Engine{
		insights: []models.Insight{
			{ID: "unk", Severity: "garbage", FirstSeen: now},
			{ID: "info", Severity: "info", FirstSeen: now.Add(-1 * time.Hour)},
		},
	}
	got := e.GetInsights("", false)
	if got[0].ID != "info" || got[1].ID != "unk" {
		t.Errorf("unknown severity should sink: got %q, %q", got[0].ID, got[1].ID)
	}
}
