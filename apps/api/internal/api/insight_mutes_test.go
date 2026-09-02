package api

import (
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
	"github.com/kubebolt/kubebolt/apps/api/internal/models"
)

// applyMuteFilter is what keeps EVERY insight surface honest about mutes
// (in-vivo find 31-ago: the Overview KPI kept counting 5 silenced insights).
func TestApplyMuteFilter(t *testing.T) {
	items := []models.Insight{
		{RuleID: "crash-loop", Resource: "Pod/ns/x", Severity: "warning"},
		{RuleID: "crash-loop", Resource: "Pod/ns/other", Severity: "warning"},
		{RuleID: "oom-killed", Resource: "Pod/ns/x", Severity: "warning"},
		{RuleID: "zero-replicas", Resource: "Deploy/ns/y", Severity: "critical"},
	}
	mutes := []insights.Mute{
		{RuleID: "crash-loop", Resource: "Pod/ns/x"},
		{RuleID: "zero-replicas", Resource: "Deploy/ns/y"},
	}

	got := applyMuteFilter(items, mutes)
	if len(got) != 3 {
		t.Fatalf("filtered = %d items (%+v), want 3", len(got), got)
	}
	// The muted warning is gone; different resource and different rule stay.
	for _, it := range got {
		if it.RuleID == "crash-loop" && it.Resource == "Pod/ns/x" {
			t.Fatal("the muted insight survived the filter")
		}
	}
	// The muted CRITICAL pierces (#54 §5) — silencing must never swallow
	// an escalation.
	found := false
	for _, it := range got {
		if it.RuleID == "zero-replicas" {
			found = true
		}
	}
	if !found {
		t.Fatal("the muted critical was hidden — escalation must pierce")
	}

	// No mutes = untouched slice.
	if n := len(applyMuteFilter(items, nil)); n != 4 {
		t.Fatalf("no-mutes pass = %d, want 4", n)
	}
}
